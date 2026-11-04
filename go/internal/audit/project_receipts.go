package audit

import "context"

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
		case "tree", "read", "view_image", "search", "write", "edit", "apply_patch", "show_changes", "commit_changes", "bash", "start_jobs":
			return ws != "" && workspaces[ws]
		}
	}
	return false
}

func ProjectReceipt(action Action) Action {
	return Action{"action_id": action["action_id"], "project_id": action["project_id"],
		"occurred_at": action["occurred_at"], "tool_name": action["tool_name"], "status": action["status"]}
}

// The visibility predicate runs before the limit. Never expose the operator
// journal's sequence, global coverage, paths, actors or command output.
func (j *Journal) ProjectReceipts(ctx context.Context, project, workspace string, ids map[string]bool, limit int) ([]Action, error) {
	if project == "" || ids == nil {
		return []Action{}, nil
	}
	result, err := j.List(ctx, Filter{ProjectID: project, WorkspaceID: workspace, Limit: limit, readWorkspaces: ids})
	if err != nil {
		return nil, err
	}
	for i, action := range result.Actions {
		result.Actions[i] = ProjectReceipt(action)
	}
	return result.Actions, nil
}
