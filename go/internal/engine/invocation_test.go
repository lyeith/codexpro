package engine

import (
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
)

func TestBatchAuditClassificationMatchesSourceCases(t *testing.T) {
	for _, item := range []struct {
		name                 string
		args                 map[string]any
		connectionTest, want bool
	}{
		{"read", map[string]any{"operations": []any{map[string]any{"tool": "read"}}}, false, false},
		{"default bash persistence", map[string]any{"operations": []any{map[string]any{"tool": "bash"}}}, false, true},
		{"empty checkpoint", map[string]any{"checkpoint": map[string]any{}}, false, true},
		{"checkpoint during connection test", map[string]any{"checkpoint": map[string]any{}}, true, true},
		{"explicit persistence", map[string]any{"persist": true}, false, true},
		{"rejected persistence during connection test", map[string]any{"persist": true}, true, false},
		{"stored recipe", map[string]any{"path": "recipe.json"}, true, true},
		{"whitespace path", map[string]any{"path": " \ufeff\t "}, false, false},
		{"disallowed lifecycle child", map[string]any{"operations": []any{map[string]any{"tool": "create_workspace"}}}, false, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			cfg := config.New(t.TempDir())
			cfg.Set("connectionTest", item.connectionTest)
			e := &Engine{Config: cfg}
			if got := e.invocationMutating("batch", item.args); got != item.want {
				t.Fatalf("mutating=%t, want %t", got, item.want)
			}
		})
	}
}
