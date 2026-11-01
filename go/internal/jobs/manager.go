package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type startOptions struct {
	Workspace     wire.Workspace
	Scope         wire.Scope
	CwdAbs        string
	Cwd           string
	Command       string
	Origin        string
	TimeoutMS     int64
	OutputLimit   int64
	BashSessionID string
	InputJobIDs   []string
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
func uniqueExact(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
func commandKey(command, cwd string, inputs []string) string {
	ids := uniqueExact(inputs)
	sort.Strings(ids)
	b, _ := json.Marshal([]any{command, cwd, ids})
	return string(b)
}
func commandLabel(command string) string {
	first := strings.Split(strings.TrimSpace(command), "\n")[0]
	label := strings.Join(strings.Fields(first), " ")
	runes := []rune(label)
	if len(runes) > 80 {
		return string(runes[:77]) + "…"
	}
	return label
}

func (s *Service) capacity(workspace string) int {
	all, local := 0, 0
	for _, r := range s.Snapshot() {
		if r.Status == "running" && r.Origin != "foreground" {
			all++
			if r.WorkspaceID == workspace {
				local++
			}
		}
	}
	return max(0, min(s.cfg.Int("maxJobs", 12)-all, s.cfg.Int("maxJobsPerWorkspace", 6)-local))
}

func (s *Service) paths(w wire.Workspace) []process.PathRedaction {
	if s.cfg.Bool("exposeAbsolutePaths", false) {
		return nil
	}
	paths := []process.PathRedaction{{w.Root, "[workspace:" + w.ID + "]"}}
	for _, p := range s.resolver.Projects() {
		paths = append(paths, process.PathRedaction{p.Root, "[project:" + p.ID + "]"})
	}
	for _, root := range s.cfg.AllowedRoots {
		paths = append(paths, process.PathRedaction{root, "[allowed-root]"})
	}
	home, _ := os.UserHomeDir()
	paths = append(paths, process.PathRedaction{s.cfg.String("worktreeRoot", filepath.Join(s.cfg.Home, "worktrees")), "[worktree-storage]"}, process.PathRedaction{s.cfg.String("codexDir", filepath.Join(home, ".codex")), "[codex-data]"}, process.PathRedaction{s.cfg.ProjectsFile, "[projects-file]"}, process.PathRedaction{home, "[home]"})
	return process.OrderedPaths(paths)
}

func (s *Service) resolveCwd(w wire.Workspace, input string) (string, string, error) {
	abs, rel, err := s.guard.ResolvePath(w, input, false)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", badArgs("cwd must be a directory.")
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	if rel == "" {
		rel = "."
	}
	return canonical, filepath.ToSlash(rel), nil
}

func (s *Service) admit(o startOptions) (Record, bool, error) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.closed.Load() {
		return Record{}, false, jobError("server_restarting", "Server restarting; retry after it is ready.")
	}
	if err := s.startupAdmissionError(); err != nil {
		return Record{}, false, err
	}
	var pendingCompletions int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM job_completions").Scan(&pendingCompletions); err != nil {
		return Record{}, false, err
	}
	if pendingCompletions >= s.cfg.Int("maxPendingJobCompletions", 1000) {
		return Record{}, false, jobError("job_limit_reached", "Pending job audit capacity reached. Retry after audit recovery.")
	}
	snapshot := s.Snapshot()
	if o.Origin == "background" {
		key := commandKey(o.Command, o.Cwd, o.InputJobIDs)
		for _, r := range snapshot {
			if r.Status == "running" && r.Origin != "foreground" && r.WorkspaceID == o.Workspace.ID && commandKey(r.Command, r.Cwd, r.InputJobIDs) == key {
				return r, true, nil
			}
		}
		if s.capacity(o.Workspace.ID) <= 0 {
			return Record{}, false, jobError("job_limit_reached", fmt.Sprintf("Background job limit reached (%d per workspace, %d per server). Collect or stop running jobs first.", s.cfg.Int("maxJobsPerWorkspace", 6), s.cfg.Int("maxJobs", 12)))
		}
	}
	active := 0
	inputs := map[string]bool{}
	for _, r := range snapshot {
		if r.Status == "running" || ownershipPending(r) {
			active++
			for _, id := range r.InputJobIDs {
				inputs[id] = true
			}
		}
	}
	if active >= s.cfg.Int("maxActiveCaptures", s.cfg.Int("maxJobs", 12)*2) {
		return Record{}, false, jobError("job_limit_reached", "Active command capacity reached.")
	}
	o.InputJobIDs = uniqueExact(o.InputJobIDs)
	for _, id := range o.InputJobIDs {
		r, err := s.require(id, o.Workspace.ID)
		if err != nil {
			return Record{}, false, err
		}
		if r.OutputExpired {
			return Record{}, false, jobError("job_output_expired", "Job output expired under retention. Rerun only if needed and safe.")
		}
		inputs[id] = true
	}
	var reserved int64
	for id := range inputs {
		r, ok := s.Get(id, "")
		if !ok {
			continue
		}
		if r.Status == "running" {
			reserved += r.OutputLimitBytes*3 + 4096
		} else {
			reserved += s.logBytes(r)
		}
	}
	if reserved > s.cfg.Int64("maxRetainedJobBytes", 536870912) {
		return Record{}, false, jobError("job_storage_limit", "Pinned log inputs exceed the retained-output budget. Inspect fewer logs per command.")
	}
	var id string
	for {
		part, err := process.RandomHex(4)
		if err != nil {
			return Record{}, false, err
		}
		id = "job_" + part
		if _, ok := s.Get(id, ""); ok {
			continue
		}
		if _, err = os.Stat(filepath.Join(s.dir, id+".spec.json")); os.IsNotExist(err) {
			break
		}
	}
	now := time.Now()
	base := filepath.Join(s.dir, id)
	r := Record{ID: id, WorkspaceID: o.Workspace.ID, ProjectID: o.Workspace.ProjectID, Root: o.Workspace.Root, Cwd: o.Cwd, Command: o.Command, CommandLabel: commandLabel(o.Command), Origin: o.Origin, Status: "running", StartedAt: wire.Timestamp(now), StartedAtMS: now.UnixMilli(), DeadlineMS: now.UnixMilli() + o.TimeoutMS, TimeoutMS: o.TimeoutMS, OutputLimitBytes: o.OutputLimit, StdoutPath: base + ".out", StderrPath: base + ".err", ExitPath: base + ".exit", ResultPath: base + ".result.json", ControlPath: base + ".control", GrantPath: base + ".grant", SpecPath: base + ".spec.json", RunnerVersion: 2, LaunchState: "prepared", InputJobIDs: o.InputJobIDs, BashSessionID: o.BashSessionID, OwnerPrincipalID: o.Scope.PrincipalID, OwnerSessionID: o.Scope.SessionID, RequestID: o.Scope.RequestID, ParentActionID: o.Scope.ParentActionID}
	if s.useScopes {
		incarnation, err := process.RandomHex(16)
		if err != nil {
			return Record{}, false, err
		}
		r.ScopeUnit = "codexpro-" + id + "-" + incarnation
	}
	if s.cfg.HostResourcesHelper != "" {
		run, err := process.RandomHex(16)
		if err != nil {
			return Record{}, false, err
		}
		registration, err := process.RandomHex(16)
		if err != nil {
			return Record{}, false, err
		}
		r.ResourceClaim = &process.ResourceClaim{Version: 1, Helper: s.cfg.HostResourcesHelper, RunID: run, RegistrationID: registration}
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return Record{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Record{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO jobs(id,workspace_id,started_ms,status,origin,payload) VALUES(?,?,?,?,?,?)", r.ID, r.WorkspaceID, r.StartedAtMS, r.Status, r.Origin, payload); err != nil {
		return Record{}, false, err
	}
	for _, input := range r.InputJobIDs {
		if _, err = tx.Exec("INSERT INTO job_inputs(job_id,input_id) VALUES(?,?)", r.ID, input); err != nil {
			return Record{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	s.records[id] = newEntry(r, true)
	s.mu.Unlock()
	s.notify()
	return r, false, nil
}

func (s *Service) start(ctx context.Context, o startOptions) (Record, error) {
	r, duplicate, err := s.admit(o)
	if err != nil || duplicate {
		return r, err
	}
	entry := s.entry(r.ID)
	defer func() { entry.launching.Store(false); s.notify() }()
	fail := func(cause error) (Record, error) {
		result := process.Result{StopReason: stringAddress("lost"), Quiescent: r.PID == 0}
		_ = s.finalize(r.ID, &result, "lost")
		s.enqueueSettlement(r.ID)
		return r, cause
	}
	nonce, err := process.RandomHex(24)
	if err != nil {
		return fail(err)
	}
	output := s.outputDir(r)
	if err = os.MkdirAll(output, 0700); err != nil {
		return fail(err)
	}
	for _, path := range []string{r.StdoutPath, r.StderrPath, filepath.Join(output, "stdout.log"), filepath.Join(output, "stderr.log")} {
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return fail(e)
		}
		if e = f.Close(); e != nil {
			return fail(e)
		}
	}
	spec := process.Spec{Version: 1, Command: r.Command, Cwd: o.CwdAbs, Stdout: r.StdoutPath, Stderr: r.StderrPath, Exit: r.ExitPath, Result: r.ResultPath, Control: r.ControlPath, OutputDir: output, Grant: r.GrantPath, Nonce: nonce, Deadline: r.DeadlineMS, TimeoutMS: r.TimeoutMS, Limit: r.OutputLimitBytes, Nice: s.cfg.Int("jobNice", 10), PathRedactions: s.paths(o.Workspace)}
	if r.ScopeUnit != "" {
		spec.ScopeUnit = r.ScopeUnit + ".scope"
	}
	if err = process.WriteJSON(r.SpecPath, spec); err != nil {
		return fail(err)
	}
	if r.ResourceClaim != nil {
		retry := time.Duration(max(0, s.cfg.Int("hostResourceRetryMs", 60000))) * time.Millisecond
		if _, err = process.BeginResourcesWithRetry(ctx, *r.ResourceClaim, o.CwdAbs, spec.ScopeUnit, retry); err != nil {
			return fail(err)
		}
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	name, args := s.executable, []string{"runner", r.SpecPath}
	if spec.Nice > 0 {
		name, args = "nice", append([]string{"-n", fmt.Sprint(spec.Nice), s.executable}, args...)
	}
	if r.ResourceClaim != nil {
		name, args = process.ResourceCommand(*r.ResourceClaim, append([]string{name}, args...))
	}
	if s.useScopes {
		name, args = "systemd-run", append([]string{"--user", "--scope", "--quiet", "--collect", "--unit", r.ScopeUnit, "--", name}, args...)
	}
	env := process.RestrictedEnv(s.cfg.Bool("inheritEnv", false))
	env = process.WithEnv(env, "CODEXPRO_JOB_OUTPUT_DIR", s.workspaceOutputDir(r.WorkspaceID))
	if s.useScopes {
		for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
			if v := os.Getenv(key); v != "" {
				env = process.WithEnv(env, key, v)
			}
		}
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = o.CwdAbs
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return fail(err)
	}
	r.PID = cmd.Process.Pid
	r.ProcessIdentity = process.ProcessIdentity(r.PID)
	updated, err := s.update(r.ID, func(next *Record) bool {
		next.PID = r.PID
		next.ProcessIdentity = r.ProcessIdentity
		next.LaunchState = "granted"
		return true
	})
	if err != nil {
		_ = process.SignalGroup(r.PID, syscall.SIGTERM)
		go cmd.Wait()
		return fail(err)
	}
	r = updated
	if err = process.ExclusiveWrite(r.GrantPath, []byte(nonce), 0600); err != nil {
		_ = process.SignalGroup(r.PID, syscall.SIGTERM)
		go cmd.Wait()
		return fail(err)
	}
	go func() { _ = cmd.Wait(); s.notify() }()
	return r, nil
}

func stringAddress(s string) *string { return &s }

func (s *Service) Stop(id, workspace, reason string) (Record, error) {
	if reason != "stopped" && reason != "timeout" && reason != "output_limit" {
		return Record{}, badArgs("Invalid job stop reason.")
	}
	r, err := s.require(id, workspace)
	if err != nil || r.Status != "running" {
		return r, err
	}
	r, err = s.update(id, func(next *Record) bool {
		if next.Status != "running" || next.RequestedStopReason != "" {
			return false
		}
		next.RequestedStopReason = reason
		next.StopRequestedAtMS = time.Now().UnixMilli()
		return true
	})
	if err != nil {
		return r, err
	}
	if r.Status == "running" {
		if err = process.AtomicWrite(r.ControlPath, []byte(r.RequestedStopReason), 0600); err != nil {
			return r, err
		}
	}
	s.notify()
	return r, nil
}

func (s *Service) promote(id string) (Record, error) {
	return s.update(id, func(r *Record) bool {
		if r.Status == "running" && r.Origin == "foreground" {
			r.Origin = "promoted"
			return true
		}
		return false
	})
}
func (s *Service) acknowledge(ids []string) error {
	for _, id := range ids {
		if _, err := s.update(id, func(r *Record) bool {
			if r.Status != "running" && !r.Acknowledged {
				r.Acknowledged = true
				return true
			}
			return false
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) session(requested string) (string, error) {
	configured := s.cfg.String("bashSessionId", "")
	required := s.cfg.Bool("requireBashSession", false)
	requested = strings.TrimSpace(requested)
	if configured == "" {
		if required {
			return "", jobError("bash_session_required", "bash session guard is enabled but no server bash session id is configured.")
		}
		return "", nil
	}
	recovery := map[string]any{"tool": "bash", "message": "Retry with the server bash session id.", "args": map[string]any{"session_id": configured}}
	if requested == "" {
		if required {
			return "", &wire.Error{Code: "bash_session_required", Message: fmt.Sprintf("bash session id is required. Retry with session_id=%q.", configured), Data: map[string]any{"retry_unchanged": false, "recovery": recovery}}
		}
		return configured, nil
	}
	if requested != configured {
		return "", &wire.Error{Code: "bash_session_mismatch", Message: fmt.Sprintf("bash session id mismatch. This CodexPro server accepts session_id=%q.", configured), Data: map[string]any{"retry_unchanged": false, "recovery": recovery}}
	}
	return configured, nil
}

func validCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return badArgs("command is required.")
	}
	if !utf8.ValidString(command) || strings.IndexByte(command, 0) >= 0 {
		return badArgs("command must be valid text without NUL bytes.")
	}
	return nil
}
