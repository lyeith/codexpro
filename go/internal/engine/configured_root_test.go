package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func configuredIdentityEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	home := filepath.Join(root, "state")
	managed := filepath.Join(root, "separate-managed-root")
	for _, directory := range []string{project, home, managed} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New(home)
	cfg.Set("worktreeMode", "mcp")
	cfg.Set("worktreeRoot", managed)
	catalog := map[string]any{
		"version": 1, "defaultProject": "fixture",
		"projects": []any{map[string]any{
			"id": "fixture", "label": "Private fixture", "root": project,
			"baseRef": "HEAD", "maxWorktrees": 4,
		}},
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ProjectsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	return &Engine{Config: cfg, Workspaces: manager}, managed
}

func TestManagedPrincipalUsesConfiguredRootAndPreservesIdentity(t *testing.T) {
	engine, managed := configuredIdentityEngine(t)
	identity := strings.Repeat("a", 48)
	file := filepath.Join(managed, "connector-id")
	if err := os.WriteFile(file, []byte(identity+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	defaultRoot := filepath.Join(engine.Config.Home, "worktrees")
	if err := os.MkdirAll(defaultRoot, 0700); err != nil {
		t.Fatal(err)
	}
	decoy := []byte(strings.Repeat("b", 48) + "\n")
	defaultFile := filepath.Join(defaultRoot, "connector-id")
	if err := os.WriteFile(defaultFile, decoy, 0600); err != nil {
		t.Fatal(err)
	}
	expected := "connector_" + workspace.Hash(identity)[:32]
	for attempt := 0; attempt < 2; attempt++ {
		principal, err := engine.PrincipalID()
		if err != nil || principal != expected {
			t.Fatalf("configured identity was not stable: %q %v", principal, err)
		}
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != identity+"\n" {
		t.Fatalf("configured identity changed: %q %v", data, err)
	}
	if data, err := os.ReadFile(defaultFile); err != nil || string(data) != string(decoy) {
		t.Fatalf("unused default identity changed: %q %v", data, err)
	}
}

func TestManagedPrincipalCreatesSourceIdentityFormatOnlyAtConfiguredRoot(t *testing.T) {
	engine, managed := configuredIdentityEngine(t)
	principal, err := engine.PrincipalID()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(managed, "connector-id")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile("^[0-9a-f]{48}\\n$").Match(raw) {
		t.Fatalf("new persistent identity has the wrong format: %q", raw)
	}
	if principal != "connector_"+workspace.Hash(strings.TrimSpace(string(raw)))[:32] {
		t.Fatal("principal does not bind its persisted connector identity")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("new connector identity does not have mode 0600: %v", err)
	}
	if _, err := os.Stat(filepath.Join(engine.Config.Home, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("default storage was touched: %v", err)
	}
}

func TestManagedPrincipalRejectsMalformedPersistentIdentity(t *testing.T) {
	for _, identity := range []string{"", strings.Repeat("A", 48), strings.Repeat("a", 47), strings.Repeat("g", 48), "\u0085" + strings.Repeat("a", 48) + "\u0085"} {
		t.Run("invalid-"+workspace.Hash(identity)[:8], func(t *testing.T) {
			engine, managed := configuredIdentityEngine(t)
			file := filepath.Join(managed, "connector-id")
			if err := os.WriteFile(file, []byte(identity+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := engine.PrincipalID()
			if err == nil || err.Error() != "Invalid persistent connector identity: "+file {
				t.Fatalf("invalid identity was accepted or changed its error: %v", err)
			}
		})
	}
}

func TestManagedPrincipalUsesECMAScriptWhitespace(t *testing.T) {
	engine, managed := configuredIdentityEngine(t)
	identity := strings.Repeat("a", 48)
	if err := os.WriteFile(filepath.Join(managed, "connector-id"), []byte("\ufeff"+identity+"\ufeff\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := engine.PrincipalID()
	if err != nil || got != "connector_"+workspace.Hash(identity)[:32] {
		t.Fatalf("BOM whitespace identity: %q %v", got, err)
	}
}
