package jobs

import (
	"context"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

// scopeProbeEligibleLocked admits a startup probe or a capability continuation
// required by an original queued job. It never turns held inspection into work.
func (s *Service) scopeProbeEligibleLocked() bool {
	if s.closed.Load() || !s.requiresScopes() || s.scopesReady.Load() || s.execution.persistenceError != "" {
		return false
	}
	if s.execution.hold == nil {
		return true
	}
	if s.execution.hold.Phase == "held" {
		return false
	}
	now := time.Now().UnixMilli()
	for _, r := range s.Snapshot() {
		if r.Status == "queued" && now < r.QueueDeadlineMS {
			return true
		}
	}
	return false
}

// startScopeProbeLocked is called with backgroundMu held, serializing Add with
// Close. Admission is rechecked before the actual probe can allocate a unit.
func (s *Service) startScopeProbeLocked() {
	if !s.backgroundActive.Load() || s.scopeProbeActive.Load() {
		return
	}
	s.admission.Lock()
	eligible := s.scopeProbeEligibleLocked()
	s.admission.Unlock()
	if !eligible || !s.scopeProbeActive.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go s.probeScopesBackground()
}

func (s *Service) startScopeProbe() {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	s.startScopeProbeLocked()
}

func (s *Service) acquireScopeProbe() (context.Context, func(), error) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if !s.scopeProbeEligibleLocked() {
		return s.ctx, nil, executionHeldError()
	}
	id, err := process.RandomHex(16)
	if err != nil {
		return s.ctx, nil, err
	}
	call := ExecutionCall{ID: id, Tool: "scope_probe", StartedAtMS: time.Now().UnixMilli()}
	if s.execution.hold != nil {
		for _, r := range s.queuedRecords() {
			if time.Now().UnixMilli() < r.QueueDeadlineMS {
				call.ParentJobID, call.WorkspaceID = r.ID, r.WorkspaceID
				call.PrincipalID, call.SessionID, call.RequestID = r.OwnerPrincipalID, r.OwnerSessionID, r.RequestID
				call.Agent = r.Agent
				break
			}
		}
	}
	root := &executionRoot{call: call, refs: 1, commands: map[uint64]process.TemporaryCommandObservation{}}
	lease := &executionLease{owner: s, root: root}
	s.execution.roots[id] = root
	if s.execution.hold != nil {
		if err := s.saveExecutionHoldLocked(); err != nil {
			delete(s.execution.roots, id)
			return s.ctx, nil, err
		}
	}
	return s.contextForExecution(s.ctx, lease), func() { s.releaseExecution(lease) }, nil
}
