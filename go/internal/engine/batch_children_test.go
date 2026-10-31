package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/batch"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func batchWrapperFixture(t *testing.T) (*Engine, wire.Scope, string, string) {
	t.Helper()
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root := filepath.Join(base, "project")
	home := filepath.Join(base, "state")
	for _, dir := range []string{root, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New(home)
	catalog := workspace.Catalog{Version: 1, DefaultProject: "test", Projects: []wire.Project{{ID: "test", Label: "Test", Root: root, BaseRef: "HEAD"}}}
	b, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.ProjectsFile, b, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err = os.WriteFile(filepath.Join(root, name), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	service, err := batch.New(cfg, e.Workspaces, e, func(name string, args map[string]any) (map[string]any, error) {
		parsed, err := e.Registry.ValidateInner(name, args)
		if validation, ok := err.(*contracts.ValidationError); ok {
			err = validation.BusinessError()
		}
		return parsed, err
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SetFiles(e.Files)
	e.Batch = service
	return e, wire.Scope{PrincipalID: "batch-principal", SessionID: "batch-session", RequestID: "batch-request"}, "ws_" + workspace.Hash(root)[:24], root
}

func TestBatchChildrenHaveOneAuditActionAndOnlyOuterPresentationTags(t *testing.T) {
	e, scope, id, _ := batchWrapperFixture(t)
	operations := []any{}
	for _, name := range []string{"a", "b", "c"} {
		operations = append(operations, map[string]any{"id": name, "tool": "read", "args": map[string]any{"path": name + ".txt"}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := e.Call(ctx, scope, "batch", map[string]any{"workspace_id": id, "operations": operations, "mode": "parallel"})
	if err != nil || r.IsError {
		t.Fatalf("parallel batch failed: %v %#v", err, r)
	}
	if r.StructuredContent["codexpro_tool"] != "batch" || r.StructuredContent["codexpro_title"] != "Batch Operations" {
		t.Fatal("outer presentation tags were lost")
	}
	encoded, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]any
	if err = json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	rows := public["results"].([]any)
	if len(rows) != 3 {
		t.Fatal("parallel child count changed")
	}
	for i, row := range rows {
		child := row.(map[string]any)
		data := child["structured"].(map[string]any)
		if child["id"] != []string{"a", "b", "c"}[i] || data["bytes"] != float64(6) {
			t.Fatalf("child semantic fields changed: %#v", child)
		}
		for _, key := range []string{"codexpro_tool", "codexpro_title", "background_jobs"} {
			if _, present := data[key]; present {
				t.Fatalf("nested MCP decoration %s", key)
			}
		}
	}
	list, err := e.Audit.List(ctx, audit.Filter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Actions) != 1 || list.Actions[0]["tool_name"] != "batch" {
		t.Fatalf("raw children became standalone audit actions: %#v", list.Actions)
	}
}

func TestBatchErrorPathsUseEnclosingWorkspaceAndRawValidation(t *testing.T) {
	e, scope, id, root := batchWrapperFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := e.CallBatchChild(ctx, scope, "read", map[string]any{"workspace_id": id, "path": 42})
	if business, ok := err.(*wire.Error); !ok || business.Code != "args_invalid" {
		t.Fatalf("raw handler did not preserve inner validation: %v", err)
	}
	_, err = e.CallBatchChild(ctx, scope, "read", map[string]any{"workspace_id": id, "path": "missing.txt"})
	if err == nil || !strings.Contains(err.Error(), root) {
		t.Fatalf("raw child error was decorated before the enclosing workspace: %v", err)
	}
	r, err := e.Call(ctx, scope, "batch", map[string]any{"workspace_id": id, "operations": []any{
		map[string]any{"id": "a", "tool": "read", "args": map[string]any{"path": "a.txt"}},
		map[string]any{"id": "missing", "tool": "read", "args": map[string]any{"path": "missing.txt"}},
		map[string]any{"id": "c", "tool": "read", "args": map[string]any{"path": "c.txt"}},
	}})
	if err != nil || !r.IsError {
		t.Fatalf("missing child outcome changed: %v %#v", err, r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), root) || strings.Contains(string(b), "[project:test]/missing.txt") || !strings.Contains(string(b), "[workspace:"+id+"]/missing.txt") {
		t.Fatalf("batch error redaction lost enclosing workspace: %s", b)
	}
	list, err := e.Audit.List(ctx, audit.Filter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Actions) != 1 {
		t.Fatalf("raw validation/error calls created audit actions: %#v", list.Actions)
	}
}
