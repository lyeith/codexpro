package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/engine"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func profileRedactorFixture(t *testing.T) (engine.Redactor, string) {
	t.Helper()
	cfg := config.New(t.TempDir())
	root := t.TempDir()
	cfg.AllowedRoots = []string{root}
	cfg.Set("codexDir", filepath.Join(cfg.Home, "empty-codex"))
	catalog := map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{
		map[string]any{"id": "fixture", "label": "Fixture", "root": root},
	}}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.ProjectsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return engine.Redactor{Config: cfg, Workspaces: manager}, root
}

func TestAdminAdapterDiscoversStoredProfileRootBeforeProjectFallback(t *testing.T) {
	redactor, root := profileRedactorFixture(t)
	value := map[string]any{
		"profile":   map[string]any{"root": root},
		"runtime":   map[string]any{"defaultRoot": root},
		"unrelated": "/outside-private-profile-location",
	}
	got := adminHTTPOptions(redactor).RedactValue(value, true).(map[string]any)
	if got["profile"].(map[string]any)["root"] != "[workspace:current]" ||
		got["runtime"].(map[string]any)["defaultRoot"] != "[workspace:current]" {
		t.Fatalf("HTTP profile lost payload-first root labels: %#v", got)
	}
	if got["unrelated"] != "[path]" {
		t.Fatalf("unknown path was not suppressed: %#v", got)
	}
	if value["profile"].(map[string]any)["root"] != root {
		t.Fatal("redaction mutated the source payload")
	}
}

func TestAdminAdapterKeepsFallbackAndDiscoveredIdentifierDistinct(t *testing.T) {
	redactor, root := profileRedactorFixture(t)
	opts := adminHTTPOptions(redactor)
	fallback := opts.RedactValue(map[string]any{"defaultRoot": root}, true).(map[string]any)
	if fallback["defaultRoot"] != "[project:fixture]" {
		t.Fatalf("no-root fallback changed: %#v", fallback)
	}
	discovered := opts.RedactValue(map[string]any{"root": root, "id": "stored", "copy": root}, true).(map[string]any)
	if discovered["root"] != "[workspace:stored]" || discovered["copy"] != "[workspace:stored]" {
		t.Fatalf("discovered root identifier was lost: %#v", discovered)
	}
}
