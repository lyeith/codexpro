package transport

import (
	"net/http"
	"net/netip"

	"github.com/lyeith/codexpro/go/internal/activity"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const statusCollectorPath = "/status/collector"

// This guard is shared by Bootstrap and the published dispatcher. The
// collector audience is never tried against ordinary MCP/dashboard routes.
func (s *Server) serveStatusCollector(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != statusCollectorPath {
		return false
	}
	peer, peerErr := netip.ParseAddrPort(r.RemoteAddr)
	_, queryErr := activity.ParseCollectorQuery(r.URL)
	if peerErr != nil || !peer.Addr().Unmap().IsLoopback() ||
		r.URL.EscapedPath() != statusCollectorPath || queryErr != nil ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) ||
		len(r.Header.Values("Authorization")) != 1 || len(r.Header.Values("Cookie")) != 0 {
		JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
		return true
	}
	path := s.Config.String("principalsFile", "")
	if path == "" {
		JSONError(w, http.StatusUnauthorized, "unauthorized", "Access denied")
		return true
	}
	policy, err := s.readPolicy(path)
	if err != nil {
		s.logPolicyFailure()
		policyUnavailable(w)
		return true
	}
	id, admitted, _ := policy.AuthenticateWithRecognition(credentialFrom(r), plansaccess.StatusCollectorAudience)
	if !admitted {
		JSONError(w, http.StatusUnauthorized, "unauthorized", "Access denied")
		return true
	}
	actor := wire.Identity{Source: "named", PrincipalType: id.PrincipalType, PrincipalID: id.PrincipalID,
		Role: id.Role, CredentialID: id.CredentialID, AgentID: id.AgentID, AgentKind: id.AgentKind}
	handler, pattern := s.Mux.Handler(r)
	if pattern == "" {
		NotFound(w, r)
		return true
	}
	handler.ServeHTTP(w, r.WithContext(wire.WithIdentity(r.Context(), actor)))
	return true
}
