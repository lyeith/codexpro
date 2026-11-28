package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// This helper exercises the real bounded resource call and cancellation path,
// but never allocates a host scope, cache lease, or command.
func r844LaunchRefusingHelper(t *testing.T, root string) (process.ResourceClaim, string) {
	t.Helper()
	helper := filepath.Join(root, "r844-refusing-helper")
	calls := filepath.Join(root, "r844-resource-calls")
	claim := process.ResourceClaim{
		Version: 1, Helper: helper,
		RunID: strings.Repeat("a", 32), RegistrationID: strings.Repeat("b", 32),
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
case "$1:$2" in
  external:begin) exit 125 ;;
  external:cancel)
    printf '%%s' '{"run_id":"%s","status":"cancelled","quiescent":true,"cancelled":true}'
    ;;
  *) exit 126 ;;
esac
`, recoveryQuote(calls), claim.RunID)
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return claim, calls
}

func r844LaunchRequireKnownFailure(t *testing.T, where string, admitted, receipt Record) {
	t.Helper()
	if receipt.ID != admitted.ID || receipt.WorkspaceID != admitted.WorkspaceID {
		t.Errorf("%s lost the admitted receipt: id=%q workspace=%q", where, receipt.ID, receipt.WorkspaceID)
	}
	if receipt.Status != "failed" || receipt.StopReason != "launch_failed" {
		t.Errorf("%s did not report the known launch refusal: status=%q reason=%q", where, receipt.Status, receipt.StopReason)
	}
	if receipt.Signal != "" || receipt.ExitCode != nil || receipt.PID != 0 || receipt.ProcessIdentity != "" {
		t.Errorf("%s invented an execution outcome: signal=%q exit=%v pid=%d identity=%q", where, receipt.Signal, receipt.ExitCode, receipt.PID, receipt.ProcessIdentity)
	}
	if receipt.StartedAt != admitted.StartedAt || receipt.StartedAtMS != admitted.StartedAtMS || receipt.DeadlineMS != admitted.DeadlineMS {
		t.Errorf("%s replaced the original admission timestamps", where)
	}
	if receipt.FinishedAt == "" || receipt.FinishedAtMS < admitted.StartedAtMS {
		t.Errorf("%s has no terminal receipt timestamp", where)
	}
}

func TestHostResourceRefusalPreservesUnlaunchedJobReceipt(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("hostResourceRetryMs", 0)
	witness := filepath.Join(resolver.workspace.Root, "r844-refused-command")
	options := opts(resolver, "printf must-not-run > "+recoveryQuote(witness))
	admitted := occupy(t, s, options)
	claim, calls := r844LaunchRefusingHelper(t, resolver.workspace.Root)
	admitted, err := s.update(admitted.ID, func(next *Record) bool {
		next.ResourceClaim = &claim
		return true
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := s.launch(ctx, options, admitted)
	if !errors.Is(err, process.ErrHostResourcesUnavailable) {
		t.Fatalf("resource refusal lost its original cause: %v", err)
	}
	stored, ok := s.Get(admitted.ID, resolver.workspace.ID)
	if !ok {
		t.Fatal("resource refusal discarded the admitted job")
	}
	r844LaunchRequireKnownFailure(t, "returned", admitted, receipt)
	r844LaunchRequireKnownFailure(t, "durable", admitted, stored)
	if stored.ResourceClaim == nil || *stored.ResourceClaim != claim || stored.Quiescent {
		t.Error("launch refusal discarded or released the unrevoked registration")
	}
	for _, path := range []string{witness, admitted.SpecPath + ".started"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("refused launch produced execution evidence at %s: %v", path, err)
		}
	}

	s.settle(admitted.ID)
	settled, ok := s.Get(admitted.ID, resolver.workspace.ID)
	if !ok || !settled.Quiescent {
		t.Fatal("exact cancellation did not settle the retained receipt")
	}
	r844LaunchRequireKnownFailure(t, "settled", admitted, settled)
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "external begin ") ||
		!strings.HasSuffix(lines[0], "--run-id "+claim.RunID+" --registration-id "+claim.RegistrationID) ||
		lines[1] != "external cancel "+claim.RunID+" --registration-id "+claim.RegistrationID {
		t.Fatalf("refusal did not cancel exactly its original registration: %q", body)
	}

	result, err := s.Call(ctx, wire.Scope{}, "jobs", map[string]any{
		"workspace_id": resolver.workspace.ID, "job_ids": []string{admitted.ID},
		"output": "none", "wait_ms": 0,
	})
	if err != nil || result.IsError {
		t.Fatalf("retained refusal could not be collected by ID: %v %#v", err, result)
	}
	views := resultData(t, result)["jobs"].([]any)
	if len(views) != 1 {
		t.Fatalf("collection returned %d receipts instead of the admitted ID", len(views))
	}
	view := views[0].(map[string]any)
	if view["job_id"] != admitted.ID || view["status"] != "failed" ||
		view["stop_reason"] != "launch_failed" || view["signal"] != nil || view["exit_code"] != nil {
		t.Errorf("public refusal receipt invented an execution outcome: %#v", view)
	}
}

func TestKnownCmdStartFailureIsNotLost(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("jobNice", 0)
	s.executable = filepath.Join(resolver.workspace.Root, "missing-runner")
	witness := filepath.Join(resolver.workspace.Root, "r844-missing-runner-command")
	options := opts(resolver, "printf must-not-run > "+recoveryQuote(witness))
	admitted := occupy(t, s, options)
	// Observe the durable checkpoint independently of the final failure state.
	if _, err := s.db.Exec(`
CREATE TABLE r844_launch_checkpoints (state TEXT NOT NULL);
CREATE TRIGGER r844_capture_launching AFTER UPDATE ON jobs
WHEN instr(CAST(NEW.payload AS TEXT), '"launch_state":"launching"') > 0
 AND instr(CAST(OLD.payload AS TEXT), '"launch_state":"prepared"') > 0
BEGIN INSERT INTO r844_launch_checkpoints(state) VALUES ('launching'); END;
`); err != nil {
		t.Fatal(err)
	}

	receipt, err := s.launch(context.Background(), options, admitted)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected an actual cmd.Start missing-executable error: %v", err)
	}
	var checkpoints int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM r844_launch_checkpoints").Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 {
		t.Fatalf("missing executable failed without the persisted launching checkpoint: %d", checkpoints)
	}
	stored, ok := s.Get(admitted.ID, resolver.workspace.ID)
	if !ok {
		t.Fatal("cmd.Start failure discarded the admitted job")
	}
	r844LaunchRequireKnownFailure(t, "returned", admitted, receipt)
	r844LaunchRequireKnownFailure(t, "durable", admitted, stored)
	if !stored.Quiescent || stored.QuiescenceScope != "not_launched" {
		t.Errorf("known cmd.Start failure did not retain positive nonexecution: quiet=%v scope=%q", stored.Quiescent, stored.QuiescenceScope)
	}
	for _, path := range []string{witness, admitted.SpecPath + ".started"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("failed cmd.Start left execution evidence at %s: %v", path, err)
		}
	}
}

func TestLaunchPIDPersistenceFailureKeepsAcceptedReceipt(t *testing.T) {
	initial, cfg, resolver := queueFixture(t)
	cfg.Set("jobNice", 0)
	witness := filepath.Join(resolver.workspace.Root, "r844-executed-once")
	options := opts(resolver, "printf 'once\\n' >> "+recoveryQuote(witness)+"; printf accepted")
	admitted := occupy(t, initial, options)
	// Reject only the supplementary post-spawn update. The launching checkpoint,
	// runner-owned files, and later terminal transition remain writable.
	if _, err := initial.db.Exec(`
CREATE TRIGGER r844_refuse_launcher_pid BEFORE UPDATE ON jobs
WHEN instr(CAST(NEW.payload AS TEXT), '"launch_state":"launched"') > 0
BEGIN SELECT RAISE(FAIL, 'r844 launcher PID persistence refused'); END;
`); err != nil {
		t.Fatal(err)
	}

	receipt, err := initial.launch(context.Background(), options, admitted)
	if err != nil {
		t.Fatalf("successful cmd.Start became a retryable launch failure: %v", err)
	}
	if receipt.ID != admitted.ID || receipt.Status != "running" ||
		receipt.LaunchState != "launching" || receipt.PID != 0 || receipt.StopReason != "" {
		t.Fatalf("failed supplementary PID write lost accepted uncertainty: %+v", receipt)
	}
	stored, ok := initial.Get(admitted.ID, resolver.workspace.ID)
	if !ok || stored.ID != receipt.ID || stored.LaunchState != "launching" || stored.PID != 0 {
		t.Fatalf("post-start persistence failure replaced the durable admission: %+v", stored)
	}
	waitStartupFile(t, admitted.SpecPath+".started")
	waitStartupFile(t, admitted.ResultPath)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	stored, ok = recovered.Get(admitted.ID, resolver.workspace.ID)
	if !ok || stored.Status != "running" || stored.LaunchState != "launching" {
		t.Fatalf("restart did not preserve the accepted checkpoint: %+v", stored)
	}
	recovered.observeRunning(stored)
	completed, ok := recovered.Get(admitted.ID, resolver.workspace.ID)
	if !ok || completed.Status != "succeeded" || completed.ExitCode == nil || *completed.ExitCode != 0 ||
		completed.StopReason != "" || completed.Signal != "" {
		t.Fatalf("runner-owned result did not recover the accepted command: %+v", completed)
	}
	if len(recovered.Snapshot()) != 1 {
		t.Fatal("recovery created another admission for the accepted command")
	}
	body, err := os.ReadFile(witness)
	if err != nil || string(body) != "once\n" {
		t.Fatalf("accepted command was omitted or replayed: %q %v", body, err)
	}
	out, err := recovered.ReadTail(context.Background(), admitted.ID, resolver.workspace.ID, 4096)
	if err != nil || out.Stdout != "accepted" {
		t.Fatalf("accepted command output was not retained: %#v %v", out, err)
	}
}

func TestRecoveredZeroPIDLaunchRemainsUncertain(t *testing.T) {
	for _, state := range []string{"launching", "launched"} {
		t.Run(state, func(t *testing.T) {
			initial, cfg, resolver := queueFixture(t)
			witness := filepath.Join(resolver.workspace.Root, "r844-late-command")
			options := opts(resolver, "printf must-not-run > "+recoveryQuote(witness))
			admitted := occupy(t, initial, options)
			writeRecoverySpec(t, initial, admitted, resolver.workspace.Root)
			_, err := initial.update(admitted.ID, func(next *Record) bool {
				next.LaunchState = state
				next.PID, next.ProcessIdentity = 0, ""
				started := time.Now().Add(-time.Second)
				next.StartedAt, next.StartedAtMS = wire.Timestamp(started), started.UnixMilli()
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}

			recovered, err := NewPaused(cfg, resolver)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			stored, ok := recovered.Get(admitted.ID, resolver.workspace.ID)
			if !ok {
				t.Fatal("restart discarded the uncertain admission")
			}
			recovered.observeRunning(stored)
			settled, ok := recovered.Get(admitted.ID, resolver.workspace.ID)
			if !ok || settled.Status != "failed" || settled.StopReason != "lost" ||
				settled.LaunchState != state || settled.PID != 0 || settled.ExitCode != nil {
				t.Fatalf("zero launcher PID was treated as known prestart failure: %+v", settled)
			}
			started, owned := recovered.started(settled)
			if !owned || !started.Revoked || !settled.Quiescent {
				t.Fatal("uncertain launch was finalized without revoking late runner startup")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			late := exec.CommandContext(ctx, os.Args[0], "runner", admitted.SpecPath)
			if err := late.Run(); err == nil || ctx.Err() != nil {
				t.Fatalf("late runner did not promptly refuse revoked ownership: %v", err)
			}
			if _, err := os.Stat(witness); !os.IsNotExist(err) {
				t.Fatalf("late runner executed the uncertain payload: %v", err)
			}
		})
	}
}
