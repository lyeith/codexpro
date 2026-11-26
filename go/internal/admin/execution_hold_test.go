package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type executionHoldFixture struct {
	status  jobs.ExecutionHoldStatus
	calls   []string
	begin   func(string) (jobs.ExecutionHoldStatus, error)
	observe func(context.Context) (jobs.ExecutionHoldObservation, error)
}

func (f *executionHoldFixture) BeginExecutionHold(key string) (jobs.ExecutionHoldStatus, error) {
	f.calls = append(f.calls, "begin:"+key)
	if f.begin != nil {
		return f.begin(key)
	}
	return f.status, nil
}
func (f *executionHoldFixture) HoldExecutionDispatch(id string) (jobs.ExecutionHoldStatus, error) {
	f.calls = append(f.calls, "dispatch:"+id)
	return f.status, nil
}
func (f *executionHoldFixture) ResumeExecution(id string) (jobs.ExecutionHoldStatus, error) {
	f.calls = append(f.calls, "resume:"+id)
	return f.status, nil
}
func (f *executionHoldFixture) ExecutionStatus() jobs.ExecutionHoldStatus {
	f.calls = append(f.calls, "status")
	return f.status
}
func (f *executionHoldFixture) ObserveExecutionHold(ctx context.Context) (jobs.ExecutionHoldObservation, error) {
	f.calls = append(f.calls, "observe")
	if f.observe != nil {
		return f.observe(ctx)
	}
	return jobs.ExecutionHoldObservation{Status: f.status}, nil
}
func executionHoldRequest(handler http.Handler, method, body string, identity wire.Identity) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://fixture.test/admin/execution-hold", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(wire.WithIdentity(request.Context(), identity))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
func executionHoldStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) jobs.ExecutionHoldStatus {
	t.Helper()
	var response struct {
		OK               bool                     `json:"ok"`
		Status           jobs.ExecutionHoldStatus `json:"status"`
		Scope            string                   `json:"scope"`
		WholeHostBarrier bool                     `json:"whole_host_barrier"`
		RootBarrier      bool                     `json:"root_barrier"`
	}
	if recorder.Code != want || json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
		t.Fatalf("status=%d want=%d body=%s", recorder.Code, want, recorder.Body)
	}
	if response.OK != (want == http.StatusOK) || response.Scope != executionHoldLocalScope ||
		response.WholeHostBarrier || response.RootBarrier || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("incorrect execution control envelope: %s", recorder.Body)
	}
	return response.Status
}

func TestExecutionHoldHTTPPrivateJobsStatusAndIdempotency(t *testing.T) {
	service, _, _ := fixture(t)
	owner, err := jobs.NewPaused(service.Config, service.Workspaces)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	mux := http.NewServeMux()
	RegisterExecutionHoldRoutes(mux, owner, nil)
	id := wire.Identity{Source: "legacy"}
	before := owner.ExecutionStatus()
	for range 2 {
		status := executionHoldStatus(t, executionHoldRequest(mux, http.MethodGet, "", id), http.StatusOK)
		if !reflect.DeepEqual(status, before) {
			t.Fatalf("GET changed or projected a different status: %#v vs %#v", status, before)
		}
	}
	if !reflect.DeepEqual(owner.ExecutionStatus(), before) {
		t.Fatal("GET mutated execution state")
	}
	begin := `{"action":"begin","request_key":"private-http-maintenance"}`
	held := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, begin, id), http.StatusOK)
	if len(held.HoldID) != 32 || !held.AdmissionHeld || !held.DispatchHeld {
		t.Fatalf("empty private owner did not establish its hold: %#v", held)
	}
	repeated := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, begin, id), http.StatusOK)
	if held.HoldID != repeated.HoldID || held.RequestedAtMS != repeated.RequestedAtMS {
		t.Fatal("repeated request key created another hold")
	}
	conflict := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost,
		`{"action":"begin","request_key":"another-maintenance"}`, id), http.StatusConflict)
	if conflict.HoldID != held.HoldID {
		t.Fatal("conflicting begin replaced the original hold")
	}
	foreign := strings.Repeat("f", 32)
	if foreign == held.HoldID {
		foreign = strings.Repeat("e", 32)
	}
	for _, action := range []string{"dispatch", "resume", "observe"} {
		body, _ := json.Marshal(map[string]string{"action": action, "hold_id": foreign})
		status := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, string(body), id), http.StatusConflict)
		if status.HoldID != held.HoldID || !status.AdmissionHeld {
			t.Fatalf("foreign %s altered the original hold", action)
		}
	}
	body := func(action string) string {
		data, _ := json.Marshal(map[string]string{"action": action, "hold_id": held.HoldID})
		return string(data)
	}
	dispatched := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, body("dispatch"), id), http.StatusOK)
	if dispatched.HoldID != held.HoldID || !dispatched.DispatchHeld {
		t.Fatal("dispatch changed the original hold identity")
	}
	observed := executionHoldRequest(mux, http.MethodPost, body("observe"), id)
	executionHoldStatus(t, observed, http.StatusOK)
	var observation struct {
		Observation *jobs.ExecutionHoldObservation `json:"observation"`
	}
	if json.Unmarshal(observed.Body.Bytes(), &observation) != nil || observation.Observation == nil ||
		observation.Observation.Status.HoldID != held.HoldID {
		t.Fatalf("missing original-hold observation: %s", observed.Body)
	}
	resumed := executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, body("resume"), id), http.StatusOK)
	if resumed.HoldID != "" || resumed.AdmissionHeld || resumed.DispatchHeld {
		t.Fatalf("resume left the private hold active: %#v", resumed)
	}
	executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, body("resume"), id), http.StatusConflict)
}

func TestExecutionHoldHTTPStrictRequestAndAuthority(t *testing.T) {
	valid := `{"action":"begin","request_key":"fixture"}`
	for _, tc := range []struct {
		name, body, media string
		want              int
		chunked           bool
	}{
		{"empty", "", "application/json", 400, false},
		{"null", "null", "application/json", 400, false},
		{"array", "[]", "application/json", 400, false},
		{"unknown action", `{"action":"cancel","hold_id":"0123456789abcdef0123456789abcdef"}`, "application/json", 400, false},
		{"unknown key", `{"action":"begin","request_key":"fixture","actor":"owner"}`, "application/json", 400, false},
		{"duplicate action", `{"action":"begin","action":"resume","request_key":"fixture"}`, "application/json", 400, false},
		{"duplicate request key", `{"action":"begin","request_key":"first","request_key":"second"}`, "application/json", 400, false},
		{"missing key", `{"action":"begin"}`, "application/json", 400, false},
		{"wrong key", `{"action":"begin","hold_id":"0123456789abcdef0123456789abcdef"}`, "application/json", 400, false},
		{"both keys", `{"action":"begin","request_key":"fixture","hold_id":"0123456789abcdef0123456789abcdef"}`, "application/json", 400, false},
		{"null key", `{"action":"begin","request_key":null}`, "application/json", 400, false},
		{"number key", `{"action":"begin","request_key":42}`, "application/json", 400, false},
		{"blank key", `{"action":"begin","request_key":" \t "}`, "application/json", 400, false},
		{"NUL key", `{"action":"begin","request_key":"a\u0000b"}`, "application/json", 400, false},
		{"long key", `{"action":"begin","request_key":"` + strings.Repeat("x", 161) + `"}`, "application/json", 400, false},
		{"short hold ID", `{"action":"observe","hold_id":"abc"}`, "application/json", 400, false},
		{"nonhex hold ID", `{"action":"resume","hold_id":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`, "application/json", 400, false},
		{"trailing object", valid + " {}", "application/json", 400, false},
		{"invalid UTF8", strings.Replace(valid, "fixture", string([]byte{0xff}), 1), "application/json", 400, false},
		{"wrong content type", valid, "text/plain", 400, false},
		{"missing content type", valid, "", 400, false},
		{"declared large", strings.Repeat(" ", maxExecutionHoldBody+1), "application/json", 413, false},
		{"chunked large", valid + strings.Repeat(" ", maxExecutionHoldBody), "application/json", 413, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := &executionHoldFixture{}
			mux := http.NewServeMux()
			RegisterExecutionHoldRoutes(mux, owner, nil)
			request := httptest.NewRequest(http.MethodPost, "http://fixture.test/admin/execution-hold", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.media)
			request = request.WithContext(wire.WithIdentity(request.Context(), wire.Identity{Source: "legacy"}))
			if tc.chunked {
				request.ContentLength = -1
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != tc.want || len(owner.calls) != 0 {
				t.Fatalf("invalid request status=%d calls=%v body=%s", recorder.Code, owner.calls, recorder.Body)
			}
		})
	}
	for _, id := range []wire.Identity{
		{}, {Source: "named", PrincipalType: "system", PrincipalID: "fleet", AgentKind: "leaf"},
		{Source: "named", PrincipalType: "person", Role: "tester", AgentKind: "concierge"},
		{Source: "named", PrincipalType: "person", Role: "owner", AgentKind: "crew"},
		{Source: "named", PrincipalType: "person", Role: "owner", AgentKind: "concierge"},
	} {
		owner := &executionHoldFixture{}
		mux := http.NewServeMux()
		RegisterExecutionHoldRoutes(mux, owner, nil)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			recorder := executionHoldRequest(mux, method, valid, id)
			if recorder.Code != http.StatusForbidden || len(owner.calls) != 0 {
				t.Fatalf("unattributed/non-owner request reached control: %#v %d %v", id, recorder.Code, owner.calls)
			}
		}
	}
}

func TestExecutionHoldHTTPOriginMethodCancellationAndBounds(t *testing.T) {
	owner := &executionHoldFixture{}
	mux := http.NewServeMux()
	RegisterExecutionHoldRoutes(mux, owner, nil)
	for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		result := executionHoldRequest(mux, method, "", wire.Identity{Source: "legacy"})
		if result.Code != http.StatusMethodNotAllowed || result.Header().Get("Allow") != "GET, POST" {
			t.Fatalf("unexpected method %s: %d %s", method, result.Code, result.Body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request := httptest.NewRequest(method, "http://fixture.test/admin/execution-hold", strings.NewReader(`{"action":"begin","request_key":"fixture"}`))
		request.Header.Set("Origin", "https://other.test")
		request = request.WithContext(wire.WithIdentity(request.Context(), wire.Identity{Source: "legacy"}))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("foreign origin returned %d", recorder.Code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "http://fixture.test/admin/execution-hold", nil)
	request = request.WithContext(wire.WithIdentity(ctx, wire.Identity{Source: "legacy"}))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestTimeout || len(owner.calls) != 0 {
		t.Fatalf("cancelled or refused request reached owner: %d %v", recorder.Code, owner.calls)
	}
	key := strings.Repeat("界", 160)
	body, _ := json.Marshal(map[string]string{"action": "begin", "request_key": key})
	padded := string(body) + strings.Repeat(" ", maxExecutionHoldBody-len(body))
	result := executionHoldRequest(mux, http.MethodPost, padded, wire.Identity{Source: "legacy"})
	if result.Code != http.StatusOK || !reflect.DeepEqual(owner.calls, []string{"begin:" + key}) {
		t.Fatalf("valid exact-size Unicode request failed: %d %v", result.Code, owner.calls)
	}
}

func TestExecutionHoldHTTPPureStatusObservationAndRedaction(t *testing.T) {
	id := strings.Repeat("a", 32)
	owner := &executionHoldFixture{status: jobs.ExecutionHoldStatus{HoldID: id, AdmissionHeld: true}}
	mux := http.NewServeMux()
	RegisterExecutionHoldRoutes(mux, owner, nil)
	executionHoldStatus(t, executionHoldRequest(mux, http.MethodGet, "", wire.Identity{Source: "legacy"}), http.StatusOK)
	if !reflect.DeepEqual(owner.calls, []string{"status"}) {
		t.Fatalf("GET performed more than status: %v", owner.calls)
	}
	owner.calls = nil
	foreign := `{"action":"observe","hold_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`
	executionHoldStatus(t, executionHoldRequest(mux, http.MethodPost, foreign, wire.Identity{Source: "legacy"}), http.StatusConflict)
	if !reflect.DeepEqual(owner.calls, []string{"status"}) {
		t.Fatalf("foreign observation reached live owner inspection: %v", owner.calls)
	}
	body := `{"action":"observe","hold_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	owner.observe = func(ctx context.Context) (jobs.ExecutionHoldObservation, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > executionObserveTimeout || ctx.Err() != nil {
			t.Fatal("live observation lacks its bounded request context")
		}
		return jobs.ExecutionHoldObservation{Status: owner.status, Complete: false}, context.DeadlineExceeded
	}
	result := executionHoldRequest(mux, http.MethodPost, body, wire.Identity{Source: "legacy"})
	executionHoldStatus(t, result, http.StatusServiceUnavailable)
	if !strings.Contains(result.Body.String(), "execution_observation_incomplete") {
		t.Fatalf("observation uncertainty lost: %s", result.Body)
	}
	owner.observe = func(context.Context) (jobs.ExecutionHoldObservation, error) {
		owner.status.HoldID = strings.Repeat("b", 32)
		return jobs.ExecutionHoldObservation{Status: owner.status, Complete: true}, nil
	}
	result = executionHoldRequest(mux, http.MethodPost, body, wire.Identity{Source: "legacy"})
	executionHoldStatus(t, result, http.StatusConflict)
	if strings.Contains(result.Body.String(), `"observation"`) {
		t.Fatal("hold replacement returned another hold's observation")
	}

	redacted := false
	owner.status.PersistenceError = "/private/execution-fixture"
	owner.begin = func(string) (jobs.ExecutionHoldStatus, error) {
		return owner.status, errors.New("private failure detail must not be returned")
	}
	mux = http.NewServeMux()
	RegisterExecutionHoldRoutes(mux, owner, &HTTPOptions{RedactValue: func(value any, force bool) any {
		redacted = force
		data, _ := json.Marshal(value)
		var copy any
		_ = json.Unmarshal([]byte(strings.ReplaceAll(string(data), "/private/execution-fixture", "[private]")), &copy)
		return copy
	}})
	result = executionHoldRequest(mux, http.MethodPost, `{"action":"begin","request_key":"redaction"}`, wire.Identity{Source: "legacy"})
	executionHoldStatus(t, result, http.StatusServiceUnavailable)
	if !redacted || strings.Contains(result.Body.String(), "/private/execution-fixture") || strings.Contains(result.Body.String(), "private failure detail") {
		t.Fatalf("unredacted execution control failure: %s", result.Body)
	}
}
