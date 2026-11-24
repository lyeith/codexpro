//go:build linux

package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestActivityOwnerPagesUseFreshPolicy(t *testing.T) {
	s, remote, legacy, _ := newHTTPFixture(t, nil)
	var rendered atomic.Int32
	for _, route := range []string{"/activity", "/activity/data"} {
		s.Mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			rendered.Add(1)
			who := wire.IdentityFrom(r.Context())
			if who.Source != "legacy" && (who.PrincipalType != "person" || who.Role != "owner" || who.AgentKind != "concierge") {
				t.Error("activity page did not receive the verified owner identity")
			}
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("owner activity snapshot"))
			}
		})
	}
	check := func(token string, want int) {
		t.Helper()
		for _, route := range []string{"/activity", "/activity/data"} {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				before := rendered.Load()
				code, _, body := request(t, remote, method, route, "", "", token, nil)
				if code != want {
					t.Fatalf("%s %s status=%d, want %d", method, route, code, want)
				}
				wantRendered := before
				if want == http.StatusOK {
					wantRendered++
				}
				if rendered.Load() != wantRendered {
					t.Fatalf("%s %s reached the page %d times, want %d", method, route, rendered.Load()-before, wantRendered-before)
				}
				if method == http.MethodHead && len(body) != 0 {
					t.Fatal("HEAD returned an activity body")
				}
				if method == http.MethodGet && want == http.StatusOK && string(body) != "owner activity snapshot" {
					t.Fatal("admitted request did not receive the activity page")
				}
			}
		}
	}
	check(legacy, http.StatusOK)

	path, p, tokens := namedPolicy(t)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", false)
	check(tokens[2], http.StatusForbidden) // An owner crew is not the owner concierge.
	p.Agents[0].Kind, p.Agents[0].Address = "concierge", "people/alpha/concierge"
	p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
	savePolicy(t, path, p)
	check(tokens[0], http.StatusForbidden) // A tester concierge is not an owner.
	check(tokens[2], http.StatusOK)

	// The same credential observes changed authority on its very next request.
	p.Principals[1].Role = "tester"
	savePolicy(t, path, p)
	check(tokens[2], http.StatusForbidden)
	p.Principals[1].Role = "owner"
	p.Credentials[2].Audience = "both"
	savePolicy(t, path, p)
	check(tokens[2], http.StatusForbidden)
	p.Credentials[2].Audience = "codexpro"
	savePolicy(t, path, p)
	check(tokens[2], http.StatusOK)
	p.Credentials[2].Revoked = true
	savePolicy(t, path, p)
	check(tokens[2], http.StatusUnauthorized)

	// A bridge credential is not an owner in a configured named policy.
	s.Config.Set("legacyBridge", true)
	check(legacy, http.StatusForbidden)
	s.Config.Set("legacyBridge", false)
	check(legacy, http.StatusUnauthorized)
}

func TestActivityOwnerRoutesPreserveFleetConsumers(t *testing.T) {
	s, remote, _, _ := newHTTPFixture(t, nil)
	path, p, tokens := namedPolicy(t)
	p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
	fleet := strings.TrimPrefix(tokens[0], "pa1_")
	digest := sha256.Sum256([]byte(fleet))
	p.Principals = append(p.Principals, plansaccess.Principal{ID: "activity-fleet", Type: "system"})
	p.Agents = append(p.Agents, plansaccess.Agent{ID: "activity-leaf", PrincipalID: "activity-fleet", Kind: "leaf", Address: "plans/activity-fleet"})
	p.Credentials = append(p.Credentials, plansaccess.Credential{ID: "activity-fleet-credential", Device: "fleet",
		PrincipalID: "activity-fleet", AgentID: "activity-leaf", Audience: "both",
		Format: plansaccess.ImportedFleetV1, SHA256: hex.EncodeToString(digest[:])})
	savePolicy(t, path, p)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", false)

	pages := []string{"/activity", "/activity/data"}
	helpers := []string{"/activity.json", "/activity/projects/alpha.json", "/activity/live", "/activity/job", "/activity/diff", "/activity/batch"}
	var rendered atomic.Int32
	for _, route := range append(append([]string{}, pages...), helpers...) {
		s.Mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			rendered.Add(1)
			w.WriteHeader(http.StatusOK)
		})
	}
	check := func(token, method, route string, want int) {
		t.Helper()
		before := rendered.Load()
		code, _, _ := request(t, remote, method, route, "", "", token, nil)
		if code != want {
			t.Fatalf("%s %s status=%d, want %d", method, route, code, want)
		}
		wantRendered := before
		if want == http.StatusOK {
			wantRendered++
		}
		if rendered.Load() != wantRendered {
			t.Fatalf("%s %s reached an unexpected handler", method, route)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, route := range pages {
			check(tokens[2], method, route, http.StatusOK)
			check(fleet, method, route, http.StatusForbidden)
		}
		for _, route := range helpers {
			check(fleet, method, route, http.StatusOK)
			check(tokens[2], method, route, http.StatusForbidden)
		}
	}
	check(tokens[2], http.MethodGet, "/activity/data?agent=SP3", http.StatusOK)
	for _, route := range []string{"/%61ctivity", "/activity/%64ata", "/activity%2Fdata"} {
		for _, token := range []string{tokens[2], fleet} {
			check(token, http.MethodGet, route, http.StatusForbidden)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, route := range pages {
			check(tokens[2], method, route, http.StatusForbidden)
			check(fleet, method, route, http.StatusForbidden)
		}
	}
}

func TestActivityOwnerPagesRefuseUnknownAuthentication(t *testing.T) {
	for _, condition := range []string{"missing credential", "unknown credential", "retired concierge", "wrong audience", "collector audience", "malformed policy"} {
		t.Run(condition, func(t *testing.T) {
			s, remote, _, _ := newHTTPFixture(t, nil)
			path, p, tokens := namedPolicy(t)
			p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
			token := tokens[2]
			switch condition {
			case "missing credential":
				token = ""
			case "unknown credential":
				token = "unknown-fixture"
			case "retired concierge":
				p.Agents[1].Disabled = true
			case "wrong audience":
				p.Credentials[2].Audience = "agentmail"
			case "collector audience":
				p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
			}
			savePolicy(t, path, p)
			if condition == "malformed policy" {
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.Config.Set("principalsFile", path)
			s.Config.Set("legacyBridge", true)
			want := http.StatusUnauthorized
			if condition == "malformed policy" {
				want = http.StatusServiceUnavailable
			}
			for _, route := range []string{"/activity", "/activity/data"} {
				s.Mux.HandleFunc(route, func(http.ResponseWriter, *http.Request) {
					t.Error("unavailable owner authentication reached the activity page")
				})
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					code, _, _ := request(t, remote, method, route, "", "", token, nil)
					if code != want {
						t.Fatalf("%s %s status=%d, want %d", method, route, code, want)
					}
				}
			}
		})
	}
}
