package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type replayErrorResolver struct{ workspace wire.Workspace }

func (r replayErrorResolver) Resolve(context.Context, wire.Scope, string) (wire.Workspace, error) {
	return r.workspace, nil
}
func (r replayErrorResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.workspace.ProjectID, Root: r.workspace.Root}}
}
func (r replayErrorResolver) Project(id string) (wire.Project, bool) {
	return wire.Project{ID: r.workspace.ProjectID, Root: r.workspace.Root}, id == r.workspace.ProjectID
}

func replayErrorService(t *testing.T) (*Service, wire.Scope, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.New(t.TempDir())
	cfg.AllowedRoots = []string{root}
	resolver := replayErrorResolver{wire.Workspace{ID: "ws_error_contract", ProjectID: "fixture", Root: root, Mode: "direct"}}
	service, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return service, wire.Scope{PrincipalID: "error-contract", SessionID: "session", RequestID: "1", TransportID: "http"}, root
}

func assertDomainError(t *testing.T, err error, message string) {
	t.Helper()
	var domain *wire.Error
	if err == nil || !errors.As(err, &domain) {
		t.Fatalf("source CodexProError was returned as a generic error: %T %v", err, err)
	}
	if domain.Code != "" {
		t.Fatalf("uncoded source error acquired a new error code: %q", domain.Code)
	}
	result := wire.ErrorResult(err)
	expected := "CodexProError: " + message
	if !result.IsError || len(result.Content) != 1 || len(result.StructuredContent) != 1 || result.Content[0]["type"] != "text" ||
		result.Content[0]["text"] != expected || result.StructuredContent["error"] != expected {
		t.Fatalf("domain error wire contract differs: %+v", result)
	}
}

func TestReplayFileDomainErrorClassesMatchTypeScript(t *testing.T) {
	for _, test := range []struct {
		name, tool, path, seed, message string
		args                            map[string]any
		read                            bool
	}{
		{"past-eof", "read", "unicode.txt", "\ufeffalpha\r\ncafé ✓ 😀\r\nlast\r\n", "end_line (4) must be >= start_line (50).", map[string]any{"start_line": 50}, false},
		{"no-op", "edit", "a.txt", "a\nb\nc\n", "Tagged edit would not change a.txt. Re-read the file and remove no-op operations.", map[string]any{"edits": []any{map[string]any{"op": "replace", "start_line": 2, "content": "b"}}}, true},
		{"overlap", "edit", "a.txt", "a\nb\nc\n", "edits[1] overlaps edits[0]. All ranges must name disjoint lines in the original edit-tag snapshot.", map[string]any{"edits": []any{map[string]any{"op": "delete", "start_line": 1, "end_line": 3}, map[string]any{"op": "replace", "start_line": 2, "content": "overlap"}}}, true},
		{"bad-image", "view_image", "not-image.txt", "plain text\n", "Unsupported image format. Use PNG, JPEG, GIF, or WebP.", map[string]any{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scope, root := replayErrorService(t)
			if err := os.WriteFile(filepath.Join(root, test.path), []byte(test.seed), 0600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"workspace_id": "ws_error_contract", "path": test.path}
			for key, value := range test.args {
				args[key] = value
			}
			if test.read {
				read, err := service.Call(context.Background(), scope, "read", map[string]any{"workspace_id": "ws_error_contract", "path": test.path})
				if err != nil {
					t.Fatal(err)
				}
				args["edit_tag"] = read.StructuredContent["edit_tag"]
			}
			_, err := service.Call(context.Background(), scope, test.tool, args)
			assertDomainError(t, err, test.message)
			if actual, readErr := os.ReadFile(filepath.Join(root, test.path)); readErr != nil || string(actual) != test.seed {
				t.Fatalf("failed file operation changed its fixture: %q %v", actual, readErr)
			}
		})
	}
}

func TestReplayStaleWritePreservesCodexProErrorAndCurrentBytes(t *testing.T) {
	service, scope, root := replayErrorService(t)
	path := filepath.Join(root, "a.txt")
	current := []byte("new\n")
	if err := os.WriteFile(path, current, 0600); err != nil {
		t.Fatal(err)
	}
	oldHash := sha256.Sum256([]byte("old\n"))
	currentHash := sha256.Sum256(current)
	oldSHA, newSHA := hex.EncodeToString(oldHash[:]), hex.EncodeToString(currentHash[:])
	_, err := service.Call(context.Background(), scope, "write", map[string]any{
		"workspace_id": "ws_error_contract", "path": "a.txt", "content": "lost update\n", "expected_sha256": oldSHA})
	assertDomainError(t, err, "File changed since it was read: a.txt. Expected SHA-256 "+oldSHA+", found "+newSHA+". Read the file again before writing.")
	if actual, readErr := os.ReadFile(path); readErr != nil || string(actual) != string(current) {
		t.Fatalf("stale write changed current data: %q %v", actual, readErr)
	}
}

func TestReplayImportValidationPreservesDomainErrorClassWithoutEffects(t *testing.T) {
	t.Setenv("CODEXPRO_IMPORT_ALLOW_LOOPBACK", "")
	t.Setenv("CODEXPRO_IMPORT_ALLOWED_HOSTS", "")
	for _, test := range []struct{ name, url, id, message string }{
		{"empty-reference", "", "", "Unsupported attachment reference. Both download_url and file_id are required."},
		{"blocked-origin", "https://example.invalid/file", "fixture-file", "Attachment download_url host is not an approved ChatGPT file origin."},
		{"non-https", "http://example.invalid/file", "fixture-file", "Attachment download_url must use HTTPS."},
		{"file-scheme", "file:///tmp/fixture", "fixture-file", "Unsupported attachment reference scheme."},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scope, root := replayErrorService(t)
			_, err := service.Call(context.Background(), scope, "import_file", map[string]any{
				"workspace_id": "ws_error_contract", "destination": "download.bin",
				"file": map[string]any{"download_url": test.url, "file_id": test.id}})
			assertDomainError(t, err, test.message)
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("rejected import created files: %v %v", entries, readErr)
			}
		})
	}
}

// capabilitiesOps.ts169-171 and363 require the displayed skill path, not an
// absolute path. The source throws an ordinary Error for a failed skill lookup;
// fixing file validation must not globally convert such failures to CodexProError.
func TestReplaySkillPathMatchingPreservesNativeErrorClass(t *testing.T) {
	service, scope, root := replayErrorService(t)
	for rel, body := range map[string]string{
		".codex/skills/demo/SKILL.md":  "---\nname: demo\ndescription: Primary skill\n---\nPrimary body.\n",
		".agents/skills/demo/SKILL.md": "---\nname: demo\n---\nShadowed body.\n",
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	abs := filepath.Join(root, ".agents/skills/demo/SKILL.md")
	args := map[string]any{"workspace_id": "ws_error_contract", "name": "demo", "source": "workspace", "path": abs, "include_global_skills": false}
	_, err := service.Call(context.Background(), scope, "load_skill", args)
	var domain *wire.Error
	if err == nil || errors.As(err, &domain) {
		t.Fatalf("skill lookup must preserve source native Error: %T %v", err, err)
	}
	expected := "Error: Skill not found: demo at " + abs + ". Similar skills: demo [workspace]"
	result := wire.ErrorResult(err)
	if !result.IsError || result.Content[0]["text"] != expected || result.StructuredContent["error"] != expected {
		t.Fatalf("skill lookup error differs: %+v", result)
	}
	args["path"] = "$WORKSPACE/.agents/skills/demo/SKILL.md"
	loaded, err := service.Call(context.Background(), scope, "load_skill", args)
	if err != nil || loaded.IsError {
		t.Fatalf("literal displayed path must select the shadowed skill: %+v %v", loaded, err)
	}
	if text, ok := loaded.StructuredContent["text"].(string); !ok || !strings.Contains(text, "Shadowed body.") {
		t.Fatalf("display path selected the wrong skill: %+v", loaded.StructuredContent)
	}
}
