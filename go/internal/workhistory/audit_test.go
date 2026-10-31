package workhistory

import (
	"context"
	"reflect"
	"testing"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func auditTargetFixture() *Service {
	runs := []map[string]any{
		{"id": "run_abcdefgh1111", "principal_id": "archived_owner", "project_id": "plans", "workspace": map[string]any{"id": "ws_historical", "root": "/do-not-resolve-this"}},
		{"id": "run_abcdefgh2222", "principal_id": "archived_owner", "project_id": "anima"},
		{"id": "run_ijklmnop3333", "principal_id": "another_owner", "project_id": "private_project", "workspace": map[string]any{"id": "ws_private"}},
		{"id": "run_qrstuvwx4444", "principal_id": "archived_owner", "project_id": "plans", "workspace": map[string]any{}},
	}
	s := &Service{options: Options{PrincipalMap: map[string]string{"current_owner": "archived_owner"}}, runs: runs, byID: map[string]map[string]any{}}
	for _, run := range runs {
		s.byID[str(run["id"])] = run
	}
	return s
}

func TestArchivedAuditTargetPreservesOwnershipAndReferenceRules(t *testing.T) {
	s := auditTargetFixture()
	cases := []struct {
		name      string
		principal string
		args      map[string]any
		result    map[string]any
		want      *audit.Target
	}{
		{"mapped owner exact ID overrides project filter", "current_owner", map[string]any{"action": "get", "run_id": "run_abcdefgh1111", "project_id": "wrong"}, nil, &audit.Target{Scope: "workspace", ProjectID: "plans", WorkspaceID: "ws_historical", RunID: "run_abcdefgh1111"}},
		{"exact archived principal", "archived_owner", map[string]any{"action": "get", "run_id": "run_abcdefgh2222"}, nil, &audit.Target{Scope: "project", ProjectID: "anima", RunID: "run_abcdefgh2222"}},
		{"unique prefix uses project filter", "current_owner", map[string]any{"action": "get", "run_id": "run_abcdefgh", "project_id": "plans"}, nil, &audit.Target{Scope: "workspace", ProjectID: "plans", WorkspaceID: "ws_historical", RunID: "run_abcdefgh1111"}},
		{"ambiguous prefix leaks no identity", "current_owner", map[string]any{"action": "get", "run_id": "run_abcdefgh"}, nil, &audit.Target{Scope: "unattributed"}},
		{"inaccessible exact ID cannot borrow successful result", "current_owner", map[string]any{"action": "get", "run_id": "run_ijklmnop3333"}, map[string]any{"run_id": "run_abcdefgh1111"}, &audit.Target{Scope: "unattributed"}},
		{"unknown exact ID leaks no input project", "current_owner", map[string]any{"action": "get", "run_id": "run_missing0000", "project_id": "private_project"}, nil, &audit.Target{Scope: "unattributed"}},
		{"empty reference is still a failed run lookup", "current_owner", map[string]any{"action": "get", "run_id": ""}, nil, &audit.Target{Scope: "unattributed"}},
		{"missing principal cannot inherit ownership", "", map[string]any{"action": "get", "run_id": "run_abcdefgh1111"}, nil, &audit.Target{Scope: "unattributed"}},
		{"default list ignores inaccessible input reference", "current_owner", map[string]any{"run_id": "run_ijklmnop3333", "project_id": "plans"}, nil, nil},
		{"explicit list ignores input reference", "current_owner", map[string]any{"action": "list", "run_id": "run_ijklmnop3333"}, nil, nil},
		{"list may resolve an explicit result run", "current_owner", map[string]any{"action": "list", "run_id": "run_ijklmnop3333"}, map[string]any{"run_id": "run_abcdefgh2222"}, &audit.Target{Scope: "project", ProjectID: "anima", RunID: "run_abcdefgh2222"}},
		{"non-list result reference is a fallback", "current_owner", map[string]any{"action": "get"}, map[string]any{"run_id": "run_abcdefgh1111"}, &audit.Target{Scope: "workspace", ProjectID: "plans", WorkspaceID: "ws_historical", RunID: "run_abcdefgh1111"}},
		{"project search delegates current catalogue validation", "current_owner", map[string]any{"action": "search_memory", "project_id": "plans", "query": "term"}, nil, nil},
		{"empty stored workspace retains source truthiness", "current_owner", map[string]any{"action": "get", "run_id": "run_qrstuvwx4444"}, nil, &audit.Target{Scope: "workspace", ProjectID: "plans", RunID: "run_qrstuvwx4444"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := s.AuditTarget(context.Background(), wire.Scope{PrincipalID: test.principal}, test.args, wire.Result{StructuredContent: test.result})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("target=%+v, want %+v", got, test.want)
			}
		})
	}
	if s.db != nil || str(s.byID["run_abcdefgh1111"]["workspace"].(map[string]any)["root"]) != "/do-not-resolve-this" {
		t.Fatal("audit attribution touched archive or workspace state")
	}
}
