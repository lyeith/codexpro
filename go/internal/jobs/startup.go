package jobs

import (
	"context"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// StartBackground is idempotent and never waits for a host helper. Existing
// ownership recovery is independent of new-job scope capability: an unavailable
// capability probe must not suppress cancellation of an earlier registration.
func (s *Service) StartBackground() {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.closed.Load() || s.backgroundActive.Load() {
		return
	}
	s.backgroundActive.Store(true)
	s.wg.Add(1)
	go s.monitor()
	s.wg.Add(1)
	go s.queueDispatcher()
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go s.settlementWorker()
	}
	s.wg.Add(1)
	go s.completionWorker()
	s.startScopeProbeLocked()
}

func (s *Service) probeScopesBackground() {
	defer func() {
		s.scopeProbeActive.Store(false)
		// Resume may race an ineligible probe's return. Recheck under the
		// background lifetime lock so that race cannot strand startup.
		s.startScopeProbe()
		s.wg.Done()
	}()
	for {
		admitted, release, err := s.acquireScopeProbe()
		if IsExecutionHeld(err) || s.ctx.Err() != nil {
			if release != nil {
				release()
			}
			return
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(admitted, 5*time.Second)
			unit, probeErr := process.ProbeScopesWithUnit(ctx)
			cancel()
			if probeErr != nil && unit != "" {
				_ = s.RecordUnverifiedExecutionOwner(admitted, ExecutionUnverifiedOwner{
					Kind: "scope_probe", Unit: unit,
					Reason: "The original scope probe failed without whole-owner quiescence proof.",
				})
			}
			if probeErr == nil {
				s.scopesReady.Store(true)
			}
			release()
			if probeErr == nil {
				s.notify()
				return
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// BackgroundReady is a nonblocking capability observation, not a settlement
// claim about historical jobs. Reads and stop requests do not wait on it.
func (s *Service) BackgroundReady() bool {
	return s.backgroundActive.Load() && (!s.useScopes || s.scopesReady.Load()) && !s.closed.Load()
}

func (s *Service) startupAdmissionError() error {
	if s.BackgroundReady() {
		return nil
	}
	return &wire.Error{Code: "server_restarting", Message: "Job resources are not ready; retry after background startup completes.", Data: map[string]any{"retry_unchanged": true}}
}
