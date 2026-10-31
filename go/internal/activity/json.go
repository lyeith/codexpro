package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(audit.Action); ok {
		return map[string]any(m)
	}
	return map[string]any{}
}
func array(v any) []any {
	switch a := v.(type) {
	case []any:
		return a
	case []string:
		o := make([]any, len(a))
		for i, v := range a {
			o[i] = v
		}
		return o
	case []map[string]any:
		o := make([]any, len(a))
		for i, v := range a {
			o[i] = v
		}
		return o
	}
	return nil
}
func stringValue(v any) string { s, _ := v.(string); return s }
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
func boolean(v any) bool { b, _ := v.(bool); return b }
func first(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func safeText(value any, maxBytes int) any {
	s, ok := value.(string)
	if !ok {
		return nil
	}
	s = wire.Redact(s)
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes - 3
	if end < 0 {
		end = 0
	}
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func parseTime(value any) (time.Time, bool) {
	s, ok := value.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	t, e := time.Parse(time.RFC3339Nano, s)
	return t, e == nil
}
func age(now time.Time, value any) any {
	t, ok := parseTime(value)
	if !ok {
		return nil
	}
	return max(int64(0), now.UnixMilli()-t.UnixMilli())
}
func newest(values []any) any {
	var best time.Time
	var found any
	for _, v := range values {
		t, ok := parseTime(v)
		if ok && (found == nil || t.After(best)) {
			best = t
			found = v
		}
	}
	return found
}
func commandFor(action audit.Action) any {
	scripts := []string{}
	for _, v := range array(object(action["dashboard_metadata"])["shell_scripts"]) {
		scripts = append(scripts, stringValue(object(v)["script"]))
	}
	joined := strings.Join(scripts, "\n")
	if joined == "" {
		joined = stringValue(object(action["request_metadata"])["command_label"])
	}
	if joined == "" {
		joined = stringValue(action["operation"])
	}
	return safeText(joined, 1024)
}
func jobIDs(action audit.Action) []string {
	result := object(action["result_metadata"])
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(stringValue(result["job_id"]))
	for _, key := range []string{"jobs", "child_results"} {
		for _, v := range array(result[key]) {
			add(stringValue(object(v)["job_id"]))
		}
	}
	return out
}
func resultSummary(action audit.Action) map[string]any {
	out := map[string]any{}
	r := object(action["result_metadata"])
	for _, key := range strings.Fields("exit_code job_status jobs_count running_count all_finished all_succeeded bytes stdout_bytes stderr_bytes changed files_count matches_count operation_count succeeded_count failed_count error_code revision state") {
		switch value := r[key].(type) {
		case string:
			out[key] = safeText(value, 160)
		case float64, int, int64, json.Number, bool:
			out[key] = value
		}
	}
	return out
}
func jobProgress(job jobs.Record, now time.Time) map[string]any {
	out := map[string]any{"last_output_at": nil, "quiet_for_ms": nil, "captured_bytes": nil, "output_progress_known": false}
	stdout, e1 := os.Stat(job.StdoutPath)
	stderr, e2 := os.Stat(job.StderrPath)
	if e1 != nil || e2 != nil {
		return out
	}
	var last int64
	for _, st := range []os.FileInfo{stdout, stderr} {
		if st.Size() > 0 && st.ModTime().UnixMilli() > last {
			last = st.ModTime().UnixMilli()
		}
	}
	if last > 0 {
		out["last_output_at"] = timestamp(time.UnixMilli(last))
	}
	anchor := last
	if anchor == 0 {
		anchor = job.StartedAtMS
	}
	out["quiet_for_ms"] = max(int64(0), now.UnixMilli()-anchor)
	out["captured_bytes"] = stdout.Size() + stderr.Size()
	out["output_progress_known"] = true
	return out
}
func (h *Handler) projects() []wire.Project {
	if h.options.Resolver == nil {
		return []wire.Project{}
	}
	return h.options.Resolver.Projects()
}
func (h *Handler) project(id string) (wire.Project, bool) {
	if h.options.Resolver == nil {
		return wire.Project{}, false
	}
	return h.options.Resolver.Project(id)
}
func (h *Handler) jobProject(job jobs.Record) (wire.Project, bool) {
	if job.ProjectID != "" {
		return h.project(job.ProjectID)
	}
	for _, p := range h.projects() {
		if filepath.Clean(p.Root) == filepath.Clean(job.Root) {
			return p, true
		}
	}
	return wire.Project{}, false
}

var compactRun = regexp.MustCompile("^(run_[A-Za-z0-9_-]{8,})(?:…|\\.\\.\\.)([A-Za-z0-9_-]{4,})$")
var prefixRun = regexp.MustCompile("^run_[A-Za-z0-9_-]{8,}$")

func resolveRun(runs []map[string]any, reference string) (map[string]any, error) {
	for _, r := range runs {
		if stringValue(r["id"]) == reference {
			return r, nil
		}
	}
	compact := compactRun.FindStringSubmatch(reference)
	if compact == nil && !prefixRun.MatchString(reference) {
		return nil, nil
	}
	prefix, suffix := reference, ""
	if compact != nil {
		prefix, suffix = compact[1], compact[2]
	}
	var found map[string]any
	for _, r := range runs {
		id := stringValue(r["id"])
		if !strings.HasPrefix(id, prefix) || suffix != "" && (!strings.HasSuffix(id, suffix) || len(id) <= len(prefix)+len(suffix)) {
			continue
		}
		if found != nil {
			return nil, &HTTPError{Status: 409, Code: "ambiguous_run", Message: "Ambiguous run_id. Use a longer prefix or the full ID."}
		}
		found = r
	}
	return found, nil
}
func monitorRun(run map[string]any, unresolved int) map[string]any {
	todoCounts := map[string]any{"total": len(array(run["todos"])), "pending": 0, "in_progress": 0, "blocked": 0, "done": 0, "skipped": 0}
	pending := []map[string]any{}
	for _, v := range array(run["todos"]) {
		todo := object(v)
		status := stringValue(todo["status"])
		if n, ok := todoCounts[status].(int); ok {
			todoCounts[status] = n + 1
		}
		if status != "done" && status != "skipped" {
			pending = append(pending, map[string]any{"id": todo["id"], "title": safeText(todo["title"], 300), "status": todo["status"], "reason": safeText(todo["reason"], 300)})
		}
	}
	todoCounts["unfinished_total"] = len(pending)
	if len(pending) > 5 {
		pending = pending[:5]
	}
	todoCounts["unfinished"] = pending
	var checkpoint, completion any
	if c := object(run["checkpoint"]); len(c) > 0 {
		blockers := []any{}
		for i, v := range array(c["blockers"]) {
			if i >= 5 {
				break
			}
			blockers = append(blockers, safeText(v, 300))
		}
		checkpoint = map[string]any{"checkpoint_id": c["id"], "recorded_at": c["recorded_at"], "summary": safeText(c["summary"], 1024), "next_action": safeText(c["next_action"], 1024), "blockers": blockers, "blockers_total": len(array(c["blockers"]))}
	}
	if c := object(run["completion"]); len(c) > 0 {
		ids := array(c["evidence_ids"])
		if len(ids) > 10 {
			ids = ids[:10]
		}
		if ids == nil {
			ids = []any{}
		}
		completion = map[string]any{"completed_at": c["completed_at"], "spec_revision": c["spec_revision"], "evidence_ids": ids, "note": "Recorded acceptance result; this GET does not recheck current source."}
	}
	return map[string]any{"run_id": run["id"], "project_id": run["project_id"], "mode": run["mode"], "title": safeText(run["title"], 300), "state": run["state"], "revision": run["revision"], "updated_at": run["updated_at"], "workspace_id": first(object(run["workspace"])["id"], nil), "claimed": stringValue(run["iteration_id"]) != "", "iteration_id": first(run["iteration_id"], nil), "unresolved_operations": unresolved, "objective": safeText(run["objective"], 1024), "scope": safeText(run["scope"], 1024), "recovery_reason": safeText(run["recovery_reason"], 512), "todos": todoCounts, "checkpoint": checkpoint, "completion": completion}
}
func (h *Handler) CollectJSON(ctx context.Context, opts JSONOptions) (map[string]any, error) {
	projectID, runID := opts.ProjectID, opts.RunID
	if projectID != "" {
		if _, ok := h.project(projectID); !ok {
			return nil, &HTTPError{Status: 404, Code: "unknown_project"}
		}
	}
	if runID != "" && (projectID == "" || len(runID) > 200) {
		return nil, &HTTPError{Status: 400, Code: "invalid_run_id"}
	}
	limit, outputBytes, quietMS := opts.Limit, opts.OutputBytes, opts.QuietAfterMS
	if limit < 1 || limit > 10 || outputBytes < 0 || outputBytes > 4096 || quietMS < 30000 || quietMS > 86400000 {
		return nil, &HTTPError{Status: 400, Code: "invalid_options"}
	}
	now := h.options.Clock()
	calls := h.options.Audit.InFlight()
	allJobs := []jobs.Record{}
	if h.options.Jobs != nil {
		allJobs = h.options.Jobs.Snapshot()
	}
	runs := []map[string]any{}
	if h.options.Work != nil && h.options.Work.Runs != nil {
		var err error
		runs, err = h.options.Work.Runs(ctx, projectID)
		if err != nil {
			return nil, err
		}
	}
	var selected map[string]any
	if runID != "" {
		eligible := []map[string]any{}
		for _, r := range runs {
			if stringValue(r["project_id"]) == projectID {
				eligible = append(eligible, r)
			}
		}
		var err error
		selected, err = resolveRun(eligible, runID)
		if err != nil {
			return nil, err
		}
		if selected == nil {
			return nil, &HTTPError{Status: 404, Code: "unknown_run"}
		}
	}
	status, err := h.options.Audit.Status(ctx)
	if err != nil {
		return nil, err
	}
	remaining := 32 * 1024
	output := func(job *jobs.Record) map[string]any {
		if job == nil {
			return map[string]any{"available": false, "reason": "not_retained_or_not_a_shell_command"}
		}
		if job.OutputExpired {
			return map[string]any{"available": false, "reason": "expired"}
		}
		if outputBytes == 0 || remaining <= 0 {
			reason := "not_requested"
			if outputBytes > 0 {
				reason = "response_budget"
			}
			return map[string]any{"available": true, "included": false, "reason": reason}
		}
		budget := min(outputBytes, remaining)
		tail, readErr := h.options.Jobs.ReadTail(ctx, job.ID, job.WorkspaceID, budget/2)
		if readErr != nil {
			return map[string]any{"available": false, "reason": "unavailable"}
		}
		returned := len([]byte(tail.Stdout)) + len([]byte(tail.Stderr))
		remaining -= returned
		return map[string]any{"available": true, "included": true, "stdout": tail.Stdout, "stderr": tail.Stderr, "returned_bytes": returned, "truncated": tail.Truncated, "complete": job.Status != "running" && !tail.Truncated}
	}
	summaries := []map[string]any{}
	for _, project := range h.projects() {
		if projectID != "" && projectID != project.ID {
			continue
		}
		pcalls := []audit.InFlightAction{}
		for _, c := range calls {
			if c.ProjectID == project.ID {
				pcalls = append(pcalls, c)
			}
		}
		pjobs, active := []jobs.Record{}, []jobs.Record{}
		for _, job := range allJobs {
			p, ok := h.jobProject(job)
			if ok && p.ID == project.ID {
				pjobs = append(pjobs, job)
				if job.Status == "running" {
					active = append(active, job)
				}
			}
		}
		workRuns := []map[string]any{}
		for _, run := range runs {
			if stringValue(run["project_id"]) != project.ID || run["state"] == "complete" || run["state"] == "cancelled" {
				continue
			}
			iteration := map[string]any{}
			iterationID := stringValue(run["iteration_id"])
			if iterationID != "" && h.options.Work != nil && h.options.Work.Iteration != nil {
				iteration, err = h.options.Work.Iteration(ctx, iterationID)
				if err != nil {
					return nil, err
				}
			}
			workRuns = append(workRuns, map[string]any{"run_id": run["id"], "mode": run["mode"], "title": safeText(run["title"], 300), "revision": run["revision"], "state": run["state"], "claimed": iterationID != "", "workspace_id": object(run["workspace"])["id"], "updated_at": run["updated_at"], "last_contact_at": iteration["last_contact_at"], "last_contact_age_ms": age(now, iteration["last_contact_at"]), "last_progress_at": iteration["last_progress_at"], "last_progress_age_ms": age(now, iteration["last_progress_at"]), "recovery_reason": safeText(run["recovery_reason"], 256)})
		}
		retained, err := h.options.Audit.ListDashboard(ctx, audit.Filter{ProjectID: project.ID, Limit: limit * 3})
		if err != nil {
			return nil, err
		}
		actions := retained.Actions
		for i, k := 0, len(actions)-1; i < k; i, k = i+1, k-1 {
			actions[i], actions[k] = actions[k], actions[i]
		}
		starts, finishes := []any{}, []any{}
		for _, c := range pcalls {
			starts = append(starts, c.StartedAt)
		}
		for _, job := range pjobs {
			starts = append(starts, job.StartedAt)
			finishes = append(finishes, job.FinishedAt)
		}
		for _, a := range actions {
			starts = append(starts, a["occurred_at"])
			finishes = append(finishes, a["finished_at"])
		}
		lastStarted, lastFinished := newest(starts), newest(finishes)
		activityTimes := []any{lastStarted, lastFinished}
		progress := map[string]map[string]any{}
		for _, run := range workRuns {
			activityTimes = append(activityTimes, run["last_contact_at"])
		}
		for _, job := range active {
			progress[job.ID] = jobProgress(job, now)
			activityTimes = append(activityTimes, progress[job.ID]["last_output_at"])
		}
		lastActivity := newest(activityTimes)
		signals := []map[string]any{}
		for _, call := range pcalls {
			elapsed := age(now, call.StartedAt)
			if number(elapsed) >= float64(quietMS) {
				signals = append(signals, map[string]any{"kind": "long_running_call", "id": call.ActionID, "age_ms": elapsed})
			}
		}
		for _, job := range active {
			quiet := progress[job.ID]["quiet_for_ms"]
			if quiet != nil && number(quiet) >= float64(quietMS) {
				signals = append(signals, map[string]any{"kind": "job_output_quiet", "id": job.ID, "age_ms": quiet})
			}
			if now.UnixMilli() > job.DeadlineMS {
				signals = append(signals, map[string]any{"kind": "job_past_deadline", "id": job.ID, "age_ms": now.UnixMilli() - job.DeadlineMS})
			}
		}
		claims := 0
		for _, run := range workRuns {
			if boolean(run["claimed"]) {
				claims++
				if number(run["last_contact_age_ms"]) >= float64(quietMS) {
					signals = append(signals, map[string]any{"kind": "claim_contact_quiet", "id": run["run_id"], "age_ms": run["last_contact_age_ms"]})
				}
			}
			if run["state"] == "blocked" || run["state"] == "recovering" {
				signals = append(signals, map[string]any{"kind": "run_" + stringValue(run["state"]), "id": run["run_id"]})
			}
			if run["state"] == "ready" && !boolean(run["claimed"]) && number(age(now, run["updated_at"])) >= float64(quietMS) {
				signals = append(signals, map[string]any{"kind": "run_ready_unclaimed", "id": run["run_id"], "age_ms": age(now, run["updated_at"])})
			}
		}
		counts := map[string]any{"tool_calls": len(pcalls), "jobs": len(active), "claims": claims}
		signalCount := len(signals)
		if len(signals) > 10 {
			signals = signals[:10]
		}
		summary := map[string]any{"project_id": project.ID, "label": safeText(project.Label, 160), "url": "/activity/projects/" + url.PathEscape(project.ID) + ".json", "has_inflight_work": len(pcalls)+len(active)+claims > 0, "inflight_counts": counts, "last_command_started_at": lastStarted, "last_command_started_age_ms": age(now, lastStarted), "last_command_finished_at": lastFinished, "last_command_finished_age_ms": age(now, lastFinished), "last_activity_at": lastActivity, "last_activity_age_ms": age(now, lastActivity), "review_recommended": signalCount > 0, "signals": signals, "signals_total": signalCount}
		if projectID != "" {
			jobByID := map[string]jobs.Record{}
			for _, job := range pjobs {
				jobByID[job.ID] = job
			}
			inflightJobs := []map[string]any{}
			for i, job := range active {
				if i >= 10 {
					break
				}
				view := map[string]any{"job_id": job.ID, "workspace_id": job.WorkspaceID, "status": job.Status, "origin": job.Origin, "command": safeText(job.Command, 1024), "started_at": job.StartedAt, "elapsed_ms": age(now, job.StartedAt), "deadline_at": timestamp(time.UnixMilli(job.DeadlineMS)), "output": output(&job)}
				for k, v := range progress[job.ID] {
					view[k] = v
				}
				inflightJobs = append(inflightJobs, view)
			}
			callViews := []map[string]any{}
			for i, call := range pcalls {
				if i >= 10 {
					break
				}
				scripts := []string{}
				for _, s := range call.ShellScripts {
					scripts = append(scripts, stringValue(s["script"]))
				}
				command := strings.Join(scripts, "\n")
				if command == "" {
					command = stringValue(call.RequestMetadata["command_label"])
				}
				if command == "" {
					command = call.ToolName
				}
				callViews = append(callViews, map[string]any{"action_id": call.ActionID, "tool": call.ToolName, "state": call.State, "workspace_id": nullable(call.WorkspaceID), "started_at": call.StartedAt, "elapsed_ms": age(now, call.StartedAt), "command": safeText(command, 1024)})
			}
			recent := []map[string]any{}
			seen := map[string]bool{}
			for _, a := range actions {
				ids := jobIDs(a)
				tool := stringValue(a["tool_name"])
				if (tool == "bash" || tool == "bash_job") && len(ids) > 0 {
					allSeen := true
					for _, id := range ids {
						if !seen[id] {
							allSeen = false
						}
					}
					if allSeen {
						continue
					}
					for _, id := range ids {
						seen[id] = true
					}
				}
				scripts := []string{}
				truncated := false
				for _, s := range array(object(a["dashboard_metadata"])["shell_scripts"]) {
					scripts = append(scripts, stringValue(object(s)["script"]))
					truncated = truncated || boolean(object(s)["truncated"])
				}
				total := len(ids)
				if len(ids) > 10 {
					ids = ids[:10]
				}
				recent = append(recent, map[string]any{"action_id": a["action_id"], "tool": tool, "status": a["status"], "workspace_id": a["workspace_id"], "started_at": a["occurred_at"], "finished_at": a["finished_at"], "started_age_ms": age(now, a["occurred_at"]), "finished_age_ms": age(now, a["finished_at"]), "duration_ms": a["duration_ms"], "command": commandFor(a), "command_truncated": truncated || len([]byte(wire.Redact(strings.Join(scripts, "\n")))) > 1024, "result": resultSummary(a), "error": safeText(object(a["dashboard_metadata"])["error_message"], 256), "job_ids": ids, "job_ids_total": total})
				if len(recent) == limit {
					break
				}
			}
			represented := map[string]bool{}
			for _, r := range recent {
				for _, v := range array(r["job_ids"]) {
					represented[stringValue(v)] = true
				}
			}
			terminal := []jobs.Record{}
			for _, job := range pjobs {
				if job.Status != "running" && !represented[job.ID] {
					terminal = append(terminal, job)
				}
			}
			sort.SliceStable(terminal, func(i, k int) bool { return terminal[i].FinishedAtMS > terminal[k].FinishedAtMS })
			for i, job := range terminal {
				if i >= limit {
					break
				}
				var exit any
				if job.ExitCode != nil {
					exit = *job.ExitCode
				}
				finished := job.FinishedAtMS
				if finished == 0 {
					finished = now.UnixMilli()
				}
				recent = append(recent, map[string]any{"action_id": nil, "source": "job_store", "tool": "bash_job", "status": job.Status, "workspace_id": job.WorkspaceID, "started_at": job.StartedAt, "finished_at": nullable(job.FinishedAt), "started_age_ms": age(now, job.StartedAt), "finished_age_ms": age(now, job.FinishedAt), "duration_ms": finished - job.StartedAtMS, "command": safeText(job.Command, 1024), "command_truncated": len([]byte(wire.Redact(job.Command))) > 1024, "result": map[string]any{"exit_code": exit, "job_status": job.Status, "stop_reason": nullable(job.StopReason)}, "error": nil, "job_ids": []string{job.ID}, "job_ids_total": 1})
			}
			sort.SliceStable(recent, func(i, k int) bool {
				a, aok := parseTime(recent[i]["finished_at"])
				b, bok := parseTime(recent[k]["finished_at"])
				return aok && bok && a.After(b)
			})
			if len(recent) > limit {
				recent = recent[:limit]
			}
			for _, r := range recent {
				ids := array(r["job_ids"])
				if len(ids) > 1 {
					r["output"] = map[string]any{"included": false, "reason": "see_job_outputs"}
					outputs := []map[string]any{}
					for i, id := range ids {
						if i >= 3 {
							break
						}
						job, ok := jobByID[stringValue(id)]
						var ptr *jobs.Record
						if ok {
							ptr = &job
						}
						outputs = append(outputs, map[string]any{"job_id": id, "output": output(ptr)})
					}
					r["job_outputs"] = outputs
					r["job_outputs_truncated"] = number(r["job_ids_total"]) > 3
				} else {
					var ptr *jobs.Record
					if len(ids) == 1 {
						job, ok := jobByID[stringValue(ids[0])]
						if ok {
							ptr = &job
						}
					}
					r["output"] = output(ptr)
				}
			}
			summary["inflight"] = map[string]any{"calls": callViews, "jobs": inflightJobs, "truncated": len(pcalls) > 10 || len(active) > 10}
			summary["work_runs_total"] = len(workRuns)
			if len(workRuns) > 10 {
				workRuns = workRuns[:10]
			}
			summary["work_runs"] = workRuns
			summary["recent_commands"] = recent
		}
		summaries = append(summaries, summary)
	}
	out := map[string]any{"schema_version": 1, "generated_at": timestamp(now), "limits": map[string]any{"recent_commands": limit, "output_bytes_per_command": outputBytes, "inflight_per_kind": 10, "quiet_after_ms": quietMS}, "coverage": map[string]any{"tool_calls": "current_server_process", "jobs": "persistent_job_store", "audit_enabled": h.options.Audit.Enabled(), "recent_receipts_scanned_per_project": limit * 3, "history_incomplete": boolean(status["gap_detected"]) || number(status["malformed_records"]) > 0, "note": "Recorded CodexPro activity only. Quiet signals invite review, not automatic cancellation. Output is a bounded retained tail; requests do not acknowledge jobs or heartbeat claims."}}
	if projectID != "" {
		if len(summaries) == 0 {
			return nil, fmt.Errorf("selected project disappeared")
		}
		out["project"] = summaries[0]
	} else {
		unattributed := 0
		for _, call := range calls {
			if call.ProjectID == "" {
				unattributed++
			}
		}
		out["projects"] = summaries
		out["unattributed_inflight_calls"] = unattributed
	}
	if selected != nil {
		count := 0
		if h.options.Work != nil && h.options.Work.OperationCount != nil {
			count, err = h.options.Work.OperationCount(ctx, stringValue(selected["id"]))
			if err != nil {
				return nil, err
			}
		}
		out["run"] = monitorRun(selected, count)
	}
	return out, nil
}
