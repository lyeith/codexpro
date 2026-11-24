package profiling

import (
	"io"
	"net/http"
	"runtime/trace"
	"testing"
	"time"
)

func TestCloseCancelsActiveTrace(t *testing.T) {
	server, err := Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := client.Get("http://" + server.Addr().String() + "/debug/pprof/trace?seconds=30")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for !trace.IsEnabled() {
		select {
		case <-finished:
			t.Fatal("trace request ended before it became active")
		case <-deadline.C:
			t.Fatal("trace request did not start")
		case <-ticker.C:
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	for trace.IsEnabled() {
		select {
		case <-deadline.C:
			t.Fatal("closing the server left runtime tracing active")
		case <-ticker.C:
		}
	}
	select {
	case <-finished:
	case <-deadline.C:
		t.Fatal("closing the server left the trace client blocked")
	}
}
