package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

func TestExecutionHoldRestartPreservesOriginalUnknownCalls(t *testing.T) {
	for _, unverifiedOwner := range []bool{false, true} {
		name := "call_not_returned"
		if unverifiedOwner {
			name = "returned_with_unknown_owner"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg, resolver := queueFixture(t)
			scope := executionHoldTestScope()
			original, release, err := s.AcquireExecution(context.Background(), scope, "batch", resolver.workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			hold, err := s.BeginExecutionHold("restart-" + name)
			if err != nil {
				t.Fatal(err)
			}
			if unverifiedOwner {
				boot, _ := process.BootID()
				owner := ExecutionUnverifiedOwner{Kind: "fixture_original_owner", PID: os.Getpid(),
					ProcessIdentity: process.ProcessIdentity(os.Getpid()), BootID: boot,
					Reason: "original direct helper returned without whole-owner proof"}
				if err := s.RecordUnverifiedExecutionOwner(original, owner); err != nil {
					t.Fatal(err)
				}
				release()
			}
			before := s.ExecutionStatus()
			if len(before.PendingCalls) != 1 || before.DispatchHeld {
				t.Fatalf("original call/owner was not pending: %+v", before)
			}
			if unverifiedOwner && (before.PendingCalls[0].References != 0 || before.PendingCalls[0].State != "owner_pending") {
				t.Fatalf("tool return erased unknown original ownership: %+v", before)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := NewPaused(cfg, resolver)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restored.Close() })
			after := restored.ExecutionStatus()
			if !after.AdmissionHeld || after.DispatchHeld || after.HoldID != hold.HoldID ||
				after.RequestKey != hold.RequestKey || after.HoldInstanceID != before.InstanceID ||
				after.InstanceID == before.InstanceID || len(after.PendingCalls) != 1 {
				t.Fatalf("restart reopened or replaced the original hold: before %+v, after %+v", before, after)
			}
			got, want := after.PendingCalls[0], before.PendingCalls[0]
			if got.ID != want.ID || got.Tool != want.Tool || got.PrincipalID != want.PrincipalID ||
				got.SessionID != want.SessionID || got.RequestID != want.RequestID ||
				got.WorkspaceID != want.WorkspaceID || got.StartedAtMS != want.StartedAtMS ||
				got.References != 0 || got.State != "unknown_after_restart" ||
				!reflect.DeepEqual(got.UnverifiedOwners, want.UnverifiedOwners) {
				t.Fatalf("restart invented completion or lost original ownership: before %+v, after %+v", want, got)
			}
			if _, release, err := restored.AcquireExecution(original, scope, "bash", resolver.workspace.ID); errorCode(err) != "execution_admission_invalid" || release != nil {
				t.Fatalf("new instance adopted an old private capability: %v", err)
			}
			if _, release, err := restored.AcquireExecution(context.Background(), scope, "batch", resolver.workspace.ID); !IsExecutionHeld(err) || release != nil {
				t.Fatalf("same labels fabricated a continuation after restart: %v", err)
			}
			if repeated, err := restored.BeginExecutionHold(hold.RequestKey); err != nil || repeated.HoldID != hold.HoldID {
				t.Fatalf("restored same-key begin replaced hold: %+v, %v", repeated, err)
			}
			if held, err := restored.HoldExecutionDispatch(hold.HoldID); err != nil || held.DispatchHeld {
				t.Fatalf("unknown prior-instance call silently settled: %+v, %v", held, err)
			}
			if _, err := restored.ResumeExecution(hold.HoldID); errorCode(err) != "execution_hold_incomplete" {
				t.Fatalf("unknown prior-instance call allowed resume: %v", err)
			}
			marker := executionHoldTestMarker(t, restored)
			observed, err := restored.ObserveExecutionHold(context.Background())
			if err != nil || observed.Complete || !bytes.Equal(marker, executionHoldTestMarker(t, restored)) {
				t.Fatalf("read-only observation cleared an unknown prior instance: %+v, %v", observed, err)
			}
			if len(restored.Snapshot()) != 0 {
				t.Fatal("restored unknown call was re-created as a job")
			}
		})
	}
}

func TestExecutionHoldCorruptedMarkerFailsBeforeAnyStartupProbe(t *testing.T) {
	cases := []struct {
		name   string
		change func(*executionHoldRecord)
		raw    []byte
	}{
		{name: "malformed_json", raw: []byte("{")},
		{name: "unsupported_version", change: func(r *executionHoldRecord) { r.Version++ }},
		{name: "invalid_hold_id", change: func(r *executionHoldRecord) { r.HoldID = "not-an-opaque-token" }},
		{name: "unsupported_phase", change: func(r *executionHoldRecord) { r.Phase = "reopened" }},
		{name: "invalid_held_timestamp", change: func(r *executionHoldRecord) { r.HeldAtMS = r.RequestedAtMS - 1 }},
		{name: "duplicate_pending_call", change: func(r *executionHoldRecord) {
			call := ExecutionCall{ID: strings.Repeat("1", 32), Tool: "batch", StartedAtMS: r.RequestedAtMS, References: 1}
			r.PendingCalls = []ExecutionCall{call, call}
		}},
		{name: "duplicate_original_job", change: func(r *executionHoldRecord) { r.JobIDs = []string{"job_deadbeef", "job_deadbeef"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg, resolver := queueFixture(t)
			if _, err := s.BeginExecutionHold("corrupt-marker"); err != nil {
				t.Fatal(err)
			}
			var saved executionHoldRecord
			if err := json.Unmarshal(executionHoldTestMarker(t, s), &saved); err != nil {
				t.Fatal(err)
			}
			payload := tc.raw
			if tc.change != nil {
				tc.change(&saved)
				var err error
				payload, err = json.Marshal(saved)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec("UPDATE execution_hold SET payload=? WHERE id=1", payload); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			helper := filepath.Join(bin, "systemd-run")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf forbidden > \"$0.called\"\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CODEXPRO_JOB_SCOPES", "1")
			cfg.Set("jobScopes", true)
			// New (not NewPaused) would run the normal startup probe if it
			// silently ignored this private durable marker.
			restored, err := New(cfg, resolver)
			if err == nil {
				_ = restored.Close()
				t.Fatal("corrupted durable hold silently reopened the service")
			}
			if restored != nil {
				_ = restored.Close()
				t.Fatal("failed recovery returned a usable service")
			}
			if _, err := os.Stat(helper + ".called"); !os.IsNotExist(err) {
				t.Fatalf("invalid hold reached a capability probe: %v", err)
			}
		})
	}
}

// This helper exercises the existing capability-probe command only. It neither
// creates a systemd scope nor supplies a fake whole-owner settlement receipt.
func executionStartupTestProbe(t *testing.T) (entered, release string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("scope capability probe fixture requires Linux")
	}
	bin := t.TempDir()
	helper := filepath.Join(bin, "systemd-run")
	entered, release = helper+".entered", helper+".release"
	body := "#!/usr/bin/python3\n" +
		"import json, os, sys, time\n" +
		"if '--scope' not in sys.argv or sys.argv[-1] != 'true': sys.exit(71)\n" +
		"with open(__file__ + '.entered', 'a') as f: f.write(json.dumps(sys.argv[1:]) + '\\n')\n" +
		"deadline = time.monotonic() + 4\n" +
		"while not os.path.exists(__file__ + '.release'):\n" +
		"    if time.monotonic() >= deadline: sys.exit(72)\n" +
		"    time.sleep(0.01)\n"
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return entered, release
}

func TestExecutionHoldRestoredCompleteHoldSkipsProbeUntilResume(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	hold, err := s.BeginExecutionHold("complete-before-restart")
	if err != nil || !hold.DispatchHeld {
		t.Fatalf("fixture hold was not complete: %+v, %v", hold, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entered, release := executionStartupTestProbe(t)
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	cfg.Set("jobScopes", true)
	restored, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		_ = restored.Close()
	})
	status := restored.ExecutionStatus()
	if !status.DispatchHeld || status.HoldID != hold.HoldID || restored.scopeProbeActive.Load() ||
		len(status.PendingCalls) != 0 || restored.BackgroundReady() {
		t.Fatalf("restored complete hold started new capability work: %+v", status)
	}
	if _, release, err := restored.acquireScopeProbe(); !IsExecutionHeld(err) || release != nil {
		t.Fatalf("held capability admission was not closed: %v", err)
	}
	if _, err := os.Stat(entered); !os.IsNotExist(err) {
		t.Fatalf("restored hold launched the startup probe: %v", err)
	}
	result, err := restored.Call(context.Background(), executionHoldTestScope(), "jobs", map[string]any{
		"workspace_id": resolver.workspace.ID, "output": "none", "wait_ms": 0,
	})
	if err != nil || result.IsError {
		t.Fatalf("held startup made original jobs inspection unavailable: %+v, %v", result, err)
	}
	if _, err := restored.ResumeExecution(hold.HoldID); err != nil {
		t.Fatal(err)
	}
	executionHoldTestWait(t, "resumed original capability probe", func() bool {
		_, err := os.Stat(entered)
		return err == nil
	})
	probing := restored.ExecutionStatus()
	if probing.AdmissionHeld || len(probing.PendingCalls) != 1 || probing.PendingCalls[0].Tool != "scope_probe" ||
		probing.PendingCalls[0].ParentJobID != "" {
		t.Fatalf("resumed probe was not a tracked fresh admission: %+v", probing)
	}
	if _, _, err := restored.admit(queueOption(resolver, executionHoldTestScope(), "printf not-ready")); errorCode(err) != "server_restarting" {
		t.Fatalf("resume falsely declared resource capability ready: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	executionHoldTestWait(t, "probe release and startup readiness", func() bool {
		return restored.BackgroundReady() && !restored.scopeProbeActive.Load() && len(restored.ExecutionStatus().PendingCalls) == 0
	})
	body, err := os.ReadFile(entered)
	if err != nil || bytes.Count(body, []byte("\n")) != 1 || len(restored.Snapshot()) != 0 {
		t.Fatalf("resume duplicated the probe or fabricated jobs: %q, %v", body, err)
	}
	_, releaseFresh, err := restored.AcquireExecution(context.Background(), executionHoldTestScope(), "bash", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	releaseFresh()
}

func TestExecutionHoldRestoredQueueGetsOnlyOriginalCapabilityContinuation(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	scope := executionHoldTestScope()
	queued := enqueueTest(t, s, []startOptions{queueOption(resolver, scope, "printf original")}, "queued-capability")[0]
	hold, err := s.BeginExecutionHold("queued-before-restart")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	cfg.Set("jobScopes", true)
	restored, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	before := executionHoldTestPayload(t, restored, queued.ID)
	ctx, release, err := restored.acquireScopeProbe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	status := restored.ExecutionStatus()
	if status.HoldID != hold.HoldID || !status.AdmissionHeld || status.DispatchHeld || len(status.PendingCalls) != 1 {
		t.Fatalf("original queued capability did not retain its hold: %+v", status)
	}
	call := status.PendingCalls[0]
	if call.Tool != "scope_probe" || call.ParentJobID != queued.ID || call.WorkspaceID != queued.WorkspaceID ||
		call.PrincipalID != queued.OwnerPrincipalID || call.SessionID != queued.OwnerSessionID ||
		call.RequestID != queued.RequestID || call.Agent != queued.Agent || call.References != 1 {
		t.Fatalf("capability continuation lost the original queued identity: %+v", call)
	}
	if _, freshRelease, err := restored.AcquireExecution(context.Background(), scope, "scope_probe", resolver.workspace.ID); !IsExecutionHeld(err) || freshRelease != nil {
		t.Fatalf("tool/request labels fabricated a fresh capability continuation: %v", err)
	}
	_, releaseChild, err := restored.RetainExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseChild)
	release()
	if current := restored.ExecutionStatus(); len(current.PendingCalls) != 1 || current.PendingCalls[0].References != 1 {
		t.Fatalf("original probe lost its retained continuation: %+v", current)
	}
	releaseChild()
	if current := restored.ExecutionStatus(); len(current.PendingCalls) != 0 || current.DispatchHeld ||
		len(current.OutstandingJobs) != 1 || current.OutstandingJobs[0].ID != queued.ID {
		t.Fatalf("probe completion hid unexecuted original queued work: %+v", current)
	}
	if !bytes.Equal(before, executionHoldTestPayload(t, restored, queued.ID)) {
		t.Fatal("capability admission rewrote the queued original's deadline, grant, or command")
	}
}

func TestExecutionHoldRestoredOriginalQueueCompletesOnce(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	scope := executionHoldTestScope()
	option := queueOption(resolver, scope, "printf 'restored-once\\n' >> restored-once; printf original-restored-output")
	queued := enqueueTest(t, s, []startOptions{option}, "restored-original-key")[0]
	grant := []byte("durable original grant\n")
	if err := os.WriteFile(queued.GrantPath, grant, 0600); err != nil {
		t.Fatal(err)
	}
	hold, err := s.BeginExecutionHold("restore-original-queue")
	if err != nil || hold.DispatchHeld {
		t.Fatalf("queued fixture was not draining: %+v, %v", hold, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := restored.wait(ctx, []string{queued.ID}, "all", 5*time.Second, false); err != nil {
		t.Fatal(err)
	}
	done, ok := restored.Get(queued.ID, resolver.workspace.ID)
	if !ok || done.Status != "succeeded" || done.ExitCode == nil || *done.ExitCode != 0 || !done.Quiescent ||
		done.DeadlineMS-done.StartedAtMS != queued.TimeoutMS {
		t.Fatalf("restored original did not finish using its accepted timeout: %+v", done)
	}
	executionHoldTestIdentity(t, queued, done)
	if again := enqueueTest(t, restored, []startOptions{option}, "restored-original-key"); len(again) != 1 || again[0].ID != queued.ID {
		t.Fatalf("restored same-key receipt created another execution: %+v", again)
	}
	if body, err := os.ReadFile(filepath.Join(resolver.workspace.Root, "restored-once")); err != nil || string(body) != "restored-once\n" {
		t.Fatalf("restored original ran more than once: %q, %v", body, err)
	}
	if body, err := os.ReadFile(queued.GrantPath); err != nil || !bytes.Equal(body, grant) {
		t.Fatalf("restart hold replaced or removed the original grant: %q, %v", body, err)
	}
	held, err := restored.HoldExecutionDispatch(hold.HoldID)
	if err != nil || !held.DispatchHeld || len(held.PendingCalls) != 0 || len(held.OutstandingJobs) != 0 ||
		len(held.AcceptedJobs) != 1 || held.AcceptedJobs[0].ID != queued.ID {
		t.Fatalf("restored completion did not close the original hold: %+v, %v", held, err)
	}
	cfg.Set("maxRetainedJobBytes", 0)
	cfg.Set("maxJobHistoryPerWorkspace", 0)
	if err := restored.prune(); err != nil {
		t.Fatal(err)
	}
	done, _ = restored.Get(queued.ID, "")
	if done.OutputExpired {
		t.Fatal("restored active hold did not pin original output")
	}
	if output, err := restored.read(done, 100, false); err != nil || output.Stdout != "original-restored-output" {
		t.Fatalf("restored original output was lost: %+v, %v", output, err)
	}
}
