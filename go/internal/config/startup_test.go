package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startupFixture(t *testing.T) (state, root string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEXPRO_") {
			t.Setenv(key, "")
			if e := os.Unsetenv(key); e != nil {
				t.Fatal(e)
			}
		}
	}
	t.Setenv("AST_GREP_PATH", "")
	base := t.TempDir()
	state = filepath.Join(base, "state")
	root = filepath.Join(base, "repo")
	if e := os.MkdirAll(state, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	writeCatalog(t, filepath.Join(state, "projects.json"), root)
	return state, root
}
func writeCatalog(t *testing.T, path, root string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "label": "Fixture", "root": root}}})
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func writeStartupProfile(t *testing.T, state, root string, profile map[string]any) string {
	t.Helper()
	real, e := filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256([]byte(real))
	path := filepath.Join(state, "profiles", hex.EncodeToString(sum[:])[:24]+".json")
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if _, present := profile["root"]; !present {
		profile["root"] = real
	}
	profile["version"] = 1
	data, _ := json.Marshal(profile)
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}
func startupArgs(state string) []string {
	return []string{"--state-dir", state, "--codex-dir", filepath.Join(state, "instructions")}
}
func TestPrivateStartupProfileAppliesOnlyAtLoad(t *testing.T) {
	state, root := startupFixture(t)
	args := startupArgs(state)
	before, e := Load(args)
	if e != nil {
		t.Fatal(e)
	}
	path := writeStartupProfile(t, state, root, map[string]any{"port": "7213", "toolMode": "full", "bash": "full", "write": "workspace", "handoffMode": "off", "codexSessions": "off", "work": "off", "bashTranscript": "full", "bashSession": "saved-1", "requireBashSession": true, "worktreeMode": "mcp", "worktreeBase": "main", "worktreeRoot": filepath.Join(state, "trees"), "maxWorktrees": "9", "token": strings.Repeat("never-use-this-", 6)})
	after, e := Load(args)
	if e != nil {
		t.Fatal(e)
	}
	if before.Listen != "127.0.0.1:7210" || before.String("worktreeMode", "") != "off" {
		t.Fatal("saving a profile mutated existing configuration")
	}
	if after.Listen != "127.0.0.1:7213" || after.String("toolMode", "") != "full" || after.String("bashTranscript", "") != "full" || after.String("worktreeMode", "") != "mcp" || after.String("worktreeBaseRef", "") != "main" || after.Int("maxWorktrees", 0) != 9 {
		t.Fatal("saved startup fields not applied")
	}
	if after.String("bashSessionId", "") != "saved-1" || !after.Bool("requireBashSession", false) || after.String("profilePath", "") != path {
		t.Fatal("profile identity/session fields drifted")
	}
	data, _ := json.Marshal(after)
	if strings.Contains(string(data), "never-use-this") {
		t.Fatal("saved credential entered runtime config")
	}
	if after.TokenFile != filepath.Join(state, "http-token") {
		t.Fatal("profile replaced the own credential file")
	}
	if _, e := os.Stat(after.TokenFile); !os.IsNotExist(e) {
		t.Fatal("config loading generated a credential as a side effect")
	}
}
func TestStartupPrecedenceCLIEnvironmentFileProfile(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{"port": "7213", "bashSession": "profile", "requireBashSession": true, "maxWorktrees": "9", "bashTranscript": "full"})
	if e := os.WriteFile(filepath.Join(state, "limits.env"), []byte("CODEXPRO_PORT=7214\nCODEXPRO_BASH_SESSION_ID=file\nCODEXPRO_MAX_JOBS=100\nCODEXPRO_BASH_TIMEOUT_MS=10\nIGNORED_SETTING=kept-private\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CODEXPRO_BASH_SESSION_ID", "environment")
	c, e := Load(append(startupArgs(state), "--listen", "127.0.0.1:7215", "--bash-session", "cli", "--require-bash-session=false", "--max-worktrees", "7"))
	if e != nil {
		t.Fatal(e)
	}
	if c.Listen != "127.0.0.1:7215" || c.String("bashSessionId", "") != "cli" || c.Bool("requireBashSession", true) || c.Int("maxWorktrees", 0) != 7 {
		t.Fatal("explicit CLI did not override inherited settings")
	}
	if c.Int("maxJobs", 0) != 64 || c.Int("bashTimeoutMs", 0) != 1000 {
		t.Fatal("startup numeric limits must clamp like the baseline")
	}
	c, e = Load(startupArgs(state))
	if e != nil {
		t.Fatal(e)
	}
	if c.Listen != "127.0.0.1:7214" || c.String("bashSessionId", "") != "environment" || c.String("bashTranscript", "") != "full" {
		t.Fatal("environment/file/profile precedence drift")
	}
	prior := c.Int("maxJobs", 0)
	if e = c.Reload(); e == nil || c.Int("maxJobs", 0) != prior {
		t.Fatal("runtime reload must strictly reject the out-of-range startup value")
	}
}
func TestStartupProfileCatalogOverrideAndIdentity(t *testing.T) {
	state, root := startupFixture(t)
	alternate := filepath.Join(state, "alternate.json")
	writeCatalog(t, alternate, root)
	writeStartupProfile(t, state, root, map[string]any{"projectsFile": alternate, "port": "7213"})
	c, e := Load(startupArgs(state))
	if e != nil || c.ProjectsFile != alternate {
		t.Fatal("saved catalog was not selected", e)
	}
	original := filepath.Join(state, "projects.json")
	c, e = Load(append(startupArgs(state), "--projects-file", original))
	if e != nil || c.ProjectsFile != original {
		t.Fatal("explicit catalog did not override profile", e)
	}
	writeStartupProfile(t, state, root, map[string]any{"root": filepath.Join(state, "different-root"), "port": "7200", "bash": "safe"})
	c, e = Load(startupArgs(state))
	if e != nil || c.Listen != "127.0.0.1:7210" || c.String("profilePath", "") != "" {
		t.Fatal("profile for a different canonical root was applied", e)
	}
}
func TestStartupRetiredModesRejectedButExplicitOverridesWork(t *testing.T) {
	for _, tc := range []struct{ key, value, flag, override string }{{"bash", "safe", "bash", "full"}, {"write", "off", "write", "workspace"}, {"handoffMode", "on", "handoff-mode", "off"}, {"codexSessions", "metadata", "codex-sessions", "off"}, {"work", "on", "work", "off"}, {"mode", "pro", "mode", "agent"}} {
		t.Run(tc.key, func(t *testing.T) {
			state, root := startupFixture(t)
			writeStartupProfile(t, state, root, map[string]any{tc.key: tc.value})
			if _, e := Load(startupArgs(state)); e == nil {
				t.Fatal("retired permission/operating mode was accepted")
			}
			if _, e := Load(append(startupArgs(state), "--"+tc.flag, tc.override)); e != nil {
				t.Fatal("explicit retained override rejected", e)
			}
		})
	}
	for _, mode := range []string{"minimal", "standard", "full"} {
		t.Run("tier-"+mode, func(t *testing.T) {
			state, _ := startupFixture(t)
			c, e := Load(append(startupArgs(state), "--tool-mode", mode))
			if e != nil || c.String("toolMode", "") != mode {
				t.Fatal("inventory tier confused with permission", e)
			}
		})
	}
}
func TestStartupRejectsLivePortsAndAllEffectiveLivePaths(t *testing.T) {
	state, root := startupFixture(t)
	for _, listen := range []string{"127.0.0.1:7200", "127.0.0.1:07200", "[::1]:7200", "localhost:7200", ":7200"} {
		if _, e := Load(append(startupArgs(state), "--listen", listen)); e == nil {
			t.Fatalf("live listener alias accepted: %s", listen)
		}
	}
	writeStartupProfile(t, state, root, map[string]any{"port": "7200"})
	if _, e := Load(startupArgs(state)); e == nil {
		t.Fatal("saved live port accepted")
	}
	if _, e := Load(append(startupArgs(state), "--listen", "127.0.0.1:7210")); e != nil {
		t.Fatal("explicit private listener should override saved port", e)
	}
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	protected := filepath.Join(home, ".codexpro", "sp6-must-not-be-opened")
	for _, flag := range []string{"state-dir", "token-file", "projects-file", "limits-file", "worktree-root", "work-history", "codex-dir", "ast-grep-path", "host-resources-helper"} {
		args := append(startupArgs(state), "--listen", "127.0.0.1:7210", "--"+flag, protected)
		if _, e := Load(args); e == nil {
			t.Fatalf("%s allowed forbidden live state", flag)
		}
	}
	writeStartupProfile(t, state, root, map[string]any{"projectsFile": filepath.Join(home, ".config", "codexpro", "projects.json")})
	if _, e := Load(startupArgs(state)); e == nil {
		t.Fatal("saved live catalog accepted")
	}
}
func TestStartupRejectsProfileSymlinkAndDoesNotDiscloseMalformedData(t *testing.T) {
	state, root := startupFixture(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(state, "profiles")); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(startupArgs(state)); e == nil || !strings.Contains(e.Error(), "escapes") {
		t.Fatal("escaped profile symlink accepted", e)
	}
	if e := os.Remove(filepath.Join(state, "profiles")); e != nil {
		t.Fatal(e)
	}
	path := writeStartupProfile(t, state, root, map[string]any{})
	sensitive := strings.Repeat("private-value-", 6)
	if e := os.WriteFile(path, []byte("{"+sensitive), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(startupArgs(state)); e == nil || strings.Contains(e.Error(), sensitive) {
		t.Fatal("profile parse failure missing or disclosed contents")
	}
}
func TestStartupSessionContextAndAuthValidation(t *testing.T) {
	state, _ := startupFixture(t)
	for _, args := range [][]string{{"--require-bash-session"}, {"--bash-session", "bad space"}, {"--worktree-base", "--bad"}, {"--context-dir", "../outside"}, {"--context-dir", ".safe/src/path"}, {"--widget-domain", "http://example.test"}, {"--listen", "127.0.0.1:7210", "--port", "7211"}} {
		if _, e := Load(append(startupArgs(state), args...)); e == nil {
			t.Fatalf("invalid startup options accepted: %#v", args)
		}
	}
	t.Setenv("CODEXPRO_AUTH_MODE", "cloudflare-access")
	if _, e := Load(startupArgs(state)); e == nil {
		t.Fatal("unsupported auth mode accepted")
	}
	t.Setenv("CODEXPRO_AUTH_MODE", "static-token")
	t.Setenv("CODEXPRO_ALLOW_NO_HTTP_TOKEN", "1")
	if _, e := Load(startupArgs(state)); e == nil {
		t.Fatal("missing-token mode accepted")
	}
}
func TestTokenRejectsSharedInodeAndOversizeBeforeUsingContents(t *testing.T) {
	c := New(t.TempDir())
	source := filepath.Join(c.Home, "shared")
	if e := os.WriteFile(source, []byte(strings.Repeat("s", 48)), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(source, c.TokenFile); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Token(); e == nil || !strings.Contains(e.Error(), "hard link") {
		t.Fatal("shared token inode accepted", e)
	}
	if e := os.Remove(c.TokenFile); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(c.TokenFile, []byte(strings.Repeat("s", 16385)), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Token(); e == nil {
		t.Fatal("oversized token accepted")
	}
}
