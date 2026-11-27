package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const staleSession = "6f1c2a4e-9b7d-4c3e-8a21-5d0f7e9b1c33"

func readCall(id int, path string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":%q}}}`, id, path)
}

func echoCaller(scopes chan<- wire.Scope) testCaller {
	return func(_ context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
		if scopes != nil {
			scopes <- scope
		}
		return wire.Success(name, "read", map[string]any{"path": args["path"]}, "echo "+fmt.Sprint(args["path"])), nil
	}
}

func rpcResult(t *testing.T, status int, data []byte) map[string]any {
	t.Helper()
	reply := decodeRPC(t, data)
	result, ok := reply["result"].(map[string]any)
	if status != http.StatusOK || !ok || reply["error"] != nil {
		t.Fatalf("status=%d reply=%.500s", status, data)
	}
	return result
}

func TestStatelessCallsNeedNoSessionAndIgnoreStaleOnes(t *testing.T) {
	scopes := make(chan wire.Scope, 8)
	s, remote, token, _ := newHTTPFixture(t, echoCaller(scopes))
	if !s.stateless {
		t.Fatal("default HTTP mode is not stateless")
	}
	// initialize still works and issues no session.
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"stateless-test","version":"1"}}}`
	status, headers, data := request(t, remote, "POST", "/mcp", init, "", token, nil)
	result := rpcResult(t, status, data)
	if result["protocolVersion"] != "2025-11-25" || headers.Get("Mcp-Session-Id") != "" {
		t.Fatalf("initialize: %v session=%q", result, headers.Get("Mcp-Session-Id"))
	}
	if caps, _ := result["capabilities"].(map[string]any); caps != nil {
		if toolsCap, _ := caps["tools"].(map[string]any); toolsCap["listChanged"] == true {
			t.Fatalf("stateless mode advertised tools.listChanged=true: %v", caps)
		}
	}
	// Initialize never negotiates the sessionless revision.
	future := `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{},"clientInfo":{"name":"stateless-test","version":"1"}}}`
	status, _, data = request(t, remote, "POST", "/mcp", future, "", token, nil)
	if result := rpcResult(t, status, data); result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("unknown version negotiated %v", result["protocolVersion"])
	}
	cases := []struct{ name, session string }{
		{"no session, no initialize", ""},
		{"stale session from an earlier process", staleSession},
		{"malformed session", "not-a-session"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, headers, data := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`, tc.session, token, map[string]string{"MCP-Protocol-Version": "2025-11-25"})
			if tools, _ := rpcResult(t, status, data)["tools"].([]any); len(tools) == 0 || headers.Get("Mcp-Session-Id") != "" {
				t.Fatalf("tools/list returned %d tools, session %q", len(tools), headers.Get("Mcp-Session-Id"))
			}
			path := fmt.Sprintf("file-%d", i)
			status, _, data = request(t, remote, "POST", "/mcp", readCall(8, path), tc.session, token, map[string]string{"MCP-Protocol-Version": "2025-11-25"})
			result := rpcResult(t, status, data)
			if structured, _ := result["structuredContent"].(map[string]any); structured["path"] != path {
				t.Fatalf("tools/call result %v", result)
			}
			scope := <-scopes
			if scope.SessionID != "" || scope.TransportID != "" || scope.PrincipalID != "fixture-principal" || scope.Identity.Source != "legacy" {
				t.Fatalf("stateless scope %+v", scope)
			}
		})
	}
}

func TestStatelessGetAndDeleteAre405(t *testing.T) {
	_, remote, token, _ := newHTTPFixture(t, nil)
	for _, method := range []string{"GET", "DELETE"} {
		for _, session := range []string{"", staleSession} {
			status, headers, data := request(t, remote, method, "/mcp", "", session, token, nil)
			reply := decodeRPC(t, data)
			rpcErr, _ := reply["error"].(map[string]any)
			if status != http.StatusMethodNotAllowed || headers.Get("Allow") != "POST" || reply["jsonrpc"] != "2.0" || rpcErr["code"] != float64(-32000) {
				t.Fatalf("%s session=%q: status=%d Allow=%q body=%s", method, session, status, headers.Get("Allow"), data)
			}
		}
	}
	// Authentication still comes first.
	if status, _, _ := request(t, remote, "GET", "/mcp", "", "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET returned %d", status)
	}
}

func TestStatelessParallelToolCalls(t *testing.T) {
	var active, peak atomic.Int32
	_, remote, token, _ := newHTTPFixture(t, func(_ context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		now := active.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		time.Sleep(50 * time.Millisecond)
		active.Add(-1)
		return wire.Success(name, "read", map[string]any{"path": args["path"]}, "ok"), nil
	})
	const calls = 16
	var wg sync.WaitGroup
	errs := make(chan string, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("parallel-%d", i)
			// Half the burst carries a stale session, as a reconnecting client would.
			session := ""
			if i%2 == 1 {
				session = staleSession
			}
			req, _ := http.NewRequest("POST", remote.URL+"/mcp", strings.NewReader(readCall(i+1, path)))
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			if session != "" {
				req.Header.Set("Mcp-Session-Id", session)
			}
			response, err := remote.Client().Do(req)
			if err != nil {
				errs <- err.Error()
				return
			}
			defer response.Body.Close()
			var body []byte
			body, err = io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				errs <- fmt.Sprintf("call %d: status %d %v", i, response.StatusCode, err)
				return
			}
			var reply map[string]any
			for _, line := range bytes.Split(body, []byte("\n")) {
				if len(line) > 6 && string(line[:6]) == "data: " && json.Unmarshal(line[6:], &reply) == nil {
					break
				}
			}
			result, _ := reply["result"].(map[string]any)
			structured, _ := result["structuredContent"].(map[string]any)
			if structured["path"] != path {
				errs <- fmt.Sprintf("call %d: %s", i, body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if peak.Load() < 2 {
		t.Fatalf("calls did not overlap (peak %d)", peak.Load())
	}
}

// A client keeps calling across a server restart without re-initializing.
func TestStatelessRestartNeedsNoReinitialize(t *testing.T) {
	home := t.TempDir()
	registry, err := contracts.New()
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().ServeHTTP(w, r) }))
	t.Cleanup(remote.Close)
	start := func(mode string) *Server {
		cfg := config.New(home)
		cfg.Set("httpSessionMode", mode)
		server, err := New(cfg, registry, echoCaller(nil), "fixture-principal", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Close() })
		current.Store(server)
		return server
	}
	first := start(config.HTTPSessionStateless)
	token := first.credential
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"restart-test","version":"1"}}}`
	status, _, data := request(t, remote, "POST", "/mcp", init, "", token, nil)
	rpcResult(t, status, data)
	status, _, data = request(t, remote, "POST", "/mcp", readCall(2, "before"), "", token, nil)
	rpcResult(t, status, data)

	// Drain and replace the process; the client neither re-initializes nor notices.
	if remaining := first.Drain(context.Background()); remaining != 0 {
		t.Fatalf("drain left %d calls", remaining)
	}
	second := start(config.HTTPSessionStateless)
	if second.credential != token {
		t.Fatal("restart changed the bearer credential")
	}
	for _, session := range []string{"", staleSession} {
		status, _, data = request(t, remote, "POST", "/mcp", readCall(3, "after"), session, token, nil)
		if structured, _ := rpcResult(t, status, data)["structuredContent"].(map[string]any); structured["path"] != "after" {
			t.Fatalf("post-restart call: %s", data)
		}
	}

	// The stateful rollback mode keeps its documented contract: a session
	// from before the restart is unknown and the client must re-initialize.
	_ = second.Drain(context.Background())
	start(config.HTTPSessionStateful)
	status, _, _ = request(t, remote, "POST", "/mcp", readCall(4, "stateful"), staleSession, token, nil)
	if status != http.StatusNotFound {
		t.Fatalf("stateful mode answered a pre-restart session with %d", status)
	}
}

func TestStatelessDrainRejectsNewWorkAndFinishesInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var cancelled atomic.Bool
	s, remote, token, _ := newHTTPFixture(t, func(ctx context.Context, _ wire.Scope, name string, _ map[string]any) (wire.Result, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			cancelled.Store(true)
		}
		return wire.Success(name, "read", map[string]any{"path": "slow"}, "finished"), nil
	})
	type reply struct {
		status int
		data   []byte
	}
	inflight := make(chan reply, 1)
	go func() {
		req, _ := http.NewRequest("POST", remote.URL+"/mcp", strings.NewReader(readCall(1, "slow")))
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := remote.Client().Do(req)
		if err != nil {
			inflight <- reply{status: -1, data: []byte(err.Error())}
			return
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		inflight <- reply{response.StatusCode, data}
	}()
	<-started
	drained := make(chan int64, 1)
	go func() { drained <- s.Drain(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	draining := func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.draining }
	for !draining() {
		if time.Now().After(deadline) {
			t.Fatal("drain did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	for _, session := range []string{"", staleSession} {
		status, headers, _ := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`, session, token, nil)
		if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" {
			t.Fatalf("draining server accepted new work (session %q): %d Retry-After=%q", session, status, headers.Get("Retry-After"))
		}
	}
	select {
	case <-drained:
		t.Fatal("drain finished while a tool call was in flight")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	got := <-inflight
	if got.status != http.StatusOK || cancelled.Load() {
		t.Fatalf("in-flight call did not finish cleanly: %d cancelled=%v %s", got.status, cancelled.Load(), got.data)
	}
	if structured, _ := rpcResult(t, got.status, got.data)["structuredContent"].(map[string]any); structured["path"] != "slow" {
		t.Fatalf("in-flight result %s", got.data)
	}
	if remaining := <-drained; remaining != 0 {
		t.Fatalf("drain reported %d calls", remaining)
	}
}

// The sessionless 2026-07-28 revision: server/discover, then a direct call with
// the standard routing headers and request _meta, never initialize.
func TestStatelessModernProtocolWithoutInitialize(t *testing.T) {
	scopes := make(chan wire.Scope, 1)
	_, remote, token, _ := newHTTPFixture(t, echoCaller(scopes))
	meta := map[string]any{
		mcp.MetaKeyProtocolVersion:    modernProtocol,
		mcp.MetaKeyClientCapabilities: map[string]any{},
		mcp.MetaKeyClientInfo:         map[string]any{"name": "modern-test", "version": "1"},
	}
	body := func(id int, method string, params map[string]any) string {
		params["_meta"] = meta
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return string(raw)
	}
	headers := map[string]string{"MCP-Protocol-Version": modernProtocol, "Mcp-Method": "server/discover"}
	status, _, data := request(t, remote, "POST", "/mcp", body(1, "server/discover", map[string]any{}), "", token, headers)
	discovered := rpcResult(t, status, data)
	versions, _ := discovered["supportedVersions"].([]any)
	if len(versions) == 0 || versions[0] != modernProtocol || discovered["resultType"] != "complete" || discovered["ttlMs"] != float64(discoveryCacheTTLMs) || discovered["cacheScope"] != discoveryCacheScope {
		t.Fatalf("server/discover: %s", data)
	}
	listHeaders := map[string]string{"MCP-Protocol-Version": modernProtocol, "Mcp-Method": "tools/list"}
	status, _, data = request(t, remote, "POST", "/mcp", body(10, "tools/list", map[string]any{}), "", token, listHeaders)
	listed := rpcResult(t, status, data)
	if tools, _ := listed["tools"].([]any); len(tools) == 0 || listed["resultType"] != "complete" || listed["ttlMs"] != float64(discoveryCacheTTLMs) || listed["cacheScope"] != discoveryCacheScope {
		t.Fatalf("modern tools/list: %s", data)
	}
	headers = map[string]string{"MCP-Protocol-Version": modernProtocol, "Mcp-Method": "tools/call", "Mcp-Name": "read"}
	call := body(2, "tools/call", map[string]any{"name": "read", "arguments": map[string]any{"workspace_id": "fixture", "path": "modern.txt"}})
	status, responseHeaders, data := request(t, remote, "POST", "/mcp", call, staleSession, token, headers)
	result := rpcResult(t, status, data)
	structured, _ := result["structuredContent"].(map[string]any)
	resultMeta, _ := result["_meta"].(map[string]any)
	if structured["path"] != "modern.txt" || result["resultType"] != "complete" || resultMeta[mcp.MetaKeyServerInfo] == nil || responseHeaders.Get("Mcp-Session-Id") != "" {
		t.Fatalf("modern tools/call: %s", data)
	}
	if scope := <-scopes; scope.SessionID != "" || scope.PrincipalID != "fixture-principal" {
		t.Fatalf("modern scope %+v", scope)
	}
	// Mismatched routing headers are refused by the SDK before dispatch.
	headers["Mcp-Name"] = "write"
	if status, _, _ = request(t, remote, "POST", "/mcp", call, "", token, headers); status == http.StatusOK {
		t.Fatal("mismatched Mcp-Name was accepted")
	}
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// The SDK's own client prefers server/discover and the sessionless revision.
func TestStatelessServesTheSDKClient(t *testing.T) {
	_, remote, token, _ := newHTTPFixture(t, echoCaller(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-client-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: remote.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("list tools: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"workspace_id": "fixture", "path": "sdk.txt"}})
	if err != nil || result.IsError {
		t.Fatalf("call tool: %v %+v", err, result)
	}
	if structured, _ := result.StructuredContent.(map[string]any); structured["path"] != "sdk.txt" {
		t.Fatalf("structured result %#v", result.StructuredContent)
	}
}
