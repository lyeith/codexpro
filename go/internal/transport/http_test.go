package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type testCaller func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error)

func (f testCaller) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	return f(ctx, scope, name, args)
}
func newHTTPFixture(t *testing.T, caller testCaller) (*Server, *httptest.Server, string, *atomic.Int32) {
	t.Helper()
	cfg := config.New(t.TempDir())
	registry, err := contracts.New()
	if err != nil {
		t.Fatal(err)
	}
	if caller == nil {
		caller = func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
			var r wire.Result
			if err := json.Unmarshal([]byte(`{"_meta":{},"content":[{"type":"text","text":"fixture"}],"structuredContent":{"path":"fixture.txt","text":"fixture"}}`), &r); err != nil {
				t.Fatal(err)
			}
			r.Meta = map[string]any{}
			return r, nil
		}
	}
	closed := &atomic.Int32{}
	server, err := New(cfg, registry, caller, "fixture-principal", func(wire.Scope) { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	t.Cleanup(func() { _ = server.Close(); remote.Close() })
	return server, remote, server.credential, closed
}
func request(t *testing.T, remote *httptest.Server, method, path, body, session, credential string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, remote.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, data
}
func decodeRPC(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if json.Unmarshal(body, &value) == nil {
		return value
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			if json.Unmarshal(line[6:], &value) == nil {
				return value
			}
		}
	}
	t.Fatalf("missing JSON-RPC response: %.500s", body)
	return nil
}
func initialize(t *testing.T, remote *httptest.Server, credential, protocol string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "contract-test", "version": "1"}}})
	status, headers, data := request(t, remote, "POST", "/mcp", string(body), "", credential, nil)
	if status != 200 {
		t.Fatalf("initialize status=%d %.500s", status, data)
	}
	session := headers.Get("Mcp-Session-Id")
	if !sessionPattern.MatchString(session) {
		t.Fatalf("invalid generated session id %q", session)
	}
	result, ok := decodeRPC(t, data)["result"].(map[string]any)
	if !ok || result["protocolVersion"] != protocol {
		t.Fatalf("protocol reply mismatch: %#v", result)
	}
	status, _, data = request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, session, credential, nil)
	if status != 202 {
		t.Fatalf("initialized notification status=%d %.500s", status, data)
	}
	return session
}
func TestHTTPHandshakeRawDescriptorsAndResultEnvelopes(t *testing.T) {
	var calls atomic.Int32
	scopes := make(chan wire.Scope, 2)
	caller := testCaller(func(_ context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		calls.Add(1)
		scopes <- scope
		var r wire.Result
		err := json.Unmarshal([]byte(`{"_meta":{},"content":[{"type":"text","text":"fixture"}],"structuredContent":{"path":"fixture.txt","text":"fixture"}}`), &r)
		r.Meta = map[string]any{}
		return r, err
	})
	server, remote, token, closed := newHTTPFixture(t, caller)
	session := initialize(t, remote, token, "2025-11-25")
	status, _, body := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, session, token, nil)
	if status != 200 {
		t.Fatalf("tools/list status %d", status)
	}
	response := decodeRPC(t, body)
	actual, _ := json.Marshal(response["result"])
	expectedRaw, _ := json.Marshal(server.Registry.List())
	var expectedValue any
	if err := json.Unmarshal(expectedRaw, &expectedValue); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(expectedValue)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("raw tool descriptors changed through SDK: got %d bytes want %d", len(actual), len(expected))
	}
	_, _, body = request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":"read-request","method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"fixture.txt"}}}`, session, token, nil)
	result := decodeRPC(t, body)["result"].(map[string]any)
	if _, ok := result["_meta"]; !ok {
		t.Fatal("empty _meta was lost by SDK serialization")
	}
	if _, ok := result["isError"]; ok {
		t.Fatal("false isError must remain omitted")
	}
	scope := <-scopes
	if scope.PrincipalID != "fixture-principal" || scope.SessionID != session || scope.RequestID != "read-request" {
		t.Fatalf("request scope was not propagated: %#v", scope)
	}
	for _, input := range []string{
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"work_manage","arguments":{}}}`,
	} {
		_, _, body = request(t, remote, "POST", "/mcp", input, session, token, nil)
		result = decodeRPC(t, body)["result"].(map[string]any)
		if result["isError"] != true {
			t.Fatalf("SDK validation was not an error result: %#v", result)
		}
		if _, ok := result["structuredContent"]; ok {
			t.Fatal("SDK error acquired business structured content")
		}
		if _, ok := result["_meta"]; ok {
			t.Fatal("SDK error acquired metadata")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("invalid inputs reached domain handler: %d", calls.Load())
	}
	status, _, _ = request(t, remote, "DELETE", "/mcp", "", session, token, nil)
	if status != 200 {
		t.Fatalf("delete status %d", status)
	}
	if server.SessionCount() != 0 || closed.Load() != 1 {
		t.Fatal("delete did not release session-scoped state")
	}
	status, _, body = request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{}}`, session, token, nil)
	if status != 404 || decodeRPC(t, body)["error"].(map[string]any)["message"] != "Session not found" {
		t.Fatal("deleted session remained usable")
	}
}
func TestHTTPAuthenticationHeadersQueryAndFailureWindow(t *testing.T) {
	server, remote, token, _ := newHTTPFixture(t, nil)
	server.Mux.HandleFunc("/fixture", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	for _, path := range []string{"/fixture?codexpro_token=" + token, "/fixture?token=" + token} {
		status, headers, body := request(t, remote, "GET", path, "", "", "", nil)
		if status != 200 || string(body) != "ok" || headers.Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("query credential or security headers changed")
		}
	}
	status, _, _ := request(t, remote, "GET", "/fixture?token="+token, "", "", "wrong", nil)
	if status != 401 {
		t.Fatal("valid query bypassed wrong explicit bearer")
	}
	for i := 1; i < 11; i++ {
		status, headers, body := request(t, remote, "GET", "/fixture", "", "", "", nil)
		if i < 10 && status != 401 {
			t.Fatalf("early authentication throttle at %d: %d", i, status)
		}
		if i == 10 && (status != 429 || headers.Get("Retry-After") == "" || string(body) != "Too Many Authentication Attempts") {
			t.Fatal("missing bounded failure throttle")
		}
	}
	status, _, _ = request(t, remote, "GET", "/fixture", "", "", token, nil)
	if status != 200 {
		t.Fatal("valid credential was blocked by failed-auth window")
	}
	status, _, body := request(t, remote, "POST", "/mcp", "broken-json", "", token, nil)
	if status != 400 || decodeRPC(t, body)["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatal("invalid JSON wire error changed")
	}
}
func TestHTTPSessionTTLActiveProtectionDrainAndOldProtocol(t *testing.T) {
	server, remote, token, closed := newHTTPFixture(t, nil)
	session := initialize(t, remote, token, "2024-10-07")
	server.mu.Lock()
	server.sessions[session].lastSeen = time.Now().Add(-time.Hour)
	server.sessions[session].active = 1
	server.mu.Unlock()
	server.prune()
	if server.SessionCount() != 1 {
		t.Fatal("active request was evicted")
	}
	server.mu.Lock()
	server.sessions[session].active = 0
	server.mu.Unlock()
	server.prune()
	if server.SessionCount() != 0 || closed.Load() != 1 {
		t.Fatal("expired session did not close")
	}
	server.BeginDrain()
	status, headers, body := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, "", token, nil)
	if status != 503 || headers.Get("Retry-After") != "1" || decodeRPC(t, body)["error"].(map[string]any)["message"] != "Server is restarting; retry shortly" {
		t.Fatal("draining admission contract changed")
	}
}
func TestHTTPProtocolCancellationReachesTool(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan struct{})
	caller := testCaller(func(ctx context.Context, _ wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return wire.Result{}, ctx.Err()
	})
	_, remote, token, _ := newHTTPFixture(t, caller)
	session := initialize(t, remote, token, "2025-11-25")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", remote.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("tool never started")
	}
	status, _, response := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"test cancellation"}}`, session, token, nil)
	if status != 202 {
		t.Fatalf("cancellation notification status=%d body=%s", status, response)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("protocol cancellation did not reach tool")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled client did not return")
	}
}
func TestSameOriginGuard(t *testing.T) {
	for _, tc := range []struct {
		origin, host string
		allowed      bool
	}{{"", "example.test", true}, {"https://example.test", "example.test", true}, {"https://foreign.test", "example.test", false}, {"null", "example.test", false}, {"https://example.test:7210", "example.test:7210", true}} {
		req := httptest.NewRequest("POST", "http://"+tc.host+"/admin/profile", nil)
		req.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		if SameOrigin(w, req) != tc.allowed {
			t.Fatalf("origin %q host %q mismatch", tc.origin, tc.host)
		}
		if !tc.allowed {
			var data map[string]any
			if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &data) != nil || data["ok"] != false {
				t.Fatal("origin error shape differs")
			}
		}
	}
}
