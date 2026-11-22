package contracts

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type ValidationError struct {
	Tool        string
	Detail      string
	WorkspaceID bool
	Unknown     bool
	SDK         bool
}

func (e *ValidationError) Error() string {
	if e.Unknown {
		return "MCP error -32602: Tool " + e.Tool + " not found"
	}
	if e.SDK {
		return "MCP error -32602: Input validation error: Invalid arguments for tool " + e.Tool + ": " + e.Detail
	}
	return "Invalid arguments for " + e.Tool + ": " + e.Detail
}

func (e *ValidationError) SDKResult() wire.Result {
	return wire.Result{Content: []wire.Content{{"type": "text", "text": e.Error()}}, IsError: true}
}

func (e *ValidationError) BusinessError() error {
	data := map[string]any{"retry_unchanged": false}
	if e.WorkspaceID {
		data["recovery"] = map[string]any{"tool": "list_projects", "message": "Copy workspace_id from list_projects or the workspace open result."}
	}
	copy := *e
	copy.SDK = false
	return &wire.Error{Code: "args_invalid", Message: copy.Error(), Data: data}
}

func (r *Registry) Validate(name string, args any) (map[string]any, error) {
	tool, ok := r.Tool(name)
	if !ok {
		return nil, &ValidationError{Tool: name, Unknown: true, SDK: true}
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, ok := args.(map[string]any)
	if !ok {
		return nil, &ValidationError{Tool: name, Detail: "arguments must be an object", SDK: true}
	}
	if agent, supplied := raw["agent"]; supplied {
		label, ok := agent.(string)
		if !ok || !wire.ValidAgent(label) {
			return nil, &ValidationError{
				Tool: name, Detail: fmt.Sprintf("agent must contain 1 to %d characters using ASCII letters, digits or dashes", wire.AgentMaxLength), SDK: true,
			}
		}
	}
	if name == "start_jobs" {
		for _, key := range []string{"admission", "queue_key", "queue_timeout_ms"} {
			if _, supplied := raw[key]; supplied {
				return nil, &ValidationError{Tool: name, Detail: key + ": Queued submissions require the queue_jobs tool.", SDK: true}
			}
		}
	}
	var strict *jsonschema.Schema
	if name == "batch" {
		strict = tool.schema.Properties["operations"].Items
	}
	projected := projectInput(tool.schema, raw, strict).(map[string]any)
	if err := tool.validator.Validate(projected); err != nil {
		workspace, supplied := raw["workspace_id"]
		_, isString := workspace.(string)
		checkWorkspace := tool.schema.Properties["workspace_id"] != nil &&
			(supplied || slices.Contains(tool.schema.Required, "workspace_id"))
		return nil, &ValidationError{
			Tool: name, Detail: schemaDiagnostic(err),
			WorkspaceID: checkWorkspace && !isString, SDK: true,
		}
	}
	if name == "bash" {
		// This supported legacy argument remains absent from tool discovery.
		if background, supplied := raw["background"]; supplied {
			if _, ok := background.(bool); !ok {
				return nil, &ValidationError{Tool: name, Detail: "background must be a boolean"}
			}
			projected["background"] = background
		}
	}
	return projected, nil
}

func schemaDiagnostic(err error) string {
	path := "arguments"
	for next := errors.Unwrap(err); next != nil; next = errors.Unwrap(err) {
		prefix, _, _ := strings.Cut(err.Error(), ": ")
		if pointer, ok := strings.CutPrefix(prefix, "validating /"); ok {
			// The pointer names the schema node, so it contains no input values.
			path = strings.ReplaceAll(strings.ReplaceAll("/"+pointer, "/properties/", "/"), "/items", "[]")
		}
		err = next
	}
	if missing, ok := strings.CutPrefix(err.Error(), "required: missing properties: "); ok {
		return path + " is missing required fields " + missing
	}
	if strings.HasPrefix(err.Error(), "unexpected additional properties ") {
		return path + " contains unsupported fields"
	}
	keyword, _, _ := strings.Cut(err.Error(), ":")
	switch keyword {
	case "type":
		return path + " has the wrong JSON type"
	case "enum", "const", "oneOf":
		return path + " does not match one of the listed options"
	case "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems":
		return path + " is outside the bounds in the tool schema"
	case "pattern":
		return path + " does not match the required format"
	}
	return path + " does not match the tool schema"
}

func (r *Registry) ValidateInner(name string, args any) (map[string]any, error) {
	value, err := r.Validate(name, args)
	if e, ok := err.(*ValidationError); ok {
		copy := *e
		copy.SDK = false
		err = &copy
	}
	return value, err
}

// Project onto properties accepted by the dispatcher. Extra fields are ignored
// by ordinary tools, remain available inside free-form batch child arguments,
// and must remain visible in inline batch operation envelopes for rejection.
func projectInput(schema *jsonschema.Schema, value any, strict *jsonschema.Schema) any {
	switch v := value.(type) {
	case map[string]any:
		for _, variant := range schema.OneOf {
			// Inline edits are the only union in the tool inputs.
			if op := variant.Properties["op"]; op != nil && op.Const != nil && v["op"] == *op.Const {
				return projectInput(variant, v, strict)
			}
		}
		if schema.Properties == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if p := schema.Properties[key]; p != nil {
				out[key] = projectInput(p, item, strict)
			} else if schema == strict {
				out[key] = item
			}
		}
		return out
	case []any:
		if schema.Items == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = projectInput(schema.Items, item, strict)
		}
		return out
	case json.Number:
		if n, err := v.Float64(); err == nil {
			return n
		}
		return nil
	}
	return value
}
