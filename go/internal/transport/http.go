// Package transport adapts the official MCP Go SDK to the frozen HTTP contract.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/httpcompat"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type rawResult struct {
	mcp.ResultBase
	Value any
}

func (r *rawResult) MarshalJSON() ([]byte, error) { return json.Marshal(r.Value) }

type holderKey struct{}
type resultHolder struct{ result *wire.Result }
type sessionRecord struct {
	principal string
	lastSeen  time.Time
	active    int
	ctx       context.Context
	cancel    context.CancelFunc
}
type failureWindow struct {
	count int
	reset time.Time
}
type Server struct {
	Config                *config.Config
	Registry              *contracts.Registry
	Mux                   *http.ServeMux
	SDK                   *mcp.Server
	handler               *mcp.StreamableHTTPHandler
	caller                wire.Caller
	credential, principal string
	mu                    sync.Mutex
	sessions              map[string]*sessionRecord
	failures              map[string]failureWindow
	draining              bool
	closeSession          func(wire.Scope)
	cancel                context.CancelFunc
	ctx                   context.Context
}

var sessionPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func uuid() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func New(cfg *config.Config, registry *contracts.Registry, caller wire.Caller, principal string, onClose func(wire.Scope)) (*Server, error) {
	credential, e := cfg.Token()
	if e != nil {
		return nil, e
	}
	s := &Server{Config: cfg, Registry: registry, Mux: http.NewServeMux(), caller: caller, credential: credential, principal: principal, sessions: map[string]*sessionRecord{}, failures: map[string]failureWindow{}, closeSession: onClose}
	instructions, _ := registry.Initialization["instructions"].(string)
	s.SDK = mcp.NewServer(&mcp.Implementation{Name: "CodexPro", Version: "0.31.0"}, &mcp.ServerOptions{
		Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
		GetSessionID: func() string {
			id := uuid()
			s.mu.Lock()
			ctx, cancel := context.WithCancel(s.ctx)
			s.sessions[id] = &sessionRecord{principal: principal, lastSeen: time.Now(), active: 1, ctx: ctx, cancel: cancel}
			s.mu.Unlock()
			return id
		},
	})
	for _, descriptor := range registry.Tools {
		descriptor := descriptor
		var tool mcp.Tool
		if e = json.Unmarshal(descriptor.Raw, &tool); e != nil {
			return nil, e
		}
		s.SDK.AddTool(&tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			raw, e := json.Marshal(req.Params)
			if e != nil {
				return nil, e
			}
			var input struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if e = json.Unmarshal(raw, &input); e != nil {
				return nil, e
			}
			scope := wire.Scope{PrincipalID: s.principal, SessionID: req.Session.ID(), TransportID: req.Session.ID(), StartedAt: time.Now()}
			if req.Extra != nil {
				scope.RequestID = req.Extra.Header.Get("X-Codexpro-Go-Request-Id")
			}
			s.mu.Lock()
			lifetime := s.ctx
			if record := s.sessions[scope.SessionID]; record != nil {
				lifetime = record.ctx
			}
			s.mu.Unlock()
			callCtx, cancelCall := context.WithCancel(ctx)
			stopLifetime := context.AfterFunc(lifetime, cancelCall)
			defer func() { stopLifetime(); cancelCall() }()
			result, e := s.caller.Call(callCtx, scope, descriptor.Name, input.Arguments)
			if e != nil {
				result = wire.ErrorResult(e)
			}
			if h, ok := ctx.Value(holderKey{}).(*resultHolder); ok {
				h.result = &result
			}
			b, e := json.Marshal(result)
			if e != nil {
				return nil, e
			}
			var sdkResult mcp.CallToolResult
			if e = json.Unmarshal(b, &sdkResult); e != nil {
				return nil, e
			}
			return &sdkResult, nil
		})
	}
	s.SDK.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				return &rawResult{Value: registry.List()}, nil
			}
			if method == "tools/call" {
				b, e := json.Marshal(request.GetParams())
				if e != nil {
					return nil, e
				}
				var input struct {
					Name      string `json:"name"`
					Arguments any    `json:"arguments"`
				}
				if e = json.Unmarshal(b, &input); e != nil {
					return nil, e
				}
				if _, e = registry.Validate(input.Name, input.Arguments); e != nil {
					if ve, ok := e.(*contracts.ValidationError); ok && ve.SDK {
						return &rawResult{Value: ve.SDKResult()}, nil
					}
				}
				holder := &resultHolder{}
				ctx = context.WithValue(ctx, holderKey{}, holder)
				result, e := next(ctx, method, request)
				if holder.result != nil {
					return &rawResult{Value: *holder.result}, nil
				}
				return result, e
			}
			result, e := next(ctx, method, request)
			if e != nil {
				return result, e
			}
			if method == "initialize" {
				b, _ := json.Marshal(result)
				var actual map[string]any
				_ = json.Unmarshal(b, &actual)
				value := map[string]any{}
				for k, v := range registry.Initialization {
					value[k] = v
				}
				if protocol, ok := actual["protocolVersion"]; ok {
					value["protocolVersion"] = protocol
				}
				if extra := request.GetExtra(); extra != nil && extra.Header.Get("X-Codexpro-Go-Legacy-Protocol") == "2024-10-07" {
					value["protocolVersion"] = "2024-10-07"
				}
				return &rawResult{Value: value}, nil
			}
			return result, nil
		}
	})
	s.handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.SDK }, &mcp.StreamableHTTPOptions{
		JSONResponse: false, SessionTimeout: 0, DisableLocalhostProtection: true, MaxRequestBodyBytes: 20 * 1024 * 1024, PropagateRequestCancellation: true,
	})
	s.Mux.HandleFunc("/mcp", s.mcp)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.ctx = ctx
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.prune()
			}
		}
	}()
	return s, nil
}
func (s *Server) Principal(*http.Request) string { return s.principal }
func (s *Server) SessionCount() int              { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sessions) }
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func JSONError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
}
func rpcError(w http.ResponseWriter, status, code int, message string) {
	jsonResponse(w, status, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": code, "message": message}, "id": nil})
}
func (s *Server) forget(id string) {
	s.mu.Lock()
	record := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if record == nil {
		return
	}
	record.cancel()
	for session := range s.SDK.Sessions() {
		if session.ID() == id {
			_ = session.Close()
			break
		}
	}
	if s.closeSession != nil {
		s.closeSession(wire.Scope{PrincipalID: record.principal, SessionID: id, TransportID: id})
	}
}
func (s *Server) prune() {
	now := time.Now()
	ttl := time.Duration(s.Config.Int("httpSessionTtlMs", 1800000)) * time.Millisecond
	s.mu.Lock()
	expired := []string{}
	for id, record := range s.sessions {
		if record.active == 0 && now.Sub(record.lastSeen) > ttl {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		s.sessions[id].cancel()
		delete(s.sessions, id)
	}
	limit := s.Config.Int("maxHttpSessions", 64)
	for len(s.sessions) > limit {
		oldest := ""
		var oldestAt time.Time
		for id, record := range s.sessions {
			if record.active == 0 && (oldest == "" || record.lastSeen.Before(oldestAt)) {
				oldest = id
				oldestAt = record.lastSeen
			}
		}
		if oldest == "" {
			break
		}
		expired = append(expired, oldest)
		s.sessions[oldest].cancel()
		delete(s.sessions, oldest)
	}
	s.mu.Unlock()
	for _, id := range expired {
		for session := range s.SDK.Sessions() {
			if session.ID() == id {
				_ = session.Close()
				break
			}
		}
		if s.closeSession != nil {
			s.closeSession(wire.Scope{PrincipalID: s.principal, SessionID: id, TransportID: id})
		}
	}
}
func (s *Server) BeginDrain() { s.mu.Lock(); s.draining = true; s.mu.Unlock() }
func (s *Server) Close() error {
	s.cancel()
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.forget(id)
	}
	return nil
}
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		NotFound(w, r)
		return
	}
	id := r.Header.Get("Mcp-Session-Id")
	s.mu.Lock()
	draining := s.draining
	s.mu.Unlock()
	if draining && id == "" {
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Connection", "close")
		rpcError(w, 503, -32000, "Server is restarting; retry shortly")
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 20*1024*1024+1))
		if e != nil {
			rpcError(w, 400, -32700, "Parse error.")
			return
		}
		if len(raw) > 20*1024*1024 {
			rpcError(w, 413, -32000, "Payload too large.")
			return
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
			rpcError(w, 400, -32700, "Parse error.")
			return
		}
		var anyBody any
		if e = json.Unmarshal(raw, &anyBody); e != nil {
			rpcError(w, 400, -32700, "Parse error.")
			return
		}
		body, _ = anyBody.(map[string]any)
		r.Header.Set("X-Codexpro-Go-Request-Id", fmt.Sprint(body["id"]))
		r.Header.Del("X-Codexpro-Go-Legacy-Protocol")
		// The SDK no longer lists the oldest TypeScript protocol. Its wire features
		// here are identical to 2024-11-05; adapt the internal version, preserving the reply.
		if body["method"] == "initialize" {
			if params, ok := body["params"].(map[string]any); ok && params["protocolVersion"] == "2024-10-07" {
				params["protocolVersion"] = "2024-11-05"
				r.Header.Set("X-Codexpro-Go-Legacy-Protocol", "2024-10-07")
				raw, _ = json.Marshal(body)
			}
		}
		if r.Header.Get("MCP-Protocol-Version") == "2024-10-07" {
			r.Header.Set("MCP-Protocol-Version", "2024-11-05")
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	s.prune()
	s.mu.Lock()
	record := s.sessions[id]
	if record != nil {
		record.lastSeen = time.Now()
	}
	s.mu.Unlock()
	initialize := r.Method == http.MethodPost && id == "" && body["method"] == "initialize"
	if !initialize {
		if id == "" {
			rpcError(w, 400, -32000, "Bad Request: Mcp-Session-Id header is required")
			return
		}
		if !sessionPattern.MatchString(id) {
			rpcError(w, 400, -32000, "Bad Request: invalid MCP session id")
			return
		}
		if record == nil {
			rpcError(w, 404, -32001, "Session not found")
			return
		}
		if record.principal != s.principal {
			rpcError(w, 403, -32003, "Workspace session belongs to a different authenticated principal")
			return
		}
		s.mu.Lock()
		record.active++
		s.mu.Unlock()
	}
	if r.Method == http.MethodDelete {
		record.cancel()
		w = &legacyDeleteResponse{ResponseWriter: w}
	}
	s.handler.ServeHTTP(w, r)
	if initialize {
		id = w.Header().Get("Mcp-Session-Id")
	}
	s.mu.Lock()
	if current := s.sessions[id]; current != nil {
		current.active = max(0, current.active-1)
		current.lastSeen = time.Now()
	}
	s.mu.Unlock()
	if r.Method == http.MethodDelete {
		s.forget(id)
	}
}
func credentialFrom(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) == 1 {
		match := bearerPattern.FindStringSubmatch(strings.TrimSpace(values[0]))
		if len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			return strings.TrimSpace(match[1])
		}
	}
	query := r.URL.Query()
	values, ok := query["codexpro_token"]
	if !ok {
		values = query["token"]
	}
	if len(values) == 1 {
		return values[0]
	}
	return ""
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/favicon.ico" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		admin.Favicon(w, r)
		return
	}
	for k, v := range map[string]string{"Cache-Control": "no-store", "Pragma": "no-cache", "Referrer-Policy": "no-referrer", "Permissions-Policy": "camera=(), microphone=(), geolocation=()", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
		w.Header().Set(k, v)
	}
	candidate := credentialFrom(r)
	if len(candidate) != len(s.credential) || subtle.ConstantTimeCompare([]byte(candidate), []byte(s.credential)) != 1 {
		key, _, e := net.SplitHostPort(r.RemoteAddr)
		if e != nil {
			key = r.RemoteAddr
		}
		if key == "" {
			key = "local"
		}
		now := time.Now()
		s.mu.Lock()
		window := s.failures[key]
		if !window.reset.After(now) {
			window = failureWindow{reset: now.Add(time.Minute)}
		}
		window.count++
		s.failures[key] = window
		if len(s.failures) > 4096 {
			for k, v := range s.failures {
				if !v.reset.After(now) {
					delete(s.failures, k)
				}
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if window.count > 10 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(window.reset).Seconds()+0.999))))
			w.WriteHeader(429)
			_, _ = io.WriteString(w, "Too Many Authentication Attempts")
			return
		}
		if admin.WantsDashboardReauth(r) {
			admin.SendDashboardReauth(w, r)
		} else {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "Unauthorized")
		}
		return
	}
	handler, pattern := s.Mux.Handler(r)
	if pattern == "" {
		NotFound(w, r)
		return
	}
	handler.ServeHTTP(w, r)
}
func SameOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, e := url.Parse(origin)
	if e != nil || parsed.Host == "" || r.Host == "" || strings.ToLower(parsed.Host) != r.Host {
		JSONError(w, 403, "origin_denied", "Cross-origin admin requests are not allowed.")
		return false
	}
	return true
}
func NotFound(w http.ResponseWriter, r *http.Request) { httpcompat.NotFound(w, r) }
