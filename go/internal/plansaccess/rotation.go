package plansaccess

import "slices"

// RotationGrant is the shared policy record written by Plans Access for
// Agentmail's rotation protocol. CodexPro validates the record but does not use
// it for authentication, project access or ordinary lifecycle authority.
// Empty ProjectID is an exact value for an existing standing registration.
type RotationGrant struct {
	Target          string `json:"target"`
	TargetPrincipal string `json:"target_principal"`
	Mailbox         string `json:"mailbox"`
	ProjectID       string `json:"project_id,omitempty"`
}

func rotationGrant(target Agent) RotationGrant {
	return RotationGrant{
		Target: target.ID, TargetPrincipal: target.PrincipalID,
		Mailbox: target.Address, ProjectID: target.ProjectID,
	}
}

// Keep these shared-file validation rules aligned with Agentmail. Loading a
// valid grant must neither widen CodexPro's authority nor hide a stale binding.
func (p *Policy) validRotationGrant(owner Principal, grant RotationGrant) bool {
	target, found := p.Agent(grant.Target)
	targetOwner, owned := p.Principal(grant.TargetPrincipal)
	if owner.Type != "person" || !found || !owned || grant != rotationGrant(target) ||
		(grant.ProjectID != "" && !slices.Contains(owner.Projects, grant.ProjectID)) {
		return false
	}
	if targetOwner.Type == "person" {
		return targetOwner.ID == owner.ID && (target.Kind == "concierge" || target.Kind == "crew")
	}
	return targetOwner.Type == "system" &&
		(target.Kind == "coordinator" || target.Kind == "supervisor" || target.Kind == "leaf")
}

func (p *Policy) validateRotationGrants() error {
	for _, owner := range p.Principals {
		seen := map[string]bool{}
		for _, grant := range owner.RotationGrants {
			if !unique(seen, grant.Target) || !p.validRotationGrant(owner, grant) {
				return errInvalid
			}
		}
	}
	return nil
}
