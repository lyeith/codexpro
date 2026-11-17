//go:build linux

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type pendingHTTPFixture struct {
	server           *Server
	remote           *httptest.Server
	tokens, sessions []string
	mu               sync.Mutex
	count            map[string]int
	sender           map[string]string
	calls            int
}

func newPendingHTTPFixture(t *testing.T, caller testCaller) *pendingHTTPFixture {
	t.Helper()
	path, policy, tokens := namedPolicy(t)
	for i := range policy.Principals {
		policy.Principals[i].Type, policy.Principals[i].Role = "system", ""
		policy.Agents[i].Kind = "leaf"
		policy.Agents[i].Address = "system/" + policy.Principals[i].ID
	}
	for i := range policy.Credentials {
		policy.Credentials[i].Audience = "both"
	}
	savePolicy(t, path, policy)
	f := &pendingHTTPFixture{tokens: []string{tokens[0], tokens[2]}, count: map[string]int{}, sender: map[string]string{}}
	socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		q := r.URL.Query()
		label := q.Get("attribution")
		index, pin := -1, ""
		for i, token := range f.tokens {
			if r.Header.Get("Authorization") == "Bearer "+token {
				index, pin = i, []string{"home", "laptop"}[i]
			}
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/pending-summary" ||
			len(q) != 1 || len(q["attribution"]) != 1 || !wire.ValidAgent(label) ||
			index < 0 || r.Header.Get("X-Agentmail-Expected-Credential") != pin {
			t.Error("notice changed attribution, named credential pin, method or summary-only route")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		selection := fmt.Sprintf("%d/%s", index, label)
		_ = json.NewEncoder(w).Encode(map[string]any{"count": f.count[selection], "latest_sender": f.sender[selection]})
	})
	if caller == nil {
		caller = func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
			return logicalMailOrdinaryResult(), nil
		}
	}
	f.server, f.remote, _, _ = newHTTPFixture(t, caller)
	f.server.Config.Set("principalsFile", path)
	f.server.Config.Set("pendingMailSocket", socket)
	for _, token := range f.tokens {
		f.sessions = append(f.sessions, initialize(t, f.remote, token, "2025-11-25"))
	}
	return f
}

func (f *pendingHTTPFixture) set(index int, label string, count int, sender string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	selection := fmt.Sprintf("%d/%s", index, label)
	f.count[selection], f.sender[selection] = count, sender
}

func (f *pendingHTTPFixture) call(t *testing.T, index int, label string, id int, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{"workspace_id": "fixture", "path": "file"}
	}
	args["agent"] = label
	result, err := logicalMailHTTPWireCall(f.remote, f.tokens[index], f.sessions[index], logicalMailHTTPCallBody(id, args))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requirePendingLine(t *testing.T, result map[string]any, want string) {
	t.Helper()
	content := result["content"].([]any)
	length := 1
	if want != "" {
		length++
	}
	if len(content) != length {
		t.Fatalf("content count %d, want %d", len(content), length)
	}
	if want != "" && content[1].(map[string]any)["text"] != want {
		t.Fatalf("notice=%v want %q", content[1], want)
	}
	raw, _ := json.Marshal(logicalMailOrdinaryResult())
	var expected map[string]any
	_ = json.Unmarshal(raw, &expected)
	copy := make(map[string]any, len(result))
	for key, value := range result {
		copy[key] = value
	}
	copy["content"] = content[:1]
	if !reflect.DeepEqual(copy, expected) {
		t.Fatal("notice changed ordinary content, metadata, structured data or error state")
	}
}

func TestPendingMailMCPFreshOnEveryCallAndSeparateNamedRegistrations(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	for i, label := range []string{"SP9", "AP1"} {
		requirePendingLine(t, f.call(t, i, label, 10+i*10, nil), "")
		// No cache expiry/sleep: arriving mail appears on the next tool call.
		f.set(i, label, 23, "people/david/concierge")
		requirePendingLine(t, f.call(t, i, label, 11+i*10, nil), label+": 23 unread, latest from people/david/concierge")
		f.set(i, label, 0, "") // The owner-side API test proves actual pull clearing.
		requirePendingLine(t, f.call(t, i, label, 12+i*10, nil), "")
	}
	f.mu.Lock()
	calls := f.calls
	f.mu.Unlock()
	if calls != 6 {
		t.Fatalf("fresh calls=%d, want 6", calls)
	}
	f.set(1, "RDP1", 1, "people/david/concierge")
	requirePendingLine(t, f.call(t, 1, "RDP1", 40, nil), "RDP1: 1 unread, latest from people/david/concierge")
	f.set(1, "AP1", 2, "people/david/concierge")
	f.set(0, "SP9", 3, "people/david/concierge")
	// Resolution belongs to Agentmail under the original named authentication.
	requirePendingLine(t, f.call(t, 0, "AP1", 41, nil), "")
	requirePendingLine(t, f.call(t, 1, "SP9", 42, nil), "")
}

func TestPendingMailMCPNewLabelWithoutConfigurationOrRestart(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	before := f.server.Config.Snapshot()
	requirePendingLine(t, f.call(t, 0, "NX-1", 43, nil), "")
	// Only backend registration/state changes. Reuse the same server and session.
	f.set(0, "NX-1", 2, "people/david/concierge")
	requirePendingLine(t, f.call(t, 0, "NX-1", 44, nil), "NX-1: 2 unread, latest from people/david/concierge")
	requirePendingLine(t, f.call(t, 0, "NX-2", 45, nil), "")
	requirePendingLine(t, f.call(t, 1, "NX-1", 46, nil), "")
	f.set(0, "NX-1", 0, "")
	requirePendingLine(t, f.call(t, 0, "NX-1", 47, nil), "")
	if !reflect.DeepEqual(before, f.server.Config.Snapshot()) {
		t.Fatal("new registration required a CodexPro configuration change")
	}
}

func TestPendingMailMCPConcurrentSelectionsAndAmbiguousRouting(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	f.set(0, "SP9", 1, "people/alpha/concierge")
	f.set(1, "AP1", 2, "people/beta/concierge")
	var wg sync.WaitGroup
	for i, label := range []string{"SP9", "AP1"} {
		wg.Add(1)
		go func(i int, label string) {
			defer wg.Done()
			result, err := logicalMailHTTPWireCall(f.remote, f.tokens[i], f.sessions[i], logicalMailHTTPCallBody(50+i, map[string]any{"workspace_id": "fixture", "path": "file", "agent": label}))
			if err != nil {
				t.Error(err)
				return
			}
			want := fmt.Sprintf("%s: %d unread, latest from people/%s/concierge", label, i+1, []string{"alpha", "beta"}[i])
			requirePendingLine(t, result, want)
		}(i, label)
	}
	wg.Wait()
	f.mu.Lock()
	before := f.calls
	f.mu.Unlock()
	for i, arguments := range []string{
		`{"workspace_id":"fixture","path":"file"}`,
		`{"workspace_id":"fixture","path":"file","agent":"ROOTX"}`,
		`{"workspace_id":"fixture","path":"file","agent":"SP9","agent":"AP1"}`,
		`{"workspace_id":"fixture","path":"file","Agent":"SP9"}`,
	} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read","arguments":%s}}`, 60+i, arguments)
		_, _, data := request(t, f.remote, "POST", "/mcp", body, f.sessions[0], f.tokens[0], map[string]string{logicalMailLabelHeader: "SP9"})
		result := decodeRPC(t, data)["result"].(map[string]any)
		if len(result["content"].([]any)) != 1 {
			t.Fatal("ambiguous or forged routing attached a notice")
		}
	}
	f.mu.Lock()
	after := f.calls
	f.mu.Unlock()
	if before != after {
		t.Fatal("invalid selection queried mail")
	}
}

func TestPendingMailKeepsEngineAndValidationErrors(t *testing.T) {
	calls := 0
	f := newPendingHTTPFixture(t, func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		calls++
		return wire.Result{}, errors.New("ordinary engine failure")
	})
	f.set(0, "SP9", 1, "people/david/concierge")
	for i, args := range []map[string]any{{"workspace_id": "fixture", "path": "file"}, {"workspace_id": "fixture"}} {
		result := f.call(t, 0, "SP9", 70+i, args)
		content := result["content"].([]any)
		if result["isError"] != true || len(content) != 2 || content[1].(map[string]any)["text"] != "SP9: 1 unread, latest from people/david/concierge" {
			t.Fatal("notice changed error result")
		}
	}
	if calls != 1 {
		t.Fatal("invalid request reached engine")
	}
}

func TestPendingMailRefusesMalformedMetadataWithoutExposingIt(t *testing.T) {
	valid := `{"count":2,"latest_sender":"people/david/concierge"}`
	if pendingMailLine([]byte(valid), "SP9") != "SP9: 2 unread, latest from people/david/concierge" {
		t.Fatal("valid metadata refused")
	}
	for _, bad := range []string{
		strings.Replace(valid, `"count":2`, `"count":2,"count":9`, 1),
		strings.Replace(valid, `"count":2`, `"count":-1`, 1),
		strings.Replace(valid, `"count":2`, `"count":null`, 1),
		strings.Replace(valid, `"count":2`, `"count":9223372036854775808`, 1),
		strings.Replace(valid, `"count":2`, `"address":"system/other","count":2`, 1),
		strings.Replace(valid, `"count":2`, `"logical_recipient":"OTHER","count":2`, 1),
		strings.Replace(valid, "people/david/concierge", "secret\\ninjected", 1),
		strings.Replace(valid, `"count":2`, `"body":"private original","count":2`, 1),
		strings.Replace(valid, `"latest_sender"`, `"Latest_Sender"`, 1),
		`{"count":0,"latest_sender":""}`,
	} {
		if pendingMailLine([]byte(bad), "SP9") != "" {
			t.Fatal("invalid or empty metadata reached notice")
		}
	}
	if pendingMailLine([]byte(valid), "SP9\ninjected") != "" {
		t.Fatal("invalid attribution reached notice")
	}
}

func TestPendingMailPreservesOriginalDeliveryReceipts(t *testing.T) {
	f := newDeliveryFixture(t, "complete original remains on its existing delivery path")
	socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/pending-summary" || len(r.URL.Query()) != 1 || r.URL.Query().Get("attribution") != "SP6" {
			t.Error("notice mutated mail")
		}
		_, _, pending := f.mail.state()
		sender := ""
		if len(pending) > 0 {
			sender = "fixture/coordinator"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": len(pending), "latest_sender": sender})
	})
	f.server.Config.Set("pendingMailSocket", socket)
	req, id := f.newRequest(context.Background())
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, req)
	result, ok := deliveryResult(w.Body.Bytes(), id)
	if !ok {
		t.Fatal("combined response missing")
	}
	content := result["content"].([]any)
	if len(content) != len(f.ordinary.Content)+2 ||
		content[len(content)-2].(map[string]any)["text"] != "SP6: 1 unread, latest from fixture/coordinator" ||
		!strings.HasPrefix(content[len(content)-1].(map[string]any)["text"].(string), logicalMailOfferPrefix) {
		t.Fatal("original envelope or single notice missing")
	}
	receipts, attempts, pending := f.mail.state()
	if len(receipts) != 1 || len(attempts) != 1 || len(pending) != 0 {
		t.Fatal("notice changed existing original receipt lifecycle")
	}
}

func TestPendingMailBackendRefusalsLeaveOrdinaryResultAndNeverRetry(t *testing.T) {
	f := newPendingHTTPFixture(t, nil)
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"forbidden", http.StatusForbidden, "private backend diagnostic"},
		{"unavailable", http.StatusServiceUnavailable, "private backend diagnostic"},
		{"oversized", http.StatusOK, strings.Repeat("private body", pendingMailMaxWireBytes)},
		{"malformed", http.StatusOK, `{"count":1,"latest_sender":"people/david/concierge","body":"private original"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			socket := logicalMailUnixHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if r.Method != http.MethodGet || r.URL.Path != "/v1/mailboxes/pending-summary" {
					t.Error("summary fallback attempted another route or mutation")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			f.server.Config.Set("pendingMailSocket", socket)
			requirePendingLine(t, f.call(t, 0, "SP9", 80, nil), "")
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("lookup calls=%d, want one without retry", calls)
			}
		})
	}
}
