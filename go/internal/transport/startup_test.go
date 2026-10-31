package transport

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
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

func TestBootstrapSharesAuthenticationBeforeAndAfterPublish(t *testing.T) {
	b, credential := bootstrapFixture(t)
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
	if err := b.Publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ready")) })); err != nil {
		t.Fatal(err)
	}
	if response := request("/mcp", credential); response.Code != 200 || response.Body.String() != "ready" {
		t.Fatal("ready handler was not atomically published")
	}
	if response := request("/mcp", "wrong"); response.Code != 401 {
		t.Fatal("ready handoff bypassed authentication")
	}
	if err := b.Publish(http.NotFoundHandler()); err == nil {
		t.Fatal("second publication was accepted")
	}
}

func TestBootstrapStopRefusesLatePublication(t *testing.T) {
	b, _ := bootstrapFixture(t)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(http.NotFoundHandler()); err == nil {
		t.Fatal("stopped startup published a late dispatcher")
	}
}

func TestBootstrapConcurrentStopAndPublishHasOneLinearizedOutcome(t *testing.T) {
	b, _ := bootstrapFixture(t)
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(stop bool) {
			defer group.Done()
			if stop {
				_ = b.Close()
			} else {
				_ = b.Publish(http.NotFoundHandler())
			}
		}(i%2 == 0)
	}
	group.Wait()
	if err := b.Publish(http.NotFoundHandler()); err == nil {
		t.Fatal("stopped startup accepted another dispatcher")
	}
}
