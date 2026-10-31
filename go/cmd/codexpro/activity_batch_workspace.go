package main

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/activity"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type activityBatchWorkspaceAccess interface {
	Mode() string
	Resolve(context.Context, wire.Scope, string) (wire.Workspace, error)
}

// Direct-mode batch viewing reads the selected project root. Its workspace ID
// is display attribution, as in the TypeScript HTTP route, not an ownership
// lookup. MCP worktree mode still requires the current principal's workspace.
func activityBatchWorkspace(ctx context.Context, access activityBatchWorkspaceAccess, principal string, project wire.Project, id string) (wire.Workspace, error) {
	if access.Mode() != "mcp" {
		if id == "" {
			id = "activity_" + project.ID
		}
		return wire.Workspace{ID: id, ProjectID: project.ID, Root: project.Root, Mode: "direct"}, nil
	}
	if id == "" {
		return wire.Workspace{}, &activity.HTTPError{Status: 400, Message: "workspace_id is required for saved batches in MCP worktree mode."}
	}
	ws, err := access.Resolve(ctx, wire.Scope{PrincipalID: principal, SessionID: "activity-read-only"}, id)
	if err != nil {
		return wire.Workspace{}, err
	}
	if ws.ProjectID != "" && ws.ProjectID != project.ID {
		return wire.Workspace{}, &activity.HTTPError{Status: 404, Message: "The saved batch does not belong to this project workspace."}
	}
	return ws, nil
}
