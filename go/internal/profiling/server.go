// Package profiling serves explicitly enabled runtime diagnostics on a separate
// literal loopback listener. It never adds routes to the MCP HTTP server.
package profiling

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"time"
)

// ValidateListen accepts an empty address (disabled), or a literal loopback IP
// and numeric port. Port zero selects an ephemeral local port for private tests.
func ValidateListen(address string) error {
	if address == "" {
		return nil
	}
	addr, err := netip.ParseAddrPort(address)
	if err != nil || !addr.Addr().Unmap().IsLoopback() {
		return errors.New("pprof-listen must be empty or a literal loopback IP:port")
	}
	return nil
}

// Server owns the optional profiling listener and its serving goroutine.
type Server struct {
	http     *http.Server
	listener net.Listener
	done     chan struct{}
	err      error // Written before done closes; read only after done.
}

// Start leaves profiling completely disabled when address is empty. A nil
// Server supports Close and Done, so its owner needs no separate shutdown path.
func Start(address string) (*Server, error) {
	if err := ValidateListen(address); err != nil {
		return nil, err
	}
	if address == "" {
		return nil, nil
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("pprof listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	s := &Server{
		http: &http.Server{
			Handler: mux, ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 * 1024,
		},
		listener: listener,
		done:     make(chan struct{}),
	}
	go func() {
		if err := s.http.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			s.err = err
		}
		close(s.done)
	}()
	return s, nil
}

func (s *Server) Addr() net.Addr {
	if s == nil {
		return nil
	}
	return s.listener.Addr()
}

// Done is nil when profiling is disabled.
func (s *Server) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Err returns the serving result after Done closes.
func (s *Server) Err() error {
	if s == nil {
		return nil
	}
	<-s.done
	return s.err
}

// Close closes active diagnostic requests, releases the listener and joins the
// serving goroutine. It does not delay or participate in MCP's tool-call drain.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	err := s.http.Close()
	<-s.done
	return errors.Join(err, s.err)
}
