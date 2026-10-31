package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var capturedReleaseBlockedGlobs = []string{
	".git", ".git/**", "**/.git/**", "node_modules", "node_modules/**", "**/node_modules/**",
	".env", ".env/**", ".env.*", ".env.*/**", "**/.env", "**/.env/**", "**/.env.*", "**/.env.*/**",
	"**/*.pem", "**/*.key", "**/id_rsa", "**/id_rsa.*", "**/id_ed25519", "**/id_ed25519.*", "**/.ssh/**",
	".codexpro", ".codexpro/**", "**/.codexpro/**", "dist", "dist/**", "**/dist/**",
	"build", "build/**", "**/build/**", ".next", ".next/**", "**/.next/**",
	"coverage", "coverage/**", "**/coverage/**", ".cache", ".cache/**", "**/.cache/**",
}

func TestDefaultBlockedGlobsMatchCapturedTypeScriptRelease(t *testing.T) {
	if actual := New(t.TempDir()).BlockedGlobs; !reflect.DeepEqual(actual, capturedReleaseBlockedGlobs) {
		t.Fatalf("default blocked globs differ from the captured release: %#v", actual)
	}
}

func TestExplicitBlockedGlobsStillExtendReleaseDefaults(t *testing.T) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEXPRO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	extra := []string{"**/secrets/**", "**/*.sqlite", "**/*.sqlite3", "**/*.db", "**/.venv/**", "**/__pycache__/**"}
	limits := filepath.Join(home, "limits.env")
	if err := os.WriteFile(limits, []byte("CODEXPRO_BLOCKED_GLOBS="+strings.Join(extra, ",")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"--state-dir", home, "--listen", "127.0.0.1:7210", "--limits-file", limits,
		"--codex-dir", filepath.Join(home, "empty-codex"), "--ast-grep-path", ""})
	if err != nil {
		t.Fatal(err)
	}
	expected := append(append([]string{}, capturedReleaseBlockedGlobs...), extra...)
	if !reflect.DeepEqual(cfg.BlockedGlobs, expected) {
		t.Fatalf("explicit private extension was ignored or changed: %#v", cfg.BlockedGlobs)
	}
}
