package process

import (
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuardedOrdinaryStopUsesFrozenAllowance(t *testing.T) {
	claim := guardedTestClaim(t, guardedTestOffer(t))
	spec := Spec{Version: GuardedRunnerSpecVersion, ResourceClaim: &claim}
	for _, reason := range []string{"stopped", "timeout", "output_limit"} {
		got, err := runnerStopAllowance(spec, reason)
		if err != nil || got != 90*time.Second {
			t.Fatalf("%s used %v instead of original 90s allowance: %v", reason, got, err)
		}
	}
	// Internal capture failure cleanup is distinct from ordinary stop grace.
	if got, err := runnerStopAllowance(spec, "lost"); err != nil || got != 1500*time.Millisecond {
		t.Fatalf("failure cleanup changed: %v %v", got, err)
	}
	legacy := Spec{Version: RunnerSpecVersion}
	for _, reason := range []string{"stopped", "timeout", "output_limit", "lost"} {
		if got, err := runnerStopAllowance(legacy, reason); err != nil || got != 1500*time.Millisecond {
			t.Fatalf("legacy %s changed: %v %v", reason, got, err)
		}
	}
	claim.StopTimeoutUSec = 1234567
	if got, err := runnerStopAllowance(spec, "timeout"); err != nil || got != 1234567*time.Microsecond {
		t.Fatalf("accepted microseconds were rounded or recaptured: %v %v", got, err)
	}
	for _, invalid := range []uint64{0, math.MaxUint64, uint64(math.MaxInt64)/uint64(time.Microsecond) + 1} {
		claim.StopTimeoutUSec = invalid
		if _, err := runnerStopAllowance(spec, "stopped"); err == nil {
			t.Fatal("unrepresentable allowance wrapped into an early KILL timer")
		}
	}
}

func TestGuardedScopeDrainJoinsOriginalKernelStop(t *testing.T) {
	for _, name := range []string{"settled", "active", "removed", "mismatch", "unknown", "already_quiet", "missing_inode", "failed_stop"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			helper := filepath.Join(root, "systemctl")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\n"
			if name == "failed_stop" {
				script += "exit 7\n"
			}
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			identity := &ScopeIdentity{Version: 1, Unit: scopeInspectionUnit, BootID: scopeInspectionBootID,
				InvocationID: scopeInspectionInvocationID, Cgroup: scopeInspectionGroupParent + scopeInspectionUnit, CgroupDevice: 29, CgroupInode: 12345}
			states := []ScopeState{ScopeActive, ScopeActive}
			want, called := false, true
			switch name {
			case "settled":
				states[1], want = ScopeQuiescent, true
			case "removed":
				states[1] = ScopeUnknown
			case "mismatch":
				states[0], called = ScopeMismatch, false
			case "unknown":
				states[0], called = ScopeUnknown, false
			case "already_quiet":
				states[0], want, called = ScopeQuiescent, true, false
			case "missing_inode":
				identity.CgroupInode, called = 0, false
			}
			observations := 0
			observe := func(_ context.Context, actual *ScopeIdentity) ScopeState {
				if actual != identity {
					t.Fatal("drain replaced the original owner")
				}
				index := observations
				observations++
				if index >= len(states) {
					t.Fatal("unexpected observer retry")
				}
				return states[index]
			}
			if got := drainGuardedScope(context.Background(), identity, observe); got != want {
				t.Fatalf("drain quiet=%t want=%t", got, want)
			}
			args, err := os.ReadFile(helper + ".args")
			if called {
				if err != nil || string(args) != "--user\nstop\n--no-block\n"+identity.Unit+"\n" {
					t.Fatalf("drain did not join original kernel stop: %q %v", args, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("unverified or quiet owner was signalled")
			}
			if strings.Contains(string(args), "kill") || strings.Contains(string(args), "property") {
				t.Fatal("drain bypassed the original kernel stop allowance")
			}
			if name == "missing_inode" && observations != 0 {
				t.Fatal("incomplete physical anchor reached observation")
			}
		})
	}
}

func TestGuardedGroupDrainDoesNotRenewExpiredStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process group drainage")
	}
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; printf R; while :; do sleep 60; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ready, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = SignalGroup(cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	if _, err := io.ReadFull(ready, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !drainGroupUntil(ctx, cmd.Process.Pid, time.Now().Add(-time.Millisecond)) {
		t.Fatal("surviving descendants acquired a new grace after the original stop deadline")
	}
}
