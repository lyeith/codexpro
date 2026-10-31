package engine

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

type pathLabel struct{ Path, Label string }
type Redactor struct {
	Config     *config.Config
	Workspaces *workspace.Manager
}

func (r Redactor) Labels(payload any) []pathLabel {
	if r.Config.Bool("exposeAbsolutePaths", false) {
		return nil
	}
	labels := []pathLabel{}
	seen := map[string]bool{}
	add := func(path, label string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if path == "." || path == "/" || seen[path] {
			return
		}
		seen[path] = true
		labels = append(labels, pathLabel{path, label})
	}
	var collect func(any, int)
	collect = func(v any, depth int) {
		if depth > 12 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if root, ok := x["root"].(string); ok && filepath.IsAbs(root) {
				id := wire.String(x, "id", wire.String(x, "workspace_id", wire.String(x, "project_id", "current")))
				add(root, "[workspace:"+id+"]")
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				collect(x[key], depth+1)
			}
		case []any:
			for _, item := range x {
				collect(item, depth+1)
			}
		}
	}
	collect(wire.JSONValue(payload), 0)
	for _, p := range r.Workspaces.Projects() {
		add(p.Root, "[project:"+p.ID+"]")
	}
	for _, p := range r.Workspaces.CreationRoots() {
		add(p.Root, "[creation-root:"+p.ID+"]")
	}
	for _, root := range r.Config.AllowedRoots {
		add(root, "[allowed-root]")
	}
	worktreeRoot := r.Config.String("worktreeRoot", "")
	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(r.Config.Home, "worktrees")
	}
	add(worktreeRoot, "[worktree-storage]")
	home, _ := os.UserHomeDir()
	add(r.Config.String("codexDir", filepath.Join(home, ".codex")), "[codex-data]")
	add(r.Config.ProjectsFile, "[projects-file]")
	add(home, "[home]")
	sort.SliceStable(labels, func(i, j int) bool { return len(labels[i].Path) > len(labels[j].Path) })
	return labels
}
func pathContinuation(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}
func replacePath(text, path, label string) string {
	if path == "" {
		return text
	}
	var out strings.Builder
	for {
		index := strings.Index(text, path)
		if index < 0 {
			out.WriteString(text)
			break
		}
		end := index + len(path)
		if end < len(text) && pathContinuation(text[end]) {
			out.WriteString(text[:end])
			text = text[end:]
			continue
		}
		out.WriteString(text[:index])
		out.WriteString(label)
		text = text[end:]
	}
	return out.String()
}
func (r Redactor) Text(text string, labels []pathLabel) string {
	text = wire.Redact(text)
	for _, label := range labels {
		text = replacePath(text, label.Path, label.Label)
	}
	return text
}
func (r Redactor) Value(value any, labels []pathLabel, unknownPaths bool) any {
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			result := r.Text(x, labels)
			if unknownPaths && !r.Config.Bool("exposeAbsolutePaths", false) && (filepath.IsAbs(result) || len(result) > 2 && result[1] == ':' && (result[2] == '\\' || result[2] == '/')) {
				return "[path]"
			}
			return result
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = walk(item)
			}
			return out
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, item := range x {
				out[k] = walk(item)
			}
			return out
		default:
			return v
		}
	}
	return walk(wire.JSONValue(value))
}
func (r Redactor) Result(result wire.Result) wire.Result {
	labels := r.Labels(result.StructuredContent)
	if result.StructuredContent != nil {
		result.StructuredContent = r.Value(result.StructuredContent, labels, false).(map[string]any)
	}
	for i, item := range result.Content {
		if item["type"] == "text" {
			if text, ok := item["text"].(string); ok {
				copy := map[string]any{}
				for k, v := range item {
					copy[k] = v
				}
				copy["text"] = r.Text(text, labels)
				result.Content[i] = copy
			}
		}
	}
	return result
}
