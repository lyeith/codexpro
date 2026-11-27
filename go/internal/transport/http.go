// Package transport serves CodexPro tools over HTTP using the MCP Go SDK.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/oplog"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/time/rate"
)
type reservationKey struct{}
type holderKey struct{}
type resultHolder struct {
	arguments map[string]any
	result    *wire.Result
}
type sessionRecord struct {
	identity wire.Identity
	lastSeen time.Time
	active   int
	ctx      context.Context
	cancel   context.CancelFunc
}
type Server struct {
	Config                *config.Config
	Registry              *contracts.Registry
	Mux                   *http.ServeMux
	SDK                   *mcp.Server
	handler               http.Handler
	caller                wire.Caller
	credential, principal string
	mu                    sync.Mutex
	sessions              map[string]*sessionRecord
	mailRequests          map[string]*logicalMailRequest
	pending               map[*initReservation]struct{}
	failures              map[string]failureWindow
	accessLogLimiter      *rate.Limiter
	policyFailureLog      oplog.Repeated
	policyReader          *plansaccess.Reader
	policyInit            sync.Once
	mailReceiptFailureLog oplog.Repeated
	httpFailureLog        oplog.Repeated
	draining              bool
	stateless             bool
	inflight              atomic.Int64
	closeSession          func(wire.Scope)
	cancel                context.CancelFunc
	ctx                   context.Context
}

var sessionPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func uuid() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func New(cfg *config.Config, registry *contracts.Registry, caller wire.Caller, principal string, onClose func(wire.Scope), readers ...*plansaccess.Reader) (*Server, error) {
	credential, e := legacyCredential(cfg)
	if e != nil {
		return nil, e
	}
	s := &Server{Config: cfg, Registry: registry, Mux: http.NewServeMux(), caller: caller, credential: credential, principal: principal, sessions: map[string]*sessionRecord{}, failures: map[string]failureWindow{}, closeSession: onClose, policyReader: configuredPolicyReader(readers)}
	s.stateless = cfg.HTTPSessionMode() == config.HTTPSessionStateless
	s.accessLogLimiter = rate.NewLimiter(accessRequestsPerSecond, accessRequestBurst)
	versions := []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}
	if s.stateless {
		// Sessionless clients may skip initialize (server/discover). Legacy
		// initialize negotiation never selects this revision.
		versions = append([]string{modernProtocol}, versions...)
	}
	s.SDK = mcp.NewServer(&mcp.Implementation{Name: "CodexPro", Version: "0.31.0"}, &mcp.ServerOptions{
		Instructions: contracts.Instructions, Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: !s.stateless}},
		SupportedProtocolVersions: versions,
		GetSessionID:              uuid,
	})
	for _, descriptor := range registry.Tools {
		s.SDK.AddTool(&descriptor.Tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			holder, ok := ctx.Value(holderKey{}).(*resultHolder)
			if !ok || holder == nil {
				return nil, errors.New("missing tool request state")
			}
			// The receiving middleware binds this POST's registration and grants.
			id := wire.IdentityFrom(ctx)
			if id.Source == "" || req.Extra == nil {
				return nil, errors.New("missing authenticated request")
			}
			scope := wire.Scope{Identity: id, PrincipalID: s.resourceOwnerKey(id), StartedAt: time.Now(), RequestID: req.Extra.Header.Get("X-Codexpro-Go-Request-Id")}
			lifetime, base := s.ctx, ctx
			if s.stateless {
				// Each POST is its own SDK session. Like a stateful call, a
				// legacy-protocol call outlives an HTTP disconnect and ends at
				// server close; 2026-07-28 cancels with its HTTP request.
				if !usesModernProtocol(req) {
					base = context.WithoutCancel(ctx)
				}
			} else {
				scope.SessionID, scope.TransportID = req.Session.ID(), req.Session.ID()
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
				lifetime = record.ctx
			}
			callCtx, cancelCall := context.WithCancel(base)
			stopLifetime := context.AfterFunc(lifetime, cancelCall)
			defer func() { stopLifetime(); cancelCall() }()
			s.inflight.Add(1)
			defer s.inflight.Add(-1)
			result, e := s.caller.Call(callCtx, scope, descriptor.Name, holder.arguments)
			if e != nil {
				result = wire.ErrorResult(e)
			}
			// A request-local label may select explicitly addressed logical mail
			// only inside the configured authenticated fleet account. Offering
			// bodies does not receipt them or change the tool result fields.
			result = s.attachLogicalMailForRequest(callCtx, req.Extra.Header, id, holder.arguments, result)
			holder.result = &result
			// The receiving middleware returns the authoritative raw envelope.
			// A nonnil placeholder lets the SDK's older-protocol middleware
			// inspect its result without decoding and then discarding ours.
			return &mcp.CallToolResult{Content: []mcp.Content{}}, nil
		})
	}
	s.SDK.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			// The SDK context belongs to initialize; RequestExtra carries the
			// decision made at this POST's HTTP admission, including its grants.
			if method == "tools/list" || method == "tools/call" {
				decision, ok := authorizationFromExtra(request.GetExtra())
				if !ok {
					return nil, errors.New("missing authenticated request")
				}
				if !sameBinding(decision.identity, wire.IdentityFrom(ctx)) {
					return nil, errors.New("invalid authenticated request")
				}
				ctx = wire.WithProjects(wire.WithIdentity(ctx, decision.identity), decision.projects)
			}
			modern := usesModernProtocol(request)
			if method == "tools/list" {
				if wire.IdentityFrom(ctx).Restricted() {
					return &rawResult{Value: registry.ReadList(), complete: modern}, nil
				}
				return &rawResult{Value: registry.List(), complete: modern}, nil
			}
			if method == "tools/call" {
				params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
				if !ok || params == nil {
					return nil, errors.New("missing tool parameters")
				}
				var arguments any
				if len(params.Arguments) != 0 {
					if e := json.Unmarshal(params.Arguments, &arguments); e != nil {
						return nil, e
					}
				}
				if wire.IdentityFrom(ctx).Restricted() && !wire.ReadToolAllowed(params.Name) {
					return nil, errors.New("tool is unavailable for this credential")
				}
				if _, e := registry.Validate(params.Name, arguments); e != nil {
					if ve, ok := e.(*contracts.ValidationError); ok && ve.SDK {
						result := ve.SDKResult()
						if args, ok := arguments.(map[string]any); ok {
							extra := request.GetExtra()
							result = s.attachLogicalMailForRequest(ctx, extra.Header, wire.IdentityFrom(ctx), args, result)
						}
						return &rawResult{Value: result, complete: modern}, nil
					}
				}
				// Keep the original decoded map: the engine owns projection and
				// business validation, and logical mail uses the explicit label.
				args, _ := arguments.(map[string]any)
				holder := &resultHolder{arguments: args}
				ctx = context.WithValue(ctx, holderKey{}, holder)
				result, e := next(ctx, method, request)
				if holder.result != nil {
					return &rawResult{Value: *holder.result, complete: modern}, nil
				}
				return result, e
			}
			result, e := next(ctx, method, request)
			if e != nil {
				return result, e
			}
			if method == "server/discover" && wire.IdentityFrom(ctx).Restricted() {
				if discovered, ok := result.(*mcp.DiscoverResult); ok {
					response := *discovered
					response.Instructions = restrictedInstructions
					return &response, nil
				}
			}
			if method == "initialize" {
				// Publish the binding before the SDK writes the successful
				// initialize response. A subsequent request can arrive while
				// the initial HTTP handler is still returning.
				id, actor := request.GetSession().ID(), wire.IdentityFrom(ctx)
				reservation, _ := ctx.Value(reservationKey{}).(*initReservation)
				if !s.stateless && !s.publishInitialize(reservation, id, actor) {
					return nil, errors.New("session initialization no longer admitted")
				}
				response := *result.(*mcp.InitializeResult)
				if actor.Restricted() {
					response.Instructions = restrictedInstructions
				}
				if extra := request.GetExtra(); extra != nil && extra.Header.Get("X-Codexpro-Go-Legacy-Protocol") == "2024-10-07" {
					response.ProtocolVersion = "2024-10-07"
				}
				return &response, nil
			}
			return result, nil
		}
	})
	s.handler = sdkAuthenticatedHandler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.SDK }, &mcp.StreamableHTTPOptions{
		Stateless: s.stateless, JSONResponse: false, SessionTimeout: 0, DisableLocalhostProtection: true, MaxRequestBodyBytes: 20 * 1024 * 1024, PropagateRequestCancellation: true,
	}))
	s.Mux.HandleFunc("/mcp", s.mcp)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.ctx = ctx
	if s.stateless {
		return s, nil
	}
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
	if s.stateless {
		// There is no session to resume, stream from or delete.
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			rpcError(w, http.StatusMethodNotAllowed, -32000, "Method not allowed in stateless MCP mode.")
			return
		}
		// A stale session ID from an earlier process or mode is ignored, never 404.
		r.Header.Del("Mcp-Session-Id")
	}
	// The ready server admits HTTP requests and owns their session registry.
	// Restricted GET requests still require this explicit session check.
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
	logged := s.requestLog(r.Method, id)
	defer func() {
		if logged != nil {
			logged.done(recorder)
		}
		if recorder.status >= 500 && s.httpFailureLog.Allow() {
			fmt.Fprintf(os.Stderr, "CodexPro MCP request failed with HTTP %d\n", recorder.status)
		}
	}()
	s.mu.Lock()
	draining := s.draining
	s.mu.Unlock()
	// Stateful drain still serves requests on existing sessions; a stateless
	// request is always new work and goes to the replacement process.
	if draining && (id == "" || s.stateless) {
		restarting(w)
		return
	}
	var rpcMethod string
	var mailRPCID json.RawMessage
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
		var peek mcpEnvelopePeek
		hasObject := false
		if trimmed[0] == '{' {
			if e = json.Unmarshal(raw, &peek); e != nil {
				rpcError(w, 400, -32700, "Parse error.")
				return
			}
			hasObject = true
			if len(peek.Method) > 0 {
				_ = json.Unmarshal(peek.Method, &rpcMethod)
			}
		} else if !json.Valid(raw) {
			rpcError(w, 400, -32700, "Parse error.")
			return
		}
		if wire.IdentityFrom(r.Context()).Restricted() {
			// A restricted request must be one supported protocol operation.
			switch rpcMethod {
			case "initialize", "notifications/initialized", "tools/list", "tools/call", "ping", "notifications/cancelled", "server/discover":
			default:
				rpcError(w, http.StatusForbidden, -32003, "Operation is unavailable for this credential")
				return
			}
		}
		rpcID := "-"
		if hasObject {
			rpcID = wire.Redact(fmt.Sprint(peek.ID))
		}
		if logged != nil {
			toolName := ""
			if rpcMethod == "tools/call" && len(peek.Params) > 0 {
				var toolParams struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(peek.Params, &toolParams)
				toolName = toolParams.Name
			}
			logged.body(len(raw), rpcID, rpcMethod, toolName)
			logged.received()
		}
		r.Header.Set("X-Codexpro-Go-Request-Id", rpcID)
		// Never accept a caller-provided routing header. Only the exact label
		// in this unambiguous request may select a logical mail offer.
		r.Header.Del(logicalMailLabelHeader)
		if (s.Config.String("logicalMailSocket", "") != "" || s.Config.String("pendingMailSocket", "") != "") && rpcMethod == "tools/call" {
			r.Header.Set(logicalMailLabelHeader, logicalMailRequestLabel(raw))
			mailRPCID, _ = uniqueMailJSONField(raw, "id")
		}
		r.Header.Del("X-Codexpro-Go-Legacy-Protocol")
		// The SDK no longer lists the oldest TypeScript protocol. Its wire features
		// here are identical to 2024-11-05; adapt the internal version, preserving the reply.
		if rpcMethod == "initialize" && bytes.Contains(peek.Params, legacyProtocolMarker) {
			var body map[string]any
			if json.Unmarshal(raw, &body) == nil {
				if params, ok := body["params"].(map[string]any); ok && params["protocolVersion"] == "2024-10-07" {
					params["protocolVersion"] = "2024-11-05"
					r.Header.Set("X-Codexpro-Go-Legacy-Protocol", "2024-10-07")
					raw, _ = json.Marshal(body)
				}
			}
		}
		if r.Header.Get("MCP-Protocol-Version") == "2024-10-07" {
			r.Header.Set("MCP-Protocol-Version", "2024-11-05")
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
	}
	actor := wire.IdentityFrom(r.Context())
	initialize := r.Method == http.MethodPost && id == "" && rpcMethod == "initialize"
	// Every stateless POST is self-contained: no admission, binding or capacity slot.
	if !s.stateless {
		s.prune()
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
				if logged != nil {
					logged.session = id
				}
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
	}
	if logged != nil {
		logged.received()
	}
	// The SDK forwards headers, not URL query parameters, in RequestExtra.
	r.Header.Set("Authorization", "Bearer "+credentialFrom(r))
	// Legacy MCP protocols intentionally retain accepted tool calls after an
	// HTTP disconnect. Only optional mail follows this POST's write boundary.
	r.Header.Del(logicalMailContextHeader)
	var mailWriter *logicalMailResponseWriter
	if r.Method == http.MethodPost && rpcMethod == "tools/call" && r.Header.Get(logicalMailLabelHeader) != "" {
		handle, request, release := s.trackLogicalMailRequest(r.Context())
		r.Header.Set(logicalMailContextHeader, handle)
		defer release()
		// A stateless SDK request returns only after its accepted tool call
		// ends; retire the mail handle when the client goes, as stateful does.
		defer context.AfterFunc(r.Context(), release)()
		mailWriter = &logicalMailResponseWriter{
			ResponseWriter: w, ctx: r.Context(), request: request, rpcID: mailRPCID,
		}
		w = mailWriter
	}
	s.handler.ServeHTTP(w, r)
	if mailWriter != nil && mailWriter.complete(s.ctx) && s.mailReceiptFailureLog.Allow() {
		fmt.Fprintln(os.Stderr, "logical mail receipt unconfirmed; original IDs may be offered again")
	}
}

func restarting(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Connection", "close")
	rpcError(w, http.StatusServiceUnavailable, -32000, "Server is restarting; retry shortly")
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