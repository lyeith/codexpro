package process

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func launcherHelperArgs(t *testing.T, args ...string) []string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{binary, "-test.run=^TestLauncherHelperProcess$", "--", "launcher-helper"}, args...)
}

func launcherTestSpec(t *testing.T, args []string) (LaunchSpec, string) {
	t.Helper()
	root := t.TempDir()
	return LaunchSpec{
		Version: LaunchSpecVersion, Args: args, Cwd: root,
		Stdout: filepath.Join(root, "launcher.stdout.log"),
		Stderr: filepath.Join(root, "launcher.stderr.log"),
		Result: filepath.Join(root, "launcher.result.json"),
		Limit:  LaunchDiagnosticLimit,
	}, filepath.Join(root, "launcher.spec.json")
}

func runLauncherTest(t *testing.T, spec LaunchSpec, path string) LaunchResult {
	t.Helper()
	if err := WriteJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	if err := RunLauncher([]string{path}); err != nil {
		t.Fatal(err)
	}
	var result LaunchResult
	if err := ReadJSON(spec.Result, &result, 16384); err != nil {
		t.Fatal(err)
	}
	if result.Version != LaunchResultVersion || result.FinishedAtMS == 0 {
		t.Fatalf("invalid durable launch result: %+v", result)
	}
	return result
}

func launcherRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func launcherWaitFile(path string, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestLauncherHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 || args[0] != "launcher-helper" {
		t.Skip("private launcher subprocess")
	}
	switch args[1] {
	case "quota":
		fmt.Fprintln(os.Stdout, "wrapper entered")
		fmt.Fprintln(os.Stderr, "/private/launch/plans-heavy-runs: [Errno 122] Disk quota exceeded")
		secret := "sk-" + strings.Repeat("q", 24)
		for _, part := range []string{"Authorization: Bea", "rer " + secret[:8], secret[8:] + "\n"} {
			fmt.Fprint(os.Stderr, part)
		}
		os.Exit(125)
	case "flood":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("out\n"), 20000))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("err\n"), 20000))
		os.Exit(0)
	case "args":
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Cwd  string
			Args []string
		}{cwd, args[2:]})
		os.Exit(0)
	case "marker":
		var marker Started
		if err := ReadJSON(args[2], &marker, 16384); err != nil || marker.Revoked ||
			marker.PID != os.Getppid() || marker.ProcessIdentity != ProcessIdentity(os.Getppid()) {
			os.Exit(10)
		}
		_ = json.NewEncoder(os.Stdout).Encode(marker)
		os.Exit(0)
	case "file":
		f, err := os.OpenFile(args[2], os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(3)
		}
		_, _ = f.WriteString("payload")
		_ = f.Close()
		fmt.Fprintln(os.Stdout, "launcher")
		os.Exit(0)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(5 * time.Second)
		os.Exit(4)
	case "holder":
		_ = os.WriteFile(args[2]+".ready", []byte("ready"), 0600)
		_ = launcherWaitFile(args[2], time.Now().Add(10*time.Second))
		_ = os.WriteFile(args[2]+".done", []byte("done"), 0600)
		os.Exit(0)
	case "hold":
		binary, _ := os.Executable()
		child := exec.Command(binary, "-test.run=^TestLauncherHelperProcess$", "--", "launcher-helper", "holder", args[2])
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		_ = child.Process.Release()
		if !launcherWaitFile(args[2]+".ready", time.Now().Add(5*time.Second)) {
			os.Exit(6)
		}
		fmt.Fprintln(os.Stderr, "wrapper exited; descendant still owns the pipe")
		os.Exit(0)
	case "gated":
		_ = os.WriteFile(args[2]+".ready", []byte("ready"), 0600)
		if !launcherWaitFile(args[2], time.Now().Add(10*time.Second)) {
			os.Exit(7)
		}
		fmt.Fprintln(os.Stderr, "diagnostic after parent exit")
		_ = os.WriteFile(args[2]+".done", []byte("once"), 0600)
		os.Exit(0)
	case "parent":
		binary, _ := os.Executable()
		child := exec.Command(binary, "launcher", args[2])
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		_ = child.Process.Release()
		os.Exit(0)
	}
	os.Exit(9)
}

func TestLauncherRetainsQuotaExitAndRedactedDiagnostics(t *testing.T) {
	spec, path := launcherTestSpec(t, launcherHelperArgs(t, "quota"))
	spec.PathRedactions = []PathRedaction{{"/private/launch", "[launch-state]"}}
	result := runLauncherTest(t, spec, path)
	if result.ExitCode == nil || *result.ExitCode != 125 || result.Signal != nil {
		t.Fatalf("launcher exit was replaced by a synthetic signal: %+v", result)
	}
	stderr := string(launcherRead(t, spec.Stderr))
	if !strings.Contains(stderr, "[launch-state]/plans-heavy-runs") ||
		!strings.Contains(stderr, "Disk quota exceeded") ||
		!strings.Contains(stderr, "[REDACTED_SECRET]") ||
		strings.Contains(stderr, "/private/launch") || strings.Contains(stderr, strings.Repeat("q", 24)) {
		t.Fatalf("missing or unredacted launch diagnosis: %q", stderr)
	}
	if string(launcherRead(t, spec.Stdout)) != "wrapper entered\n" {
		t.Fatal("launcher stdout was not retained separately")
	}
}

func TestLauncherBudgetDrainsBothStreams(t *testing.T) {
	spec, path := launcherTestSpec(t, launcherHelperArgs(t, "flood"))
	spec.Limit = 256
	result := runLauncherTest(t, spec, path)
	out, errout := launcherRead(t, spec.Stdout), launcherRead(t, spec.Stderr)
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Error != "" || !result.OutputTruncated {
		t.Fatalf("capture limit changed the child outcome: %+v", result)
	}
	if int64(len(out)+len(errout)) > spec.Limit || !utf8.Valid(out) || !utf8.Valid(errout) {
		t.Fatalf("combined rendered limit exceeded: %d + %d", len(out), len(errout))
	}
}

func TestLauncherPassesExactArgsWithoutShell(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	marker := filepath.Join(spec.Cwd, "must-not-exist")
	args := []string{"literal space", "$(touch " + marker + ")", "; exit 44", ""}
	spec.Args = launcherHelperArgs(t, append([]string{"args"}, args...)...)
	result := runLauncherTest(t, spec, path)
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("argument helper failed: %+v", result)
	}
	var got struct {
		Cwd  string
		Args []string
	}
	if err := json.Unmarshal(launcherRead(t, spec.Stdout), &got); err != nil {
		t.Fatal(err)
	}
	if got.Cwd != spec.Cwd || !reflect.DeepEqual(got.Args, args) {
		t.Fatalf("launcher changed cwd/argv: %+v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("launcher evaluated shell syntax")
	}
}

func TestLauncherLeavesPayloadFilesWithTheirWriter(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	payload := filepath.Join(spec.Cwd, "payload.out")
	if err := os.WriteFile(payload, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	spec.Args = launcherHelperArgs(t, "file", payload)
	result := runLauncherTest(t, spec, path)
	if result.ExitCode == nil || *result.ExitCode != 0 || string(launcherRead(t, payload)) != "retainedpayload" {
		t.Fatalf("launcher touched payload-owned output: %+v", result)
	}
	if string(launcherRead(t, spec.Stdout)) != "launcher\n" {
		t.Fatal("launcher evidence was mixed with payload-owned output")
	}
}

func TestLauncherPublishesIdentityBeforeChild(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	spec.Args = launcherHelperArgs(t, "marker", path+".started")
	result := runLauncherTest(t, spec, path)
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("child did not observe its original launcher marker: %+v", result)
	}
	var marker Started
	if err := json.Unmarshal(launcherRead(t, spec.Stdout), &marker); err != nil {
		t.Fatal(err)
	}
	if marker.PID != os.Getpid() || marker.ProcessIdentity != ProcessIdentity(os.Getpid()) || marker.Revoked {
		t.Fatalf("wrong independent launcher identity: %+v", marker)
	}
}

func TestLauncherDuplicateCannotReplayOrReplaceEvidence(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	payload := filepath.Join(spec.Cwd, "payload.once")
	if err := os.WriteFile(payload, nil, 0600); err != nil {
		t.Fatal(err)
	}
	spec.Args = launcherHelperArgs(t, "file", payload)
	first := runLauncherTest(t, spec, path)
	if first.ExitCode == nil || *first.ExitCode != 0 {
		t.Fatalf("first launch failed: %+v", first)
	}
	before := map[string][]byte{}
	for _, evidence := range []string{path + ".started", spec.Stdout, spec.Stderr, spec.Result} {
		before[evidence] = launcherRead(t, evidence)
	}
	if err := RunLauncher([]string{path}); !os.IsExist(err) {
		t.Fatalf("duplicate launch was not refused at its existing marker: %v", err)
	}
	if string(launcherRead(t, payload)) != "payload" {
		t.Fatal("duplicate launch replayed the child")
	}
	for evidence, original := range before {
		if !bytes.Equal(launcherRead(t, evidence), original) {
			t.Fatalf("duplicate launch changed original evidence %s", filepath.Base(evidence))
		}
	}
}

func TestLauncherRecordsSetupAndStartErrors(t *testing.T) {
	for _, kind := range []string{"capture", "start"} {
		t.Run(kind, func(t *testing.T) {
			spec, path := launcherTestSpec(t, []string{"/missing-codexpro-launcher/executable"})
			if kind == "capture" {
				if err := os.Mkdir(spec.Stderr, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result := runLauncherTest(t, spec, path)
			if result.ExitCode != nil || result.Signal != nil || result.Error == "" {
				t.Fatalf("pre-exec failure lacks durable evidence: %+v", result)
			}
			if !strings.Contains(result.Error, "launcher "+kind) {
				t.Fatalf("wrong failure stage: %+v", result)
			}
		})
	}
}

func TestLauncherPreservesObservedSignal(t *testing.T) {
	spec, path := launcherTestSpec(t, launcherHelperArgs(t, "signal"))
	result := runLauncherTest(t, spec, path)
	if result.ExitCode != nil || result.Signal == nil || *result.Signal != "SIGTERM" {
		t.Fatalf("observed launcher signal changed: %+v", result)
	}
}

func TestLauncherBoundsPostExitPipeDrain(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	release := filepath.Join(spec.Cwd, "release-holder")
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte("release"), 0600)
		if !launcherWaitFile(release+".done", time.Now().Add(5*time.Second)) {
			t.Error("owned pipe holder did not exit")
		}
	})
	spec.Args = launcherHelperArgs(t, "hold", release)
	started := time.Now()
	result := runLauncherTest(t, spec, path)
	if time.Since(started) > 5*time.Second || result.ExitCode == nil || *result.ExitCode != 0 ||
		!strings.Contains(result.Error, "WaitDelay") {
		t.Fatalf("post-exit inherited pipe was not bounded: %+v", result)
	}
	if _, err := os.Stat(release + ".done"); !os.IsNotExist(err) {
		t.Fatal("descendant was killed or completed before its owner released it")
	}
}

func TestLauncherSurvivesLaunchingParentExit(t *testing.T) {
	spec, path := launcherTestSpec(t, nil)
	gate := filepath.Join(spec.Cwd, "release-after-parent")
	t.Cleanup(func() { _ = os.WriteFile(gate, []byte("release"), 0600) })
	spec.Args = launcherHelperArgs(t, "gated", gate)
	if err := WriteJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	args := launcherHelperArgs(t, "parent", path)
	parent := exec.Command(args[0], args[1:]...)
	if output, err := parent.CombinedOutput(); err != nil {
		t.Fatalf("private parent failed: %v %s", err, output)
	}
	if !launcherWaitFile(gate+".ready", time.Now().Add(5*time.Second)) {
		t.Fatal("accepted child did not survive its launching parent")
	}
	if _, err := os.Stat(spec.Result); !os.IsNotExist(err) {
		t.Fatal("gated child already had a result")
	}
	if err := os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if !launcherWaitFile(spec.Result, time.Now().Add(5*time.Second)) {
		t.Fatal("surviving launcher did not publish a receipt")
	}
	var result LaunchResult
	if err := ReadJSON(spec.Result, &result, 16384); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 || result.Error != "" ||
		string(launcherRead(t, gate+".done")) != "once" ||
		string(launcherRead(t, spec.Stderr)) != "diagnostic after parent exit\n" {
		t.Fatalf("accepted execution or diagnostics depended on parent lifetime: %+v", result)
	}
}

func TestLauncherCaptureKeepsDrainingAfterStorageError(t *testing.T) {
	budget := NewRenderBudget(256)
	writer, err := NewOutputWriter(filepath.Join(t.TempDir(), "out"), nil, budget)
	if err != nil {
		t.Fatal(err)
	}
	_ = writer.file.Close()
	capture := &launchCapture{writer: writer, budget: budget}
	for _, part := range [][]byte{[]byte("first record\n"), bytes.Repeat([]byte("discard\n"), 10000)} {
		if n, err := capture.Write(part); err != nil || n != len(part) {
			t.Fatalf("storage failure closed the child's stream: n=%d err=%v", n, err)
		}
	}
	if capture.err == nil {
		t.Fatal("capture storage failure was not retained")
	}
	_ = writer.Finish()
}

func TestLauncherRejectsUnconfinedOrAliasedEvidence(t *testing.T) {
	spec, path := launcherTestSpec(t, []string{"true"})
	tests := map[string]func(*LaunchSpec){
		"escape":       func(s *LaunchSpec) { s.Result = filepath.Join(filepath.Dir(s.Cwd), "outside.json") },
		"spec_alias":   func(s *LaunchSpec) { s.Stderr = path },
		"marker_alias": func(s *LaunchSpec) { s.Stderr = path + ".started" },
		"stream_alias": func(s *LaunchSpec) { s.Stderr = s.Stdout },
		"version":      func(s *LaunchSpec) { s.Version++ },
		"limit":        func(s *LaunchSpec) { s.Limit = LaunchDiagnosticLimit + 1 },
		"nul_argument": func(s *LaunchSpec) { s.Args = []string{"true", "bad\x00arg"} },
		"relative_cwd": func(s *LaunchSpec) { s.Cwd = "." },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := spec
			change(&candidate)
			if err := candidate.Validate(path); err == nil {
				t.Fatal("invalid launcher specification accepted")
			}
		})
	}
}
