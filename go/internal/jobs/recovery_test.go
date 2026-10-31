package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func writeRecoverySpec(t *testing.T, s *Service, record Record, cwd string) process.Spec {
	t.Helper()
	spec := process.Spec{Version: 1, Command: record.Command, Cwd: cwd, Stdout: record.StdoutPath,
		Stderr: record.StderrPath, Exit: record.ExitPath, Result: record.ResultPath, Control: record.ControlPath,
		OutputDir: s.outputDir(record), Grant: record.GrantPath, Nonce: strings.Repeat("f", 48),
		Deadline: record.DeadlineMS, TimeoutMS: record.TimeoutMS, Limit: record.OutputLimitBytes, Nice: 10}
	if record.ScopeUnit != "" {
		spec.ScopeUnit = record.ScopeUnit + ".scope"
	}
	if err := process.WriteJSON(record.SpecPath, spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestPreparedRevocationRetainsCapacityUntilVerifiedReceipt(t *testing.T) {
	for _, failure := range []string{"error", "unverified-receipt"} {
		t.Run(failure, func(t *testing.T) {
			s, cfg, resolver := newFixture(t)
			cfg.Set("maxActiveCaptures", 1)
			record, _, err := s.admit(opts(resolver, "ungranted fixture"))
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
				t.Fatal("absent finish was used as an ungranted revocation receipt")
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

func TestCommittedLaunchWithoutPublishedGrantRevokesRegistration(t *testing.T) {
	for _, mode := range []string{"missing", "partial", "wrong-nonce", "symlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			s, _, resolver := newFixture(t)
			record, _, err := s.admit(opts(resolver, "ungranted commit window"))
			if err != nil {
				t.Fatal(err)
			}
			claim, revoked, finished, _ := recoveryHelper(t, resolver.workspace.Root, "")
			spec := writeRecoverySpec(t, s, record, resolver.workspace.Root)
			switch mode {
			case "partial":
				err = process.ExclusiveWrite(record.GrantPath, []byte(spec.Nonce[:12]), 0600)
			case "wrong-nonce":
				err = process.ExclusiveWrite(record.GrantPath, []byte(strings.Repeat("a", 48)), 0600)
			case "symlink":
				target := filepath.Join(resolver.workspace.Root, "outside-grant")
				if err = os.WriteFile(target, []byte(spec.Nonce), 0600); err == nil {
					err = os.Symlink(target, record.GrantPath)
				}
			case "fifo":
				err = syscall.Mkfifo(record.GrantPath, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			record, err = s.update(record.ID, func(next *Record) bool {
				next.LaunchState = "granted"
				next.Status = "failed"
				next.FinishedAtMS = time.Now().UnixMilli()
				next.ResourceClaim = &claim
				next.Quiescent = false
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			s.settle(record.ID)
			if time.Since(started) > 2*time.Second {
				t.Fatal("ownership inspection blocked on non-regular grant metadata")
			}
			record, _ = s.Get(record.ID, "")
			if !record.Quiescent {
				t.Fatal("ungranted launch registration was not revoked")
			}
			if _, err = os.Stat(finished); !os.IsNotExist(err) {
				t.Fatal("a committed launch record was mistaken for an execution grant")
			}
			if _, err = os.Stat(revoked); err != nil {
				t.Fatal("caller registration was not revoked")
			}
		})
	}
}

func TestPublishedGrantKeepsNormalFinishSemantics(t *testing.T) {
	s, _, resolver := newFixture(t)
	record, _, err := s.admit(opts(resolver, "accepted terminal fixture"))
	if err != nil {
		t.Fatal(err)
	}
	claim, revoked, finished, _ := recoveryHelper(t, resolver.workspace.Root, "")
	spec := writeRecoverySpec(t, s, record, resolver.workspace.Root)
	if err = process.ExclusiveWrite(record.GrantPath, []byte(spec.Nonce), 0600); err != nil {
		t.Fatal(err)
	}
	record, err = s.update(record.ID, func(next *Record) bool {
		next.LaunchState = "granted"
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
		t.Fatal("successful granted completion was unnecessarily revoked")
	}
}
