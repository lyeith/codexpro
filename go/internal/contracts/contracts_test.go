package contracts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func fixtureArguments() map[string]map[string]any {
	ws := "ws_fixture"
	jobs := []any{map[string]any{"command": "true"}}
	return map[string]map[string]any{
		"list_projects":          {},
		"create_project":         {"project_id": "test", "parent_id": "root", "source": "empty"},
		"server_config":          {},
		"activity_list":          {},
		"activity_get":           {"action_id": "cpa_" + strings.Repeat("a", 32)},
		"activity_status":        {},
		"activity_export":        {"after_sequence": 0},
		"codexpro_self_test":     {"workspace_id": ws},
		"load_skill":             {"workspace_id": ws, "name": "test"},
		"open_workspace":         {"workspace_id": ws},
		"open_current_workspace": {},
		"list_workspaces":        {},
		"inspect_workspace":      {"workspace_id": ws},
		"tree":                   {"workspace_id": ws},
		"search":                 {"workspace_id": ws, "query": "Go", "context_after": 0},
		"ast_grep":               {"workspace_id": ws, "kind": "function", "language": "go"},
		"read":                   {"workspace_id": ws, "path": "fixture.go", "start_line": 1.0},
		"view_image":             {"workspace_id": ws, "path": "fixture.png"},
		"write":                  {"workspace_id": ws, "path": "fixture.go", "content": ""},
		"edit": {"workspace_id": ws, "path": "fixture.go", "edit_tag": "BEEF",
			"edits": []any{map[string]any{"op": "replace", "start_line": 1, "content": "package fixture"}}},
		"apply_patch": {"workspace_id": ws, "patch": "patch text"},
		"import_file": {"workspace_id": ws, "destination": "fixture.txt",
			"file": map[string]any{"download_url": "https://example.com/file", "file_id": "test"}},
		"bash":           {"workspace_id": ws, "command": "true"},
		"start_jobs":     {"workspace_id": ws, "commands": jobs},
		"queue_jobs":     {"workspace_id": ws, "commands": jobs, "queue_key": "stable"},
		"jobs":           {"workspace_id": ws, "include_finished": false},
		"stop_jobs":      {"workspace_id": ws, "job_ids": []any{"job"}},
		"show_changes":   {"workspace_id": ws, "include_diff": false},
		"commit_changes": {"workspace_id": ws, "message": "test"},
		"batch":          {"workspace_id": ws, "operations": []any{map[string]any{"tool": "read", "args": map[string]any{"path": "fixture.go"}}}},
		"work_status":    {"action": "get", "run_id": "r_123"},
	}
}

func TestNativeCatalogValidatesEveryTool(t *testing.T) {
	r, err := NewConfigured(ConfiguredOptions{ToolMode: "full", ProjectCount: 1, AnalysisEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fixtures := fixtureArguments()
	for _, tool := range r.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			args, ok := fixtures[tool.Name]
			if !ok {
				t.Fatal("missing tool fixture")
			}
			parsed, err := r.Validate(tool.Name, args)
			if err != nil || !reflect.DeepEqual(parsed, args) {
				t.Fatalf("valid input changed: %v, %v", parsed, err)
			}
			for _, key := range tool.schema.Required {
				value := args[key]
				delete(args, key)
				if _, err := r.Validate(tool.Name, args); err == nil {
					t.Errorf("missing required field %s accepted", key)
				}
				args[key] = value
			}
		})
	}

	read, _ := r.Tool("read")
	if !strings.Contains(read.Description, "whole source must fit") {
		t.Fatal("read schema lost source-ceiling guidance")
	}
	file, _ := r.Tool("import_file")
	if !slices.Equal(file.Meta["openai/fileParams"].([]string), []string{"file"}) {
		t.Fatal("import_file lost attachment metadata")
	}
}

func TestBashHiddenFieldAndUnknownProjection(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"workspace_id": "ws", "command": "true", "background": true, "unknown": "strip"}
	got, err := r.Validate("bash", args)
	if err != nil || got["background"] != true || got["unknown"] != nil {
		t.Fatalf("unexpected Bash projection: %v, %v", got, err)
	}
	bash, _ := r.Tool("bash")
	if bash.schema.Properties["background"] != nil {
		t.Fatal("hidden Bash field is advertised")
	}
	args["background"] = "yes"
	_, err = r.Validate("bash", args)
	validation, ok := err.(*ValidationError)
	if !ok || validation.SDK || !strings.Contains(validation.Error(), "background") {
		t.Fatalf("wrong validation layer: %v", err)
	}
}

func TestArgumentDiagnosticsIdentifyTheFieldWithoutRepeatingTheValue(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	const rejected = "a private enum value"
	args := map[string]any{"workspace_id": "ws", "query": "go", "kind": rejected}
	_, err = r.Validate("search", args)
	if err == nil || !strings.Contains(err.Error(), "kind") || strings.Contains(err.Error(), rejected) {
		t.Fatalf("unexpected validation diagnostic: %v", err)
	}
	delete(args, "workspace_id")
	delete(args, "kind")
	_, err = r.ValidateInner("search", args)
	validation, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("workspace validation error missing: %v", err)
	}
	business := validation.BusinessError().(*wire.Error)
	if business.Code != "args_invalid" || business.Data["recovery"].(map[string]any)["tool"] != "list_projects" {
		t.Fatalf("workspace recovery guidance missing: %#v", business)
	}
}

func TestBatchAndEditArguments(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	batch := fixtureArguments()["batch"]
	op := wire.Objects(batch, "operations")[0]
	child := wire.Object(op, "args")
	child["child_extension"] = "pass through for child dispatch"
	got, err := r.Validate("batch", batch)
	if err != nil || !reflect.DeepEqual(got, batch) {
		t.Fatalf("batch lost child arguments: %v, %v", got, err)
	}
	op["unknown"] = true
	if _, err := r.Validate("batch", batch); err == nil {
		t.Fatal("batch admitted an unknown operation envelope field")
	}
	edit := fixtureArguments()["edit"]
	hunk := wire.Objects(edit, "edits")[0]
	hunk["op"] = "insert_after"
	hunk["line"] = json.Number("2")
	got, err = r.Validate("edit", edit)
	if err != nil || wire.Int(wire.Objects(got, "edits")[0], "line", 0) != 2 {
		t.Fatalf("valid line insertion was rejected: %v, %v", got, err)
	}
	hunk["op"] = "insert"
	if _, err := r.Validate("edit", edit); err == nil {
		t.Fatal("edit admitted an unknown operation")
	}
}

func TestCapacityQueueContractIsExplicitTool(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	start, _ := r.Tool("start_jobs")
	queued, ok := r.Tool("queue_jobs")
	if !ok {
		t.Fatal("missing queue tool")
	}
	if !reflect.DeepEqual(start.schema.Properties["workspace_id"], queued.schema.Properties["workspace_id"]) {
		t.Fatal("queue workspace selector differs from start_jobs")
	}
	args := fixtureArguments()["start_jobs"]
	if _, err := r.Validate("start_jobs", args); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Validate("queue_jobs", args); err == nil {
		t.Fatal("missing stable queue key was admitted")
	}
	args["queue_key"] = "stable"
	args["queue_timeout_ms"] = 1000
	if _, err := r.Validate("queue_jobs", args); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Validate("start_jobs", args); err == nil {
		t.Fatal("start_jobs accepted a queue alias")
	}
	args["queue_timeout_ms"] = 86400001
	if _, err := r.Validate("queue_jobs", args); err == nil {
		t.Fatal("queue_jobs admitted an invalid wait")
	}
}
