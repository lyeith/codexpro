//go:build linux

package transport

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestRotationGrantsPreserveNamedProjectAuthority(t *testing.T) {
	path, p, tokens := namedPolicy(t)
	p.Principals[0].Projects = []string{"one"}
	p.Agents[0].ProjectID = "one"
	p.Principals = append(p.Principals, plansaccess.Principal{
		ID: "system", Type: "system", Projects: []string{"two"},
	})
	p.Agents = append(p.Agents, plansaccess.Agent{
		ID: "coord", PrincipalID: "system", Kind: "coordinator", Address: "two/coordinator",
	})
	savePolicy(t, path, p)

	type observedCall struct {
		identity wire.Identity
		projects []string
	}
	calls := make(chan observedCall, 2)
	s, remote, _, _ := newHTTPFixture(t, func(ctx context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		calls <- observedCall{scope.Identity, wire.ProjectsFrom(ctx)}
		return wire.Success("read", "read", nil, "ok"), nil
	})
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	session := initialize(t, remote, tokens[0], "2025-11-25")
	wantIdentity, wantProjects, admitted, wantOwnerPage, policyErr := s.authenticateHTTP(tokens[0])
	if policyErr != nil || !admitted || !slices.Equal(wantProjects, []string{"one"}) {
		t.Fatal("baseline named project scope was not admitted")
	}
	checkRead := func(t *testing.T, wantStatus int) {
		t.Helper()
		const call = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file"}}}`
		code, _, body := request(t, remote, http.MethodPost, "/mcp", call, session, tokens[0], nil)
		if code != wantStatus {
			t.Fatalf("read returned %d, want %d", code, wantStatus)
		}
		if code != http.StatusOK {
			if len(calls) != 0 {
				t.Fatal("refused credential reached the dispatcher")
			}
			return
		}
		if decodeRPC(t, body)["error"] != nil {
			t.Fatal("admitted read returned an RPC error")
		}
		select {
		case call := <-calls:
			if call.identity != wantIdentity || !call.identity.Restricted() || !slices.Equal(call.projects, wantProjects) {
				t.Fatalf("rotation grant changed dispatched identity or projects: %+v", call)
			}
		default:
			t.Fatal("admitted read did not reach the dispatcher")
		}
	}
	checkRead(t, http.StatusOK)

	grant := plansaccess.RotationGrant{
		Target: "coord", TargetPrincipal: "system", Mailbox: "two/coordinator",
	}
	p.Principals[0].RotationGrants = []plansaccess.RotationGrant{grant}
	// Use the writer's literal JSON shape so a missing reader schema field is
	// exercised through Load and the live HTTP authentication path.
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withGrant := bytes.Replace(original, []byte(`"id":"alpha"`), []byte(`"id":"alpha","rotation_grants":[{"target":"coord","target_principal":"system","mailbox":"two/coordinator"}]`), 1)
	if bytes.Equal(original, withGrant) {
		t.Fatal("raw writer-form fixture did not change")
	}
	if err := os.WriteFile(path, withGrant, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := plansaccess.Load(path)
	if err != nil {
		t.Fatal("valid writer-form grant made the shared policy unreadable:", err)
	}
	if actor, ok := loaded.Authenticate(tokens[0], "codexpro"); !ok || actor.PrincipalID != wantIdentity.PrincipalID || actor.AgentID != wantIdentity.AgentID || actor.Role != wantIdentity.Role {
		t.Fatal("writer-form grant changed ordinary named authentication")
	}
	checkRead(t, http.StatusOK) // Existing sessions survive a valid writer-form grant.
	initialize(t, remote, tokens[0], "2025-11-25")
	gotIdentity, gotProjects, admitted, gotOwnerPage, policyErr := s.authenticateHTTP(tokens[0])
	if policyErr != nil || !admitted || gotIdentity != wantIdentity || !slices.Equal(gotProjects, wantProjects) || gotOwnerPage != wantOwnerPage {
		t.Fatal("rotation grant widened ordinary project or owner-page authority")
	}
	for _, tool := range []string{"bash", "write", "open_workspace"} {
		call := fmt.Sprintf(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool)
		code, _, body := request(t, remote, http.MethodPost, "/mcp", call, session, tokens[0], nil)
		if code != http.StatusOK || decodeRPC(t, body)["error"] == nil || len(calls) != 0 {
			t.Fatalf("rotation grant admitted restricted tool %s", tool)
		}
	}

	p.Principals[0].RotationGrants[0].Mailbox = "two/stale-coordinator"
	savePolicy(t, path, p)
	if _, err := plansaccess.Load(path); err == nil {
		t.Fatal("strict loader admitted the stale rotation binding")
	}
	checkRead(t, http.StatusOK) // Serving retains the last validated project scope.
	p.Principals[0].RotationGrants[0] = grant
	savePolicy(t, path, p)
	checkRead(t, http.StatusOK)

	// A disabled rotation target does not invalidate unrelated authentication.
	p.Agents[len(p.Agents)-1].Disabled = true
	savePolicy(t, path, p)
	checkRead(t, http.StatusOK)
	for _, tc := range []struct {
		name string
		flag *bool
	}{
		{"revoked-credential", &p.Credentials[0].Revoked},
		{"disabled-person", &p.Principals[0].Disabled},
		{"disabled-agent", &p.Agents[0].Disabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*tc.flag = true
			savePolicy(t, path, p)
			checkRead(t, http.StatusUnauthorized)
			*tc.flag = false
			savePolicy(t, path, p)
			checkRead(t, http.StatusOK)
		})
	}
	p.Principals[0].RotationGrants = nil
	savePolicy(t, path, p)
	checkRead(t, http.StatusOK)
}
