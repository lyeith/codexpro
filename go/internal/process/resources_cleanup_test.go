package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const boundedControlFixture = `import json, os, pathlib, sys, time
witness, parent_release, child_release = map(pathlib.Path, sys.argv[1:4])
nonce, code = sys.argv[4], int(sys.argv[5])
def released(path):
    try: return path.read_text() == nonce
    except OSError: return False
child = os.fork()
if child == 0:
    fields = pathlib.Path('/proc/self/stat').read_text().rsplit(')', 1)[1].split()
    record = {'nonce': nonce, 'pid': os.getpid(), 'identity': fields[19],
              'parent_pid': os.getppid(), 'pgid': os.getpgrp()}
    temporary = witness.with_name(witness.name + '.tmp')
    with temporary.open('x') as stream:
        stream.write(json.dumps(record)); stream.flush(); os.fsync(stream.fileno())
    temporary.chmod(0o600); temporary.replace(witness)
    deadline = time.monotonic() + 8
    # Deliberately inherit stdout/stderr so the parent exit reaches WaitDelay.
    while time.monotonic() < deadline and not released(child_release): time.sleep(.01)
    os._exit(0)
deadline = time.monotonic() + 8
while time.monotonic() < deadline and not released(parent_release): time.sleep(.01)
os._exit(code if released(parent_release) else 91)
`

type boundedControlWitness struct {
	Nonce     string `json:"nonce"`
	PID       int    `json:"pid"`
	Identity  string `json:"identity"`
	ParentPID int    `json:"parent_pid"`
	PGID      int    `json:"pgid"`
}

// This is intentionally a control-helper test, not a job/scope test. It checks
// the two Cmd.Wait outcomes that can leave a descendant holding output pipes:
// ErrWaitDelay for exit 0 and ExitError masking that delay for exit 1.
func TestRunBoundedSettlesPipeHolderAfterHelperExit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kernel process identity proof requires Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("bounded fixture requires Python 3")
	}
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprintf("helper_exit_%d", exitCode), func(t *testing.T) {
			root := t.TempDir()
			nonce, err := RandomHex(16)
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "helper.py")
			if err := os.WriteFile(script, []byte(boundedControlFixture), 0600); err != nil {
				t.Fatal(err)
			}
			witnessPath := filepath.Join(root, "witness.json")
			parentRelease, childRelease := filepath.Join(root, "parent-release"), filepath.Join(root, "child-release")
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			done := make(chan error, 1)
			var witness boundedControlWitness
			observed, returned := false, false
			defer func() {
				// Only these nonce-bound fixture files request an exit. No PID or
				// process group is signalled by cleanup, even after an assertion fails.
				if err := os.WriteFile(childRelease, []byte(nonce), 0600); err != nil {
					t.Errorf("release own fixture: %v", err)
				}
				if err := os.WriteFile(parentRelease, []byte(nonce), 0600); err != nil {
					t.Errorf("release own helper: %v", err)
				}
				cancel()
				if !returned {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("bounded control call did not finish during fixture cleanup")
					}
				}
				if observed {
					until := time.Now().Add(2 * time.Second)
					for ObserveProcess(witness.PID, witness.Identity) != "dead" && time.Now().Before(until) {
						time.Sleep(10 * time.Millisecond)
					}
					if state := ObserveProcess(witness.PID, witness.Identity); state != "dead" {
						t.Errorf("nonce-bound fixture did not exit after its release file: %s", state)
					}
				}
			}()
			go func() {
				_, err := RunBounded(ctx, 5*time.Second, 1024, python, script, witnessPath, parentRelease, childRelease, nonce, strconv.Itoa(exitCode))
				done <- err
			}()
			until := time.Now().Add(2 * time.Second)
			for time.Now().Before(until) {
				body, readErr := os.ReadFile(witnessPath)
				if readErr == nil && json.Unmarshal(body, &witness) == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			_, identityErr := strconv.ParseUint(witness.Identity, 10, 64)
			if witness.Nonce != nonce || witness.PID <= 1 || witness.ParentPID <= 1 || witness.PID == witness.ParentPID || witness.PGID != witness.ParentPID || identityErr != nil {
				t.Fatal("control fixture did not supply a valid nonce-bound private group witness")
			}
			observed = true
			if ObserveProcess(witness.PID, witness.Identity) != "active" {
				t.Fatal("pipe holder was not observed alive before helper exit")
			}
			if err := os.WriteFile(parentRelease, []byte(nonce), 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				returned = true
			case <-time.After(6 * time.Second):
				t.Fatal("control helper exceeded its explicit bound")
			}
			if ctx.Err() != nil {
				t.Fatalf("fixture reached cancellation instead of direct helper exit: %v", ctx.Err())
			}
			if exitCode == 0 {
				if !errors.Is(err, exec.ErrWaitDelay) {
					t.Fatalf("expected pipe wait error after successful helper exit, got %v", err)
				}
			} else {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
					t.Fatalf("nonzero helper exit lost its result: %v", err)
				}
			}
			until = time.Now().Add(time.Second)
			for ObserveProcess(witness.PID, witness.Identity) != "dead" && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if state := ObserveProcess(witness.PID, witness.Identity); state != "dead" {
				t.Errorf("exact owned pipe holder survived control-helper completion: %s", state)
			}
		})
	}
}
