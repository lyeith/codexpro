//go:build linux

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func logicalMailUnixHTTP(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cpmail-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "mail.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("private mail server shutdown: %v", err)
		}
	})
	return socket
}

func logicalMailHTTPFixture(t *testing.T, handler func(http.ResponseWriter, *http.Request, string), caller testCaller) (*Server, *httptest.Server, string, string, func()) {
	t.Helper()
	path, policy, tokens := namedPolicy(t)
	policy.Principals[0].Type, policy.Principals[0].Role = "system", ""
	policy.Agents[0].Kind, policy.Agents[0].Address = "leaf", "system/alpha"
	policy.Credentials[0].Audience = "both"
	savePolicy(t, path, policy)
	socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tokens[0] ||
			r.Header.Get("X-Agentmail-Expected-Credential") != policy.Credentials[0].ID {
			t.Error("local mail request omitted this request's credential or registration pin")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			var receipt map[string]string
			if json.NewDecoder(r.Body).Decode(&receipt) != nil || len(receipt) != 2 ||
				receipt["recipient"] != "system/alpha" ||
				(receipt["logical_recipient"] != "SP3" && receipt["logical_recipient"] != "SP7") ||
				r.URL.Path != "/v1/messages/original-"+receipt["logical_recipient"]+"/read" ||
				r.URL.RawQuery != "" {
				t.Error("receipt changed the included original ID or logical recipient")
				http.Error(w, "invalid", http.StatusBadRequest)
				return
			}
			// These stateless fixtures exercise selection/auth/lifecycle while
			// receipts remain unconfirmed. Stateful delivery tests separately
			// verify successful receipts remove exactly the included originals.
			http.Error(w, "fixture receipt unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method != "GET" || r.URL.Path != "/v1/mailboxes/logical-offer" ||
			r.URL.Query().Get("address") != "system/alpha" ||
			r.URL.Query().Get("after") != "0" || r.URL.Query().Get("limit") != "3" {
			t.Error("mail integration advanced a cursor or used a different offer endpoint")
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		handler(w, r, r.URL.Query().Get("logical_recipient"))
	})
	server, remote, _, _ := newHTTPFixture(t, caller)
	server.Config.Set("principalsFile", path)
	server.Config.Set("logicalMailSocket", socket)
	server.Config.Set("logicalMailPrincipal", "alpha")
	server.Config.Set("logicalMailAgent", "crew-alpha")
	server.Config.Set("logicalMailMailbox", "system/alpha")
	session := initialize(t, remote, tokens[0], "2025-11-25")
	revoke := func() {
		policy.Credentials[0].Revoked = true
		savePolicy(t, path, policy)
	}
	return server, remote, tokens[0], session, revoke
}

func logicalMailHTTPWriteOffer(w http.ResponseWriter, label string) {
	if label != "SP3" && label != "SP7" {
		http.Error(w, "unknown logical recipient", http.StatusBadRequest)
		return
	}
	seq := 3
	if label == "SP7" {
		seq = 7
	}
	body := "Complete original body for " + label
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"mailbox":           map[string]any{"address": "system/alpha", "revision": 2, "delivery_mode": "pull", "logical_recipients": []string{"SP3", "SP7"}},
		"logical_recipient": label,
		"messages": []any{map[string]any{
			"id": "original-" + label, "seq": seq, "from": "fixture/coordinator", "to": "system/alpha",
			"logical_recipient": label, "body": body, "reply_to": "original-question",
			"created_at": "2026-11-09T00:00:00Z", "delivery_mode": "pull", "delivery_status": "awaiting_pull",
		}},
		"next_after": seq, "more": false, "body_bytes": len(body), "max_body_bytes": 65536,
	})
}

func logicalMailHTTPCallBody(id int, args map[string]any) string {
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": "read", "arguments": args}})
	return string(data)
}

func logicalMailHTTPWireCall(remote *httptest.Server, token, session, body string) (map[string]any, error) {
	req, err := http.NewRequest("POST", remote.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != 200 {
		return nil, fmt.Errorf("tool HTTP status=%d read error=%v", response.StatusCode, err)
	}
	var rpc map[string]any
	if json.Unmarshal(data, &rpc) != nil {
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) && json.Unmarshal(line[6:], &rpc) == nil {
				break
			}
		}
	}
	result, ok := rpc["result"].(map[string]any)
	if !ok {
		return nil, errors.New("tool response omitted normal result")
	}
	return result, nil
}

func logicalMailOrdinaryResult() wire.Result {
	return wire.Result{
		Meta:              map[string]any{"ordinary_meta": "preserved"},
		Content:           []wire.Content{{"type": "text", "text": "ordinary tool output"}},
		StructuredContent: map[string]any{"ordinary_data": []any{"one", float64(2)}},
		DashboardMetadata: map[string]any{"private_dashboard": "must not appear"},
	}
}

func logicalMailRequireResult(t *testing.T, result map[string]any, label string, offered bool) {
	t.Helper()
	original, _ := json.Marshal(logicalMailOrdinaryResult())
	var expected map[string]any
	_ = json.Unmarshal(original, &expected)
	content, _ := result["content"].([]any)
	wantLength := 1
	if offered {
		wantLength = 2
	}
	if len(content) != wantLength {
		t.Fatalf("content count=%d, want %d", len(content), wantLength)
	}
	if offered {
		text, _ := content[1].(map[string]any)["text"].(string)
		other := "SP7"
		if label == "SP7" {
			other = "SP3"
		}
		if !strings.Contains(text, "original-"+label) || !strings.Contains(text, "Complete original body for "+label) ||
			!strings.Contains(text, "original-question") || strings.Contains(text, "original-"+other) {
			t.Fatal("logical offer lost an original envelope or included another recipient")
		}
	}
	copy := make(map[string]any, len(result))
	for key, value := range result {
		copy[key] = value
	}
	copy["content"] = content[:1]
	if !reflect.DeepEqual(copy, expected) {
		t.Fatal("mail attachment changed normal content, structured result, metadata or error state")
	}
}

func TestLogicalMailMCPLabelsDoNotBindOrLeakAcrossSharedSession(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	var gate atomic.Bool
	gate.Store(true)
	var offers atomic.Int32
	_, remote, token, session, revoke := logicalMailHTTPFixture(t, func(w http.ResponseWriter, r *http.Request, label string) {
		offers.Add(1)
		if gate.Load() {
			entered <- label
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		logicalMailHTTPWriteOffer(w, label)
	}, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return logicalMailOrdinaryResult(), nil
	})
	type received struct {
		label string
		value map[string]any
		err   error
	}
	results := make(chan received, 2)
	for i, label := range []string{"SP3", "SP7"} {
		go func(id int, label string) {
			body := logicalMailHTTPCallBody(id, map[string]any{"workspace_id": "fixture", "path": "file", "agent": label})
			value, err := logicalMailHTTPWireCall(remote, token, session, body)
			results <- received{label, value, err}
		}(i+2, label)
	}
	labels := map[string]bool{}
	for range 2 {
		select {
		case label := <-entered:
			labels[label] = true
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("two request-local labels did not reach separate concurrent offers")
		}
	}
	gate.Store(false)
	close(release)
	if !labels["SP3"] || !labels["SP7"] {
		t.Fatal("one request's label replaced the other")
	}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		logicalMailRequireResult(t, got.value, got.label, true)
	}
	// A new MCP transport needs no first-use registration. The fixture's
	// unconfirmed receipts leave the same original pending, with its exact ID.
	reconnected := initialize(t, remote, token, "2025-11-25")
	for _, id := range []string{session, reconnected} {
		result, err := logicalMailHTTPWireCall(remote, token, id, logicalMailHTTPCallBody(10, map[string]any{"workspace_id": "fixture", "path": "file", "agent": "SP3"}))
		if err != nil {
			t.Fatal(err)
		}
		logicalMailRequireResult(t, result, "SP3", true)
	}
	before := offers.Load()
	result, err := logicalMailHTTPWireCall(remote, token, session, logicalMailHTTPCallBody(11, map[string]any{"workspace_id": "fixture", "path": "file"}))
	if err != nil {
		t.Fatal(err)
	}
	logicalMailRequireResult(t, result, "", false)
	if offers.Load() != before {
		t.Fatal("unlabeled call inherited a previous label")
	}
	revoke()
	body := logicalMailHTTPCallBody(12, map[string]any{"workspace_id": "fixture", "path": "file", "agent": "SP3"})
	status, _, _ := request(t, remote, "POST", "/mcp", body, session, token, nil)
	if status != http.StatusUnauthorized || offers.Load() != before {
		t.Fatal("revoked identity reached the mail selector")
	}
}

func TestLogicalMailMCPConflictsAndFailuresPreserveOrdinaryResult(t *testing.T) {
	var offers atomic.Int32
	var unavailable atomic.Bool
	_, remote, token, session, _ := logicalMailHTTPFixture(t, func(w http.ResponseWriter, _ *http.Request, label string) {
		offers.Add(1)
		if unavailable.Load() {
			http.Error(w, "private backend detail", http.StatusServiceUnavailable)
			return
		}
		logicalMailHTTPWriteOffer(w, label)
	}, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return logicalMailOrdinaryResult(), nil
	})
	for _, raw := range []string{
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"read\",\"arguments\":{\"workspace_id\":\"fixture\",\"path\":\"file\",\"agent\":\"SP3\",\"agent\":\"SP7\"}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"read\",\"arguments\":{\"workspace_id\":\"fixture\",\"path\":\"file\",\"agent\":\"SP3\"},\"arguments\":{\"workspace_id\":\"fixture\",\"path\":\"file\",\"agent\":\"SP7\"}}}",
		"{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"tools/call\",\"Params\":{\"name\":\"read\",\"arguments\":{\"workspace_id\":\"fixture\",\"path\":\"file\",\"agent\":\"SP7\"}},\"params\":{\"name\":\"read\",\"arguments\":{\"workspace_id\":\"fixture\",\"path\":\"file\",\"agent\":\"SP3\"}}}",
	} {
		result, err := logicalMailHTTPWireCall(remote, token, session, raw)
		if err != nil {
			t.Fatal(err)
		}
		logicalMailRequireResult(t, result, "", false)
	}
	if offers.Load() != 0 {
		t.Fatal("conflicting routing fields reached Agentmail")
	}
	for _, label := range []string{"BAD", "SP3"} {
		unavailable.Store(label == "SP3")
		result, err := logicalMailHTTPWireCall(remote, token, session, logicalMailHTTPCallBody(4, map[string]any{"workspace_id": "fixture", "path": "file", "agent": label}))
		if err != nil {
			t.Fatal(err)
		}
		logicalMailRequireResult(t, result, "", false)
		data, _ := json.Marshal(result)
		if bytes.Contains(data, []byte("private backend detail")) || bytes.Contains(data, []byte(token)) {
			t.Fatal("mail failure leaked backend or credential details")
		}
	}
	before := offers.Load()
	body := logicalMailHTTPCallBody(5, map[string]any{"workspace_id": "fixture", "path": "file"})
	_, _, data := request(t, remote, "POST", "/mcp", body, session, token, map[string]string{logicalMailLabelHeader: "SP3"})
	result := decodeRPC(t, data)["result"].(map[string]any)
	logicalMailRequireResult(t, result, "", false)
	if offers.Load() != before {
		t.Fatal("caller-forged internal header selected mail")
	}
}

func TestLogicalMailMCPKeepsValidationAndToolErrors(t *testing.T) {
	var calls atomic.Int32
	_, remote, token, session, _ := logicalMailHTTPFixture(t, func(w http.ResponseWriter, _ *http.Request, label string) {
		logicalMailHTTPWriteOffer(w, label)
	}, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		calls.Add(1)
		return wire.Result{}, errors.New("ordinary engine failure")
	})
	for i, args := range []map[string]any{
		{"workspace_id": "fixture", "path": "file", "agent": "SP3"},
		{"workspace_id": "fixture", "agent": "SP3"},
	} {
		result, err := logicalMailHTTPWireCall(remote, token, session, logicalMailHTTPCallBody(i+2, args))
		if err != nil {
			t.Fatal(err)
		}
		content, _ := result["content"].([]any)
		if result["isError"] != true || len(content) != 2 {
			t.Fatal("ordinary tool error or offered mail was lost")
		}
		first, _ := content[0].(map[string]any)["text"].(string)
		last, _ := content[1].(map[string]any)["text"].(string)
		if i == 0 && !strings.Contains(first, "ordinary engine failure") || i == 1 && !strings.Contains(first, "path") ||
			!strings.Contains(last, "original-SP3") {
			t.Fatal("mail replaced the ordinary error or selected the wrong recipient")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("schema-invalid request reached the ordinary tool")
	}
}

func TestLogicalMailMCPCancelledOuterResponseRepeatsOriginal(t *testing.T) {
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	var block atomic.Bool
	block.Store(true)
	_, remote, token, session, _ := logicalMailHTTPFixture(t, func(w http.ResponseWriter, r *http.Request, label string) {
		if block.Load() {
			entered <- struct{}{}
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		logicalMailHTTPWriteOffer(w, label)
	}, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return logicalMailOrdinaryResult(), nil
	})
	body := logicalMailHTTPCallBody(2, map[string]any{"workspace_id": "fixture", "path": "file", "agent": "SP3"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", remote.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	done := make(chan error, 1)
	go func() {
		response, err := remote.Client().Do(req)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("offer did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("lost outer request did not cancel its offer before the separate two-second timeout")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled outer request remained blocked")
	}
	block.Store(false)
	result, err := logicalMailHTTPWireCall(remote, token, session, body)
	if err != nil {
		t.Fatal(err)
	}
	logicalMailRequireResult(t, result, "SP3", true)
	// This fixture refuses receipts, so later successful responses recover
	// the same pending original even if the caller discards an earlier result.
	if _, err := logicalMailHTTPWireCall(remote, token, session, body); err != nil {
		t.Fatal(err)
	}
	result, err = logicalMailHTTPWireCall(remote, token, session, body)
	if err != nil {
		t.Fatal(err)
	}
	logicalMailRequireResult(t, result, "SP3", true)
}

func TestLogicalMailMCPDisconnectPreservesAcceptedToolAndRetiresOffer(t *testing.T) {
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	var offers atomic.Int32
	var completed atomic.Int32
	server, remote, token, session, _ := logicalMailHTTPFixture(t, func(w http.ResponseWriter, _ *http.Request, label string) {
		offers.Add(1)
		logicalMailHTTPWriteOffer(w, label)
	}, func(ctx context.Context, _ wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		started <- ctx
		<-release
		completed.Add(1)
		return logicalMailOrdinaryResult(), nil
	})
	body := logicalMailHTTPCallBody(2, map[string]any{"workspace_id": "fixture", "path": "file", "agent": "SP3"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", remote.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	// Caller-supplied handles cannot be reused as HTTP request lifetime state.
	req.Header.Set(logicalMailContextHeader, "forged-context-handle")
	done := make(chan struct{})
	go func() {
		response, _ := remote.Client().Do(req)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		close(done)
	}()
	var accepted context.Context
	select {
	case accepted = <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("ordinary tool did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("disconnected HTTP request did not return")
	}
	// The HTTP owner must retire its handle even though the SDK retains the
	// accepted ordinary call. The call's original lifetime is unchanged.
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		live := len(server.mailRequests)
		server.mu.Unlock()
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("retired HTTP request kept a mail lifetime handle")
		}
		time.Sleep(time.Millisecond)
	}
	if accepted.Err() != nil {
		close(release)
		t.Fatal("mail-only cancellation changed accepted tool lifetime")
	}
	close(release)
	deadline = time.Now().Add(time.Second)
	for server.inflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("accepted tool did not finish after release")
		}
		time.Sleep(time.Millisecond)
	}
	if completed.Load() != 1 || offers.Load() != 0 {
		t.Fatal("accepted tool was lost or a disconnected request acquired mail")
	}
}
