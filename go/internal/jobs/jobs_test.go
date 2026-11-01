package jobs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		if err := process.RunRunner(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type testResolver struct{ workspace wire.Workspace }

func (r testResolver) Resolve(_ context.Context, _ wire.Scope, id string) (wire.Workspace, error) {
	if id == "" || id == r.workspace.ID {
		return r.workspace, nil
	}
	if id == "other" {
		w := r.workspace
		w.ID = "other"
		return w, nil
	}
	return wire.Workspace{}, wire.Errorf("workspace_not_found", "Unknown workspace_id: %s", id)
}
func (r testResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.workspace.ProjectID, Root: r.workspace.Root, Label: "test"}}
}
func (r testResolver) Project(id string) (wire.Project, bool) {
	if id == r.workspace.ProjectID {
		return r.Projects()[0], true
	}
	return wire.Project{}, false
}

func newFixture(t *testing.T) (*Service, *config.Config, testResolver) {
	t.Helper()
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(base, "state"))
	cfg.Set("jobTimeoutMs", 5000)
	cfg.Set("bashTimeoutMs", 1000)
	cfg.Set("maxBashTimeoutMs", 5000)
	cfg.Set("jobRetentionMs", 60000)
	resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: root, Mode: "workspace"}}
	s, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !s.closed.Load() {
			for _, r := range s.Snapshot() {
				if r.Status == "running" {
					_, _ = s.Stop(r.ID, "", "stopped")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			ids := []string{}
			for _, r := range s.Snapshot() {
				ids = append(ids, r.ID)
			}
			_, _ = s.wait(ctx, ids, "all", 3*time.Second, false)
			_ = s.Close()
		}
	})
	return s, cfg, resolver
}

func resultData(t *testing.T, result wire.Result) map[string]any {
	t.Helper()
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func invoke(t *testing.T, s *Service, name string, args map[string]any) map[string]any {
	t.Helper()
	args["workspace_id"] = "test-workspace"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.Call(ctx, wire.Scope{PrincipalID: "principal", SessionID: "session", RequestID: "request", StartedAt: time.Now()}, name, args)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatal("unexpected tool error")
	}
	return resultData(t, result)
}

func TestBashWireOutcomeAndReadAcknowledgement(t *testing.T) {
	s, _, _ := newFixture(t)
	d := invoke(t, s, "bash", map[string]any{"command": "printf 'ok\\n'; printf 'warn\\n' >&2; exit 7"})
	if d["exit_code"] != float64(7) || d["job_status"] != "failed" || d["stdout"] != "ok\n" || d["stderr"] != "warn\n" || d["signal"] != nil || d["timed_out"] != false {
		t.Fatalf("wrong Bash contract: %#v", d)
	}
	if d["root"] != "[workspace:test-workspace]" || d["bash_session_id"] != nil {
		t.Fatalf("path/null contract: %#v", d)
	}
	id := d["job_id"].(string)
	invoke(t, s, "jobs", map[string]any{"job_ids": []string{id}, "output": "none", "wait_ms": 0})
	r, _ := s.Get(id, "")
	if r.Acknowledged {
		t.Fatal("metadata read acknowledged completion")
	}
	invoke(t, s, "jobs", map[string]any{"job_ids": []string{id}, "output": "incremental", "wait_ms": 0, "max_bytes": 256})
	r, _ = s.Get(id, "")
	if r.Acknowledged {
		t.Fatal("incremental read acknowledged completion")
	}
	invoke(t, s, "jobs", map[string]any{"job_ids": []string{id}, "wait_ms": 0})
	r, _ = s.Get(id, "")
	if !r.Acknowledged {
		t.Fatal("completion collection did not acknowledge")
	}
	_, err := s.Call(context.Background(), wire.Scope{}, "jobs", map[string]any{"workspace_id": "other", "job_ids": []string{id}, "wait_ms": 0})
	if e, ok := err.(*wire.Error); !ok || e.Code != "job_not_found" {
		t.Fatalf("cross-workspace lookup escaped: %v", err)
	}
}

func TestForegroundPromotionUsesRequestWait(t *testing.T) {
	s, _, _ := newFixture(t)
	d := invoke(t, s, "bash", map[string]any{"command": "printf begin; sleep 1.6; printf end", "timeout_ms": 1000})
	if d["job_status"] != "running" || d["job_origin"] != "promoted" {
		t.Fatalf("foreground request did not promote: %#v", d)
	}
	id := d["job_id"].(string)
	d = invoke(t, s, "jobs", map[string]any{"job_ids": []string{id}, "wait_ms": 4000})
	if d["all_succeeded"] != true || d["all_finished"] != true {
		t.Fatalf("promoted job did not complete: %#v", d)
	}
	view := d["jobs"].([]any)[0].(map[string]any)
	if view["stdout_tail"] != "beginend" {
		t.Fatalf("promoted output lost: %#v", view)
	}
}

func TestIncrementalUTF8CursorAndIdentity(t *testing.T) {
	s, _, _ := newFixture(t)
	d := invoke(t, s, "bash", map[string]any{"command": "for i in $(seq 1 120); do printf 'αβγδ\\n'; done; printf 'err\\n' >&2"})
	id := d["job_id"].(string)
	cursor := ""
	var stdout, stderr strings.Builder
	for i := 0; i < 20; i++ {
		args := map[string]any{"job_ids": []string{id}, "output": "incremental", "max_bytes": 256}
		if cursor != "" {
			args["cursor"] = cursor
		}
		data := invoke(t, s, "jobs", args)
		view := data["jobs"].([]any)[0].(map[string]any)
		out, err := view["stdout"].(string), view["stderr"].(string)
		if !utf8.ValidString(out) || !utf8.ValidString(err) || len(out)+len(err) > 256 {
			t.Fatal("invalid bounded UTF-8 page")
		}
		stdout.WriteString(out)
		stderr.WriteString(err)
		cursor = view["next_cursor"].(string)
		if view["output_complete"] == true {
			break
		}
		if i == 19 {
			t.Fatal("cursor failed to advance")
		}
	}
	if stdout.String() != strings.Repeat("αβγδ\n", 120) || stderr.String() != "err\n" {
		t.Fatal("incremental output omitted or duplicated bytes")
	}
	r, _ := s.Get(id, "")
	bad, _ := json.Marshal(map[string]any{"v": 1, "id": r.WorkspaceID + ":" + r.ID + ":" + r.StartedAt, "offsets": []int{1, 0}})
	_, err := s.Call(context.Background(), wire.Scope{}, "jobs", map[string]any{"workspace_id": r.WorkspaceID, "job_ids": []string{id}, "output": "incremental", "cursor": base64.RawURLEncoding.EncodeToString(bad)})
	if e, ok := err.(*wire.Error); !ok || e.Code != "job_cursor_invalid" {
		t.Fatalf("split cursor accepted: %v", err)
	}
}

func opts(r testResolver, command string) startOptions {
	return startOptions{Workspace: r.workspace, CwdAbs: r.workspace.Root, Cwd: ".", Command: command, Origin: "background", TimeoutMS: 5000, OutputLimit: 8192}
}

func TestPinsProtectOutputUntilDependentCompletion(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	source, err := s.start(context.Background(), opts(resolver, "printf retained"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.wait(context.Background(), []string{source.ID}, "all", 3*time.Second, false); err != nil {
		t.Fatal(err)
	}
	dependentOpts := opts(resolver, "sleep 2; cat \"$CODEXPRO_JOB_OUTPUT_DIR/"+source.ID+"/stdout.log\"")
	dependentOpts.InputJobIDs = []string{source.ID}
	dependent, err := s.start(context.Background(), dependentOpts)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set("jobRetentionMs", 1)
	time.Sleep(10 * time.Millisecond)
	if err = s.prune(); err != nil {
		t.Fatal(err)
	}
	protected, _ := s.Get(source.ID, "")
	if protected.OutputExpired {
		t.Fatal("running dependency lost pinned output")
	}
	if _, err = s.Stop(dependent.ID, "", "stopped"); err != nil {
		t.Fatal(err)
	}
	_, err = s.wait(context.Background(), []string{dependent.ID}, "all", 3*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err = s.prune(); err != nil {
		t.Fatal(err)
	}
	expired, _ := s.Get(source.ID, "")
	if !expired.OutputExpired {
		t.Fatal("unpinned expired output was retained")
	}
	if _, err = s.page(expired, "", 256); err == nil {
		t.Fatal("expired incremental output was served")
	}
}

func TestRestartRecoversAcceptedDetachedRunner(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	r, err := s.start(context.Background(), opts(resolver, "sleep .6; printf persisted"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err = recovered.wait(context.Background(), []string{r.ID}, "all", 4*time.Second, false); err != nil {
		t.Fatal(err)
	}
	r, ok := recovered.Get(r.ID, "")
	if !ok || r.Status != "succeeded" {
		t.Fatalf("restart lost job: %+v", r)
	}
	out, err := recovered.ReadTail(context.Background(), r.ID, r.WorkspaceID, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stdout != "persisted" {
		t.Fatal("restart lost retained output")
	}
}

func TestPreparedClaimRecoveryCancelsStoredAllocationIdentity(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	r, _, err := s.admit(opts(resolver, "touch must-not-run"))
	if err != nil {
		t.Fatal(err)
	}
	run, registration := strings.Repeat("a", 32), strings.Repeat("b", 32)
	marker := filepath.Join(resolver.workspace.Root, "cancelled")
	helper := filepath.Join(resolver.workspace.Root, "helper")
	script := "#!/bin/sh\nif [ \"$2\" = finish ]; then exit 125; fi\nif [ \"$2\" = cancel ]; then printf '%s' \"$3:$5\" > '" + marker + "'; printf '%s' '{\"run_id\":\"" + run + "\",\"status\":\"cancelled\",\"quiescent\":true,\"cancelled\":true}'; fi\n"
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = s.update(r.ID, func(next *Record) bool {
		next.ResourceClaim = &process.ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: registration}
		next.ScopeUnit = "codexpro-" + r.ID + "-" + strings.Repeat("c", 32)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		r, _ = recovered.Get(r.ID, "")
		if r.Quiescent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.Status != "failed" || !r.Quiescent {
		t.Fatalf("prepared claim not recovered: %+v", r)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != run+":"+registration {
		t.Fatalf("wrong allocation cancelled: %q", b)
	}
	if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "must-not-run")); !os.IsNotExist(err) {
		t.Fatal("ungranted command ran")
	}
}

func TestConcurrentAdmissionDeduplicatesAndHonorsCapacity(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobs", 3)
	cfg.Set("maxJobsPerWorkspace", 3)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.start(context.Background(), opts(resolver, "sleep 2; printf same"))
			if err != nil {
				errs <- err
			} else {
				ids <- r.ID
			}
			for i := 0; i < 20; i++ {
				_ = s.Snapshot()
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for id := range ids {
		set[id] = true
	}
	if len(set) != 1 {
		t.Fatalf("duplicate admitted %d jobs", len(set))
	}
	for id := range set {
		_, _ = s.Stop(id, "", "stopped")
	}
	for id := range set {
		_, _ = s.wait(context.Background(), []string{id}, "all", 3*time.Second, false)
	}
	count := 0
	for i := 0; i < 5; i++ {
		_, err := s.start(context.Background(), opts(resolver, fmt.Sprintf("sleep 2; printf job%d", i)))
		if err == nil {
			count++
		} else if e, ok := err.(*wire.Error); !ok || e.Code != "job_limit_reached" {
			t.Fatal(err)
		}
	}
	if count != 3 {
		t.Fatalf("admitted %d distinct jobs at capacity 3", count)
	}
}

func TestInvalidOptionsFailBeforeLaunching(t *testing.T) {
	s, _, _ := newFixture(t)
	cases := []map[string]any{{"output": "none", "full_output": false}, {"output": "incremental", "job_ids": []string{"missing"}, "tail_bytes": 256}, {"cursor": "x"}, {"output": "head"}}
	for _, args := range cases {
		args["workspace_id"] = "test-workspace"
		_, err := s.Call(context.Background(), wire.Scope{}, "jobs", args)
		if e, ok := err.(*wire.Error); !ok || e.Code != "args_invalid" {
			t.Fatalf("option validation mismatch: %v", err)
		}
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("invalid query launched work")
	}
}

func TestUnresolvedOwnerReservesCaptureUntilExactSettlement(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxActiveCaptures", 1)
	r, _, err := s.admit(opts(resolver, "never granted"))
	if err != nil {
		t.Fatal(err)
	}
	unit := "codexpro-" + r.ID + "-" + strings.Repeat("d", 32)
	_, err = s.update(r.ID, func(next *Record) bool {
		next.Status = "failed"
		next.ScopeUnit = unit
		next.Quiescent = false
		next.FinishedAtMS = time.Now().UnixMilli()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.start(context.Background(), opts(resolver, "touch admitted"))
	if e, ok := err.(*wire.Error); !ok || e.Code != "job_limit_reached" {
		t.Fatalf("unresolved owner did not reserve capture: %v", err)
	}
	if len(s.Snapshot()) != 1 {
		t.Fatal("refused admission allocated another job")
	}
	if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "admitted")); !os.IsNotExist(err) {
		t.Fatal("child ran beyond ownership capacity")
	}
	_, err = s.update(r.ID, func(next *Record) bool {
		next.ScopeIdentity = &process.ScopeIdentity{Version: 1, Unit: unit + ".scope", BootID: "00000000-0000-0000-0000-000000000000", InvocationID: strings.Repeat("a", 32), Cgroup: "/user.slice/" + unit + ".scope"}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settle(r.ID)
	r, _ = s.Get(r.ID, "")
	if !r.Quiescent {
		t.Fatal("exact prior-boot proof did not release owner")
	}
	newJob, err := s.start(context.Background(), opts(resolver, "touch admitted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.wait(context.Background(), []string{newJob.ID}, "all", 3*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "admitted")); err != nil {
		t.Fatal("settlement did not release capture capacity")
	}
}

func TestCompletionOutboxSurvivesRestartAndLostAcknowledgement(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	r, err := s.start(context.Background(), opts(resolver, "printf audited"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.wait(context.Background(), []string{r.ID}, "all", 3*time.Second, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM job_completions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal result not atomically queued: count=%d err=%v", count, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	var mu sync.Mutex
	attempts, effects := 0, 0
	receipts := map[string]bool{}
	recovered.SetCompletionSink(func(record Record) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		key := CompletionDedupKey(record)
		if receipts[key] {
			return nil
		}
		receipts[key] = true
		effects++
		return errors.New("simulated lost acknowledgement after durable audit append")
	})
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		if err = recovered.db.QueryRow("SELECT COUNT(*) FROM job_completions").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 0 || attempts < 2 || effects != 1 {
		t.Fatalf("outbox acknowledgement mismatch: pending=%d attempts=%d effects=%d", count, attempts, effects)
	}
	first := Record{ID: "job_same", StartedAt: "first"}
	second := Record{ID: "job_same", StartedAt: "second"}
	if CompletionDedupKey(first) == CompletionDedupKey(second) {
		t.Fatal("public ID reuse would suppress a new completion")
	}
}

func TestConcurrentOutputExpiryReturnsDefinedReceipt(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	r, err := s.start(context.Background(), opts(resolver, "printf 'αβγδ\\n'"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.wait(context.Background(), []string{r.ID}, "all", 3*time.Second, false)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.ID, "")
	var wg sync.WaitGroup
	failures := make(chan error, 100)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := s.page(r, "", 256); err != nil {
					if e, ok := err.(*wire.Error); !ok || e.Code != "job_output_expired" {
						failures <- err
					}
				}
			}
		}()
	}
	cfg.Set("jobRetentionMs", 1)
	time.Sleep(5 * time.Millisecond)
	if err = s.prune(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("expiry raced an output read: %v", err)
	}
}

func TestInterruptWaitsReturnsPendingJobWaitsAsRestarting(t *testing.T) {
	s, _, _ := newFixture(t)
	d := invoke(t, s, "bash", map[string]any{"command": "sleep 4", "timeout_ms": 1000})
	if d["job_status"] != "running" {
		t.Fatalf("job did not stay running: %#v", d)
	}
	id := d["job_id"].(string)
	for _, output := range []string{"tail", "incremental"} {
		t.Run(output, func(t *testing.T) {
			done := make(chan map[string]any, 1)
			go func() {
				done <- invoke(t, s, "jobs", map[string]any{"job_ids": []string{id}, "output": output, "wait_ms": 8000})
			}()
			if output == "tail" {
				time.Sleep(300 * time.Millisecond)
				s.InterruptWaits()
			}
			select {
			case d := <-done:
				if d["server_restarting"] != true || d["all_finished"] != false {
					t.Fatalf("interrupted wait has wrong receipt: %#v", d)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pending wait was not interrupted by drain")
			}
		})
	}
	// A foreground command wait is not interruptible: it still completes.
	d = invoke(t, s, "bash", map[string]any{"command": "sleep 0.3; printf done"})
	if d["job_status"] != "succeeded" || d["stdout"] != "done" {
		t.Fatalf("foreground command interrupted by drain: %#v", d)
	}
}
