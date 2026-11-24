package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	_ "net/http/pprof"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func bootstrapFixture(t *testing.T) (*Bootstrap, string) {
	t.Helper()
	cfg := config.New(t.TempDir())
	credential := "owned-startup-test-credential"
	if err := os.WriteFile(cfg.TokenFile, []byte(credential+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	health := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true,"name":"CodexPro","unchanged":null}`))
	})
	b, err := NewBootstrap(cfg, health)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, credential
}

func bootstrapReadyFixture(t *testing.T, bootstrap *Bootstrap) *Server {
	t.Helper()
	registry, err := contracts.New()
	if err != nil {
		t.Fatal(err)
	}
	caller := testCaller(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return wire.Success("fixture", "Fixture", nil, "ready"), nil
	})
	server, err := New(bootstrap.auth.Config, registry, caller, "fixture-principal", nil, bootstrap.auth.policyReader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestBootstrapSharesAuthenticationBeforeAndAfterPublish(t *testing.T) {
	b, credential := bootstrapFixture(t)
	if err := b.Publish(nil); err == nil {
		t.Fatal("nil ready server was published")
	}
	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	if response := request("/healthz", ""); response.Code != 401 || response.Body.String() != "Unauthorized" {
		t.Fatalf("startup health bypassed ordinary authentication: %d %s", response.Code, response.Body.String())
	}
	health := request("/healthz", credential)
	if health.Code != 200 || health.Body.String() != `{"ok":true,"name":"CodexPro","unchanged":null}` || health.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("startup health changed body/headers: %+v", health)
	}
	if response := request("/mcp", credential); response.Code != 503 || response.Header().Get("Retry-After") != "1" || !strings.Contains(response.Body.String(), "Server is starting; retry shortly") {
		t.Fatalf("pending dispatcher did not return explicit bounded unavailability: %+v", response)
	}
	ready := bootstrapReadyFixture(t, b)
	ready.Mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ready")) })
	if err := b.Publish(ready); err != nil {
		t.Fatal(err)
	}
	if response := request("/ready", credential); response.Code != 200 || response.Body.String() != "ready" {
		t.Fatal("ready handler was not atomically published")
	}
	if response := request("/ready", "wrong"); response.Code != 401 {
		t.Fatal("ready handoff bypassed authentication")
	}
	if err := b.Publish(ready); err == nil {
		t.Fatal("second publication was accepted")
	}
}

func TestBootstrapStopRefusesLatePublication(t *testing.T) {
	b, _ := bootstrapFixture(t)
	ready := bootstrapReadyFixture(t, b)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ready); err == nil {
		t.Fatal("stopped startup published a late dispatcher")
	}
}

func TestBootstrapConcurrentStopAndPublishHasOneLinearizedOutcome(t *testing.T) {
	b, _ := bootstrapFixture(t)
	ready := bootstrapReadyFixture(t, b)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(stop bool) {
			defer group.Done()
			if stop {
				_ = b.Close()
			} else {
				_ = b.Publish(ready)
			}
		}(i%2 == 0)
	}
	group.Wait()
	if err := b.Publish(ready); err == nil {
		t.Fatal("stopped startup accepted another dispatcher")
	}
}

func TestReadyHTTPDoesNotExposeProfiling(t *testing.T) {
	bootstrap, credential := bootstrapFixture(t)
	if err := bootstrap.Publish(bootstrapReadyFixture(t, bootstrap)); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(bootstrap)
	t.Cleanup(remote.Close)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine"} {
		status, _, _ := request(t, remote, http.MethodGet, path, "", "", credential, nil)
		if status != http.StatusNotFound {
			t.Errorf("authenticated normal HTTP %s returned %d, want 404", path, status)
		}
	}
}
