//go:build linux

package transport

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestBootstrapPersonalProjectSessionMethods(t *testing.T) {
	path, _, devices := namedPolicy(t)
	server, _, _, _ := newHTTPFixture(t, nil)
	server.Config.Set("principalsFile", path)
	b, err := NewBootstrap(server.Config, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	remote := httptest.NewServer(b)
	t.Cleanup(remote.Close)

	if code, headers, _ := request(t, remote, "GET", "/mcp", "", "", devices[0], nil); code != 503 || headers.Get("Retry-After") != "1" {
		t.Fatalf("starting restricted GET returned %d with Retry-After %q", code, headers.Get("Retry-After"))
	}
	if code, _, _ := request(t, remote, "GET", "/admin/profile", "", "", devices[0], nil); code != 403 {
		t.Fatalf("starting restricted admin GET returned %d", code)
	}
	if err := b.Publish(server); err != nil {
		t.Fatal(err)
	}
	own := initialize(t, remote, devices[0], "2025-11-25")
	crossed := initialize(t, remote, devices[1], "2025-11-25")
	for _, tc := range []struct {
		method, session, credential string
		want                        int
		allow                       string
	}{
		{"GET", own, devices[0], 405, "POST, DELETE"},
		{"GET", "", devices[0], 405, "POST, DELETE"},
		{"GET", crossed, devices[0], 403, ""},
		{"GET", "00000000-0000-0000-0000-000000000000", devices[0], 403, ""},
		{"GET", "malformed", devices[0], 403, ""},
		{"GET", own, "", 401, ""},
		{"DELETE", own, devices[0], 200, ""},
		{"GET", own, devices[0], 403, ""},
	} {
		status, headers, _ := request(t, remote, tc.method, "/mcp", "", tc.session, tc.credential, nil)
		if status != tc.want || headers.Get("Allow") != tc.allow {
			t.Fatalf("published %s status/Allow = %d/%q, want %d/%q", tc.method, status, headers.Get("Allow"), tc.want, tc.allow)
		}
	}
}

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
		if scope.Identity.AgentID != p.Agents[0].ID {
			t.Error("read lost its registered agent identity")
		}
		calls <- wire.ProjectsFrom(ctx)
		return wire.Success("read", "read", nil, "ok"), nil
	})
	s.Config.Set("principalsFile", path)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	for _, id := range []string{"", session} {
		code, headers, _ := request(t, remote, "GET", "/mcp", "", id, tokens[0], nil)
		if code != 405 || headers.Get("Allow") != "POST, DELETE" {
			t.Fatalf("restricted GET /mcp returned %d with Allow %q", code, headers.Get("Allow"))
		}
	}
	crossed := initialize(t, remote, tokens[1], "2025-11-25")
	for _, id := range []string{"", "malformed", "00000000-0000-0000-0000-000000000000", crossed} {
		code, headers, _ := request(t, remote, "GET", "/mcp", "", "", tokens[0], map[string]string{"Mcp-Session-Id": id})
		if code != 403 || headers.Get("Allow") != "" {
			t.Fatalf("GET /mcp with an invalid or unowned session returned %d with Allow %q", code, headers.Get("Allow"))
		}
	}
	if code, headers, _ := request(t, remote, "GET", "/mcp", "", session, "", nil); code != 401 || headers.Get("Allow") != "" {
		t.Fatalf("unauthenticated GET /mcp returned %d with Allow %q", code, headers.Get("Allow"))
	}
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
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		names = append(names, name)
		schema := tool["inputSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if _, ok := properties["agent"]; ok {
			t.Fatalf("restricted %s advertises a caller-set agent", name)
		}
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
	for _, route := range []string{"/", "/healthz", "/mcp/", "/m%63p", "/activity/live", "/activity/job", "/activity.json", "/activity/diff", "/usage/v1/pro", "/inbox/v1/", "/admin/profile", "/setup"} {
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
		const call = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file"}}}`
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
	if code, _, _ := request(t, remote, "DELETE", "/mcp", "", session, tokens[0], nil); code != 200 {
		t.Fatalf("restricted DELETE /mcp returned %d", code)
	}
}
