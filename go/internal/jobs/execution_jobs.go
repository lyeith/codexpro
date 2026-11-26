package jobs

import (
	"encoding/json"
	"sort"
)

func executionJobTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "timed_out", "stopped":
		return true
	}
	return false
}

func (s *Service) projectExecutionJob(r Record, launching bool) ExecutionJob {
	launcherState, launcherQuiescent := s.executionLauncherState(r, launching)
	return ExecutionJob{ID: r.ID, WorkspaceID: r.WorkspaceID, Status: r.Status,
		LaunchState: r.LaunchState, Launching: launching, Quiescent: r.Quiescent,
		LauncherState: launcherState, LauncherQuiescent: launcherQuiescent,
		QuiescenceScope: r.QuiescenceScope, ScopeUnit: r.ScopeUnit,
		ScopeIdentity: r.ScopeIdentity, ResourceClaim: r.ResourceClaim,
		OutputExpired: r.OutputExpired}
}

func missingExecutionJob(id string) ExecutionJob {
	return ExecutionJob{ID: id, Status: "unknown", LaunchState: "unknown",
		ObservationError: "The original accepted job record is unavailable."}
}

func (s *Service) acceptedExecutionJobsLocked() []ExecutionJob {
	jobs := []ExecutionJob{}
	if s.execution.hold == nil {
		return jobs
	}
	for _, id := range s.execution.hold.JobIDs {
		r, ok := s.Get(id, "")
		if !ok {
			jobs = append(jobs, missingExecutionJob(id))
			continue
		}
		entry := s.entry(id)
		jobs = append(jobs, s.projectExecutionJob(r, entry != nil && entry.launching.Load()))
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })
	return jobs
}

func appendExecutionJobIDs(ids []string, additional []string) []string {
	out := append([]string{}, ids...)
	seen := make(map[string]bool, len(out))
	for _, id := range out {
		seen[id] = true
	}
	for _, id := range additional {
		if !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) rememberExecutionJobsLocked() {
	if s.execution.hold == nil {
		return
	}
	ids := []string{}
	for _, job := range s.outstandingExecutionJobsLocked() {
		ids = append(ids, job.ID)
	}
	s.execution.hold.JobIDs = appendExecutionJobIDs(s.execution.hold.JobIDs, ids)
}

// executionHoldForAdmissionLocked returns a detached marker for the SAME jobs
// transaction. Original admitted calls may add rows after ingress closes; their
// receipt/log pins and admission evidence become durable with those original rows.
func (s *Service) executionHoldForAdmissionLocked(records []Record) (*executionHoldRecord, []byte, error) {
	if s.execution.hold == nil {
		return nil, nil, nil
	}
	next := *s.execution.hold
	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	next.JobIDs = appendExecutionJobIDs(next.JobIDs, ids)
	next.PendingCalls = s.pendingExecutionCallsLocked()
	body, err := json.Marshal(next)
	return &next, body, err
}

// protectExecutionHoldJobsLocked reuses the existing retention exclusion. It
// preserves original records, logs, launch specs and inputs until explicit resume.
func (s *Service) protectExecutionHoldJobsLocked(protected map[string]bool) {
	if s.execution.hold == nil {
		return
	}
	for _, id := range s.execution.hold.JobIDs {
		protected[id] = true
		if r, ok := s.Get(id, ""); ok {
			for _, input := range r.InputJobIDs {
				protected[input] = true
			}
		}
	}
}
