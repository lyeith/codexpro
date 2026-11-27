package process

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This synthetic Fleet v1 document is a fixture, never an authority source.
const guardedContextFixture = `{
 "schema_version":1,"config_revision":"fixture-policy","config_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
 "resources":{"stack_memory_mib":6144,"stack_tasks":4096,"go_parallelism":2,"go_memory_mib":2048,"node_heap_mib":1024,"frontend_workers":1},
 "priorities":{"stream_nice":10,"ionice_class":2,"ionice_priority":7},
 "timeouts":{
  "heavy_exit_seconds":1,"heavy_drain_seconds":1,"heavy_adopted_child_seconds":1,"heavy_full_poll_seconds":0.25,"heavy_cleanup_poll_seconds":1,
  "darwin_poll_seconds":1,"detached_start_seconds":1,"detached_poll_seconds":1,"command_group_settle_seconds":1,"supervisor_term_seconds":1,
  "supervisor_owned_reap_seconds":1,"supervisor_post_exit_seconds":1,"supervisor_control_seconds":1,"supervisor_max_grace_seconds":1,"supervisor_poll_seconds":1,
  "ssh_connect_seconds":1,"host_probe_seconds":1,"host_probe_via_seconds":1,"queue_terminate_seconds":1,"queue_settle_seconds":1,"queue_output_drain_seconds":1,
  "queue_control_seconds":1,"queue_scope_query_seconds":1,"queue_scope_stop_seconds":1,"queue_scope_terminate_seconds":1,"queue_db_wait_seconds":1,
  "queue_source_git_seconds":1,"queue_stack_release_seconds":1,"maintenance_cache_pass_seconds":1,"idle_stack_seconds":1,"generation_grace_seconds":1,
  "landing_lock_poll_seconds":1,"landing_inventory_inner_seconds":1,"landing_inventory_kill_seconds":1,"landing_inventory_outer_seconds":1,
  "stack_external_seconds":60,"stack_stop_grace_seconds":90,"stack_kill_settle_seconds":1,"stack_finite_poll_seconds":1,"stack_role_poll_seconds":1,
  "stack_stop_poll_seconds":1,"stack_supervisor_start_seconds":1,"stack_supervisor_start_failure_seconds":1,"stack_supervisor_request_seconds":1,
  "stack_supervisor_settle_seconds":1,"stack_supervisor_poll_seconds":1,"stack_ephemeral_probe_seconds":1},
 "worker_defaults":{"go_parallelism":2,"frontend_workers":1,"gradle_workers":1}
}`

func guardedTestOffer(t *testing.T) *GuardedResourceOffer {
	t.Helper()
	root := t.TempDir()
	helper := filepath.Join(root, "producer", "bin", "ssd-dev")
	return &GuardedResourceOffer{
		Version: 1, Helper: helper, Config: filepath.Join(root, "ssd.json"),
		JobsState: filepath.Join(root, "account", ".codexpro-go", "state"), StopTimeoutUSec: 90000000,
		ControlLocator:   ResourceControlLocator{Version: 1, ReceiptsRoot: filepath.Join(root, "control", "runs"), DefaultHome: filepath.Join(root, "account")},
		ExecutionContext: json.RawMessage(guardedContextFixture),
		FleetRelease:     FleetReleaseSnapshot{Root: filepath.Join(root, "fleet", strings.Repeat("b", 64)), Version: "fixture", Digest: strings.Repeat("b", 64), Python: "/usr/bin/python3"},
		ProducerSource: ProducerSourceSnapshot{CheckoutRoot: filepath.Dir(filepath.Dir(helper)), ExecutablePath: helper,
			ExecutableSHA256: strings.Repeat("c", 64), SourceCommit: strings.Repeat("d", 40), InterpreterPath: "/usr/bin/python3"},
	}
}

func guardedTestClaim(t *testing.T, offer *GuardedResourceOffer) ResourceClaim {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("guarded host resources require Linux")
	}
	claim, err := offer.Claim(scopeInspectionRunID, scopeInspectionRegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func TestGuardedOfferPreservesContextAndExactClaimSchema(t *testing.T) {
	offer := guardedTestOffer(t)
	claim := guardedTestClaim(t, offer)
	if err := offer.Validate(offer.JobsState); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"version", "helper", "run_id", "registration_id", "config", "receipts_root", "default_home", "execution_context", "stop_timeout_usec"}
	if len(fields) != len(want) {
		t.Fatalf("guarded claim changed schema: %s", data)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing claim field %s", key)
		}
	}
	if !equalJSON(fields["execution_context"], offer.ExecutionContext) {
		t.Fatal("claim rewrote the offered context")
	}
	var roundtrip ResourceClaim
	if err := json.Unmarshal(data, &roundtrip); err != nil || !EqualResourceClaims(claim, roundtrip) {
		t.Fatalf("claim changed after durable roundtrip: %v", err)
	}
	clone := CloneResourceClaim(claim)
	clone.ExecutionContext[0] = '['
	if EqualResourceClaims(claim, clone) || claim.ExecutionContext[0] != '{' {
		t.Fatal("cloned claim aliases accepted context")
	}
	copy := CloneGuardedResourceOffer(offer)
	copy.ExecutionContext[0] = '['
	if offer.ExecutionContext[0] != '{' {
		t.Fatal("cloned offer aliases accepted context")
	}
	legacy := ResourceClaim{Version: 1, Helper: offer.Helper, RunID: claim.RunID, RegistrationID: claim.RegistrationID}
	data, err = json.Marshal(legacy)
	if err != nil || json.Unmarshal(data, &fields) != nil {
		t.Fatal(err)
	}
	var legacyFields map[string]json.RawMessage
	_ = json.Unmarshal(data, &legacyFields)
	if len(legacyFields) != 4 {
		t.Fatalf("legacy claim acquired guarded fields: %s", data)
	}
}

func TestGuardedClaimRejectsUnknownDuplicateAndAliasedFields(t *testing.T) {
	offer := guardedTestOffer(t)
	claim := guardedTestClaim(t, offer)
	data, _ := json.Marshal(claim)
	for name, raw := range map[string]string{
		"unknown":   strings.TrimSuffix(string(data), "}") + ",\"jobs_state\":\"/tmp/other\"}",
		"duplicate": strings.TrimSuffix(string(data), "}") + ",\"version\":2}",
		"alias":     strings.Replace(string(data), "\"helper\":", "\"Helper\":", 1),
		"missing":   strings.Replace(string(data), "\"version\":2,", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			var got ResourceClaim
			err := json.Unmarshal([]byte(raw), &got)
			if err == nil {
				err = ValidateClaim(got)
			}
			if err == nil {
				t.Fatal("invalid guarded claim accepted")
			}
		})
	}
}

func TestGuardedContextValidationKeepsFractionsAndExactNumbers(t *testing.T) {
	if _, err := validateExecutionContext(json.RawMessage(guardedContextFixture)); err != nil {
		t.Fatalf("valid fractional policy rejected: %v", err)
	}
	cases := map[string]string{
		"resource_bool":     strings.Replace(guardedContextFixture, "\"stack_memory_mib\":6144", "\"stack_memory_mib\":true", 1),
		"resource_fraction": strings.Replace(guardedContextFixture, "\"stack_memory_mib\":6144", "\"stack_memory_mib\":6144.0", 1),
		"timeout_bool":      strings.Replace(guardedContextFixture, "\"heavy_exit_seconds\":1", "\"heavy_exit_seconds\":true", 1),
		"timeout_string":    strings.Replace(guardedContextFixture, "\"heavy_exit_seconds\":1", "\"heavy_exit_seconds\":\"1\"", 1),
		"missing_timeout":   strings.Replace(guardedContextFixture, "\"stack_role_poll_seconds\":1,", "", 1),
		"unknown_context":   strings.Replace(guardedContextFixture, "\"schema_version\":1", "\"admission\":{},\"schema_version\":1", 1),
		"duplicate_context": strings.Replace(guardedContextFixture, "\"schema_version\":1", "\"schema_version\":1,\"schema_version\":1", 1),
		"memory_overflow":   strings.Replace(guardedContextFixture, "\"stack_memory_mib\":6144", "\"stack_memory_mib\":18446744073709551615", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validateExecutionContext(json.RawMessage(raw)); err == nil {
				t.Fatal("invalid context accepted")
			}
		})
	}
	offer := guardedTestOffer(t)
	left := guardedTestClaim(t, offer)
	left.ExecutionContext = json.RawMessage(strings.Replace(guardedContextFixture, "\"go_memory_mib\":2048", "\"go_memory_mib\":9007199254740992", 1))
	right := CloneResourceClaim(left)
	right.ExecutionContext = json.RawMessage(strings.Replace(string(left.ExecutionContext), "9007199254740992", "9007199254740993", 1))
	if EqualResourceClaims(left, right) {
		t.Fatal("claim equality rounded an integer through float64")
	}
}

func TestGuardedCaptureRefusesCustomStateBeforeHelper(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf called > \"$0.called\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config")
	_ = os.WriteFile(config, []byte("{}"), 0600)
	custom := filepath.Join(root, "custom")
	_ = os.Mkdir(custom, 0700)
	if _, err := CaptureGuardedResources(context.Background(), helper, config, custom, 90000000); err == nil {
		t.Fatal("custom state acquired a guarded offer")
	}
	if _, err := os.Stat(helper + ".called"); !os.IsNotExist(err) {
		t.Fatal("custom-state refusal invoked the helper")
	}
	home := filepath.Join(root, "account")
	state := filepath.Join(home, ".codexpro-go", "state")
	_ = os.MkdirAll(state, 0700)
	if err := validateJobsStateAtHome(state, home); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	if err := validateJobsStateAtHome(filepath.Join(alias, ".codexpro-go", "state"), alias); err == nil {
		t.Fatal("a symbolic home replaced the physical accepted state")
	}
}

func TestGuardedOfferRejectsStateAndSourceMismatch(t *testing.T) {
	offer := guardedTestOffer(t)
	wire := map[string]any{"version": offer.Version, "control_locator": offer.ControlLocator,
		"jobs_state": offer.JobsState, "execution_context": offer.ExecutionContext,
		"fleet_release": offer.FleetRelease, "producer_source": offer.ProducerSource}
	data, _ := json.Marshal(wire)
	got, err := decodeGuardedOffer(data, offer.Helper, offer.Config, offer.JobsState, offer.StopTimeoutUSec)
	if err != nil || !equalJSON(got.ExecutionContext, offer.ExecutionContext) {
		t.Fatalf("valid offer rejected or changed: %v", err)
	}
	for name, modify := range map[string]func(*GuardedResourceOffer){
		"jobs_state":     func(o *GuardedResourceOffer) { o.JobsState += "-other" },
		"helper":         func(o *GuardedResourceOffer) { o.Helper += "-other" },
		"source_digest":  func(o *GuardedResourceOffer) { o.ProducerSource.ExecutableSHA256 = "missing" },
		"release_digest": func(o *GuardedResourceOffer) { o.FleetRelease.Root += "-other" },
		"broad_locator": func(o *GuardedResourceOffer) {
			o.ControlLocator.ReceiptsRoot = filepath.Join(o.ControlLocator.DefaultHome, "runs")
		},
		"unselected_memory": func(o *GuardedResourceOffer) {
			o.ExecutionContext = json.RawMessage(strings.Replace(string(o.ExecutionContext), "6144", "8192", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := CloneGuardedResourceOffer(offer)
			modify(copy)
			if err := copy.Validate(offer.JobsState); err == nil {
				t.Fatal("changed offer accepted")
			}
		})
	}
}

type guardedProtocolFixture struct {
	offer      *GuardedResourceOffer
	claim      ResourceClaim
	spec       Spec
	memory     guardedMemoryBinding
	inspect    map[string]any
	activation ResourceActivation
}

func guardedProtocol(t *testing.T) guardedProtocolFixture {
	t.Helper()
	offer := guardedTestOffer(t)
	claim := guardedTestClaim(t, offer)
	if err := os.MkdirAll(filepath.Dir(offer.Helper), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\numask 077\nprintf '%s\\n' CALL \"$@\" >> \"$0.args\"\n" +
		"if [ \"$1\" = --config ]; then shift 2; fi\n[ \"$1\" = external ] || exit 91\n" +
		"if [ -e \"$0.$2.fail\" ]; then exit 7; fi\nexec /bin/cat \"$0.$2.json\"\n"
	if err := os.WriteFile(offer.Helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Version: GuardedRunnerSpecVersion, ResourceClaim: &claim, ScopeUnit: scopeInspectionUnit,
		TimeoutMS: 60000, Deadline: time.Now().Add(time.Minute).UnixMilli(), Nice: 10}
	envelope, err := guardedEnvelope(claim, spec.TimeoutMS, spec.Nice)
	if err != nil {
		t.Fatal(err)
	}
	memory := guardedMemoryBinding{Key: ResourceMemoryKey{HostID: strings.Repeat("f", 32), BootID: scopeInspectionBootID,
		Producer: "ssd-dev", RunID: claim.RunID, RegistrationID: claim.RegistrationID},
		FleetRelease: offer.FleetRelease, ProducerSource: offer.ProducerSource, ExecutionContext: offer.ExecutionContext,
		UnitEnvelope: envelope, DeadlineMS: spec.Deadline, BindingDigest: strings.Repeat("e", 64),
		OriginatingUpstream: json.RawMessage(`{"producer":"codexpro","job_id":"job_deadbeef","binding_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)}
	inspect := scopeInspectionEnvelope()
	inspect["pending"], inspect["allocation_claim"] = false, "active"
	scopeInspectionReceipt(inspect)["host_memory"] = memory
	activation := ResourceActivation{Schema: 1, Key: memory.Key, BindingDigest: memory.BindingDigest, State: "launching",
		TransitionMade: true, ChargedBytes: GuardedMemoryBytes, ParentKey: json.RawMessage("null"), KernelOwner: json.RawMessage("null")}
	fixture := guardedProtocolFixture{offer, claim, spec, memory, inspect, activation}
	fixture.save(t, "inspect", inspect)
	fixture.save(t, "activate", activation)
	return fixture
}

func (f guardedProtocolFixture) save(t *testing.T, action string, value any) {
	t.Helper()
	if err := WriteJSON(f.offer.Helper+"."+action+".json", value); err != nil {
		t.Fatal(err)
	}
}

func (f guardedProtocolFixture) pending(t *testing.T) {
	t.Helper()
	r := scopeInspectionReceipt(f.inspect)
	r["status"], r["exit_code"], r["invocation_id"] = "created", nil, ""
	r["owner"] = map[string]any{"kind": "unit", "unit": f.spec.ScopeUnit}
	f.inspect["pending"], f.inspect["alive"], f.inspect["quiescent"], f.inspect["allocation_claim"] = true, false, false, "reserved"
	f.inspect["unit_state"] = map[string]any{}
	f.save(t, "inspect", f.inspect)
}

func TestGuardedActivationRequiresFreshExactTransition(t *testing.T) {
	for _, name := range []string{"fresh", "repeat", "wrong_key", "wrong_digest", "partial_charge", "borrowed_parent", "existing_kernel"} {
		t.Run(name, func(t *testing.T) {
			f := guardedProtocol(t)
			f.pending(t)
			switch name {
			case "repeat":
				f.activation.TransitionMade = false
			case "wrong_key":
				f.activation.Key.RegistrationID = strings.Repeat("9", 32)
			case "wrong_digest":
				f.activation.BindingDigest = strings.Repeat("8", 64)
			case "partial_charge":
				f.activation.ChargedBytes--
			case "borrowed_parent":
				f.activation.ParentKey = json.RawMessage("{}")
			case "existing_kernel":
				f.activation.KernelOwner = json.RawMessage("{}")
			}
			f.save(t, "activate", f.activation)
			result, err := ActivateResources(context.Background(), f.claim, f.spec, f.offer)
			if (err == nil) != (name == "fresh") {
				t.Fatalf("activation result=%+v err=%v", result, err)
			}
			args, _ := os.ReadFile(f.offer.Helper + ".args")
			wantObserve := "CALL\nexternal\ninspect\n" + f.claim.RunID + "\n--registration-id\n" + f.claim.RegistrationID +
				"\n--receipts-root\n" + f.claim.ReceiptsRoot + "\n--default-home\n" + f.claim.DefaultHome + "\n"
			if !strings.HasPrefix(string(args), wantObserve) || strings.Count(string(args), "\nactivate\n") != 1 {
				t.Fatalf("activation did not preserve original read/transition arguments: %s", args)
			}
		})
	}
}

func TestGuardedActivationRejectsOriginalMismatchBeforeTransition(t *testing.T) {
	for _, name := range []string{"deadline", "envelope", "source", "context", "offer", "already_started"} {
		t.Run(name, func(t *testing.T) {
			f := guardedProtocol(t)
			f.pending(t)
			switch name {
			case "deadline":
				f.memory.DeadlineMS++
			case "envelope":
				f.memory.UnitEnvelope.StopUSec++
			case "source":
				f.memory.ProducerSource.SourceCommit = strings.Repeat("7", 40)
			case "context":
				f.memory.ExecutionContext = json.RawMessage(strings.Replace(guardedContextFixture, "fixture-policy", "other-policy", 1))
			case "offer":
				f.offer.ControlLocator.ReceiptsRoot += "-changed"
			case "already_started":
				scopeInspectionReceipt(f.inspect)["status"] = "running"
			}
			scopeInspectionReceipt(f.inspect)["host_memory"] = f.memory
			f.save(t, "inspect", f.inspect)
			if _, err := ActivateResources(context.Background(), f.claim, f.spec, f.offer); err == nil {
				t.Fatal("changed original granted a fresh launch")
			}
			args, _ := os.ReadFile(f.offer.Helper + ".args")
			if strings.Contains(string(args), "\nactivate\n") {
				t.Fatal("mismatched original reached activation")
			}
		})
	}
}

func TestGuardedLostActivationNeverRetriesOrLaunches(t *testing.T) {
	f := guardedProtocol(t)
	f.pending(t)
	_ = os.WriteFile(f.offer.Helper+".activate.fail", nil, 0600)
	if _, err := ActivateResources(context.Background(), f.claim, f.spec, f.offer); err == nil {
		t.Fatal("lost activation reply granted launch")
	}
	args, _ := os.ReadFile(f.offer.Helper + ".args")
	if strings.Count(string(args), "\nactivate\n") != 1 || strings.Contains(string(args), "\nexec\n") || strings.Contains(string(args), "\ncancel\n") {
		t.Fatalf("lost reply triggered retry, launch or cancellation: %s", args)
	}
}

func TestGuardedBeginLostReplyKeepsOriginalWithoutRetry(t *testing.T) {
	f := guardedProtocol(t)
	_ = os.WriteFile(f.offer.Helper+".begin.fail", nil, 0600)
	if _, err := BeginResourcesWithRetry(context.Background(), f.claim, t.TempDir(), f.spec.ScopeUnit, time.Minute); err == nil {
		t.Fatal("lost begin reply was accepted")
	}
	args, _ := os.ReadFile(f.offer.Helper + ".args")
	if strings.Count(string(args), "\nbegin\n") != 1 {
		t.Fatalf("guarded begin was retried: %s", args)
	}
}

func TestGuardedScopeUsesOriginalFiniteDeadline(t *testing.T) {
	f := guardedProtocol(t)
	now := time.UnixMilli(f.spec.Deadline - 30000)
	name, args, err := GuardedScopeCommand(f.claim, f.spec, []string{f.claim.Helper, "external", "exec"}, now)
	if err != nil || name != "systemd-run" {
		t.Fatalf("finite scope rejected: %v", err)
	}
	joined := strings.Join(args, "\n")
	for _, property := range []string{"MemoryMax=6442450944", "MemorySwapMax=0", "TasksMax=4096", "RuntimeMaxSec=28000000us",
		"RuntimeRandomizedExtraSec=0", "TimeoutStopSec=90000000us", "KillMode=control-group", "SendSIGKILL=yes"} {
		if !strings.Contains(joined, "--property="+property+"\n") {
			t.Fatalf("missing finite property %s: %v", property, args)
		}
	}
	if strings.Contains(joined, "--property=Nice=") || f.spec.Deadline != now.UnixMilli()+30000 {
		t.Fatal("scope changed the original clock or invented a scope Nice property")
	}
	if _, _, err := GuardedScopeCommand(f.claim, f.spec, []string{"true"}, time.UnixMilli(f.spec.Deadline-1000)); err == nil {
		t.Fatal("expired startup allowance permitted owner creation")
	}
}

func TestGuardedLifecycleCommandsKeepSavedLocator(t *testing.T) {
	f := guardedProtocol(t)
	name, args := ResourceCommand(f.claim, []string{"/original/runner", "runner", "/original/spec"})
	want := []string{"--config", f.claim.Config, "external", "exec", f.claim.RunID, "--registration-id", f.claim.RegistrationID,
		"--receipts-root", f.claim.ReceiptsRoot, "--default-home", f.claim.DefaultHome, "--", "/original/runner", "runner", "/original/spec"}
	if name != f.claim.Helper || !reflect.DeepEqual(args, want) {
		t.Fatalf("exec replaced saved controls: %s %v", name, args)
	}
	f.activation.State, f.activation.TransitionMade = "closing", true
	f.save(t, "close", f.activation)
	if err := CloseResources(context.Background(), f.claim); err != nil {
		t.Fatal(err)
	}
	f.save(t, "finish", Settlement{RunID: f.claim.RunID, Status: "succeeded", Quiescent: true})
	if _, err := FinishResources(context.Background(), f.claim, 0); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(f.claim.Helper + ".args")
	if !strings.Contains(string(log), "\nclose\n") || !strings.Contains(string(log), "\nfinish\n") {
		t.Fatal("missing original close/finish")
	}
}

func TestGuardedObservationPreservesOriginalPhysicalIdentity(t *testing.T) {
	f := guardedProtocol(t)
	got, err := ObserveResourceOwnership(context.Background(), f.claim, f.spec.ScopeUnit, nil)
	if err != nil || got.ScopeIdentity == nil || got.ScopeIdentity.CgroupDevice != 29 || got.ScopeIdentity.CgroupInode != 12345 {
		t.Fatalf("physical identity lost: %+v %v", got, err)
	}
	original := *got.ScopeIdentity
	scopeInspectionCgroup(f.inspect)["inode"] = 12346
	f.save(t, "inspect", f.inspect)
	if _, err := ObserveResourceOwnership(context.Background(), f.claim, f.spec.ScopeUnit, &original); err == nil {
		t.Fatal("a replacement inode replaced the original guarded owner")
	}
	identity, err := InspectResourceScope(context.Background(), f.claim, f.spec.ScopeUnit)
	if err != nil || identity.CgroupInode != 12346 {
		t.Fatalf("receipt recovery did not retain its exact identity: %+v %v", identity, err)
	}
}

func TestGuardedObservationRetainsPendingAndUnknownCharge(t *testing.T) {
	for _, kind := range []string{"unactivated", "bound_but_created", "unknown", "previous_boot", "revoked"} {
		t.Run(kind, func(t *testing.T) {
			f := guardedProtocol(t)
			r := scopeInspectionReceipt(f.inspect)
			switch kind {
			case "unactivated", "bound_but_created":
				r["status"], f.inspect["pending"], f.inspect["alive"], f.inspect["allocation_claim"] = "created", true, false, "reserved"
				f.inspect["unit_state"] = map[string]any{}
				if kind == "unactivated" {
					r["invocation_id"] = ""
					r["owner"] = map[string]any{"kind": "unit", "unit": f.spec.ScopeUnit}
				}
			case "unknown":
				f.inspect["alive"], f.inspect["allocation_claim"] = false, "unknown"
			case "previous_boot":
				r["status"], r["owner_quiescent_verified"] = "failed", true
				f.inspect["alive"], f.inspect["quiescent"], f.inspect["allocation_claim"] = false, true, "previous-boot"
			case "revoked":
				r["status"], r["owner_quiescent_verified"], r["invocation_id"] = "cancelled", true, ""
				r["owner"] = map[string]any{"kind": "unit", "unit": f.spec.ScopeUnit}
				f.inspect["alive"], f.inspect["quiescent"], f.inspect["allocation_claim"] = false, true, "revoked"
			}
			f.save(t, "inspect", f.inspect)
			got, err := ObserveResourceOwnership(context.Background(), f.claim, f.spec.ScopeUnit, nil)
			if kind == "previous_boot" {
				if err == nil {
					t.Fatal("changed boot released guarded ownership")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != "revoked" && (got.Quiescent || got.OwnerQuiescentVerified) {
				t.Fatal("pending or unknown owner was released")
			}
			if kind == "bound_but_created" && (got.ScopeIdentity == nil || got.ScopeIdentity.CgroupInode != 12345) {
				t.Fatal("failed startup lost its already recorded physical owner")
			}
		})
	}
}

func TestGuardedPriorityReadbackMatchesLinuxRawConvention(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux raw priority convention")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	actual, err := guardedThreadNice()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("/proc/thread-self/stat")
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndexByte(string(data), ')')
	fields := strings.Fields(string(data[end+1:]))
	want, err := strconv.Atoi(fields[16])
	if err != nil || actual != want {
		t.Fatalf("readback=%d proc nice=%d err=%v", actual, want, err)
	}
}
func TestGuardedRunnerSpecSelectionPreservesLegacyVersion(t *testing.T) {
	offer := guardedTestOffer(t)
	claim := guardedTestClaim(t, offer)
	root := t.TempDir()
	specPath := filepath.Join(root, "job_deadbeef.spec.json")
	spec := Spec{Version: RunnerSpecVersion, Command: "true", Cwd: root, TimeoutMS: 60000,
		Deadline: time.Now().Add(time.Minute).UnixMilli(), Limit: 4096, Nice: 10,
		Stdout: filepath.Join(root, "out"), Stderr: filepath.Join(root, "err"), Exit: filepath.Join(root, "exit"),
		Result: filepath.Join(root, "result"), Control: filepath.Join(root, "control"), OutputDir: filepath.Join(root, "rendered")}
	if RunnerSpecVersion != 2 || spec.Validate(specPath) != nil {
		t.Fatal("legacy specification version changed")
	}
	spec.ResourceClaim, spec.ScopeUnit = &claim, scopeInspectionUnit
	if spec.Validate(specPath) == nil {
		t.Fatal("legacy specification silently selected guarded execution")
	}
	spec.Version = GuardedRunnerSpecVersion
	if err := spec.Validate(specPath); err != nil {
		t.Fatal(err)
	}
	spec.Command = strings.Repeat("x", MaxGuardedMetadataBytes)
	if spec.Validate(specPath) == nil {
		t.Fatal("guarded specification exceeded the original producer byte limit")
	}
	legacy := spec
	legacy.Version, legacy.ResourceClaim, legacy.ScopeUnit = RunnerSpecVersion, nil, ""
	if err := legacy.Validate(specPath); err != nil {
		t.Fatal("guarded metadata bound changed legacy specification behavior")
	}
	spec.Command = "true"
	spec.Nice++
	if spec.Validate(specPath) == nil {
		t.Fatal("guarded specification ignored its accepted priority")
	}
	spec.Nice--
	spec.ResourceClaim = nil
	if spec.Validate(specPath) == nil {
		t.Fatal("guarded specification accepted a missing owner")
	}
}

func TestGuardedUnallocatedCancellationRequiresExactTombstone(t *testing.T) {
	for _, name := range []string{"valid", "locator", "clock", "allocated", "owner", "missing_proof", "wrong_uid", "accepted_owner"} {
		t.Run(name, func(t *testing.T) {
			f := guardedProtocol(t)
			r := map[string]any{"schema": 1, "policy_version": 1, "run_id": f.claim.RunID, "registration_id": f.claim.RegistrationID,
				"owner_uid": os.Geteuid(), "boot_id": scopeInspectionBootID, "owner": map[string]any{"kind": "revoked"}, "unit": nil,
				"status": "cancelled", "purpose": "tool", "pinned": false, "cache_consumer": false, "checkout": nil,
				"started_at": 1234.5, "finished_at": 1234.5, "scratch_removed_at": 1234.5, "expires_at": 9999,
				"exit_code": 125, "owner_quiescent_verified": true, "control_locator": f.offer.ControlLocator}
			f.inspect["receipt"], f.inspect["unit_state"] = r, map[string]any{}
			f.inspect["pending"], f.inspect["alive"], f.inspect["quiescent"], f.inspect["allocation_claim"] = false, false, true, "revoked"
			var expected *ScopeIdentity
			switch name {
			case "locator":
				locator := f.offer.ControlLocator
				locator.DefaultHome += "-other"
				r["control_locator"] = locator
			case "clock":
				r["finished_at"] = 1234.6
			case "allocated":
				r["scratch"] = "/allocated/scratch"
			case "owner":
				r["owner"] = map[string]any{"kind": "revoked", "unit": scopeInspectionUnit}
			case "missing_proof":
				delete(r, "owner_quiescent_verified")
			case "wrong_uid":
				r["owner_uid"] = os.Geteuid() + 1
			case "accepted_owner":
				expected = &ScopeIdentity{Version: 1, Unit: scopeInspectionUnit, BootID: scopeInspectionBootID,
					InvocationID: scopeInspectionInvocationID, Cgroup: scopeInspectionGroupParent + scopeInspectionUnit, CgroupDevice: 29, CgroupInode: 12345}
			}
			f.save(t, "inspect", f.inspect)
			observed, err := ObserveResourceOwnership(context.Background(), f.claim, f.spec.ScopeUnit, expected)
			if (err == nil) != (name == "valid") {
				t.Fatalf("tombstone observation=%+v err=%v", observed, err)
			}
			if name == "valid" {
				if !observed.Quiescent || !observed.OwnerQuiescentVerified || observed.ScopeIdentity != nil {
					t.Fatal("unallocated original proof was lost or fabricated a kernel owner")
				}
				if err := CloseResources(context.Background(), f.claim); err != nil {
					t.Fatalf("already revoked original was not closed: %v", err)
				}
				args, _ := os.ReadFile(f.claim.Helper + ".args")
				if strings.Contains(string(args), "\nclose\n") {
					t.Fatal("verified original tombstone invoked a new transition")
				}
			}
		})
	}
}

func TestGuardedSettlementRequiresExactTerminalProof(t *testing.T) {
	for _, action := range []string{"finish", "cancel"} {
		for _, name := range []string{"valid", "cancelled", "running", "missing_status", "missing_cancelled", "null_cancelled", "contradictory", "not_quiet", "wrong_run", "extra"} {
			t.Run(action+"/"+name, func(t *testing.T) {
				f := guardedProtocol(t)
				response := map[string]any{"run_id": f.claim.RunID, "status": "succeeded", "quiescent": true, "cancelled": false}
				switch name {
				case "cancelled":
					response["status"], response["cancelled"] = "cancelled", true
				case "running":
					response["status"] = "running"
				case "missing_status":
					delete(response, "status")
				case "missing_cancelled":
					delete(response, "cancelled")
				case "null_cancelled":
					response["cancelled"] = nil
				case "contradictory":
					response["cancelled"] = true
				case "not_quiet":
					response["quiescent"] = false
				case "wrong_run":
					response["run_id"] = strings.Repeat("7", 32)
				case "extra":
					response["settled"] = true
				}
				f.save(t, action, response)
				var err error
				if action == "finish" {
					_, err = FinishResources(context.Background(), f.claim, 0)
				} else {
					_, err = CancelResources(context.Background(), f.claim)
				}
				if (err == nil) != (name == "valid" || name == "cancelled") {
					t.Fatalf("settlement accepted=%t error=%v", err == nil, err)
				}
			})
		}
	}
}
