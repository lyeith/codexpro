package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestExecutionHoldOriginalQueueExpiryDuringResolutionCompletesWithoutLaunch(t *testing.T) {
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	helperCalled := filepath.Join(base, "helper-called")
	helper := filepath.Join(base, "helper")
	// Any accidental launch records the violation and refuses allocation;
	// this fixture cannot allocate a real systemd owner.
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
	t.Cleanup(func() { _ = s.Close() })
	// Drive only the accepted queue item, without a background dispatcher or
	// capability probe that could obscure the resolver/admission ordering.
	s.backgroundActive.Store(true)
	s.scopesReady.Store(true)

	scope := executionHoldTestScope()
	scope.ParentActionID = "original-parent"
	ctx, releaseAdmission, err := s.AcquireExecution(context.Background(), scope, "start_jobs", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseAdmission)
	option := queueOption(resolver, scope, "printf payload > payload-ran")
	option.admission = executionLeaseFrom(ctx)
	options := []startOptions{option}
	payload := []any{[]any{option.Command, option.Cwd, option.InputJobIDs}}
	const queueKey = "held-expiry-during-resolution"
	const queueTimeoutMS int64 = 5000
	accepted, err := s.enqueue(options, queueKey, queueTimeoutMS, payload)
	if err != nil || len(accepted) != 1 {
		t.Fatalf("original queue admission failed: %+v, %v", accepted, err)
	}
	// Compare the same detached publication representation throughout; Get
	// owns the existing nil-to-empty input-slice copy convention.
	original, ok := s.Get(accepted[0].ID, resolver.workspace.ID)
	if !ok || original.Status != "queued" || original.DeadlineMS != 0 || original.LauncherVersion != process.LaunchSpecVersion {
		t.Fatalf("fixture did not accept an unstarted version-1 original: %+v", original)
	}
	releaseAdmission()
	admittedPayload := executionHoldTestPayload(t, s, original.ID)

	// Begin before dispatch can take admission. Starting a concurrent hold
	// behind the resolver barrier would only test mutex waiting.
	hold, err := s.BeginExecutionHold("original-queue-expiry")
	if err != nil || hold.HoldID == "" || !hold.AdmissionHeld || hold.DispatchHeld ||
		len(hold.PendingCalls) != 0 || len(hold.AcceptedJobs) != 1 || len(hold.OutstandingJobs) != 1 ||
		hold.AcceptedJobs[0].ID != original.ID || hold.OutstandingJobs[0].ID != original.ID {
		t.Fatalf("hold did not retain the accepted original for draining: %+v, %v", hold, err)
	}
	if !bytes.Equal(admittedPayload, executionHoldTestPayload(t, s, original.ID)) {
		t.Fatal("beginning the hold changed the accepted queue record")
	}
	retryOption := option
	retryOption.admission = nil
	if _, err := s.enqueue([]startOptions{retryOption}, "fresh-during-hold", queueTimeoutMS, payload); !IsExecutionHeld(err) {
		t.Fatalf("fresh queue admission escaped the original hold: %v", err)
	}

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
			t.Error("held original dispatcher did not leave resolver barrier")
		}
	})
	select {
	case <-barrier.entered:
	case <-done:
		t.Fatal("hold prevented dispatch from selecting the unexpired original")
	case <-time.After(10 * time.Second):
		t.Fatal("original dispatcher did not enter resolver barrier")
	}
	timer := time.NewTimer(time.Until(time.UnixMilli(original.QueueDeadlineMS)) + time.Millisecond)
	defer timer.Stop()
	<-timer.C
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("original dispatcher did not finish after resolver release")
	}
	s.wg.Wait()
	if expiryBlocked {
		t.Fatal("held queue expiry unexpectedly failed to persist")
	}

	got, ok := s.Get(original.ID, resolver.workspace.ID)
	if !ok || got.ID != original.ID || got.Status != "timed_out" || got.StopReason != "queue_timeout" {
		t.Fatalf("hold lost or dispatched the expired original: %+v", got)
	}
	executionHoldTestIdentity(t, original, got)
	if got.DeadlineMS != 0 || got.StartedAtMS != original.StartedAtMS || got.StartedAt != original.StartedAt ||
		got.PID != 0 || got.ProcessIdentity != "" || got.ScopeUnit != "" || got.ScopeIdentity != nil ||
		got.ResourceClaim != nil || got.LaunchResult != nil || got.ExitCode != nil || got.Signal != "" ||
		got.LaunchState != "queued" || s.entry(original.ID).launching.Load() {
		t.Fatalf("expired held original acquired execution state: %+v", got)
	}
	if !got.Quiescent || got.QuiescenceScope != "not_launched" ||
		got.FinishedAtMS < original.QueueDeadlineMS || got.FinishedAt == "" || got.QuiescedAt != got.FinishedAt {
		t.Fatalf("expired held original did not settle as unstarted: %+v", got)
	}
	assertNoArtifacts := func() {
		t.Helper()
		paths := []string{
			helperCalled, filepath.Join(root, "payload-ran"), s.outputDir(original),
			original.SpecPath, original.SpecPath + ".started", original.StdoutPath, original.StderrPath,
			original.ExitPath, original.ResultPath, original.ControlPath, original.GrantPath,
			s.launcherPath(original, launcherSpecName), s.launcherPath(original, launcherSpecName) + ".started",
			s.launcherPath(original, launcherResultName), s.launcherPath(original, launcherStdoutName),
			s.launcherPath(original, launcherStderrName),
		}
		for _, path := range paths {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("expired held original left a launch artifact at %s: %v", path, err)
			}
		}
	}
	assertNoArtifacts()

	terminalPayload := executionHoldTestPayload(t, s, original.ID)
	var completionPayload []byte
	if err := s.db.QueryRow("SELECT payload FROM job_completions WHERE job_id=?", original.ID).Scan(&completionPayload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(terminalPayload, completionPayload) {
		t.Fatal("durable completion omitted or changed original terminal record fields")
	}
	var completion Record
	if err := json.Unmarshal(completionPayload, &completion); err != nil {
		t.Fatal(err)
	}
	// Preserve every accepted receipt field, permitting only the terminal
	// outcome and its observed finish/quiescence timestamps to change.
	want := original
	want.Status, want.StopReason = "timed_out", "queue_timeout"
	want.FinishedAtMS, want.FinishedAt = got.FinishedAtMS, got.FinishedAt
	want.Quiescent, want.QuiescenceScope, want.QuiescedAt = true, "not_launched", got.FinishedAt
	if !reflect.DeepEqual(completion, want) {
		t.Fatalf("completion changed the accepted receipt beyond queue expiry:\nwant %+v\ngot  %+v", want, completion)
	}
	again, err := s.enqueue([]startOptions{retryOption}, queueKey, queueTimeoutMS, payload)
	if err != nil || len(again) != 1 || again[0].ID != original.ID || !reflect.DeepEqual(again[0], completion) ||
		len(s.Snapshot()) != 1 {
		t.Fatalf("held retry changed or replayed the original receipt: %+v, %v", again, err)
	}
	var completions int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM job_completions").Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("held retry changed original completion count: count=%d err=%v", completions, err)
	}
	var afterRetryCompletion []byte
	if err := s.db.QueryRow("SELECT payload FROM job_completions WHERE job_id=?", original.ID).Scan(&afterRetryCompletion); err != nil ||
		!bytes.Equal(afterRetryCompletion, completionPayload) || !bytes.Equal(terminalPayload, executionHoldTestPayload(t, s, original.ID)) {
		t.Fatalf("held retry rewrote the original durable receipt: %v", err)
	}

	status, err := s.HoldExecutionDispatch(hold.HoldID)
	if err != nil || status.HoldID != hold.HoldID || !status.AdmissionHeld || !status.DispatchHeld ||
		len(status.PendingCalls) != 0 || len(status.OutstandingJobs) != 0 || len(status.AcceptedJobs) != 1 ||
		status.AcceptedJobs[0].ID != original.ID || status.AcceptedJobs[0].LauncherState != "not_launched" ||
		!status.AcceptedJobs[0].LauncherQuiescent {
		t.Fatalf("unstarted original could not complete its original hold: %+v, %v", status, err)
	}
	heldPayload := executionHoldTestMarker(t, s)
	var saved executionHoldRecord
	if err := json.Unmarshal(heldPayload, &saved); err != nil || saved.HoldID != hold.HoldID ||
		!reflect.DeepEqual(saved.JobIDs, []string{original.ID}) {
		t.Fatalf("held durable cohort changed the original identity: %+v, %v", saved, err)
	}
	readBack, ok := s.Get(original.ID, resolver.workspace.ID)
	if !ok || !reflect.DeepEqual(readBack, completion) {
		t.Fatalf("GET changed the original terminal receipt: %+v", readBack)
	}
	observed, err := s.ObserveExecutionHold(context.Background())
	if err != nil || !observed.Complete || observed.Status.HoldID != hold.HoldID || len(observed.Owners) != 1 {
		t.Fatalf("unstarted original hold remained incomplete: %+v, %v", observed, err)
	}
	owner := observed.Owners[0]
	if owner.JobID != original.ID || !owner.CommandTerminal || owner.CommandStatus != "timed_out" ||
		!owner.PublishedQuiescent || !owner.Quiescent || !owner.OwnerQuiescentVerified || owner.Error != "" ||
		owner.Scope != "not_launched" || owner.LauncherState != "not_launched" || !owner.LauncherQuiescent || owner.Resource != nil {
		t.Fatalf("hold invented an owner or lost unstarted quiescence: %+v", owner)
	}
	if !bytes.Equal(terminalPayload, executionHoldTestPayload(t, s, original.ID)) ||
		!bytes.Equal(heldPayload, executionHoldTestMarker(t, s)) {
		t.Fatal("read-only GET/Observe changed the original row or held marker")
	}
	assertNoArtifacts()
}
