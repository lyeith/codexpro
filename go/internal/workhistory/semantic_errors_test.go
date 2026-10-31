package workhistory

import (
	"context"
	"errors"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// The release's workError helper defaults these semantic input conflicts to
// work_conflict. Transport/schema validation and ownership failures are separate
// contracts and must not be rewritten along with them.
func TestArchivedWorkSemanticErrorsPreserveReleaseCodes(t *testing.T) {
	run := map[string]any{"id": "run_abcdefgh12345678", "principal_id": "owner", "project_id": "plans"}
	service := &Service{byID: map[string]map[string]any{run["id"].(string): run}, runs: []map[string]any{run}}
	cases := []struct {
		name          string
		args          map[string]any
		code, message string
	}{
		{"missing run", map[string]any{"action": "get"}, "work_conflict", "This action requires run_id."},
		{"missing search scope", map[string]any{"action": "search_memory", "query": "topic"}, "work_conflict", "search_memory requires query and run_id or project_id."},
		{"missing search query", map[string]any{"action": "search_memory", "run_id": run["id"]}, "work_conflict", "search_memory requires query and run_id or project_id."},
		{"missing document", map[string]any{"action": "read_document", "run_id": run["id"]}, "work_conflict", "read_document requires document_id."},
		{"unknown operation", map[string]any{"action": "operation", "run_id": run["id"], "operation_id": "op_missing"}, "work_conflict", "Unknown operation_id."},
		{"missing operation", map[string]any{"action": "operation", "run_id": run["id"]}, "work_conflict", "Unknown operation_id."},
		{"unknown run unchanged", map[string]any{"action": "get", "run_id": "run_zzzzzzzz12345678"}, "work_not_found", "Unknown or inaccessible run_id. Use the full ID or a unique prefix with at least 8 characters after run_."},
	}
	for _, row := range cases {
		t.Run(row.name, func(t *testing.T) {
			_, err := service.Call(context.Background(), wire.Scope{PrincipalID: "owner"}, "work_status", row.args)
			var typed *wire.Error
			if !errors.As(err, &typed) || typed.Code != row.code || typed.Message != row.message {
				t.Fatalf("code/message mismatch: %#v %v", typed, err)
			}
		})
	}
	_, err := service.Call(context.Background(), wire.Scope{PrincipalID: "foreign"}, "work_status", map[string]any{"action": "get", "run_id": run["id"]})
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != "work_not_found" || typed.Message != "Unknown or inaccessible run_id." {
		t.Fatalf("foreign ownership error changed: %#v %v", typed, err)
	}
}
