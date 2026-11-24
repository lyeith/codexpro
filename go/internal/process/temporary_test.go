package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunTemporaryCommandPreservesRawResultAndOwnsGroup(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			root := t.TempDir()
			cmd := exec.CommandContext(ctx, "python3", "-c", `import os, sys
assert os.getcwd() == os.environ["CODEXPRO_TEMPORARY_TEST_ROOT"]
assert os.getpgrp() == os.getpid()
os.write(1, sys.stdin.buffer.read() + b"\xff")
os.write(2, b"\xfe")
sys.exit(int(sys.argv[1]))
`, strconv.Itoa(exitCode))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "CODEXPRO_TEMPORARY_TEST_ROOT="+root)
			cmd.Stdin = strings.NewReader("caller input")
			// The lifecycle owner must replace a caller-supplied group with a
			// fresh one, never target the caller's process group for cleanup.
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: syscall.Getpgrp()}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := RunTemporaryCommand(ctx, cmd)
			if exitCode == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitCode {
					t.Fatalf("raw exit result changed: %v", err)
				}
			}
			if stdout.String() != "caller input\xff" || stderr.String() != "\xfe" {
				t.Fatalf("caller-owned output was changed: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunTemporaryCommandCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(t.TempDir(), "must-not-run")
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "touch \"$1\"", "fixture", marker)
	if err := RunTemporaryCommand(ctx, cmd); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled context changed: %v", err)
	}
	if cmd.Process != nil {
		t.Fatal("pre-cancelled command acquired a process")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-cancelled payload ran: %v", err)
	}
}

func TestRunTemporaryCommandPreservesStartFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-command"))
	if err := RunTemporaryCommand(ctx, cmd); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start failure was replaced: %v", err)
	}
	if cmd.Process != nil {
		t.Fatal("failed start acquired a process")
	}
}
