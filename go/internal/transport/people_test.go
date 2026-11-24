//go:build linux

package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

var peopleTestRoutes = []struct{ method, path string }{
	{http.MethodGet, "/people"},
	{http.MethodHead, "/people"},
	{http.MethodGet, "/people/data"},
	{http.MethodPost, "/people/actions"},
}

type peopleAuthFixture struct {
	server             *Server
	handler            http.Handler
	policyPath         string
	policy             *plansaccess.Policy
	ownerToken, legacy string
	calls              atomic.Int32
}

func newPeopleAuthFixture(t *testing.T) *peopleAuthFixture {
	t.Helper()
	server, _, legacy, _ := newHTTPFixture(t, nil)
	path, policy, tokens := namedPolicy(t)
	for i := range policy.Agents {
		policy.Agents[i].Kind = "concierge"
		policy.Agents[i].Address = "people/" + policy.Principals[i].ID + "/concierge"
	}
	savePolicy(t, path, policy)
	server.Config.Set("principalsFile", path)
	server.Config.Set("legacyBridge", false)
	f := &peopleAuthFixture{server: server, handler: server, policyPath: path, policy: policy, ownerToken: tokens[2], legacy: legacy}
	// Count every dispatch, including an alias that escaped the outer admission.
	server.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		who := wire.IdentityFrom(r.Context())
		if who.Source != "named" || who.PrincipalType != "person" || who.PrincipalID != "beta" ||
			who.Role != "owner" || who.AgentKind != "concierge" || who.AgentID != "crew-beta" || who.CredentialID != "laptop" {
			t.Error("People handler did not receive the current authenticated owner binding")
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("authenticated People fixture"))
		}
	})
	return f
}

func (f *peopleAuthFixture) request(t *testing.T, method, path, token string, headers http.Header, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Accept", "text/html")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for name, values := range headers {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	before := f.calls.Load()
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status=%d, want %d", method, r.URL.EscapedPath(), w.Code, want)
	}
	delta := int32(0)
	if want == http.StatusOK {
		delta = 1
	}
	if f.calls.Load()-before != delta {
		t.Fatalf("%s %s reached the handler %d times, want %d", method, r.URL.EscapedPath(), f.calls.Load()-before, delta)
	}
	return w
}

func (f *peopleAuthFixture) checkRoutes(t *testing.T, token string, want int) {
	t.Helper()
	for _, route := range peopleTestRoutes {
		f.request(t, route.method, route.path, token, nil, want)
	}
}

func TestPeopleRequiresCurrentNamedOwnerConcierge(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"owner concierge", 200}, {"owner crew", 403}, {"nonowner concierge", 403},
		{"both audience", 403}, {"agentmail audience", 401}, {"collector audience", 401},
		{"disabled person", 401}, {"retired concierge", 401}, {"revoked credential", 401},
		{"different person agent", 503}, {"system", 403}, {"imported fleet", 403},
		{"legacy bridge", 403}, {"legacy without policy", 403},
		{"missing credential", 401}, {"unknown credential", 401},
		{"malformed policy", 503}, {"unavailable policy", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeopleAuthFixture(t)
			p, token := f.policy, f.ownerToken
			switch tc.name {
			case "owner crew":
				p.Agents[1].Kind = "crew"
			case "nonowner concierge":
				p.Principals[1].Role = "engineer"
			case "both audience":
				p.Credentials[2].Audience = "both"
			case "agentmail audience":
				p.Credentials[2].Audience = "agentmail"
			case "collector audience":
				p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
			case "disabled person":
				p.Principals[1].Disabled = true
			case "retired concierge":
				p.Agents[1].Disabled = true
			case "revoked credential":
				p.Credentials[2].Revoked = true
			case "different person agent":
				p.Credentials[2].AgentID = p.Agents[0].ID
			case "system", "imported fleet":
				p.Principals[1].Type, p.Principals[1].Role = "system", ""
				p.Agents[1].Kind, p.Agents[1].Address = "leaf", "plans/people-test-fleet"
				if tc.name == "imported fleet" {
					token = strings.TrimPrefix(token, "pa1_")
					digest := sha256.Sum256([]byte(token))
					p.Credentials[2].Format, p.Credentials[2].Audience = plansaccess.ImportedFleetV1, "both"
					p.Credentials[2].SHA256 = hex.EncodeToString(digest[:])
				}
			case "legacy bridge":
				f.server.Config.Set("legacyBridge", true)
				token = f.legacy
			case "legacy without policy":
				f.server.Config.Set("principalsFile", "")
				token = f.legacy
			case "missing credential":
				token = ""
			case "unknown credential":
				token = "unknown-people-fixture"
			}
			savePolicy(t, f.policyPath, p)
			if tc.name == "malformed policy" {
				if err := os.WriteFile(f.policyPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "unavailable policy" {
				if err := os.Remove(f.policyPath); err != nil {
					t.Fatal(err)
				}
			}
			f.checkRoutes(t, token, tc.want)
		})
	}
}

func TestPeopleUsesFreshRoleAudienceAndRevocation(t *testing.T) {
	f := newPeopleAuthFixture(t)
	f.checkRoutes(t, f.ownerToken, http.StatusOK)
	f.policy.Principals[1].Role = "tester"
	savePolicy(t, f.policyPath, f.policy)
	f.checkRoutes(t, f.ownerToken, http.StatusForbidden)
	f.policy.Principals[1].Role = "owner"
	f.policy.Credentials[2].Audience = "both"
	savePolicy(t, f.policyPath, f.policy)
	f.checkRoutes(t, f.ownerToken, http.StatusForbidden)
	f.policy.Credentials[2].Audience = "codexpro"
	savePolicy(t, f.policyPath, f.policy)
	f.checkRoutes(t, f.ownerToken, http.StatusOK)
	f.policy.Credentials[2].Revoked = true
	savePolicy(t, f.policyPath, f.policy)
	f.checkRoutes(t, f.ownerToken, http.StatusUnauthorized)
}

func TestPeopleRefusesCredentialQueriesWithOrWithoutAuthorization(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		form string
	}{
		{[]string{"token"}, "value"}, {[]string{"codexpro_token"}, "value"},
		{[]string{"%74oken"}, "value"}, {[]string{"codexpro%5Ftoken"}, "value"},
		{[]string{"token", "token"}, "value"}, {[]string{"codexpro_token", "codexpro_token"}, "value"},
		{[]string{"token", "%74oken"}, "value"}, {[]string{"token", "codexpro_token"}, "value"},
		{[]string{"token"}, "bare"}, {[]string{"codexpro_token"}, "empty"},
		{[]string{"%74%6f%6b%65%6e"}, "value"},
	} {
		t.Run(strings.Join(tc.keys, "+")+"/"+tc.form, func(t *testing.T) {
			f := newPeopleAuthFixture(t)
			parts := make([]string, 0, len(tc.keys))
			for _, key := range tc.keys {
				part := key
				if tc.form != "bare" {
					part += "="
				}
				if tc.form == "value" {
					part += url.QueryEscape(f.ownerToken)
				}
				parts = append(parts, part)
			}
			rawQuery := strings.Join(parts, "&")
			for _, route := range peopleTestRoutes {
				for _, token := range []string{"", f.ownerToken} {
					f.request(t, route.method, route.path+"?"+rawQuery, token, nil, http.StatusUnauthorized)
				}
			}
		})
	}
}

func TestPeopleDoesNotAcceptCookieOrAmbiguousHeaderAuthority(t *testing.T) {
	for _, condition := range []string{"cookie only", "duplicate headers", "wrong scheme", "invalid header and valid cookie"} {
		t.Run(condition, func(t *testing.T) {
			f := newPeopleAuthFixture(t)
			headers := http.Header{}
			switch condition {
			case "cookie only":
				headers.Add("Cookie", (&http.Cookie{Name: "codexpro_token", Value: f.ownerToken}).String())
				headers.Add("Cookie", (&http.Cookie{Name: "token", Value: f.ownerToken}).String())
			case "duplicate headers":
				headers.Add("Authorization", "Bearer "+f.ownerToken)
				headers.Add("Authorization", "Bearer "+f.ownerToken)
			case "wrong scheme":
				headers.Set("Authorization", "Basic "+f.ownerToken)
			case "invalid header and valid cookie":
				headers.Set("Authorization", "Bearer invalid-people-fixture")
				headers.Set("Cookie", (&http.Cookie{Name: "codexpro_token", Value: f.ownerToken}).String())
			}
			for _, route := range peopleTestRoutes {
				f.request(t, route.method, route.path, "", headers, http.StatusUnauthorized)
			}
		})
	}
}

func TestPeopleRefusesEncodedPathsAliasesAndWrongMethodsBeforeDispatch(t *testing.T) {
	f := newPeopleAuthFixture(t)
	f.server.Config.Set("legacyBridge", true)
	for _, path := range []string{
		"/%70eople", "/people/%64ata", "/people%2fdata", "/people/%61ctions", "/people%2Factions",
		"/people/", "/people/data/", "/people/actions/", "/people.json", "/people/data.json",
		"/people/actions.json", "/people//data", "/people/./data", "/people/../people/data",
	} {
		for _, token := range []string{f.ownerToken, f.legacy} {
			f.request(t, http.MethodGet, path, token, nil, http.StatusForbidden)
		}
	}
	for _, route := range []struct {
		path    string
		methods []string
	}{
		{"/people", []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"}},
		{"/people/data", []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}},
		{"/people/actions", []string{"GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"}},
	} {
		for _, method := range route.methods {
			f.request(t, method, route.path, f.ownerToken, nil, http.StatusMethodNotAllowed)
		}
	}
}

func TestPeopleUnauthenticatedShellContainsNoPolicyFacts(t *testing.T) {
	f := newPeopleAuthFixture(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := f.request(t, method, "/people", "", nil, http.StatusUnauthorized)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") ||
			w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("sign-in shell lost its HTML, cache or referrer contract")
		}
		if method == http.MethodHead {
			if w.Body.Len() != 0 {
				t.Fatal("unauthenticated HEAD returned a People body")
			}
			continue
		}
		body := w.Body.String()
		if !strings.Contains(strings.ToLower(body), "<html") {
			t.Fatal("missing generic People sign-in shell")
		}
		for _, private := range []string{
			f.policy.Principals[0].ID, f.policy.Principals[1].ID, f.policy.Agents[1].ID,
			f.policy.Agents[1].Address, f.policy.Credentials[2].SHA256, f.ownerToken, f.policyPath,
		} {
			if strings.Contains(body, private) {
				t.Fatal("sign-in shell exposed fixture policy facts or credential material")
			}
		}
	}
}

func TestPeopleAuthenticatedContextIgnoresPostedIdentityHeaders(t *testing.T) {
	f := newPeopleAuthFixture(t)
	headers := http.Header{
		"X-Codexpro-Principal": []string{"alpha"},
		"X-Codexpro-Agent":     []string{"crew-alpha"},
		"X-Codexpro-Role":      []string{"owner"},
	}
	f.request(t, http.MethodPost, "/people/actions", f.ownerToken, headers, http.StatusOK)
	f.policy.Principals[1].Role = "tester"
	savePolicy(t, f.policyPath, f.policy)
	f.request(t, http.MethodPost, "/people/actions", f.ownerToken, headers, http.StatusForbidden)
}

func TestPeopleAdmissionPreservesOtherRoutesAuthentication(t *testing.T) {
	f := newPeopleAuthFixture(t)
	query := url.Values{"token": []string{f.ownerToken}}
	f.request(t, http.MethodGet, "/status?"+query.Encode(), "", nil, http.StatusOK)
	f.server.Config.Set("principalsFile", "")
	f.server.Mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if wire.IdentityFrom(r.Context()).Source != "legacy" {
			t.Error("unconfigured legacy status compatibility changed")
		}
		w.WriteHeader(http.StatusOK)
	})
	query = url.Values{"codexpro_token": []string{f.legacy}}
	f.request(t, http.MethodGet, "/status?"+query.Encode(), "", nil, http.StatusOK)
	f.request(t, http.MethodGet, "/people", f.legacy, nil, http.StatusForbidden)
}

func TestPeopleBootstrapRetainsAdmissionBeforeAndAfterPublication(t *testing.T) {
	f := newPeopleAuthFixture(t)
	b, err := NewBootstrap(f.server.Config, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	f.handler = b
	w := f.request(t, http.MethodGet, "/people/data", f.ownerToken, nil, http.StatusServiceUnavailable)
	if w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), "server_starting") {
		t.Fatal("authenticated pending People data did not report bounded startup unavailability")
	}
	f.request(t, http.MethodGet, "/people", "", nil, http.StatusUnauthorized)
	f.request(t, http.MethodGet, "/people/data", "", nil, http.StatusUnauthorized)
	f.request(t, http.MethodGet, "/people/data", f.legacy, nil, http.StatusUnauthorized)
	if err := b.Publish(f.server); err != nil {
		t.Fatal(err)
	}
	f.request(t, http.MethodGet, "/people/data", f.ownerToken, nil, http.StatusOK)
	f.request(t, http.MethodPost, "/people/actions", f.ownerToken, nil, http.StatusOK)
	f.policy.Principals[1].Role = "engineer"
	savePolicy(t, f.policyPath, f.policy)
	f.request(t, http.MethodGet, "/people/data", f.ownerToken, nil, http.StatusForbidden)
	f.policy.Principals[1].Role = "owner"
	f.policy.Credentials[2].Revoked = true
	savePolicy(t, f.policyPath, f.policy)
	f.request(t, http.MethodPost, "/people/actions", f.ownerToken, nil, http.StatusUnauthorized)
}
