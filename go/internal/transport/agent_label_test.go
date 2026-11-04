//go:build linux

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/activity"
	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/engine"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

// Exercise the advertised MCP field through the real dispatcher and read-only
// activity route, including a later unlabeled call on the same MCP session.
func TestAgentLabelMCPToAuditAndActivity(t *testing.T) {
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root, home := filepath.Join(base, "project"), filepath.Join(base, "state")
	for _, dir := range []string{root, home} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New(home)
	catalog, _ := json.Marshal(map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "root": root}}})
	if err := os.WriteFile(cfg.ProjectsFile, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "label.txt"), []byte("label fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := engine.NewPaused(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dispatcher.Close() })
	server, err := New(cfg, dispatcher.Registry, dispatcher, "label-fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	monitor := activity.RegisterRoutes(server.Mux, activity.Options{Config: cfg, Resolver: dispatcher.Workspaces, Audit: dispatcher.Audit, Jobs: dispatcher.Jobs})
	t.Cleanup(func() { _ = monitor.Close() })
	remote := httptest.NewServer(server)
	t.Cleanup(func() { _ = server.Close(); remote.Close() })
	session := initialize(t, remote, server.credential, "2025-11-25")

	call := func(id int, label any, include bool) map[string]any {
		t.Helper()
		args := map[string]any{"workspace_id": workspace.IDForRoot(root), "path": "label.txt"}
		if include {
			args["agent"] = label
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "read", "arguments": args}})
		status, _, data := request(t, remote, "POST", "/mcp", string(body), session, server.credential, nil)
		if status != 200 {
			t.Fatalf("tool status=%d", status)
		}
		result, ok := decodeRPC(t, data)["result"].(map[string]any)
		if !ok {
			t.Fatalf("missing tool result: %s", data)
		}
		return result
	}
	for i, label := range []string{"SP-3", ""} {
		if result := call(i+2, label, label != ""); result["isError"] == true {
			t.Fatalf("valid tool call failed: %v", result)
		}
	}
	for i, invalid := range []any{"SP 3", strings.Repeat("x", 33), 3, nil, ""} {
		result := call(i+4, invalid, true)
		encoded, _ := json.Marshal(result)
		if result["isError"] != true || !strings.Contains(string(encoded), "agent") {
			t.Fatalf("invalid label did not return a clear error: %s", encoded)
		}
	}
	retained, err := dispatcher.Audit.List(context.Background(), audit.Filter{ToolName: "read", Limit: 10})
	if err != nil || len(retained.Actions) != 2 {
		t.Fatalf("invalid labels reached dispatch: audit count=%d err=%v", len(retained.Actions), err)
	}
	if retained.Actions[0]["agent"] != "SP-3" {
		t.Fatalf("label lost from audit: %v", retained.Actions[0])
	}
	if _, exists := retained.Actions[1]["agent"]; exists {
		t.Fatal("unlabeled call inherited an agent")
	}
	for _, route := range []string{"/activity/projects/fixture.json", "/activity/projects/fixture.json?agent=SP-3"} {
		status, _, body := request(t, remote, "GET", route, "", "", server.credential, nil)
		if status != 200 {
			t.Fatalf("activity status=%d", status)
		}
		var value struct {
			Project struct {
				Recent []map[string]any `json:"recent_commands"`
			} `json:"project"`
		}
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(route, "?") {
			if len(value.Project.Recent) != 1 || value.Project.Recent[0]["agent"] != "SP-3" {
				t.Fatalf("filtered activity lost label: %s", body)
			}
		} else if len(value.Project.Recent) != 2 || value.Project.Recent[1]["agent"] != "SP-3" {
			t.Fatalf("activity lost labeled or unlabeled receipt: %s", body)
		} else if _, exists := value.Project.Recent[0]["agent"]; exists {
			t.Fatal("activity invented label on unlabeled receipt")
		}
	}
}
