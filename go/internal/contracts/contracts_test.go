package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Keep this expectation independent of the production descriptor adjustment.
const expectedSourceLimitedReadDescription = "Read a bounded page of a text file with line numbers and a four-character edit_tag. The whole source must fit the configured source-size limit before line selection; start_line, end_line and max_bytes do not raise that limit. max_bytes limits returned text. Continue a paginated result with next_start_line. Before editing, read every range you plan to change, then send all intended same-file changes in one combined multi-hunk edit. Re-read after any mutation before another tagged edit; otherwise avoid redundant rereads when the returned diff is sufficient."

func TestFrozenDescriptorsOnlyApprovedRemovalsCallerLabelAndReadDescription(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../docs/go-rewrite/capture-contracts/tools-list-live.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if e = json.Unmarshal(b, &source); e != nil {
		t.Fatal(e)
	}
	legacy := []Tool{}
	for _, tool := range r.Tools {
		if tool.Name != "queue_jobs" {
			legacy = append(legacy, tool)
		}
	}
	index := 0
	for _, raw := range source.Result.Tools {
		var tool map[string]any
		_ = json.Unmarshal(raw, &tool)
		if tool["name"] == "work_manage" || tool["name"] == "work_update" {
			continue
		}
		if tool["name"] == "read" {
			tool["description"] = expectedSourceLimitedReadDescription
			if legacy[index].Description != expectedSourceLimitedReadDescription {
				t.Error("decoded read description differs from the maintained contract")
			}
		}
		var actual map[string]any
		_ = json.Unmarshal(legacy[index].Raw, &actual)
		delete(actual["inputSchema"].(map[string]any)["properties"].(map[string]any), "agent")
		if !reflect.DeepEqual(tool, actual) {
			t.Errorf("descriptor drift for %s", tool["name"])
		}
		index++
	}
	if index != 28 || len(r.Tools) != 29 {
		t.Fatal("unexpected removal/addition")
	}
}
func TestValidationMatchesCapturedPrivateTypeScript(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../docs/go-rewrite/evidence/private-validation.json")
	if e != nil {
		t.Fatal(e)
	}
	var evidence struct {
		Cases []struct {
			Case      string `json:"case"`
			Tool      string `json:"tool"`
			Arguments any    `json:"arguments"`
			Reference struct {
				Body struct {
					Result map[string]any `json:"result"`
				} `json:"body"`
			} `json:"reference"`
		} `json:"cases"`
	}
	if e = json.Unmarshal(b, &evidence); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, c := range evidence.Cases {
		if _, ok := c.Reference.Body.Result["structuredContent"]; ok {
			continue
		}
		t.Run(c.Case, func(t *testing.T) {
			_, e := r.Validate(c.Tool, c.Arguments)
			ve, ok := e.(*ValidationError)
			if !ok {
				t.Fatalf("expected validation failure, got %v", e)
			}
			encoded, _ := json.Marshal(ve.SDKResult())
			var actual map[string]any
			_ = json.Unmarshal(encoded, &actual)
			if !reflect.DeepEqual(actual, c.Reference.Body.Result) {
				t.Fatalf("actual %s; reference %v", encoded, c.Reference.Body.Result)
			}
		})
		count++
	}
	if count < 9 {
		t.Fatal("missing validation captures")
	}
}
func TestHiddenBashFieldAndUnknownStripping(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	args, e := r.Validate("bash", map[string]any{"workspace_id": "ws", "command": "true", "background": true, "unadvertised_unknown": "strip"})
	if e != nil {
		t.Fatal(e)
	}
	if args["background"] != true {
		t.Fatal("legacy hidden background lost")
	}
	if _, ok := args["unadvertised_unknown"]; ok {
		t.Fatal("unknown input retained")
	}
	_, e = r.Validate("bash", map[string]any{"workspace_id": "ws", "command": "true", "background": "yes"})
	ve, ok := e.(*ValidationError)
	if !ok || ve.SDK || !strings.Contains(ve.Error(), "background") {
		t.Fatalf("wrong hidden validation layer %v", e)
	}
}

// The old start_jobs descriptor remains unchanged; waiting uses an additive tool.
func TestCapacityQueueContractIsExplicitTool(t *testing.T) {
	registry, err := New()
	if err != nil {
		t.Fatal(err)
	}
	start, _ := registry.Tool("start_jobs")
	queued, ok := registry.Tool("queue_jobs")
	if !ok {
		t.Fatal("missing queue tool")
	}
	var a, b map[string]any
	_ = json.Unmarshal(start.Raw, &a)
	_ = json.Unmarshal(queued.Raw, &b)
	props := b["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["admission"] != nil || props["queue_key"].(map[string]any)["maxLength"] != float64(160) || props["queue_timeout_ms"].(map[string]any)["maximum"] != float64(86400000) {
		t.Fatal("queue bounds changed")
	}
	for _, key := range []string{"admission", "queue_key", "queue_timeout_ms"} {
		if a["inputSchema"].(map[string]any)["properties"].(map[string]any)[key] != nil {
			t.Fatal("start_jobs acquired queue alias")
		}
	}
	base := map[string]any{"workspace_id": "ws", "commands": []any{map[string]any{"command": "true"}}}
	if _, err = registry.Validate("start_jobs", base); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Validate("queue_jobs", base); err == nil {
		t.Fatal("missing stable key admitted")
	}
	base["queue_key"] = "stable"
	base["queue_timeout_ms"] = 1000
	if _, err = registry.Validate("queue_jobs", base); err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Validate("start_jobs", base); err == nil {
		t.Fatal("old unreleased queue alias silently stripped")
	}
	base["queue_timeout_ms"] = 86400001
	if _, err = registry.Validate("queue_jobs", base); err == nil {
		t.Fatal("invalid wait admitted")
	}
}
func TestCapacityQueueContractSurvivesEveryWorkspaceRoutingProfile(t *testing.T) {
	for _, workspaceMode := range []string{"direct", "mcp"} {
		for _, toolMode := range []string{"minimal", "standard", "full"} {
			t.Run(workspaceMode+"/"+toolMode, func(t *testing.T) {
				registry, err := NewConfigured(ConfiguredOptions{WorktreeMode: workspaceMode, ToolMode: toolMode, ProjectCount: 1, AnalysisEnabled: true, AuditMode: "metadata"})
				if err != nil {
					t.Fatal(err)
				}
				queued, ok := registry.Tool("queue_jobs")
				if !ok || strings.Count(queued.Description, "Durably queue") != 1 {
					t.Fatal("missing/duplicate queue descriptor")
				}
				args, err := registry.Validate("queue_jobs", map[string]any{"workspace_id": "ws", "commands": []any{map[string]any{"command": "true"}}, "queue_key": "stable", "queue_timeout_ms": 1000})
				if err != nil || args["queue_key"] != "stable" {
					t.Fatalf("routing lost key: %#v %v", args, err)
				}
				start, _ := registry.Tool("start_jobs")
				var a, b map[string]any
				_ = json.Unmarshal(start.InputSchema, &a)
				_ = json.Unmarshal(queued.InputSchema, &b)
				if !reflect.DeepEqual(a["properties"].(map[string]any)["workspace_id"], b["properties"].(map[string]any)["workspace_id"]) {
					t.Fatal("queue workspace selector differs")
				}
			})
		}
	}
}
