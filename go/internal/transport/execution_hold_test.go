package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Exercise the real policy/HTTP boundary before the mux, not an inferred role
// or a synthetic identity installed directly on the protected control handler.
func TestExecutionHoldHTTPCurrentOwnerAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"owner concierge", 200}, {"owner crew", 403}, {"tester concierge", 403},
		{"engineer concierge", 403}, {"system leaf", 403}, {"both audience", 403},
		{"agentmail audience", 401}, {"collector audience", 401},
		{"disabled owner", 401}, {"retired concierge", 401}, {"revoked credential", 401},
		{"legacy bridge", 403}, {"legacy without policy", 200}, {"missing", 401},
		{"malformed policy", 503}, {"unavailable policy", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, legacy, _ := newHTTPFixture(t, nil)
			path, policy, credentials := namedPolicy(t)
			policy.Agents[1].Kind, policy.Agents[1].Address = "concierge", "people/beta/concierge"
			server.Config.Set("principalsFile", path)
			server.Config.Set("legacyBridge", false)
			credential := credentials[2]
			switch tc.name {
			case "owner crew":
				policy.Agents[1].Kind, policy.Agents[1].Address = "crew", "people/beta/crew"
			case "tester concierge":
				policy.Principals[1].Role = "tester"
			case "engineer concierge":
				policy.Principals[1].Role = "engineer"
			case "system leaf":
				policy.Principals[1].Type, policy.Principals[1].Role = "system", ""
				policy.Agents[1].Kind, policy.Agents[1].Address = "leaf", "plans/execution-control-fixture"
			case "both audience":
				policy.Credentials[2].Audience = "both"
			case "agentmail audience":
				policy.Credentials[2].Audience = "agentmail"
			case "collector audience":
				policy.Credentials[2].Audience = plansaccess.StatusCollectorAudience
			case "disabled owner":
				policy.Principals[1].Disabled = true
			case "retired concierge":
				policy.Agents[1].Disabled = true
			case "revoked credential":
				policy.Credentials[2].Revoked = true
			case "legacy bridge":
				server.Config.Set("legacyBridge", true)
				credential = legacy
			case "legacy without policy":
				server.Config.Set("principalsFile", "")
				credential = legacy
			case "missing":
				credential = ""
			}
			savePolicy(t, path, policy)
			if tc.name == "malformed policy" {
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "unavailable policy" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			var called atomic.Int32
			server.Mux.HandleFunc("/admin/execution-hold", func(w http.ResponseWriter, r *http.Request) {
				called.Add(1)
				id := wire.IdentityFrom(r.Context())
				if id.Source != "legacy" && (id.PrincipalType != "person" || id.Role != "owner" || id.AgentKind != "concierge") {
					t.Error("control did not receive the verified owner attribution")
				}
				w.WriteHeader(http.StatusOK)
			})
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				request := httptest.NewRequest(method, "http://fixture.test/admin/execution-hold", strings.NewReader("{}"))
				if credential != "" {
					request.Header.Set("Authorization", "Bearer "+credential)
				}
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, request)
				if recorder.Code != tc.want {
					t.Fatalf("%s status=%d want=%d body=%s", method, recorder.Code, tc.want, recorder.Body)
				}
			}
			wantCalls := int32(0)
			if tc.want == http.StatusOK {
				wantCalls = 2
			}
			if called.Load() != wantCalls {
				t.Fatalf("control calls=%d want=%d", called.Load(), wantCalls)
			}
		})
	}
}

func TestExecutionHoldHTTPRejectsAliasesBeforeMux(t *testing.T) {
	server, _, credential, _ := newHTTPFixture(t, nil)
	var called atomic.Int32
	server.Mux.HandleFunc("/admin/execution-hold", func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	for _, path := range []string{
		"/admin/%65xecution-hold", "/%61dmin/execution-hold", "/admin/execution%2dhold",
		"/admin//execution-hold", "/admin/./execution-hold", "/admin/extra/../execution-hold",
		"//admin/execution-hold", "/admin/execution-hold/", "/admin%2fexecution-hold",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request := httptest.NewRequest(method, "http://fixture.test"+path, nil)
			request.Header.Set("Authorization", "Bearer "+credential)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || recorder.Header().Get("Location") != "" {
				t.Fatalf("%s %s status=%d location=%q", method, path, recorder.Code, recorder.Header().Get("Location"))
			}
		}
	}
	if called.Load() != 0 {
		t.Fatal("an alias reached the execution control mux")
	}
}

func TestExecutionHoldHTTPUsesFreshAuthorityAndKeepsOtherAdminRoutes(t *testing.T) {
	server, _, _, _ := newHTTPFixture(t, nil)
	path, policy, credentials := namedPolicy(t)
	policy.Agents[1].Kind, policy.Agents[1].Address = "concierge", "people/beta/concierge"
	savePolicy(t, path, policy)
	server.Config.Set("principalsFile", path)
	server.Config.Set("legacyBridge", false)
	var called atomic.Int32
	for _, route := range []string{"/admin/execution-hold", "/admin/profile"} {
		server.Mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			called.Add(1)
			w.WriteHeader(http.StatusOK)
		})
	}
	check := func(route string, want int) {
		t.Helper()
		before := called.Load()
		request := httptest.NewRequest(http.MethodPost, "http://fixture.test"+route, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+credentials[2])
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s status=%d want=%d body=%s", route, recorder.Code, want, recorder.Body)
		}
		if (called.Load() != before) != (want == http.StatusOK) {
			t.Fatal("refusal reached a control handler")
		}
	}
	check("/admin/execution-hold", 200)
	policy.Principals[1].Role = "engineer"
	savePolicy(t, path, policy)
	check("/admin/execution-hold", 403)
	policy.Principals[1].Role = "owner"
	policy.Credentials[2].Audience = "both"
	savePolicy(t, path, policy)
	check("/admin/execution-hold", 403)
	policy.Credentials[2].Audience = "codexpro"
	savePolicy(t, path, policy)
	check("/admin/execution-hold", 200)
	policy.Credentials[2].Revoked = true
	savePolicy(t, path, policy)
	check("/admin/execution-hold", 401)
	policy.Credentials[2].Revoked = false
	policy.Principals[1].Type, policy.Principals[1].Role = "system", ""
	policy.Agents[1].Kind, policy.Agents[1].Address = "leaf", "plans/execution-control-fixture"
	savePolicy(t, path, policy)
	check("/admin/execution-hold", 403)
	check("/admin/profile", 200)
}
