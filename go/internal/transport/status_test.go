package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestStatusUsesFreshOwnerIdentityOnlyOnExactRoute(t *testing.T) {
	s, remote, legacy, _ := newHTTPFixture(t, nil)
	var rendered int
	s.Mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		rendered++
		who := wire.IdentityFrom(r.Context())
		if who.Source != "legacy" && (who.PrincipalType != "person" || who.Role != "owner") {
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
			before := rendered
			code, _, body := request(t, remote, verb, path, "", "", token, nil)
			if code != status {
				t.Fatalf("%s %s status %d, want %d", verb, path, code, status)
			}
			if status != 200 && rendered != before {
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
	p.Agents[1].Kind = "concierge"
	p.Agents[1].Address = "people/beta/custom-device"
	raw := strings.TrimPrefix(tokens[0], "pa1_")
	digest := sha256.Sum256([]byte(raw))
	p.Principals = append(p.Principals, plansaccess.Principal{ID: "fleet-service", Type: "system"})
	p.Agents = append(p.Agents, plansaccess.Agent{ID: "fleet", PrincipalID: "fleet-service", Kind: "leaf", Address: "plans/fleet"})
	p.Credentials = append(p.Credentials, plansaccess.Credential{ID: "opaque", Device: "fleet", PrincipalID: "fleet-service", AgentID: "fleet", Audience: "both", Format: plansaccess.ImportedFleetV1, SHA256: hex.EncodeToString(digest[:])})
	savePolicy(t, path, p)
	check(tokens[2], "/status", 200)
	check(raw, "/status", 403)
	check(tokens[2], "/%73tatus", 403)
	check(tokens[2], "/activity", 403)
	code, _, _ := request(t, remote, "POST", "/status", "", "", tokens[2], nil)
	if code != 403 {
		t.Fatalf("POST %d", code)
	}
	p.Credentials[2].Revoked = true
	savePolicy(t, path, p)
	check(tokens[2], "/status", 401)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	check(raw, "/status", 401)
}
