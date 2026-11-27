package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPAdmissionPreservesLoadedLegacyCredentialGrammar(t *testing.T) {
	cfg := config.New(t.TempDir())
	cfg.Set("httpSessionMode", config.HTTPSessionStateful) // checks session issuance and reuse
	const credential = "legacy credential\twith internal whitespace"
	if err := os.WriteFile(cfg.TokenFile, []byte("  "+credential+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := NewBootstrap(cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bootstrap.Close() })
	registry, err := contracts.New()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(cfg, registry, testCaller(func(_ context.Context, scope wire.Scope, _ string, _ map[string]any) (wire.Result, error) {
		if scope.Identity != (wire.Identity{Source: "legacy"}) {
			t.Error("loaded legacy credential changed authenticated identity")
		}
		return wire.Success("list_projects", "Projects", nil, "ok"), nil
	}), "fixture-principal", nil, bootstrap.auth.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.credential != credential {
		t.Fatal("fixture did not load the existing legacy credential grammar")
	}
	headers := make(chan string, 2)
	server.SDK.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if extra := request.GetExtra(); extra != nil {
					headers <- extra.Header.Get("Authorization")
				} else {
					t.Error("SDK request lost HTTP metadata")
				}
			}
			return next(ctx, method, request)
		}
	})
	if err := bootstrap.Publish(server); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(bootstrap)
	t.Cleanup(remote.Close)
	const init = `{"jsonrpc":"2.0","id":"initialize","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-grammar","version":"1"}}}`
	const initialized = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	const call = `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"list_projects","arguments":{}}}`
	var lastSession string
	query := url.Values{"codexpro_token": []string{credential}}
	for _, tc := range []struct {
		name             string
		path             string
		headerCredential string
	}{
		{"header", "/mcp", credential},
		{"query", "/mcp?" + query.Encode(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, responseHeaders, data := request(t, remote, http.MethodPost, tc.path, init, "", tc.headerCredential, nil)
			session := responseHeaders.Get("Mcp-Session-Id")
			if status != http.StatusOK || !sessionPattern.MatchString(session) {
				t.Fatalf("admitted legacy initialize failed: status=%d", status)
			}
			lastSession = session
			status, _, _ = request(t, remote, http.MethodPost, tc.path, initialized, session, tc.headerCredential, nil)
			if status != http.StatusAccepted {
				t.Fatalf("admitted legacy notification failed: status=%d", status)
			}
			status, _, data = request(t, remote, http.MethodPost, tc.path, call, session, tc.headerCredential, nil)
			reply := decodeRPC(t, data)
			if status != http.StatusOK || reply["result"] == nil || reply["error"] != nil {
				t.Fatalf("admitted legacy tool call failed: status=%d", status)
			}
			select {
			case got := <-headers:
				if got != "Bearer "+credential {
					t.Fatal("SDK or logical mail would receive the adapter marker instead of the original canonical credential")
				}
			default:
				t.Fatal("tool call did not carry current HTTP metadata")
			}
		})
	}
	status, _, _ := request(t, remote, http.MethodPost, "/mcp", call, lastSession, "codexpro-admitted-request", nil)
	if status != http.StatusUnauthorized || len(headers) != 0 {
		t.Fatal("the SDK lexical marker substituted for HTTP admission")
	}
}
