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
	identity, projects, ok, _, _ := s.authenticateHTTP(token)
	return identity, projects, ok
}

func configuredPolicyReader(readers []*plansaccess.Reader) *plansaccess.Reader {
	if len(readers) != 0 && readers[0] != nil {
		return readers[0]
	}
	return plansaccess.NewReader()
}

// A serving reader belongs to the service, including its collector and startup
// handler. Policy writers continue to use strict Load under their writer lock.
func (s *Server) readPolicy(path string) (*plansaccess.Policy, error) {
	s.policyInit.Do(func() {
		if s.policyReader == nil {
			s.policyReader = plansaccess.NewReader()
		}
	})
	return s.policyReader.Load(path)
}

// The owner-page decision uses this exact policy observation. An unavailable
// policy is a service failure, never an incorrect credential.
func (s *Server) authenticateHTTP(token string) (wire.Identity, []string, bool, bool, error) {
	path := s.Config.String("principalsFile", "")
	if path != "" {
		policy, err := s.readPolicy(path)
		if err != nil {
			s.logPolicyFailure()
			return wire.Identity{}, nil, false, false, err
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
			return actor, projects, true, id.PrincipalType == "person" && id.Role == "owner" && id.AgentKind == "concierge" && id.Audience == "codexpro", nil
		}
		if recognized || !s.Config.Bool("legacyBridge", false) {
			return wire.Identity{}, nil, false, false, nil
		}
	}
	if token == "" || plansaccess.IsNamedToken(token) || subtle.ConstantTimeCompare([]byte(token), []byte(s.credential)) != 1 {
		return wire.Identity{}, nil, false, false, nil
	}
	return wire.Identity{Source: "legacy"}, nil, true, path == "", nil
}

func policyUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	JSONError(w, http.StatusServiceUnavailable, "policy_unavailable", "Access policy unavailable; retry shortly")
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
