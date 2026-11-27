package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
)

// These are admission and persistence fixtures, not native owner proofs.
// NewPaused leaves dispatch/recovery stopped. The capture seam supplies a
// synthetic validated snapshot; no helper, runner or systemd unit is started.
func guardedJobsFixture(t *testing.T) (*Service, *config.Config, testResolver, *process.GuardedResourceOffer) {
	t.Helper()
	s, cfg, resolver := queueFixture(t)
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	cfg.Set("guardedHostResources", true)
	cfg.Set("hostResourcesStopTimeoutMs", 90000)
	helper := filepath.Join(cfg.Home, "producer", "bin", "ssd-dev")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf forbidden > \"$0.called\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	producerConfig := filepath.Join(cfg.Home, "producer.json")
	if err := os.WriteFile(producerConfig, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Set("guardedHostResourcesHelper", helper)
	cfg.Set("hostResourcesConfig", producerConfig)
	for _, slot := range []*string{&s.executable, &s.launcherExecutable} {
		file, err := os.CreateTemp(cfg.Home, "unexecuted-binary-")
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file.Name(), 0700); err != nil {
			t.Fatal(err)
		}
		*slot = file.Name()
	}
	// Only bypass startup readiness in this no-background unit fixture.
	s.scopesReady.Store(true)
	offer := guardedJobsOffer(t, cfg.Home, helper, producerConfig, "frozen-fixture")
	s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		return offer, nil
	}
	t.Cleanup(func() {
		if _, err := os.Stat(helper + ".called"); !os.IsNotExist(err) {
			t.Errorf("an admission-only fixture invoked the producer: %v", err)
		}
	})
	return s, cfg, resolver, offer
}

func guardedJobsOffer(t *testing.T, state, helper, producerConfig, revision string) *process.GuardedResourceOffer {
	t.Helper()
	// Keep the explicit producer schema independent of the receiver's private
	// timeout-key slice so an incompatible schema change fails this fixture.
	names := strings.Fields(
		"heavy_exit_seconds heavy_drain_seconds heavy_adopted_child_seconds heavy_full_poll_seconds heavy_cleanup_poll_seconds " +
			"darwin_poll_seconds detached_start_seconds detached_poll_seconds command_group_settle_seconds supervisor_term_seconds " +
			"supervisor_owned_reap_seconds supervisor_post_exit_seconds supervisor_control_seconds supervisor_max_grace_seconds supervisor_poll_seconds " +
			"ssh_connect_seconds host_probe_seconds host_probe_via_seconds queue_terminate_seconds queue_settle_seconds queue_output_drain_seconds " +
			"queue_control_seconds queue_scope_query_seconds queue_scope_stop_seconds queue_scope_terminate_seconds queue_db_wait_seconds " +
			"queue_source_git_seconds queue_stack_release_seconds maintenance_cache_pass_seconds idle_stack_seconds generation_grace_seconds " +
			"landing_lock_poll_seconds landing_inventory_inner_seconds landing_inventory_kill_seconds landing_inventory_outer_seconds " +
			"stack_external_seconds stack_stop_grace_seconds stack_kill_settle_seconds stack_finite_poll_seconds stack_role_poll_seconds " +
			"stack_stop_poll_seconds stack_supervisor_start_seconds stack_supervisor_start_failure_seconds stack_supervisor_request_seconds " +
			"stack_supervisor_settle_seconds stack_supervisor_poll_seconds stack_ephemeral_probe_seconds")
	if len(names) != 47 {
		t.Fatal("guarded fixture timeout schema is incomplete")
	}
	timeouts := make(map[string]any, len(names))
	for _, name := range names {
		timeouts[name] = json.Number("1.2500000000000001")
	}
	raw, err := json.Marshal(map[string]any{
		"schema_version": 1, "config_revision": revision, "config_sha256": strings.Repeat("a", 64),
		"resources": map[string]any{"stack_memory_mib": 6144, "stack_tasks": 4096},
		"timeouts":  timeouts, "priorities": map[string]any{"stream_nice": 10, "ionice_class": 2, "ionice_priority": 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	offer := &process.GuardedResourceOffer{
		Version: 1, JobsState: state, Helper: helper, Config: producerConfig, StopTimeoutUSec: 90000000,
		ControlLocator:   process.ResourceControlLocator{Version: 1, ReceiptsRoot: filepath.Join(state, "original-receipts", "runs"), DefaultHome: filepath.Join(state, "original-home")},
		ExecutionContext: raw,
		FleetRelease:     process.FleetReleaseSnapshot{Root: filepath.Join(state, "releases", digest), Version: "fixture-v1", Digest: digest, Python: "/usr/bin/python3"},
		ProducerSource: process.ProducerSourceSnapshot{CheckoutRoot: filepath.Dir(filepath.Dir(helper)), ExecutablePath: helper,
			ExecutableSHA256: strings.Repeat("c", 64), SourceCommit: strings.Repeat("d", 40), InterpreterPath: "/usr/bin/python3"},
	}
	if err := offer.Validate(state); err != nil {
		t.Fatal("invalid synthetic producer offer", err)
	}
	return offer
}

func guardedJobsStoredRecord(t *testing.T, s *Service, id string) Record {
	t.Helper()
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := json.Unmarshal(payload, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func guardedJobsNoAcceptance(t *testing.T, s *Service) {
	t.Helper()
	if len(s.Snapshot()) != 0 {
		t.Fatal("refused capture published a job")
	}
	var rows int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("refused capture left a durable accepted row", rows, err)
	}
	files, err := os.ReadDir(s.dir)
	if err != nil || len(files) != 0 {
		t.Fatal("refused capture created an owner or execution artifact", err)
	}
}

func TestGuardedCaptureRefusalPrecedesDurableAcceptance(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, nilOffer := range []bool{false, true} {
			name := "immediate"
			if queued {
				name = "queued"
			}
			if nilOffer {
				name += "-nil-offer"
			}
			t.Run(name, func(t *testing.T) {
				s, _, resolver, _ := guardedJobsFixture(t)
				refusal := errors.New("fixture capture refused")
				calls := 0
				s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
					calls++
					if nilOffer {
						return nil, nil
					}
					return nil, refusal
				}
				o := queueOption(resolver, queueOwner("FP5"), "must remain unaccepted")
				var err error
				if queued {
					var records []Record
					records, err = s.enqueue([]startOptions{o}, "refused-capture", 3600000, []any{o.Command})
					if len(records) != 0 {
						t.Fatal("refused queue returned an accepted ID")
					}
				} else {
					var record Record
					record, _, err = s.admit(o)
					if record.ID != "" {
						t.Fatal("refused immediate call returned an accepted ID")
					}
				}
				if calls != 1 || err == nil || !nilOffer && !errors.Is(err, refusal) {
					t.Fatal("capture refusal was lost or retried", calls, err)
				}
				guardedJobsNoAcceptance(t, s)
			})
		}
	}
}

func TestGuardedCustomStateRefusesBeforeHelperInvocation(t *testing.T) {
	s, _, resolver, _ := guardedJobsFixture(t)
	s.guardedCapture = nil // Exercise the real state guard, not the positive seam.
	o := queueOption(resolver, queueOwner("FP5"), "custom state must not execute")
	if _, err := s.enqueue([]startOptions{o}, "custom-state", 3600000, []any{o.Command}); err == nil || !strings.Contains(err.Error(), "passwd-home") {
		t.Fatalf("unsupported physical state reached acceptance: %v", err)
	}
	guardedJobsNoAcceptance(t, s)
}

func TestGuardedCaptureWriteFailureDoesNotAcknowledge(t *testing.T) {
	s, _, resolver, offer := guardedJobsFixture(t)
	calls := 0
	s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		calls++
		return offer, nil
	}
	if _, err := s.db.Exec("CREATE TRIGGER refuse_guarded_admission BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'fixture durable write refusal'); END"); err != nil {
		t.Fatal(err)
	}
	o := queueOption(resolver, queueOwner("FP5"), "capture is not an acknowledgment")
	if records, err := s.enqueue([]startOptions{o}, "write-refused", 3600000, []any{o.Command}); err == nil || len(records) != 0 || calls != 1 {
		t.Fatal("read-only capture bypassed the durable acceptance commit", err, calls)
	}
	guardedJobsNoAcceptance(t, s)
}

func TestGuardedQueueOfferAndExecutablesSurviveDelayedPreparation(t *testing.T) {
	s, cfg, resolver, offer := guardedJobsFixture(t)
	expected := process.CloneGuardedResourceOffer(offer)
	originalRunner, originalLauncher := s.executable, s.launcherExecutable
	runnerLink, launcherLink := filepath.Join(cfg.Home, "runner-current"), filepath.Join(cfg.Home, "launcher-current")
	for link, target := range map[string]string{runnerLink: originalRunner, launcherLink: originalLauncher} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	s.executable, s.launcherExecutable = runnerLink, launcherLink
	calls := 0
	s.guardedCapture = func(_ context.Context, helper, producer, state string, stop uint64) (*process.GuardedResourceOffer, error) {
		calls++
		if helper != expected.Helper || producer != expected.Config || state != cfg.Home || stop != expected.StopTimeoutUSec {
			t.Fatal("capture did not receive the actual selected acceptance bindings")
		}
		return offer, nil
	}
	o := queueOption(resolver, queueOwner("FP5"), "original guarded queue")
	queued := enqueueTest(t, s, []startOptions{o}, "frozen-offer")[0]
	physicalRunner, err := filepath.EvalSymlinks(originalRunner)
	if err != nil {
		t.Fatal(err)
	}
	physicalLauncher, err := filepath.EvalSymlinks(originalLauncher)
	if err != nil {
		t.Fatal(err)
	}
	stored := guardedJobsStoredRecord(t, s, queued.ID)
	if calls != 1 || !reflect.DeepEqual(stored.GuardedResourceOffer, expected) || stored.ExecutionNice == nil || *stored.ExecutionNice != 10 ||
		stored.RunnerExecutable != physicalRunner || stored.LauncherExecutable != physicalLauncher {
		t.Fatal("queue returned before persisting its original offer or executable selection")
	}
	if stored.ResourceClaim != nil || stored.ScopeUnit != "" || stored.PID != 0 || stored.DeadlineMS != 0 {
		t.Fatal("queue acceptance allocated an execution owner")
	}
	for _, path := range []string{stored.SpecPath, stored.SpecPath + ".started", stored.GrantPath, stored.ControlPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("queued acceptance created an execution artifact", err)
		}
	}
	for link, target := range map[string]string{runnerLink: originalLauncher, launcherLink: originalRunner} {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Set("guardedHostResources", false)
	cfg.Set("guardedHostResourcesHelper", "/changed/producer/bin/ssd-dev")
	cfg.Set("hostResourcesConfig", "/changed/policy.json")
	cfg.Set("hostResourcesStopTimeoutMs", 600000)
	cfg.Set("jobNice", 19)
	cfg.Set("runnerExecutable", originalLauncher)
	cfg.HostResourcesHelper = ""
	cfg.Set("jobScopes", false)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if restored.useScopes || !restored.requiresScopes() || restored.scopesReady.Load() {
		t.Fatal("disabling fresh guarded acceptance erased the scope requirement of a recovered job")
	}
	// Mark only background publication active in the fixture; no worker starts.
	restored.backgroundActive.Store(true)
	if restored.BackgroundReady() || restored.startupAdmissionError() == nil {
		t.Fatal("recovered guarded work bypassed its pending capability check")
	}
	restored.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		t.Fatal("delayed preparation recaptured an accepted context")
		return nil, errors.New("unexpected recapture")
	}
	prepared, ok := restored.Get(queued.ID, queued.WorkspaceID)
	if !ok || !reflect.DeepEqual(prepared.GuardedResourceOffer, expected) {
		t.Fatal("reopen lost the original acceptance offer")
	}
	queueDeadline := prepared.QueueDeadlineMS
	if err := restored.prepareExecution(&prepared); err != nil {
		t.Fatal(err)
	}
	claim := prepared.ResourceClaim
	if claim == nil || claim.Version != 2 || claim.Helper != expected.Helper || claim.Config != expected.Config ||
		claim.ReceiptsRoot != expected.ControlLocator.ReceiptsRoot || claim.DefaultHome != expected.ControlLocator.DefaultHome ||
		claim.StopTimeoutUSec != expected.StopTimeoutUSec || !bytes.Equal(claim.ExecutionContext, expected.ExecutionContext) ||
		prepared.ExecutionNice == nil || *prepared.ExecutionNice != 10 || prepared.RunnerExecutable != physicalRunner || prepared.LauncherExecutable != physicalLauncher {
		t.Fatal("delayed preparation substituted current guarded configuration or executable links")
	}
	if prepared.QueueDeadlineMS != queueDeadline || prepared.TimeoutMS != queued.TimeoutMS || prepared.DeadlineMS-prepared.StartedAtMS != queued.TimeoutMS {
		t.Fatal("preparation changed the accepted queue or execution allowance")
	}
	if prepared.ScopeUnit == "" || claim.RunID == "" || claim.RegistrationID == "" {
		t.Fatal("preparation did not create durable execution identifiers")
	}
	if _, err := os.Stat(prepared.SpecPath); !os.IsNotExist(err) {
		t.Fatal("metadata-only preparation started execution")
	}
}

func TestGuardedQueueDuplicateAndHoldDoNotRecapture(t *testing.T) {
	s, _, resolver, offer := guardedJobsFixture(t)
	calls := 0
	s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		calls++
		return offer, nil
	}
	o := queueOption(resolver, queueOwner("FP5"), "original duplicate key")
	first := enqueueTest(t, s, []startOptions{o}, "original-key")[0]
	if _, err := s.BeginExecutionHold("guarded-no-new-capture"); err != nil {
		t.Fatal(err)
	}
	s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		calls++
		return nil, errors.New("current producer unavailable")
	}
	again := enqueueTest(t, s, []startOptions{o}, "original-key")[0]
	if again.ID != first.ID || calls != 1 || !reflect.DeepEqual(again.GuardedResourceOffer, first.GuardedResourceOffer) {
		t.Fatal("duplicate receipt recaptured or replaced its original acceptance")
	}
	if records, err := s.enqueue([]startOptions{o}, "fresh-key", 3600000, []any{o.Command}); !IsExecutionHeld(err) || len(records) != 0 {
		t.Fatal("held queue accepted fresh guarded work", err)
	}
	if record, _, err := s.admit(o); !IsExecutionHeld(err) || record.ID != "" {
		t.Fatal("held direct admission reached guarded capture", err)
	}
	if calls != 1 || len(s.Snapshot()) != 1 {
		t.Fatal("held refusal changed original accepted work")
	}
}

func TestGuardedAcceptanceAndReadSnapshotsDetachRawContext(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "prepared"
		if queued {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			s, _, resolver, offer := guardedJobsFixture(t)
			expected := process.CloneGuardedResourceOffer(offer)
			o := queueOption(resolver, queueOwner("FP5"), "detached acceptance")
			var accepted Record
			if queued {
				accepted = enqueueTest(t, s, []startOptions{o}, "detached")[0]
			} else {
				accepted = occupy(t, s, o)
			}
			id := accepted.ID
			mutate := func(r *Record) {
				r.GuardedResourceOffer.ExecutionContext[0] = '!'
				r.GuardedResourceOffer.Config = "/mutated/config"
				*r.ExecutionNice = 19
				if r.ResourceClaim != nil {
					r.ResourceClaim.ExecutionContext[0] = '!'
					r.ResourceClaim.Config = "/mutated/claim"
				}
			}
			// Both the producer's returned object and the accepted return value
			// must be detached from subsequent stored/public snapshots.
			offer.ExecutionContext[0] = '!'
			offer.Config = "/producer-mutated/config"
			mutate(&accepted)
			got, ok := s.Get(id, o.Workspace.ID)
			if !ok {
				t.Fatal("accepted record disappeared")
			}
			mutate(&got)
			snapshot := s.Snapshot()
			if len(snapshot) != 1 {
				t.Fatal("unexpected fixture inventory")
			}
			mutate(&snapshot[0])
			if _, err := s.update(id, func(r *Record) bool { r.Acknowledged = true; return true }); err != nil {
				t.Fatal("a returned mutable reference contaminated the stored record", err)
			}
			for _, current := range []Record{guardedJobsStoredRecord(t, s, id), s.Snapshot()[0]} {
				if !reflect.DeepEqual(current.GuardedResourceOffer, expected) || current.ExecutionNice == nil || *current.ExecutionNice != 10 {
					t.Fatal("a source or returned snapshot mutated the frozen acceptance")
				}
				if !queued && (current.ResourceClaim == nil || current.ResourceClaim.Config != expected.Config || !bytes.Equal(current.ResourceClaim.ExecutionContext, expected.ExecutionContext)) {
					t.Fatal("a returned claim shared its mutable raw context with the stored owner")
				}
			}
		})
	}
}

func TestGuardedOptInDoesNotUpgradeLegacyQueuedAcceptance(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	legacyHelper := filepath.Join(cfg.Home, "legacy", "ssd-dev")
	cfg.HostResourcesHelper = legacyHelper
	s.useScopes = true
	s.scopesReady.Store(true)
	o := queueOption(resolver, queueOwner("FP5"), "legacy queued original")
	original := enqueueTest(t, s, []startOptions{o}, "legacy-key")[0]
	if original.GuardedResourceOffer != nil || original.ResourceClaim != nil {
		t.Fatal("legacy queue fixture was already guarded")
	}
	cfg.Set("guardedHostResources", true)
	cfg.Set("guardedHostResourcesHelper", "/new/producer/bin/ssd-dev")
	cfg.Set("hostResourcesConfig", "/new/policy.json")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	calls := 0
	restored.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		calls++
		return nil, errors.New("legacy acceptance must not capture")
	}
	prepared, ok := restored.Get(original.ID, original.WorkspaceID)
	if !ok {
		t.Fatal("legacy acceptance disappeared on reopen")
	}
	if err := restored.prepareExecution(&prepared); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || prepared.GuardedResourceOffer != nil || prepared.ResourceClaim == nil || prepared.ResourceClaim.Version != 1 ||
		prepared.ResourceClaim.Helper != legacyHelper || prepared.ExecutionNice != nil || prepared.RunnerExecutable != "" || prepared.LauncherExecutable != "" {
		t.Fatal("fresh guarded opt-in changed an already accepted legacy contract")
	}
	if prepared.TimeoutMS != original.TimeoutMS || prepared.QueueDeadlineMS != original.QueueDeadlineMS {
		t.Fatal("guarded opt-in changed legacy timing")
	}
}

func TestGuardedNativeRecordLimitRefusesBeforeAcceptance(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "immediate"
		if queued {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg, resolver, base := guardedJobsFixture(t)
			// This context is valid in isolation. The native Job must later carry
			// both its accepted offer and prepared claim, exceeding the reader's
			// finite per-record bound even with ample total retention space.
			offer := guardedJobsOffer(t, cfg.Home, base.Helper, base.Config, strings.Repeat("r", 640*1024))
			if len(offer.ExecutionContext) >= process.MaxGuardedMetadataBytes || 2*len(offer.ExecutionContext) <= process.MaxGuardedMetadataBytes {
				t.Fatal("fixture does not separate one context from the native record's two copies")
			}
			cfg.Set("maxRetainedJobBytes", int64(64*1024*1024))
			calls := 0
			s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
				calls++
				return offer, nil
			}
			o := queueOption(resolver, queueOwner("FP5"), "oversized native job")
			var err error
			if queued {
				var records []Record
				records, err = s.enqueue([]startOptions{o}, "oversized-native-record", 3600000, []any{o.Command})
				if len(records) != 0 {
					t.Fatal("oversized queue returned an accepted ID")
				}
			} else {
				var record Record
				record, _, err = s.admit(o)
				if record.ID != "" {
					t.Fatal("oversized immediate call returned an accepted ID")
				}
			}
			if calls != 1 || errorCode(err) != "guarded_metadata_limit" {
				t.Fatal("native record limit was not enforced before acknowledgment", calls, err)
			}
			guardedJobsNoAcceptance(t, s)
		})
	}
}

func TestGuardedQueueReservationCoversFutureClaimContext(t *testing.T) {
	s, cfg, resolver, base := guardedJobsFixture(t)
	offer := guardedJobsOffer(t, cfg.Home, base.Helper, base.Config, strings.Repeat("r", 256*1024))
	s.guardedCapture = func(context.Context, string, string, string, uint64) (*process.GuardedResourceOffer, error) {
		return offer, nil
	}
	o := queueOption(resolver, queueOwner("FP5"), "large frozen context")
	queued := enqueueTest(t, s, []startOptions{o}, "large-guarded-context")[0]
	projected, err := s.recordPayloadBytes([]Record{queued})
	if err != nil {
		t.Fatal(err)
	}
	s.admission.Lock()
	inventory, err := s.submissionPayloadBytes()
	s.admission.Unlock()
	if err != nil || projected != inventory {
		t.Fatal("queued admission and stored reservations disagree", projected, inventory, err)
	}
	prepared := copyRecord(queued)
	if err := s.prepareExecution(&prepared); err != nil {
		t.Fatal(err)
	}
	preparedBytes, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	// Persist synthetic lifecycle metadata through the real row/outbox path.
	// There is no launched process or allocation to settle in this fixture.
	prepared.Status = "succeeded"
	prepared.ExitCode = intPointer(0)
	prepared.FinishedAtMS = prepared.StartedAtMS + 1
	prepared.FinishedAt = time.UnixMilli(prepared.FinishedAtMS).UTC().Format(time.RFC3339Nano)
	prepared.Quiescent = true
	if _, err := s.update(queued.ID, func(r *Record) bool { *r = prepared; return true }); err != nil {
		t.Fatal(err)
	}
	jobBytes, completionBytes := submissionProjectionStoredBytes(t, s, queued.ID)
	if completionBytes == 0 || inventory < jobBytes+completionBytes || projected < int64(len(preparedBytes))+completionBytes {
		t.Fatalf("queue omitted future claim/context bytes: reserve=%d prepared=%d job=%d completion=%d", projected, len(preparedBytes), jobBytes, completionBytes)
	}
	s.admission.Lock()
	settledBytes, err := s.submissionPayloadBytes()
	s.admission.Unlock()
	if err != nil || settledBytes != jobBytes+completionBytes {
		t.Fatal("settled fixture did not release only unused reservation", err)
	}
}

// TestGuardedNativeEncodingFixture exercises the originating Go encoders without
// launching an owner. Set FP5_GUARDED_SCHEMA_OUTPUT to a fresh absolute directory
// outside the checkout to retain its exact bytes for schema receiving.
func TestGuardedNativeEncodingFixture(t *testing.T) {
	s, _, resolver, offered := guardedJobsFixture(t)
	owner := queueOwner("FP5")
	owner.Identity.CredentialID = ""
	owner.Identity.PrincipalID = "schema-fixture"
	o := queueOption(resolver, owner, "printf schema-fixture")
	accepted, duplicate, err := s.admit(o)
	if err != nil || duplicate {
		t.Fatal("fixture admission failed", duplicate, err)
	}
	// This is the actual INSERT payload, not a reconstructed example record.
	var nativeJob []byte
	if err := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", accepted.ID).Scan(&nativeJob); err != nil {
		t.Fatal(err)
	}
	var saved Record
	if err := json.Unmarshal(nativeJob, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.GuardedResourceOffer == nil || saved.ResourceClaim == nil || saved.ResourceClaim.Version != 2 ||
		saved.PID != 0 || saved.ScopeIdentity != nil || len(saved.ResourceActivation) != 0 || saved.Identity.CredentialID != "" {
		t.Fatal("fixture must contain accepted metadata without native owner evidence or credentials")
	}
	// Match launch's encoder path, stopping before allocation or process start.
	spec, err := s.runnerSpecification(saved, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.WriteJSON(saved.SpecPath, spec); err != nil {
		t.Fatal(err)
	}
	nativeSpec, err := os.ReadFile(saved.SpecPath)
	if err != nil {
		t.Fatal(err)
	}
	var jobFields, specFields, offerFields map[string]json.RawMessage
	if err := json.Unmarshal(nativeJob, &jobFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(nativeSpec, &specFields); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jobFields["guarded_resource_offer"], &offerFields); err != nil {
		t.Fatal(err)
	}
	keys := strings.Fields("version control_locator jobs_state execution_context fleet_release producer_source helper config stop_timeout_usec")
	if len(offerFields) != len(keys) {
		t.Fatal("native guarded_resource_offer changed its nine-field nesting")
	}
	for _, key := range keys {
		if len(offerFields[key]) == 0 {
			t.Fatalf("native guarded_resource_offer omitted %s", key)
		}
	}
	if !bytes.Equal(offerFields["execution_context"], offered.ExecutionContext) ||
		!bytes.Equal(jobFields["resource_claim"], specFields["resourceClaim"]) ||
		!bytes.Equal(specFields["version"], []byte("3")) {
		t.Fatal("native record/spec encoding changed the original context or prepared claim")
	}
	// Parsing above verifies the contract only. Export the untouched BLOB and
	// on-disk RunnerSpec bytes, preserving nesting, numeric lexemes and types.
	output := os.Getenv("FP5_GUARDED_SCHEMA_OUTPUT")
	if output == "" {
		return
	}
	if !filepath.IsAbs(output) {
		t.Fatal("FP5_GUARDED_SCHEMA_OUTPUT must be an absolute disposable directory")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 {
		t.Fatal("FP5_GUARDED_SCHEMA_OUTPUT must be an empty disposable directory", err)
	}
	const fixtureNote = "FP5 guarded schema receiving fixture\n\n" +
		"This is a paused Go admission/SQLite encoding fixture with a synthetic capture offer.\n" +
		"No helper, runner, scope or resource allocation was started; no native account/owner proof is claimed.\n" +
		"Temporary paths, source pins and minted identifiers belong only to this disposable fixture.\n" +
		"The fixture principal is schema-fixture and its credential identifier is empty.\n\n" +
		"native-job.json is the exact jobs.payload BLOB from Service.admit -> newRecord -> insertRecords -> json.Marshal(Record).\n" +
		"runner-spec.json is the exact file from Service.runnerSpecification -> process.WriteJSON -> json.Marshal(Spec).\n" +
		"guarded_resource_offer is nested in Record; resource_claim and the other accepted job fields remain alongside it.\n" +
		"The prepared claim is also encoded as RunnerSpec.resourceClaim. No exported JSON was reconstructed, reformatted or coerced.\n"
	for name, content := range map[string][]byte{
		"native-job.json":  nativeJob,
		"runner-spec.json": nativeSpec,
		"FIXTURE.txt":      []byte(fixtureNote),
	} {
		if err := os.WriteFile(filepath.Join(output, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("exported native guarded schema fixture to %s", output)
}

func TestGuardedStartedAndFinalizeRetainOriginalKernelAnchor(t *testing.T) {
	cases := []struct {
		name     string
		original bool
		legacy   bool
		absent   bool
		accept   bool
		change   func(*process.ScopeIdentity)
	}{
		{name: "first-complete", accept: true},
		{name: "same-original", original: true, accept: true},
		{name: "first-missing-device", change: func(s *process.ScopeIdentity) { s.CgroupDevice = 0 }},
		{name: "first-missing-inode", change: func(s *process.ScopeIdentity) { s.CgroupInode = 0 }},
		{name: "absent-result", original: true, absent: true},
		{name: "changed-device", original: true, change: func(s *process.ScopeIdentity) { s.CgroupDevice++ }},
		{name: "changed-inode", original: true, change: func(s *process.ScopeIdentity) { s.CgroupInode++ }},
		{name: "changed-invocation", original: true, change: func(s *process.ScopeIdentity) { s.InvocationID = strings.Repeat("b", 32) }},
		{name: "guarded-rejects-legacy-zeros", original: true, change: func(s *process.ScopeIdentity) {
			s.CgroupDevice, s.CgroupInode = 0, 0
		}},
		{name: "legacy-keeps-prior-result-behavior", original: true, legacy: true, accept: true, change: func(s *process.ScopeIdentity) {
			s.InvocationID, s.CgroupDevice, s.CgroupInode = strings.Repeat("b", 32), 0, 0
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg, resolver, offer := guardedJobsFixture(t)
			if tc.legacy {
				// Admit an actual legacy record; do not mutate a guarded claim's
				// version into a synthetic hybrid after acceptance.
				cfg.Set("guardedHostResources", false)
				cfg.HostResourcesHelper = offer.Helper
				s.useScopes = true
			}
			r := occupy(t, s, queueOption(resolver, queueOwner("FP5"), "unlaunched anchor fixture"))
			original := process.ScopeIdentity{
				Version: 1, Unit: r.ScopeUnit + ".scope",
				BootID: "00000000-0000-0000-0000-000000000000", InvocationID: strings.Repeat("a", 32),
				Cgroup: "/user.slice/" + r.ScopeUnit + ".scope", CgroupDevice: 17, CgroupInode: 29,
			}
			candidateValue := original
			if tc.change != nil {
				tc.change(&candidateValue)
			}
			candidate := &candidateValue
			if tc.absent {
				candidate = nil
			} else if !process.ValidScopeIdentity(candidate, original.Unit) {
				t.Fatal("fixture must isolate physical anchor checks from legacy identity validity")
			}
			var persisted *process.ScopeIdentity
			if tc.original {
				persisted = &original
			}
			_, err := s.update(r.ID, func(next *Record) bool {
				next.ScopeIdentity = persisted
				// Supply only a synthetic diagnostic completion so finalize reaches
				// owner-identity handling without invoking an unstarted launcher.
				next.LaunchResult = &process.LaunchResult{
					Version: process.LaunchResultVersion, ExitCode: intPointer(0), FinishedAtMS: next.StartedAtMS + 1,
				}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			r = guardedJobsStoredRecord(t, s, r.ID)
			if err := process.WriteJSON(r.SpecPath+".started", process.Started{ScopeIdentity: candidate}); err != nil {
				t.Fatal(err)
			}
			started, ok := s.started(r)
			if !ok || (started.ScopeIdentity != nil) != tc.accept {
				t.Fatal("started did not enforce the original guarded kernel anchor")
			}
			if tc.accept && !reflect.DeepEqual(started.ScopeIdentity, candidate) {
				t.Fatal("started changed an accepted complete or legacy identity")
			}
			if !reflect.DeepEqual(guardedJobsStoredRecord(t, s, r.ID).ScopeIdentity, persisted) {
				t.Fatal("reading a started receipt replaced the persisted owner")
			}
			result := &process.Result{
				Version: 2, ExitCode: intPointer(0), FinishedAtMS: r.StartedAtMS + 1,
				ScopeIdentity: candidate, Quiescent: true,
			}
			if err := s.finalize(r.ID, result, ""); err != nil {
				t.Fatal(err)
			}
			want := persisted
			if tc.accept {
				want = candidate
			}
			got := guardedJobsStoredRecord(t, s, r.ID)
			if got.Status != "succeeded" || got.Quiescent || !reflect.DeepEqual(got.ScopeIdentity, want) {
				t.Fatal("finalize replaced the original guarded anchor or skipped the synthetic result")
			}
		})
	}
}
