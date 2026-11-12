package audit

import (
	"context"
	"slices"
)

// ProjectReceiptVisible checks the stored workspace against IDs resolved for
// the caller's granted project. Old global and mixed-project records have no
// single resource to publish.
func ProjectReceiptVisible(action Action, workspaces map[string]bool) bool {
	if text(action["project_id"]) == "" {
		return false
	}
	ws := text(action["workspace_id"])
	switch text(action["audit_scope"]) {
	case "project":
		return ws == ""
	case "workspace":
		return ws != "" && workspaces[ws]
	case "":
		// Before explicit workspace targets, these tools took one workspace
		// and had no caller-selected project argument.
		switch text(action["tool_name"]) {
		case "tree", "read", "view_image", "search", "write", "edit", "apply_patch", "show_changes", "commit_changes", "bash", "start_jobs", "queue_jobs":
			return ws != "" && workspaces[ws]
		}
	}
	return false
}

func ProjectReceipt(action Action) Action {
	return Action{"action_id": action["action_id"], "project_id": action["project_id"],
		"occurred_at": action["occurred_at"], "tool_name": action["tool_name"], "status": action["status"]}
}

// HasMore is nil when the bounded scan cannot establish whether another
// visible receipt exists. The response never includes the operator sequence.
type ProjectReceiptPage struct {
	Actions []Action
	HasMore *bool
}

func (j *Journal) ProjectReceipts(ctx context.Context, project, workspace string, ids map[string]bool, limit int) (ProjectReceiptPage, error) {
	return j.projectReceiptPage(ctx, project, workspace, ids, limit, 512, 4<<20)
}

func (j *Journal) projectReceiptPage(ctx context.Context, project, workspace string, ids map[string]bool, limit, scanLimit int, byteBudget int64) (ProjectReceiptPage, error) {
	hasMore := false
	page := ProjectReceiptPage{Actions: []Action{}, HasMore: &hasMore}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if !j.Enabled() || project == "" || ids == nil {
		return page, nil
	}
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)

	// The (project_id,sequence) index bounds the work even when unrelated
	// projects dominate the journal. Release the sole DB connection before
	// acquiring a journal read lock: writers acquire those in the other order.
	candidates, err := j.projectCandidateIDs(ctx, project, scanLimit+1)
	if err != nil {
		return page, err
	}
	for i, id := range candidates {
		if i == scanLimit {
			page.HasMore = nil
			break
		}
		action, incomplete, err := j.readProjectCandidate(ctx, project, id, &byteBudget)
		if err != nil {
			return page, err
		}
		if incomplete {
			page.HasMore = nil
			break
		}
		if text(action["project_id"]) != project || (workspace != "" && text(action["workspace_id"]) != workspace) || !ProjectReceiptVisible(action, ids) {
			continue
		}
		if len(page.Actions) == limit {
			hasMore = true
			break
		}
		page.Actions = append(page.Actions, ProjectReceipt(action))
	}
	slices.Reverse(page.Actions)
	return page, nil
}

func (j *Journal) projectCandidateIDs(ctx context.Context, project string, limit int) ([]string, error) {
	rows, err := j.db.QueryContext(ctx, "SELECT action_id FROM entries INDEXED BY entries_project_sequence WHERE project_id=? ORDER BY sequence DESC LIMIT ?", project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Pin the current segment location for one bounded read. Retention may have
// dropped a candidate since the index query, in which case completeness is unknown.
func (j *Journal) readProjectCandidate(ctx context.Context, project, id string, remaining *int64) (Action, bool, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	e, ok := j.byID[id]
	if !ok || e.ProjectID != project || e.Length > *remaining {
		return nil, true, nil
	}
	action, err := j.readEntry(e)
	*remaining -= e.Length
	return action, false, err
}
