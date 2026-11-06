package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const (
	peoplePrincipalJSON = `{"id":"owner-fixture","type":"person","role":"owner","projects":[],"disabled":false,"has_delegations":false}`
	peopleAgentJSON     = `{"id":"concierge-fixture","principal_id":"owner-fixture","kind":"concierge","address":"people/owner-fixture/concierge","project_id":"","disabled":false}`
	peopleDeviceJSON    = `{"id":"device-fixture","principal_id":"owner-fixture","agent_id":"concierge-fixture","device":"desktop","audience":"codexpro","revoked":false,"can_revoke":true}`
	peopleAddJSON       = `{"kind":"add_person","id":"new-person","role":"tester"}`
)

type peopleTestOwner func(context.Context, PeopleRequest) PeopleResponse

func (f peopleTestOwner) Execute(ctx context.Context, request PeopleRequest) PeopleResponse {
	return f(ctx, request)
}

func peopleTestIdentity() wire.Identity {
	return wire.Identity{Source: "named", PrincipalType: "person", PrincipalID: "owner-fixture", Role: "owner",
		AgentID: "concierge-fixture", AgentKind: "concierge", CredentialID: "device-fixture"}
}

func peopleTestActor() PeopleActor {
	return PeopleActor{PrincipalID: "owner-fixture", AgentID: "concierge-fixture", CredentialID: "device-fixture"}
}

func peopleTestReadResponse() PeopleResponse {
	return PeopleResponse{OK: true, Self: &PeopleSelf{PrincipalID: "owner-fixture", AgentID: "concierge-fixture",
		CredentialID: "device-fixture", Role: "owner"}, Roster: &PeopleRoster{
		Principals: []PeoplePrincipal{{ID: "owner-fixture", Type: "person", Role: "owner", Projects: []string{}}},
		Agents:     []PeopleAgent{{ID: "concierge-fixture", PrincipalID: "owner-fixture", Kind: "concierge", Address: "people/owner-fixture/concierge"}},
		Devices: []PeopleDevice{{ID: "device-fixture", PrincipalID: "owner-fixture", AgentID: "concierge-fixture",
			Device: "desktop", Audience: "codexpro", CanRevoke: true}},
	}}
}

func peopleTestHTTPRequest(method, path, body string, identity wire.Identity) *http.Request {
	r := httptest.NewRequest(method, "http://people.test:7210"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r.WithContext(wire.WithIdentity(r.Context(), identity))
}

func servePeopleTestHTTP(t *testing.T, handler http.Handler, r *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s returned %d, want %d", r.Method, r.URL.Path, w.Code, want)
	}
	return w
}

func TestPeopleHTTPReadUsesAuthenticatedActorWithoutApplying(t *testing.T) {
	var seen []PeopleRequest
	owner := peopleTestOwner(func(_ context.Context, request PeopleRequest) PeopleResponse {
		seen = append(seen, request)
		return peopleTestReadResponse()
	})
	mux := http.NewServeMux()
	RegisterPeopleRoutes(mux, owner, nil)
	r := peopleTestHTTPRequest(http.MethodGet, "/people/data", peopleAddJSON, peopleTestIdentity())
	r.Header.Set("X-Codexpro-Principal", "posted-person")
	r.Header.Set("X-Codexpro-Agent", "posted-agent")
	w := servePeopleTestHTTP(t, mux, r, http.StatusOK)
	if len(seen) != 1 || seen[0].Version != 1 || seen[0].Operation != "read" ||
		seen[0].Actor != peopleTestActor() || len(seen[0].Action) != 0 {
		t.Fatal("GET did not make exactly one actor-bound read with no action")
	}
	var response PeopleResponse
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || !reflect.DeepEqual(response, peopleTestReadResponse()) {
		t.Fatal("read metadata changed at the HTTP boundary")
	}
	for _, identity := range []wire.Identity{peopleTestIdentity(), {}} {
		want := http.StatusOK
		if identity.Source == "" {
			want = http.StatusUnauthorized
		}
		servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodGet, "/people", "", identity), want)
	}
	if len(seen) != 1 {
		t.Fatal("page rendering invoked the policy owner")
	}
}

func TestPeopleHTTPForwardsOnlySixExactActionsAndExpectedMetadata(t *testing.T) {
	for _, action := range []string{
		peopleAddJSON,
		`{"kind":"change_role","id":"owner-fixture","role":"engineer","expected":` + peoplePrincipalJSON + `}`,
		`{"kind":"disable_person","id":"owner-fixture","expected":` + peoplePrincipalJSON + `}`,
		`{"kind":"register_agent","agent":{"id":"crew-fixture","principal_id":"owner-fixture","kind":"crew","address":"people/owner-fixture/crew"},"expected":` + peoplePrincipalJSON + `}`,
		`{"kind":"retire_agent","id":"concierge-fixture","expected":` + peopleAgentJSON + `}`,
		`{"kind":"revoke_credential","id":"device-fixture","expected":` + peopleDeviceJSON + `}`,
	} {
		var kind struct{ Kind string }
		if err := json.Unmarshal([]byte(action), &kind); err != nil {
			t.Fatal(err)
		}
		t.Run(kind.Kind, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(_ context.Context, request PeopleRequest) PeopleResponse {
				calls++
				if request.Version != 1 || request.Operation != "apply" || request.Actor != peopleTestActor() ||
					string(request.Action) != action {
					t.Error("HTTP changed expected metadata or accepted posted actor authority")
				}
				return PeopleResponse{OK: true, Outcome: "confirmed"}
			}), nil)
			r := peopleTestHTTPRequest(http.MethodPost, "/people/actions", action, peopleTestIdentity())
			r.Header.Set("X-Codexpro-Principal", "someone-else")
			w := servePeopleTestHTTP(t, mux, r, http.StatusOK)
			if calls != 1 || decode(t, w)["outcome"] != "confirmed" {
				t.Fatal("approved action did not return its single confirmed owner result")
			}
		})
	}
}

func TestPeopleHTTPStrictInputRefusesBeforeOwner(t *testing.T) {
	change := `{"kind":"change_role","id":"owner-fixture","role":"tester","expected":` + peoplePrincipalJSON + `}`
	register := `{"kind":"register_agent","agent":` + strings.TrimSuffix(peopleAgentJSON, `,"disabled":false}`) + `},"expected":` + peoplePrincipalJSON + `}`
	for _, tc := range []struct {
		name, body, media string
		length            int64
		want              int
	}{
		{"malformed", "{", "application/json", 0, 400},
		{"empty", "", "application/json", 0, 400},
		{"trailing object", peopleAddJSON + "{}", "application/json", 0, 400},
		{"top null", "null", "application/json", 0, 400},
		{"top array", "[]", "application/json", 0, 400},
		{"duplicate", strings.Replace(peopleAddJSON, `"kind":`, `"kind":"add_person","kind":`, 1), "application/json", 0, 400},
		{"escaped duplicate", strings.Replace(peopleAddJSON, `"kind":`, `"ki\u006ed":"add_person","kind":`, 1), "application/json", 0, 400},
		{"null role", strings.Replace(peopleAddJSON, `"tester"`, "null", 1), "application/json", 0, 400},
		{"wrong field case", strings.Replace(peopleAddJSON, `"kind"`, `"Kind"`, 1), "application/json", 0, 400},
		{"unknown field", strings.TrimSuffix(peopleAddJSON, "}") + `,"other":true}`, "application/json", 0, 400},
		{"posted actor", strings.TrimSuffix(peopleAddJSON, "}") + `,"actor":{"principal_id":"forged"}}`, "application/json", 0, 400},
		{"posted path", strings.TrimSuffix(peopleAddJSON, "}") + `,"file":"/private/fixture-policy"}`, "application/json", 0, 400},
		{"posted argv", strings.TrimSuffix(peopleAddJSON, "}") + `,"args":["issue"]}`, "application/json", 0, 400},
		{"issuance", `{"kind":"issue_credential","id":"new-device"}`, "application/json", 0, 400},
		{"grants", `{"kind":"set_grants","id":"crew-fixture"}`, "application/json", 0, 400},
		{"null expected", strings.Replace(change, peoplePrincipalJSON, "null", 1), "application/json", 0, 400},
		{"null expected field", strings.Replace(change, `"disabled":false`, `"disabled":null`, 1), "application/json", 0, 400},
		{"null project member", strings.Replace(change, `"projects":[]`, `"projects":[null]`, 1), "application/json", 0, 400},
		{"duplicate expected", strings.Replace(change, `"type":"person"`, `"type":"person","type":"system"`, 1), "application/json", 0, 400},
		{"private expected field", strings.Replace(change, `"type":"person"`, `"type":"person","sha256":"fixture-digest"`, 1), "application/json", 0, 400},
		{"duplicate agent", strings.Replace(register, `"kind":"concierge"`, `"kind":"concierge","kind":"crew"`, 1), "application/json", 0, 400},
		{"private agent field", strings.Replace(register, `"project_id":""`, `"project_id":"","native_id":"private-native"`, 1), "application/json", 0, 400},
		{"wrong MIME", peopleAddJSON, "text/plain", 0, 400},
		{"missing MIME", peopleAddJSON, "", 0, 400},
		{"declared overflow", peopleAddJSON, "application/json", (32 << 10) + 1, 413},
		{"streamed overflow", strings.Repeat(" ", 32<<10) + peopleAddJSON, "application/json", -1, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(context.Context, PeopleRequest) PeopleResponse {
				calls++
				return PeopleResponse{OK: true, Outcome: "confirmed"}
			}), nil)
			r := peopleTestHTTPRequest(http.MethodPost, "/people/actions", tc.body, peopleTestIdentity())
			r.Header.Set("Content-Type", tc.media)
			if tc.length != 0 {
				r.ContentLength = tc.length
			}
			w := servePeopleTestHTTP(t, mux, r, tc.want)
			if calls != 0 {
				t.Fatal("refused HTTP input reached the owner")
			}
			wantCode := "invalid_request"
			if tc.want == 413 {
				wantCode = "payload_too_large"
			}
			if errorCode(t, w) != wantCode {
				t.Fatal("invalid request returned a different error class")
			}
		})
	}
}

func TestPeopleHTTPAcceptsExactlyTheBodyLimit(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	RegisterPeopleRoutes(mux, peopleTestOwner(func(_ context.Context, request PeopleRequest) PeopleResponse {
		calls++
		if len(request.Action) != 32<<10 {
			t.Error("bounded body was changed before owner dispatch")
		}
		return PeopleResponse{OK: true, Outcome: "confirmed"}
	}), nil)
	body := strings.Repeat(" ", (32<<10)-len(peopleAddJSON)) + peopleAddJSON
	servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodPost, "/people/actions", body, peopleTestIdentity()), 200)
	if calls != 1 {
		t.Fatal("exactly bounded action was not dispatched once")
	}
}

func TestPeopleHTTPRetainsExistingOriginSemantics(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"", 200}, {"http://people.test:7210", 200}, {"https://people.test:7210", 200},
		{"https://foreign.test", 403}, {"http://people.test:7211", 403}, {"null", 403},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			calls := 0
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(context.Context, PeopleRequest) PeopleResponse {
				calls++
				return PeopleResponse{OK: true, Outcome: "confirmed"}
			}), nil)
			r := peopleTestHTTPRequest(http.MethodPost, "/people/actions", peopleAddJSON, peopleTestIdentity())
			r.Header.Set("Origin", tc.origin)
			w := servePeopleTestHTTP(t, mux, r, tc.want)
			if tc.want == 403 {
				if calls != 0 || errorCode(t, w) != "origin_denied" {
					t.Fatal("foreign origin reached the owner or lost its refusal class")
				}
			} else if calls != 1 {
				t.Fatal("existing same-host or absent-Origin request was not dispatched once")
			}
		})
	}
}

func TestPeopleHTTPRejectsMissingIdentityBeforeOwner(t *testing.T) {
	for _, field := range []string{"source", "type", "role", "kind", "principal", "agent", "credential"} {
		t.Run(field, func(t *testing.T) {
			id := peopleTestIdentity()
			switch field {
			case "source":
				id.Source = "legacy"
			case "type":
				id.PrincipalType = "system"
			case "role":
				id.Role = "tester"
			case "kind":
				id.AgentKind = "crew"
			case "principal":
				id.PrincipalID = ""
			case "agent":
				id.AgentID = ""
			case "credential":
				id.CredentialID = ""
			}
			calls := 0
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(context.Context, PeopleRequest) PeopleResponse {
				calls++
				return PeopleResponse{OK: true, Outcome: "confirmed"}
			}), nil)
			servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodGet, "/people/data", "", id), 403)
			servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodPost, "/people/actions", peopleAddJSON, id), 403)
			if calls != 0 {
				t.Fatal("unverified actor reached the policy owner")
			}
		})
	}
}

func TestPeopleHTTPErrorsAreFixedAndKeepUncertainty(t *testing.T) {
	for _, tc := range []struct {
		code string
		want int
	}{
		{"invalid_request", 400}, {"forbidden", 403}, {"not_found", 404},
		{"conflict", 409}, {"unavailable", 503}, {"uncertain", 503}, {"private_owner_error", 503},
	} {
		t.Run(tc.code, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(context.Context, PeopleRequest) PeopleResponse {
				return PeopleResponse{Error: &PeopleError{Code: tc.code, Outcome: "untrusted",
					Message: "/private/fixture-policy: fixture-digest; raw subprocess diagnostic"}}
			}), nil)
			w := servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodPost, "/people/actions", peopleAddJSON, peopleTestIdentity()), tc.want)
			var response PeopleResponse
			if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.OK || response.Error == nil ||
				response.Roster != nil || response.Self != nil {
				t.Fatal("error was not projected to its fixed public shape")
			}
			code, outcome := tc.code, "refused"
			if code == "private_owner_error" {
				code = "unavailable"
			}
			if code == "uncertain" {
				outcome = "uncertain"
				if !strings.Contains(response.Error.Message, "could not be confirmed") {
					t.Fatal("uncertain owner result became a refusal or success claim")
				}
			}
			if response.Error.Code != code || response.Error.Outcome != outcome ||
				strings.Contains(w.Body.String(), "/private/") || strings.Contains(w.Body.String(), "fixture-digest") ||
				strings.Contains(w.Body.String(), "subprocess") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("owner error leaked details or changed its public outcome")
			}
		})
	}
}

func TestPeopleHTTPKeepsReceiptSeparateFromLaterSelfRevocationRefusal(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name, status := "confirmed", http.StatusOK
		if !confirmed {
			name, status = "uncertain", http.StatusServiceUnavailable
		}
		t.Run(name, func(t *testing.T) {
			var operations []string
			mux := http.NewServeMux()
			RegisterPeopleRoutes(mux, peopleTestOwner(func(_ context.Context, request PeopleRequest) PeopleResponse {
				operations = append(operations, request.Operation)
				if request.Operation == "read" {
					return PeopleResponse{Error: &PeopleError{Code: "forbidden", Outcome: "refused"}}
				}
				if confirmed {
					return PeopleResponse{OK: true, Outcome: "confirmed"}
				}
				return PeopleResponse{Error: &PeopleError{Code: "uncertain", Outcome: "uncertain"}}
			}), nil)
			body := `{"kind":"revoke_credential","id":"device-fixture","expected":` + peopleDeviceJSON + `}`
			post := servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodPost, "/people/actions", body, peopleTestIdentity()), status)
			read := servePeopleTestHTTP(t, mux, peopleTestHTTPRequest(http.MethodGet, "/people/data", "", peopleTestIdentity()), 403)
			if errorCode(t, read) != "forbidden" || decode(t, read)["ok"] != false ||
				!reflect.DeepEqual(operations, []string{"apply", "read"}) {
				t.Fatal("later authority refusal fabricated success or repeated the action")
			}
			if confirmed && decode(t, post)["outcome"] != "confirmed" {
				t.Fatal("confirmed owner receipt was lost")
			}
			if !confirmed && errorCode(t, post) != "uncertain" {
				t.Fatal("read refusal resolved an uncertain mutation without a receipt")
			}
		})
	}
}
