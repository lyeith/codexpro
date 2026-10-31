// Package wire owns the explicit compatibility DTOs shared by domain packages.
package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Content = map[string]any

type Result struct {
	Meta              map[string]any
	Content           []Content
	StructuredContent map[string]any
	IsError           bool
	// Private audit-only payload; never serialized by MarshalJSON.
	DashboardMetadata map[string]any
}

func (r Result) MarshalJSON() ([]byte, error) {
	v := map[string]any{"content": r.Content}
	if r.Meta != nil {
		v["_meta"] = r.Meta
	}
	if r.StructuredContent != nil {
		v["structuredContent"] = r.StructuredContent
	}
	if r.IsError {
		v["isError"] = true
	}
	return json.Marshal(v)
}

func Success(tool, title string, data map[string]any, text string) Result {
	if data == nil {
		data = map[string]any{}
	}
	data["codexpro_tool"], data["codexpro_title"] = tool, title
	return Result{Meta: map[string]any{}, Content: []Content{{"type": "text", "text": text}}, StructuredContent: data}
}

type Error struct {
	Code    string
	Message string
	Data    map[string]any
}

func (e *Error) Error() string { return e.Message }
func Errorf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Scope struct {
	PrincipalID    string
	SessionID      string
	RequestID      string
	TransportID    string
	ParentActionID string
	StartedAt      time.Time
}

type Workspace struct {
	ID        string
	ProjectID string
	Root      string
	Mode      string
	Branch    string
	BaseRef   string
}

type Project struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Root         string `json:"root"`
	BaseRef      string `json:"baseRef,omitempty"`
	MaxWorktrees int    `json:"maxWorktrees,omitempty"`
}

type Resolver interface {
	Resolve(context.Context, Scope, string) (Workspace, error)
	Projects() []Project
	Project(string) (Project, bool)
}

type Caller interface {
	Call(context.Context, Scope, string, map[string]any) (Result, error)
}

func String(a map[string]any, key, fallback string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return fallback
}
func Int(a map[string]any, key string, fallback int) int {
	switch v := a[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, e := strconv.Atoi(string(v))
		if e == nil {
			return n
		}
	}
	return fallback
}
func Int64(a map[string]any, key string, fallback int64) int64 {
	switch v := a[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, e := v.Int64()
		if e == nil {
			return n
		}
	}
	return fallback
}
func Bool(a map[string]any, key string, fallback bool) bool {
	if v, ok := a[key].(bool); ok {
		return v
	}
	return fallback
}
func Strings(a map[string]any, key string) []string {
	switch v := a[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func Object(a map[string]any, key string) map[string]any {
	if v, ok := a[key].(map[string]any); ok {
		return v
	}
	return nil
}
func Objects(a map[string]any, key string) []map[string]any {
	out := []map[string]any{}
	if list, ok := a[key].([]any); ok {
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}
func Timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func Now() string                  { return Timestamp(time.Now()) }
