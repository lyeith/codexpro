//go:build linux

package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSDKAuthorizationRequiresHTTPAdmission(t *testing.T) {
	identity := wire.Identity{Source: "named", PrincipalType: "person", PrincipalID: "alpha", CredentialID: "device", AgentID: "crew", AgentKind: "crew", Role: "tester"}
	projects := []string{"one"}
	admitted := withRequestAuthorization(context.Background(), identity, projects)
	projects[0] = "mutated-after-admission"
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"headers-only", context.Background(), http.StatusUnauthorized},
		{"identity-only", wire.WithIdentity(context.Background(), identity), http.StatusUnauthorized},
		{"admitted", admitted, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler := sdkAuthenticatedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				decision, ok := authorizationFromExtra(&mcp.RequestExtra{TokenInfo: auth.TokenInfoFromContext(r.Context())})
				if !ok || decision.identity != identity || !slices.Equal(decision.projects, []string{"one"}) {
					t.Error("SDK metadata lost the immutable admission decision")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(tc.ctx)
			req.Header.Set("Authorization", "Bearer synthetic-adapter-test")
			req.Header.Set("X-Principal-ID", "alpha")
			req.Header.Set("X-Projects", "one")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.want || reached != (tc.want == http.StatusNoContent) {
				t.Fatalf("status/reached = %d/%v, want %d", recorder.Code, reached, tc.want)
			}
		})
	}
	for _, extra := range []*mcp.RequestExtra{
		nil,
		{},
		{TokenInfo: &auth.TokenInfo{}},
		{TokenInfo: &auth.TokenInfo{Extra: map[string]any{requestAuthorizationExtraKey: map[string]any{"identity": identity}}}},
		{TokenInfo: &auth.TokenInfo{Extra: map[string]any{requestAuthorizationExtraKey: (*requestAuthorization)(nil)}}},
	} {
		if _, ok := authorizationFromExtra(extra); ok {
			t.Fatal("missing or untyped SDK metadata supplied authorization")
		}
	}
}

func TestHTTPAdmissionUsesOnePolicySnapshot(t *testing.T) {
	path, policy, tokens := namedPolicy(t)
	policy.Principals[0].Projects = []string{"one"}
	savePolicy(t, path, policy)
	type observed struct {
		identity wire.Identity
		projects []string
	}
	observations := make(chan observed, 4)
	server, _, _, _ := newHTTPFixture(t, func(ctx context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		if wire.IdentityFrom(ctx) != scope.Identity {
			t.Error("dispatch context and scope use different identities")
		}
		observations <- observed{scope.Identity, wire.ProjectsFrom(ctx)}
		return wire.Success("read", "Read", nil, "ok"), nil
	})
	server.Config.Set("principalsFile", path)
	bootstrap, err := NewBootstrap(server.Config, http.NotFoundHandler(), server.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bootstrap.Close() })
	if err := bootstrap.Publish(server); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(bootstrap)
	t.Cleanup(remote.Close)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	const call = `{"jsonrpc":"2.0","id":"admitted","method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file"}}}`
	check := func(response *httptest.ResponseRecorder, role string, grants []string) {
		t.Helper()
		if response.Code != http.StatusOK || decodeRPC(t, response.Body.Bytes())["result"] == nil {
			t.Fatalf("admitted call failed: %d %s", response.Code, response.Body.String())
		}
		select {
		case got := <-observations:
			if got.identity.Role != role || !slices.Equal(got.projects, grants) {
				t.Fatalf("admission role/grants = %q/%v, want %q/%v", got.identity.Role, got.projects, role, grants)
			}
		default:
			t.Fatal("successful result did not come from the domain caller")
		}
	}
	response := postWithAdmissionPause(t, bootstrap, session, tokens[0], call, func() {
		policy.Principals[0].Role = "engineer"
		policy.Principals[0].Projects = []string{"two"}
		savePolicy(t, path, policy)
	})
	check(response, "tester", []string{"one"})

	response = postWithAdmissionPause(t, bootstrap, session, tokens[0], call, func() {
		policy.Credentials[0].Revoked = true
		savePolicy(t, path, policy)
	})
	check(response, "engineer", []string{"two"})
	status, _, _ := request(t, remote, http.MethodPost, "/mcp", call, session, tokens[0], nil)
	if status != http.StatusUnauthorized || len(observations) != 0 {
		t.Fatal("a subsequent POST reused the revoked admission")
	}
}

type admissionPausedBody struct {
	ctx     context.Context
	reader  io.Reader
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
	err     error
}

func (r *admissionPausedBody) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		select {
		case <-r.release:
		case <-r.ctx.Done():
			r.err = r.ctx.Err()
		}
	})
	if r.err != nil {
		return 0, r.err
	}
	return r.reader.Read(p)
}

func (*admissionPausedBody) Close() error { return nil }

// The first body read occurs after the outer HTTP policy admission. Channels
// establish that boundary without relying on timing or polling.
func postWithAdmissionPause(t *testing.T, handler http.Handler, session, token, body string, afterAdmission func()) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", nil).WithContext(ctx)
	req.Body = &admissionPausedBody{ctx: ctx, reader: strings.NewReader(body), entered: entered, release: release}
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, req)
		close(done)
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("request ended before the admission boundary: %d", response.Code)
	case <-ctx.Done():
		t.Fatal("request did not reach the admission boundary")
	}
	afterAdmission()
	unblock()
	select {
	case <-done:
		return response
	case <-ctx.Done():
		t.Fatal("admitted request did not finish")
		return nil
	}
}
