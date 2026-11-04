package transport

import (
	"context"
	"crypto/subtle"
	"net/http"

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

func (s *Server) resourcePrincipal(id wire.Identity) string {
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

func (s *Server) ScopePrincipal(ctx context.Context) string {
	return s.resourcePrincipal(wire.IdentityFrom(ctx))
}

func (s *Server) Principal(r *http.Request) string {
	id := wire.IdentityFrom(r.Context())
	if id.Source == "legacy" {
		return "legacy"
	}
	return id.PrincipalID
}
