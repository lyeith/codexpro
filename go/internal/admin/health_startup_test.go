package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestEarlyHealthUsesExactSteadyHealthHandler(t *testing.T) {
	root := t.TempDir()
	cfg := config.New(filepath.Join(root, "state"))
	if err := os.MkdirAll(cfg.Home, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "label": "Private fixture", "root": root}}})
	if err := os.WriteFile(cfg.ProjectsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := workspace.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	early, err := NewHealthHandler(cfg, manager, nil)
	if err != nil {
		t.Fatal(err)
	}
	steady := (&Service{Config: cfg, Workspaces: manager}).HealthHandler(nil)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			first, second := httptest.NewRecorder(), httptest.NewRecorder()
			early.ServeHTTP(first, httptest.NewRequest(method, "/healthz", nil))
			steady.ServeHTTP(second, httptest.NewRequest(method, "/healthz", nil))
			if first.Code != second.Code || first.Body.String() != second.Body.String() || first.Header().Get("Content-Type") != second.Header().Get("Content-Type") {
				t.Fatalf("early health differs from steady contract: %d/%d %q/%q", first.Code, second.Code, first.Body.String(), second.Body.String())
			}
			if method == http.MethodGet {
				var body map[string]any
				if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["ok"] != true || body["name"] != "CodexPro" || body["defaultProjectId"] != "fixture" {
					t.Fatalf("source health fields changed: %+v", body)
				}
				if _, exists := body["ready"]; exists {
					t.Fatal("startup invented a health field")
				}
			}
		})
	}
}
