package main

import (
	"context"
	"errors"
	"testing"

	"github.com/lyeith/codexpro/go/internal/activity"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type batchWorkspaceAccessFixture struct {
	mode      string
	workspace wire.Workspace
	err       error
	calls     int
	scope     wire.Scope
	id        string
}

func (f *batchWorkspaceAccessFixture) Mode() string { return f.mode }
func (f *batchWorkspaceAccessFixture) Resolve(_ context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	f.calls++
	f.scope, f.id = scope, id
	return f.workspace, f.err
}

func TestActivityBatchDirectAttributionDoesNotResolveOwnership(t *testing.T) {
	project := wire.Project{ID: "fixture", Root: "/private/fixture"}
	for _, id := range []string{"", "arbitrary-display-label"} {
		t.Run(id, func(t *testing.T) {
			access := &batchWorkspaceAccessFixture{mode: "off", err: errors.New("must not resolve")}
			ws, err := activityBatchWorkspace(context.Background(), access, "principal", project, id)
			wantID := id
			if wantID == "" {
				wantID = "activity_fixture"
			}
			if err != nil || access.calls != 0 || ws.ID != wantID || ws.Root != project.Root || ws.ProjectID != project.ID || ws.Mode != "direct" {
				t.Fatalf("direct attribution changed: ws=%+v calls=%d err=%v", ws, access.calls, err)
			}
		})
	}
}

func TestActivityBatchManagedOwnershipStillRequired(t *testing.T) {
	project := wire.Project{ID: "fixture", Root: "/private/project"}
	owned := wire.Workspace{ID: "owned-workspace", ProjectID: project.ID, Root: "/private/managed", Mode: "mcp"}
	access := &batchWorkspaceAccessFixture{mode: "mcp", workspace: owned}
	ws, err := activityBatchWorkspace(context.Background(), access, "principal", project, owned.ID)
	if err != nil || ws != owned || access.calls != 1 || access.id != owned.ID || access.scope.PrincipalID != "principal" || access.scope.SessionID != "activity-read-only" {
		t.Fatalf("managed ownership lookup changed: ws=%+v access=%+v err=%v", ws, access, err)
	}
	for _, scenario := range []string{"missing", "wrong-project", "unowned"} {
		t.Run(scenario, func(t *testing.T) {
			access := &batchWorkspaceAccessFixture{mode: "mcp", workspace: owned}
			id := owned.ID
			switch scenario {
			case "missing":
				id = ""
			case "wrong-project":
				access.workspace.ProjectID = "other"
			case "unowned":
				access.err = errors.New("not owned")
			}
			ws, err := activityBatchWorkspace(context.Background(), access, "principal", project, id)
			if scenario != "unowned" {
				var typed *activity.HTTPError
				wantStatus, wantMessage := 400, "workspace_id is required for saved batches in MCP worktree mode."
				if scenario == "wrong-project" {
					wantStatus, wantMessage = 404, "The saved batch does not belong to this project workspace."
				}
				if !errors.As(err, &typed) || typed.Status != wantStatus || typed.Message != wantMessage {
					t.Fatalf("route-specific error changed: %#v", err)
				}
			}
			if err == nil || ws != (wire.Workspace{}) || (id == "" && access.calls != 0) {
				t.Fatalf("unowned workspace was accepted: ws=%+v calls=%d err=%v", ws, access.calls, err)
			}
		})
	}
}
