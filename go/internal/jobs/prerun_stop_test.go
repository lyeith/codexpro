package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type preRunCgroup struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Boot   string `json:"boot"`
}

type preRunOwner struct {
	Kind         string       `json:"kind"`
	Unit         string       `json:"unit"`
	InvocationID string       `json:"invocation_id"`
	Cgroup       preRunCgroup `json:"cgroup"`
}

type preRunReceipt struct {
	RunID          string      `json:"run_id"`
	RegistrationID string      `json:"registration_id"`
	BootID         string      `json:"boot_id"`
	Status         string      `json:"status"`
	InvocationID   string      `json:"invocation_id"`
	Owner          preRunOwner `json:"owner"`
}

func (r preRunReceipt) identity() *process.ScopeIdentity {
	return &process.ScopeIdentity{
		Version: 1, Unit: r.Owner.Unit, BootID: r.BootID,
		InvocationID: r.Owner.InvocationID,
		Cgroup:       strings.TrimPrefix(r.Owner.Cgroup.Path, "/sys/fs/cgroup"),
	}
}

func preRunInspection(r *preRunReceipt) map[string]any {
	// These observations are deliberately not an alternative owner binding.
	// Only the retained receipt identifies the accepted scope.
	return map[string]any{
		"receipt": r, "unit_state": map[string]string{},
		"alive": true, "pending": false, "quiescent": false,
		"allocation_claim": "active",
	}
}

// The executable helper is this test binary, with a private directory embedded
// in its wrapper. It never invokes the installed SSD helper or canonical store.
func preRunPrivateHelper(t *testing.T, root string, realScopes bool) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if realScopes {
		if err := os.WriteFile(filepath.Join(root, "real-scopes"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "resource-helper")
	body := "#!/bin/sh\nexec " + recoveryQuote(binary) +
		" '-test.run=^TestPreRunResourceHelperProcess$' -- prerun-resource-helper " +
		recoveryQuote(root) + " \"$@\"\n"
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return helper
}

func TestPreRunResourceHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) < 3 || args[0] != "prerun-resource-helper" {
		t.Skip("private subprocess helper")
	}
	if err := preRunHelperMain(args[1], args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	os.Exit(0)
}

func preRunArgument(args []string, key string) string {
	for i := range args {
		if args[i] == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func preRunAbort(root, run string) error {
	if _, err := os.Stat(filepath.Join(root, run+".abort")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("private admission aborted")
}

func preRunHelperMain(root string, args []string) error {
	calls, err := os.OpenFile(filepath.Join(root, "calls.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(calls).Encode(args)
	closeErr := calls.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	_, realScopesErr := os.Stat(filepath.Join(root, "real-scopes"))
	realScopes := realScopesErr == nil
	if len(args) == 2 && args[0] == "inspect" {
		if !realScopes {
			body, err := os.ReadFile(filepath.Join(root, "inspection.json"))
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(root, "pause-inspect")); err == nil {
				if err := process.AtomicWrite(filepath.Join(root, "inspect-entered"), nil, 0600); err != nil {
					return err
				}
				until := time.Now().Add(4 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(root, "release-inspect")); err == nil {
						break
					}
					if time.Now().After(until) {
						return errors.New("private inspect barrier expired")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			_, err = os.Stdout.Write(body)
			return err
		}
		var receipt preRunReceipt
		if err := process.ReadJSON(filepath.Join(root, args[1]+".receipt.json"), &receipt, 16384); err != nil {
			return err
		}
		if receipt.RunID != args[1] {
			return errors.New("private inspect run mismatch")
		}
		return json.NewEncoder(os.Stdout).Encode(preRunInspection(&receipt))
	}
	if !realScopes || len(args) < 2 || args[0] != "external" {
		return errors.New("unexpected private resource operation")
	}
	if args[1] == "begin" {
		run := preRunArgument(args, "--run-id")
		registration := preRunArgument(args, "--registration-id")
		claim := process.ResourceClaim{Version: 1, Helper: os.Args[0], RunID: run, RegistrationID: registration}
		if err := process.ValidateClaim(claim); err != nil {
			return err
		}
		boot, err := process.BootID()
		if err != nil {
			return err
		}
		receipt := preRunReceipt{RunID: run, RegistrationID: registration, BootID: boot, Status: "created",
			Owner: preRunOwner{Kind: "unit", Unit: preRunArgument(args, "--owner-unit")}}
		if err := process.WriteJSON(filepath.Join(root, run+".receipt.json"), receipt); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(process.Allocation{
			RunID: run, RegistrationID: registration, Scratch: root,
			Environment: map[string]string{}, Owner: map[string]any{"kind": "unit", "unit": receipt.Owner.Unit},
		})
	}
	if len(args) < 5 || args[3] != "--registration-id" {
		return errors.New("missing private original registration")
	}
	run, registration := args[2], args[4]
	var receipt preRunReceipt
	if err := process.ReadJSON(filepath.Join(root, run+".receipt.json"), &receipt, 16384); err != nil {
		return err
	}
	if receipt.RunID != run || receipt.RegistrationID != registration {
		return errors.New("private original registration changed")
	}
	switch args[1] {
	case "exec":
		if len(args) < 7 || args[5] != "--" || receipt.Status != "created" {
			return errors.New("invalid private activation")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		identity, err := process.CaptureScopeIdentity(ctx, receipt.Owner.Unit)
		if err != nil {
			return err
		}
		path := "/sys/fs/cgroup" + identity.Cgroup
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || identity.BootID != receipt.BootID {
			return errors.New("private scope incarnation unavailable")
		}
		receipt.Status, receipt.InvocationID = "running", identity.InvocationID
		receipt.Owner.InvocationID = identity.InvocationID
		receipt.Owner.Cgroup = preRunCgroup{
			Path: path, Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Boot: identity.BootID,
		}
		if err := process.WriteJSON(filepath.Join(root, run+".receipt.json"), receipt); err != nil {
			return err
		}
		// Publish the captured original even if cleanup arrived while capture was
		// delayed, so cleanup can settle that exact scope without a name-only kill.
		if err := preRunAbort(root, run); err != nil {
			return err
		}
		if err := process.AtomicWrite(filepath.Join(root, run+".entered"), []byte("bound before runner"), 0600); err != nil {
			return err
		}
		// This is the host-admission boundary: the scope and its original receipt
		// exist, but the supplied CodexPro runner has not been executed.
		until := time.Now().Add(30 * time.Second)
		for time.Now().Before(until) {
			if err := preRunAbort(root, run); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(root, "release-runner")); err == nil {
				command := args[6:]
				executable, err := exec.LookPath(command[0])
				if err != nil {
					return err
				}
				if err := preRunAbort(root, run); err != nil {
					return err
				}
				return syscall.Exec(executable, command, os.Environ())
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("private admission barrier expired")
	case "finish", "cancel":
		// A test-controlled acknowledgement delay proves terminal status alone
		// cannot release capacity. Even after release, verify the original scope.
		if _, err := os.Stat(filepath.Join(root, "allow-settlement")); err != nil {
			return errors.New("private resource settlement not acknowledged")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		identity := receipt.identity()
		if !process.ValidScopeIdentity(identity, receipt.Owner.Unit) ||
			process.InspectScope(ctx, identity) != process.ScopeQuiescent {
			return errors.New("private original scope is not quiescent")
		}
		if err := process.WriteJSON(filepath.Join(root, run+".settled"), args); err != nil {
			return err
		}
		status := "failed"
		if args[1] == "cancel" {
			status = "cancelled"
		} else if preRunArgument(args, "--exit-code") == "0" {
			status = "succeeded"
		}
		return json.NewEncoder(os.Stdout).Encode(process.Settlement{
			RunID: run, Status: status, Quiescent: true, Cancelled: args[1] == "cancel",
		})
	default:
		return errors.New("unsupported private resource operation")
	}
}

func preRunCalls(t *testing.T, root string) [][]string {
	t.Helper()
	file, err := os.Open(filepath.Join(root, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls [][]string
	decoder := json.NewDecoder(file)
	for {
		var args []string
		if err := decoder.Decode(&args); err != nil {
			if errors.Is(err, io.EOF) {
				return calls
			}
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
}

type preRunFixture struct {
	service  *Service
	resolver testResolver
	record   Record
	root     string
	receipt  preRunReceipt
	signal   string
}

func newPreRunFixture(t *testing.T) *preRunFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("original scope and process identity require Linux")
	}
	boot, err := process.BootID()
	if err != nil {
		t.Fatal(err)
	}
	initial, cfg, resolver := newFixture(t)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Set("maxActiveCaptures", 1)
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise admission synchronously, without a monitor racing the fixture.
	// This only marks this private service ready; it starts no worker or scope.
	s.backgroundActive.Store(true)
	t.Cleanup(func() { _ = s.Close() })
	record, _, err := s.admit(opts(resolver, "touch must-not-run"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolver.workspace.Root, "prerun-helper")
	helper := preRunPrivateHelper(t, root, false)
	claim := process.ResourceClaim{Version: 1, Helper: helper,
		RunID: strings.Repeat("a", 32), RegistrationID: strings.Repeat("b", 32)}
	unit := "codexpro-" + record.ID + "-" + strings.Repeat("c", 32)
	record, err = s.update(record.ID, func(next *Record) bool {
		next.ScopeUnit, next.ResourceClaim = unit, &claim
		next.PID, next.ProcessIdentity = os.Getpid(), process.ProcessIdentity(os.Getpid())
		next.LaunchState = "launched"
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	writeRecoverySpec(t, s, record, resolver.workspace.Root)
	receipt := preRunReceipt{
		RunID: claim.RunID, RegistrationID: claim.RegistrationID, BootID: boot,
		Status: "running", InvocationID: strings.Repeat("d", 32),
		Owner: preRunOwner{Kind: "unit", Unit: unit + ".scope", InvocationID: strings.Repeat("d", 32),
			Cgroup: preRunCgroup{Path: "/sys/fs/cgroup/user.slice/" + unit + ".scope", Device: 1, Inode: 2, Boot: boot}},
	}
	signal := filepath.Join(root, "signal")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// The current unit is a different invocation. Receipt recovery may persist
	// the original identity, but it must never signal this replacement.
	script := "#!/bin/sh\ncase \"$2\" in\nshow) printf '%s\\n' 'LoadState=loaded' 'ActiveState=active' " +
		recoveryQuote("InvocationID="+strings.Repeat("e", 32)) + " " +
		recoveryQuote("ControlGroup="+receipt.identity().Cgroup) + ";;\nkill) touch " + recoveryQuote(signal) + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &preRunFixture{s, resolver, record, root, receipt, signal}
}

func (f *preRunFixture) requestStop(t *testing.T, reason string, aged bool) {
	t.Helper()
	var err error
	f.record, err = f.service.Stop(f.record.ID, "", reason)
	if err != nil {
		t.Fatal(err)
	}
	if aged {
		f.record, err = f.service.update(f.record.ID, func(next *Record) bool {
			next.StopRequestedAtMS = time.Now().Add(-6 * time.Second).UnixMilli()
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func preRunAssertCapacityHeld(t *testing.T, s *Service, resolver testResolver) {
	t.Helper()
	_, _, err := s.admit(opts(resolver, "touch must-not-start-next"))
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != "job_limit_reached" {
		t.Fatalf("unsettled original released admission capacity: %v", err)
	}
}

func (f *preRunFixture) assertUnsettled(t *testing.T, wantIdentity bool) Record {
	t.Helper()
	record, ok := f.service.Get(f.record.ID, "")
	if !ok || record.Status != "running" || record.Quiescent {
		t.Fatalf("unresolved original changed outcome: %+v", record)
	}
	if wantIdentity {
		if !reflect.DeepEqual(record.ScopeIdentity, f.receipt.identity()) {
			t.Fatalf("original scope identity not retained: %+v", record.ScopeIdentity)
		}
	} else if record.ScopeIdentity != nil {
		t.Fatalf("unverified receipt became an owner: %+v", record.ScopeIdentity)
	}
	if _, err := os.Stat(f.signal); !os.IsNotExist(err) {
		t.Fatal("replacement scope was signalled")
	}
	if _, err := os.Stat(record.SpecPath + ".started"); !os.IsNotExist(err) {
		t.Fatal("fixture unexpectedly started a runner")
	}
	preRunAssertCapacityHeld(t, f.service, f.resolver)
	return record
}

func TestPreRunStopRecoversAndPersistsOriginalScope(t *testing.T) {
	f := newPreRunFixture(t)
	f.requestStop(t, "stopped", true)
	if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&f.receipt)); err != nil {
		t.Fatal(err)
	}
	f.service.settle(f.record.ID)
	f.assertUnsettled(t, true)
	wantCall := []string{"inspect", f.receipt.RunID}
	if calls := preRunCalls(t, f.root); len(calls) != 1 || !reflect.DeepEqual(calls[0], wantCall) {
		t.Fatalf("recovery used another resource operation: %v", calls)
	}
	cfg := f.service.cfg
	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPaused(cfg, f.resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.backgroundActive.Store(true)
	f.service = reopened
	f.assertUnsettled(t, true)
	// A newer inspect response must not replace the durable original after a
	// manager restart, even though its unit name still matches.
	foreign := f.receipt
	foreign.InvocationID, foreign.Owner.InvocationID = strings.Repeat("e", 32), strings.Repeat("e", 32)
	if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&foreign)); err != nil {
		t.Fatal(err)
	}
	reopened.settle(f.record.ID)
	f.assertUnsettled(t, true)
	if calls := preRunCalls(t, f.root); len(calls) != 1 {
		t.Fatalf("existing accepted identity was reselected: %v", calls)
	}
}

func TestPreRunStopDoesNotAdoptAfterConcurrentOwnerChange(t *testing.T) {
	for _, mode := range []string{"accepted-identity", "registration", "unit"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreRunFixture(t)
			f.requestStop(t, "stopped", true)
			if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&f.receipt)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.root, "pause-inspect"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			release := filepath.Join(f.root, "release-inspect")
			done := make(chan struct{})
			// The private service also owns this manual worker so its fixture
			// cannot close the database before the worker returns on a failure.
			f.service.wg.Add(1)
			go func() {
				defer f.service.wg.Done()
				defer close(done)
				f.service.settle(f.record.ID)
			}()
			t.Cleanup(func() {
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Errorf("release private inspect during cleanup: %v", err)
				}
				select {
				case <-done:
					return
				case <-time.After(5 * time.Second):
					f.service.cancel()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("private settle worker did not join after cancellation")
				}
			})
			waitStartupFile(t, filepath.Join(f.root, "inspect-entered"))
			var wantIdentity *process.ScopeIdentity
			current, err := f.service.update(f.record.ID, func(next *Record) bool {
				switch mode {
				case "accepted-identity":
					wantIdentity = f.receipt.identity()
					wantIdentity.InvocationID = strings.Repeat("f", 32)
					next.ScopeIdentity = wantIdentity
				case "registration":
					claim := *next.ResourceClaim
					claim.RegistrationID = strings.Repeat("f", 32)
					next.ResourceClaim = &claim
				case "unit":
					next.ScopeUnit = "codexpro-" + next.ID + "-" + strings.Repeat("f", 32)
				}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("private inspected original did not return after release")
			}
			record, _ := f.service.Get(f.record.ID, "")
			if !reflect.DeepEqual(record.ScopeIdentity, wantIdentity) ||
				record.ScopeUnit != current.ScopeUnit ||
				!reflect.DeepEqual(record.ResourceClaim, current.ResourceClaim) ||
				record.Status != "running" || record.Quiescent {
				t.Fatalf("stale inspection replaced a concurrently accepted owner: %+v", record)
			}
			if _, err := os.Stat(f.signal); !os.IsNotExist(err) {
				t.Fatal("stale inspected original signalled a replacement")
			}
			preRunAssertCapacityHeld(t, f.service, f.resolver)
		})
	}
}

func TestPreRunStopRetriesUnknownOrForeignRegistration(t *testing.T) {
	for _, mode := range []string{"unavailable", "foreign-registration"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreRunFixture(t)
			f.requestStop(t, "stopped", true)
			if mode == "foreign-registration" {
				foreign := f.receipt
				foreign.RegistrationID = strings.Repeat("f", 32)
				if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&foreign)); err != nil {
					t.Fatal(err)
				}
			}
			f.service.settle(f.record.ID)
			f.assertUnsettled(t, false)
			if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&f.receipt)); err != nil {
				t.Fatal(err)
			}
			f.service.settle(f.record.ID)
			f.assertUnsettled(t, true)
			calls := preRunCalls(t, f.root)
			if len(calls) != 2 {
				t.Fatalf("original receipt was not retried exactly: %v", calls)
			}
			for _, call := range calls {
				if !reflect.DeepEqual(call, []string{"inspect", f.receipt.RunID}) {
					t.Fatalf("unresolved owner triggered another action: %v", calls)
				}
			}
		})
	}
}

func TestPreRunDeadlineRecordsControlAndPreservesGrace(t *testing.T) {
	f := newPreRunFixture(t)
	var err error
	f.record, err = f.service.update(f.record.ID, func(next *Record) bool {
		next.DeadlineMS = time.Now().Add(-time.Second).UnixMilli()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.observeRunning(f.record)
	record, _ := f.service.Get(f.record.ID, "")
	body, err := os.ReadFile(record.ControlPath)
	if err != nil || string(body) != "timeout" || record.RequestedStopReason != "timeout" {
		t.Fatalf("pre-run deadline did not retain its stop request: %q %+v %v", body, record, err)
	}
	f.service.settle(record.ID)
	f.assertUnsettled(t, false)
	if calls := preRunCalls(t, f.root); len(calls) != 0 {
		t.Fatalf("receipt inspection bypassed the existing five-second grace: %v", calls)
	}
	f.record, err = f.service.update(record.ID, func(next *Record) bool {
		next.StopRequestedAtMS = time.Now().Add(-6 * time.Second).UnixMilli()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.WriteJSON(filepath.Join(f.root, "inspection.json"), preRunInspection(&f.receipt)); err != nil {
		t.Fatal(err)
	}
	f.service.settle(record.ID)
	f.assertUnsettled(t, true)
}

func TestPreRunInspectionFailureKeepsPreparedRevocation(t *testing.T) {
	f := newPreRunFixture(t)
	claim, revoked, _, _ := recoveryHelper(t, f.root, "")
	_, err := f.service.update(f.record.ID, func(next *Record) bool {
		next.Status, next.LaunchState = "failed", "prepared"
		next.FinishedAtMS = time.Now().UnixMilli()
		next.PID, next.ProcessIdentity = 0, ""
		next.ResourceClaim = &claim
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service.settle(f.record.ID)
	record, _ := f.service.Get(f.record.ID, "")
	body, err := os.ReadFile(revoked)
	if err != nil || string(body) != claim.RunID+":"+claim.RegistrationID || !record.Quiescent {
		t.Fatalf("failed inspect stranded verified original revocation: %q %+v %v", body, record, err)
	}
	if record.ScopeIdentity != nil || record.Status != "failed" {
		t.Fatalf("prepared revocation fabricated a scope or changed outcome: %+v", record)
	}
}

// preRunCleanupOriginal keeps the fixture alive until the exact original scope
// and its systemd-run supervisor are gone. An unavailable receipt is retried,
// never treated as evidence that a delayed external exec cannot still bind.
func preRunCleanupOriginal(ctx context.Context, s *Service, root string, original Record) error {
	if original.ResourceClaim == nil || original.ScopeUnit == "" {
		return errors.New("private launch has no retained resource/scope original")
	}
	var failures []error
	if err := os.WriteFile(filepath.Join(root, original.ResourceClaim.RunID+".abort"), nil, 0600); err != nil {
		failures = append(failures, fmt.Errorf("write private abort: %w", err))
	}
	if _, err := s.Stop(original.ID, "", "stopped"); err != nil {
		failures = append(failures, fmt.Errorf("stop private original: %w", err))
	}
	// Assertions have finished; allow the private helper to acknowledge only
	// after its normal exact-scope quiescence check.
	if err := os.WriteFile(filepath.Join(root, "allow-settlement"), nil, 0600); err != nil {
		failures = append(failures, fmt.Errorf("release private settlement: %w", err))
	}
	expectedUnit := original.ScopeUnit + ".scope"
	lastObservation := "original scope identity unavailable"
	for {
		var identity *process.ScopeIdentity
		current, ok := s.Get(original.ID, "")
		if ok && current.ScopeUnit == original.ScopeUnit &&
			reflect.DeepEqual(current.ResourceClaim, original.ResourceClaim) &&
			process.ValidScopeIdentity(current.ScopeIdentity, expectedUnit) {
			identity = current.ScopeIdentity
		}
		if identity == nil {
			var err error
			identity, err = process.InspectResourceScope(ctx, *original.ResourceClaim, expectedUnit)
			if err != nil {
				lastObservation = err.Error()
			}
		}
		quiet := identity != nil && process.SettleScope(ctx, identity)
		// launch already owns cmd.Wait. Require the original birth to disappear,
		// not merely become a zombie, before removing files its supervisor used.
		supervisorJoined := original.PID <= 0 ||
			(original.ProcessIdentity != "" &&
				process.ObserveProcess(original.PID, original.ProcessIdentity) == "dead" &&
				process.ProcessIdentity(original.PID) != original.ProcessIdentity)
		if quiet && supervisorJoined {
			return errors.Join(failures...)
		}
		if identity != nil {
			lastObservation = fmt.Sprintf("scope=%s quiet=%v supervisor_joined=%v", identity.Unit, quiet, supervisorJoined)
		}
		select {
		case <-ctx.Done():
			failures = append(failures, fmt.Errorf("unresolved private original %s: %s: %w", original.ID, lastObservation, ctx.Err()))
			return errors.Join(failures...)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func preRunWait(t *testing.T, ctx context.Context, s *Service, id string, quiet bool) Record {
	t.Helper()
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		record, ok := s.Get(id, "")
		if ok && record.Status != "running" && (!quiet || record.Quiescent) {
			return record
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	record, _ := s.Get(id, "")
	t.Fatalf("private pre-run owner did not settle: status=%s quiet=%v", record.Status, record.Quiescent)
	return Record{}
}

// Explicit opt-in: only fresh private scopes and fake resource receipts are
// created. The real systemd/cgroup signal boundary is exercised without the
// installed SSD helper, canonical CodexPro state, or live Fleet reservations.
func TestPreRunStopHostScopeIntegration(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("CODEXPRO_GO_TEST_PRERUN_SCOPES") != "1" {
		t.Skip("set CODEXPRO_GO_TEST_PRERUN_SCOPES=1 on a Linux user-scope host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, mode := range []string{"stopped", "timeout", "released-control"} {
		t.Run(mode, func(t *testing.T) {
			// Use an explicitly owned directory so failed cleanup retains evidence
			// instead of t.TempDir deleting files a delayed child may still use.
			base, err := os.MkdirTemp("", "codexpro-prerun-scope-")
			if err != nil {
				t.Fatal(err)
			}
			workspaceRoot := filepath.Join(base, "workspace")
			root := filepath.Join(workspaceRoot, "prerun-native-helper")
			var s *Service
			var original Record
			t.Cleanup(func() {
				remove := true
				if s != nil && original.ID != "" {
					cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
					if err := preRunCleanupOriginal(cleanup, s, root, original); err != nil {
						remove = false
						t.Errorf("private original cleanup failed; retaining %s: %v", base, err)
					}
					done()
				}
				if s != nil {
					if err := s.Close(); err != nil {
						remove = false
						t.Errorf("private service close failed; retaining %s: %v", base, err)
					}
				}
				if remove {
					if err := os.RemoveAll(base); err != nil {
						t.Errorf("remove settled private fixture %s: %v", base, err)
					}
				}
			})
			if err := os.Mkdir(workspaceRoot, 0700); err != nil {
				t.Fatal(err)
			}
			resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: workspaceRoot}}
			cfg := config.New(filepath.Join(base, "state"))
			cfg.HostResourcesHelper = preRunPrivateHelper(t, root, true)
			cfg.Set("maxActiveCaptures", 1)
			cfg.Set("jobTimeoutMs", 10000)
			cfg.Set("bashTimeoutMs", 1000)
			cfg.Set("maxBashTimeoutMs", 5000)
			cfg.Set("jobRetentionMs", 60000)
			t.Setenv("CODEXPRO_JOB_SCOPES", "1")
			s, err = New(cfg, resolver)
			if err != nil {
				t.Fatal(err)
			}
			options := opts(resolver, "touch payload-ran")
			options.TimeoutMS = 10000
			if mode == "timeout" {
				options.TimeoutMS = 1200
			}
			original, err = s.start(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			if original.ResourceClaim == nil {
				t.Fatal("private launch did not retain its original resource claim")
			}
			run := original.ResourceClaim.RunID
			waitStartupFile(t, filepath.Join(root, run+".entered"))
			if _, err := os.Stat(original.SpecPath + ".started"); !os.IsNotExist(err) {
				t.Fatal("runner started before the private admission barrier")
			}
			if mode == "released-control" {
				if err := os.WriteFile(filepath.Join(root, "allow-settlement"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "release-runner"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				record := preRunWait(t, ctx, s, original.ID, true)
				if record.Status != "succeeded" {
					t.Fatalf("released control did not execute its runner: %+v", record)
				}
				if _, err := os.Stat(filepath.Join(resolver.workspace.Root, "payload-ran")); err != nil {
					t.Fatal("private barrier never admitted the positive-control payload")
				}
				if _, err := os.Stat(original.SpecPath + ".started"); err != nil {
					t.Fatal("positive control did not traverse the actual runner")
				}
				return
			}
			if mode == "stopped" {
				if _, err := s.Stop(original.ID, "", "stopped"); err != nil {
					t.Fatal(err)
				}
			}
			terminal := preRunWait(t, ctx, s, original.ID, false)
			wantStatus := "stopped"
			if mode == "timeout" {
				wantStatus = "timed_out"
			}
			if terminal.Status != wantStatus || terminal.RequestedStopReason != mode ||
				terminal.StopReason != mode || terminal.ScopeIdentity == nil || terminal.Quiescent {
				t.Fatalf("pre-run stop lost its original or released before resource acknowledgement: %+v", terminal)
			}
			if terminal.ScopeUnit != original.ScopeUnit || !reflect.DeepEqual(terminal.ResourceClaim, original.ResourceClaim) {
				t.Fatal("pre-run settlement changed the accepted resource or unit")
			}
			if _, err := os.Stat(terminal.SpecPath + ".started"); !os.IsNotExist(err) {
				t.Fatal("stopped pre-run owner unexpectedly executed its runner")
			}
			if _, err := os.Stat(filepath.Join(resolver.workspace.Root, "payload-ran")); !os.IsNotExist(err) {
				t.Fatal("payload ran before pre-run cancellation settled")
			}
			preRunAssertCapacityHeld(t, s, resolver)
			if err := os.WriteFile(filepath.Join(root, "allow-settlement"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			settled := preRunWait(t, ctx, s, original.ID, true)
			if settled.Status != wantStatus || settled.QuiescenceScope != "systemd_scope" {
				t.Fatalf("verified original settlement changed the terminal outcome: %+v", settled)
			}
			var acknowledgement []string
			if err := process.ReadJSON(filepath.Join(root, run+".settled"), &acknowledgement, 16384); err != nil {
				t.Fatal(err)
			}
			if len(acknowledgement) < 5 || acknowledgement[2] != run ||
				acknowledgement[3] != "--registration-id" || acknowledgement[4] != original.ResourceClaim.RegistrationID {
				t.Fatalf("another registration was settled: %v", acknowledgement)
			}
			// Opening the old barrier after settlement cannot resurrect its
			// supervisor or payload. The original scope is already empty.
			if err := os.WriteFile(filepath.Join(root, "release-runner"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if process.InspectScope(ctx, settled.ScopeIdentity) != process.ScopeQuiescent {
				t.Fatal("late admission found a surviving original scope")
			}
			if _, err := os.Stat(filepath.Join(resolver.workspace.Root, "payload-ran")); !os.IsNotExist(err) {
				t.Fatal("late admission resurrected the stopped payload")
			}
			next, _, err := s.admit(opts(resolver, "unlaunched next private command"))
			if err != nil || next.PID != 0 {
				t.Fatalf("verified settlement did not restore capacity: %+v %v", next, err)
			}
		})
	}
}
