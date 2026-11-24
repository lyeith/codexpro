package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
)

// Bootstrap uses the ordinary transport's authentication and response headers
// before storage recovery starts. Its health handler is the ordinary admin
// handler; other authenticated paths return an explicit 503 until the complete
// server is atomically published. The ready server owns its own admission.
type Bootstrap struct {
	auth   *Server
	next   atomic.Pointer[Server]
	mu     sync.Mutex
	closed bool
}

func NewBootstrap(cfg *config.Config, health http.Handler, readers ...*plansaccess.Reader) (*Bootstrap, error) {
	if health == nil {
		return nil, errors.New("startup health handler is required")
	}
	credential, err := legacyCredential(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	auth := &Server{Config: cfg, Mux: http.NewServeMux(), credential: credential,
		sessions: map[string]*sessionRecord{}, failures: map[string]failureWindow{}, ctx: ctx, cancel: cancel, policyReader: configuredPolicyReader(readers)}
	b := &Bootstrap{auth: auth}
	auth.Mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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

func (b *Bootstrap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Pick one authentication owner at request entry. A request already on
	// the startup path may finish there while publication happens concurrently.
	if ready := b.next.Load(); ready != nil {
		ready.ServeHTTP(w, r)
		return
	}
	b.auth.ServeHTTP(w, r)
}
func (b *Bootstrap) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.auth.Close()
}
func (b *Bootstrap) Publish(server *Server) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("HTTP startup was closed before publication")
	}
	if server == nil {
		return errors.New("ready HTTP server is required")
	}
	if !b.next.CompareAndSwap(nil, server) {
		return errors.New("HTTP dispatcher was already published")
	}
	return nil
}
