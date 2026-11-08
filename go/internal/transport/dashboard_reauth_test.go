//go:build linux

package transport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type dashboardReauthFixture struct {
	server     *Server
	ownerToken string
	fleetToken string
	dispatched atomic.Int32
	identity   wire.Identity
	private    []string
}

func newDashboardReauthFixture(t *testing.T) *dashboardReauthFixture {
	t.Helper()
	server, _, _, _ := newHTTPFixture(t, nil)
	path, policy, tokens := namedPolicy(t)
	policy.Agents[1].Kind, policy.Agents[1].Address = "concierge", "people/beta/concierge"
	fleet := strings.TrimPrefix(tokens[0], "pa1_")
	digest := sha256.Sum256([]byte(fleet))
	policy.Principals = append(policy.Principals, plansaccess.Principal{ID: "dashboard-fleet-private", Type: "system"})
	policy.Agents = append(policy.Agents, plansaccess.Agent{
		ID: "dashboard-fleet-agent", PrincipalID: "dashboard-fleet-private", Kind: "leaf", Address: "plans/dashboard-fleet",
	})
	policy.Credentials = append(policy.Credentials, plansaccess.Credential{
		ID: "dashboard-fleet-credential", Device: "synthetic-fleet", PrincipalID: "dashboard-fleet-private",
		AgentID: "dashboard-fleet-agent", Audience: "both", Format: plansaccess.ImportedFleetV1,
		SHA256: hex.EncodeToString(digest[:]),
	})
	if err := policy.Validate(); err != nil {
		t.Fatalf("invalid dashboard credential-recovery policy fixture: %v", err)
	}
	savePolicy(t, path, policy)
	server.Config.Set("principalsFile", path)
	server.Config.Set("legacyBridge", false)
	f := &dashboardReauthFixture{
		server: server, ownerToken: tokens[2], fleetToken: fleet,
		private: []string{
			"private-dashboard-roster-fixture", path, tokens[2], fleet,
			policy.Principals[0].ID, policy.Principals[1].ID,
			policy.Agents[1].ID, policy.Agents[1].Address, policy.Credentials[2].SHA256,
			"dashboard-fleet-private", "dashboard-fleet-agent", "dashboard-fleet-credential",
		},
	}
	// Any accidental dispatch exposes the marker, including routing aliases.
	server.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.dispatched.Add(1)
		f.identity = wire.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("private-dashboard-roster-fixture"))
		}
	})
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() {
		log.SetOutput(previous)
		for _, private := range f.private {
			if strings.Contains(logs.String(), private) {
				t.Error("credential recovery logged credential or policy details")
			}
		}
	})
	return f
}

func (f *dashboardReauthFixture) request(t *testing.T, method, target, credential, accept string, extra http.Header, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Accept", accept)
	if credential != "" {
		r.Header.Set("Authorization", "Bearer "+credential)
	}
	for name, values := range extra {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	before := f.dispatched.Load()
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status=%d, want %d", method, r.URL.Path, w.Code, want)
	}
	wantDispatch := int32(0)
	if want == http.StatusOK {
		wantDispatch = 1
	}
	if f.dispatched.Load()-before != wantDispatch {
		t.Fatalf("%s %s reached a protected handler unexpectedly", method, r.URL.Path)
	}
	if want != http.StatusOK {
		for _, private := range f.private {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("credential recovery exposed credential or policy details")
			}
			for _, values := range w.Header() {
				if strings.Contains(strings.Join(values, "\n"), private) {
					t.Fatal("credential recovery reflected private material in headers")
				}
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("credential refusal lost cache or referrer protection")
		}
	}
	return w
}

func requireDashboardCredentialHelp(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(w.Body.String(), "id=\"dashboard-auth-form\"") ||
		!strings.Contains(strings.ToLower(w.Body.String()), "personal browser credential") ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action 'none'") {
		t.Fatal("missing protected dashboard credential-entry response")
	}
}

func TestDashboardBrowserCredentialHelpPreservesOwnerAdmission(t *testing.T) {
	f := newDashboardReauthFixture(t)
	for _, path := range []string{"/activity", "/status"} {
		t.Run(path, func(t *testing.T) {
			requireDashboardCredentialHelp(t, f.request(t, http.MethodGet, path, "", "text/html", nil, http.StatusUnauthorized))
			requireDashboardCredentialHelp(t, f.request(t, http.MethodGet, path, f.fleetToken, "text/html", nil, http.StatusForbidden))
			w := f.request(t, http.MethodGet, path, f.ownerToken, "text/html", nil, http.StatusOK)
			if w.Body.String() != "private-dashboard-roster-fixture" ||
				f.identity.Source != "named" || f.identity.PrincipalID != "beta" ||
				f.identity.Role != "owner" || f.identity.AgentKind != "concierge" {
				t.Fatal("valid personal owner credential did not reach the protected page with its verified identity")
			}
		})
	}
}

func TestDashboardCredentialHelpPreservesOtherHTTPContracts(t *testing.T) {
	f := newDashboardReauthFixture(t)
	for _, tc := range []struct {
		name, method, path, accept, credential, contentType string
		status                                              int
		help                                                bool
	}{
		{"activity JSON", http.MethodGet, "/activity", "application/json", f.fleetToken, "application/json", 403, false},
		{"status JSON", http.MethodGet, "/status", "application/json", f.fleetToken, "application/json", 403, false},
		{"data HTML", http.MethodGet, "/activity/data", "text/html", f.fleetToken, "application/json", 403, false},
		{"owner helper refusal", http.MethodGet, "/activity.json", "text/html", f.ownerToken, "application/json", 403, false},
		{"encoded activity", http.MethodGet, "/%61ctivity", "text/html", f.fleetToken, "application/json", 403, false},
		{"encoded status", http.MethodGet, "/%73tatus", "text/html", f.fleetToken, "application/json", 403, false},
		{"wrong method", http.MethodPost, "/activity", "text/html", f.fleetToken, "application/json", 403, false},
		{"HEAD", http.MethodHead, "/status", "text/html", f.fleetToken, "application/json", 403, false},
		{"missing data JSON", http.MethodGet, "/activity/data", "application/json", "", "text/html", 401, false},
		// Missing-auth recovery deliberately retains its historical broader predicate.
		{"missing data HTML", http.MethodGet, "/activity/data", "text/html", "", "text/html", 401, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.request(t, tc.method, tc.path, tc.credential, tc.accept, nil, tc.status)
			if !strings.HasPrefix(w.Header().Get("Content-Type"), tc.contentType) {
				t.Fatal("credential recovery changed a non-page response format")
			}
			if tc.help {
				requireDashboardCredentialHelp(t, w)
			} else if strings.Contains(w.Body.String(), "dashboard-auth-form") {
				t.Fatal("browser credential form escaped its refusal boundary")
			}
			if tc.contentType == "application/json" && tc.method != http.MethodHead {
				var failure struct {
					OK    bool
					Error struct{ Code, Message string }
				}
				if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.OK ||
					failure.Error.Code != "forbidden" || failure.Error.Message != "Access denied" {
					t.Fatal("non-browser owner refusal lost its fixed JSON error")
				}
			}
		})
	}
	f.request(t, http.MethodGet, "/activity.json", f.fleetToken, "application/json", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/activity/data", f.ownerToken, "application/json", nil, http.StatusOK)
}

func TestDashboardCredentialHelpPreservesQueryAndHeaderCompatibility(t *testing.T) {
	f := newDashboardReauthFixture(t)
	for _, path := range []string{"/activity", "/status"} {
		f.request(t, http.MethodGet, path, f.ownerToken, "text/html", nil, http.StatusOK)
		for _, key := range []string{"codexpro_token", "token"} {
			query := url.Values{key: {f.ownerToken}}.Encode()
			f.request(t, http.MethodGet, path+"?"+query, "", "text/html", nil, http.StatusOK)
		}
	}
	query := url.Values{"codexpro_token": {f.ownerToken}}.Encode()
	requireDashboardCredentialHelp(t, f.request(t, http.MethodGet, "/activity?"+query, f.ownerToken, "text/html", nil, http.StatusUnauthorized))
	duplicateQuery := url.Values{"token": {f.ownerToken}, "codexpro_token": {f.ownerToken}}.Encode()
	requireDashboardCredentialHelp(t, f.request(t, http.MethodGet, "/activity?"+duplicateQuery, "", "text/html", nil, http.StatusUnauthorized))
	requireDashboardCredentialHelp(t, f.request(t, http.MethodGet, "/status", f.ownerToken, "text/html",
		http.Header{"Authorization": {"Bearer " + f.ownerToken}}, http.StatusUnauthorized))
}

func TestPeopleBrowserCredentialRecoveryKeepsHeaderOnlyOwnerBoundary(t *testing.T) {
	f := newDashboardReauthFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, tc := range []struct {
			credential string
			status     int
		}{
			{"", http.StatusUnauthorized},
			{f.fleetToken, http.StatusForbidden},
			{f.ownerToken, http.StatusOK},
		} {
			w := f.request(t, method, "/people", tc.credential, "text/html", nil, tc.status)
			if method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatal("People HEAD recovery returned a body")
			}
			if tc.status != http.StatusOK {
				if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
					t.Fatal("People browser refusal did not return its content-free sign-in shell")
				}
				if method == http.MethodGet && !strings.Contains(strings.ToLower(w.Body.String()), "<html") {
					t.Fatal("People browser refusal lost its sign-in shell")
				}
			}
		}
	}
	for _, tc := range []struct{ method, path, accept string }{
		{http.MethodGet, "/people", "application/json"},
		{http.MethodGet, "/people/data", "text/html"},
		{http.MethodPost, "/people/actions", "text/html"},
	} {
		w := f.request(t, tc.method, tc.path, f.fleetToken, tc.accept, nil, http.StatusForbidden)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("People non-page refusal changed to HTML")
		}
	}
	for _, key := range []string{"codexpro_token", "token"} {
		query := url.Values{key: {f.ownerToken}}.Encode()
		w := f.request(t, http.MethodGet, "/people?"+query, f.ownerToken, "text/html", nil, http.StatusUnauthorized)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("People query-credential refusal was replaced by a browser shell")
		}
	}
}
