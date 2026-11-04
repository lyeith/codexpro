//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func namedPolicy(t *testing.T) (string, *plansaccess.Policy, []string) {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	p := &plansaccess.Policy{Version: 1}
	p.Principals = []plansaccess.Principal{{ID: "alpha", Type: "person", Role: "tester"}, {ID: "beta", Type: "person", Role: "owner"}}
	p.Agents = []plansaccess.Agent{
		{ID: "crew-alpha", PrincipalID: "alpha", Kind: "crew", Address: "people/alpha/crew"},
		{ID: "crew-beta", PrincipalID: "beta", Kind: "crew", Address: "people/beta/crew"}}
	var tokens []string
	for i, dev := range []string{"home", "office", "laptop"} {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		token := "pa1_" + base64.RawURLEncoding.EncodeToString(b)
		hash := sha256.Sum256([]byte(token))
		agent := p.Agents[i/2]
		p.Credentials = append(p.Credentials, plansaccess.Credential{ID: dev, Device: dev, PrincipalID: agent.PrincipalID, AgentID: agent.ID, Audience: "codexpro", SHA256: hex.EncodeToString(hash[:])})
		tokens = append(tokens, token)
	}
	path := filepath.Join(d, "principals.json")
	savePolicy(t, path, p)
	return path, p, tokens
}

func savePolicy(t *testing.T, path string, p *plansaccess.Policy) {
	t.Helper()
	b, err := json.Marshal(p)
	if err == nil {
		err = os.WriteFile(path+".next", b, 0600)
	}
	if err == nil {
		err = os.Rename(path+".next", path)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestNamedCredentialsBindSessionsAndReload(t *testing.T) {
	path, p, tokens := namedPolicy(t)
	scopes := make(chan wire.Scope, 8)
	s, remote, legacy, _ := newHTTPFixture(t, func(_ context.Context, scope wire.Scope, _ string, args map[string]any) (wire.Result, error) {
		scopes <- scope
		return wire.Success("read", "read", nil, "ok"), nil
	})
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	type response struct {
		index int
		id    string
		err   error
	}
	ids := make(chan response, len(tokens))
	for i, cred := range tokens {
		go func() {
			req, err := http.NewRequest("POST", remote.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
			if err != nil {
				ids <- response{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+cred)
			reply, err := remote.Client().Do(req)
			if err != nil {
				ids <- response{err: err}
				return
			}
			defer reply.Body.Close()
			ids <- response{index: i, id: reply.Header.Get("Mcp-Session-Id")}
		}()
	}
	sessions := make([]string, len(tokens))
	for range tokens {
		v := <-ids
		if v.err != nil || !sessionPattern.MatchString(v.id) {
			t.Fatal("initialize did not issue a session")
		}
		sessions[v.index] = v.id
		status, _, _ := request(t, remote, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, v.id, tokens[v.index], nil)
		if status != 202 {
			t.Fatalf("immediate initialized returned %d", status)
		}
	}
	const call = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read","arguments":{"workspace_id":"fixture","path":"file","agent":"coordinator"}}}`
	forged := map[string]string{"X-Codexpro-Principal": "beta", "X-Codexpro-Agent": "coordinator", "X-Codexpro-Go-Request-Id": "spoof"}
	for _, cred := range []string{tokens[1], tokens[2], legacy} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			if status, _, _ := request(t, remote, method, "/mcp", call, sessions[0], cred, forged); status != 403 {
				t.Fatalf("crossed %s returned %d", method, status)
			}
		}
	}
	p.Principals[0].Role = "engineer"
	savePolicy(t, path, p)
	status, _, body := request(t, remote, "POST", "/mcp?codexpro_token="+tokens[0], call, sessions[0], "", forged)
	if status != 200 || decodeRPC(t, body)["result"] == nil {
		t.Fatalf("own tool call failed: %d", status)
	}
	select {
	case scope := <-scopes:
		if scope.Identity.Source != "named" || scope.Identity.PrincipalID != "alpha" || scope.Identity.CredentialID != "home" || scope.Identity.AgentKind != "crew" || scope.Identity.AgentID != "crew-alpha" || scope.Identity.Role != "engineer" || scope.PrincipalID != "principal:alpha" || scope.RequestID != "5" {
			t.Fatalf("unexpected dispatch scope %+v", scope)
		}
	default:
		t.Fatal("request did not reach dispatcher")
	}
	p.Credentials[0].Revoked = true
	savePolicy(t, path, p)
	if status, _, _ := request(t, remote, "POST", "/mcp", call, sessions[0], tokens[0], nil); status != 401 {
		t.Fatalf("revoked device returned %d", status)
	}
	if status, _, _ := request(t, remote, "POST", "/mcp", call, sessions[1], tokens[1], nil); status != 200 {
		t.Fatalf("independent device returned %d", status)
	}
	legacySession := initialize(t, remote, legacy, "2025-11-25")
	s.Config.Set("legacyBridge", false)
	if status, _, _ := request(t, remote, "POST", "/mcp", call, legacySession, legacy, nil); status != 401 {
		t.Fatal("legacy bridge remained enabled")
	}
	s.Config.Set("legacyBridge", true)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cred := range []string{tokens[1], legacy} {
		if status, _, data := request(t, remote, "POST", "/mcp", call, sessions[1], cred, nil); status != 401 || bytes.Contains(data, []byte(cred)) {
			t.Fatal("malformed policy did not fail closed")
		}
	}
}

func TestAmbiguousCredentialsRefused(t *testing.T) {
	for _, test := range []struct {
		query   string
		headers []string
	}{
		{"token=query", []string{"Basic ignored"}},
		{"token=query", []string{"Bearer header"}},
		{"token=a&codexpro_token=b", nil}, {"token=a&token=b", nil},
		{"token=query", []string{"Bearer a", "Bearer b"}},
		{"", []string{"Bearer a", "Bearer b"}},
	} {
		r := httptest.NewRequest("GET", "/mcp?"+test.query, nil)
		for _, v := range test.headers {
			r.Header.Add("Authorization", v)
		}
		if credentialFrom(r) != "" {
			t.Error("ambiguous credential accepted")
		}
	}
}

func TestBootstrapAndReadyHTTPUseVerifiedIdentity(t *testing.T) {
	path, _, tokens := namedPolicy(t)
	b, old := bootstrapFixture(t)
	b.auth.Config.Set("principalsFile", path)
	request := func(credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/healthz", nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("X-Principal-ID", "forged")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, req)
		return w
	}
	if request(old).Code != 401 || request(tokens[0]).Code != 200 {
		t.Fatal("early health auth differs from policy")
	}
	if err := b.Publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := wire.IdentityFrom(r.Context())
		if id.PrincipalID != "alpha" || id.CredentialID != "home" {
			t.Error("ready handler received wrong identity")
		}
	})); err != nil {
		t.Fatal(err)
	}
	if request(tokens[0]).Code != 200 {
		t.Fatal("ready auth failed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if request(tokens[0]).Code != 401 {
		t.Fatal("publication bypassed unavailable policy")
	}
}
