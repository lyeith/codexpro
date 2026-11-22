package transport

import (
	"crypto/subtle"
	"log"
	"net/http"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func legacyCredential(cfg *config.Config) (string, error) {
	if !cfg.LegacyAuthEnabled() {
		return "", nil
	}
	return cfg.Token()
}

func (s *Server) authenticate(token string) (wire.Identity, []string, bool) {
	identity, projects, ok, _ := s.authenticateHTTP(token)
	return identity, projects, ok
}

// The final result is the owner-page decision made from this exact policy
// observation. MCP, activity helpers and machine JSON retain their own rules.
func (s *Server) authenticateHTTP(token string) (wire.Identity, []string, bool, bool) {
	path := s.Config.String("principalsFile", "")
	if path != "" {
		policy, err := plansaccess.Load(path)
		if err != nil {
			s.logPolicyFailure()
			return wire.Identity{}, nil, false, false
		}
		id, admitted, recognized := policy.AuthenticateWithRecognition(token, "codexpro")
		if admitted {
			actor := wire.Identity{Source: "named", PrincipalType: id.PrincipalType, PrincipalID: id.PrincipalID, Role: id.Role,
				CredentialID: id.CredentialID, AgentID: id.AgentID, AgentKind: id.AgentKind}
			var projects []string
			if id.PrincipalType == "person" && (id.AgentKind == "crew" || id.AgentKind == "concierge") {
				principal, _ := policy.Principal(id.PrincipalID)
				agent, _ := policy.Agent(id.AgentID)
				for _, project := range principal.Projects {
					if project != "projects" && (agent.ProjectID == "" || agent.ProjectID == project) {
						projects = append(projects, project)
					}
				}
			}
			return actor, projects, true, id.PrincipalType == "person" && id.Role == "owner" && id.AgentKind == "concierge" && id.Audience == "codexpro"
		}
		if recognized || !s.Config.Bool("legacyBridge", false) {
			return wire.Identity{}, nil, false, false
		}
	}
	if token == "" || plansaccess.IsNamedToken(token) || subtle.ConstantTimeCompare([]byte(token), []byte(s.credential)) != 1 {
		return wire.Identity{}, nil, false, false
	}
	return wire.Identity{Source: "legacy"}, nil, true, path == ""
}

// This diagnostic is bounded per server. Keep credentials, file locations and
// content-derived errors out of both the response and server logs.
func (s *Server) logPolicyFailure() {
	if s.policyFailureLog.Allow() {
		log.Print("CodexPro authentication refused: configured access policy unavailable")
	}
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

// AttributedPrincipal returns the policy ID used in named audit and Agentmail
// records. A shared request can identify only its legacy lane.
func AttributedPrincipal(r *http.Request) string {
	id := wire.IdentityFrom(r.Context())
	if id.Source == "legacy" {
		return "legacy"
	}
	return id.PrincipalID
}
