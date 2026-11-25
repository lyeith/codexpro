//go:build linux

package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
)

func namedFleetReceiverPolicy(t *testing.T, kind string) (string, *plansaccess.Policy, []string) {
	t.Helper()
	path, p, tokens := namedPolicy(t)
	p.Principals[0].Type, p.Principals[0].Role = "system", ""
	p.Agents[0] = plansaccess.Agent{
		ID: "rdp-" + kind, PrincipalID: p.Principals[0].ID, Kind: kind, Address: "rdp/" + kind,
	}
	if kind == "coordinator" {
		p.Agents[0].Host, p.Agents[0].Repo = "ssd", "/work/rdp"
		p.Agents[0].NativeID = "codex:ssd:existing-coordinator"
	}
	p.Credentials[0].AgentID, p.Credentials[1].AgentID = p.Agents[0].ID, p.Agents[0].ID
	p.Credentials[0].Audience = "both"
	saveValidNamedFleetPolicy(t, path, p)
	return path, p, tokens
}

func saveValidNamedFleetPolicy(t *testing.T, path string, p *plansaccess.Policy) {
	t.Helper()
	if err := p.Validate(); err != nil {
		t.Fatalf("invalid named Fleet receiver policy: %v", err)
	}
	savePolicy(t, path, p)
}

func TestNamedFleetPolicyUpgradePreservesMCPBinding(t *testing.T) {
	for _, kind := range []string{"coordinator", "leaf"} {
		for _, restriction := range []string{"credential", "removed credential", "agent", "principal"} {
			t.Run(kind+"/"+restriction, func(t *testing.T) {
				path, p, tokens := namedFleetReceiverPolicy(t, kind)
				s, remote, _, _ := newHTTPFixture(t, nil)
				s.Config.Set("principalsFile", path)
				session := initialize(t, remote, tokens[0], "2025-11-25")
				otherSession := initialize(t, remote, tokens[2], "2025-11-25")
				const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
				before, _, ok := s.authenticate(tokens[0])
				if !ok {
					t.Fatal("version 1 registration was not authenticated")
				}
				if err := p.GrantFleetCoordinator(p.Agents[0].Address); err != nil {
					t.Fatal(err)
				}
				if p.Version != 2 {
					t.Fatal("named promotion did not select version 2")
				}
				saveValidNamedFleetPolicy(t, path, p)
				if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil); status != http.StatusOK {
					t.Fatalf("existing session after promotion=%d", status)
				}
				if after, _, ok := s.authenticate(tokens[0]); !ok || after != before {
					t.Fatalf("promotion changed the bound identity: %+v", after)
				}
				if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[1], nil); status != http.StatusForbidden {
					t.Fatalf("another credential entered the preserved session: %d", status)
				}
				switch restriction {
				case "credential":
					p.Credentials[0].Revoked = true
				case "removed credential":
					p.Credentials = p.Credentials[1:]
				case "agent":
					p.Agents[0].Disabled = true
				case "principal":
					p.Principals[0].Disabled = true
				}
				saveValidNamedFleetPolicy(t, path, p)
				check := func() {
					t.Helper()
					if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil); status != http.StatusUnauthorized {
						t.Fatalf("version 2 restriction returned %d", status)
					}
					if status, _, _ := request(t, remote, "POST", "/mcp", list, otherSession, tokens[2], nil); status != http.StatusOK {
						t.Fatalf("unaffected session returned %d", status)
					}
				}
				check()
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				check()
			})
		}
	}
}

func TestNamedFleetV2BootstrapAndCollectorShareRestrictions(t *testing.T) {
	path, p, tokens := namedFleetReceiverPolicy(t, "coordinator")
	p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
	p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
	saveValidNamedFleetPolicy(t, path, p)
	s, remote, _, _ := newHTTPFixture(t, nil)
	s.Config.Set("principalsFile", path)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	bootstrap, err := NewBootstrap(s.Config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), s.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	s.Mux.HandleFunc(statusCollectorPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	call := func(handler http.Handler, target, credential string) int {
		t.Helper()
		req := httptest.NewRequest("GET", target, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+credential)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := call(bootstrap, "/healthz", tokens[0]); got != http.StatusOK {
		t.Fatalf("version 1 bootstrap=%d", got)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("version 1 collector=%d", got)
	}
	if err := p.GrantFleetCoordinator(p.Agents[0].Address); err != nil {
		t.Fatal(err)
	}
	saveValidNamedFleetPolicy(t, path, p)
	if got := call(bootstrap, "/healthz", tokens[0]); got != http.StatusOK {
		t.Fatalf("version 2 bootstrap=%d", got)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("version 2 collector=%d", got)
	}

	p.Credentials[0].Revoked = true
	saveValidNamedFleetPolicy(t, path, p)
	// Only the collector observes this valid version 2 revocation.
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("collector refresh=%d", got)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call(bootstrap, "/healthz", tokens[0]); got != http.StatusUnauthorized {
		t.Fatalf("bootstrap lost the collector's revocation: %d", got)
	}
	const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil); status != http.StatusUnauthorized {
		t.Fatalf("MCP lost the collector's revocation: %d", status)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("unaffected collector=%d", got)
	}

	p.Credentials[2].Revoked = true
	saveValidNamedFleetPolicy(t, path, p)
	// Only bootstrap observes the collector revocation before the bad write.
	if got := call(bootstrap, "/healthz", tokens[1]); got != http.StatusOK {
		t.Fatalf("bootstrap refresh=%d", got)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusUnauthorized {
		t.Fatalf("collector lost bootstrap's revocation: %d", got)
	}
	if err := bootstrap.Publish(s); err != nil {
		t.Fatal(err)
	}
	if got := call(bootstrap, statusCollectorPath, tokens[2]); got != http.StatusUnauthorized {
		t.Fatalf("publication restored collector access: %d", got)
	}
}

func TestNamedFleetColdUnsupportedPolicyRefused(t *testing.T) {
	path, p, tokens := namedFleetReceiverPolicy(t, "leaf")
	if err := p.GrantFleetCoordinator(p.Agents[0].Address); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("invalid version 2 fixture before version refusal: %v", err)
	}
	p.Version = 3
	savePolicy(t, path, p)
	s, _, _, _ := newHTTPFixture(t, nil)
	s.Config.Set("principalsFile", path)
	bootstrap, err := NewBootstrap(s.Config, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unsupported cold policy reached the health handler")
	}), s.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	for name, handler := range map[string]http.Handler{"ready": s, "bootstrap": bootstrap} {
		for _, route := range []struct{ method, path string }{
			{"POST", "/mcp"}, {"GET", "/healthz"}, {"GET", statusCollectorPath},
		} {
			t.Run(name+route.path, func(t *testing.T) {
				var body []byte
				if route.method == "POST" {
					body = []byte("{}")
				}
				req := httptest.NewRequest(route.method, route.path, bytes.NewReader(body))
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set("Authorization", "Bearer "+tokens[0])
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" ||
					!bytes.Contains(rec.Body.Bytes(), []byte("policy_unavailable")) {
					t.Fatalf("cold version 3 response=%d", rec.Code)
				}
				if bytes.Contains(rec.Body.Bytes(), []byte(tokens[0])) || bytes.Contains(rec.Body.Bytes(), []byte(path)) {
					t.Fatal("cold refusal exposed credential or policy path")
				}
			})
		}
	}
}
