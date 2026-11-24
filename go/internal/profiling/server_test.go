package profiling

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestProfilingDisabledByDefault(t *testing.T) {
	server, err := Start("")
	if err != nil || server != nil {
		t.Fatalf("disabled profiling created a server: %v, %v", server, err)
	}
	if server.Done() != nil || server.Addr() != nil || server.Err() != nil || server.Close() != nil {
		t.Fatal("disabled profiling did not remain inert")
	}
}

func TestProfilingRequiresLiteralLoopback(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1:0", "127.23.45.67:6060", "[::1]:6060", "[::ffff:127.0.0.1]:6060"} {
		if err := ValidateListen(address); err != nil {
			t.Errorf("valid address %q: %v", address, err)
		}
	}
	for _, address := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "192.0.2.1:6060", "localhost:6060", "127.0.0.1", "127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1:http", " 127.0.0.1:6060"} {
		t.Run(address, func(t *testing.T) {
			if server, err := Start(address); err == nil || server != nil {
				if server != nil {
					_ = server.Close()
				}
				t.Fatalf("unsafe or malformed address was admitted: %v", err)
			}
		})
	}
}

func TestProfilingUsesPrivateMuxAndClosesListener(t *testing.T) {
	// A route on the process default mux must never leak into this listener.
	previous := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()
	t.Cleanup(func() { http.DefaultServeMux = previous })
	http.DefaultServeMux.HandleFunc("/default-mux-only", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server, err := Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	address := server.Addr().String()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/debug/pprof/", http.StatusOK},
		{"/debug/pprof/goroutine?debug=1", http.StatusOK},
		{"/debug/pprof/heap", http.StatusOK},
		{"/mcp", http.StatusNotFound},
		{"/default-mux-only", http.StatusNotFound},
	} {
		response, err := client.Get("http://" + address + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != tc.code {
			t.Fatalf("%s: status=%d read=%v", tc.path, response.StatusCode, readErr)
		}
		if tc.code == http.StatusOK && len(body) == 0 {
			t.Fatalf("%s returned no profile content", tc.path)
		}
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/debug/pprof/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("non-GET profiling method admitted: %d", response.StatusCode)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("Close did not join the serving goroutine")
	}
	if err := server.Err(); err != nil {
		t.Fatal(err)
	}
	again, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("Close did not release its listener: %v", err)
	}
	_ = again.Close()
}

func TestProfilingBindFailureIsReported(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if server, err := Start(listener.Addr().String()); err == nil || server != nil {
		if server != nil {
			_ = server.Close()
		}
		t.Fatalf("occupied profiling address was ignored: %v", err)
	}
}
