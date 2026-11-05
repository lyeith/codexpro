//go:build linux

package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestImportedFleetDoesNotFallBackToSharedCredential(t *testing.T) {
	path, p, generated := namedPolicy(t)
	raw := strings.TrimPrefix(generated[0], "pa1_")
	digest := sha256.Sum256([]byte(raw))
	p.Principals = append(p.Principals, plansaccess.Principal{ID: "sp-fleet", Type: "system"})
	p.Agents = append(p.Agents, plansaccess.Agent{ID: "sp-service", PrincipalID: "sp-fleet", Kind: "leaf", Address: "plans/fleet"})
	p.Credentials = append(p.Credentials, plansaccess.Credential{ID: "imported", Device: "connector", PrincipalID: "sp-fleet", AgentID: "sp-service", Audience: "both", Format: plansaccess.ImportedFleetV1, SHA256: hex.EncodeToString(digest[:])})
	savePolicy(t, path, p)
	s, _, _, _ := newHTTPFixture(t, nil)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	s.credential = raw
	var actor wire.Identity
	s.Mux.HandleFunc("/test-fleet", func(w http.ResponseWriter, r *http.Request) {
		actor = wire.IdentityFrom(r.Context())
		w.WriteHeader(204)
	})
	check := func(want int) {
		t.Helper()
		req := httptest.NewRequest("GET", "/test-fleet", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		req.Header.Set("X-Codexpro-Principal", "david")
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("fleet status %d, want %d", rr.Code, want)
		}
	}
	check(204)
	if actor.Source != "named" || actor.PrincipalType != "system" || actor.PrincipalID != "sp-fleet" || actor.AgentID != "sp-service" || actor.AgentKind != "leaf" || actor.CredentialID != "imported" {
		t.Fatal("fleet did not retain its registered service identity")
	}
	p.Credentials[len(p.Credentials)-1].Revoked = true
	savePolicy(t, path, p)
	check(401)
}
