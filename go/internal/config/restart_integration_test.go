package config_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/files"
	gitservice "github.com/lyeith/codexpro/go/internal/git"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestSavedAdminProfileAndStartupOnlyEnvironmentApplyOnNextLoad(t *testing.T) {
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
	state := filepath.Join(base, "state")
	root := filepath.Join(base, "repo")
	if e := os.MkdirAll(state, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	catalog, _ := json.Marshal(map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "label": "Fixture", "root": root}}})
	if e := os.WriteFile(filepath.Join(state, "projects.json"), catalog, 0600); e != nil {
		t.Fatal(e)
	}
	args := []string{"--state-dir", state, "--codex-dir", filepath.Join(state, "instructions")}
	current, e := config.Load(args)
	if e != nil {
		t.Fatal(e)
	}
	manager, e := workspace.New(current)
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Close()
	fs, e := files.New(current, manager)
	if e != nil {
		t.Fatal(e)
	}
	git, e := gitservice.New(current, manager, fs, nil)
	if e != nil {
		t.Fatal(e)
	}
	registry, e := contracts.New()
	if e != nil {
		t.Fatal(e)
	}
	controller, e := admin.New(current, manager, fs, git, registry, nil)
	if e != nil {
		t.Fatal(e)
	}
	mux := http.NewServeMux()
	controller.RegisterRoutes(mux, nil)
	payload, _ := json.Marshal(map[string]any{"port": 7216, "worktreeMode": "mcp", "worktreeRoot": filepath.Join(state, "next-trees"), "bashSession": "next-session", "requireBashSession": true})
	request := httptest.NewRequest("POST", "http://127.0.0.1:7210/admin/profile", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("profile save status=%d body=%s", response.Code, response.Body.String())
	}
	if current.Listen != "127.0.0.1:7210" || current.String("worktreeMode", "") != "off" {
		t.Fatal("profile save mutated running configuration")
	}
	next, e := config.Load(args)
	if e != nil {
		t.Fatal(e)
	}
	if next.Listen != "127.0.0.1:7216" || next.String("worktreeMode", "") != "mcp" || next.String("worktreeRoot", "") != filepath.Join(state, "next-trees") || next.String("bashSessionId", "") != "next-session" || !next.Bool("requireBashSession", false) {
		t.Fatal("admin profile did not apply to next startup")
	}
	if e := os.WriteFile(next.EnvFile, []byte("CODEXPRO_TOOL_MODE=full\nCODEXPRO_PORT=7217\nCODEXPRO_MAX_JOBS=4\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = next.Reload(); e != nil {
		t.Fatal(e)
	}
	if next.Listen != "127.0.0.1:7216" || next.String("toolMode", "") != "standard" || next.Int("maxJobs", 0) != 4 {
		t.Fatal("reload changed startup settings or missed operational limit")
	}
	restarted, e := config.Load(args)
	if e != nil {
		t.Fatal(e)
	}
	if restarted.Listen != "127.0.0.1:7217" || restarted.String("toolMode", "") != "full" || restarted.Int("maxJobs", 0) != 4 {
		t.Fatal("startup-only environment settings were not applied on next load")
	}
}
