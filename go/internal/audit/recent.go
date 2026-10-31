package audit

import "context"

const RecentActivityCoverage = "Recorded CodexPro activity only; the last-change scan covers at most 500 actions. Launch is not completion. External edits, older changes and expired records may be missing; inspect current source. A different worktree's changes may not be present here."

// RecentActivity is the shared read-only workspace/run briefing. Last-change
// evidence comes from the committed cache; only the bounded recent action page
// is read from retained segments. It never resolves a workspace or starts Git.
func (j *Journal) RecentActivity(ctx context.Context, projectID, workspaceID string, after ...*int64) (map[string]any, error) {
	var cursor *int64
	if len(after) > 0 {
		cursor = after[0]
	}
	changes, err := j.List(ctx, Filter{ProjectID: projectID, AfterSequence: cursor, CursorRecovery: true, Limit: 10, MutatingOnly: true})
	if err != nil {
		return nil, err
	}
	recent, err := j.List(ctx, Filter{ProjectID: projectID, Limit: 1, MutatingOnly: true})
	if err != nil {
		return nil, err
	}
	last, err := j.LastRecordedProjectChange(ctx, projectID)
	if err != nil {
		return nil, err
	}
	actions := []map[string]any{}
	for _, action := range changes.Actions {
		paths := array(action["changed_paths"])
		if paths == nil {
			paths = []any{}
		}
		if len(paths) > 10 {
			paths = paths[:10]
		}
		attribution := "other or unattributed project work"
		if text(action["workspace_id"]) == workspaceID {
			attribution = "this workspace; see actor/operation evidence"
		}
		item := map[string]any{"action_id": action["action_id"], "sequence": action["sequence"], "at": action["finished_at"], "tool": action["tool_name"], "status": action["status"], "actor_ref": action["actor_ref"], "changed_paths": paths, "changed_path_count": action["changed_path_count"], "attribution": attribution}
		for _, key := range []string{"workspace_id", "work"} {
			if value, ok := action[key]; ok {
				item[key] = value
			}
		}
		actions = append(actions, item)
	}
	var latest, lastValue any
	if len(recent.Actions) > 0 {
		latest = recent.Actions[len(recent.Actions)-1]["finished_at"]
	}
	if last != nil {
		lastValue = last
	}
	out := map[string]any{"enabled": changes.Enabled, "last_recorded_project_change": lastValue, "latest_recorded_activity": latest, "actions": actions, "latest_sequence": changes.LatestSequence, "next_sequence": changes.NextSequence, "earliest_sequence": changes.EarliestSequence, "has_more": changes.HasMore, "gap_detected": changes.GapDetected || changes.CursorRecovery != nil, "coverage": RecentActivityCoverage}
	if changes.CursorRecovery != nil || changes.GapDetected {
		warning := map[string]any{}
		if changes.CursorRecovery == nil {
			warning["reason"] = "journal_gap"
			warning["mode"] = "recent_retained"
			warning["oldest_safe_forward_cursor"] = nil
		} else {
			for key, value := range changes.CursorRecovery {
				warning[key] = value
			}
		}
		warning["guidance"] = "Activity history is incomplete. This is a recent retained snapshot, not a complete replay since the checkpoint. Inspect current source, the saved handoff and durable operation/job receipts before continuing. Missing activity is not evidence that work did not happen."
		out["warning"] = warning
	}
	return out, nil
}
