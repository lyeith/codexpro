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

func TestColdPolicyFailureDoesNotCountAsBadCredentials(t *testing.T) {
	s, remote, _, _ := newHTTPFixture(t, nil)
	path, p, tokens := namedPolicy(t)
	s.Config.Set("principalsFile", path)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 24; i++ {
		status, headers, body := request(t, remote, "POST", "/mcp", "{}", "", tokens[0], nil)
		if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" ||
			!bytes.Contains(body, []byte("policy_unavailable")) ||
			bytes.Contains(body, []byte(tokens[0])) || bytes.Contains(body, []byte(path)) {
			t.Fatalf("cold failure %d returned unsafe or incorrect response: status=%d", i, status)
		}
	}
	s.mu.Lock()
	failures := len(s.failures)
	s.mu.Unlock()
	if failures != 0 {
		t.Fatal("policy outage consumed the credential failure budget")
	}
	savePolicy(t, path, p)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil); status != http.StatusOK {
		t.Fatalf("repaired policy did not recover immediately: %d", status)
	}
	for i := 0; i < 11; i++ {
		want := http.StatusUnauthorized
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if status, _, _ := request(t, remote, "POST", "/mcp", "{}", "", "incorrect-fixture-credential", nil); status != want {
			t.Fatalf("real credential failure %d returned %d, want %d", i, status, want)
		}
	}
}

func TestValidatedRestrictionsSurviveMalformedReload(t *testing.T) {
	cases := []struct {
		name   string
		change func(*plansaccess.Policy)
		want   int
	}{
		{"revocation", func(p *plansaccess.Policy) { p.Credentials[0].Revoked = true }, http.StatusUnauthorized},
		{"credential removal", func(p *plansaccess.Policy) { p.Credentials = p.Credentials[1:] }, http.StatusUnauthorized},
		{"disabled principal", func(p *plansaccess.Policy) { p.Principals[0].Disabled = true }, http.StatusUnauthorized},
		{"disabled agent", func(p *plansaccess.Policy) { p.Agents[0].Disabled = true }, http.StatusUnauthorized},
		{"audience reduction", func(p *plansaccess.Policy) { p.Credentials[0].Audience = "agentmail" }, http.StatusUnauthorized},
		{"credential rebinding", func(p *plansaccess.Policy) {
			p.Credentials[0].PrincipalID, p.Credentials[0].AgentID = p.Principals[1].ID, p.Agents[1].ID
		}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, remote, _, _ := newHTTPFixture(t, nil)
			path, p, tokens := namedPolicy(t)
			s.Config.Set("principalsFile", path)
			session := initialize(t, remote, tokens[0], "2025-11-25")
			const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
			tc.change(p)
			savePolicy(t, path, p)
			check := func() {
				t.Helper()
				if status, _, _ := request(t, remote, "POST", "/mcp", list, session, tokens[0], nil); status != tc.want {
					t.Fatalf("restriction returned %d, want %d", status, tc.want)
				}
			}
			check()
			if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			check()
			// A different principal remains usable from the same validated snapshot.
			initialize(t, remote, tokens[2], "2025-11-25")
		})
	}
}

func TestBootstrapAndCollectorShareLastValidatedPolicy(t *testing.T) {
	s, _, _, _ := newHTTPFixture(t, nil)
	path, p, tokens := namedPolicy(t)
	p.Principals[0].Type, p.Principals[0].Role = "system", ""
	p.Agents[0].Kind, p.Agents[0].Address = "leaf", "system/alpha"
	p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
	p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
	savePolicy(t, path, p)
	s.Config.Set("principalsFile", path)
	b, err := NewBootstrap(s.Config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), s.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s.Mux.HandleFunc(statusCollectorPath, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	call := func(handler http.Handler, target, credential string) int {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := call(b, "/healthz", tokens[0]); got != http.StatusOK {
		t.Fatalf("bootstrap=%d", got)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("collector=%d", got)
	}
	p.Credentials[0].Revoked = true
	savePolicy(t, path, p)
	// Only the collector observes the valid restriction before the next bad write.
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("collector refresh=%d", got)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call(b, "/healthz", tokens[0]); got != http.StatusUnauthorized {
		t.Fatalf("bootstrap resurrected authority observed by collector: %d", got)
	}
	if got := call(s, statusCollectorPath, tokens[2]); got != http.StatusOK {
		t.Fatalf("collector lost unaffected authority: %d", got)
	}
	// A newly created service has no validated snapshot to fall back to.
	cold, err := NewBootstrap(s.Config, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("cold invalid policy reached the health handler")
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	if got := call(cold, statusCollectorPath, tokens[2]); got != http.StatusServiceUnavailable {
		t.Fatalf("cold collector failure=%d", got)
	}
}
