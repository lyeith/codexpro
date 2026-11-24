// Package plansaccess consumes the policy owned by Agentmail. Keep this facade
// free of schema definitions or validation rules; every service must parse the
// same serialized policy through the public shared implementation.
package plansaccess

import shared "github.com/lyeith/agent-mailbox/plansaccess"

type Policy = shared.Policy
type Principal = shared.Principal
type Agent = shared.Agent
type Credential = shared.Credential
type Identity = shared.Identity
type Grant = shared.Grant
type ProjectGrant = shared.ProjectGrant
type RotationGrant = shared.RotationGrant
type ProjectStream = shared.ProjectStream
type Reader = shared.Reader

const (
	maxSize                 = shared.MaxSize
	ImportedFleetV1         = shared.ImportedFleetV1
	StatusCollectorAudience = shared.StatusCollectorAudience
	FleetObserverAudience   = shared.FleetObserverAudience
)

func Load(path string) (*Policy, error)     { return shared.Load(path) }
func parse(data []byte) (*Policy, error)    { return shared.Parse(data) }
func NewReader() *Reader                    { return shared.NewReader() }
func IsNamedToken(raw string) bool          { return shared.IsNamedToken(raw) }
func ValidFleetImportToken(raw string) bool { return shared.ValidFleetImportToken(raw) }
