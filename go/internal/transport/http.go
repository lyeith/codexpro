// Package transport adapts the official MCP Go SDK to the frozen HTTP contract.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

type reservationKey struct{}
type holderKey struct{}
type resultHolder struct{ result *wire.Result }
type sessionRecord struct {
	identity wire.Identity
	lastSeen time.Time
	active   int
	ctx      context.Context
	cancel   context.CancelFunc
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
	pending               map[*initReservation]struct{}
	failures              map[string]failureWindow
	nextPolicyLog         time.Time
	draining              bool
	inflight              atomic.Int64
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
	credential, e := legacyCredential(cfg)
	if e != nil {
		return nil, e
	}
	s := &Server{Config: cfg, Registry: registry, Mux: http.NewServeMux(), caller: caller, credential: credential, principal: principal, sessions: map[string]*sessionRecord{}, failures: map[string]failureWindow{}, closeSession: onClose}
	instructions, _ := registry.Initialization["instructions"].(string)
	s.SDK = mcp.NewServer(&mcp.Implementation{Name: "CodexPro", Version: "0.31.0"}, &mcp.ServerOptions{
		Instructions: instructions, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
		GetSessionID:              uuid,
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
			// The receiving middleware binds this POST's registration and grants.
			id := wire.IdentityFrom(ctx)
			if id.Source == "" || req.Extra == nil {
				return nil, errors.New("missing authenticated request")
			}
			scope := wire.Scope{Identity: id, PrincipalID: s.resourceOwnerKey(id), SessionID: req.Session.ID(), TransportID: req.Session.ID(), StartedAt: time.Now(), RequestID: req.Extra.Header.Get("X-Codexpro-Go-Request-Id")}
			// An HTTP client can disconnect before its engine call returns.
			// Keep that session pinned until the call itself has finished.
			s.mu.Lock()
			record := s.sessions[scope.SessionID]
			if record == nil || !sameBinding(record.identity, id) {
				s.mu.Unlock()
				return nil, errors.New("missing authenticated session")
			}
			record.active++
			s.mu.Unlock()
			defer s.unpin(record)
			callCtx, cancelCall := context.WithCancel(ctx)
			stopLifetime := context.AfterFunc(record.ctx, cancelCall)
			defer func() { stopLifetime(); cancelCall() }()
			s.inflight.Add(1)
			defer s.inflight.Add(-1)
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
			// The SDK context belongs to initialize; RequestExtra belongs to
			// this POST. Derive identity and grants from the same policy read.
			if method == "tools/list" || method == "tools/call" {
				extra := request.GetExtra()
				if extra == nil {
					return nil, errors.New("missing authenticated request")
				}
				id, projects, ok := s.authenticate(strings.TrimPrefix(extra.Header.Get("Authorization"), "Bearer "))
				if !ok || !sameBinding(id, wire.IdentityFrom(ctx)) {
					return nil, errors.New("invalid authenticated request")
				}
				ctx = wire.WithProjects(wire.WithIdentity(ctx, id), projects)
			}
			if method == "tools/list" {
				if wire.IdentityFrom(ctx).Restricted() {
					return &rawResult{Value: registry.ReadList()}, nil
				}
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
				if wire.IdentityFrom(ctx).Restricted() && !wire.ReadToolAllowed(input.Name) {
					return nil, errors.New("tool is unavailable for this credential")
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
				// Publish the binding before the SDK writes the successful
				// initialize response. A subsequent request can arrive while
				// the initial HTTP handler is still returning.
				id, actor := request.GetSession().ID(), wire.IdentityFrom(ctx)
				reservation, _ := ctx.Value(reservationKey{}).(*initReservation)
				if !s.publishInitialize(reservation, id, actor) {
					return nil, errors.New("session initialization no longer admitted")
				}
				b, _ := json.Marshal(result)
				var actual map[string]any
				_ = json.Unmarshal(b, &actual)
				value := map[string]any{}
				for k, v := range registry.Initialization {
					value[k] = v
				}
				if actor.Restricted() {
					value["instructions"] = "Use list_projects to find granted workspace IDs for file tools and literal text search. Use a project_id with activity_list and its action_id with activity_get."
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
func (s *Server) SessionCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sessions) }
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
	if record != nil {
		s.closeDetachedSession(id, record)
	}
}

func (s *Server) unpin(record *sessionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record.active--
	record.lastSeen = time.Now()
}

func (s *Server) prune() {
	now := time.Now()
	ttl := time.Duration(s.Config.Int("httpSessionTtlMs", 1800000)) * time.Millisecond
	s.mu.Lock()
	expired := map[string]*sessionRecord{}
	for id, record := range s.sessions {
		if record.active == 0 && now.Sub(record.lastSeen) > ttl {
			expired[id] = record
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
	for id, record := range expired {
		s.closeDetachedSession(id, record)
	}
}
func (s *Server) BeginDrain() { s.mu.Lock(); s.draining = true; s.mu.Unlock() }

// InFlight reports tool calls currently executing.
func (s *Server) InFlight() int64 { return s.inflight.Load() }

// Drain stops new MCP sessions and waits until no tool call is executing or ctx
// ends, then closes every MCP session so open streams end. It returns the
// number of tool calls still executing when it gave up waiting.
func (s *Server) Drain(ctx context.Context) int64 {
	s.BeginDrain()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for s.inflight.Load() > 0 {
		select {
		case <-ctx.Done():
			remaining := s.inflight.Load()
			_ = s.Close()
			return remaining
		case <-ticker.C:
		}
	}
	_ = s.Close()
	return 0
}
func (s *Server) Close() error {
	s.mu.Lock()
	s.draining = true
	clear(s.pending)
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	s.cancel()
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
	// Only the published MCP server owns the session registry. Bootstrap has
	// already authenticated the request but cannot decide session ownership.
	if identity := wire.IdentityFrom(r.Context()); r.Method == http.MethodGet && identity.Restricted() {
		if !s.matchesOptionalSession(r, identity) {
			JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
			return
		}
		w.Header().Set("Allow", "POST, DELETE")
		JSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST or DELETE for this endpoint.")
		return
	}
	id := r.Header.Get("Mcp-Session-Id")
	recorder := &statusRecorder{ResponseWriter: w}
	w = recorder
	logged := &mcpRequestLog{enabled: s.Config.Bool("accessLog", true), started: time.Now(), httpMethod: r.Method, session: id, rpcID: "-", rpcMethod: "-", tool: "-", recorder: recorder}
	defer logged.done()
	s.mu.Lock()
	draining := s.draining
	s.mu.Unlock()
	if draining && id == "" {
		restarting(w)
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		// The body must arrive within bodyReadTimeoutMs (the TypeScript service
		// had Node's 300 s request timeout); a stalled upload would otherwise hold
		// the request before dispatch until the client gave up.
		control := http.NewResponseController(recorder.ResponseWriter)
		_ = control.SetReadDeadline(time.Now().Add(time.Duration(max(1, s.Config.Int("bodyReadTimeoutMs", 60000))) * time.Millisecond))
		raw, e := io.ReadAll(io.LimitReader(r.Body, 20*1024*1024+1))
		if e != nil && errors.Is(e, os.ErrDeadlineExceeded) {
			// Keep the expired deadline: the server would otherwise block draining
			// the unread body before it can write this reply.
			w.Header().Set("Connection", "close")
			rpcError(w, http.StatusRequestTimeout, -32000, "Request body was not received in time.")
			return
		}
		_ = control.SetReadDeadline(time.Time{})
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
		if wire.IdentityFrom(r.Context()).Restricted() {
			// A restricted request must be one supported protocol operation.
			method, _ := body["method"].(string)
			switch method {
			case "initialize", "notifications/initialized", "tools/list", "tools/call", "ping", "notifications/cancelled":
			default:
				rpcError(w, http.StatusForbidden, -32003, "Operation is unavailable for this credential")
				return
			}
		}
		logged.bodyBytes = len(raw)
		if body != nil {
			logged.rpcID = wire.Redact(fmt.Sprint(body["id"]))
			if method, ok := body["method"].(string); ok {
				logged.rpcMethod = wire.Redact(method)
			}
			if params, ok := body["params"].(map[string]any); ok {
				if name, ok := params["name"].(string); ok && logged.rpcMethod == "tools/call" {
					logged.tool = wire.Redact(name)
				}
			}
		}
		logged.received()
		r.Header.Set("X-Codexpro-Go-Request-Id", logged.rpcID)
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
	actor := wire.IdentityFrom(r.Context())
	initialize := r.Method == http.MethodPost && id == "" && body["method"] == "initialize"
	if initialize {
		reservation, state := s.reserveInitialize(r.Context(), actor)
		switch state {
		case admissionDraining:
			restarting(w)
			return
		case admissionCapacity:
			w.Header().Set("Retry-After", "1")
			rpcError(w, http.StatusServiceUnavailable, -32000, "Session capacity reached; retry later")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), reservationKey{}, reservation))
		defer func() {
			s.releaseInitialize(reservation)
			id := w.Header().Get("Mcp-Session-Id")
			logged.session = id
			if id == "" {
				return
			}
			s.mu.Lock()
			record := s.sessions[id]
			s.mu.Unlock()
			switch {
			case record == nil:
				// The SDK may have initialized before a concurrent drain
				// refused publication. Close outside the SDK method handler.
				s.closeSDKSession(id)
			case r.Context().Err() != nil:
				s.forget(id)
			default:
				s.unpin(record)
			}
		}()
	} else {
		if id == "" {
			rpcError(w, 400, -32000, "Bad Request: Mcp-Session-Id header is required")
			return
		}
		if !sessionPattern.MatchString(id) {
			rpcError(w, 400, -32000, "Bad Request: invalid MCP session id")
			return
		}
		s.mu.Lock()
		record := s.sessions[id]
		if record == nil {
			s.mu.Unlock()
			rpcError(w, 404, -32001, "Session not found")
			return
		}
		if !sameBinding(record.identity, actor) {
			s.mu.Unlock()
			rpcError(w, 403, -32003, "Workspace session belongs to a different authenticated principal")
			return
		}
		record.lastSeen = time.Now()
		record.active++
		s.mu.Unlock()
		defer s.unpin(record)
		if r.Method == http.MethodDelete {
			record.cancel()
			w = &legacyDeleteResponse{ResponseWriter: w}
			defer s.forget(id)
		}
	}
	logged.received()
	// The SDK forwards headers, not URL query parameters, in RequestExtra.
	r.Header.Set("Authorization", "Bearer "+credentialFrom(r))
	s.handler.ServeHTTP(w, r)
}

func restarting(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Connection", "close")
	rpcError(w, http.StatusServiceUnavailable, -32000, "Server is restarting; retry shortly")
}
func credentialFrom(r *http.Request) string {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ""
	}
	queryValues := append(query["codexpro_token"], query["token"]...)
	headers := r.Header.Values("Authorization")
	switch {
	case len(headers) == 1 && len(queryValues) == 0:
		match := bearerPattern.FindStringSubmatch(strings.TrimSpace(headers[0]))
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	case len(headers) == 0 && len(queryValues) == 1:
		return queryValues[0]
	}
	return ""
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	favicon := r.URL.Path == "/favicon.ico" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	if favicon && len(r.Header.Values("Authorization")) == 0 {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err == nil && !query.Has("codexpro_token") && !query.Has("token") {
			admin.Favicon(w, r)
			return
		}
	}
	for k, v := range map[string]string{"Cache-Control": "no-store", "Pragma": "no-cache", "Referrer-Policy": "no-referrer", "Permissions-Policy": "camera=(), microphone=(), geolocation=()", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
		w.Header().Set(k, v)
	}
	if s.serveStatusCollector(w, r) {
		return
	}
	candidate := credentialFrom(r)
	identity, _, ok, statusReader := s.authenticateHTTP(candidate)
	if !ok {
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
	if favicon {
		admin.Favicon(w, r)
		return
	}
	if r.URL.Path == "/status" {
		if r.URL.EscapedPath() != "/status" || (r.Method != http.MethodGet && r.Method != http.MethodHead) || !statusReader {
			JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
			return
		}
	} else if identity.Restricted() && (r.URL.EscapedPath() != "/mcp" ||
		(r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete)) {
		JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}
	handler, pattern := s.Mux.Handler(r)
	if pattern == "" {
		NotFound(w, r)
		return
	}
	r = r.WithContext(wire.WithIdentity(r.Context(), identity))
	handler.ServeHTTP(w, r)
}

func (s *Server) matchesOptionalSession(r *http.Request, identity wire.Identity) bool {
	ids := r.Header.Values("Mcp-Session-Id")
	if len(ids) == 0 {
		return true
	}
	if len(ids) != 1 || !sessionPattern.MatchString(ids[0]) {
		return false
	}
	s.prune()
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.sessions[ids[0]]
	return record != nil && sameBinding(record.identity, identity)
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
