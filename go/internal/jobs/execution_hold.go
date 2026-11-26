package jobs

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// ExecutionUnverifiedOwner preserves an owner for which the original execution
// contract supplies no whole-owner proof. A completed direct child cannot clear it.
type ExecutionUnverifiedOwner struct {
	Kind            string `json:"kind"`
	PID             int    `json:"pid,omitempty"`
	ProcessIdentity string `json:"process_identity,omitempty"`
	BootID          string `json:"boot_id,omitempty"`
	Unit            string `json:"unit,omitempty"`
	Reason          string `json:"reason"`
}

// ExecutionCall identifies one admitted call and its actual continuations.
// Display labels and request IDs are evidence, never admission capabilities.
type ExecutionCall struct {
	ID                string                                `json:"id"`
	Tool              string                                `json:"tool"`
	PrincipalID       string                                `json:"principal_id,omitempty"`
	SessionID         string                                `json:"session_id,omitempty"`
	RequestID         string                                `json:"request_id,omitempty"`
	WorkspaceID       string                                `json:"workspace_id,omitempty"`
	Agent             string                                `json:"agent,omitempty"`
	StartedAtMS       int64                                 `json:"started_at_ms"`
	References        int                                   `json:"references"`
	State             string                                `json:"state"`
	TemporaryCommands []process.TemporaryCommandObservation `json:"temporary_commands"`
	ParentJobID       string                                `json:"parent_job_id,omitempty"`
	UnverifiedOwners  []ExecutionUnverifiedOwner            `json:"unverified_owners"`
}

// ExecutionJob projects existing durable ownership without command text or logs.
type ExecutionJob struct {
	ID                string                 `json:"id"`
	WorkspaceID       string                 `json:"workspace_id"`
	Status            string                 `json:"status"`
	LaunchState       string                 `json:"launch_state"`
	Launching         bool                   `json:"launching"`
	LauncherState     string                 `json:"launcher_state"`
	LauncherQuiescent bool                   `json:"launcher_quiescent"`
	Quiescent         bool                   `json:"quiescent"`
	QuiescenceScope   string                 `json:"quiescence_scope,omitempty"`
	ScopeUnit         string                 `json:"scope_unit,omitempty"`
	ScopeIdentity     *process.ScopeIdentity `json:"scope_identity,omitempty"`
	ResourceClaim     *process.ResourceClaim `json:"resource_claim,omitempty"`
	ObservationError  string                 `json:"observation_error,omitempty"`
	OutputExpired     bool                   `json:"output_expired"`
}

// ExecutionHoldStatus is a local owner snapshot. Command completion, dispatch
// closure and the owner's existing quiescence publication remain distinct.
type ExecutionHoldStatus struct {
	InstanceID       string          `json:"instance_id"`
	PID              int             `json:"pid"`
	ProcessIdentity  string          `json:"process_identity,omitempty"`
	BootID           string          `json:"boot_id,omitempty"`
	HoldInstanceID   string          `json:"hold_instance_id,omitempty"`
	HoldID           string          `json:"hold_id,omitempty"`
	RequestKey       string          `json:"request_key,omitempty"`
	RequestedAtMS    int64           `json:"requested_at_ms,omitempty"`
	HeldAtMS         int64           `json:"held_at_ms,omitempty"`
	AdmissionHeld    bool            `json:"admission_held"`
	DispatchHeld     bool            `json:"dispatch_held"`
	PendingCalls     []ExecutionCall `json:"pending_calls"`
	SettledCalls     []ExecutionCall `json:"settled_calls"`
	OutstandingJobs  []ExecutionJob  `json:"outstanding_jobs"`
	AcceptedJobs     []ExecutionJob  `json:"accepted_jobs"`
	PersistenceError string          `json:"persistence_error,omitempty"`
}

type executionHoldRecord struct {
	Version       int             `json:"version"`
	HoldID        string          `json:"hold_id"`
	RequestKey    string          `json:"request_key"`
	InstanceID    string          `json:"instance_id"`
	RequestedAtMS int64           `json:"requested_at_ms"`
	HeldAtMS      int64           `json:"held_at_ms,omitempty"`
	Phase         string          `json:"phase"`
	PendingCalls  []ExecutionCall `json:"pending_calls"`
	JobIDs        []string        `json:"job_ids"`
	SettledCalls  []ExecutionCall `json:"settled_calls"`
}

type executionAdmission struct {
	instance         string
	pid              int
	processIdentity  string
	bootID           string
	hold             *executionHoldRecord
	roots            map[string]*executionRoot
	recovered        map[string]ExecutionCall
	persistenceError string
}

type executionRoot struct {
	call     ExecutionCall
	refs     int
	commands map[uint64]process.TemporaryCommandObservation
}

type executionLease struct {
	owner    *Service
	root     *executionRoot
	released bool // guarded by owner.admission
}

type executionLeaseKey struct{}

func IsExecutionHeld(err error) bool {
	var problem *wire.Error
	return errors.As(err, &problem) && problem.Code == "execution_held"
}

func executionHeldError() error {
	return &wire.Error{Code: "execution_held", Message: "Execution admission is held for maintenance. Inspect existing results; no new work was accepted.",
		Data: map[string]any{"retry_unchanged": false, "new_work_accepted": false}}
}

func invalidExecutionLease() error {
	return wire.Errorf("execution_admission_invalid", "The original execution admission is absent, released, or belongs to another server.")
}

func validHoldKey(key string) bool {
	return strings.TrimSpace(key) != "" && utf8.ValidString(key) &&
		strings.IndexByte(key, 0) < 0 && utf8.RuneCountInString(key) <= 160
}

func validExecutionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Service) loadExecutionHold() error {
	instance, err := process.RandomHex(16)
	if err != nil {
		return err
	}
	boot, _ := process.BootID()
	pid := os.Getpid()
	s.execution = executionAdmission{instance: instance, pid: pid, processIdentity: process.ProcessIdentity(pid), bootID: boot,
		roots: map[string]*executionRoot{}, recovered: map[string]ExecutionCall{}}
	var body []byte
	err = s.db.QueryRow("SELECT payload FROM execution_hold WHERE id=1").Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved executionHoldRecord
	if err = json.Unmarshal(body, &saved); err != nil || saved.Version != 1 ||
		!validExecutionID(saved.HoldID) || !validExecutionID(saved.InstanceID) || !validHoldKey(saved.RequestKey) ||
		saved.RequestedAtMS <= 0 || saved.Phase != "draining" && saved.Phase != "held" ||
		saved.Phase == "held" && saved.HeldAtMS < saved.RequestedAtMS {
		return errors.New("invalid persisted execution hold; admission remains unavailable")
	}
	seen := map[string]bool{}
	for _, call := range saved.PendingCalls {
		if !validExecutionID(call.ID) || call.Tool == "" || call.StartedAtMS <= 0 || call.References < 0 || seen[call.ID] {
			return errors.New("invalid persisted execution admission identity")
		}
		seen[call.ID] = true
		call.References, call.State = 0, "unknown_after_restart"
		s.execution.recovered[call.ID] = call
	}
	jobIDs := map[string]bool{}
	for _, id := range saved.JobIDs {
		if id == "" || jobIDs[id] {
			return errors.New("invalid persisted execution hold job identity")
		}
		jobIDs[id] = true
	}
	s.execution.hold = &saved
	return nil
}

func (s *Service) executionLeaseLocked(ctx context.Context) (*executionLease, bool, error) {
	value := ctx.Value(executionLeaseKey{})
	if value == nil {
		return nil, false, nil
	}
	lease, ok := value.(*executionLease)
	if !ok || lease == nil || lease.owner != s || lease.released || lease.root == nil ||
		s.execution.roots[lease.root.call.ID] != lease.root || lease.root.refs <= 0 {
		return nil, true, invalidExecutionLease()
	}
	return lease, true, nil
}

func (s *Service) contextForExecution(ctx context.Context, lease *executionLease) context.Context {
	ctx = context.WithValue(ctx, executionLeaseKey{}, lease)
	return process.WithTemporaryCommandObserver(ctx, func(observation process.TemporaryCommandObservation) {
		s.observeTemporaryCommand(lease.root, observation)
	})
}

// AcquireExecution serializes fresh ingress with durable jobs acceptance.
// A live private capability may retain its original admission after ingress closes.
func (s *Service) AcquireExecution(ctx context.Context, scope wire.Scope, tool, workspaceID string) (context.Context, func(), error) {
	s.admission.Lock()
	lease, present, err := s.executionLeaseLocked(ctx)
	if err == nil && s.closed.Load() {
		err = jobError("server_restarting", "Server restarting; no new execution was accepted.")
	}
	if err == nil && !present {
		if s.execution.hold != nil || s.execution.persistenceError != "" {
			err = executionHeldError()
		} else {
			var id string
			id, err = process.RandomHex(16)
			if err == nil {
				root := &executionRoot{call: ExecutionCall{ID: id, Tool: tool,
					PrincipalID: scope.PrincipalID, SessionID: scope.SessionID, RequestID: scope.RequestID,
					WorkspaceID: workspaceID, Agent: scope.Agent, StartedAtMS: time.Now().UnixMilli()},
					commands: map[uint64]process.TemporaryCommandObservation{}}
				s.execution.roots[id] = root
				lease = &executionLease{owner: s, root: root}
			}
		}
	} else if err == nil {
		lease = &executionLease{owner: s, root: lease.root}
	}
	if err == nil {
		lease.root.refs++
	}
	s.admission.Unlock()
	if err != nil {
		return ctx, nil, err
	}
	return s.contextForExecution(ctx, lease), func() { s.releaseExecution(lease) }, nil
}

// RetainExecution never upgrades a fresh or released context into permission.
// Call it before handing an admitted continuation to an unjoined goroutine.
func (s *Service) RetainExecution(ctx context.Context) (context.Context, func(), error) {
	s.admission.Lock()
	source, present, err := s.executionLeaseLocked(ctx)
	if err == nil && !present {
		err = invalidExecutionLease()
	}
	var lease *executionLease
	if err == nil {
		lease = &executionLease{owner: s, root: source.root}
		lease.root.refs++
	}
	s.admission.Unlock()
	if err != nil {
		return ctx, nil, err
	}
	return s.contextForExecution(ctx, lease), func() { s.releaseExecution(lease) }, nil
}

func snapshotExecutionRoot(root *executionRoot) ExecutionCall {
	call := root.call
	call.UnverifiedOwners = append([]ExecutionUnverifiedOwner{}, root.call.UnverifiedOwners...)
	call.References = root.refs
	call.State = "active"
	if root.refs == 0 {
		call.State = "owner_pending"
	}
	call.TemporaryCommands = make([]process.TemporaryCommandObservation, 0, len(root.commands))
	for _, observation := range root.commands {
		if observation.ExitCode != nil {
			value := *observation.ExitCode
			observation.ExitCode = &value
		}
		call.TemporaryCommands = append(call.TemporaryCommands, observation)
	}
	sort.Slice(call.TemporaryCommands, func(i, j int) bool {
		return call.TemporaryCommands[i].CommandID < call.TemporaryCommands[j].CommandID
	})
	return call
}

func executionRootSettled(root *executionRoot) bool {
	if root.refs != 0 || len(root.call.UnverifiedOwners) != 0 {
		return false
	}
	for _, observation := range root.commands {
		if observation.Phase != "finished" || !observation.QuiescenceKnown || !observation.Quiescent {
			return false
		}
	}
	return true
}

func (s *Service) finishExecutionRootLocked(root *executionRoot) {
	if !executionRootSettled(root) {
		return
	}
	if s.execution.hold != nil {
		call := snapshotExecutionRoot(root)
		call.State = "settled"
		s.execution.hold.SettledCalls = append(s.execution.hold.SettledCalls, call)
	}
	delete(s.execution.roots, root.call.ID)
}

func (s *Service) releaseExecution(lease *executionLease) {
	s.admission.Lock()
	if !lease.released {
		lease.released = true
		if s.execution.roots[lease.root.call.ID] == lease.root {
			lease.root.refs--
			s.finishExecutionRootLocked(lease.root)
			if s.execution.hold != nil {
				if s.saveExecutionHoldLocked() == nil {
					_ = s.advanceExecutionHoldLocked()
				}
			}
		}
	}
	s.admission.Unlock()
	s.notify()
}

func (s *Service) observeTemporaryCommand(root *executionRoot, observation process.TemporaryCommandObservation) {
	s.admission.Lock()
	if s.execution.roots[root.call.ID] != root {
		// A late command outside its retained continuation is uncertainty, never
		// evidence that the original call had no owned work.
		call := root.call
		call.State = "late_temporary_command"
		call.TemporaryCommands = []process.TemporaryCommandObservation{observation}
		s.execution.recovered[call.ID] = call
	} else {
		root.commands[observation.CommandID] = observation
		s.finishExecutionRootLocked(root)
	}
	if s.execution.hold != nil {
		if s.saveExecutionHoldLocked() == nil {
			_ = s.advanceExecutionHoldLocked()
		}
	}
	s.admission.Unlock()
	s.notify()
}

func (s *Service) pendingExecutionCallsLocked() []ExecutionCall {
	calls := make([]ExecutionCall, 0, len(s.execution.roots)+len(s.execution.recovered))
	for _, root := range s.execution.roots {
		calls = append(calls, snapshotExecutionRoot(root))
	}
	for _, call := range s.execution.recovered {
		// JSON cloning keeps snapshots detached from retained observer evidence.
		body, _ := json.Marshal(call)
		var copy ExecutionCall
		_ = json.Unmarshal(body, &copy)
		calls = append(calls, copy)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].ID < calls[j].ID })
	return calls
}

func (s *Service) outstandingExecutionJobsLocked() []ExecutionJob {
	jobs := []ExecutionJob{}
	seen := map[string]bool{}
	for _, r := range s.Snapshot() {
		seen[r.ID] = true
		entry := s.entry(r.ID)
		launching := entry != nil && entry.launching.Load()
		job := s.projectExecutionJob(r, launching)
		if executionJobTerminal(r.Status) && r.Quiescent && !launching && job.LauncherQuiescent {
			continue
		}
		jobs = append(jobs, job)
	}
	if hold := s.execution.hold; hold != nil {
		for _, id := range hold.JobIDs {
			if !seen[id] {
				jobs = append(jobs, missingExecutionJob(id))
			}
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return jobs
}

func (s *Service) executionStatusLocked() ExecutionHoldStatus {
	status := ExecutionHoldStatus{InstanceID: s.execution.instance, PID: s.execution.pid,
		ProcessIdentity: s.execution.processIdentity, BootID: s.execution.bootID, PendingCalls: s.pendingExecutionCallsLocked(),
		SettledCalls: []ExecutionCall{}, OutstandingJobs: s.outstandingExecutionJobsLocked(), AcceptedJobs: s.acceptedExecutionJobsLocked(),
		PersistenceError: s.execution.persistenceError}
	if hold := s.execution.hold; hold != nil {
		status.HoldID, status.RequestKey, status.HoldInstanceID = hold.HoldID, hold.RequestKey, hold.InstanceID
		status.RequestedAtMS, status.HeldAtMS = hold.RequestedAtMS, hold.HeldAtMS
		status.AdmissionHeld, status.DispatchHeld = true, hold.Phase == "held"
		body, _ := json.Marshal(hold.SettledCalls)
		_ = json.Unmarshal(body, &status.SettledCalls)
		if status.SettledCalls == nil {
			status.SettledCalls = []ExecutionCall{}
		}
	}
	return status
}

func (s *Service) ExecutionStatus() ExecutionHoldStatus {
	s.admission.Lock()
	defer s.admission.Unlock()
	return s.executionStatusLocked()
}

func (s *Service) saveExecutionHoldLocked() error {
	hold := s.execution.hold
	if hold == nil {
		return nil
	}
	s.rememberExecutionJobsLocked()
	hold.PendingCalls = s.pendingExecutionCallsLocked()
	body, err := json.Marshal(hold)
	if err == nil {
		_, err = s.db.Exec("INSERT INTO execution_hold(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", body)
	}
	if err != nil {
		s.execution.persistenceError = "Execution hold persistence failed; completion is unavailable."
		return fmt.Errorf("persist execution hold: %w", err)
	}
	s.execution.persistenceError = ""
	return nil
}

func (s *Service) advanceExecutionHoldLocked() error {
	hold := s.execution.hold
	if hold == nil || hold.Phase == "held" || s.execution.persistenceError != "" ||
		len(s.execution.roots) != 0 || len(s.execution.recovered) != 0 ||
		len(s.outstandingExecutionJobsLocked()) != 0 {
		return nil
	}
	hold.Phase, hold.HeldAtMS = "held", time.Now().UnixMilli()
	if err := s.saveExecutionHoldLocked(); err != nil {
		// The in-memory dispatch latch stays closed, but no complete or durable
		// acknowledgment may be inferred while persistence is unresolved.
		return err
	}
	return nil
}

func (s *Service) BeginExecutionHold(requestKey string) (ExecutionHoldStatus, error) {
	if !validHoldKey(requestKey) {
		return ExecutionHoldStatus{}, badArgs("A nonblank maintenance request key of at most 160 characters is required.")
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.closed.Load() {
		return s.executionStatusLocked(), jobError("server_restarting", "Server is restarting.")
	}
	if hold := s.execution.hold; hold != nil {
		if hold.RequestKey != requestKey {
			return s.executionStatusLocked(), wire.Errorf("execution_hold_conflict", "Another maintenance hold is already active.")
		}
	} else {
		id, err := process.RandomHex(16)
		if err != nil {
			return s.executionStatusLocked(), err
		}
		s.execution.hold = &executionHoldRecord{Version: 1, HoldID: id, RequestKey: requestKey,
			InstanceID: s.execution.instance, RequestedAtMS: time.Now().UnixMilli(), Phase: "draining",
			SettledCalls: []ExecutionCall{}}
	}
	if err := s.saveExecutionHoldLocked(); err != nil {
		return s.executionStatusLocked(), err
	}
	err := s.advanceExecutionHoldLocked()
	s.notify()
	return s.executionStatusLocked(), err
}

func (s *Service) HoldExecutionDispatch(holdID string) (ExecutionHoldStatus, error) {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.execution.hold == nil || s.execution.hold.HoldID != holdID {
		return s.executionStatusLocked(), wire.Errorf("execution_hold_conflict", "The original maintenance hold does not match.")
	}
	if err := s.saveExecutionHoldLocked(); err != nil {
		return s.executionStatusLocked(), err
	}
	err := s.advanceExecutionHoldLocked()
	s.notify()
	return s.executionStatusLocked(), err
}

// Resume releases local ingress using the original hold token after the local
// continuation/queue owner has settled. It is not a fresh SSD or ROOT barrier
// certificate. Unknown prior-instance calls are never cleared by this operation.
func (s *Service) ResumeExecution(holdID string) (ExecutionHoldStatus, error) {
	s.admission.Lock()
	if s.execution.hold == nil || s.execution.hold.HoldID != holdID {
		status := s.executionStatusLocked()
		s.admission.Unlock()
		return status, wire.Errorf("execution_hold_conflict", "The original maintenance hold does not match.")
	}
	status := s.executionStatusLocked()
	if !status.DispatchHeld || len(status.PendingCalls) != 0 || len(status.OutstandingJobs) != 0 || status.PersistenceError != "" {
		s.admission.Unlock()
		return status, wire.Errorf("execution_hold_incomplete", "The original accepted work has not reached verified local quiescence.")
	}
	if _, err := s.db.Exec("DELETE FROM execution_hold WHERE id=1"); err != nil {
		s.admission.Unlock()
		return status, err
	}
	s.execution.hold = nil
	status = s.executionStatusLocked()
	s.admission.Unlock()
	s.startScopeProbe()
	s.notify()
	return status, nil
}

// executionAllowedLocked is checked before newRecord or owner preparation.
// Existing accepted rows bypass this fresh-admission check when dispatching.
func (s *Service) executionAllowedLocked(lease *executionLease) error {
	if lease != nil {
		if lease.owner != s || lease.released || lease.root == nil ||
			s.execution.roots[lease.root.call.ID] != lease.root || lease.root.refs <= 0 {
			return invalidExecutionLease()
		}
		return nil
	}
	if s.execution.hold != nil || s.execution.persistenceError != "" {
		return executionHeldError()
	}
	return nil
}

func executionLeaseFrom(ctx context.Context) *executionLease {
	lease, _ := ctx.Value(executionLeaseKey{}).(*executionLease)
	return lease
}

// RecordUnverifiedExecutionOwner attaches observed original ownership to a live
// private admission. It never signals or reassigns that owner. Unknown remains
// an explicit hold blocker after the tool itself returns.
func (s *Service) RecordUnverifiedExecutionOwner(ctx context.Context, owner ExecutionUnverifiedOwner) error {
	if owner.Kind == "" || owner.Reason == "" || owner.PID <= 0 && owner.Unit == "" {
		return badArgs("An observed original owner and uncertainty reason are required.")
	}
	s.admission.Lock()
	defer s.admission.Unlock()
	lease, present, err := s.executionLeaseLocked(ctx)
	if err != nil {
		return err
	}
	if !present {
		return invalidExecutionLease()
	}
	lease.root.call.UnverifiedOwners = append(lease.root.call.UnverifiedOwners, owner)
	if s.execution.hold != nil {
		return s.saveExecutionHoldLocked()
	}
	return nil
}
