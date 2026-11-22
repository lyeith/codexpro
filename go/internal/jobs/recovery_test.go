package jobs

import (
	"context"
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

func recoveryQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

// The fake helper distinguishes instantaneous absence from durable revocation.
// It never allocates a real host scope, cache, scratch lease or child command.
func recoveryHelper(t *testing.T, root, blockedMode string) (process.ResourceClaim, string, string, string) {
	t.Helper()
	run, registration := strings.Repeat("d", 32), strings.Repeat("e", 32)
	helper := filepath.Join(root, "recovery-helper")
	revoked, finished, blocked := filepath.Join(root, "revoked"), filepath.Join(root, "finished"), filepath.Join(root, "blocked")
	script := fmt.Sprintf(`#!/bin/sh
case "$2" in
  finish)
    printf '%%s' "$3:$5:$7" > %s
    printf '%%s' '{"run_id":"%s","status":"absent","quiescent":true,"cancelled":false}'
    ;;
  cancel)
    if [ -f %s ]; then
      if [ %s = error ]; then exit 125; fi
      printf '%%s' '{"run_id":"%s","status":"absent","quiescent":true,"cancelled":false}'
      exit 0
    fi
    printf '%%s' "$3:$5" > %s
    printf '%%s' '{"run_id":"%s","status":"cancelled","quiescent":true,"cancelled":true}'
    ;;
  begin)
    if [ -f %s ]; then exit 125; fi
    printf '%%s' '{"run_id":"%s","registration_id":"%s","scratch":"/unused","environment":{},"owner":{}}'
    ;;
  *) exit 126 ;;
esac
`, recoveryQuote(finished), run, recoveryQuote(blocked), recoveryQuote(blockedMode), run,
		recoveryQuote(revoked), run, recoveryQuote(revoked), run, registration)
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return process.ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: registration}, revoked, finished, blocked
}

func writeRecoverySpec(t *testing.T, s *Service, record Record, cwd string) {
	t.Helper()
	spec := process.Spec{Version: process.RunnerSpecVersion, Command: record.Command, Cwd: cwd, Stdout: record.StdoutPath,
		Stderr: record.StderrPath, Exit: record.ExitPath, Result: record.ResultPath, Control: record.ControlPath,
		OutputDir: s.outputDir(record),
		Deadline:  record.DeadlineMS, TimeoutMS: record.TimeoutMS, Limit: record.OutputLimitBytes, Nice: 10}
	if record.ScopeUnit != "" {
		spec.ScopeUnit = record.ScopeUnit + ".scope"
	}
	if err := process.WriteJSON(record.SpecPath, spec); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRevocationRetainsCapacityUntilVerifiedReceipt(t *testing.T) {
	for _, failure := range []string{"error", "unverified-receipt"} {
		t.Run(failure, func(t *testing.T) {
			s, cfg, resolver := newFixture(t)
			cfg.Set("maxActiveCaptures", 1)
			record, _, err := s.admit(opts(resolver, "prepared fixture"))
			if err != nil {
				t.Fatal(err)
			}
			claim, revoked, finished, blocked := recoveryHelper(t, resolver.workspace.Root, failure)
			if err = os.WriteFile(blocked, []byte("block cancellation acknowledgement"), 0600); err != nil {
				t.Fatal(err)
			}
			record, err = s.update(record.ID, func(next *Record) bool {
				next.Status = "failed"
				next.FinishedAtMS = time.Now().UnixMilli()
				next.ResourceClaim = &claim
				next.Quiescent = false
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			s.settle(record.ID)
			record, _ = s.Get(record.ID, "")
			if record.Quiescent {
				t.Fatal("unverified cancellation released prepared ownership")
			}
			_, err = s.start(context.Background(), opts(resolver, "touch must-not-start"))
			if failure, ok := err.(*wire.Error); !ok || failure.Code != "job_limit_reached" {
				t.Fatalf("unresolved registration did not retain admission capacity: %v", err)
			}
			if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "must-not-start")); !os.IsNotExist(err) {
				t.Fatal("new child ran before original registration was revoked")
			}
			if _, err = os.Stat(finished); !os.IsNotExist(err) {
				t.Fatal("absent finish was used as a revocation receipt")
			}
			if err = os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			s.settle(record.ID)
			record, _ = s.Get(record.ID, "")
			if !record.Quiescent {
				t.Fatal("verified cancellation did not release prepared ownership")
			}
			body, err := os.ReadFile(revoked)
			if err != nil || string(body) != claim.RunID+":"+claim.RegistrationID {
				t.Fatalf("wrong registration revoked: %q %v", body, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err = process.BeginResources(ctx, claim, resolver.workspace.Root, "codexpro-job_deadbeef-"+strings.Repeat("a", 32)+".scope"); err == nil {
				t.Fatal("late begin activated after ownership was released")
			}
			next, err := s.start(ctx, opts(resolver, "touch admitted-after-revocation"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.wait(ctx, []string{next.ID}, "all", 3*time.Second, false); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "admitted-after-revocation")); err != nil {
				t.Fatal("verified revocation did not restore admission capacity")
			}
		})
	}
}

func TestRecoveredLaunchPreventsLateRunner(t *testing.T) {
	initial, cfg, resolver := newFixture(t)
	options := opts(resolver, "touch must-not-run")
	options.TimeoutMS = 10000
	record, _, err := initial.admit(options)
	if err != nil {
		t.Fatal(err)
	}
	claim, revoked, finished, _ := recoveryHelper(t, resolver.workspace.Root, "")
	writeRecoverySpec(t, initial, record, resolver.workspace.Root)
	_, err = initial.update(record.ID, func(next *Record) bool {
		next.LaunchState = "launching"
		next.StartedAtMS = time.Now().Add(-time.Second).UnixMilli()
		next.ResourceClaim = &claim
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
	record, _ = recovered.Get(record.ID, "")
	recovered.observeRunning(record)
	recovered.settle(record.ID)
	record, _ = recovered.Get(record.ID, "")
	if record.Status != "failed" || !record.Quiescent {
		t.Fatalf("unobserved launch did not settle: %+v", record)
	}
	started, ok := recovered.started(record)
	if !ok || !started.Revoked {
		t.Fatal("recovery did not revoke late runner startup")
	}
	if _, err := os.Stat(revoked); err != nil {
		t.Fatalf("resource registration was not cancelled: %v", err)
	}
	if _, err := os.Stat(finished); !os.IsNotExist(err) {
		t.Fatal("unobserved launch was finished instead of cancelled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "runner", record.SpecPath)
	if err := command.Run(); err == nil || ctx.Err() != nil {
		t.Fatalf("revoked runner did not exit promptly with an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resolver.workspace.Root, "must-not-run")); !os.IsNotExist(err) {
		t.Fatal("late runner executed a revoked payload")
	}
}

func TestCompletedCommandFinishesResourceRegistration(t *testing.T) {
	s, _, resolver := newFixture(t)
	record, _, err := s.admit(opts(resolver, "accepted terminal fixture"))
	if err != nil {
		t.Fatal(err)
	}
	claim, revoked, finished, _ := recoveryHelper(t, resolver.workspace.Root, "")
	record, err = s.update(record.ID, func(next *Record) bool {
		next.LaunchState = "launched"
		next.Status = "succeeded"
		next.ExitCode = intPointer(0)
		next.FinishedAtMS = time.Now().UnixMilli()
		next.ResourceClaim = &claim
		next.Quiescent = false
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settle(record.ID)
	record, _ = s.Get(record.ID, "")
	if !record.Quiescent || record.Status != "succeeded" {
		t.Fatal("accepted completion lost its normal outcome or settlement")
	}
	body, err := os.ReadFile(finished)
	if err != nil || string(body) != claim.RunID+":"+claim.RegistrationID+":0" {
		t.Fatalf("normal completion did not finish its exact registration and exit code: %q %v", body, err)
	}
	if _, err = os.Stat(revoked); !os.IsNotExist(err) {
		t.Fatal("successful completion was unnecessarily revoked")
	}
}
