package contracts

import (
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestEveryToolAcceptsOptionalAgentLabel(t *testing.T) {
	r, err := NewConfigured(ConfiguredOptions{ToolMode: "full", ProjectCount: 1, AnalysisEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	fixtures := fixtureArguments()
	for _, tool := range r.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			schema := tool.schema
			field := schema.Properties["agent"]
			if field == nil || field.Pattern != wire.AgentPattern || *field.MaxLength != wire.AgentMaxLength ||
				!strings.Contains(field.Description, "explicitly addressed logical mail") {
				t.Fatalf("missing bounded caller-label schema: %v", field)
			}
			args := fixtures[tool.Name]
			without, err := r.Validate(tool.Name, args)
			if err != nil {
				t.Fatal(err)
			}
			if _, present := without["agent"]; present {
				t.Fatal("omitted label acquired a value")
			}
			args["agent"] = "SP-3"
			with, err := r.Validate(tool.Name, args)
			if err != nil || with["agent"] != "SP-3" {
				t.Fatalf("valid label lost: %v, %v", with, err)
			}
			for _, bad := range []any{"", "SP 3", "SP_3", "SP/3", "SP\n3", "é", "ABCDE", 3, nil} {
				args["agent"] = bad
				if _, err := r.Validate(tool.Name, args); err == nil || !strings.Contains(err.Error(), "4 characters") {
					t.Errorf("invalid label %q lacked a validation error: %v", bad, err)
				}
			}
		})
	}
}
