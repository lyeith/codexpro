package audit

import (
	"context"
	"encoding/json"
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
	page, err := j.ProjectReceipts(context.Background(), "alpha", "", ids, 1)
	actions := page.Actions
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

func TestProjectReceiptPageBoundsProjectWorkAndReportsVisibleMore(t *testing.T) {
	j := openTest(t, testOptions(t.TempDir()))
	write := func(project, ws, request string) Receipt {
		in := recordInput(project, request)
		in.Target = &Target{Scope: "workspace", ProjectID: project, WorkspaceID: ws}
		return writeTest(t, j, in)
	}
	first := write("alpha", "own", "1")
	second := write("alpha", "own", "2")
	hidden := write("alpha", "other", "3")
	write("beta", "own", "4")
	ids := map[string]bool{"own": true, "other": true}
	for _, tc := range []struct {
		name    string
		limit   int
		scan    int
		bytes   int64
		wantIDs []string
		more    string
	}{
		{"extra visible", 1, 3, 4 << 20, []string{second.ActionID}, "true"},
		{"exactly full", 2, 3, 4 << 20, []string{first.ActionID, second.ActionID}, "false"},
		{"count budget even when full", 1, 2, 4 << 20, []string{second.ActionID}, "null"},
		{"hidden consumes bytes", 2, 3, j.byID[hidden.ActionID].Length, nil, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := j.projectReceiptPage(context.Background(), "alpha", "own", ids, tc.limit, tc.scan, tc.bytes)
			if err != nil {
				t.Fatal(err)
			}
			more, err := json.Marshal(page.HasMore)
			if err != nil || string(more) != tc.more || len(page.Actions) != len(tc.wantIDs) {
				t.Fatalf("actions=%v has_more=%s err=%v", page.Actions, more, err)
			}
			for i, id := range tc.wantIDs {
				if page.Actions[i]["action_id"] != id || len(page.Actions[i]) != 5 {
					t.Fatalf("receipt %d: %#v", i, page.Actions[i])
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.ProjectReceipts(ctx, "alpha", "", ids, 1); err != context.Canceled {
		t.Fatalf("cancelled scan: %v", err)
	}
	disabled := openTest(t, Options{Enabled: false})
	page, err := disabled.ProjectReceipts(context.Background(), "alpha", "", ids, 1)
	if err != nil || len(page.Actions) != 0 || page.HasMore == nil || *page.HasMore {
		t.Fatalf("disabled journal: %#v %v", page, err)
	}
}
