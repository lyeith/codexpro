//go:build linux

package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/plansaccess"
)

func TestCollectorAuthConfinedAtBootstrapAndReady(t *testing.T) {
	s, _, legacy, _ := newHTTPFixture(t, nil)
	path, p, tokens := namedPolicy(t)
	p.Agents[1].Kind, p.Agents[1].Address = "concierge", "people/beta/concierge"
	p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
	savePolicy(t, path, p)
	s.Config.Set("principalsFile", path)
	s.Config.Set("legacyBridge", true)
	s.credential = tokens[2] // Prove a recognized denial cannot reuse this legacy value.
	rendered := 0
	s.Mux.HandleFunc(statusCollectorPath, func(w http.ResponseWriter, r *http.Request) {
		rendered++
		w.WriteHeader(http.StatusOK)
		if r.Method != "HEAD" {
			_, _ = w.Write([]byte("projection"))
		}
	})
	bootstrap, err := NewBootstrap(s.Config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close()
	bootstrap.auth.credential = tokens[2]
	serial := 0
	call := func(h http.Handler, method, target, token, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, reader)
		serial++
		req.RemoteAddr = fmt.Sprintf("127.0.0.%d:1234", serial)
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if mutate != nil {
			mutate(req)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	checkPeers := func(handler http.Handler, want int) {
		t.Helper()
		for _, peer := range []string{"127.0.0.1:1234", "127.42.1.9:1234", "[::1]:1234", "[::ffff:127.0.0.1]:1234"} {
			got := call(handler, "GET", statusCollectorPath, tokens[2], "", func(r *http.Request) {
				r.RemoteAddr = peer
				r.Header.Set("X-Forwarded-For", "192.0.2.1")
				r.Header.Set("Forwarded", "for=192.0.2.1")
			})
			if got.Code != want {
				t.Fatalf("actual loopback peer %q=%d, want%d", peer, got.Code, want)
			}
		}
		before := rendered
		for _, peer := range []string{"", "127.0.0.1", "localhost:1234", "bad-peer", "127.0.0.1:bad",
			"192.0.2.1:1234", "[2001:db8::1]:1234", "[::ffff:192.0.2.1]:1234"} {
			got := call(handler, "GET", statusCollectorPath, tokens[2], "", func(r *http.Request) {
				r.RemoteAddr = peer
				r.Header.Set("X-Forwarded-For", "127.0.0.1")
				r.Header.Set("X-Real-IP", "127.0.0.1")
				r.Header.Set("Forwarded", "for=127.0.0.1")
			})
			if got.Code != http.StatusForbidden {
				t.Fatalf("nonloopback or malformed peer %q=%d, want403", peer, got.Code)
			}
		}
		if rendered != before {
			t.Fatal("refused socket peer reached collector projection")
		}
	}
	checkPeers(bootstrap, http.StatusServiceUnavailable)
	if got := call(bootstrap, "GET", statusCollectorPath, tokens[2], "", nil); got.Code != 503 {
		t.Fatalf("startup collector=%d, want503", got.Code)
	}
	if got := call(bootstrap, "GET", statusCollectorPath, legacy, "", nil); got.Code != 401 {
		t.Fatalf("legacy startup collector=%d", got.Code)
	}
	if err := bootstrap.Publish(s); err != nil {
		t.Fatal(err)
	}
	for name, handler := range map[string]http.Handler{"ready": s, "bootstrap-ready": bootstrap} {
		t.Run(name, func(t *testing.T) {
			checkPeers(handler, http.StatusOK)
			for _, method := range []string{"GET", "HEAD"} {
				rec := call(handler, method, statusCollectorPath, tokens[2], "", nil)
				if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("valid collector %s=%d", method, rec.Code)
				}
				if method == "HEAD" && rec.Body.Len() != 0 {
					t.Fatal("HEAD emitted data")
				}
			}
			for _, target := range []string{"/favicon.ico", "/favicon.ico?", "/favicon.ico?v=fixture"} {
				if rec := call(handler, "GET", target, "", "", nil); rec.Code != 200 {
					t.Fatal("ordinary anonymous favicon behavior changed")
				}
			}
			if rec := call(handler, "GET", "/favicon.ico", tokens[0], "", nil); rec.Code != 200 {
				t.Fatal("ordinary named credential lost favicon")
			}
			for _, query := range []string{"?token=", "?codexpro_token="} {
				if rec := call(handler, "GET", "/favicon.ico"+query+tokens[2], "", "", nil); rec.Code != 401 {
					t.Fatal("collector URL credential escaped through favicon")
				}
			}
			before := rendered
			for _, target := range []string{"/mcp", "/status", "/activity.json", "/activity", "/healthz", "/", "/admin", "/favicon.ico", "/status/collector/", "/status//collector"} {
				if rec := call(handler, "GET", target, tokens[2], "", nil); rec.Code != 401 {
					t.Fatalf("collector escaped to %s: %d", target, rec.Code)
				}
			}
			for _, target := range []string{"/%73tatus/collector", "/status/%63ollector", "/status/collector?", "/status/collector?limit=1", "/status/collector?token=fixture"} {
				if rec := call(handler, "GET", target, tokens[2], "", nil); rec.Code != 403 {
					t.Fatalf("noncanonical collector request %s=%d", target, rec.Code)
				}
			}
			for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
				if rec := call(handler, method, statusCollectorPath, tokens[2], "", nil); rec.Code != 403 {
					t.Fatalf("method %s escaped: %d", method, rec.Code)
				}
			}
			if rec := call(handler, "GET", statusCollectorPath, tokens[2], "body", nil); rec.Code != 403 {
				t.Fatal("GET body admitted")
			}
			for _, mutate := range []func(*http.Request){
				func(r *http.Request) { r.Header.Add("Authorization", "Bearer duplicate") },
				func(r *http.Request) { r.Header.Set("Cookie", "token=fixture") },
			} {
				if rec := call(handler, "GET", statusCollectorPath, tokens[2], "", mutate); rec.Code != 403 {
					t.Fatal("ambiguous credential surface admitted")
				}
			}
			if rendered != before {
				t.Fatal("denied request reached projection")
			}
			for _, token := range []string{legacy, tokens[0], "unknown"} {
				if rec := call(handler, "GET", statusCollectorPath, token, "", nil); rec.Code != 401 {
					t.Fatal("ordinary credential gained collector")
				}
			}
			p.Credentials[2].Audience = "both"
			savePolicy(t, path, p)
			if rec := call(handler, "GET", statusCollectorPath, tokens[2], "", nil); rec.Code != 401 {
				t.Fatal("both gained collector")
			}
			p.Credentials[2].Audience = plansaccess.StatusCollectorAudience
			for _, change := range []func(){
				func() { p.Credentials[2].Revoked = true },
				func() { p.Principals[1].Disabled = true },
				func() { p.Agents[1].Disabled = true },
				func() { p.Principals[1].Role = "engineer" },
				func() { p.Agents[1].Kind = "crew" },
			} {
				change()
				savePolicy(t, path, p)
				if rec := call(handler, "GET", statusCollectorPath, tokens[2], "", nil); rec.Code != 401 {
					t.Fatal("fresh collector reduction was ignored")
				}
				if rec := call(handler, "POST", "/mcp", tokens[2], "", nil); rec.Code != 401 {
					t.Fatal("denied collector fell into legacy MCP")
				}
				p.Credentials[2].Revoked, p.Principals[1].Disabled, p.Agents[1].Disabled = false, false, false
				p.Principals[1].Role, p.Agents[1].Kind = "owner", "concierge"
			}
			if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			if rec := call(handler, "GET", statusCollectorPath, tokens[2], "", nil); rec.Code != 401 {
				t.Fatal("invalid policy fell back")
			}
			savePolicy(t, path, p)
		})
	}
}
