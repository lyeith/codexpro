package contracts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestEveryToolAcceptsOptionalAgentLabel(t *testing.T) {
	// Cover managed lifecycle tools and the direct-only workspace tools.
	tools := []Tool{}
	seen := map[string]bool{}
	for _, mode := range []string{"mcp", "direct"} {
		registry, err := NewVariant(mode, "full", 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range registry.Tools {
			if !seen[tool.Name] {
				tools = append(tools, tool)
				seen[tool.Name] = true
			}
		}
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			descriptor := decodeJSON(tool.Raw).(map[string]any)
			schema := descriptor["inputSchema"].(map[string]any)
			field := schema["properties"].(map[string]any)["agent"].(map[string]any)
			description, _ := field["description"].(string)
			if field["type"] != "string" || field["minLength"] != json.Number("1") || field["maxLength"] != json.Number("4") || field["pattern"] != wire.AgentPattern || !strings.Contains(description, "activity tracking") || !strings.Contains(description, "explicitly addressed logical mail") || !strings.Contains(description, "Never used for authorization") || !strings.Contains(description, "each call") {
				t.Fatalf("missing bounded caller-label schema: %v", field)
			}
			for _, v := range []string{"SP-3", "a", strings.Repeat("Z", wire.AgentMaxLength)} {
				parsed, issues := tool.Schema.Validate(map[string]any{"agent": v}, true, "")
				if parsed.(map[string]any)["agent"] != v {
					t.Fatalf("agent was lost: %v", parsed)
				}
				for _, issue := range issues {
					if issue.Path == "agent" {
						t.Fatalf("valid label %q rejected: %v", v, issue)
					}
				}
			}
			for _, v := range []any{"", "SP 3", "SP_3", "SP/3", "SP\n3", "é", strings.Repeat("a", wire.AgentMaxLength+1), 3, nil} {
				_, issues := tool.Schema.Validate(map[string]any{"agent": v}, true, "")
				found := false
				for _, issue := range issues {
					found = found || issue.Path == "agent"
				}
				if !found {
					t.Fatalf("invalid label %v was not identified: %v", v, issues)
				}
			}
		})
	}
}

func TestCallerLabelOmissionLeavesValidatedArgumentsUnchanged(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"workspace_id": "ws", "command": "true", "background": true}
	without, err := r.Validate("bash", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := without["agent"]; present {
		t.Fatal("omitted label acquired a value")
	}
	args["agent"] = "SP-3"
	with, err := r.Validate("bash", args)
	if err != nil || with["agent"] != "SP-3" {
		t.Fatalf("caller label did not reach validated input: %v %v", with, err)
	}
	delete(with, "agent")
	if !reflect.DeepEqual(with, without) {
		t.Fatalf("label changed tool arguments: %v != %v", with, without)
	}
	args["agent"] = "SP 3"
	if _, err := r.Validate("bash", args); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("invalid caller label did not return a clear error: %v", err)
	}
	args["agent"] = "ABCDE"
	if _, err := r.Validate("bash", args); err == nil || !strings.Contains(err.Error(), "agent") || !strings.Contains(err.Error(), "<=4 characters") {
		t.Fatalf("five-character label did not explain the four-character limit: %v", err)
	}
}
