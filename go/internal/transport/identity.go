package transport

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func legacyCredential(cfg *config.Config) (string, error) {
	if cfg.String("principalsFile", "") != "" && !cfg.Bool("legacyBridge", false) {
		return "", nil
	}
	return cfg.Token()
}

func (s *Server) authenticate(token string) (wire.Identity, bool) {
	path := s.Config.String("principalsFile", "")
	if path != "" {
		policy, err := plansaccess.Load(path)
		if err != nil {
			s.logPolicyFailure()
			return wire.Identity{}, false
		}
		if id, ok := policy.Authenticate(token, "codexpro"); ok {
			return wire.Identity{Source: "named", PrincipalID: id.PrincipalID, Role: id.Role,
				CredentialID: id.CredentialID, AgentID: id.AgentID, AgentKind: id.AgentKind}, true
		}
		if !s.Config.Bool("legacyBridge", false) {
			return wire.Identity{}, false
		}
	}
	if token == "" || plansaccess.IsNamedToken(token) || subtle.ConstantTimeCompare([]byte(token), []byte(s.credential)) != 1 {
		return wire.Identity{}, false
	}
	return wire.Identity{Source: "legacy"}, true
}

// This diagnostic is bounded per server. Keep credentials, file locations and
// content-derived errors out of both the response and server logs.
func (s *Server) logPolicyFailure() {
	s.mu.Lock()
	now := time.Now()
	if now.Before(s.nextPolicyLog) {
		s.mu.Unlock()
		return
	}
	s.nextPolicyLog = now.Add(time.Minute)
	s.mu.Unlock()
	log.Print("CodexPro authentication refused: configured access policy unavailable")
}

// Resource owners are persisted keys for leases and recovered jobs.
func (s *Server) resourceOwnerKey(id wire.Identity) string {
	if id.Source == "legacy" {
		return s.principal
	}
	if id.Source == "named" {
		return "principal:" + id.PrincipalID
	}
	return ""
}

// A role change on a bound credential takes effect on the next request.
func sameBinding(a, b wire.Identity) bool {
	a.Role, b.Role = "", ""
	return a.Source != "" && a == b
}

func (s *Server) ResourceOwnerKey(ctx context.Context) string {
	return s.resourceOwnerKey(wire.IdentityFrom(ctx))
}

// AttributedPrincipal returns the policy ID used in named audit and Agentmail
// records. A shared request can identify only its legacy lane.
func AttributedPrincipal(r *http.Request) string {
	id := wire.IdentityFrom(r.Context())
	if id.Source == "legacy" {
		return "legacy"
	}
	return id.PrincipalID
}
