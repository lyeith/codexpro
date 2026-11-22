package transport

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const (
	accessRequestsPerSecond = 20
	accessRequestBurst      = 40
	accessFieldBytes        = 128
)

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
func (r *statusRecorder) FlushError() error {
	return http.NewResponseController(r.ResponseWriter).Flush()
}
func (r *statusRecorder) Flush()                      { _ = r.FlushError() }
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type mcpRequestLog struct {
	started          time.Time
	httpMethod       string
	session, rpcID   string
	rpcMethod, tool  string
	bodyBytes        int
	receivedRecorded bool
}

func (s *Server) requestLog(method, session string) *mcpRequestLog {
	if !s.Config.Bool("accessLog", false) || !s.accessLogLimiter.Allow() {
		return nil
	}
	return &mcpRequestLog{
		started: time.Now(), httpMethod: method, session: session,
		rpcID: "-", rpcMethod: "-", tool: "-",
	}
}

func (l *mcpRequestLog) body(bytes int, rpcID string, body map[string]any) {
	l.bodyBytes = bytes
	l.rpcID = rpcID
	if method, ok := body["method"].(string); ok {
		l.rpcMethod = wire.Redact(method)
		if method == "tools/call" {
			params, _ := body["params"].(map[string]any)
			if name, ok := params["name"].(string); ok {
				l.tool = wire.Redact(name)
			}
		}
	}
}

func sessionPrefix(id string) string {
	if !sessionPattern.MatchString(id) {
		return "-"
	}
	return id[:8]
}

func accessField(value string) string {
	if len(value) > accessFieldBytes {
		return value[:accessFieldBytes] + "..."
	}
	return value
}

func (l *mcpRequestLog) received() {
	if l.receivedRecorded {
		return
	}
	l.receivedRecorded = true
	fmt.Fprintf(os.Stderr, "mcp recv %s rpc=%q session=%s method=%q tool=%q bytes=%d\n", l.httpMethod, accessField(l.rpcID), sessionPrefix(l.session), accessField(l.rpcMethod), accessField(l.tool), l.bodyBytes)
}

func (l *mcpRequestLog) done(recorder *statusRecorder) {
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	fmt.Fprintf(os.Stderr, "mcp done %s rpc=%q session=%s method=%q tool=%q status=%d ms=%.1f out=%d\n", l.httpMethod, accessField(l.rpcID), sessionPrefix(l.session), accessField(l.rpcMethod), accessField(l.tool), status, float64(time.Since(l.started).Microseconds())/1000, recorder.bytes)
}
