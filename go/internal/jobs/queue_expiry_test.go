package jobs

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type queueExpiryResolver struct {
	testResolver
	entered chan struct{}
	release chan struct{}
}

func (r *queueExpiryResolver) Resolve(ctx context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	close(r.entered)
	select {
	case <-r.release:
		return r.testResolver.Resolve(ctx, scope, id)
	case <-ctx.Done():
		return wire.Workspace{}, ctx.Err()
	}
}

func TestCapacityQueueExpiryDuringResolutionNeverLaunches(t *testing.T) {
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	helperCalled := filepath.Join(base, "helper-called")
	helper := filepath.Join(base, "helper")
	// If the regression launches, this private helper records it and refuses
	// allocation immediately; no actual systemd owner can be created.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf called > "+recoveryQuote(helperCalled)+"\nexit 125\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(base, "state"))
	cfg.HostResourcesHelper = helper
	cfg.Set("hostResourceRetryMs", 0)
	resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: root}}
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	// The dispatcher is driven manually, with the private refusing helper in
	// place of a host-capability probe. No background worker is started.
	s.backgroundActive.Store(true)
	s.scopesReady.Store(true)
	t.Cleanup(func() { _ = s.Close() })

	option := queueOption(resolver, queueOwner("fp5"), "printf payload > payload-ran")
	options := []startOptions{option}
	payload := []any{[]any{option.Command, option.Cwd, option.InputJobIDs}}
	const queueTimeoutMS int64 = 5000
	accepted, err := s.enqueue(options, "expiry-during-resolution", queueTimeoutMS, payload)
	if err != nil {
		t.Fatal(err)
	}
	original := accepted[0]
	barrier := &queueExpiryResolver{testResolver: resolver, entered: make(chan struct{}), release: make(chan struct{})}
	s.resolver = barrier
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	done := make(chan struct{})
	var expiryBlocked bool
	go func() {
		expiryBlocked = s.dispatchQueued()
		close(done)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("dispatcher did not leave resolver barrier")
		}
	})
	select {
	case <-barrier.entered:
	case <-done:
		t.Fatal("dispatcher did not select the unexpired original record")
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher did not enter resolver barrier")
	}
	// Let the accepted deadline elapse while resolution is blocked. The
	// original deadline is never rewritten or replaced with an execution one.
	timer := time.NewTimer(time.Until(time.UnixMilli(original.QueueDeadlineMS)) + time.Millisecond)
	defer timer.Stop()
	<-timer.C
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher did not finish after resolver release")
	}
	s.wg.Wait()
	if expiryBlocked {
		t.Error("queue expiry unexpectedly failed to persist")
	}
	got, ok := s.Get(original.ID, resolver.workspace.ID)
	if !ok || got.ID != original.ID || got.Status != "timed_out" || got.StopReason != "queue_timeout" {
		t.Fatalf("expired original was dispatched or lost: %+v", got)
	}
	if got.QueueDeadlineMS != original.QueueDeadlineMS || got.DeadlineMS != 0 ||
		got.StartedAtMS != original.StartedAtMS || got.StartedAt != original.StartedAt || got.TimeoutMS != original.TimeoutMS {
		t.Fatalf("expiry replaced the original acceptance or started execution: %+v", got)
	}
	if got.PID != 0 || got.ProcessIdentity != "" || got.ScopeUnit != "" || got.ScopeIdentity != nil || got.ResourceClaim != nil ||
		got.LaunchState != "queued" || s.entry(original.ID).launching.Load() {
		t.Fatalf("expired record acquired execution state: %+v", got)
	}
	if !got.Quiescent || got.QuiescenceScope != "not_launched" || got.FinishedAtMS < original.QueueDeadlineMS {
		t.Fatalf("expiry did not preserve unstarted settlement: %+v", got)
	}
	for _, path := range []string{helperCalled, filepath.Join(root, "payload-ran"), original.SpecPath, original.SpecPath + ".started", original.StdoutPath, original.StderrPath, original.ResultPath, original.ControlPath, original.GrantPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expired queue entry performed launch work at %s: %v", path, err)
		}
	}
	var completions int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM job_completions WHERE job_id=?", original.ID).Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("missing durable completion for original: count=%d err=%v", completions, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	recovered, ok := fresh.Get(original.ID, resolver.workspace.ID)
	if !ok || recovered.Status != "timed_out" || recovered.StopReason != "queue_timeout" || recovered.DeadlineMS != 0 ||
		recovered.QueueDeadlineMS != original.QueueDeadlineMS || recovered.FinishedAtMS != got.FinishedAtMS {
		t.Fatalf("restart lost original queue expiry: %+v", recovered)
	}
	again, err := fresh.enqueue(options, "expiry-during-resolution", queueTimeoutMS, payload)
	if err != nil || len(again) != 1 || again[0].ID != original.ID || len(fresh.Snapshot()) != 1 {
		t.Fatalf("expired retry created another execution: %+v %v", again, err)
	}
}
