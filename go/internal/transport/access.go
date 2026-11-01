package transport

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// MCP requests are logged on receipt and on completion (journal via stderr):
// whether a client request reached this process, and where it went, could
// otherwise not be established. No credential, argument or result content is
// logged: only the JSON-RPC id and method, the tool name, a session id prefix,
// status, duration and response bytes.

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, e := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, e
}
func (r *statusRecorder) Flush()                      { _ = http.NewResponseController(r.ResponseWriter).Flush() }
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type mcpRequestLog struct {
	enabled          bool
	started          time.Time
	httpMethod       string
	session, rpcID   string
	rpcMethod, tool  string
	bodyBytes        int
	receivedRecorded bool
	recorder         *statusRecorder
}

func sessionPrefix(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "-"
	}
	return id
}

func (l *mcpRequestLog) received() {
	if !l.enabled || l.receivedRecorded {
		return
	}
	l.receivedRecorded = true
	fmt.Fprintf(os.Stderr, "mcp recv %s rpc=%s session=%s method=%s tool=%s bytes=%d\n", l.httpMethod, l.rpcID, sessionPrefix(l.session), l.rpcMethod, l.tool, l.bodyBytes)
}

func (l *mcpRequestLog) done() {
	if !l.enabled {
		return
	}
	status := l.recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	fmt.Fprintf(os.Stderr, "mcp done %s rpc=%s session=%s method=%s tool=%s status=%d ms=%.1f out=%d\n", l.httpMethod, l.rpcID, sessionPrefix(l.session), l.rpcMethod, l.tool, status, float64(time.Since(l.started).Microseconds())/1000, l.recorder.bytes)
}
