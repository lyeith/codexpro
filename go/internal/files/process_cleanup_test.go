package files

import (
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

	"github.com/lyeith/codexpro/go/internal/process"
)

func waitProcessFixture(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process fixture never became ready: %s", filepath.Base(path))
	return nil
}

// Exercise the OS boundary, not a mocked Cancel callback: the descendant ignores
// TERM and deliberately holds both pipes beyond its direct parent's lifetime.
func TestRunCommandDrainsDescendantAfterWaitBoundary(t *testing.T) {
	for _, mode := range []string{"cancel", "exit", "nonzero", "overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			timeout := 5 * time.Second
			if mode == "timeout" {
				timeout = 1200 * time.Millisecond
			}
			script := `import os, pathlib, signal, subprocess, sys, time
child_code = "import os,pathlib,signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); pathlib.Path('holder.pid').write_text(str(os.getpid())); time.sleep(30)"
subprocess.Popen([sys.executable, '-c', child_code], stdout=sys.stdout, stderr=sys.stderr)
mode = sys.argv[1]
if mode == 'overflow': signal.signal(signal.SIGTERM, signal.SIG_IGN)
deadline = time.monotonic() + 8
while not pathlib.Path('go').exists() and time.monotonic() < deadline: time.sleep(.01)
if mode == 'exit': sys.exit(0)
if mode == 'nonzero': sys.exit(7)
if mode == 'overflow': sys.stdout.write('x' * 12000); sys.stdout.flush()
time.sleep(30)
`
			done := make(chan ProcessResult, 1)
			go func() {
				done <- RunCommandEnv(ctx, root, "python3", []string{"-c", script, mode}, "", 4000, timeout, []string{"SP6_PROCESS_FIXTURE=1"})
			}()
			pid, err := strconv.Atoi(strings.TrimSpace(string(waitProcessFixture(t, filepath.Join(root, "holder.pid")))))
			if err != nil {
				t.Fatal(err)
			}
			identity := process.ProcessIdentity(pid)
			if identity == "" || !process.ProcessAlive(pid, identity) {
				t.Fatal("fixture descendant was not active before completion")
			}
			defer func() {
				if process.ProcessAlive(pid, identity) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			if mode == "cancel" {
				cancel()
			} else if mode != "timeout" {
				if err = os.WriteFile(filepath.Join(root, "go"), []byte("go"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var result ProcessResult
			select {
			case result = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("process cleanup exceeded its bounded grace period")
			}
			switch mode {
			case "exit":
				if !errors.Is(result.Err, exec.ErrWaitDelay) || result.ExitCode != 0 {
					t.Fatalf("pipe timeout lost its result: %+v", result)
				}
			case "nonzero":
				if result.ExitCode != 7 || result.Err != nil {
					t.Fatalf("normal exit contract changed: %+v", result)
				}
			case "cancel":
				if result.Err == nil || result.TimedOut {
					t.Fatalf("caller cancellation became success or owned timeout: %+v", result)
				}
			case "overflow":
				if !result.Truncated || len(result.Stdout) != 4000 || result.Err == nil || result.Err.Error() != "spawnSync python3 ENOBUFS" || ctx.Err() != nil {
					t.Fatalf("output cap contract changed: %+v", result)
				}
			case "timeout":
				if !result.TimedOut || result.Err == nil || result.Err.Error() != "spawnSync python3 ETIMEDOUT" || ctx.Err() != nil {
					t.Fatalf("timeout ownership changed: %+v", result)
				}
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			for process.ProcessAlive(pid, identity) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if process.ProcessAlive(pid, identity) {
				t.Fatal("owned descendant survived completion")
			}
		})
	}
}

func TestRunCommandRetainsIndependentStreamCaps(t *testing.T) {
	r := RunCommand(context.Background(), t.TempDir(), "python3", []string{"-c", "import sys; sys.stdout.write('x' * 3000); sys.stderr.write('y' * 3000)"}, "", 4000, 3*time.Second)
	if r.Err != nil || r.Truncated || r.ExitCode != 0 || r.Stdout != strings.Repeat("x", 3000) || r.Stderr != strings.Repeat("y", 3000) {
		t.Fatalf("stream caps became combined: %+v", r)
	}
}
