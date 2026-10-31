package contracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type Issue struct{ Path, Message string }
type ValidationError struct {
	Tool         string
	Issues       []Issue
	Unknown, SDK bool
}

func (e *ValidationError) Error() string {
	if e.Unknown {
		return "MCP error -32602: Tool " + e.Tool + " not found"
	}
	parts := []string{}
	for _, i := range e.Issues {
		if e.SDK {
			message := i.Message
			if i.Path != "" {
				message += " at " + i.Path
			}
			parts = append(parts, message)
		} else {
			path := i.Path
			if path == "" {
				path = "arguments"
			}
			path = strings.ReplaceAll(strings.ReplaceAll(path, "[", "."), "]", "")
			parts = append(parts, path+": "+i.Message)
		}
	}
	if e.SDK {
		return "MCP error -32602: Input validation error: Invalid arguments for tool " + e.Tool + ": " + strings.Join(parts, "\n")
	}
	return "Invalid arguments for " + e.Tool + ": " + strings.Join(parts, "; ")
}
func (e *ValidationError) SDKResult() wire.Result {
	return wire.Result{Content: []wire.Content{{"type": "text", "text": e.Error()}}, IsError: true}
}
func (e *ValidationError) BusinessError() error {
	data := map[string]any{"retry_unchanged": false}
	for _, i := range e.Issues {
		if i.Path == "workspace_id" || strings.HasPrefix(i.Path, "workspace_id.") {
			data["recovery"] = map[string]any{"tool": "list_projects", "message": "Copy workspace_id from list_projects or the workspace open result."}
			break
		}
	}
	copy := *e
	copy.SDK = false
	return &wire.Error{Code: "args_invalid", Message: copy.Error(), Data: data}
}

type Field struct {
	Name     string
	Schema   *Schema
	Required bool
}
type Schema struct {
	Types                                                []string
	Fields                                               []Field
	Items                                                *Schema
	Alternatives                                         []*Schema
	Enum                                                 []any
	Constant                                             any
	HasConstant                                          bool
	Pattern, Format                                      string
	Minimum, Maximum, ExclusiveMinimum, ExclusiveMaximum *float64
	MinLength, MaxLength, MinItems, MaxItems             *int
	Strict, Passthrough                                  bool
	Additional                                           *Schema
}

func orderedObject(raw []byte) ([]string, map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, e := decoder.Token()
	if e != nil {
		return nil, nil, e
	}
	if token != json.Delim('{') {
		return nil, nil, fmt.Errorf("expected object")
	}
	keys := []string{}
	values := map[string]json.RawMessage{}
	for decoder.More() {
		key, e := decoder.Token()
		if e != nil {
			return nil, nil, e
		}
		var value json.RawMessage
		if e = decoder.Decode(&value); e != nil {
			return nil, nil, e
		}
		k := key.(string)
		keys = append(keys, k)
		values[k] = value
	}
	_, e = decoder.Token()
	return keys, values, e
}
func parseSchema(raw []byte, path, tool string) (*Schema, error) {
	s := &Schema{}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return s, nil
	}
	var data map[string]json.RawMessage
	if e := json.Unmarshal(raw, &data); e != nil {
		return nil, e
	}
	if b := data["type"]; len(b) > 0 {
		var one string
		if json.Unmarshal(b, &one) == nil {
			s.Types = []string{one}
		} else if e := json.Unmarshal(b, &s.Types); e != nil {
			return nil, e
		}
	}
	_ = json.Unmarshal(data["enum"], &s.Enum)
	if b, ok := data["const"]; ok {
		s.HasConstant = true
		s.Constant = decodeJSON(b)
	}
	_ = json.Unmarshal(data["pattern"], &s.Pattern)
	_ = json.Unmarshal(data["format"], &s.Format)
	for key, target := range map[string]**float64{"minimum": &s.Minimum, "maximum": &s.Maximum, "exclusiveMinimum": &s.ExclusiveMinimum, "exclusiveMaximum": &s.ExclusiveMaximum} {
		if b, ok := data[key]; ok {
			var n float64
			if e := json.Unmarshal(b, &n); e != nil {
				return nil, e
			}
			*target = &n
		}
	}
	for key, target := range map[string]**int{"minLength": &s.MinLength, "maxLength": &s.MaxLength, "minItems": &s.MinItems, "maxItems": &s.MaxItems} {
		if b, ok := data[key]; ok {
			var n int
			if e := json.Unmarshal(b, &n); e != nil {
				return nil, e
			}
			*target = &n
		}
	}
	required := []string{}
	_ = json.Unmarshal(data["required"], &required)
	requiredSet := map[string]bool{}
	for _, k := range required {
		requiredSet[k] = true
	}
	if b := data["properties"]; len(b) > 0 {
		keys, props, e := orderedObject(b)
		if e != nil {
			return nil, e
		}
		for _, key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			child, e := parseSchema(props[key], next, tool)
			if e != nil {
				return nil, e
			}
			s.Fields = append(s.Fields, Field{Name: key, Schema: child, Required: requiredSet[key]})
		}
	}
	if b := data["items"]; len(b) > 0 {
		var e error
		s.Items, e = parseSchema(b, path+"[]", tool)
		if e != nil {
			return nil, e
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if b := data[key]; len(b) > 0 {
			var alternatives []json.RawMessage
			if e := json.Unmarshal(b, &alternatives); e != nil {
				return nil, e
			}
			for _, raw := range alternatives {
				alt, e := parseSchema(raw, path, tool)
				if e != nil {
					return nil, e
				}
				s.Alternatives = append(s.Alternatives, alt)
			}
		}
	}
	if b, ok := data["additionalProperties"]; ok {
		if bytes.Equal(b, []byte("true")) {
			s.Passthrough = true
		} else if !bytes.Equal(b, []byte("false")) {
			var e error
			s.Additional, e = parseSchema(b, path+".*", tool)
			if e != nil {
				return nil, e
			}
			s.Passthrough = true
		}
	} else if len(s.Fields) == 0 {
		s.Passthrough = true
	}
	// Zod objects strip unknown keys by default; only explicitly strict nodes reject.
	s.Strict = tool == "batch" && path == "operations[]"
	return s, nil
}
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case map[string]any:
		return "object"
	case []any, []string:
		return "array"
	case float64, float32, int, int64, int32, uint, uint64, json.Number:
		return "number"
	default:
		return "undefined"
	}
}
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}
func equal(a, b any) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Equal(aJSON, bJSON)
}
func display(v any) string               { b, _ := json.Marshal(v); return string(b) }
func issue(path, message string) []Issue { return []Issue{{Path: path, Message: message}} }
func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
func (s *Schema) Validate(value any, present bool, path string) (any, []Issue) {
	if len(s.Alternatives) > 0 {
		if object, ok := value.(map[string]any); ok {
			options := []string{}
			all := true
			var selected *Schema
			for _, alt := range s.Alternatives {
				matched := false
				for _, field := range alt.Fields {
					if field.Name == "op" && field.Schema.HasConstant {
						option, ok := field.Schema.Constant.(string)
						if !ok {
							continue
						}
						options = append(options, option)
						matched = true
						if equal(object["op"], option) {
							selected = alt
						}
					}
				}
				all = all && matched
			}
			if all && len(options) == len(s.Alternatives) {
				if selected != nil {
					return selected.Validate(value, present, path)
				}
				quoted := []string{}
				for _, v := range options {
					quoted = append(quoted, "'"+v+"'")
				}
				return nil, issue(childPath(path, "op"), "Invalid discriminator value. Expected "+strings.Join(quoted, " | "))
			}
		}
		for _, alt := range s.Alternatives {
			v, problems := alt.Validate(value, present, path)
			if len(problems) == 0 {
				return v, nil
			}
		}
		return nil, issue(path, "Invalid input")
	}
	received := typeName(value)
	if !present {
		received = "undefined"
	}
	if s.HasConstant && !equal(value, s.Constant) {
		return nil, issue(path, "Invalid input: expected "+display(s.Constant))
	}
	if len(s.Enum) > 0 {
		match := false
		options := []string{}
		for _, option := range s.Enum {
			match = match || equal(value, option)
			options = append(options, display(option))
		}
		if !match {
			if len(options) == 1 {
				return nil, issue(path, "Invalid input: expected "+options[0])
			}
			return nil, issue(path, "Invalid option: expected one of "+strings.Join(options, "|"))
		}
	}
	expected := ""
	matchesType := len(s.Types) == 0
	for _, kind := range s.Types {
		if expected == "" {
			expected = kind
		}
		match := received == kind
		if kind == "integer" {
			_, ok := numeric(value)
			match = ok
		}
		if match {
			matchesType = true
			expected = kind
			break
		}
	}
	if !matchesType {
		return nil, issue(path, "Invalid input: expected "+expected+", received "+received)
	}
	if expected == "integer" {
		n, ok := numeric(value)
		if !ok || math.Trunc(n) != n {
			return nil, issue(path, "Invalid input: expected int, received number")
		}
	}
	issues := []Issue{}
	switch v := value.(type) {
	case string:
		length := len(utf16.Encode([]rune(v)))
		if s.MinLength != nil && length < *s.MinLength {
			issues = append(issues, Issue{path, fmt.Sprintf("Too small: expected string to have >=%d characters", *s.MinLength)})
		}
		if s.MaxLength != nil && length > *s.MaxLength {
			issues = append(issues, Issue{path, fmt.Sprintf("Too big: expected string to have <=%d characters", *s.MaxLength)})
		}
		if s.Pattern != "" {
			pattern, e := regexp.Compile(s.Pattern)
			if e != nil || !pattern.MatchString(v) {
				issues = append(issues, Issue{path, "Invalid string: must match pattern /" + s.Pattern + "/"})
			}
		}
		if s.Format == "uri" || s.Format == "url" {
			parsed, e := url.Parse(v)
			if e != nil || parsed.Scheme == "" {
				issues = append(issues, Issue{path, "Invalid URL"})
			}
		}
	case []any:
		if s.MinItems != nil && len(v) < *s.MinItems {
			issues = append(issues, Issue{path, fmt.Sprintf("Too small: expected array to have >=%d items", *s.MinItems)})
		}
		if s.MaxItems != nil && len(v) > *s.MaxItems {
			issues = append(issues, Issue{path, fmt.Sprintf("Too big: expected array to have <=%d items", *s.MaxItems)})
		}
		out := make([]any, len(v))
		for i, item := range v {
			if s.Items == nil {
				out[i] = item
				continue
			}
			result, problems := s.Items.Validate(item, true, fmt.Sprintf("%s[%d]", path, i))
			out[i] = result
			issues = append(issues, problems...)
		}
		value = out
	case map[string]any:
		out := map[string]any{}
		known := map[string]bool{}
		for _, field := range s.Fields {
			item, present := v[field.Name]
			known[field.Name] = true
			if !present && !field.Required {
				continue
			}
			parsed, problems := field.Schema.Validate(item, present, childPath(path, field.Name))
			issues = append(issues, problems...)
			if present {
				out[field.Name] = parsed
			}
		}
		unknown := []string{}
		for key, item := range v {
			if known[key] {
				continue
			}
			unknown = append(unknown, key)
			if s.Passthrough {
				if s.Additional != nil {
					parsed, problems := s.Additional.Validate(item, true, childPath(path, key))
					out[key] = parsed
					issues = append(issues, problems...)
				} else {
					out[key] = item
				}
			}
		}
		if s.Strict && len(unknown) > 0 {
			sort.Strings(unknown)
			quoted := []string{}
			for _, key := range unknown {
				quoted = append(quoted, strconv.Quote(key))
			}
			label := "Unrecognized key: "
			if len(unknown) > 1 {
				label = "Unrecognized keys: "
			}
			issues = append(issues, Issue{path, label + strings.Join(quoted, ", ")})
		}
		value = out
	}
	if n, ok := numeric(value); ok {
		for _, bound := range []struct {
			value            *float64
			lower, exclusive bool
		}{{s.Minimum, true, false}, {s.Maximum, false, false}, {s.ExclusiveMinimum, true, true}, {s.ExclusiveMaximum, false, true}} {
			if bound.value == nil {
				continue
			}
			limit := *bound.value
			bad := n < limit
			if !bound.lower {
				bad = n > limit
			}
			if bound.exclusive && n == limit {
				bad = true
			}
			if bad {
				kind, op := "small", ">="
				if !bound.lower {
					kind, op = "big", "<="
				}
				if bound.exclusive {
					op = strings.TrimSuffix(op, "=")
				}
				issues = append(issues, Issue{path, fmt.Sprintf("Too %s: expected number to be %s%s", kind, op, strconv.FormatFloat(limit, 'f', -1, 64))})
			}
		}
	}
	return value, issues
}
