// Package contracts owns MCP tool definitions and validates inputs for direct
// MCP calls and batch children against the same schemas.
package contracts

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Instructions = "Use list_projects to select a workspace; reuse its returned ids.\n" +
	"Before editing, call open_workspace once to load project instructions.\n" +
	"Follow the loaded AGENTS instructions and check for more specific instructions in subdirectories. Resolve unreadable or truncated instructions before editing.\n" +
	"For an existing managed run, use work_status to read its assignment, workspace_id and saved progress.\n" +
	"Pass your session label as agent when you have one (1-4 ASCII letters, digits or dashes, for example SP3, CO or SUP)."

type Tool struct {
	mcp.Tool
	schema             *jsonschema.Schema
	validator          *jsonschema.Resolved
	omitIdempotentHint bool
}

type Registry struct {
	Tools  []Tool
	byName map[string]int
}

func New() (*Registry, error) {
	return NewConfigured(ConfiguredOptions{ProjectCount: 2, AnalysisEnabled: true})
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.Tools))
	for _, tool := range r.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func (r *Registry) Tool(name string) (Tool, bool) {
	i, ok := r.byName[name]
	if !ok {
		return Tool{}, false
	}
	return r.Tools[i], true
}

// Tools have advertised task support as forbidden since before the SDK added
// support for tasks. Keep that field and the existing hint presence on the wire.
type listedTool struct {
	mcp.Tool
	Annotations listedAnnotations `json:"annotations"`
	Execution   taskSupport       `json:"execution"`
}

type listedAnnotations struct {
	ReadOnly    bool  `json:"readOnlyHint"`
	OpenWorld   *bool `json:"openWorldHint,omitempty"`
	Destructive *bool `json:"destructiveHint,omitempty"`
	Idempotent  *bool `json:"idempotentHint,omitempty"`
}

type taskSupport struct {
	TaskSupport string `json:"taskSupport"`
}

type toolList struct {
	Tools []listedTool `json:"tools"`
}

func (t Tool) listed() listedTool {
	a := t.Annotations
	hints := listedAnnotations{
		ReadOnly:    a.ReadOnlyHint,
		OpenWorld:   a.OpenWorldHint,
		Destructive: a.DestructiveHint,
	}
	if !t.omitIdempotentHint {
		hints.Idempotent = &a.IdempotentHint
	}
	return listedTool{Tool: t.Tool, Annotations: hints, Execution: taskSupport{TaskSupport: "forbidden"}}
}

func (r *Registry) List() toolList {
	tools := make([]listedTool, 0, len(r.Tools))
	for _, t := range r.Tools {
		tools = append(tools, t.listed())
	}
	return toolList{Tools: tools}
}

func (r *Registry) ReadList() toolList {
	descriptions := map[string]string{
		"list_projects": "List the granted projects and the workspace IDs available to this credential.",
		"tree":          "List visible files and directories in a granted workspace.",
		"read":          "Read lines from a visible text file in a granted workspace.",
		"view_image":    "View an image file in a granted workspace.",
		"search":        "Find literal text in visible files in a granted workspace.",
		"activity_list": "List activity receipts for a granted project, optionally restricted to one of its workspaces. When the scan cannot determine whether more visible receipts exist, has_more is null and truncated is true.",
		"activity_get":  "Read the projected metadata for a visible activity receipt.",
	}
	tools := make([]listedTool, 0, len(descriptions))
	for _, t := range r.Tools {
		if !wire.ReadToolAllowed(t.Name) {
			continue
		}
		tool := t.listed()
		tool.Description = descriptions[t.Name]
		schema := t.schema.CloneSchemas()
		schema.AdditionalProperties = closedObject()
		for key, property := range schema.Properties {
			if !wire.ReadArgumentAllowed(t.Name, key) {
				delete(schema.Properties, key)
				continue
			}
			switch key {
			case "workspace_id":
				property.Description = "A granted workspace ID returned by list_projects."
			case "query":
				property.Description = "Literal text to find."
			case "kind":
				allowValues(property, "text")
				property.Description = "Supported value: text."
			case "scope":
				allowValues(property, "workspace")
				property.Description = "Supported value: workspace."
			}
		}
		switch t.Name {
		case "activity_list":
			tool.Title = "List Project Activity"
			schema.Required = []string{"project_id"}
		case "activity_get":
			tool.Title = "Get Project Activity"
		}
		tool.InputSchema = schema
		tools = append(tools, tool)
	}
	return toolList{Tools: tools}
}

func newRegistry(options ConfiguredOptions) (*Registry, error) {
	registry := &Registry{byName: make(map[string]int)}
	for _, def := range catalog() {
		name := def.tool.Name
		if !variantAvailable(name, options) || !configuredAvailable(name, options) {
			continue
		}
		if _, exists := registry.byName[name]; exists {
			return nil, fmt.Errorf("duplicate MCP tool definition: %s", name)
		}
		schema := def.input()
		validator, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%s input schema: %w", name, err)
		}
		def.tool.InputSchema = schema
		registry.byName[name] = len(registry.Tools)
		registry.Tools = append(registry.Tools, Tool{
			Tool: def.tool, schema: schema, validator: validator, omitIdempotentHint: def.omitIdempotentHint,
		})
	}
	return registry, nil
}
