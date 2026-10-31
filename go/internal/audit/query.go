package audit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Enabled is immutable for the lifetime of a journal.
func (j *Journal) Enabled() bool { return j.options.Enabled }
func (j *Journal) mode() string {
	if j.options.Enabled {
		return "metadata"
	}
	return "off"
}
func cursorFloor(entries []entry) int64 {
	if len(entries) == 0 {
		return 0
	}
	n := len(entries) - 1
	for n > 0 && entries[n-1].Sequence+1 == entries[n].Sequence {
		n--
	}
	return entries[n].Sequence - 1
}
func (j *Journal) List(ctx context.Context, filter Filter) (ListResult, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.listLocked(ctx, filter, false)
}

// ListDashboard is an authenticated, in-process projection. It includes the
// bounded script/diagnostic metadata deliberately excluded from public tools.
func (j *Journal) ListDashboard(ctx context.Context, filter Filter) (ListResult, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.listLocked(ctx, filter, true)
}
func (j *Journal) listLocked(ctx context.Context, f Filter, trusted bool) (ListResult, error) {
	out := ListResult{Enabled: j.options.Enabled, Mode: j.mode(), Namespace: "debug", SchemaVersion: "1.0", Actions: []Action{}, MalformedRecords: j.state.Malformed, GapDetected: j.state.Gap}
	if !j.options.Enabled {
		return out, nil
	}
	out.LatestSequence = j.state.Latest
	out.NextSequence = j.state.Latest
	if len(j.entries) > 0 {
		out.EarliestSequence = j.entries[0].Sequence
	}
	after := f.AfterSequence
	if after != nil {
		requested := *after
		if requested < 0 {
			requested = 0
		}
		floor := cursorFloor(j.entries)
		reason, message := "", ""
		switch {
		case j.state.Gap:
			reason = "journal_gap"
			message = "Action-journal gap detected; forward cursor reads are disabled until the source is reconciled."
		case requested > j.state.Latest:
			reason = "cursor_ahead"
			message = fmt.Sprintf("after_sequence %d is beyond the latest action sequence %d", requested, j.state.Latest)
		case requested < floor:
			reason = "expired"
			if floor > out.EarliestSequence-1 {
				message = fmt.Sprintf("after_sequence %d expired because per-project retention has planned gaps before the safe cursor %d; the oldest safe forward cursor is %d", requested, floor, floor)
			} else {
				message = fmt.Sprintf("after_sequence %d expired because retention dropped actions through sequence %d; the earliest retained action sequence is %d", requested, max(int64(0), out.EarliestSequence-1), out.EarliestSequence)
			}
		}
		if reason != "" {
			if !f.CursorRecovery {
				return out, errors.New(message)
			}
			var safe any = floor
			if j.state.Gap {
				safe = nil
			}
			out.CursorRecovery = map[string]any{"reason": reason, "requested_after_sequence": requested, "oldest_safe_forward_cursor": safe, "mode": "recent_retained"}
			after = nil
		}
	}
	if len(j.entries) == 0 {
		return out, nil
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	maxLimit := 500
	if trusted {
		maxLimit = 100000
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	read := func(e entry) (Action, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := files[e.Segment]
		if file == nil {
			var err error
			file, err = os.Open(filepath.Join(j.options.Directory, e.Segment))
			if err != nil {
				return nil, err
			}
			files[e.Segment] = file
		}
		if e.Length < 1 || e.Length > 131072 {
			return nil, errors.New("invalid audit entry length")
		}
		body := make([]byte, int(e.Length))
		if _, err := file.ReadAt(body, e.Offset); err != nil {
			return nil, err
		}
		a, err := parseAction(body)
		if err != nil {
			return nil, err
		}
		if int64(number(a["sequence"])) != e.Sequence || a["action_id"] != e.ActionID {
			return nil, errors.New("audit index points to a different record")
		}
		if !trusted {
			delete(a, "dashboard_metadata")
		}
		return a, nil
	}
	matches := func(a Action) bool {
		if f.MutatingOnly && !boolValue(a["mutating"]) {
			return false
		}
		for _, pair := range [][2]string{{"tool_name", f.ToolName}, {"operation_class", f.OperationClass}, {"status", f.Status}, {"project_id", f.ProjectID}, {"workspace_id", f.WorkspaceID}} {
			if pair[1] != "" && text(a[pair[0]]) != pair[1] {
				return false
			}
		}
		return true
	}
	if after != nil {
		out.NextSequence = *after
		if out.NextSequence < 0 {
			out.NextSequence = 0
		}
		start := sort.Search(len(j.entries), func(i int) bool { return j.entries[i].Sequence > out.NextSequence })
		for i := start; i < len(j.entries); i++ {
			e := j.entries[i]
			out.NextSequence = e.Sequence
			if f.ProjectID != "" && e.ProjectID != f.ProjectID {
				continue
			}
			action, err := read(e)
			if err != nil {
				return out, err
			}
			if matches(action) {
				out.Actions = append(out.Actions, action)
			}
			if len(out.Actions) >= limit {
				out.HasMore = i < len(j.entries)-1
				break
			}
		}
	} else {
		out.NextSequence = j.entries[len(j.entries)-1].Sequence
		for i := len(j.entries) - 1; i >= 0 && len(out.Actions) < limit; i-- {
			e := j.entries[i]
			if f.ProjectID != "" && e.ProjectID != f.ProjectID {
				continue
			}
			action, err := read(e)
			if err != nil {
				return out, err
			}
			if matches(action) {
				out.Actions = append(out.Actions, action)
			}
		}
		for a, b := 0, len(out.Actions)-1; a < b; a, b = a+1, b-1 {
			out.Actions[a], out.Actions[b] = out.Actions[b], out.Actions[a]
		}
	}
	return out, nil
}
func (j *Journal) Get(ctx context.Context, id string) (Action, bool, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !j.options.Enabled {
		return nil, false, nil
	}
	e, ok := j.byID[id]
	if !ok {
		return nil, false, nil
	}
	a, err := j.readEntry(e)
	if err != nil {
		return nil, false, err
	}
	return public(a), true, nil
}
func (j *Journal) GetDashboard(ctx context.Context, id string) (Action, bool, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	e, ok := j.byID[id]
	if !ok {
		return nil, false, nil
	}
	a, err := j.readEntry(e)
	return a, err == nil, err
}
func (j *Journal) Status(ctx context.Context) (map[string]any, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	earliest := int64(0)
	if len(j.entries) > 0 {
		earliest = j.entries[0].Sequence
	}
	floor := cursorFloor(j.entries)
	var storage int64
	if j.options.Enabled {
		for _, pattern := range []string{"segment-*.jsonl", "compact-*.jsonl"} {
			files, err := filepath.Glob(filepath.Join(j.options.Directory, pattern))
			if err != nil {
				return nil, err
			}
			for _, name := range files {
				st, err := os.Stat(name)
				if err != nil {
					return nil, err
				}
				storage += st.Size()
			}
		}
	}
	var gaps int64
	if len(j.entries) > 0 {
		gaps = j.state.Latest - earliest + 1 - int64(len(j.entries))
		if gaps < 0 {
			gaps = 0
		}
	}
	limits := j.currentLimits()
	retention := map[string]any{"max_bytes": limits.maxBytes, "retain_actions": limits.retainActions, "retain_actions_per_project": limits.retainActions, "rotation_count": j.state.Rotation, "dropped_through_sequence": max(int64(0), earliest-1), "cursor_floor_sequence": floor, "planned_gap_count": gaps}
	if j.state.CompactedAt != "" {
		retention["compacted_at"] = j.state.CompactedAt
	}
	return map[string]any{"enabled": j.options.Enabled, "mode": j.mode(), "namespace": "debug", "schema_version": "1.0", "storage_format": "jsonl", "journal_ref": "codexpro://actions", "retained_from_sequence": earliest, "latest_sequence": j.state.Latest, "next_sequence": j.state.Latest + 1, "action_count": len(j.entries), "malformed_records": j.state.Malformed, "gap_detected": j.state.Gap, "storage_bytes": storage, "deduplicated_requests": j.deduplicated, "retention": retention}, nil
}

const disabledMessage = "CodexPro metadata auditing is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata."

func filterArgs(args map[string]any) Filter {
	f := Filter{Limit: wire.Int(args, "limit", 100), MutatingOnly: wire.Bool(args, "mutating_only", false), ToolName: wire.String(args, "tool_name", ""), OperationClass: wire.String(args, "operation_class", ""), Status: wire.String(args, "status", ""), ProjectID: wire.String(args, "project_id", ""), WorkspaceID: wire.String(args, "workspace_id", "")}
	if _, ok := args["after_sequence"]; ok {
		seq := wire.Int64(args, "after_sequence", 0)
		f.AfterSequence = &seq
	}
	return f
}
func (j *Journal) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	switch name {
	case "activity_status":
		status, err := j.Status(ctx)
		if err != nil {
			return wire.Result{}, err
		}
		summary := disabledMessage
		if j.Enabled() {
			summary = fmt.Sprintf("retained_from_sequence=%v; latest_sequence=%v; next_sequence=%v; gap_detected=%v.", status["retained_from_sequence"], status["latest_sequence"], status["next_sequence"], status["gap_detected"])
		}
		return wire.Success(name, "CodexPro Debug Activity Status", status, "# CodexPro Debug Activity Status\n\n"+summary), nil
	case "activity_list":
		result, err := j.List(ctx, filterArgs(args))
		if err != nil {
			return wire.Result{}, err
		}
		summary := disabledMessage
		if result.Enabled {
			summary = fmt.Sprintf("%d action(s); next_sequence=%d; earliest_sequence=%d; latest_sequence=%d; has_more=%t.", len(result.Actions), result.NextSequence, result.EarliestSequence, result.LatestSequence, result.HasMore)
		}
		return wire.Success(name, "List CodexPro Debug Activity", toMap(result), "# CodexPro Debug Activity\n\n"+summary), nil
	case "activity_get":
		data := map[string]any{"enabled": j.Enabled(), "mode": j.mode(), "namespace": "debug", "schema_version": "1.0", "action": nil}
		body := "# CodexPro Debug Action\n\nCodexPro debug activity is disabled. Set CODEXPRO_AUDIT_MODE=metadata or start with --audit metadata."
		if j.Enabled() {
			id := wire.String(args, "action_id", "")
			a, ok, err := j.Get(ctx, id)
			if err != nil {
				return wire.Result{}, err
			}
			if !ok {
				return wire.Result{}, wire.Errorf("INVALID_ARGUMENT", "Unknown CodexPro action_id: %s.", id)
			}
			data["action"] = a
			body = fmt.Sprintf("# CodexPro Debug Action\n\n%v · %s · %s", a["sequence"], text(a["tool_name"]), text(a["status"]))
		}
		return wire.Success(name, "Get CodexPro Debug Action", data, body), nil
	case "activity_export":
		return j.Export(ctx, filterArgs(args), wire.String(args, "format", "jsonl"))
	default:
		return wire.Result{}, wire.Errorf("INVALID_ARGUMENT", "Unknown activity tool: %s.", name)
	}
}
func (j *Journal) Export(ctx context.Context, f Filter, format string) (wire.Result, error) {
	if format != "json" && format != "jsonl" {
		return wire.Result{}, wire.Errorf("INVALID_ARGUMENT", "Invalid activity export format.")
	}
	if !j.Enabled() {
		return wire.Success("activity_export", "Export CodexPro Debug Activity", map[string]any{"enabled": false, "mode": "off", "namespace": "debug", "schema_version": "1.0", "export_format": format, "export_bytes": 0, "action_count": 0}, disabledMessage), nil
	}
	activity, err := j.List(ctx, f)
	if err != nil {
		return wire.Result{}, err
	}
	budget := j.currentLimits().maxOutputBytes * 3 / 4
	if budget > 100000 {
		budget = 100000
	}
	if budget < 1024 {
		budget = 1024
	}
	included := []Action{}
	exportText := ""
	if format == "json" {
		exportText = "[]\n"
	}
	for _, a := range activity.Actions {
		candidate := append(append([]Action(nil), included...), a)
		var data []byte
		if format == "json" {
			data, err = encode(candidate)
		} else {
			var b bytes.Buffer
			for _, a := range candidate {
				line, e := encode(a)
				if e != nil {
					return wire.Result{}, e
				}
				b.Write(line)
			}
			data = b.Bytes()
		}
		if err != nil {
			return wire.Result{}, err
		}
		if len(data) > budget {
			if len(included) == 0 {
				return wire.Result{}, wire.Errorf("INVALID_ARGUMENT", "The first matching action exceeds the %d-byte activity export budget. Narrow the filters or raise CODEXPRO_MAX_OUTPUT_BYTES.", budget)
			}
			break
		}
		included = candidate
		exportText = string(data)
	}
	safe := j.options.Redact(exportText)
	truncated := len(included) < len(activity.Actions)
	next := activity.NextSequence
	if len(included) > 0 {
		next = int64(number(included[len(included)-1]["sequence"]))
	}
	data := map[string]any{"enabled": true, "mode": "metadata", "namespace": "debug", "schema_version": "1.0", "export_format": format, "export_bytes": len([]byte(safe)), "export_sha256": digest(safe), "action_count": len(included), "next_sequence": next, "earliest_sequence": activity.EarliestSequence, "latest_sequence": activity.LatestSequence, "has_more": truncated || activity.HasMore, "truncated_by_bytes": truncated, "malformed_records": activity.MalformedRecords, "gap_detected": activity.GapDetected}
	// Enabled exports return a raw content envelope in the release handler;
	// unlike textResult-backed status/list/get, they do not contain _meta:{}.
	result := wire.Success("activity_export", "Export CodexPro Debug Activity", data, safe)
	result.Meta = nil
	return result, nil
}
