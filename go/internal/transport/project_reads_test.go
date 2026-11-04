//go:build linux

package transport

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestPersonalProjectTransport(t *testing.T) {
	path, p, tokens := namedPolicy(t)
	p.Principals[0].Role, p.Principals[0].Projects = "owner", []string{"one", "two", "projects"}
	p.Agents[0].Kind, p.Agents[0].ProjectID = "concierge", "one"
	savePolicy(t, path, p)

	calls := make(chan []string, 8)
	s, remote, _, _ := newHTTPFixture(t, func(ctx context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		if !scope.Identity.Restricted() {
			t.Error("personal owner bypassed its agent kind")
		}
		calls <- wire.ProjectsFrom(ctx)
		return wire.Success("read", "read", nil, "ok"), nil
	})
	s.Config.Set("principalsFile", path)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	const list = `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`
	status, _, body := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil)
	if status != 200 {
		t.Fatalf("tools/list returned %d", status)
	}
	result, ok := decodeRPC(t, body)["result"].(map[string]any)
	if !ok {
		t.Fatal("missing discovery result")
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("missing advertised tools")
	}
	var names []string
	for _, raw := range tools {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"activity_get", "activity_list", "list_projects", "read", "search", "tree", "view_image"}) {
		t.Fatalf("unexpected personal tools: %v", names)
	}
	for _, name := range []string{"bash", "write", "apply_patch", "open_workspace", "start_jobs", "jobs", "batch", "server_config", "activity_export", "future_tool"} {
		call := fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name)
		status, _, body := request(t, remote, "POST", "/mcp", call, session, tokens[0], nil)
		if status != 200 || decodeRPC(t, body)["error"] == nil {
			t.Fatalf("%s was not refused", name)
		}
	}
	for _, op := range []string{`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`} {
		if status, _, _ := request(t, remote, "POST", "/mcp", op, session, tokens[0], nil); status != 403 {
			t.Fatalf("alternate RPC returned %d", status)
		}
	}
	for _, route := range []string{"/", "/healthz", "/mcp", "/activity/live", "/activity/job", "/activity.json", "/activity/diff", "/usage/v1/pro", "/inbox/v1/", "/admin/profile", "/setup"} {
		if status, _, _ := request(t, remote, "GET", route, "", session, tokens[0], nil); status != 403 {
			t.Fatalf("GET %s returned %d", route, status)
		}
	}
	for _, route := range []string{"/activity/batch", "/usage/v1/pro/reset", "/inbox/v1/", "/admin/profile", "/setup"} {
		if status, _, _ := request(t, remote, "POST", route, "{}", session, tokens[0], nil); status != 403 {
			t.Fatalf("POST %s returned %d", route, status)
		}
	}
	if len(calls) != 0 {
		t.Fatal("refused operation reached dispatcher")
	}
	check := func(want []string) {
		t.Helper()
		const call = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file","agent":"SP5"}}}`
		status, _, _ := request(t, remote, "POST", "/mcp", call, session, tokens[0], map[string]string{"X-Projects": "forged", "X-Principal-ID": "beta"})
		if status != 200 {
			t.Fatalf("read returned %d", status)
		}
		select {
		case got := <-calls:
			if !slices.Equal(got, want) {
				t.Fatalf("dispatch grants %v, want %v", got, want)
			}
		default:
			t.Fatal("read did not reach dispatcher")
		}
	}
	check([]string{"one"})
	p.Agents[0].ProjectID = ""
	savePolicy(t, path, p)
	check([]string{"one", "two"})
	p.Principals[0].Projects = nil
	savePolicy(t, path, p)
	check(nil)
}
