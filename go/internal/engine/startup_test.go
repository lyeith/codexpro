package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestPausedEngineServesRealFileReadBeforeBackgroundActivation(t *testing.T) {
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root, home := filepath.Join(base, "fixture"), filepath.Join(base, "state")
	for _, path := range []string{root, home} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New(home)
	raw, _ := json.Marshal(map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "root": root}}})
	if err := os.WriteFile(cfg.ProjectsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready.txt"), []byte("file reads do not need host recovery\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := NewPaused(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Jobs.BackgroundReady() || e.maintenanceCancel != nil {
		t.Fatal("paused constructor started background maintenance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := e.Call(ctx, wire.Scope{PrincipalID: "fixture-owner", SessionID: "fixture-session", RequestID: "read-before-start"}, "read", map[string]any{"workspace_id": workspace.IDForRoot(root), "path": "ready.txt", "start_line": 1, "end_line": 1})
	if err != nil || result.IsError || result.StructuredContent["sha256"] == nil {
		t.Fatalf("paused engine cannot serve file reads: %v %+v", err, result)
	}
	e.StartBackground()
	if !e.Jobs.BackgroundReady() || e.maintenanceCancel == nil {
		t.Fatal("explicit activation did not start maintenance")
	}
}
