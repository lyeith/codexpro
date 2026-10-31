package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/lyeith/codexpro/go/internal/config"
)

type startupHandler struct{ handler http.Handler }

// Bootstrap uses the ordinary transport's authentication and response headers
// before storage recovery starts. Its health handler is the ordinary admin
// handler; the only transient change is an explicit503 on other authenticated
// paths until the complete dispatcher is atomically published.
type Bootstrap struct {
	auth   *Server
	next   atomic.Pointer[startupHandler]
	mu     sync.Mutex
	closed bool
}

func NewBootstrap(cfg *config.Config, health http.Handler) (*Bootstrap, error) {
	if health == nil {
		return nil, errors.New("startup health handler is required")
	}
	credential, err := cfg.Token()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	auth := &Server{Config: cfg, Mux: http.NewServeMux(), credential: credential,
		sessions: map[string]*sessionRecord{}, failures: map[string]failureWindow{}, ctx: ctx, cancel: cancel}
	b := &Bootstrap{auth: auth}
	auth.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if target := b.next.Load(); target != nil {
			target.handler.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/healthz" {
			health.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "1")
		if r.URL.Path == "/mcp" {
			rpcError(w, http.StatusServiceUnavailable, -32000, "Server is starting; retry shortly")
		} else {
			JSONError(w, http.StatusServiceUnavailable, "server_starting", "Server is starting; retry shortly")
		}
	})
	return b, nil
}

func (b *Bootstrap) ServeHTTP(w http.ResponseWriter, r *http.Request) { b.auth.ServeHTTP(w, r) }
func (b *Bootstrap) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.auth.Close()
}
func (b *Bootstrap) Publish(handler http.Handler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("HTTP startup was closed before publication")
	}
	if handler == nil {
		return errors.New("ready HTTP handler is required")
	}
	if !b.next.CompareAndSwap(nil, &startupHandler{handler: handler}) {
		return errors.New("HTTP dispatcher was already published")
	}
	return nil
}
