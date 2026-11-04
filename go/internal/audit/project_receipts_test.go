package audit

import (
	"context"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestProjectReceiptsFilterBeforeLimitAndOmitOperatorFields(t *testing.T) {
	j := openTest(t, testOptions(t.TempDir()))
	save := func(project, workspace, request string) Receipt {
		in := recordInput(project, request)
		in.Scope.Identity = wire.Identity{Source: "named", CredentialID: "private-device"}
		in.Target = &Target{Scope: "workspace", ProjectID: project, WorkspaceID: workspace}
		in.DashboardMetadata = map[string]any{"script": "private command"}
		return writeTest(t, j, in)
	}
	first := save("alpha", "own", "1")
	save("alpha", "foreign-lease", "2")
	save("beta", "other", "3")
	ids := map[string]bool{"own": true}
	actions, err := j.ProjectReceipts(context.Background(), "alpha", "", ids, 1)
	if err != nil || len(actions) != 1 || actions[0]["action_id"] != first.ActionID {
		t.Fatalf("visible receipt displaced: %#v, %v", actions, err)
	}
	action := actions[0]
	if len(action) != 5 || action["project_id"] != "alpha" || action["tool_name"] != "read" || action["status"] == nil || action["occurred_at"] == nil {
		t.Fatalf("unexpected projection: %#v", action)
	}
	for _, input := range []Action{
		{"tool_name": "open_workspace", "project_id": "alpha", "workspace_id": "own"},
		{"tool_name": "read", "project_id": "alpha", "workspace_id": "foreign-lease"},
		{"tool_name": "read", "project_id": "alpha", "workspace_id": "own", "audit_scope": "unattributed"},
		{"tool_name": "read", "workspace_id": "own"},
	} {
		if ProjectReceiptVisible(input, ids) {
			t.Fatalf("ambiguous or unowned receipt admitted: %#v", input)
		}
	}
	if !ProjectReceiptVisible(Action{"tool_name": "read", "project_id": "alpha", "workspace_id": "own"}, ids) {
		t.Fatal("historical single-workspace read disappeared")
	}
}
