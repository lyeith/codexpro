package engine

import (
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// src/pathLabels.ts registers config.worktreeRoot after payload/project roots.
// A failed operation has no payload root, so the configured storage fallback
// must be available independently of successful workspace discovery.
func TestRedactorUsesConfiguredWorktreeStorageForRootlessErrors(t *testing.T) {
	e, _, _ := auditTargetFixture(t)
	custom := filepath.Join(t.TempDir(), "managed-checkouts")
	e.Config.Set("worktreeRoot", custom)
	r := Redactor{Config: e.Config, Workspaces: e.Workspaces}
	text := "Error: missing " + filepath.Join(custom, "lease", "file.txt")
	result := r.Result(wire.Result{
		StructuredContent: map[string]any{"error": text},
		Content:           []wire.Content{{"type": "text", "text": text}},
	})
	want := "Error: missing [worktree-storage]/lease/file.txt"
	if result.StructuredContent["error"] != want || result.Content[0]["text"] != want {
		t.Fatalf("rootless error lost configured storage label: %#v", result)
	}

	// The exact source gives an explicitly discovered workspace first priority.
	result = r.Result(wire.Result{StructuredContent: map[string]any{
		"root": custom, "workspace_id": "managed", "error": text,
	}})
	if result.StructuredContent["error"] != "Error: missing [workspace:managed]/lease/file.txt" {
		t.Fatalf("fallback displaced discovered workspace: %#v", result)
	}
}

func TestRedactorUsesDefaultWorktreeStorageWhenUnsetOrEmpty(t *testing.T) {
	e, _, _ := auditTargetFixture(t)
	r := Redactor{Config: e.Config, Workspaces: e.Workspaces}
	text := "Error: missing " + filepath.Join(e.Config.Home, "worktrees", "lease", "file.txt")
	for _, value := range []any{nil, ""} {
		e.Config.Set("worktreeRoot", value)
		got := r.Text(text, r.Labels(nil))
		if got != "Error: missing [worktree-storage]/lease/file.txt" {
			t.Fatalf("empty configured root changed storage fallback: %q", got)
		}
	}
}
