package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func integerArg(args map[string]any, key string, fallback, low, high int) (int, error) {
	v, ok := args[key]
	if !ok {
		return fallback, nil
	}
	var n float64
	switch x := v.(type) {
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case float64:
		n = x
	case json.Number:
		var err error
		n, err = strconv.ParseFloat(string(x), 64)
		if err != nil {
			return 0, badArgs(key + " must be an integer.")
		}
	default:
		return 0, badArgs(key + " must be an integer.")
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(low) || n > float64(high) {
		return 0, badArgs(fmt.Sprintf("%s must be an integer between %d and %d.", key, low, high))
	}
	return int(n), nil
}

func stringsArg(args map[string]any, key string, maxCount int, required bool) ([]string, error) {
	v, exists := args[key]
	if !exists {
		if required {
			return nil, badArgs(key + " is required.")
		}
		return []string{}, nil
	}
	var result []string
	switch list := v.(type) {
	case []string:
		result = append([]string{}, list...)
	case []any:
		result = []string{}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, badArgs(key + " must contain strings.")
			}
			result = append(result, s)
		}
	default:
		return nil, badArgs(key + " must be an array.")
	}
	if len(result) > maxCount || (required && len(result) == 0) {
		return nil, badArgs(fmt.Sprintf("%s must contain %s%d entries.", key, map[bool]string{true: "1 through ", false: "at most "}[required], maxCount))
	}
	return result, nil
}

func validateOptionalTypes(args map[string]any, stringsKeys, boolKeys []string) error {
	for _, k := range stringsKeys {
		if v, ok := args[k]; ok {
			if _, ok = v.(string); !ok {
				return badArgs(k + " must be a string.")
			}
		}
	}
	for _, k := range boolKeys {
		if v, ok := args[k]; ok {
			if _, ok = v.(bool); !ok {
				return badArgs(k + " must be a boolean.")
			}
		}
	}
	return nil
}

func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	switch name {
	case "bash", "start_jobs", "jobs", "stop_jobs":
	default:
		return wire.Result{}, wire.Errorf("unknown_tool", "Unknown job tool: %s", name)
	}
	w, err := s.resolver.Resolve(ctx, scope, wire.String(args, "workspace_id", ""))
	if err != nil {
		return wire.Result{}, err
	}
	switch name {
	case "bash":
		return s.callBash(ctx, scope, w, args)
	case "start_jobs":
		return s.callStartJobs(ctx, scope, w, args)
	case "jobs":
		return s.callJobs(ctx, w, args)
	default:
		return s.callStopJobs(ctx, w, args)
	}
}

func (s *Service) runBash(ctx context.Context, scope wire.Scope, w wire.Workspace, command, cwd, session string, inputs []string, background bool, waitMS int, onTimeout string) (Record, Output, error) {
	if err := validCommand(command); err != nil {
		return Record{}, Output{}, err
	}
	bashSession, err := s.session(session)
	if err != nil {
		return Record{}, Output{}, err
	}
	abs, relative, err := s.resolveCwd(w, cwd)
	if err != nil {
		return Record{}, Output{}, err
	}
	origin := "foreground"
	if background {
		origin = "background"
	}
	hard := s.cfg.Int64("jobTimeoutMs", 1500000)
	if !background && onTimeout == "kill" {
		hard = min(hard, int64(waitMS))
	}
	r, err := s.start(ctx, startOptions{Workspace: w, Scope: scope, CwdAbs: abs, Cwd: relative, Command: command, Origin: origin, TimeoutMS: hard, OutputLimit: s.cfg.Int64("maxJobOutputBytes", 8388608), BashSessionID: bashSession, InputJobIDs: inputs})
	if err != nil {
		return r, Output{}, err
	}
	wait := time.Duration(waitMS) * time.Millisecond
	if background {
		wait = time.Second
	}
	_, err = s.wait(ctx, []string{r.ID}, "all", wait, false)
	if err != nil {
		if !background {
			_, _ = s.promote(r.ID)
		}
		return r, Output{}, err
	}
	r, err = s.require(r.ID, w.ID)
	if err != nil {
		return r, Output{}, err
	}
	if !background && r.Status == "running" && !s.closed.Load() {
		if onTimeout == "background" {
			r, err = s.promote(r.ID)
		} else {
			if _, err = s.Stop(r.ID, w.ID, "timeout"); err == nil {
				_, err = s.wait(ctx, []string{r.ID}, "all", 3*time.Second, false)
			}
			if err == nil {
				r, err = s.require(r.ID, w.ID)
			}
		}
		if err != nil {
			return r, Output{}, err
		}
	}
	budget := min(s.cfg.Int("maxOutputBytes", 120000), outputPageMax/2)
	head := true
	if r.Status == "running" {
		budget = 4096
		head = false
	}
	out, err := s.read(r, budget, head)
	return r, out, err
}

func (s *Service) callBash(ctx context.Context, scope wire.Scope, w wire.Workspace, args map[string]any) (wire.Result, error) {
	if err := validateOptionalTypes(args, []string{"command", "cwd", "session_id", "on_timeout"}, []string{"background"}); err != nil {
		return wire.Result{}, err
	}
	wait, err := integerArg(args, "timeout_ms", s.cfg.Int("bashTimeoutMs", 120000), 1000, 2147483647)
	if err != nil {
		return wire.Result{}, err
	}
	maximum := s.cfg.Int("maxBashTimeoutMs", 600000)
	if wait > maximum {
		return wire.Result{}, badArgs(fmt.Sprintf("timeout_ms exceeds the current maximum of %d ms; check server_config.", maximum))
	}
	onTimeout := wire.String(args, "on_timeout", "background")
	if onTimeout != "background" && onTimeout != "kill" {
		return wire.Result{}, badArgs("on_timeout must be background or kill.")
	}
	inputs, err := stringsArg(args, "input_job_ids", 16, false)
	if err != nil {
		return wire.Result{}, err
	}
	r, out, err := s.runBash(ctx, scope, w, wire.String(args, "command", ""), wire.String(args, "cwd", "."), wire.String(args, "session_id", ""), inputs, wire.Bool(args, "background", false), wait, onTimeout)
	if err != nil {
		return wire.Result{}, err
	}
	data := s.OutputMetadata(r)
	data["workspace_id"] = w.ID
	data["root"] = w.Root
	data["returned_bytes"] = len(out.Stdout) + len(out.Stderr)
	data["command"] = r.Command
	data["cwd"] = r.Cwd
	data["exit_code"] = exitValue(r.ExitCode)
	data["signal"] = nullable(r.Signal)
	data["duration_ms"] = duration(r)
	data["stdout"] = out.Stdout
	data["stderr"] = out.Stderr
	data["truncated"] = out.Truncated
	data["timed_out"] = r.Status == "timed_out"
	data["job_id"] = r.ID
	data["job_status"] = r.Status
	data["job_origin"] = r.Origin
	data["bash_session_id"] = nullable(r.BashSessionID)
	text := outputReceipt(r, data, len(out.Stdout)+len(out.Stderr)) + "\n\n" + s.bashText(r, out)
	return s.success(w, "bash", "Bash", data, text), nil
}

func commandObjects(value any) ([]map[string]any, error) {
	switch values := value.(type) {
	case []map[string]any:
		return values, nil
	case []any:
		out := make([]map[string]any, 0, len(values))
		for _, item := range values {
			v, ok := item.(map[string]any)
			if !ok {
				return nil, badArgs("commands must contain command objects.")
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, badArgs("commands must be an array.")
	}
}

func (s *Service) callStartJobs(ctx context.Context, scope wire.Scope, w wire.Workspace, args map[string]any) (wire.Result, error) {
	if err := validateOptionalTypes(args, []string{"session_id"}, nil); err != nil {
		return wire.Result{}, err
	}
	commands, err := commandObjects(args["commands"])
	if err != nil {
		return wire.Result{}, err
	}
	maximum := s.cfg.Int("maxJobsPerWorkspace", 6)
	if len(commands) == 0 {
		return wire.Result{}, badArgs("commands must contain at least one command.")
	}
	if len(commands) > maximum {
		return wire.Result{}, badArgs(fmt.Sprintf("commands exceeds the current per-call maximum of %d; check server_config.", maximum))
	}
	existing := map[string]bool{}
	for _, r := range s.Snapshot() {
		if r.Status == "running" && r.WorkspaceID == w.ID && r.Origin != "foreground" {
			existing[commandKey(r.Command, r.Cwd, r.InputJobIDs)] = true
		}
	}
	newKeys := map[string]bool{}
	inputIDs := make([][]string, len(commands))
	for i, item := range commands {
		for key := range item {
			if key != "command" && key != "cwd" && key != "label" && key != "input_job_ids" {
				return wire.Result{}, badArgs("Unknown command option: " + key)
			}
		}
		if err = validateOptionalTypes(item, []string{"command", "cwd", "label"}, nil); err != nil {
			return wire.Result{}, err
		}
		command := wire.String(item, "command", "")
		if err = validCommand(command); err != nil {
			return wire.Result{}, err
		}
		if utf8.RuneCountInString(wire.String(item, "label", "")) > 80 {
			return wire.Result{}, badArgs("label must contain at most 80 characters.")
		}
		_, relative, err := s.resolveCwd(w, wire.String(item, "cwd", "."))
		if err != nil {
			return wire.Result{}, err
		}
		inputIDs[i], err = stringsArg(item, "input_job_ids", 16, false)
		if err != nil {
			return wire.Result{}, err
		}
		key := commandKey(command, relative, inputIDs[i])
		if !existing[key] {
			newKeys[key] = true
		}
	}
	capacity := s.capacity(w.ID)
	if len(newKeys) > capacity {
		return wire.Result{}, &wire.Error{Code: "job_limit_reached", Message: fmt.Sprintf("Cannot start %d new jobs: only %d more background job%s may run in this workspace right now (%d per workspace, %d per server). Collect or stop running jobs first, or start fewer.", len(newKeys), capacity, plural(capacity), maximum, s.cfg.Int("maxJobs", 12)), Data: map[string]any{"retry_unchanged": false, "capacity": capacity}}
	}
	views := []map[string]any{}
	ids := []string{}
	running := 0
	lines := []string{}
	for i, item := range commands {
		r, _, err := s.runBash(ctx, scope, w, wire.String(item, "command", ""), wire.String(item, "cwd", "."), wire.String(args, "session_id", ""), inputIDs[i], true, s.cfg.Int("bashTimeoutMs", 120000), "background")
		if err != nil {
			return wire.Result{}, err
		}
		var out *Output
		if r.Status != "running" {
			value, err := s.read(r, 1024, false)
			if err != nil {
				return wire.Result{}, err
			}
			out = &value
		} else {
			running++
		}
		view := s.jobView(r, out, false)
		label := wire.String(item, "label", r.CommandLabel)
		view["label"] = label
		views = append(views, view)
		ids = append(ids, r.ID)
		line := "- " + r.ID + " · " + label + " · " + r.Status
		if r.ExitCode != nil {
			line += " exit " + exitLabel(r.ExitCode)
		}
		lines = append(lines, line)
	}
	collect := "All finished already."
	if running > 0 {
		b, _ := json.Marshal(ids)
		collect = "Collect with one call: jobs(job_ids=" + string(b) + ", wait_for=\"all\", wait_ms=300000) — or wait_for=\"any\" to act on the first finisher. Do other work meanwhile."
	}
	text := fmt.Sprintf("# Started %d job%s\n\n", len(views), plural(len(views))) + strings.Join(lines, "\n") + "\n\n" + collect
	return s.success(w, "start_jobs", "Start Background Jobs", map[string]any{"workspace_id": w.ID, "root": w.Root, "jobs": views, "job_ids": ids, "running_count": running}, text), nil
}

func (s *Service) callJobs(ctx context.Context, w wire.Workspace, args map[string]any) (wire.Result, error) {
	if err := validateOptionalTypes(args, []string{"output", "cursor", "wait_for"}, []string{"full_output", "include_finished"}); err != nil {
		return wire.Result{}, err
	}
	ids, err := stringsArg(args, "job_ids", 32, false)
	if err != nil {
		return wire.Result{}, err
	}
	if _, exists := args["job_ids"]; exists && len(ids) == 0 {
		return wire.Result{}, badArgs("job_ids must contain at least one job.")
	}
	ids = uniqueIDs(ids)
	mode := wire.String(args, "output", "")
	if mode == "" {
		mode = "tail"
		if wire.Bool(args, "full_output", false) {
			mode = "head"
		}
	}
	switch mode {
	case "none", "head", "tail", "incremental":
	default:
		return wire.Result{}, badArgs("Invalid output mode.")
	}
	_, hasOutput := args["output"]
	_, hasFull := args["full_output"]
	_, hasCursor := args["cursor"]
	_, hasMax := args["max_bytes"]
	_, hasWaitFor := args["wait_for"]
	_, hasTail := args["tail_bytes"]
	if hasOutput && hasFull {
		return wire.Result{}, badArgs("Choose output or legacy full_output, not both.")
	}
	if mode != "incremental" && (hasCursor || hasMax) {
		return wire.Result{}, badArgs("cursor/max_bytes require output=incremental.")
	}
	if mode == "incremental" && (len(ids) != 1 || hasWaitFor || hasTail || hasFull) {
		return wire.Result{}, badArgs("Incremental output requires one job; omit completion-wait and head/tail options.")
	}
	if len(ids) == 0 && mode != "tail" && mode != "none" {
		return wire.Result{}, badArgs("Specify job_ids to read output.")
	}
	waitFor := wire.String(args, "wait_for", "all")
	if waitFor != "all" && waitFor != "any" {
		return wire.Result{}, badArgs("wait_for must be all or any.")
	}
	defaultWait := 30000
	if mode == "incremental" {
		defaultWait = 0
	}
	waitMS, err := integerArg(args, "wait_ms", defaultWait, 0, 300000)
	if err != nil {
		return wire.Result{}, err
	}
	pageBudget, err := integerArg(args, "max_bytes", outputPageDefault, 256, outputPageMax)
	if err != nil {
		return wire.Result{}, err
	}
	tailDefault := 4096
	if len(ids) == 0 {
		tailDefault = 512
	}
	tailBytes, err := integerArg(args, "tail_bytes", tailDefault, 256, 65536)
	if err != nil {
		return wire.Result{}, err
	}
	cursor := wire.String(args, "cursor", "")
	if len(cursor) > 2048 {
		return wire.Result{}, badArgs("cursor must contain at most 2048 characters.")
	}
	recordIDs := append([]string{}, ids...)
	if len(ids) == 0 {
		for _, r := range s.Snapshot() {
			if r.WorkspaceID == w.ID && r.Origin != "foreground" && (wire.Bool(args, "include_finished", true) || r.Status == "running") {
				recordIDs = append(recordIDs, r.ID)
				if len(recordIDs) == 20 {
					break
				}
			}
		}
	} else {
		for _, id := range ids {
			if _, err = s.require(id, w.ID); err != nil {
				return wire.Result{}, err
			}
		}
	}
	started := time.Now()
	interrupted := false
	if mode == "incremental" {
		r, _ := s.require(recordIDs[0], w.ID)
		interrupted, err = s.waitOutput(ctx, r, cursor, time.Duration(waitMS)*time.Millisecond)
	} else if len(ids) > 0 {
		interrupted, err = s.wait(ctx, ids, waitFor, time.Duration(waitMS)*time.Millisecond, true)
	}
	if err != nil {
		return wire.Result{}, err
	}
	budget := outputPageMax
	if mode == "incremental" {
		budget = pageBudget
	}
	for {
		views := []map[string]any{}
		records := []Record{}
		texts := []string{fmt.Sprintf("# Jobs (%d)", len(recordIDs))}
		returned, running, succeeded := 0, 0, 0
		perStream := budget / max(1, len(recordIDs)*2)
		for _, id := range recordIDs {
			r, err := s.require(id, w.ID)
			if err != nil {
				return wire.Result{}, err
			}
			records = append(records, r)
			var view map[string]any
			var out Output
			if mode == "incremental" {
				p, err := s.page(r, cursor, budget)
				if err != nil {
					return wire.Result{}, err
				}
				view = s.jobView(r, nil, false)
				for k, v := range p.fields() {
					view[k] = v
				}
				out = Output{Stdout: p.Stdout, Stderr: p.Stderr}
			} else {
				head := mode == "head" && (wire.String(args, "output", "") == "head" || r.Status != "running")
				var ptr *Output
				if mode != "none" && (len(ids) > 0 || r.Status == "running") {
					readBudget := min(tailBytes, perStream)
					if head {
						readBudget = min(s.cfg.Int("maxOutputBytes", 120000), perStream)
					}
					out, err = s.read(r, readBudget, head)
					if err != nil {
						return wire.Result{}, err
					}
					ptr = &out
				}
				view = s.jobView(r, ptr, head)
				if mode == "none" {
					view["output_mode"] = "none"
				}
			}
			views = append(views, view)
			n := int(number(view["returned_bytes"]))
			returned += n
			if r.Status == "running" {
				running++
			}
			if r.Status == "succeeded" {
				succeeded++
			}
			label := ""
			if view["output_mode"] == "tail" {
				label = " (tail)"
			}
			texts = append(texts, jobLine(r)+"\n"+outputReceipt(r, view, n)+"\n"+outputBlocks(out, label))
		}
		if interrupted {
			texts = append(texts, "Server restarting; resume with the same job IDs/cursor.")
		}
		requested := waitMS
		if len(ids) == 0 {
			requested = 0
		}
		returnedWaitFor := waitFor
		if mode == "incremental" {
			returnedWaitFor = "output"
		}
		data := map[string]any{"workspace_id": w.ID, "root": w.Root, "jobs": views, "job_ids": ids, "count": len(views), "wait_for": returnedWaitFor, "waited_ms": time.Since(started).Milliseconds(), "requested_wait_ms": requested, "all_finished": running == 0, "all_succeeded": succeeded == len(records), "running_count": running, "returned_output_bytes": returned, "max_response_bytes": outputResponseMax}
		if interrupted {
			data["server_restarting"] = true
		}
		result := s.success(w, "jobs", "Background Jobs", data, strings.Join(texts, "\n\n"))
		encoded, err := json.Marshal(result)
		if err != nil {
			return wire.Result{}, err
		}
		if len(encoded) <= outputResponseMax-4096 {
			if len(ids) > 0 && mode != "incremental" && mode != "none" {
				done := []string{}
				for _, r := range records {
					if r.Status != "running" {
						done = append(done, r.ID)
					}
				}
				if err = s.acknowledge(done); err != nil {
					return wire.Result{}, err
				}
			}
			return result, nil
		}
		if budget <= 256 {
			return wire.Result{}, badArgs("Job metadata exceeds the response budget; collect fewer job IDs.")
		}
		budget = max(256, budget/2)
	}
}

func (s *Service) callStopJobs(ctx context.Context, w wire.Workspace, args map[string]any) (wire.Result, error) {
	ids, err := stringsArg(args, "job_ids", 32, true)
	if err != nil {
		return wire.Result{}, err
	}
	ids = uniqueIDs(ids)
	for _, id := range ids {
		if _, err = s.require(id, w.ID); err != nil {
			return wire.Result{}, err
		}
	}
	stopped := []string{}
	already := []string{}
	for _, id := range ids {
		r, _ := s.require(id, w.ID)
		if r.Status == "running" {
			stopped = append(stopped, id)
			if _, err = s.Stop(id, w.ID, "stopped"); err != nil {
				return wire.Result{}, err
			}
		} else {
			already = append(already, id)
		}
	}
	if len(stopped) > 0 {
		if _, err = s.wait(ctx, stopped, "all", 3*time.Second, false); err != nil {
			return wire.Result{}, err
		}
	}
	views := []map[string]any{}
	lines := []string{}
	done := []string{}
	for _, id := range ids {
		r, err := s.require(id, w.ID)
		if err != nil {
			return wire.Result{}, err
		}
		if r.Status != "running" {
			done = append(done, id)
		}
		out, err := s.read(r, min(4096, outputPageMax/max(1, len(ids)*2)), false)
		if err != nil {
			return wire.Result{}, err
		}
		views = append(views, s.jobView(r, &out, false))
		lines = append(lines, jobLine(r))
	}
	if err = s.acknowledge(done); err != nil {
		return wire.Result{}, err
	}
	return s.success(w, "stop_jobs", "Stop Jobs", map[string]any{"workspace_id": w.ID, "root": w.Root, "jobs": views, "stopped_ids": stopped, "already_finished_ids": already}, fmt.Sprintf("# Stopped %d of %d\n\n", len(stopped), len(ids))+strings.Join(lines, "\n")), nil
}
