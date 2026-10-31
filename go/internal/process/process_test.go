package process

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "runner" {
		if e := RunRunner(os.Args[2:]); e != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRenderedRecordsNeverPublishPartialCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	w, e := NewOutputWriter(path, []PathRedaction{{"/private/check", "[workspace:w]"}}, NewRenderBudget(4096))
	if e != nil {
		t.Fatal(e)
	}
	value := "sk-" + strings.Repeat("z", 24)
	for _, chunk := range []string{"Authorization: Bea", "rer " + value[:9], value[9:]} {
		if _, e = w.Write([]byte(chunk)); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != 0 {
		t.Fatalf("partial record was exposed: %q", b)
	}
	if _, e = w.Write([]byte("\n/private/check/file /private/check-other\n")); e != nil {
		t.Fatal(e)
	}
	if e = w.Finish(); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(path)
	if bytes.Contains(b, []byte(value)) || !bytes.Contains(b, []byte("[REDACTED_SECRET]")) {
		t.Fatalf("credential redaction failed: %q", b)
	}
	if !bytes.Contains(b, []byte("[workspace:w]/file /private/check-other")) {
		t.Fatalf("path boundary failed: %q", b)
	}
}

func TestRenderedUTF8BudgetAndBinaryRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	w, e := NewOutputWriter(path, nil, NewRenderBudget(1024))
	if e != nil {
		t.Fatal(e)
	}
	for _, chunk := range [][]byte{{0xe2}, {0x82}, {0xac, '\n'}, {'x', 0, 'y', '\n'}} {
		if _, e = w.Write(chunk); e != nil {
			t.Fatal(e)
		}
	}
	_, e = w.Write(bytes.Repeat([]byte{'x'}, maxRecordBytes+10))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = w.Write([]byte("\n"))
	if e = w.Finish(); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	if !utf8.Valid(b) || !bytes.HasPrefix(b, []byte("€\n")) || bytes.Count(b, []byte(omittedRecord)) != 2 {
		t.Fatalf("invalid rendered records: %q", b)
	}
	if len(b) > 1024 {
		t.Fatal("render budget exceeded")
	}
}

func TestScopeRejectsForeignIncarnationBeforeSignal(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "signal")
	script := "#!/bin/sh\ncase \"$2\" in\nshow) printf 'LoadState=loaded\\nActiveState=active\\nInvocationID=" + strings.Repeat("b", 32) + "\\nControlGroup=/user.slice/fake.scope\\n';;\nkill) touch '" + marker + "';;\nesac\n"
	if e := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	boot, e := BootID()
	if e != nil {
		t.Skip(e)
	}
	unit := "codexpro-job_ab12-" + strings.Repeat("a", 32) + ".scope"
	s := &ScopeIdentity{Version: 1, Unit: unit, BootID: boot, InvocationID: strings.Repeat("a", 32), Cgroup: "/user.slice/" + unit}
	if got := InspectScope(context.Background(), s); got != ScopeMismatch {
		t.Fatalf("got %s", got)
	}
	if SignalScope(context.Background(), s, syscall.SIGKILL) {
		t.Fatal("foreign owner was signalled")
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("kill command was executed")
	}
	s.Cgroup = "/../../" + unit
	if ValidScopeIdentity(s, "") {
		t.Fatal("traversal identity accepted")
	}
}

func testSpec(t *testing.T, command string, timeout time.Duration, limit int64) (Spec, string) {
	t.Helper()
	dir := t.TempDir()
	nonce, e := RandomHex(24)
	if e != nil {
		t.Fatal(e)
	}
	s := Spec{Version: 1, Command: command, Cwd: dir, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"), Exit: filepath.Join(dir, "exit"), Result: filepath.Join(dir, "result"), Control: filepath.Join(dir, "control"), OutputDir: filepath.Join(dir, "rendered"), Grant: filepath.Join(dir, "grant"), Nonce: nonce, Deadline: time.Now().Add(timeout).UnixMilli(), TimeoutMS: timeout.Milliseconds(), Limit: limit, Nice: 10}
	p := filepath.Join(dir, "spec.json")
	if e = WriteJSON(p, s); e != nil {
		t.Fatal(e)
	}
	return s, p
}

func runSpec(t *testing.T, s Spec, path string, grant bool) Result {
	t.Helper()
	if grant {
		if e := ExclusiveWrite(s.Grant, []byte(s.Nonce), 0600); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "runner", path)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("runner failed: %v %s", e, output)
	}
	var result Result
	if e := ReadJSON(s.Result, &result, 16384); e != nil {
		t.Fatal(e)
	}
	return result
}

func TestRunnerRequiresGrantBeforePayload(t *testing.T) {
	s, p := testSpec(t, "touch must-not-run", 250*time.Millisecond, 4096)
	r := runSpec(t, s, p, false)
	if r.StopReason == nil || *r.StopReason != "timeout" || !r.Quiescent {
		t.Fatalf("bad result: %+v", r)
	}
	if _, e := os.Stat(filepath.Join(s.Cwd, "must-not-run")); !os.IsNotExist(e) {
		t.Fatal("ungranted payload ran")
	}
}

func TestRunnerDeadlineAndBoundedCapture(t *testing.T) {
	s, p := testSpec(t, "printf hello; sleep 30", 1200*time.Millisecond, 4096)
	r := runSpec(t, s, p, true)
	if r.StopReason == nil || *r.StopReason != "timeout" || !r.Quiescent {
		t.Fatalf("deadline not enforced: %+v", r)
	}
	b, _ := os.ReadFile(filepath.Join(s.OutputDir, "stdout.log"))
	if string(b) != "hello" {
		t.Fatalf("partial output lost: %q", b)
	}
	s, p = testSpec(t, "head -c 65536 /dev/zero", 3*time.Second, 512)
	r = runSpec(t, s, p, true)
	if r.StopReason == nil || *r.StopReason != "output_limit" {
		t.Fatalf("limit not enforced: %+v", r)
	}
	info, e := os.Stat(s.Stdout)
	if e != nil {
		t.Fatal(e)
	}
	if info.Size() > 512 {
		t.Fatalf("raw cap exceeded: %d", info.Size())
	}
}

func TestRunnerDrainsSurvivingCommandGroup(t *testing.T) {
	s, p := testSpec(t, "sleep 30 </dev/null >/dev/null 2>&1 & printf '%s' $!", 4*time.Second, 4096)
	r := runSpec(t, s, p, true)
	if !r.Quiescent || GroupAlive(r.CommandPID) {
		t.Fatalf("surviving process group: %+v", r)
	}
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatalf("parent outcome changed: %+v", r)
	}
}

func TestHostResourceCallsBindIndependentIDs(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	run, registration := strings.Repeat("a", 32), strings.Repeat("b", 32)
	script := "#!/bin/sh\ncase \"$2\" in\nbegin) printf '%s' '{\"run_id\":\"" + run + "\",\"registration_id\":\"" + registration + "\",\"scratch\":\"/private/scratch\",\"environment\":{},\"owner\":{}}';;\nfinish) printf '%s' '{\"run_id\":\"" + run + "\",\"status\":\"succeeded\",\"quiescent\":true,\"cancelled\":false}';;\ncancel) printf '%s' '{\"run_id\":\"" + run + "\",\"status\":\"succeeded\",\"quiescent\":true,\"cancelled\":false}';;\nesac\n"
	if e := os.WriteFile(helper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	c := ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: registration}
	a, e := BeginResources(context.Background(), c, dir, "fresh.scope")
	if e != nil {
		t.Fatal(e)
	}
	if a.RunID != run || a.RegistrationID != registration {
		t.Fatal("identity lost")
	}
	if _, e = FinishResources(context.Background(), c, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = CancelResources(context.Background(), c); e == nil {
		t.Fatal("non-cancelled terminal receipt treated as revoked")
	}
	var encoded bytes.Buffer
	if e = json.NewEncoder(&encoded).Encode(c); e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(encoded.Bytes(), []byte(registration)) {
		t.Fatal("claim cannot be recovered")
	}
}
