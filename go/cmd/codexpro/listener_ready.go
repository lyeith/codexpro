package main

import (
	"net"
	"sync"
)

// readyListener marks entry into the HTTP server's accept loop. Background
// activation cannot race ahead of a bound, already-serving listener.
type readyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *readyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}
