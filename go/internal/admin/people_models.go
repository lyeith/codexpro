package admin

import (
	"context"
	"encoding/json"
)

// PeopleActor is populated only from the outer authenticated request. It is not
// an HTTP input. The sole writer rechecks this binding against its current policy.
type PeopleActor struct {
	PrincipalID  string `json:"principal_id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
}

type PeoplePrincipal struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Role           string   `json:"role"`
	Projects       []string `json:"projects"`
	Disabled       bool     `json:"disabled"`
	HasDelegations bool     `json:"has_delegations"`
}

type PeopleAgent struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	Kind        string `json:"kind"`
	Address     string `json:"address"`
	ProjectID   string `json:"project_id"`
	Disabled    bool   `json:"disabled"`
}

type PeopleDevice struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	AgentID     string `json:"agent_id"`
	Device      string `json:"device"`
	Audience    string `json:"audience"`
	Revoked     bool   `json:"revoked"`
	CanRevoke   bool   `json:"can_revoke"`
}

type PeopleRoster struct {
	Principals []PeoplePrincipal `json:"principals"`
	Agents     []PeopleAgent     `json:"agents"`
	Devices    []PeopleDevice    `json:"devices"`
}

type PeopleSelf struct {
	PrincipalID  string `json:"principal_id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
	Role         string `json:"role"`
}

type PeopleRequest struct {
	Version   int             `json:"version"`
	Actor     PeopleActor     `json:"actor"`
	Operation string          `json:"operation"`
	Action    json.RawMessage `json:"action,omitempty"`
}

type PeopleError struct {
	Code    string `json:"code"`
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

type PeopleResponse struct {
	OK      bool          `json:"ok"`
	Roster  *PeopleRoster `json:"roster,omitempty"`
	Self    *PeopleSelf   `json:"self,omitempty"`
	Outcome string        `json:"outcome,omitempty"`
	Error   *PeopleError  `json:"error,omitempty"`
}

// PeopleOwner is the typed local plans-access boundary, not an arbitrary command
// runner. The actual owner retains validation, authorization and the writer lock.
type PeopleOwner interface {
	Execute(context.Context, PeopleRequest) PeopleResponse
}

func peopleFailure(code string) PeopleResponse {
	outcome := "refused"
	if code == "uncertain" {
		outcome = "uncertain"
	}
	return PeopleResponse{Error: &PeopleError{Code: code, Outcome: outcome}}
}
