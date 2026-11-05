// Project streams route decisions. They are never access grants.
package plansaccess

import (
	"slices"
	"strings"
)

type ProjectStream struct {
	ProjectID string   `json:"project_id"`
	Stream    string   `json:"stream"`
	Decides   string   `json:"decides"`
	Informed  []string `json:"informed,omitempty"`
}

func (p *Policy) ProjectStream(project, stream string) (ProjectStream, bool) {
	for _, row := range p.ProjectStreams {
		if row.ProjectID == project && row.Stream == stream {
			return row, true
		}
	}
	return ProjectStream{}, false
}

// Registered projects here are the per-repository IDs in the access policy.
// A project grant on the aggregate projects workspace cannot qualify a stream.
func (p *Policy) registeredStreamProject(id string) bool {
	if !idRE.MatchString(id) || id == "projects" {
		return false
	}
	return slices.ContainsFunc(p.Principals, func(person Principal) bool {
		return slices.Contains(person.Projects, id)
	})
}

// HasStreamKey accepts a bounded stream name within a registered project.
func (p *Policy) HasStreamKey(project, stream string) bool {
	return p.registeredStreamProject(project) && idRE.MatchString(stream)
}

// A disabled registration still exists. Validate the names independently of
// liveness so a retained routing row can never veto disable or retirement.
func (p *Policy) streamRef(ref, project string) (known, active bool) {
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || !idRE.MatchString(id) {
		return false, false
	}
	switch kind {
	case "person":
		person, ok := p.Principal(id)
		if !ok || person.Type != "person" {
			return false, false
		}
		return true, !person.Disabled && slices.Contains(person.Projects, project)
	case "agent":
		agent, ok := p.Agent(id)
		if !ok || agent.ProjectID != "" && agent.ProjectID != project {
			return false, false
		}
		owner, ok := p.Principal(agent.PrincipalID)
		if !ok {
			return false, false
		}
		return true, !agent.Disabled && !owner.Disabled && slices.Contains(owner.Projects, project)
	default:
		return false, false
	}
}

func (p *Policy) validateProjectStreams() error {
	seen := map[[2]string]bool{}
	for _, row := range p.ProjectStreams {
		key := [2]string{row.ProjectID, row.Stream}
		if seen[key] || !p.HasStreamKey(row.ProjectID, row.Stream) {
			return errInvalid
		}
		seen[key] = true
		if known, _ := p.streamRef(row.Decides, row.ProjectID); !known {
			return errInvalid
		}
		informed := map[string]bool{}
		for _, ref := range row.Informed {
			if known, _ := p.streamRef(ref, row.ProjectID); !known || !unique(informed, ref) {
				return errInvalid
			}
		}
	}
	return nil
}

// StreamRecipients resolves only the configured routing and mandatory concierge.
// A queue's current owner is added by its consumer from the actual queue binding.
func (p *Policy) StreamRecipients(row ProjectStream) ([]string, bool) {
	david, ok := p.Principal("david")
	if !ok || david.Type != "person" || david.Role != "owner" || david.Disabled {
		return nil, false
	}
	concierge, ok := p.AgentAt("people/david/concierge")
	if !ok || concierge.PrincipalID != david.ID || concierge.Kind != "concierge" {
		return nil, false
	}
	if _, active := p.streamRef(row.Decides, row.ProjectID); !active {
		return nil, false
	}
	recipients := slices.Clone(row.Informed)
	mandatory := "agent:" + concierge.ID
	if !slices.Contains(recipients, mandatory) {
		recipients = append(recipients, mandatory)
	}
	for _, ref := range recipients {
		if _, active := p.streamRef(ref, row.ProjectID); !active {
			return nil, false
		}
	}
	return recipients, true
}
