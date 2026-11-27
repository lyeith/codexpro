package jobs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

func (s *Service) runnerSpecification(r Record, o startOptions) (process.Spec, error) {
	spec := process.Spec{Version: process.RunnerSpecVersion, Command: r.Command, Cwd: o.CwdAbs,
		Stdout: r.StdoutPath, Stderr: r.StderrPath, Exit: r.ExitPath, Result: r.ResultPath,
		Control: r.ControlPath, OutputDir: s.outputDir(r), Deadline: r.DeadlineMS,
		TimeoutMS: r.TimeoutMS, Limit: r.OutputLimitBytes, Nice: s.cfg.Int("jobNice", 10),
		PathRedactions: s.paths(o.Workspace)}
	if r.ScopeUnit != "" {
		spec.ScopeUnit = r.ScopeUnit + ".scope"
	}
	if r.GuardedResourceOffer != nil {
		if r.ResourceClaim == nil || r.ResourceClaim.Version != 2 || r.ExecutionNice == nil {
			return spec, fmt.Errorf("guarded job lost its accepted claim or execution settings")
		}
		if err := r.GuardedResourceOffer.Validate(filepath.Dir(s.dir)); err != nil {
			return spec, err
		}
		claim := process.CloneResourceClaim(*r.ResourceClaim)
		spec.Version, spec.ResourceClaim, spec.Nice = process.GuardedRunnerSpecVersion, &claim, *r.ExecutionNice
	} else if r.ResourceClaim != nil && r.ResourceClaim.Version != 1 {
		return spec, fmt.Errorf("job lost its original guarded acceptance offer")
	}
	return spec, spec.Validate(r.SpecPath)
}

// A queue acknowledgement must not accept an offer/command that the producer's
// bounded native Job or RunnerSpec reader can never consume. Projection mints
// no authority: these fixed-width placeholders are not persisted or launched.
func (s *Service) validateGuardedInputs(r Record, o startOptions) error {
	if r.GuardedResourceOffer == nil {
		return nil
	}
	projected := copyRecord(r)
	if projected.ResourceClaim == nil {
		claim, err := projected.GuardedResourceOffer.Claim(strings.Repeat("0", 32), strings.Repeat("0", 32))
		if err != nil {
			return err
		}
		projected.ResourceClaim = &claim
		projected.ScopeUnit = "codexpro-" + r.ID + "-" + strings.Repeat("0", 32)
		projected.DeadlineMS = time.Now().UnixMilli() + r.TimeoutMS
	}
	_, err := s.runnerSpecification(projected, o)
	return err
}

func validateGuardedRecord(r Record) error {
	if r.GuardedResourceOffer == nil {
		return nil
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	growth, err := guardedClaimGrowth(r)
	if err != nil {
		return err
	}
	// Reserve the existing bounded ownership/result growth too: the same
	// original record remains readable through activation and settlement.
	if int64(len(body))+growth+queuedLifecyclePayloadBytes > process.MaxGuardedMetadataBytes {
		return jobError("guarded_metadata_limit", "Guarded job metadata exceeds the producer's native record limit; no job was accepted.")
	}
	return nil
}
