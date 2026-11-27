package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/lyeith/codexpro/go/internal/process"
)

// The capture is read-only. It is called under admission, after duplicate and
// hold checks, and before the immutable job is committed or acknowledged.
// The per-service seam keeps fixtures away from the executing account's store.
type guardedResourceCapture func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error)

func (s *Service) requiresScopes() bool {
	return s.useScopes || s.cfg.Bool("guardedHostResources", false) || s.acceptedGuardedScopes.Load()
}

func (s *Service) captureGuardedAcceptance(r *Record) error {
	if !s.cfg.Bool("guardedHostResources", false) {
		return nil
	}
	if os.Getenv("CODEXPRO_JOB_SCOPES") == "0" {
		return errors.New("guarded host resources require systemd job scopes")
	}
	stopMS := s.cfg.Int64("hostResourcesStopTimeoutMs", 90000)
	if stopMS < 1 || stopMS > 600000 {
		return errors.New("guarded host resources require a finite accepted stop timeout")
	}
	capture := s.guardedCapture
	if capture == nil {
		capture = process.CaptureGuardedResources
	}
	offer, err := capture(s.ctx, s.cfg.String("guardedHostResourcesHelper", ""),
		s.cfg.String("hostResourcesConfig", ""), filepath.Dir(s.dir), uint64(stopMS)*1000)
	if err != nil {
		return err
	}
	if offer == nil {
		return errors.New("guarded host resources did not return an accepted offer")
	}
	// Preserve actual executable references as well as the helper. A later
	// dispatcher/configuration must not silently choose a new accepted runner.
	runner, err := originalExecutable(s.executable)
	if err != nil {
		return err
	}
	launcher, err := originalExecutable(s.launcherExecutable)
	if err != nil {
		return err
	}
	nice, err := offer.ExecutionNice()
	if err != nil {
		return err
	}
	r.GuardedResourceOffer = process.CloneGuardedResourceOffer(offer)
	r.ExecutionNice = &nice
	r.RunnerExecutable, r.LauncherExecutable = runner, launcher
	return nil
}

func originalExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("guarded job executable must have an absolute path")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("guarded job executable is unavailable")
	}
	info, err := os.Stat(physical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("guarded job executable is not an executable regular file")
	}
	return physical, nil
}

func (s *Service) preparedGuardedClaim(r *Record, run, registration string) (*process.ResourceClaim, error) {
	offer := r.GuardedResourceOffer
	if offer == nil {
		return nil, errors.New("guarded job lost its original acceptance offer")
	}
	if err := offer.Validate(filepath.Dir(s.dir)); err != nil {
		return nil, err
	}
	if r.ExecutionNice == nil || *r.ExecutionNice < 0 || *r.ExecutionNice > 19 ||
		!filepath.IsAbs(r.RunnerExecutable) || !filepath.IsAbs(r.LauncherExecutable) {
		return nil, errors.New("guarded job lost its accepted execution settings")
	}
	claim, err := offer.Claim(run, registration)
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

// Close the original common owner before any deliberate signalling/drainage.
// Legacy claims retain their existing protocol and are never upgraded here.
func closeGuardedResources(ctx context.Context, r Record) error {
	if r.ResourceClaim == nil || r.ResourceClaim.Version != 2 {
		return nil
	}
	return process.CloseResources(ctx, *r.ResourceClaim)
}

// A guarded result may establish the original physical owner once. Later
// observations must retain that complete binding; a legacy-shaped or changed
// identity cannot replace it during completion or restart recovery.
func matchesOriginalScope(r Record, candidate *process.ScopeIdentity) bool {
	if !process.ValidScopeIdentity(candidate, r.ScopeUnit+".scope") {
		return false
	}
	if r.ResourceClaim == nil || r.ResourceClaim.Version != 2 {
		return true
	}
	if candidate.CgroupDevice == 0 || candidate.CgroupInode == 0 {
		return false
	}
	return r.ScopeIdentity == nil || *r.ScopeIdentity == *candidate
}

func drainOriginalScope(ctx context.Context, r Record, identity *process.ScopeIdentity) bool {
	if !matchesOriginalScope(r, identity) {
		return false
	}
	if r.ResourceClaim != nil && r.ResourceClaim.Version == 2 {
		// The original finite unit owns its accepted stop timeout, including
		// across dispatcher restarts. Do not apply legacy early KILL timers.
		return process.DrainGuardedScope(ctx, identity)
	}
	return process.SettleScope(ctx, identity)
}
