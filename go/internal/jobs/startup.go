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
	for i := 0; i < 2; i++ {
		s.wg.Add(1)
		go s.settlementWorker()
	}
	s.wg.Add(1)
	go s.completionWorker()
	if s.useScopes && !s.scopesReady.Load() {
		s.wg.Add(1)
		go s.probeScopesBackground()
	}
}

func (s *Service) probeScopesBackground() {
	defer s.wg.Done()
	for {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		err := process.ProbeScopes(ctx)
		cancel()
		if err == nil {
			s.scopesReady.Store(true)
			return
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
