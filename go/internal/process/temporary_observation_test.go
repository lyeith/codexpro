package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTemporaryCommandObserverPreservesResultAndReportsExactGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity observation requires Linux")
	}
	bootID, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	var previousID uint64
	for _, exitCode := range []int{0, 7} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			base, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			input, release := io.Pipe()
			defer input.Close()
			defer release.Close()
			var events []TemporaryCommandObservation
			ctx := WithTemporaryCommandObserver(base, func(event TemporaryCommandObservation) {
				events = append(events, event)
				if event.Phase == TemporaryCommandStarted {
					// The child cannot finish reading stdin until its original
					// PID/start/boot snapshot has been captured.
					if event.PID != 0 {
						if _, err := io.WriteString(release, "caller input"); err != nil {
							t.Errorf("release own command input: %v", err)
						}
					}
					_ = release.Close()
				}
			})
			root := t.TempDir()
			cmd := exec.CommandContext(ctx, "python3", "-c", `import os, sys
assert os.getcwd() == os.environ["CODEXPRO_TEMPORARY_TEST_ROOT"]
assert os.getpgrp() == os.getpid()
os.write(1, sys.stdin.buffer.read() + b"\xff")
os.write(2, b"\xfe")
sys.exit(int(sys.argv[1]))
`, strconv.Itoa(exitCode))
			cmd.Dir, cmd.Stdin = root, input
			cmd.Env = append(os.Environ(), "CODEXPRO_TEMPORARY_TEST_ROOT="+root)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: syscall.Getpgrp()}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := RunTemporaryCommand(ctx, cmd)
			if exitCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
					t.Fatalf("observer replaced command outcome: %v", err)
				}
			}
			if stdout.String() != "caller input\xff" || stderr.String() != "\xfe" {
				t.Fatalf("observer changed caller I/O: %q, %q", stdout.String(), stderr.String())
			}
			if len(events) != 2 {
				t.Fatalf("wanted paired command observations, got %+v", events)
			}
			started, finished := events[0], events[1]
			if started.CommandID == 0 || started.CommandID == previousID ||
				started.Phase != TemporaryCommandStarted || started.PID != cmd.Process.Pid ||
				started.ProcessIdentity == "" || started.BootID != bootID ||
				started.ObservationError != "" || started.QuiescenceKnown {
				t.Fatalf("start identity was not captured before release: %+v", started)
			}
			previousID = started.CommandID
			if finished.CommandID != started.CommandID || finished.Phase != TemporaryCommandFinished ||
				finished.PID != started.PID || finished.ProcessIdentity != started.ProcessIdentity ||
				finished.BootID != started.BootID || finished.ExitCode == nil || *finished.ExitCode != exitCode ||
				finished.QuiescenceScope != TemporaryQuiescenceProcessGroup ||
				!finished.QuiescenceKnown || !finished.Quiescent || finished.ObservationError != "" {
				t.Fatalf("finished observation lost the exact group or result: %+v", finished)
			}
			wantError := ""
			if err != nil {
				wantError = err.Error()
			}
			if finished.CommandError != wantError {
				t.Fatalf("reported result differs from original return: %+v, %v", finished, err)
			}
		})
	}
}

func TestTemporaryCommandObserverReportsUnlaunchedWithoutChangingFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "missing_executable"
		if cancelled {
			name = "already_cancelled"
		}
		t.Run(name, func(t *testing.T) {
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			var events []TemporaryCommandObservation
			ctx := WithTemporaryCommandObserver(base, func(event TemporaryCommandObservation) {
				events = append(events, event)
			})
			command := filepath.Join(t.TempDir(), "missing-command")
			want := os.ErrNotExist
			if cancelled {
				cancel()
				command, want = "/bin/sh", context.Canceled
			}
			cmd := exec.CommandContext(ctx, command)
			err := RunTemporaryCommand(ctx, cmd)
			if !errors.Is(err, want) || cmd.Process != nil {
				t.Fatalf("observer changed failed start: process=%v err=%v", cmd.Process, err)
			}
			if len(events) != 2 || events[0].Phase != TemporaryCommandStarted ||
				events[1].Phase != TemporaryCommandFinished || events[0].CommandID == 0 ||
				events[1].CommandID != events[0].CommandID {
				t.Fatalf("failed attempt lost its paired identity: %+v", events)
			}
			finished := events[1]
			if finished.QuiescenceScope != TemporaryQuiescenceNotLaunched || !finished.QuiescenceKnown ||
				!finished.Quiescent || finished.PID != 0 || finished.ProcessIdentity != "" ||
				finished.BootID != "" || finished.ExitCode != nil || finished.CommandError != err.Error() ||
				finished.ObservationError != "" {
				t.Fatalf("failed start became an owner/process proof: %+v", finished)
			}
		})
	}
}

func TestTemporaryCommandObserverPanicCannotReplaceCommandError(t *testing.T) {
	var finished TemporaryCommandObservation
	base, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx := WithTemporaryCommandObserver(base, func(event TemporaryCommandObservation) {
		if event.Phase == TemporaryCommandStarted {
			panic("disposable observer failure")
		}
		finished = event
	})
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 7")
	err := RunTemporaryCommand(ctx, cmd)
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 7 {
		t.Fatalf("observer panic changed command outcome: %v", err)
	}
	if finished.Phase != TemporaryCommandFinished || finished.ExitCode == nil || *finished.ExitCode != 7 ||
		finished.CommandError != err.Error() || finished.ObservationError == "" ||
		finished.QuiescenceKnown || finished.Quiescent {
		t.Fatalf("failed observer produced successful proof: %+v", finished)
	}
}

func TestTemporaryCommandObserverRetainsSurvivingGroupThenObservesNaturalExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process-group identity proof requires Linux")
	}
	nonce, err := RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	events := make(chan TemporaryCommandObservation, 2)
	ctx := WithTemporaryCommandObserver(base, func(event TemporaryCommandObservation) { events <- event })
	leaderInput, leaderRelease := io.Pipe()
	memberInput, memberRelease := io.Pipe()
	leader := exec.CommandContext(ctx, "/bin/sh", "-c", `IFS= read -r value && test "$value" = "$1"`, "fixture", nonce)
	leader.Stdin = leaderInput
	member := exec.CommandContext(base, "/bin/sh", "-c", `IFS= read -r value && test "$value" = "$1"`, "fixture", nonce)
	member.Stdin = memberInput
	done := make(chan error, 1)
	leaderReturned, memberStarted, memberReturned := false, false, false
	defer func() {
		// Only close the private release streams and cancel these exact Cmds.
		// The extra group member is a direct test child so Wait reaps it;
		// this control never depends on an external init reaping an orphan.
		_ = leaderRelease.Close()
		_ = memberRelease.Close()
		cancel()
		if !leaderReturned {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("owned leader did not return during cleanup")
			}
		}
		if memberStarted && !memberReturned {
			_ = member.Wait()
		}
		_ = leaderInput.Close()
		_ = memberInput.Close()
	}()
	go func() { done <- RunTemporaryCommand(ctx, leader) }()
	var started TemporaryCommandObservation
	select {
	case started = <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("private leader did not start")
	}
	if started.Phase != TemporaryCommandStarted || started.PID <= 0 ||
		started.ProcessIdentity == "" || started.ObservationError != "" {
		t.Fatalf("unverified private leader: %+v", started)
	}
	// Join only the fresh, observed group while its original leader is held
	// on stdin. This separately waitable member makes later absence exact.
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: started.PID}
	if err := member.Start(); err != nil {
		t.Fatal(err)
	}
	memberStarted = true
	memberIdentity := ProcessIdentity(member.Process.Pid)
	if memberIdentity == "" || ObserveProcess(member.Process.Pid, memberIdentity) != "active" {
		t.Fatal("private surviving member was not observed active")
	}
	if _, err := io.WriteString(leaderRelease, nonce+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = leaderRelease.Close()
	select {
	case err = <-done:
		leaderReturned = true
	case <-time.After(3 * time.Second):
		t.Fatal("private leader did not return")
	}
	if err != nil || base.Err() != nil {
		t.Fatalf("observation changed successful leader exit: err=%v context=%v", err, base.Err())
	}
	var finished TemporaryCommandObservation
	select {
	case finished = <-events:
	case <-time.After(time.Second):
		t.Fatal("private leader returned without its final observation")
	}
	if finished.CommandID != started.CommandID || finished.Phase != TemporaryCommandFinished ||
		finished.ExitCode == nil || *finished.ExitCode != 0 || finished.CommandError != "" ||
		finished.QuiescenceScope != TemporaryQuiescenceProcessGroup ||
		!finished.QuiescenceKnown || finished.Quiescent || finished.ObservationError != "" ||
		ObserveProcess(member.Process.Pid, memberIdentity) != "active" {
		t.Fatalf("surviving group was drained or forgotten: %+v", finished)
	}
	stillActive, err := ObserveTemporaryCommandQuiescence(finished)
	if err != nil || !stillActive.QuiescenceKnown || stillActive.Quiescent {
		t.Fatalf("refresh lost surviving group: %+v, %v", stillActive, err)
	}
	if _, err := io.WriteString(memberRelease, nonce+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = memberRelease.Close()
	if err := member.Wait(); err != nil {
		t.Fatal(err)
	}
	memberReturned = true
	quiet, err := ObserveTemporaryCommandQuiescence(finished)
	if err != nil || !quiet.QuiescenceKnown || !quiet.Quiescent || quiet.ObservationError != "" ||
		quiet.CommandID != finished.CommandID || quiet.PID != finished.PID ||
		quiet.ProcessIdentity != finished.ProcessIdentity || quiet.BootID != finished.BootID ||
		quiet.Phase != finished.Phase || quiet.QuiescenceScope != TemporaryQuiescenceProcessGroup ||
		quiet.ExitCode == nil || *quiet.ExitCode != 0 || quiet.CommandError != finished.CommandError {
		t.Fatalf("natural group exit was not safely refreshed: %+v, %v", quiet, err)
	}
}

func TestObserveTemporaryCommandQuiescenceRefusesUnboundOrForeignIdentity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity proof requires Linux")
	}
	identity, err := strconv.ParseUint(ProcessIdentity(os.Getpid()), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	bootID, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	exitCode := 7
	original := TemporaryCommandObservation{
		CommandID: 1, Phase: TemporaryCommandFinished, PID: os.Getpid(),
		ProcessIdentity: strconv.FormatUint(identity+1, 10), BootID: bootID,
		ExitCode: &exitCode, CommandError: "original exit status 7",
		QuiescenceScope: TemporaryQuiescenceProcessGroup, Quiescent: true, QuiescenceKnown: true,
	}
	cases := []struct {
		name   string
		change func(*TemporaryCommandObservation)
	}{
		{"foreign_start_identity", func(*TemporaryCommandObservation) {}},
		{"unfinished", func(o *TemporaryCommandObservation) { o.Phase = TemporaryCommandStarted }},
		{"missing_command_id", func(o *TemporaryCommandObservation) { o.CommandID = 0 }},
		{"missing_pid", func(o *TemporaryCommandObservation) { o.PID = 0 }},
		{"missing_start_identity", func(o *TemporaryCommandObservation) { o.ProcessIdentity = "" }},
		{"wrong_boot", func(o *TemporaryCommandObservation) { o.BootID = strings.Repeat("0", 36) }},
		{"wrong_scope", func(o *TemporaryCommandObservation) { o.QuiescenceScope = "ssd_owner" }},
		{"contradictory_unlaunched", func(o *TemporaryCommandObservation) { o.QuiescenceScope = TemporaryQuiescenceNotLaunched }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := original
			tc.change(&input)
			got, err := ObserveTemporaryCommandQuiescence(input)
			if err == nil || got.QuiescenceKnown || got.Quiescent || got.ObservationError == "" {
				t.Fatalf("unbound/foreign identity became quiet: %+v, %v", got, err)
			}
			if got.CommandID != input.CommandID || got.PID != input.PID ||
				got.ProcessIdentity != input.ProcessIdentity || got.BootID != input.BootID ||
				got.ExitCode == nil || *got.ExitCode != 7 || got.CommandError != input.CommandError ||
				got.Phase != input.Phase || got.QuiescenceScope != input.QuiescenceScope {
				t.Fatalf("refresh replaced original identity or command outcome: %+v", got)
			}
		})
	}
}
