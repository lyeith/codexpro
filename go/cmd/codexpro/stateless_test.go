package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/engine"
	"github.com/lyeith/codexpro/go/internal/transport"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

// Principal state that used to live in an MCP session (the selected workspace
// and show_changes review checkpoints) carries across self-contained POSTs.
func TestStatelessRequestsShareReviewCheckpointsAndOpenWorkspaces(t *testing.T) {
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root, home := filepath.Join(base, "project"), filepath.Join(base, "state")
	for _, dir := range []string{root, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.txt")
	git("commit", "-q", "-m", "initial")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(home)
	cfg.Set("httpSessionMode", config.HTTPSessionStateless)
	cfg.Set("toolMode", "full") // exposes list_workspaces
	catalog := workspace.Catalog{Version: 1, DefaultProject: "main", Projects: []wire.Project{
		{ID: "main", Label: "Main", Root: root, BaseRef: "HEAD"},
		{ID: "other", Label: "Other", Root: base, BaseRef: "HEAD"},
	}}
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(cfg.ProjectsFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	principal, err := e.PrincipalID()
	if err != nil {
		t.Fatal(err)
	}
	server, err := transport.New(cfg, e.Registry, e, principal, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	remote := httptest.NewServer(server)
	t.Cleanup(remote.Close)
	token, err := cfg.Token()
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req, _ := http.NewRequest("POST", remote.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := remote.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		var reply map[string]any
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) && json.Unmarshal(line[6:], &reply) == nil {
				break
			}
		}
		result, _ := reply["result"].(map[string]any)
		structured, _ := result["structuredContent"].(map[string]any)
		if response.StatusCode != http.StatusOK || structured == nil || result["isError"] == true {
			t.Fatalf("%s: status %d %s", name, response.StatusCode, data)
		}
		return structured
	}
	main := workspace.IDForRoot(root)
	first := call("show_changes", map[string]any{"workspace_id": main, "since": "last_shown"})
	second := call("show_changes", map[string]any{"workspace_id": main, "since": "last_shown"})
	if first["review_checkpoint_hit"] != false || second["review_checkpoint_hit"] != true {
		t.Fatalf("last_shown did not carry across requests: %v then %v", first["review_checkpoint_hit"], second["review_checkpoint_hit"])
	}
	// A workspace opened in one request is listed by later requests.
	other := call("open_workspace", map[string]any{"project_id": "other"})
	listed := call("list_workspaces", map[string]any{})
	found := false
	for _, item := range listed["workspaces"].([]any) {
		found = found || item.(map[string]any)["id"] == other["workspace_id"]
	}
	if other["workspace_id"] != workspace.IDForRoot(base) || !found {
		t.Fatalf("open workspaces did not carry across requests: opened %v, listed %v", other["workspace_id"], listed)
	}
}
