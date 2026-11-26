package jobs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
)

func installLauncherTestRunner(t *testing.T, s *Service, cfg *config.Config, resolver testResolver, body string) {
	t.Helper()
	path := filepath.Join(resolver.workspace.Root, "private-launcher-runner")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	// Match runnerExecutable selection, including after the restart test
	// reopens the service. The diagnostic executable must remain unchanged.
	s.executable = path
	cfg.Set("runnerExecutable", path)
}

func waitLauncherTerminal(t *testing.T, s *Service, id string) Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.wait(ctx, []string{id}, "all", 8*time.Second, false); err != nil {
		t.Fatal(err)
	}
	record, ok := s.Get(id, "")
	if !ok || unfinished(record) {
		t.Fatalf("launcher job did not become terminal: %+v", record)
	}
	return record
}

func launcherGateBody(entered, release string) string {
	return "printf entered > " + recoveryQuote(entered) + "\n" +
		"n=0\nwhile [ ! -f " + recoveryQuote(release) + " ]; do\n" +
		"  n=$((n+1))\n  [ \"$n\" -lt 500 ] || exit 124\n  sleep 0.02\ndone\n"
}

func TestLauncherFailureKeepsObservedExitAndCause(t *testing.T) {
	for _, code := range []int{2, 125} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s, cfg, resolver := newFixture(t)
			secret := "sk-" + strings.Repeat("z", 24)
			failedPath := filepath.Join(resolver.workspace.Root, "plans-heavy-runs")
			body := "printf '%s\\n' 'pre-runner entered'\n" +
				"printf '%s\\n' " + recoveryQuote(failedPath+": [Errno 122] Disk quota exceeded") + " >&2\n" +
				"printf '%s' 'Authorization: Bea' >&2\n" +
				"printf '%s\\n' " + recoveryQuote("rer "+secret) + " >&2\n" +
				fmt.Sprintf("exit %d\n", code)
			installLauncherTestRunner(t, s, cfg, resolver, body)
			payload := filepath.Join(resolver.workspace.Root, "payload-must-not-run")
			started, err := s.start(context.Background(), opts(resolver, "touch "+recoveryQuote(payload)))
			if err != nil {
				t.Fatal(err)
			}
			result := waitLauncherTerminal(t, s, started.ID)
			if result.Status != "failed" || result.StopReason != "launch_failed" ||
				result.ResultSource != "launcher" || result.ExitCode == nil || *result.ExitCode != code ||
				result.Signal != "" || result.LaunchResult == nil || !result.Quiescent {
				t.Fatalf("pre-runner failure lost its measured source: %+v", result)
			}
			if _, err := os.Stat(payload); !os.IsNotExist(err) {
				t.Fatal("payload executed after pre-runner failure")
			}
			var runner process.Started
			if err := process.ReadJSON(result.SpecPath+".started", &runner, 16384); err != nil || !runner.Revoked {
				t.Fatalf("failed launch did not retain runner revocation: %+v %v", runner, err)
			}
			output, err := s.ReadTail(context.Background(), result.ID, result.WorkspaceID, 8192)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.Stderr, "[workspace:test-workspace]/plans-heavy-runs") ||
				!strings.Contains(output.Stderr, "Disk quota exceeded") ||
				!strings.Contains(output.Stderr, "[REDACTED_SECRET]") ||
				strings.Contains(output.Stderr, secret) || strings.Contains(output.Stderr, failedPath) {
				t.Fatalf("public diagnostic omitted or exposed the cause: %q", output.Stderr)
			}
			view := s.jobView(result, &output, true)
			if view["signal"] != nil || view["result_source"] != "launcher" || view["exit_code"] != code {
				t.Fatalf("public launcher outcome differs from original: %#v", view)
			}
			files := view["output_files"].(map[string]any)
			if !strings.HasSuffix(files["stderr"].(string), "/"+launcherStderrName) {
				t.Fatal("public output still points at the empty payload stream")
			}
		})
	}
}

func TestLauncherPayloadResultWinsOverOuterSuccess(t *testing.T) {
	s, _, resolver := newFixture(t)
	started, err := s.start(context.Background(), opts(resolver, "printf actual-payload; exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	result := waitLauncherTerminal(t, s, started.ID)
	if result.Status != "failed" || result.ExitCode == nil || *result.ExitCode != 7 ||
		result.StopReason != "" || result.Signal != "" || result.ResultSource == "launcher" ||
		result.LaunchResult == nil || result.LaunchResult.ExitCode == nil || *result.LaunchResult.ExitCode != 0 {
		t.Fatalf("outer launcher overwrote the payload result: %+v", result)
	}
	output, err := s.ReadTail(context.Background(), result.ID, result.WorkspaceID, 8192)
	if err != nil || output.Stdout != "actual-payload" {
		t.Fatalf("payload output changed: %+v %v", output, err)
	}
}

func TestLauncherLegacyAcceptedRecordKeepsOriginalRoute(t *testing.T) {
	s, _, resolver := newFixture(t)
	options := opts(resolver, "printf legacy-payload")
	record, _, err := s.admit(options)
	if err != nil {
		t.Fatal(err)
	}
	record, err = s.update(record.ID, func(next *Record) bool {
		next.LauncherVersion = 0
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.launch(context.Background(), options, record); err != nil {
		t.Fatal(err)
	}
	result := waitLauncherTerminal(t, s, record.ID)
	if result.Status != "succeeded" || result.LauncherVersion != 0 || result.LaunchResult != nil {
		t.Fatalf("legacy accepted execution acquired a new launcher contract: %+v", result)
	}
	if _, err := os.Stat(s.launcherPath(result, launcherSpecName)); !os.IsNotExist(err) {
		t.Fatal("legacy execution created a new launch specification")
	}
	output, err := s.ReadTail(context.Background(), result.ID, result.WorkspaceID, 8192)
	if err != nil || output.Stdout != "legacy-payload" {
		t.Fatalf("legacy output changed: %+v %v", output, err)
	}
}

func TestLauncherRestartRecoversMissingSavedPIDWithoutReplay(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	entered := filepath.Join(resolver.workspace.Root, "wrapper-entered")
	release := filepath.Join(resolver.workspace.Root, "release-wrapper")
	calls := filepath.Join(resolver.workspace.Root, "wrapper-calls")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	body := "printf once >> " + recoveryQuote(calls) + "\n" +
		launcherGateBody(entered, release) +
		"printf '%s\\n' 'quota failure after reattach' >&2\nexit 125\n"
	installLauncherTestRunner(t, s, cfg, resolver, body)
	record, err := s.start(context.Background(), opts(resolver, "printf must-not-run"))
	if err != nil {
		t.Fatal(err)
	}
	waitStartupFile(t, entered)
	var marker process.Started
	if err := process.ReadJSON(s.launcherPath(record, launcherSpecName)+".started", &marker, 16384); err != nil {
		t.Fatal(err)
	}
	record, err = s.update(record.ID, func(next *Record) bool {
		next.PID, next.ProcessIdentity = 0, ""
		next.LaunchState = "launching"
		next.StartedAtMS -= 2000
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_, _ = recovered.wait(ctx, []string{record.ID}, "all", 7*time.Second, false)
		_ = recovered.Close()
	})
	recovered.observeRunning(record)
	attached, ok := recovered.Get(record.ID, "")
	if !ok || attached.Status != "running" || len(recovered.Snapshot()) != 1 {
		t.Fatalf("reattachment discarded or repeated accepted work: %+v", attached)
	}
	if _, err := os.Stat(record.SpecPath + ".started"); !os.IsNotExist(err) {
		t.Fatal("live diagnostic writer was mistaken for an absent launcher")
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	result := waitLauncherTerminal(t, recovered, record.ID)
	var retained process.Started
	if err := process.ReadJSON(recovered.launcherPath(result, launcherSpecName)+".started", &retained, 16384); err != nil {
		t.Fatal(err)
	}
	if result.ID != record.ID || result.ExitCode == nil || *result.ExitCode != 125 ||
		result.ResultSource != "launcher" || retained.PID != marker.PID ||
		retained.ProcessIdentity != marker.ProcessIdentity || string(mustLauncherRead(t, calls)) != "once" {
		t.Fatalf("reattachment changed original execution or evidence: %+v", result)
	}
}

func mustLauncherRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestLauncherOutputPaginationAndExistingPinRetention(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	var body strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "printf '%%s\\n' %s >&2\n", recoveryQuote(
			fmt.Sprintf("row-%02d %s/roster Authorization: Bearer sk-%s", i, resolver.workspace.Root, strings.Repeat("r", 24))))
	}
	body.WriteString("exit 125\n")
	installLauncherTestRunner(t, s, cfg, resolver, body.String())
	started, err := s.start(context.Background(), opts(resolver, "printf must-not-run"))
	if err != nil {
		t.Fatal(err)
	}
	result := waitLauncherTerminal(t, s, started.ID)
	var stdout, stderr strings.Builder
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		if pageNumber > 200 {
			t.Fatal("launcher pagination failed to make progress")
		}
		page, err := s.page(result, cursor, 97)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(page.Stdout) || !utf8.ValidString(page.Stderr) || page.ReturnedBytes > 97 {
			t.Fatalf("invalid launcher page: %+v", page)
		}
		stdout.WriteString(page.Stdout)
		stderr.WriteString(page.Stderr)
		if !page.HasMore {
			if !page.OutputComplete {
				t.Fatal("finished launcher output remained growing")
			}
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("launcher pagination cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if stdout.Len() != 0 || strings.Count(stderr.String(), "[REDACTED_SECRET]") != 40 ||
		strings.Contains(stderr.String(), resolver.workspace.Root) || strings.Contains(stderr.String(), strings.Repeat("r", 24)) {
		t.Fatalf("launcher pages changed or exposed evidence: %q", stderr.String())
	}
	if result.StoredBytes < s.launcherStoredBytes(result) || result.StoredBytes <= int64(stderr.Len()) {
		t.Fatalf("retention omitted launcher metadata or output: %+v", result)
	}
	dependentOptions := opts(resolver, "unlaunched retained-output reader")
	dependentOptions.InputJobIDs = []string{result.ID}
	dependent, _, err := s.admit(dependentOptions)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set("maxRetainedJobBytes", 0)
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	protected, _ := s.Get(result.ID, "")
	if protected.OutputExpired || !bytes.Equal(mustLauncherRead(t, s.launcherPath(result, launcherStderrName)), []byte(stderr.String())) {
		t.Fatal("existing input pin did not preserve launcher evidence")
	}
	completeAdmission(t, s, dependent)
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	expired, _ := s.Get(result.ID, "")
	if !expired.OutputExpired || expired.LaunchResult == nil || expired.CapturedStderrBytes != int64(stderr.Len()) {
		t.Fatalf("retention removed the durable receipt or lost captured size: %+v", expired)
	}
	if _, err := os.Stat(s.outputDir(result)); !os.IsNotExist(err) {
		t.Fatal("expired launcher output directory survived cleanup")
	}
	if _, err := s.page(expired, "", 128); errorCode(err) != "job_output_expired" {
		t.Fatalf("expired launch output was still readable: %v", err)
	}
}

func TestLauncherFinalizationWaitsForDiagnosticWriterBeforePrune(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	realRunner := s.executable
	entered := filepath.Join(resolver.workspace.Root, "payload-complete-wrapper-held")
	release := filepath.Join(resolver.workspace.Root, "release-diagnostics")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	body := recoveryQuote(realRunner) + " \"$@\"\n" +
		launcherGateBody(entered, release) +
		"printf '%s\\n' 'late wrapper diagnostic' >&2\nexit 0\n"
	installLauncherTestRunner(t, s, cfg, resolver, body)
	record, err := s.start(context.Background(), opts(resolver, "printf seven; exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	waitStartupFile(t, entered)
	cfg.Set("maxRetainedJobBytes", 0)
	s.observeRunning(record)
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	held, _ := s.Get(record.ID, "")
	if held.Status != "running" || held.OutputExpired {
		t.Fatalf("payload completion released a live diagnostic writer: %+v", held)
	}
	if _, err := os.Stat(s.outputDir(held)); err != nil {
		t.Fatalf("active launcher evidence was pruned: %v", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	result := waitLauncherTerminal(t, s, record.ID)
	if result.ExitCode == nil || *result.ExitCode != 7 || result.ResultSource == "launcher" ||
		result.LaunchResult == nil || result.LaunchResult.ExitCode == nil || *result.LaunchResult.ExitCode != 0 {
		t.Fatalf("late capture changed payload authority: %+v", result)
	}
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	// Join any background prune that published expiry before removing files.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.outputDir(result)); !os.IsNotExist(err) {
		t.Fatal("finished diagnostic writer retained its expired directory")
	}
}

func TestLauncherPostPayloadStopAndDeadlineKeepPayloadOutcome(t *testing.T) {
	for _, reason := range []string{"stopped", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			s, cfg, resolver := newFixture(t)
			realRunner := s.executable
			entered := filepath.Join(resolver.workspace.Root, "completed-payload-wrapper-held")
			release := filepath.Join(resolver.workspace.Root, "release-held-wrapper")
			wrapperCalls := filepath.Join(resolver.workspace.Root, "wrapper-calls")
			payloadCalls := filepath.Join(resolver.workspace.Root, "payload-calls")
			naturalExit := filepath.Join(resolver.workspace.Root, "wrapper-exited-without-stop")
			t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
			body := "printf once >> " + recoveryQuote(wrapperCalls) + "\n" +
				recoveryQuote(realRunner) + " \"$@\"\n" +
				"printf '%s\\n' 'payload complete; wrapper still held' >&2\n" +
				"printf entered > " + recoveryQuote(entered) + "\n" +
				"n=0\nwhile [ ! -f " + recoveryQuote(release) + " ]; do\n" +
				"  n=$((n+1))\n  [ \"$n\" -lt 1200 ] || break\n  sleep 0.02\ndone\n" +
				"printf natural > " + recoveryQuote(naturalExit) + "\nexit 0\n"
			installLauncherTestRunner(t, s, cfg, resolver, body)
			options := opts(resolver, "printf once >> "+recoveryQuote(payloadCalls)+"; printf actual-payload; exit 7")
			options.TimeoutMS = 15000
			if reason == "timeout" {
				options.TimeoutMS = 3000
			}
			record, err := s.start(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			waitStartupFile(t, entered)
			originalPayload := mustLauncherRead(t, record.ResultPath)
			var payload process.Result
			if err := process.ReadJSON(record.ResultPath, &payload, 16384); err != nil ||
				payload.Version != 2 || payload.ExitCode == nil || *payload.ExitCode != 7 ||
				payload.FinishedAtMS <= 0 || payload.FinishedAtMS >= record.DeadlineMS {
				t.Fatalf("fixture did not finish its payload before the deadline: %+v %v", payload, err)
			}
			var runner, shim process.Started
			if err := process.ReadJSON(record.SpecPath+".started", &runner, 16384); err != nil ||
				process.ObserveProcess(runner.PID, runner.ProcessIdentity) != "dead" {
				t.Fatalf("completed runner remained live: %+v %v", runner, err)
			}
			if err := process.ReadJSON(s.launcherPath(record, launcherSpecName)+".started", &shim, 16384); err != nil ||
				shim.PID != record.PID || shim.ProcessIdentity != record.ProcessIdentity ||
				process.ObserveProcess(shim.PID, shim.ProcessIdentity) != "active" {
				t.Fatalf("held launcher lost its original identity: %+v %v", shim, err)
			}
			if reason == "stopped" {
				if _, err := s.Stop(record.ID, "", reason); err != nil {
					t.Fatal(err)
				}
			}
			var requested Record
			requestDeadline := time.UnixMilli(record.DeadlineMS).Add(2 * time.Second)
			if reason == "stopped" {
				requestDeadline = time.Now().Add(2 * time.Second)
			}
			for time.Now().Before(requestDeadline) {
				requested, _ = s.Get(record.ID, "")
				if requested.RequestedStopReason != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if requested.Status != "running" || requested.RequestedStopReason != reason ||
				requested.StopRequestedAtMS <= payload.FinishedAtMS {
				t.Fatalf("payload result suppressed the later stop/deadline: %+v", requested)
			}
			if reason == "timeout" && requested.StopRequestedAtMS < record.DeadlineMS {
				t.Fatal("timeout was requested before the accepted deadline")
			}
			// Exercise the real five-second grace. Do not backdate timestamps or
			// call settlement directly: the monitor must reach it with a result present.
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			terminalWaitDeadline := time.Now().Add(14 * time.Second)
			if _, err := s.wait(ctx, []string{record.ID}, "all", time.Until(terminalWaitDeadline), false); err != nil {
				t.Fatal(err)
			}
			result, ok := s.Get(record.ID, "")
			if !ok || result.Status != "failed" || result.ID != record.ID ||
				result.ExitCode == nil || *result.ExitCode != 7 || result.Signal != "" ||
				result.StopReason != "" || result.RequestedStopReason != reason ||
				result.ResultSource == "launcher" || result.FinishedAtMS != payload.FinishedAtMS {
				t.Fatalf("stopping diagnostics rewrote or stranded the completed payload: %+v", result)
			}
			if time.Now().UnixMilli()-requested.StopRequestedAtMS < 5000 {
				t.Fatal("held launcher was terminated before the existing stop grace")
			}
			// A capture receipt permits terminal publication before the launcher
			// exits. Observe its original identity within the same wait budget.
			launcherState := process.ObserveProcess(shim.PID, shim.ProcessIdentity)
			for launcherState != "dead" {
				remaining := time.Until(terminalWaitDeadline)
				if remaining <= 0 {
					break
				}
				time.Sleep(min(10*time.Millisecond, remaining))
				launcherState = process.ObserveProcess(shim.PID, shim.ProcessIdentity)
			}
			if launcherState != "dead" {
				var launcherReceipt process.LaunchResult
				receiptErr := process.ReadJSON(s.launcherPath(result, launcherResultName), &launcherReceipt, 32768)
				t.Fatalf("original launcher did not exit within the terminal wait budget: state=%q shim=%+v retained_launcher=%+v launcher_receipt=%+v receipt_error=%v",
					launcherState, shim, result.LaunchResult, launcherReceipt, receiptErr)
			}
			if _, err := os.Stat(naturalExit); !os.IsNotExist(err) {
				t.Fatal("wrapper exited naturally instead of exercising stop settlement")
			}
			if len(s.Snapshot()) != 1 || string(mustLauncherRead(t, wrapperCalls)) != "once" ||
				string(mustLauncherRead(t, payloadCalls)) != "once" ||
				!bytes.Equal(mustLauncherRead(t, record.ResultPath), originalPayload) {
				t.Fatal("stop settlement replaced original execution or payload evidence")
			}
			output, err := s.ReadTail(context.Background(), record.ID, record.WorkspaceID, 8192)
			if err != nil || output.Stdout != "actual-payload" {
				t.Fatalf("payload output changed during launcher settlement: %+v %v", output, err)
			}
		})
	}
}

func TestLauncherMissingIdentityIsRevokedBeforeLateDispatch(t *testing.T) {
	s, _, resolver := queueFixture(t)
	options := opts(resolver, "touch must-not-run")
	record, _, err := s.admit(options)
	if err != nil {
		t.Fatal(err)
	}
	writeRecoverySpec(t, s, record, resolver.workspace.Root)
	if _, _, err := s.launcherCommand(record, options, s.executable, []string{"runner", record.SpecPath}); err != nil {
		t.Fatal(err)
	}
	record, err = s.update(record.ID, func(next *Record) bool {
		next.LaunchState = "launching"
		next.StartedAtMS -= 2000
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.entry(record.ID).launching.Store(false)
	s.observeRunning(record)
	current, _ := s.Get(record.ID, "")
	var marker process.Started
	specPath := s.launcherPath(record, launcherSpecName)
	if err := process.ReadJSON(specPath+".started", &marker, 16384); err != nil || !marker.Revoked {
		t.Fatalf("missing identity was not exclusively revoked: %+v %v", marker, err)
	}
	if current.Status != "failed" || current.Signal != "" {
		t.Fatalf("unobserved loss acquired a synthetic signal: %+v", current)
	}
	if err := process.RunLauncher([]string{specPath}); !os.IsExist(err) {
		t.Fatalf("late launcher passed its revoked marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(resolver.workspace.Root, "must-not-run")); !os.IsNotExist(err) {
		t.Fatal("revoked launcher dispatched the payload")
	}
}

func TestLauncherMetadataResultCopiesDoNotSharePointers(t *testing.T) {
	s, _, resolver := queueFixture(t)
	record := occupy(t, s, opts(resolver, "unlaunched"))
	code, signal := 125, "SIGTERM"
	original := &process.LaunchResult{Version: process.LaunchResultVersion, ExitCode: &code, Signal: &signal, FinishedAtMS: time.Now().UnixMilli()}
	record, err := s.update(record.ID, func(next *Record) bool {
		next.LaunchResult = copyLaunchResult(original)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	copied := copyRecord(record)
	*copied.LaunchResult.ExitCode = 9
	*copied.LaunchResult.Signal = "changed"
	retained, _ := s.Get(record.ID, "")
	if *retained.LaunchResult.ExitCode != 125 || *retained.LaunchResult.Signal != "SIGTERM" {
		t.Fatal("a snapshot mutation changed durable launch evidence")
	}
	// Scalar exit values in the public DTO retain the existing wire contract.
	view := s.jobView(record, nil, false)
	if view["launcher"].(map[string]any)["exit_code"] != 125 {
		t.Fatalf("launcher exit was not normalized in the DTO: %#v", view)
	}
}
