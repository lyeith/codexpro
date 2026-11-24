package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStatusUsesFreshOwnerIdentityOnlyOnExactRoute(t *testing.T) {
	s, remote, legacy, _ := newHTTPFixture(t, nil)
	var rendered atomic.Int32
	s.Mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		rendered.Add(1)
		who := wire.IdentityFrom(r.Context())
		if who.Source != "legacy" && (who.PrincipalType != "person" || who.Role != "owner" || who.AgentKind != "concierge") {
			t.Error("verified identity was not forwarded")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != "HEAD" {
			_, _ = w.Write([]byte("owner snapshot"))
		}
	})
	check := func(token, path string, status int) {
		t.Helper()
		for _, verb := range []string{"GET", "HEAD"} {
			before := rendered.Load()
			code, _, body := request(t, remote, verb, path, "", "", token, nil)
			if code != status {
				t.Fatalf("%s %s status %d, want %d", verb, path, code, status)
			}
			if status != 200 && rendered.Load() != before {
				t.Fatal("denied request reached page")
			}
			if verb == "HEAD" && len(body) != 0 {
				t.Fatal("HEAD returned body")
			}
		}
	}
	check(legacy, "/status", 200)
	check("wrong", "/status", 401)
	path, p, tokens := namedPolicy(t)
	// The fixture's owner is initially using a crew credential.
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", false)
	check(tokens[0], "/status", 403)
	check(tokens[2], "/status", 403)
	check(legacy, "/status", 401)
	p.Agents[0].Kind = "concierge"
	p.Agents[0].Address = "people/alpha/concierge"
	savePolicy(t, path, p)
	check(tokens[0], "/status", 403)
	p.Agents[1].Kind = "concierge"
	p.Agents[1].Address = "people/beta/custom-device"
	raw := strings.TrimPrefix(tokens[0], "pa1_")
	digest := sha256.Sum256([]byte(raw))
	p.Principals = append(p.Principals, plansaccess.Principal{ID: "fleet-service", Type: "system"})
	p.Agents = append(p.Agents, plansaccess.Agent{ID: "fleet", PrincipalID: "fleet-service", Kind: "leaf", Address: "plans/fleet"})
	p.Credentials = append(p.Credentials, plansaccess.Credential{ID: "opaque", Device: "fleet", PrincipalID: "fleet-service", AgentID: "fleet", Audience: "both", Format: plansaccess.ImportedFleetV1, SHA256: hex.EncodeToString(digest[:])})
	savePolicy(t, path, p)
	check(tokens[2], "/status", 200)
	p.Credentials[2].Audience = "both"
	savePolicy(t, path, p)
	check(tokens[2], "/status", 403)
	p.Credentials[2].Audience = "codexpro"
	savePolicy(t, path, p)
	check(raw, "/status", 403)
	check(tokens[2], "/%73tatus", 403)
	check(tokens[2], "/activity/live", 403)
	code, _, _ := request(t, remote, "POST", "/status", "", "", tokens[2], nil)
	if code != 403 {
		t.Fatalf("POST %d", code)
	}
	p.Credentials[2].Revoked = true
	savePolicy(t, path, p)
	check(tokens[2], "/status", 401)
}

func TestStatusRefusesInvalidOwnerBindings(t *testing.T) {
	for _, condition := range []string{"retired concierge", "wrong audience", "person coordinator", "malformed policy"} {
		t.Run(condition, func(t *testing.T) {
			s, remote, _, _ := newHTTPFixture(t, nil)
			path, p, tokens := namedPolicy(t)
			p.Agents[1].Kind = "concierge"
			p.Agents[1].Address = "people/beta/concierge"
			switch condition {
			case "retired concierge":
				p.Agents[1].Disabled = true
			case "wrong audience":
				p.Credentials[2].Audience = "agentmail"
			case "person coordinator":
				p.Agents[1].Kind = "coordinator"
			}
			savePolicy(t, path, p)
			if condition == "malformed policy" {
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.Config.Set("principalsFile", path)
			s.Config.Set("legacyBridge", false)
			s.Mux.HandleFunc("/status", func(http.ResponseWriter, *http.Request) { t.Error("invalid identity reached the page") })
			want := http.StatusUnauthorized
			if condition == "malformed policy" || condition == "person coordinator" {
				want = http.StatusServiceUnavailable
			}
			for _, verb := range []string{"GET", "HEAD"} {
				code, _, _ := request(t, remote, verb, "/status", "", "", tokens[2], nil)
				if code != want {
					t.Fatalf("%s returned %d", verb, code)
				}
			}
		})
	}
}
