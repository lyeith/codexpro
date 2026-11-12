// Package contracts preserves captured TypeScript descriptors with a shared
// caller-label extension and a maintained read description, without SDK reflection.
package contracts

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/lyeith/codexpro/go/internal/wire"
)

//go:embed assets/tools.json
var toolsJSON []byte

//go:embed assets/initialize.json
var initializeJSON []byte

type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Raw         json.RawMessage `json:"-"`
	Schema      *Schema         `json:"-"`
}
type Registry struct {
	Tools          []Tool
	byName         map[string]int
	Initialization map[string]any
}

func New() (*Registry, error) {
	var raw []json.RawMessage
	if e := json.Unmarshal(toolsJSON, &raw); e != nil {
		return nil, e
	}
	return newRegistry(raw)
}

func newRegistry(raw []json.RawMessage) (*Registry, error) {
	r := &Registry{byName: map[string]int{}}
	if e := json.Unmarshal(initializeJSON, &r.Initialization); e != nil {
		return nil, e
	}
	for _, data := range raw {
		var t Tool
		if e := json.Unmarshal(data, &t); e != nil {
			return nil, e
		}
		if retiredDescriptor(t.Name) {
			continue
		}
		if _, duplicate := r.byName[t.Name]; duplicate {
			return nil, fmt.Errorf("duplicate captured tool descriptor: %s", t.Name)
		}
		data, err := withAgentLabel(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, err)
		}
		if t.Name == "start_jobs" {
			data, err = withCapacityQueue(data)
			if err != nil {
				return nil, fmt.Errorf("%s queue contract: %w", t.Name, err)
			}
		}
		if t.Name == "read" {
			description, err := json.Marshal("Read a bounded page of a text file with line numbers and a four-character edit_tag. The whole source must fit the configured source-size limit before line selection; start_line, end_line and max_bytes do not raise that limit. max_bytes limits returned text. Continue a paginated result with next_start_line. Before editing, read every range you plan to change, then send all intended same-file changes in one combined multi-hunk edit. Re-read after any mutation before another tagged edit; otherwise avoid redundant rereads when the returned diff is sufficient.")
			if err != nil {
				return nil, fmt.Errorf("%s description: %w", t.Name, err)
			}
			data, err = setObjectField(data, "description", description)
			if err != nil {
				return nil, fmt.Errorf("%s description: %w", t.Name, err)
			}
		}
		if err = json.Unmarshal(data, &t); err != nil {
			return nil, err
		}
		t.Raw = append([]byte(nil), data...)
		s, e := parseSchema(t.InputSchema, "", t.Name)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", t.Name, e)
		}
		t.Schema = s
		r.byName[t.Name] = len(r.Tools)
		r.Tools = append(r.Tools, t)
	}
	return r, nil
}
func (r *Registry) Names() []string {
	out := make([]string, len(r.Tools))
	for i, t := range r.Tools {
		out[i] = t.Name
	}
	return out
}
func (r *Registry) Tool(name string) (Tool, bool) {
	i, ok := r.byName[name]
	if !ok {
		return Tool{}, false
	}
	return r.Tools[i], true
}
func (r *Registry) List() any {
	tools := make([]json.RawMessage, len(r.Tools))
	for i, t := range r.Tools {
		tools[i] = t.Raw
	}
	return map[string]any{"tools": tools}
}

// ReadList projects the captured descriptors, preserving their annotations and
// scalar validators. Each decode owns its maps; the operator schema is immutable.
func (r *Registry) ReadList() any {
	descriptions := map[string]string{
		"list_projects": "List the granted projects and the workspace IDs available to this credential.",
		"tree":          "List visible files and directories in a granted workspace.",
		"read":          "Read lines from a visible text file in a granted workspace.",
		"view_image":    "View an image file in a granted workspace.",
		"search":        "Find literal text in visible files in a granted workspace.",
		"activity_list": "List activity receipts for a granted project, optionally restricted to one of its workspaces. When the scan cannot determine whether more visible receipts exist, has_more is null and truncated is true.",
		"activity_get":  "Read the projected metadata for a visible activity receipt.",
	}
	tools := []map[string]any{}
	for _, t := range r.Tools {
		if !wire.ReadToolAllowed(t.Name) {
			continue
		}
		tool := decodeJSON(t.Raw).(map[string]any)
		tool["description"] = descriptions[t.Name]
		schema := tool["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		schema["additionalProperties"] = false
		for key, value := range props {
			if !wire.ReadArgumentAllowed(t.Name, key) {
				delete(props, key)
				continue
			}
			prop := value.(map[string]any)
			switch key {
			case "workspace_id":
				prop["description"] = "A granted workspace ID returned by list_projects."
			case "query":
				prop["description"] = "Literal text to find."
			case "kind", "scope":
				fixed := "text"
				if key == "scope" {
					fixed = "workspace"
				}
				prop["enum"] = []string{fixed}
				prop["description"] = "Supported value: " + fixed + "."
			}
		}
		switch t.Name {
		case "activity_list":
			tool["title"] = "List Project Activity"
			schema["required"] = []string{"project_id"}
		case "activity_get":
			tool["title"] = "Get Project Activity"
		}
		tools = append(tools, tool)
	}
	return map[string]any{"tools": tools}
}
func decodeJSON(b []byte) any {
	var x any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	_ = d.Decode(&x)
	return x
}
func (r *Registry) Validate(name string, args any) (map[string]any, error) {
	tool, ok := r.Tool(name)
	if !ok {
		return nil, &ValidationError{Tool: name, Unknown: true, SDK: true}
	}
	if args == nil {
		args = map[string]any{}
	}
	value, issues := tool.Schema.Validate(args, true, "")
	if len(issues) > 0 {
		return nil, &ValidationError{Tool: name, Issues: issues, SDK: true}
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, &ValidationError{Tool: name, Issues: []Issue{{Message: "Invalid input: expected object, received " + typeName(value)}}, SDK: true}
	}
	// This legacy field is deliberately accepted but never advertised.
	if name == "bash" {
		known := map[string]bool{"background": true}
		for _, field := range tool.Schema.Fields {
			known[field.Name] = true
		}
		for key := range out {
			if !known[key] {
				delete(out, key)
			}
		}
		if original, ok := args.(map[string]any); ok {
			if v, present := original["background"]; present {
				if _, ok = v.(bool); !ok {
					return nil, &ValidationError{Tool: name, Issues: []Issue{{Path: "background", Message: "Invalid input: expected boolean, received " + typeName(v)}}, SDK: false}
				}
				out["background"] = v
			}
		}
	}
	return out, nil
}
func (r *Registry) ValidateInner(name string, args any) (map[string]any, error) {
	value, e := r.Validate(name, args)
	if v, ok := e.(*ValidationError); ok {
		copy := *v
		copy.SDK = false
		e = &copy
	}
	return value, e
}
