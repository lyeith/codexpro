//go:build linux

package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func lifecyclePOST(ctx context.Context, token, session, body string) *http.Request {
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		r.Header.Set("Mcp-Session-Id", session)
	}
	return r
}

func TestHTTPInitializeDrainingAfterSDKAdmissionReleasesSession(t *testing.T) {
	s, _, token, closed := newHTTPFixture(t, nil)
	entered, proceed := make(chan struct{}), make(chan struct{})
	s.SDK.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				close(entered)
				<-proceed
			}
			return next(ctx, method, req)
		}
	})
	reply := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(reply, lifecyclePOST(t.Context(), token, "", fairnessInit))
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("initialize never reached SDK middleware, status=%d", reply.Code)
	}
	s.mu.Lock()
	pendingBefore := len(s.pending)
	s.mu.Unlock()
	s.BeginDrain()
	close(proceed)
	<-done
	if pendingBefore != 1 {
		t.Fatalf("SDK handshake held %d reservations before drain, want one", pendingBefore)
	}
	// The 2025-11-25 SDK has already opened the HTTP/SSE exchange.
	// Its generic Go method error is a JSON-RPC error with zero code.
	want := map[string]any{"jsonrpc": "2.0", "id": float64(1), "error": map[string]any{
		"code": float64(0), "message": "session initialization no longer admitted",
	}}
	if reply.Code != http.StatusOK || !reflect.DeepEqual(decodeRPC(t, reply.Body.Bytes()), want) {
		t.Fatalf("late initialize refusal=%d %s", reply.Code, reply.Body.Bytes())
	}
	s.mu.Lock()
	pendingAfter := len(s.pending)
	s.mu.Unlock()
	if pendingAfter != 0 || s.SessionCount() != 0 || closed.Load() != 0 {
		t.Fatalf("late initialize retained pending=%d sessions=%d or called owner cleanup %d times", pendingAfter, s.SessionCount(), closed.Load())
	}
	for range s.SDK.Sessions() {
		t.Fatal("rejected initialize left an SDK session behind")
	}
}

func TestHTTPSessionStaysPinnedAfterItsToolClientDisconnects(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	s, remote, token, closed := newHTTPFixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		close(entered)
		<-release
		return wire.Success("read", "Read", nil, "finished"), nil
	})
	s.Config.Set("maxHttpSessions", 1)
	toolReturned := make(chan struct{})
	s.SDK.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				defer close(toolReturned)
			}
			return next(ctx, method, req)
		}
	})
	session := initialize(t, remote, token, fairnessProtocol)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reply := httptest.NewRecorder()
	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		s.ServeHTTP(reply, lifecyclePOST(ctx, token, session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"x"}}}`))
	}()
	select {
	case <-entered:
	case <-httpDone:
		t.Fatalf("tool never reached the engine, status=%d", reply.Code)
	}
	cancel()
	<-httpDone // The HTTP request pin is gone. The engine call still holds its own.
	s.mu.Lock()
	active := s.sessions[session].active
	s.mu.Unlock()
	if active != 1 || s.InFlight() != 1 {
		t.Fatalf("detached call has %d session pins and %d in-flight tools, want one of each", active, s.InFlight())
	}
	requireCapacityRefusal(t, remote, token)
	if closed.Load() != 0 {
		t.Fatal("admission cleaned up a still-running tool")
	}
	unblock()
	<-toolReturned // The engine's unpin runs before its SDK middleware returns.
	replacement := initialize(t, remote, token, fairnessProtocol)
	if replacement == session || closed.Load() != 1 {
		t.Fatal("the disconnected session's idle slot was not replaced exactly once")
	}
}
