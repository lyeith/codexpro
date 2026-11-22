package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const defaultQueueTimeoutMS = 60 * 60 * 1000
const maxQueueTimeoutMS = 24 * 60 * 60 * 1000

// insertRecords publishes a complete submission and its input pins atomically.
// The admission mutex serializes both ordinary admission and queued submissions.
func (s *Service) insertRecords(records []Record) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range records {
		payload, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO jobs(id,workspace_id,started_ms,status,origin,payload) VALUES(?,?,?,?,?,?)", r.ID, r.WorkspaceID, r.StartedAtMS, r.Status, r.Origin, payload); err != nil {
			return err
		}
		for _, id := range r.InputJobIDs {
			if _, err = tx.Exec("INSERT INTO job_inputs(job_id,input_id) VALUES(?,?)", r.ID, id); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	for _, r := range records {
		s.records[r.ID] = newEntry(r, r.Status == "running")
	}
	s.mu.Unlock()
	s.notify()
	return nil
}

func validateQueueKey(key string) error {
	if strings.TrimSpace(key) == "" || !utf8.ValidString(key) || strings.IndexByte(key, 0) >= 0 || utf8.RuneCountInString(key) > 160 {
		return badArgs("queue_key must contain 1 to 160 nonblank text characters.")
	}
	return nil
}

func findSubmission(snapshot []Record, identity wire.Identity, workspace, key, digest string, size int) ([]Record, error) {
	existing := []Record{}
	for _, r := range snapshot {
		if r.SubmissionKey == key && r.WorkspaceID == workspace && r.sameCaller(identity) {
			existing = append(existing, r)
		}
	}
	if len(existing) > 0 {
		sort.Slice(existing, func(i, j int) bool { return existing[i].SubmissionIndex < existing[j].SubmissionIndex })
		if len(existing) != size {
			return nil, jobError("job_submission_conflict", "queue_key belongs to another or partially expired submission; do not replay it.")
		}
		for i, r := range existing {
			if r.SubmissionDigest != digest || r.SubmissionSize != size || r.SubmissionIndex != i {
				return nil, jobError("job_submission_conflict", "queue_key was already used with another payload.")
			}
		}
		return existing, nil
	}
	return nil, nil
}

func (s *Service) enqueue(options []startOptions, key string, timeout int64, payload any) ([]Record, error) {
	if err := validateQueueKey(key); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.closed.Load() || s.draining.Load() {
		return nil, jobError("server_restarting", "Server restarting; retry the same queue_key after it is ready.")
	}
	first := options[0]
	snapshot := s.Snapshot()
	total, local, retained := 0, 0, 0
	order := int64(0)
	for _, r := range snapshot {
		if r.SubmissionKey != "" {
			retained++
		}
		if r.QueueOrder > order {
			order = r.QueueOrder
		}
		if r.Status == "queued" {
			total++
			if r.WorkspaceID == first.Workspace.ID {
				local++
			}
		}
	}
	if existing, err := findSubmission(snapshot, first.Scope.Identity, first.Workspace.ID, key, digest, len(options)); err != nil || len(existing) > 0 {
		return existing, err
	}
	if err = s.startupAdmissionError(); err != nil {
		return nil, err
	}
	if retained+len(options) > 1000 {
		return nil, jobError("job_queue_full", "Queue retry receipt capacity is full; no commands were accepted.")
	}
	if total+len(options) > 4*s.cfg.Int("maxJobs", 12) || local+len(options) > 4*s.cfg.Int("maxJobsPerWorkspace", 6) {
		return nil, jobError("job_queue_full", "Pending job queue is full; no commands were accepted.")
	}
	records := make([]Record, 0, len(options))
	now := time.Now().UnixMilli()
	for i, o := range options {
		r, err := s.newRecord(o, true)
		if err != nil {
			return nil, err
		}
		r.SubmissionKey, r.SubmissionDigest, r.SubmissionIndex, r.SubmissionSize = key, digest, i, len(options)
		r.QueueOrder = order + int64(i) + 1
		r.QueueDeadlineMS = now + timeout
		r.SubmissionExpiresMS = r.QueueDeadlineMS + s.cfg.Int64("jobRetentionMs", 86400000)
		r.QueuedCwdAbs = o.CwdAbs
		records = append(records, r)
	}
	additional, err := s.recordPayloadBytes(records)
	if err != nil {
		return nil, err
	}
	if err = s.checkInputPins(options, snapshot, additional); err != nil {
		return nil, err
	}
	if err = s.insertRecords(records); err != nil {
		return nil, err
	}
	return records, nil
}

// Pending jobs pin only existing caller-owned logs, never host/process capacity.
func (s *Service) checkInputPins(options []startOptions, snapshot []Record, additionalPayload int64) error {
	inputs := map[string]bool{}
	for _, r := range snapshot {
		if unfinished(r) || ownershipPending(r) {
			for _, id := range r.InputJobIDs {
				inputs[id] = true
			}
		}
	}
	for _, o := range options {
		for _, id := range o.InputJobIDs {
			r, err := s.lookup(id, o.Workspace.ID, &o.Scope.Identity)
			if err != nil {
				return err
			}
			if r.OutputExpired {
				return jobError("job_output_expired", "Job output expired under retention. Rerun only if needed and safe.")
			}
			inputs[id] = true
		}
	}
	var reserved int64
	for id := range inputs {
		r, ok := s.Get(id, "")
		if !ok {
			continue
		}
		if unfinished(r) {
			reserved += r.OutputLimitBytes*3 + 4096
		} else {
			reserved += s.logBytes(r)
		}
	}
	payloadBytes, err := s.submissionPayloadBytes()
	if err != nil {
		return err
	}
	budget := s.cfg.Int64("maxRetainedJobBytes", 536870912)
	if payloadBytes+additionalPayload > budget {
		return jobError("job_storage_limit", "Queued submission payloads exceed the retained-storage budget. No new commands were accepted.")
	}
	if reserved > budget-payloadBytes-additionalPayload {
		return jobError("job_storage_limit", "Pinned log inputs exceed the retained-output budget. Inspect fewer logs per command.")
	}
	return nil
}

// nextQueued is called under admission. New immediate submissions may not jump
// this oldest eligible command. A full workspace does not block other workspaces.
func (s *Service) nextQueued() *Record {
	active := 0
	queued := []Record{}
	for _, r := range s.Snapshot() {
		if r.Status == "running" || ownershipPending(r) {
			active++
		}
		if r.Status == "queued" {
			queued = append(queued, r)
		}
	}
	if active >= s.cfg.Int("maxActiveCaptures", s.cfg.Int("maxJobs", 12)*2) {
		return nil
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].QueueOrder < queued[j].QueueOrder })
	now := time.Now().UnixMilli()
	for _, r := range queued {
		if r.QueueDeadlineMS > now && s.capacity(r.WorkspaceID) > 0 {
			return &r
		}
	}
	return nil
}

// finishQueued records a terminal outcome before any runner starts.
func (s *Service) finishQueued(r Record, status, reason string) (Record, error) {
	next, err := s.update(r.ID, func(r *Record) bool {
		if r.Status != "queued" {
			return false
		}
		r.Status = status
		r.StopReason = reason
		r.FinishedAtMS = time.Now().UnixMilli()
		r.FinishedAt = wire.Now()
		r.Quiescent = true
		r.QuiescenceScope = "not_launched"
		r.QuiescedAt = wire.Now()
		return true
	})
	if err == nil && !unfinished(next) {
		_, err = s.db.Exec("DELETE FROM job_inputs WHERE job_id=?", r.ID)
		s.finished(next)
	}
	return next, err
}

// dispatchQueued durably consumes just one queue item before any host allocation.
// A crash thereafter follows existing lost/ownership recovery, never requeue.
func (s *Service) dispatchQueued() {
	s.admission.Lock()
	defer s.admission.Unlock()
	if s.closed.Load() || s.draining.Load() {
		return
	}
	for _, r := range s.Snapshot() {
		if r.Status == "queued" && time.Now().UnixMilli() >= r.QueueDeadlineMS {
			_, _ = s.finishQueued(r, "timed_out", "queue_timeout")
		}
	}
	if s.startupAdmissionError() != nil {
		return
	}
	r := s.nextQueued()
	if r == nil {
		return
	}
	var completions int
	if s.db.QueryRow("SELECT COUNT(*) FROM job_completions").Scan(&completions) != nil || completions >= s.cfg.Int("maxPendingJobCompletions", 1000) {
		return
	}
	scope := wire.Scope{PrincipalID: r.OwnerPrincipalID, SessionID: r.OwnerSessionID, RequestID: r.RequestID, ParentActionID: r.ParentActionID, Agent: r.Agent, Identity: r.Identity}
	w, err := s.resolver.Resolve(s.ctx, scope, r.WorkspaceID)
	if err == nil && (w.Root != r.Root || w.ProjectID != r.ProjectID) {
		err = fmt.Errorf("workspace identity changed")
	}
	abs := ""
	if err == nil {
		abs, _, err = s.resolveCwd(w, r.Cwd)
	}
	if err == nil && abs != r.QueuedCwdAbs {
		err = fmt.Errorf("working directory changed")
	}
	if err != nil {
		_, _ = s.finishQueued(*r, "failed", "queued_workspace_changed")
		return
	}
	o := startOptions{Workspace: w, Scope: scope, CwdAbs: abs, Cwd: r.Cwd, Command: r.Command, Origin: r.Origin, TimeoutMS: r.TimeoutMS, OutputLimit: r.OutputLimitBytes, BashSessionID: r.BashSessionID, InputJobIDs: r.InputJobIDs}
	// Mint scope and host-resource registrations only for this admitted execution.
	execution := *r
	err = s.prepareExecution(&execution)
	if err != nil {
		return
	}
	entry := s.entry(r.ID)
	entry.launching.Store(true)
	next, err := s.update(r.ID, func(next *Record) bool {
		if next.Status != "queued" {
			return false
		}
		next.Status = "running"
		next.LaunchState = "prepared"
		next.StartedAt = execution.StartedAt
		next.StartedAtMS = execution.StartedAtMS
		next.DeadlineMS = execution.DeadlineMS
		next.ScopeUnit = execution.ScopeUnit
		next.ResourceClaim = execution.ResourceClaim
		return true
	})
	if err != nil {
		entry.launching.Store(false)
		return
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); _, _ = s.launch(s.ctx, o, next) }()
}

func (s *Service) queueDispatcher() {
	defer s.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		changed := s.changeSignal()
		s.dispatchQueued()
		select {
		case <-s.ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
		}
	}
}

func (s *Service) callQueued(ctx context.Context, scope wire.Scope, w wire.Workspace, args map[string]any, commands []map[string]any) (wire.Result, error) {
	key := wire.String(args, "queue_key", "")
	if err := validateQueueKey(key); err != nil {
		return wire.Result{}, err
	}
	timeout, err := integerArg(args, "queue_timeout_ms", defaultQueueTimeoutMS, 1000, maxQueueTimeoutMS)
	if err != nil {
		return wire.Result{}, err
	}
	payload := []any{commands, wire.String(args, "session_id", ""), timeout}
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256(body)
	s.admission.Lock()
	retry, err := findSubmission(s.Snapshot(), scope.Identity, w.ID, key, hex.EncodeToString(sum[:]), len(commands))
	s.admission.Unlock()
	if err != nil {
		return wire.Result{}, err
	}
	if len(retry) > 0 {
		return s.queuedReceipt(w, key, retry, commands), nil
	}
	session, err := s.session(wire.String(args, "session_id", ""))
	if err != nil {
		return wire.Result{}, err
	}
	options := make([]startOptions, 0, len(commands))
	for _, item := range commands {
		abs, cwd, err := s.resolveCwd(w, wire.String(item, "cwd", "."))
		if err != nil {
			return wire.Result{}, err
		}
		inputs, err := stringsArg(item, "input_job_ids", 16, false)
		if err != nil {
			return wire.Result{}, err
		}
		options = append(options, startOptions{Workspace: w, Scope: scope, CwdAbs: abs, Cwd: cwd, Command: wire.String(item, "command", ""), Origin: "background", TimeoutMS: s.cfg.Int64("jobTimeoutMs", 1500000), OutputLimit: s.cfg.Int64("maxJobOutputBytes", 8388608), BashSessionID: session, InputJobIDs: uniqueExact(inputs)})
	}
	records, err := s.enqueue(options, key, int64(timeout), payload)
	if err != nil {
		return wire.Result{}, err
	}
	return s.queuedReceipt(w, key, records, commands), nil
}

func (s *Service) queuedReceipt(w wire.Workspace, key string, records []Record, commands []map[string]any) wire.Result {
	ids := []string{}
	views := []map[string]any{}
	lines := []string{}
	queued, running := 0, 0
	for _, r := range records {
		ids = append(ids, r.ID)
		view := s.jobView(r, nil, false)
		view["label"] = wire.String(commands[r.SubmissionIndex], "label", r.CommandLabel)
		views = append(views, view)
		lines = append(lines, jobLine(r))
		if r.Status == "queued" {
			queued++
		}
		if r.Status == "running" {
			running++
		}
	}
	return s.success(w, "queue_jobs", "Queue Background Jobs", map[string]any{"workspace_id": w.ID, "root": w.Root, "jobs": views, "job_ids": ids, "running_count": running, "queued_count": queued, "queue_key": key}, "# Accepted jobs\n\n"+strings.Join(lines, "\n")+"\n\nCollect or cancel these same IDs; do not resubmit with a new key.")
}
