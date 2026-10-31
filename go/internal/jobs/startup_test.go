package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func waitStartupFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned startup fixture was never entered: %s", path)
}

func TestPausedRecoveryWaitsForActivationAndDoesNotBlockReads(t *testing.T) {
	initial, cfg, resolver := newFixture(t)
	record, _, err := initial.admit(opts(resolver, "ungranted startup fixture"))
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is needed for the bounded private helper fixture")
	}
	helper := filepath.Join(resolver.workspace.Root, "owned-startup-helper")
	entered, release := helper+".entered", helper+".release"
	run, registration := strings.Repeat("a", 32), strings.Repeat("b", 32)
	script := fmt.Sprintf(`#!%s
import json, os, pathlib, sys, time
assert sys.argv[1:] == ["external", "cancel", %q, "--registration-id", %q]
pathlib.Path(%q).write_text(json.dumps({"pid": os.getpid(), "nice": os.getpriority(os.PRIO_PROCESS, 0)}))
deadline = time.monotonic() + 4
while not pathlib.Path(%q).exists():
    if time.monotonic() >= deadline: sys.exit(125)
    time.sleep(.01)
print(json.dumps({"run_id": %q, "status": "cancelled", "cancelled": True, "quiescent": True}))
`, python, run, registration, entered, release, run)
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = initial.update(record.ID, func(next *Record) bool {
		next.Status, next.LaunchState = "failed", "prepared"
		next.FinishedAtMS = time.Now().UnixMilli()
		next.ResourceClaim = &process.ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: registration}
		next.Quiescent = false
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = initial.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer os.WriteFile(release, []byte("release owned helper"), 0600)
	if s.BackgroundReady() {
		t.Fatal("paused service admitted background work")
	}
	// This observable barrier distinguishes a paused constructor from an
	// immediately started worker which merely happens not to have been scheduled.
	time.Sleep(60 * time.Millisecond)
	if _, err = os.Stat(entered); !os.IsNotExist(err) {
		t.Fatalf("constructor entered recovery: %v", err)
	}
	s.StartBackground()
	s.StartBackground()
	waitStartupFile(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := s.Call(ctx, wire.Scope{}, "jobs", map[string]any{"workspace_id": resolver.workspace.ID,
		"job_ids": []string{record.ID}, "output": "none", "wait_ms": 0})
	if err != nil || result.IsError {
		t.Fatalf("read waited for blocked recovery: %v %+v", err, result)
	}
	current, ok := s.Get(record.ID, resolver.workspace.ID)
	if !ok || current.Quiescent || current.ResourceClaim == nil || current.ResourceClaim.RunID != run {
		t.Fatal("blocked helper released or lost exact ownership")
	}
	if err = os.WriteFile(release, []byte("release owned helper"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ = s.Get(record.ID, resolver.workspace.ID)
		if current.Quiescent {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !current.Quiescent {
		t.Fatal("released helper's exact cancellation was never settled")
	}
}

func TestPausedScopeProbeIsBackgroundAndAdmissionFailsClosed(t *testing.T) {
	initial, cfg, resolver := newFixture(t)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	cfg.Set("jobScopes", true)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is needed for the bounded private scope fixture")
	}
	bin := filepath.Join(resolver.workspace.Root, "control-bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	entered, release := filepath.Join(bin, "probe-entered"), filepath.Join(bin, "probe-release")
	script := fmt.Sprintf(`#!%s
import pathlib, sys, time
assert "--scope" in sys.argv and sys.argv[-1] == "true"
pathlib.Path(%q).write_text("owned probe")
deadline = time.monotonic() + 4
while not pathlib.Path(%q).exists():
    if time.monotonic() >= deadline: sys.exit(125)
    time.sleep(.01)
`, python, entered, release)
	if err := os.WriteFile(filepath.Join(bin, "systemd-run"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer os.WriteFile(release, []byte("release owned probe"), 0600)
	if _, err := os.Stat(entered); !os.IsNotExist(err) {
		t.Fatal("paused constructor ran the host scope probe")
	}
	s.StartBackground()
	waitStartupFile(t, entered)
	_, _, err = s.admit(opts(resolver, "must not launch while probe is pending"))
	if typed, ok := err.(*wire.Error); !ok || typed.Code != "server_restarting" {
		t.Fatalf("pending probe did not fail admission closed: %v", err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("pending capability reserved a new child")
	}
	if err = os.WriteFile(release, []byte("release owned probe"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !s.BackgroundReady() {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.BackgroundReady() {
		t.Fatal("successful capability probe did not enable admission")
	}
}

func TestPausedStartAndCloseSerializeWorkerLifetime(t *testing.T) {
	initial, cfg, resolver := newFixture(t)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func(closeIt bool) {
			defer group.Done()
			if closeIt {
				_ = s.Close()
			} else {
				s.StartBackground()
			}
		}(i%3 == 0)
	}
	group.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.StartBackground()
	if s.BackgroundReady() {
		t.Fatal("closed service restarted workers")
	}
}
