package plansaccess

import (
	"path"
	"slices"
	"strings"
	"unicode"
)

// Grant names a specific registered agent and operation. The service also
// checks the kind ceiling, actual target object, registered host and ownership.
type Grant struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

func (a Agent) Allows(action, target string) bool {
	return !a.Disabled && slices.Contains(a.Grants, Grant{Action: action, Target: target})
}

func (a Agent) validateGrants(agents map[string]Agent) error {
	seen := make(map[Grant]bool)
	for _, g := range a.Grants {
		target, ok := agents[g.Target]
		if !ok || seen[g] || !slices.Contains([]string{"bind", "rebind", "start", "resume", "stop"}, g.Action) {
			return errInvalid
		}
		switch a.Kind {
		case "concierge":
			if target.Kind != "crew" || target.PrincipalID != a.PrincipalID {
				return errInvalid
			}
		case "coordinator", "supervisor":
			// An exact supervisor grant designates one of its design leads;
			// neither system manager can delegate rights to a personal agent.
			if target.Kind != "leaf" {
				return errInvalid
			}
		default:
			return errInvalid
		}
		seen[g] = true
	}
	return nil
}

// Product delegations belong to an explicit project. A docs path is a
// canonical repository-relative file or subtree below docs (no trailing slash).
type ProjectGrant struct {
	ProjectID     string   `json:"project_id"`
	DocsPaths     []string `json:"docs_paths,omitempty"`
	DecisionAreas []string `json:"decision_areas,omitempty"`
}

func (p Principal) DocsFor(project string) []string {
	if p.Disabled || p.Role != "product" {
		return nil
	}
	for _, g := range p.Delegations {
		if g.ProjectID == project {
			return slices.Clone(g.DocsPaths)
		}
	}
	return nil
}
func (p Principal) validateDelegations(projects map[string]bool) error {
	seen := map[string]bool{}
	for _, g := range p.Delegations {
		if p.Role != "product" || !projects[g.ProjectID] || !unique(seen, g.ProjectID) || len(g.DocsPaths)+len(g.DecisionAreas) == 0 {
			return errInvalid
		}
		paths, areas := map[string]bool{}, map[string]bool{}
		for _, s := range g.DocsPaths {
			if !strings.HasPrefix(s, "docs/") || path.Clean(s) != s || strings.ContainsAny(s, "\\:*?[]") || strings.IndexFunc(s, unicode.IsControl) >= 0 || !unique(paths, s) {
				return errInvalid
			}
			for _, part := range strings.Split(s, "/") {
				if strings.EqualFold(part, ".git") {
					return errInvalid
				}
			}
		}
		for _, area := range g.DecisionAreas {
			if !idRE.MatchString(area) || !unique(areas, area) {
				return errInvalid
			}
		}
	}
	return nil
}
