//go:build linux

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const fairnessProtocol = "2025-11-25"
const fairnessInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`

func sessionPing(t *testing.T, remote *httptest.Server, id, credential string, want int) {
	t.Helper()
	status, _, _ := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","id":7,"method":"ping"}`, id, credential, nil)
	if status != want {
		t.Fatalf("session ping status=%d; want %d", status, want)
	}
}

func requireCapacityRefusal(t *testing.T, remote *httptest.Server, credential string) {
	t.Helper()
	status, headers, body := request(t, remote, "POST", "/mcp", fairnessInit, "", credential, nil)
	if status != 503 || headers.Get("Retry-After") != "1" || len(headers.Values("Mcp-Session-Id")) != 0 {
		t.Fatalf("capacity refusal status=%d, Retry-After=%q, session header present=%t", status, headers.Get("Retry-After"), len(headers.Values("Mcp-Session-Id")) != 0)
	}
	want := map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{
		"code": float64(-32000), "message": "Session capacity reached; retry later",
	}}
	if !reflect.DeepEqual(decodeRPC(t, body), want) {
		t.Fatal("capacity refusal did not return the exact public RPC envelope")
	}
}

func TestHTTPSessionFairnessEvictsOnlyTheOldestOwnIdleSession(t *testing.T) {
	path, policy, tokens := namedPolicy(t)
	// The fleet is a registered system principal. Its token is distinct from
	// both the named personal credentials and the fixture's shared bridge.
	fleet := strings.TrimPrefix(tokens[0], "pa1_")
	digest := sha256.Sum256([]byte(fleet))
	policy.Principals = append(policy.Principals, plansaccess.Principal{ID: "sp-fleet", Type: "system"})
	policy.Agents = append(policy.Agents, plansaccess.Agent{ID: "sp-service", PrincipalID: "sp-fleet", Kind: "leaf", Address: "plans/fleet"})
	policy.Credentials = append(policy.Credentials, plansaccess.Credential{
		ID: "imported", Device: "connector", PrincipalID: "sp-fleet", AgentID: "sp-service", Audience: "both",
		Format: plansaccess.ImportedFleetV1, SHA256: hex.EncodeToString(digest[:]),
	})
	savePolicy(t, path, policy)
	s, remote, legacy, _ := newHTTPFixture(t, nil)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	s.Config.Set("maxHttpSessions", 8)
	closed := make(chan wire.Scope, 16)
	s.closeSession = func(scope wire.Scope) { closed <- scope }

	home, office, beta := tokens[0], tokens[1], tokens[2]
	operator := initialize(t, remote, beta, fairnessProtocol)
	alphaOld := initialize(t, remote, home, fairnessProtocol)
	alphaNew := initialize(t, remote, office, fairnessProtocol)
	bridgeOld := initialize(t, remote, legacy, fairnessProtocol)
	bridgeNew := initialize(t, remote, legacy, fairnessProtocol)
	fleetOld := initialize(t, remote, fleet, fairnessProtocol)
	fleetNew := initialize(t, remote, fleet, fairnessProtocol)
	if s.SessionCount() != 7 || len(closed) != 0 {
		t.Fatal("independent named owners and the legacy bridge did not receive their own slots")
	}

	// Fix the ordering explicitly, with beta older than every own candidate.
	s.mu.Lock()
	now := time.Now()
	for i, id := range []string{operator, alphaOld, bridgeOld, fleetOld, alphaNew, bridgeNew, fleetNew} {
		s.sessions[id].lastSeen = now.Add(time.Duration(i-7) * time.Second)
	}
	s.mu.Unlock()
	for _, tc := range []struct {
		name, oldest, oldCredential, newer, newCredential, owner, source, id, device string
	}{
		{"alpha across devices", alphaOld, home, alphaNew, office, "principal:alpha", "named", "alpha", "home"},
		{"imported fleet", fleetOld, fleet, fleetNew, fleet, "principal:sp-fleet", "named", "sp-fleet", "imported"},
		{"separate legacy bridge", bridgeOld, legacy, bridgeNew, legacy, "fixture-principal", "legacy", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.mu.Lock()
			prior := s.sessions[tc.oldest]
			s.mu.Unlock()
			if prior == nil {
				t.Fatal("the expected idle session disappeared before its owner's request")
			}
			replacement := initialize(t, remote, tc.newCredential, fairnessProtocol)
			if prior.ctx.Err() != context.Canceled || s.SessionCount() != 7 || len(closed) != 1 {
				t.Fatal("replacement did not cancel and clean exactly one session")
			}
			got := <-closed
			if got.SessionID != tc.oldest || got.TransportID != tc.oldest || got.PrincipalID != tc.owner ||
				got.Identity.Source != tc.source || got.Identity.PrincipalID != tc.id || got.Identity.CredentialID != tc.device {
				t.Fatalf("cleanup did not identify the expected displaced %s slot", tc.name)
			}
			sessionPing(t, remote, tc.oldest, tc.oldCredential, 404)
			sessionPing(t, remote, tc.newer, tc.newCredential, 200)
			sessionPing(t, remote, replacement, tc.newCredential, 200)
		})
	}
	sessionPing(t, remote, operator, beta, 200)
	sessionPing(t, remote, alphaNew, home, 403)
	sessionPing(t, remote, fleetNew, legacy, 403)
	if len(closed) != 0 {
		t.Fatal("stale or mismatched session lookup ran cleanup again")
	}
}

func TestHTTPSessionFairnessRefusesWhileOwnCallsArePinned(t *testing.T) {
	entered := make(chan string, 2)
	release, unblock := context.WithCancel(t.Context())
	defer unblock()
	s, remote, _, cleaned := newHTTPFixture(t, func(ctx context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		entered <- scope.SessionID
		select {
		case <-release.Done():
			return wire.Success("read", "read", nil, "completed"), nil
		case <-ctx.Done():
			return wire.Result{}, ctx.Err()
		}
	})
	path, _, tokens := namedPolicy(t)
	s.Config.Set("principalsFile", path)
	s.Config.Set("maxHttpSessions", 8)
	foreign := initialize(t, remote, tokens[2], fairnessProtocol)
	finished := make(chan *httptest.ResponseRecorder, 2)
	for _, credential := range tokens[:2] {
		id := initialize(t, remote, credential, fairnessProtocol)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Mcp-Session-Id", id)
		go func() {
			rr := httptest.NewRecorder()
			s.ServeHTTP(rr, req)
			finished <- rr
		}()
		select {
		case got := <-entered:
			if got != id {
				t.Fatal("tool call pinned a different session")
			}
		case reply := <-finished:
			t.Fatalf("tool returned before reaching the fixture: %d", reply.Code)
		case <-time.After(3 * time.Second):
			t.Fatal("tool did not enter")
		}
	}
	requireCapacityRefusal(t, remote, tokens[0])
	if s.SessionCount() != 3 || cleaned.Load() != 0 || s.InFlight() != 2 {
		t.Fatal("refusal displaced a session or disturbed a pinned call")
	}
	sessionPing(t, remote, foreign, tokens[2], 200)
	unblock()
	for range 2 {
		reply := <-finished
		if reply.Code != http.StatusOK {
			t.Fatalf("pinned call returned %d", reply.Code)
		}
		result, _ := decodeRPC(t, reply.Body.Bytes())["result"].(map[string]any)
		want := []any{map[string]any{"type": "text", "text": "completed"}}
		if !reflect.DeepEqual(result["content"], want) {
			t.Fatal("pinned call did not return the fixture result")
		}
	}
}

func TestHTTPSessionFairnessRefusesWhenAllGlobalSlotsAreForeign(t *testing.T) {
	path, _, tokens := namedPolicy(t)
	s, remote, legacy, cleaned := newHTTPFixture(t, nil)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	s.Config.Set("maxHttpSessions", 2)
	alpha := initialize(t, remote, tokens[0], fairnessProtocol)
	beta := initialize(t, remote, tokens[2], fairnessProtocol)
	requireCapacityRefusal(t, remote, legacy)
	if cleaned.Load() != 0 || s.SessionCount() != 2 {
		t.Fatal("a new owner displaced a foreign idle session at global capacity")
	}
	sessionPing(t, remote, alpha, tokens[0], 200)
	sessionPing(t, remote, beta, tokens[2], 200)
}

func TestHTTPSessionFairnessLegacyOnlyUsesTheGlobalBudget(t *testing.T) {
	s, remote, token, cleaned := newHTTPFixture(t, nil)
	s.Config.Set("maxHttpSessions", 3)
	var ids []string
	for range 3 {
		ids = append(ids, initialize(t, remote, token, fairnessProtocol))
	}
	if s.SessionCount() != 3 || cleaned.Load() != 0 {
		t.Fatal("the legacy-only transport did not admit its full global allowance")
	}
	for _, id := range ids {
		sessionPing(t, remote, id, token, 200)
	}
}
