package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

func TestOpenRetainedWorkspaceRejectsCombinedArrayArguments(t *testing.T) {
	m, files, scope := fixture(t, false)
	workspace, err := m.OpenProject(scope, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(m, files)
	for _, projects := range []any{[]any{"test"}, []string{"test"}, []any{}} {
		_, err := service.Call(context.Background(), scope, "open_workspace", map[string]any{"workspace_id": workspace.ID, "project_ids": projects})
		if err == nil || err.Error() != "Use workspace_id alone when opening a retained run workspace." {
			t.Fatalf("combined array argument did not produce the contract error: %v", err)
		}
	}
}

func TestMCPMissingAndUnknownWorkspaceRemainDistinct(t *testing.T) {
	m, _, scope := fixture(t, true)
	_, missing := m.Resolve(context.Background(), scope, "")
	_, unknown := m.Resolve(context.Background(), scope, "wt_not_found")
	if missing == nil || !strings.HasPrefix(missing.Error(), "workspace_id is required") {
		t.Fatalf("missing: %v", missing)
	}
	if unknown == nil || unknown.Error() != "Unknown workspace_id." {
		t.Fatalf("unknown: %v", unknown)
	}
}

func TestManagedWorkspaceListPreservesInsertionAcrossUpdateAndRestart(t *testing.T) {
	m, _, scope := fixture(t, true)
	ids := []string{"wt_z", "wt_a", "wt_m"}
	for _, id := range ids {
		lease := Lease{Version: 2, Revision: 1, WorkspaceID: id, OwnerID: scope.PrincipalID, State: "ready", CreatedAt: "2026-10-31T00:00:00.000Z"}
		if err := m.storeLease(lease); err != nil {
			t.Fatal(err)
		}
	}
	lease, _ := m.leaseSnapshot(ids[0])
	lease.Revision++
	if err := m.storeLease(lease); err != nil {
		t.Fatal(err)
	}
	check := func(manager *Manager) {
		list := manager.WorkspaceList(scope)
		if len(list) != len(ids) {
			t.Fatalf("list length: %d", len(list))
		}
		for i, id := range ids {
			if list[i]["id"] != id {
				t.Fatalf("insertion order changed: %#v", list)
			}
		}
	}
	check(m)
	reopened, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened)
}

func TestLeaseLockReferencesAreBoundedAndDoNotSplitWaiters(t *testing.T) {
	m, _, scope := fixture(t, true)
	for i := 0; i < 1000; i++ {
		_, _ = m.ReleaseWorkspace(context.Background(), scope, fmt.Sprintf("unknown-%d", i))
		_ = m.RemoveWorkspace(context.Background(), scope, fmt.Sprintf("unknown-%d", i))
	}
	var inside atomic.Int32
	var split atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				lock, release := m.leaseLock("shared")
				lock.Lock()
				if inside.Add(1) != 1 {
					split.Store(true)
				}
				inside.Add(-1)
				lock.Unlock()
				release()
			}
		}()
	}
	wg.Wait()
	if split.Load() {
		t.Fatal("waiters were split across two ownership locks")
	}
	m.leaseLocks.mu.Lock()
	count := len(m.leaseLocks.entries)
	m.leaseLocks.mu.Unlock()
	if count != 0 {
		t.Fatalf("inactive/invalid leases retained %d lock entries", count)
	}
}

func fakeGit(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	path := filepath.Join(bin, "git")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env python3\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

func waitFile(t *testing.T, path string, limit time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not become ready: %s", filepath.Base(path))
	return nil
}

func TestGitCancellationAndPipeHolderCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "exit", "nonzero", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := fixture(t, false)
			m.cfg.Set("maxOutputBytes", 4000)
			root := t.TempDir()
			fakeGit(t, `import os, pathlib, signal, subprocess, sys, time
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
`)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := m.runGit(ctx, root, mode); done <- err }()
			pid, err := strconv.Atoi(strings.TrimSpace(string(waitFile(t, filepath.Join(root, "holder.pid"), 2*time.Second))))
			if err != nil {
				t.Fatal(err)
			}
			identity := process.ProcessIdentity(pid)
			if identity == "" || !process.ProcessAlive(pid, identity) {
				t.Fatal("pipe holder was not active at cancellation boundary")
			}
			defer func() {
				if process.ProcessAlive(pid, identity) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}()
			started := time.Now()
			if mode == "cancel" {
				cancel()
			} else if err = os.WriteFile(filepath.Join(root, "go"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("Git reported success despite interrupted/incomplete output")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Git cleanup exceeded its bounded grace period")
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation changed error ownership: %v", err)
			}
			if mode == "overflow" && (err.Error() != "Git output exceeded 4000 bytes." || ctx.Err() != nil) {
				t.Fatalf("overflow was not detected before timeout: %v", err)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("unbounded process cleanup")
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			for process.ProcessAlive(pid, identity) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if process.ProcessAlive(pid, identity) {
				t.Fatal("descendant survived direct-exit/cancellation/output cleanup")
			}
		})
	}
}

func TestGitOutputCapsRemainPerStream(t *testing.T) {
	m, _, _ := fixture(t, false)
	m.cfg.Set("maxOutputBytes", 4000)
	fakeGit(t, "import sys\nsys.stdout.write('x' * 3000)\nsys.stderr.write('y' * 3000)")
	output, err := m.runGit(context.Background(), t.TempDir(), "per-stream")
	if err != nil || output != strings.Repeat("x", 3000) {
		t.Fatalf("combined cap replaced source per-stream limit: bytes=%d err=%v", len(output), err)
	}
}

func TestLiveProvisioningCannotBeReconciledAsInterrupted(t *testing.T) {
	m, _, scope := fixture(t, true)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	control := t.TempDir()
	fakeGit(t, fmt.Sprintf(`import os, pathlib, sys, time
control = pathlib.Path(%q)
if sys.argv[1:3] == ['worktree', 'add']:
    (control / 'entered').write_text('entered')
    deadline = time.monotonic() + 5
    while not (control / 'release').exists() and time.monotonic() < deadline: time.sleep(.01)
os.execv(%q, [%q] + sys.argv[1:])
`, control, git, git))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		handle Handle
		err    error
	}
	done := make(chan result, 1)
	go func() {
		h, err := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test", "idempotency_key": "long-checkout"})
		done <- result{h, err}
	}()
	waitFile(t, filepath.Join(control, "entered"), 2*time.Second)
	if err := m.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	for _, lease := range m.leases {
		if lease.State != "provisioning" {
			t.Errorf("active checkout reclassified as %s", lease.State)
		}
	}
	m.mu.RUnlock()
	if err := os.WriteFile(filepath.Join(control, "release"), []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.handle.Lease.State != "ready" {
			t.Fatalf("accepted provisioning did not complete: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provisioning did not finish after release")
	}
}
