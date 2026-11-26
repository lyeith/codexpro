package jobs

import (
	"os"
	"path/filepath"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func (s *Service) newRecord(o startOptions, queued bool) (Record, error) {
	var id string
	for {
		part, err := process.RandomHex(4)
		if err != nil {
			return Record{}, err
		}
		id = "job_" + part
		if _, ok := s.Get(id, ""); ok {
			continue
		}
		if _, err = os.Stat(filepath.Join(s.dir, id+".spec.json")); os.IsNotExist(err) {
			break
		}
	}
	now := time.Now()
	base := filepath.Join(s.dir, id)
	r := Record{ID: id, WorkspaceID: o.Workspace.ID, ProjectID: o.Workspace.ProjectID, Root: o.Workspace.Root, Cwd: o.Cwd, Command: o.Command, CommandLabel: commandLabel(o.Command), Origin: o.Origin, Status: "running", StartedAt: wire.Timestamp(now), StartedAtMS: now.UnixMilli(), DeadlineMS: now.UnixMilli() + o.TimeoutMS, TimeoutMS: o.TimeoutMS, OutputLimitBytes: o.OutputLimit, StdoutPath: base + ".out", StderrPath: base + ".err", ExitPath: base + ".exit", ResultPath: base + ".result.json", ControlPath: base + ".control", SpecPath: base + ".spec.json", RunnerVersion: 3, LaunchState: "prepared", InputJobIDs: o.InputJobIDs, BashSessionID: o.BashSessionID, OwnerPrincipalID: o.Scope.PrincipalID, OwnerSessionID: o.Scope.SessionID, RequestID: o.Scope.RequestID, ParentActionID: o.Scope.ParentActionID}
	r.Agent, r.Identity = o.Scope.Agent, o.Scope.Identity
	r.LauncherVersion = process.LaunchSpecVersion
	if !queued {
		if err := s.prepareExecution(&r); err != nil {
			return Record{}, err
		}
	}
	if queued {
		r.Status, r.LaunchState = "queued", "queued"
		r.DeadlineMS = 0
		// A rollback binary may dispatch a queued record using its old runner.
		r.GrantPath = base + ".grant"
	}
	return r, nil
}

// prepareExecution mints only durable execution identifiers. Actual host
// allocation and runner startup happen in launch.
func (s *Service) prepareExecution(r *Record) error {
	now := time.Now()
	r.Status, r.LaunchState = "running", "prepared"
	r.StartedAt, r.StartedAtMS = wire.Timestamp(now), now.UnixMilli()
	r.DeadlineMS = now.UnixMilli() + r.TimeoutMS
	if s.useScopes {
		incarnation, err := process.RandomHex(16)
		if err != nil {
			return err
		}
		r.ScopeUnit = "codexpro-" + r.ID + "-" + incarnation
	}
	if s.cfg.HostResourcesHelper != "" {
		run, err := process.RandomHex(16)
		if err != nil {
			return err
		}
		registration, err := process.RandomHex(16)
		if err != nil {
			return err
		}
		r.ResourceClaim = &process.ResourceClaim{Version: 1, Helper: s.cfg.HostResourcesHelper, RunID: run, RegistrationID: registration}
	}
	return nil
}
