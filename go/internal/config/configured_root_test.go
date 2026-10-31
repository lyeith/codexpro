package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredManagedIdentityRejectsLiveLeafAliasBeforeOpening(t *testing.T) {
	root := t.TempDir()
	cfg := New(filepath.Join(root, "state"))
	managed := filepath.Join(root, "separate-managed-root")
	cfg.Set("worktreeMode", "mcp")
	cfg.Set("worktreeRoot", managed)
	if err := os.MkdirAll(managed, 0700); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	// The target need not exist and is never opened or created. Validation must
	// reject the forbidden pathname before the identity reader can follow it.
	target := filepath.Join(home, ".codexpro", "sp6-nonexistent-"+filepath.Base(root))
	if err := os.Symlink(target, filepath.Join(managed, "connector-id")); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateIsolation(); err == nil {
		t.Fatal("custom connector-id alias into live state was accepted")
	}
	if _, err := os.Stat(cfg.Home); !os.IsNotExist(err) {
		t.Fatalf("isolation validation unexpectedly initialized private state: %v", err)
	}
}

func TestConfiguredManagedIdentityRejectsHardlinkAndAcceptsPrivateFile(t *testing.T) {
	root := t.TempDir()
	cfg := New(filepath.Join(root, "state"))
	managed := filepath.Join(root, "separate-managed-root")
	cfg.Set("worktreeMode", "mcp")
	cfg.Set("worktreeRoot", managed)
	if err := os.MkdirAll(managed, 0700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(managed, "connector-id")
	if err := os.WriteFile(identity, []byte(strings.Repeat("a", 48)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateIsolation(); err != nil {
		t.Fatalf("ordinary private identity rejected: %v", err)
	}
	alias := filepath.Join(root, "shared-identity")
	if err := os.Link(identity, alias); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateIsolation(); err == nil {
		t.Fatal("hardlinked configured connector identity was accepted")
	}
}
