package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func auditTargetFixture(t *testing.T) (*Engine, wire.Scope, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "project")
	state := filepath.Join(base, "state")
	for _, path := range []string{root, state} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New(state)
	data, err := json.Marshal(workspace.Catalog{Version: 1, DefaultProject: "test", Projects: []wire.Project{{ID: "test", Label: "Test", Root: root, BaseRef: "HEAD"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.ProjectsFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return &Engine{Config: cfg, Workspaces: manager}, wire.Scope{PrincipalID: "audit-principal", SessionID: "audit-session"}, "ws_" + workspace.Hash(root)[:24]
}

func TestAuditScopePreservesOrdinaryOmission(t *testing.T) {
	e, scope, id := auditTargetFixture(t)
	for _, name := range []string{"read", "write", "edit", "apply_patch", "bash", "start_jobs", "jobs", "stop_jobs", "batch", "open_workspace", "open_current_workspace", "codexpro_self_test"} {
		result := wire.Result{StructuredContent: map[string]any{"workspace_id": id, "project_id": "test"}}
		ws, target := e.target(context.Background(), scope, name, map[string]any{"workspace_id": id}, result)
		if ws == nil || ws.ID != id || target != nil {
			t.Fatalf("%s changed ordinary identity or audit_scope omission: workspace=%#v target=%#v", name, ws, target)
		}
	}
}

func TestAuditDoesNotOpenWorkspaceBeforeFirstOpen(t *testing.T) {
	e, scope, id := auditTargetFixture(t)
	for _, name := range []string{"open_workspace", "open_current_workspace"} {
		ws, target := e.admissionTarget(context.Background(), scope, name, map[string]any{"project_id": "test"})
		if ws != nil || target != nil || e.Workspaces.WasOpen(scope, id) {
			t.Fatal("observing open changed its initial session state")
		}
		if got := e.admissionIdentity(name, map[string]any{"project_id": "test"}, ws)["project_id"]; got != "test" {
			t.Fatalf("first-open admission lost project identity: %v", got)
		}
	}
}

func TestAuditLifecycleAttributionUsesOnlyVerifiedIdentity(t *testing.T) {
	e, scope, id := auditTargetFixture(t)
	cases := []struct {
		name                      string
		args                      map[string]any
		result                    map[string]any
		scope, project, workspace string
	}{
		{"server_config", map[string]any{}, nil, "server", "", ""},
		{"create_project", map[string]any{"project_id": "test"}, nil, "project", "test", ""},
		{"remove_workspace", map[string]any{"workspace_id": "unknown"}, nil, "server", "", ""},
		{"create_workspace", map[string]any{}, map[string]any{"workspace_id": id, "project_id": "test"}, "workspace", "test", id},
		{"release_workspace", map[string]any{"workspace_id": "unknown"}, map[string]any{"workspace_id": id}, "workspace", "test", id},
	}
	for _, item := range cases {
		_, target := e.target(context.Background(), scope, item.name, item.args, wire.Result{StructuredContent: item.result})
		if target == nil || target.Scope != item.scope || target.ProjectID != item.project || target.WorkspaceID != item.workspace {
			t.Fatalf("%s attribution: %#v", item.name, target)
		}
	}
}

func TestCancelledRequestKeepsVerifiedAuditIdentity(t *testing.T) {
	e, scope, id := auditTargetFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ws, target := e.target(ctx, scope, "release_workspace", map[string]any{"workspace_id": id}, wire.Result{})
	if ws == nil || ws.ID != id || target == nil || target.Scope != "workspace" || target.WorkspaceID != id {
		t.Fatalf("cancellation erased verified identity: %#v %#v", ws, target)
	}
	if before := e.admissionIdentity("open_workspace", map[string]any{"project_ids": []any{}}, nil); before["project_id"] != nil {
		t.Fatal("empty source project array fell back to the default")
	}
}

type auditHistoryFixture struct{ target *audit.Target }

func (h auditHistoryFixture) Call(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
	return wire.Result{}, nil
}
func (h auditHistoryFixture) AuditTarget(context.Context, wire.Scope, map[string]any, wire.Result) *audit.Target {
	return h.target
}

func TestAuditHistorySeparatesOwnedRunsFromConfiguredProjects(t *testing.T) {
	e, scope, _ := auditTargetFixture(t)
	e.WorkHistory = auditHistoryFixture{}
	for _, item := range []struct {
		args map[string]any
		want string
	}{{map[string]any{"action": "list"}, "server"}, {map[string]any{"action": "list", "project_id": "test"}, "project"}, {map[string]any{"action": "list", "project_id": "archived-only"}, "unattributed"}, {map[string]any{"action": "get", "run_id": "inaccessible"}, "unattributed"}} {
		ws, target := e.target(context.Background(), scope, "work_status", item.args, wire.Result{})
		if ws != nil || target == nil || target.Scope != item.want || target.RunID != "" {
			t.Fatalf("history fallback leaked identity: %#v %#v", ws, target)
		}
	}
	owned := &audit.Target{Scope: "project", ProjectID: "archived-only", RunID: "owned-run"}
	e.WorkHistory = auditHistoryFixture{owned}
	ws, target := e.target(context.Background(), scope, "work_status", map[string]any{"action": "get", "run_id": "owned"}, wire.Result{})
	if ws != nil || target != owned {
		t.Fatal("verified archived identity was replaced by current source identity")
	}
}

func TestCheckpointMakesReadOnlyBatchAnAuditedMutation(t *testing.T) {
	read := map[string]any{"operations": []any{map[string]any{"tool": "read", "args": map[string]any{"path": "README.md"}}}}
	if Mutating("batch", read) {
		t.Fatal("read-only batch changed classification")
	}
	for _, checkpoint := range []map[string]any{{}, {"label": "checkpoint"}} {
		read["checkpoint"] = checkpoint
		if !Mutating("batch", read) {
			t.Fatal("persisted checkpoint was classified as read-only")
		}
	}
}
