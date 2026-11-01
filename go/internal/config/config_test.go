package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolationRejectsResolvedTypeScriptStatePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := New(filepath.Join(home, "private"))
	live := filepath.Join(home, ".codexpro")
	if e := os.Mkdir(live, 0700); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(home, "alias")
	if e := os.Symlink(live, alias); e != nil {
		t.Fatal(e)
	}
	c.TokenFile = filepath.Join(alias, "future", "token")
	if e := c.ValidateIsolation(); e == nil {
		t.Fatal("future path beneath a live-state symlink was accepted")
	}
	if _, e := os.Stat(filepath.Join(live, "future")); !os.IsNotExist(e) {
		t.Fatal("validation wrote to live state")
	}
	c.TokenFile = filepath.Join(c.Home, "token")
	if e := c.ValidateIsolation(); e != nil {
		t.Fatal(e)
	}
}

// The production service listens on the codexpro.socket port and reads the
// shared operator configuration directory.
func TestProductionPortAndSharedConfigurationAreAccepted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, ".config", "codexpro")
	if e := os.MkdirAll(shared, 0700); e != nil {
		t.Fatal(e)
	}
	c := New(filepath.Join(home, ".codexpro-go", "state"))
	c.Listen = "0.0.0.0:7200"
	c.TokenFile = filepath.Join(shared, "http-token")
	c.ProjectsFile = filepath.Join(shared, "projects.json")
	c.EnvFile = filepath.Join(shared, "env")
	if e := c.ValidateIsolation(); e != nil {
		t.Fatalf("production configuration rejected: %v", e)
	}
	c.Home = filepath.Join(home, ".codexpro", "go-state")
	if e := c.ValidateIsolation(); e == nil {
		t.Fatal("state inside the TypeScript state tree was accepted")
	}
}
func TestPrivateTokenPersistsAndRequiresPrivatePermissions(t *testing.T) {
	c := New(t.TempDir())
	a, e := c.Token()
	if e != nil {
		t.Fatal(e)
	}
	b, e := c.Token()
	if e != nil {
		t.Fatal(e)
	}
	if a != b || len(a) < 48 || strings.ContainsAny(a, " \t\r\n") {
		t.Fatal("unstable or malformed private credential")
	}
	if e := os.Chmod(c.TokenFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Token(); e == nil {
		t.Fatal("world-readable credential accepted")
	}
}
func TestReloadIsAtomic(t *testing.T) {
	c := New(t.TempDir())
	if e := os.WriteFile(c.EnvFile, []byte("CODEXPRO_MAX_JOBS=3\nCODEXPRO_MAX_OUTPUT_BYTES=oops\n"), 0600); e != nil {
		t.Fatal(e)
	}
	before := c.Int("maxJobs", 0)
	if e := c.Reload(); e == nil {
		t.Fatal("invalid limit accepted")
	}
	if c.Int("maxJobs", 0) != before {
		t.Fatal("partial update escaped rejected reload")
	}
}
