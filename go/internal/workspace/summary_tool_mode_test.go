package workspace

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSummaryTextMatchesConfiguredToolMode(t *testing.T) {
	for _, mode := range []string{"full", "minimal", "standard"} {
		t.Run(mode, func(t *testing.T) {
			manager, fileService, scope := fixture(t, false)
			manager.cfg.Set("toolMode", mode)
			service := NewService(manager, fileService)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			for _, tool := range []string{"open_current_workspace", "open_workspace"} {
				args := map[string]any{
					"include_tree": false, "include_skills": false, "include_global_skills": false,
				}
				if tool == "open_workspace" {
					args["project_id"] = "test"
				}
				result, err := service.Call(ctx, scope, tool, args)
				if err != nil || result.IsError {
					t.Fatalf("%s failed for configured %s: result=%+v error=%v", tool, mode, result, err)
				}
				structured, ok := result.StructuredContent["tool_mode"].(string)
				if !ok || structured != mode {
					t.Fatalf("%s structured tool_mode=%#v, configured %q", tool, result.StructuredContent["tool_mode"], mode)
				}
				if len(result.Content) != 1 || result.Content[0]["type"] != "text" {
					t.Fatalf("%s lost its single summary text block: %+v", tool, result.Content)
				}
				text, ok := result.Content[0]["text"].(string)
				if !ok {
					t.Fatalf("%s summary text has unexpected type: %T", tool, result.Content[0]["text"])
				}
				lines := []string{}
				for _, line := range strings.Split(text, "\n") {
					if strings.HasPrefix(line, "Tool mode: ") {
						lines = append(lines, line)
					}
				}
				if len(lines) != 1 || lines[0] != "Tool mode: "+structured {
					t.Fatalf("%s summary mode disagrees with configured/structured mode %q: %q", tool, mode, lines)
				}
			}
		})
	}
}
