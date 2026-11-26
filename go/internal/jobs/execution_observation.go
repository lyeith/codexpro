package jobs

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// ExecutionOwnerObservation keeps command outcome, the jobs owner's durable
// publication and fresh read-only owner evidence as separate facts.
type ExecutionOwnerObservation struct {
	JobID                  string                       `json:"job_id"`
	WorkspaceID            string                       `json:"workspace_id"`
	CommandStatus          string                       `json:"command_status"`
	CommandTerminal        bool                         `json:"command_terminal"`
	PublishedQuiescent     bool                         `json:"published_quiescent"`
	Scope                  string                       `json:"scope"`
	Quiescent              bool                         `json:"quiescent"`
	OwnerQuiescentVerified bool                         `json:"owner_quiescent_verified"`
	Resource               *process.ResourceObservation `json:"resource,omitempty"`
	Error                  string                       `json:"error,omitempty"`
}

type ExecutionHoldObservation struct {
	Status       ExecutionHoldStatus         `json:"status"`
	Owners       []ExecutionOwnerObservation `json:"owners"`
	Complete     bool                        `json:"complete"`
	Scope        string                      `json:"scope"`
	ObservedAtMS int64                       `json:"observed_at_ms"`
}

// refreshTemporaryOwners observes only original finished commands whose actual
// continuations have returned. It cannot clear a foreign, restarted or unknown
// uncontained owner and cannot alter a command's original outcome.
func (s *Service) refreshTemporaryOwners(ctx context.Context, status ExecutionHoldStatus) {
	type refresh struct {
		callID string
		before process.TemporaryCommandObservation
		after  process.TemporaryCommandObservation
	}
	updates := []refresh{}
	for _, call := range status.PendingCalls {
		if call.References != 0 || call.State != "owner_pending" {
			continue
		}
		for _, command := range call.TemporaryCommands {
			if ctx.Err() != nil {
				break
			}
			if command.Phase != process.TemporaryCommandFinished || command.QuiescenceKnown && command.Quiescent {
				continue
			}
			current, _ := process.ObserveTemporaryCommandQuiescence(command)
			updates = append(updates, refresh{call.ID, command, current})
		}
	}
	if len(updates) == 0 {
		return
	}
	s.admission.Lock()
	changed := false
	if s.execution.hold != nil && s.execution.hold.HoldID == status.HoldID && s.execution.instance == status.InstanceID {
		for _, update := range updates {
			root := s.execution.roots[update.callID]
			if root == nil || root.refs != 0 {
				continue
			}
			current, ok := root.commands[update.before.CommandID]
			if !ok || !reflect.DeepEqual(current, update.before) {
				continue
			}
			root.commands[update.before.CommandID] = update.after
			s.finishExecutionRootLocked(root)
			changed = true
		}
		if changed && s.saveExecutionHoldLocked() == nil {
			_ = s.advanceExecutionHoldLocked()
		}
	}
	s.admission.Unlock()
	if changed {
		s.notify()
	}
}

func observeExecutionJob(ctx context.Context, job ExecutionJob) ExecutionOwnerObservation {
	out := ExecutionOwnerObservation{JobID: job.ID, WorkspaceID: job.WorkspaceID,
		CommandStatus: job.Status, CommandTerminal: executionJobTerminal(job.Status),
		PublishedQuiescent: job.Quiescent, Scope: job.QuiescenceScope}
	if job.ObservationError != "" {
		out.Error = job.ObservationError
		return out
	}
	if ctx.Err() != nil {
		out.Error = "Owner observation deadline ended before this original owner was verified."
		return out
	}
	if job.ResourceClaim != nil {
		out.Scope = "ssd_owner"
		unit := job.ScopeUnit + ".scope"
		observed, err := process.ObserveResourceOwnership(ctx, *job.ResourceClaim, unit, job.ScopeIdentity)
		if err != nil {
			out.Error = "The original resource owner could not be verified through its saved control locator."
			return out
		}
		out.Resource = &observed
		out.Quiescent = observed.Quiescent
		out.OwnerQuiescentVerified = observed.OwnerQuiescentVerified && !observed.Alive && !observed.Pending &&
			observed.Quiescent && (observed.AllocationClaim == "settled" || observed.AllocationClaim == "revoked")
		return out
	}
	if job.ScopeUnit != "" {
		out.Scope = "systemd_scope"
		boot, err := process.BootID()
		if err != nil || !process.ValidScopeIdentity(job.ScopeIdentity, job.ScopeUnit+".scope") || boot != job.ScopeIdentity.BootID {
			out.Error = "The original systemd owner boot and invocation remain unverified."
			return out
		}
		state := process.InspectScope(ctx, job.ScopeIdentity)
		out.Quiescent = state == process.ScopeQuiescent
		out.OwnerQuiescentVerified = out.Quiescent
		if state != process.ScopeActive && state != process.ScopeQuiescent {
			out.Error = "The original systemd owner identity or whole-owner state is unknown."
		}
		return out
	}
	// Existing unscoped jobs already have a durable supervisor group result.
	// This remains process-group proof, never SSD or whole-host proof.
	if out.CommandTerminal && job.Quiescent && (job.QuiescenceScope == "process_group" || job.QuiescenceScope == "not_launched") {
		out.Quiescent, out.OwnerQuiescentVerified = true, true
	} else if out.CommandTerminal {
		out.Error = "The original command ended without verified owner quiescence."
	}
	return out
}

// ObserveExecutionHold performs bounded existing read-contract observations. It
// never settles, stops, cancels, allocates, requeues, or changes accepted Job rows.
// Complete covers this instance's tracked original execution only; ROOT still
// owns the independent producer barrier and any live owner-unit operation.
func (s *Service) ObserveExecutionHold(ctx context.Context) (ExecutionHoldObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	initial := s.ExecutionStatus()
	out := ExecutionHoldObservation{Status: initial, Owners: []ExecutionOwnerObservation{},
		Scope: "codexpro_tracked_execution"}
	if !initial.AdmissionHeld {
		return out, wire.Errorf("execution_hold_conflict", "No execution hold is active.")
	}
	s.refreshTemporaryOwners(ctx, initial)
	status := s.ExecutionStatus()
	if status.HoldID != initial.HoldID || status.InstanceID != initial.InstanceID {
		out.Status = status
		return out, wire.Errorf("execution_hold_conflict", "The original maintenance hold changed during observation.")
	}
	bindings := make(map[string]ExecutionJob, len(status.AcceptedJobs))
	verified := make(map[string]bool, len(status.AcceptedJobs))
	for _, job := range status.AcceptedJobs {
		bindings[job.ID] = job
		owner := observeExecutionJob(ctx, job)
		out.Owners = append(out.Owners, owner)
		verified[job.ID] = owner.CommandTerminal && owner.PublishedQuiescent && owner.OwnerQuiescentVerified &&
			owner.Quiescent && owner.Error == "" && !job.Launching
	}
	out.Status = s.ExecutionStatus()
	out.ObservedAtMS = time.Now().UnixMilli()
	sort.Slice(out.Owners, func(i, j int) bool { return out.Owners[i].JobID < out.Owners[j].JobID })
	if out.Status.HoldID != initial.HoldID || out.Status.InstanceID != initial.InstanceID {
		return out, wire.Errorf("execution_hold_conflict", "The original maintenance hold changed during observation.")
	}
	out.Complete = out.Status.AdmissionHeld && out.Status.DispatchHeld && out.Status.PersistenceError == "" &&
		len(out.Status.PendingCalls) == 0 && len(out.Status.OutstandingJobs) == 0
	for _, job := range out.Status.AcceptedJobs {
		before, ok := bindings[job.ID]
		if !ok || !verified[job.ID] || !reflect.DeepEqual(before, job) {
			out.Complete = false
		}
	}
	return out, nil
}
