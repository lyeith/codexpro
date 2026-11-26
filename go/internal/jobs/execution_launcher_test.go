package jobs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

// This private TestMain mode executes the real launcher, then keeps that exact
// process alive after its durable receipt. No timing assumption substitutes for
// the observable post-receipt barrier.
func runExecutionHoldLauncherBarrier(args []string) int {
	if len(args) != 2 || args[0] != "launcher" {
		return 111
	}
	if err := process.RunLauncher(args[1:]); err != nil {
		return 112
	}
	dir := filepath.Dir(args[1])
	if err := os.WriteFile(filepath.Join(dir, "test-post-receipt-ready"), []byte("ready"), 0600); err != nil {
		return 113
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "test-post-receipt-release")); err == nil {
			return 0
		}
		select {
		case <-deadline.C:
			return 124
		case <-ticker.C:
		}
	}
}

func TestExecutionHoldWaitsForOriginalLauncherAfterReceiptAndRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exact launcher start identity uses Linux process evidence")
	}
	for _, missingSavedPID := range []bool{false, true} {
		name := "saved_identity"
		if missingSavedPID {
			name = "marker_reattachment"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg, resolver := newFixture(t)
			wrapper := filepath.Join(resolver.workspace.Root, "post-receipt-launcher")
			body := "#!/bin/sh\nexec " + recoveryQuote(s.launcherExecutable) + " test-execution-hold-launcher \"$@\"\n"
			if err := os.WriteFile(wrapper, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			s.launcherExecutable = wrapper
			original, err := s.start(context.Background(), opts(resolver, "printf 'once\\n' >> once; printf payload; exit 7"))
			if err != nil {
				t.Fatal(err)
			}
			dir := s.outputDir(original)
			release := filepath.Join(dir, "test-post-receipt-release")
			var shim process.Started
			t.Cleanup(func() {
				_ = os.WriteFile(release, []byte("release"), 0600)
				if shim.PID > 0 {
					executionHoldTestWait(t, "original fixture launcher cleanup", func() bool {
						return process.ObserveProcess(shim.PID, shim.ProcessIdentity) == "dead"
					})
				}
			})
			terminal := waitLauncherTerminal(t, s, original.ID)
			executionHoldTestWait(t, "actual launcher post-receipt barrier", func() bool {
				_, err := os.Stat(filepath.Join(dir, "test-post-receipt-ready"))
				return err == nil
			})
			if terminal.Status != "failed" || terminal.ExitCode == nil || *terminal.ExitCode != 7 ||
				terminal.Signal != "" || terminal.StopReason != "" || !terminal.Quiescent ||
				terminal.LaunchResult == nil || terminal.LaunchResult.ExitCode == nil || *terminal.LaunchResult.ExitCode != 0 {
				t.Fatalf("ordinary payload publication changed: %+v", terminal)
			}
			if err := process.ReadJSON(s.launcherPath(terminal, launcherSpecName)+".started", &shim, 16384); err != nil ||
				shim.PID != terminal.PID || shim.ProcessIdentity != terminal.ProcessIdentity ||
				process.ObserveProcess(shim.PID, shim.ProcessIdentity) != "active" {
				t.Fatalf("post-receipt launcher lost its original identity: %+v, %v", shim, err)
			}
			if missingSavedPID {
				// Reproduce loss between Start and supplementary PID persistence.
				// The actual launch and its independent marker are unchanged.
				terminal, err = s.update(original.ID, func(next *Record) bool {
					next.PID, next.ProcessIdentity, next.LaunchState = 0, "", "launching"
					return true
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			jobBefore := executionHoldTestPayload(t, s, original.ID)
			markerBefore, err := os.ReadFile(s.launcherPath(terminal, launcherSpecName) + ".started")
			if err != nil {
				t.Fatal(err)
			}
			hold, err := s.BeginExecutionHold("post-receipt-" + name)
			if err != nil || hold.DispatchHeld || len(hold.OutstandingJobs) != 1 ||
				hold.OutstandingJobs[0].ID != original.ID || hold.OutstandingJobs[0].LauncherState != "active" ||
				hold.OutstandingJobs[0].LauncherQuiescent || !hold.OutstandingJobs[0].Quiescent {
				t.Fatalf("live original launcher was omitted from the hold: %+v, %v", hold, err)
			}
			observed, err := s.ObserveExecutionHold(context.Background())
			if err != nil || observed.Complete || len(observed.Owners) != 1 ||
				!observed.Owners[0].PublishedQuiescent || !observed.Owners[0].OwnerQuiescentVerified ||
				observed.Owners[0].LauncherQuiescent || observed.Owners[0].LauncherState != "active" {
				t.Fatalf("payload and launcher quiescence were conflated: %+v, %v", observed, err)
			}
			cfg.Set("maxRetainedJobBytes", 0)
			cfg.Set("maxJobHistoryPerWorkspace", 0)
			if err := s.prune(); err != nil {
				t.Fatal(err)
			}
			if current, _ := s.Get(original.ID, ""); current.OutputExpired {
				t.Fatal("hold pruned the original live launcher's evidence")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			recovered, err := NewPaused(cfg, resolver)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			status := recovered.ExecutionStatus()
			if status.HoldID != hold.HoldID || status.InstanceID == hold.InstanceID || status.DispatchHeld ||
				len(status.OutstandingJobs) != 1 || status.OutstandingJobs[0].ID != original.ID ||
				status.OutstandingJobs[0].LauncherState != "active" || status.OutstandingJobs[0].LauncherQuiescent {
				t.Fatalf("restart lost the original launcher blocker: %+v", status)
			}
			if _, _, err := recovered.admit(opts(resolver, "printf replay")); !IsExecutionHeld(err) {
				t.Fatalf("recovered hold admitted fresh execution: %v", err)
			}
			if after := executionHoldTestPayload(t, recovered, original.ID); !bytes.Equal(after, jobBefore) {
				t.Fatal("hold/restart rewrote the original job outcome or accepted identity")
			}
			if after, err := os.ReadFile(recovered.launcherPath(terminal, launcherSpecName) + ".started"); err != nil || !bytes.Equal(after, markerBefore) {
				t.Fatalf("hold/restart changed the original ownership marker: %q, %v", after, err)
			}
			if observed, err := recovered.ObserveExecutionHold(context.Background()); err != nil || observed.Complete {
				t.Fatalf("read-only recovered observation forgot the active launcher: %+v, %v", observed, err)
			}
			if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			executionHoldTestWait(t, "exact original launcher death", func() bool {
				return process.ObserveProcess(shim.PID, shim.ProcessIdentity) == "dead"
			})
			status, err = recovered.HoldExecutionDispatch(hold.HoldID)
			if err != nil || !status.DispatchHeld || len(status.OutstandingJobs) != 0 ||
				len(status.AcceptedJobs) != 1 || status.AcceptedJobs[0].ID != original.ID ||
				!status.AcceptedJobs[0].LauncherQuiescent || status.AcceptedJobs[0].LauncherState != "dead" {
				t.Fatalf("exact launcher death did not settle its original hold: %+v, %v", status, err)
			}
			observed, err = recovered.ObserveExecutionHold(context.Background())
			if err != nil || !observed.Complete || len(observed.Owners) != 1 ||
				!observed.Owners[0].LauncherQuiescent || !observed.Owners[0].OwnerQuiescentVerified {
				t.Fatalf("settled original could not be observed: %+v, %v", observed, err)
			}
			if len(recovered.Snapshot()) != 1 || !bytes.Equal(executionHoldTestPayload(t, recovered, original.ID), jobBefore) {
				t.Fatal("settlement replayed or changed the original job")
			}
			if body, err := os.ReadFile(filepath.Join(resolver.workspace.Root, "once")); err != nil || string(body) != "once\n" {
				t.Fatalf("original payload did not execute exactly once: %q, %v", body, err)
			}
			if output, err := recovered.ReadTail(context.Background(), original.ID, original.WorkspaceID, 8192); err != nil || output.Stdout != "payload" {
				t.Fatalf("original payload output was not retained: %+v, %v", output, err)
			}
		})
	}
}

func executionLauncherTestRecord(t *testing.T, s *Service, resolver testResolver) Record {
	t.Helper()
	record := occupy(t, s, queueOption(resolver, executionHoldTestScope(), "printf never-dispatched"))
	// Model an original capture directory that already existed at launch.
	if err := os.MkdirAll(s.outputDir(record), 0700); err != nil {
		t.Fatal(err)
	}
	code, launchCode := 7, 0
	next, err := s.update(record.ID, func(next *Record) bool {
		next.Status, next.LaunchState = "failed", "launching"
		next.ExitCode, next.Quiescent, next.QuiescenceScope = &code, true, "process_group"
		next.FinishedAtMS = time.Now().UnixMilli()
		next.LaunchResult = &process.LaunchResult{Version: 1, FinishedAtMS: next.FinishedAtMS, ExitCode: &launchCode}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.entry(record.ID).launching.Store(false)
	return next
}

func TestExecutionHoldLauncherMetadataKeepsUnknownOwners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exact process identity uses Linux process evidence")
	}
	identity := process.ProcessIdentity(os.Getpid())
	if identity == "" {
		t.Fatal("fixture process identity is unavailable")
	}
	for _, tc := range []struct {
		name, marker, wantState string
		version                 int
		savedPID                bool
		conflict                bool
		wantQuiet               bool
	}{
		{"legacy", "malformed", "legacy", 0, false, false, true},
		{"revoked", "revoked", "prevented", 1, false, false, true},
		{"active_marker", "active", "active", 1, false, false, false},
		{"saved_identity_only", "", "active", 1, true, false, false},
		{"conflicting_identity", "active", "unknown", 1, true, true, false},
		{"malformed_marker", "malformed", "unknown", 1, false, false, false},
		{"unreadable_marker", "unreadable", "unknown", 1, false, false, false},
		{"symlink_marker", "symlink", "unknown", 1, false, false, false},
		{"revoked_with_identity", "mixed", "unknown", 1, false, false, false},
		{"unsupported_version", "", "unknown", 2, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.marker == "unreadable" && os.Geteuid() == 0 {
				t.Skip("permission refusal requires an ordinary user")
			}
			s, _, resolver := queueFixture(t)
			record := executionLauncherTestRecord(t, s, resolver)
			record, err := s.update(record.ID, func(next *Record) bool {
				next.LauncherVersion = tc.version
				if tc.savedPID {
					next.PID, next.ProcessIdentity = os.Getpid(), identity
				}
				if tc.conflict {
					next.ProcessIdentity += "-different"
				}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			path := s.launcherPath(record, launcherSpecName) + ".started"
			switch tc.marker {
			case "active", "unreadable":
				err = process.WriteJSON(path, process.Started{PID: os.Getpid(), ProcessIdentity: identity})
			case "revoked":
				err = process.WriteJSON(path, process.Started{Revoked: true})
			case "mixed":
				err = process.WriteJSON(path, process.Started{Revoked: true, PID: os.Getpid()})
			case "malformed":
				err = os.WriteFile(path, []byte("{"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(s.outputDir(record), "missing-marker-target"), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.marker == "unreadable" {
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0600)
			}
			before := executionHoldTestPayload(t, s, record.ID)
			job := s.projectExecutionJob(record, false)
			if job.LauncherState != tc.wantState || job.LauncherQuiescent != tc.wantQuiet {
				t.Fatalf("incorrect original launcher classification: %+v", job)
			}
			status, err := s.BeginExecutionHold("metadata-" + tc.name)
			if err != nil || status.DispatchHeld != tc.wantQuiet {
				t.Fatalf("hold discarded uncertain launcher evidence: %+v, %v", status, err)
			}
			observed, err := s.ObserveExecutionHold(context.Background())
			if err != nil || observed.Complete != tc.wantQuiet {
				t.Fatalf("observation conflated receipt and exact ownership: %+v, %v", observed, err)
			}
			if !bytes.Equal(before, executionHoldTestPayload(t, s, record.ID)) {
				t.Fatal("hold changed original terminal metadata")
			}
			if after := s.projectExecutionJob(record, false); !reflect.DeepEqual(after, job) {
				t.Fatalf("hold changed or replaced original launcher evidence: before %+v, after %+v", job, after)
			}
		})
	}
}

func TestExecutionHoldMissingLauncherMarkerRemainsBlocked(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	record := executionLauncherTestRecord(t, s, resolver)
	entry := s.entry(record.ID)
	entry.launching.Store(true)
	hold, err := s.BeginExecutionHold("unclaimed-launcher")
	if err != nil || hold.DispatchHeld {
		t.Fatalf("hold forgot the original launch continuation: %+v, %v", hold, err)
	}
	entry.launching.Store(false)
	path := s.launcherPath(record, launcherSpecName) + ".started"
	jobBefore, holdBefore := executionHoldTestPayload(t, s, record.ID), executionHoldTestMarker(t, s)
	status := s.ExecutionStatus()
	observed, err := s.ObserveExecutionHold(context.Background())
	if err != nil || status.DispatchHeld || observed.Complete || len(status.OutstandingJobs) != 1 ||
		status.OutstandingJobs[0].LauncherState != "unclaimed" {
		t.Fatalf("missing marker was treated as exact death: %+v, %+v, %v", status, observed, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only status/observe wrote an ownership marker: %v", err)
	}
	if !bytes.Equal(holdBefore, executionHoldTestMarker(t, s)) || !bytes.Equal(jobBefore, executionHoldTestPayload(t, s, record.ID)) {
		t.Fatal("read-only status/observe changed durable control or job evidence")
	}
	status, err = s.HoldExecutionDispatch(hold.HoldID)
	if err != nil || status.DispatchHeld || len(status.OutstandingJobs) != 1 ||
		status.OutstandingJobs[0].ID != record.ID || status.OutstandingJobs[0].LauncherQuiescent {
		t.Fatalf("dispatch converted missing identity into owner proof: %+v, %v", status, err)
	}
	if _, err := s.ResumeExecution(hold.HoldID); errorCode(err) != "execution_hold_incomplete" {
		t.Fatalf("unknown original launcher allowed resume: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	status, err = recovered.HoldExecutionDispatch(hold.HoldID)
	if err != nil || status.HoldID != hold.HoldID || status.DispatchHeld || len(status.OutstandingJobs) != 1 ||
		status.OutstandingJobs[0].ID != record.ID || status.OutstandingJobs[0].LauncherState != "unclaimed" {
		t.Fatalf("restart discarded the original missing-identity blocker: %+v, %v", status, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("hold advancement installed a new marker: %v", err)
	}
	if !bytes.Equal(jobBefore, executionHoldTestPayload(t, recovered, record.ID)) {
		t.Fatal("hold rewrote the original outcome")
	}
}

func TestExecutionHoldMissingLauncherEvidenceRemainsBlocked(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "missing"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			s, _, resolver := queueFixture(t)
			record := executionLauncherTestRecord(t, s, resolver)
			if expired {
				var err error
				record, err = s.update(record.ID, func(next *Record) bool { next.OutputExpired = true; return true })
				if err != nil {
					t.Fatal(err)
				}
			}
			dir := s.outputDir(record)
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			before := executionHoldTestPayload(t, s, record.ID)
			status, err := s.BeginExecutionHold("missing-directory")
			if err != nil || status.DispatchHeld || len(status.OutstandingJobs) != 1 ||
				status.OutstandingJobs[0].LauncherQuiescent {
				t.Fatalf("missing original evidence was silently settled: %+v, %v", status, err)
			}
			observed, err := s.ObserveExecutionHold(context.Background())
			if err != nil || observed.Complete {
				t.Fatalf("missing evidence became owner proof: %+v, %v", observed, err)
			}
			if _, err := os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatalf("hold recreated removed output evidence: %v", err)
			}
			if !bytes.Equal(before, executionHoldTestPayload(t, s, record.ID)) {
				t.Fatal("missing evidence changed the original terminal record")
			}
		})
	}
}
