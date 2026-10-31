// Package contracts preserves captured TypeScript descriptors without SDK reflection.
package contracts

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
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
	r := &Registry{byName: map[string]int{}}
	if e := json.Unmarshal(initializeJSON, &r.Initialization); e != nil {
		return nil, e
	}
	for _, data := range raw {
		var t Tool
		if e := json.Unmarshal(data, &t); e != nil {
			return nil, e
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
