package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func executionHoldTestScope() wire.Scope {
	scope := queueOwner("fp5")
	scope.PrincipalID = "original-principal"
	scope.SessionID = "original-session"
	scope.RequestID = "original-request"
	return scope
}

func executionHoldTestPayload(t *testing.T, s *Service, id string) []byte {
	t.Helper()
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func executionHoldTestMarker(t *testing.T, s *Service) []byte {
	t.Helper()
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM execution_hold WHERE id=1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func executionHoldTestWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for " + description)
		case <-ticker.C:
		}
	}
}

func executionHoldTestIDs(jobs []ExecutionJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestExecutionHoldSerializesAdmissionAndOriginalReferences(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("maxActiveCaptures", 64)
	scope := executionHoldTestScope()
	original, releaseOriginal, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseOriginal)

	type outcome struct {
		record  Record
		release func()
		err     error
	}
	const contenders = 24
	start, held := make(chan struct{}), make(chan struct{})
	results := make(chan outcome, contenders)
	type holdOutcome struct {
		status ExecutionHoldStatus
		err    error
	}
	holdResult := make(chan holdOutcome, 1)
	for i := 0; i < contenders; i++ {
		go func(i int) {
			<-start
			ctx, release, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			// A winning call deliberately enters jobs admission only after ingress
			// closes. Its private reference, not its request labels, authorizes this.
			<-held
			o := queueOption(resolver, scope, fmt.Sprintf("printf contender-%d", i))
			o.Origin, o.admission = "foreground", executionLeaseFrom(ctx)
			record, _, err := s.admit(o)
			results <- outcome{record: record, release: release, err: err}
		}(i)
	}
	go func() {
		<-start
		status, err := s.BeginExecutionHold("serialized-admission")
		holdResult <- holdOutcome{status, err}
		close(held)
	}()
	close(start)
	h := <-holdResult
	if h.err != nil || !h.status.AdmissionHeld || h.status.DispatchHeld {
		t.Fatalf("hold did not serialize with original references: %+v, %v", h.status, h.err)
	}
	records := []Record{}
	releases := []func(){}
	for i := 0; i < contenders; i++ {
		got := <-results
		if got.release == nil {
			if !IsExecutionHeld(got.err) || got.record.ID != "" {
				t.Fatalf("loser was not an explicit no-admission refusal: %+v", got)
			}
			continue
		}
		t.Cleanup(got.release)
		if got.err != nil || got.record.ID == "" {
			t.Fatalf("original accepted call lost its admission: %+v", got)
		}
		records, releases = append(records, got.record), append(releases, got.release)
	}
	o := queueOption(resolver, scope, "printf guaranteed-original")
	o.Origin, o.admission = "foreground", executionLeaseFrom(original)
	r, _, err := s.admit(o)
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, r)

	if _, release, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID); !IsExecutionHeld(err) || release != nil {
		t.Fatalf("fresh same-identity call acquired execution: %v", err)
	}
	fresh := queueOption(resolver, scope, "printf refused")
	if _, _, err := s.admit(fresh); !IsExecutionHeld(err) {
		t.Fatalf("direct fresh jobs admission escaped hold: %v", err)
	}
	if _, err := s.enqueue([]startOptions{fresh}, "refused-key", 3600000, []any{"refused"}); !IsExecutionHeld(err) {
		t.Fatalf("fresh queue admission escaped hold: %v", err)
	}
	if got := s.Snapshot(); len(got) != len(records) {
		t.Fatalf("refused contenders created rows: got %d, accepted %d", len(got), len(records))
	}
	for _, record := range records {
		if record.PID != 0 || record.ResourceClaim != nil || record.ScopeUnit != "" {
			t.Fatalf("unlaunched fixture unexpectedly allocated an owner: %+v", record)
		}
		if _, err := os.Stat(record.SpecPath); !os.IsNotExist(err) {
			t.Fatalf("unlaunched/refused admission wrote a runner spec: %v", err)
		}
	}
	for _, release := range releases {
		release()
		release() // Release is idempotent and cannot consume another call's ref.
	}
	releaseOriginal()
	status, err := s.HoldExecutionDispatch(h.status.HoldID)
	if err != nil || status.DispatchHeld || len(status.OutstandingJobs) != len(records) {
		t.Fatalf("returned calls hid their accepted prepared rows: %+v, %v", status, err)
	}
	for _, record := range records {
		s.entry(record.ID).launching.Store(false)
		completeAdmission(t, s, record)
	}
	status, err = s.HoldExecutionDispatch(h.status.HoldID)
	if err != nil || !status.DispatchHeld || len(status.PendingCalls) != 0 || len(status.OutstandingJobs) != 0 {
		t.Fatalf("original records did not settle the barrier: %+v, %v", status, err)
	}
}

func TestExecutionHoldCapabilityAndOpaqueTokenLifetime(t *testing.T) {
	s, _, resolver := queueFixture(t)
	foreign, _, _ := queueFixture(t)
	scope := executionHoldTestScope()
	parent, releaseParent, err := s.AcquireExecution(context.Background(), scope, "batch", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseParent)
	child, releaseChild, err := s.RetainExecution(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseChild)
	status, err := s.BeginExecutionHold("original-maintenance-key")
	if err != nil || len(status.PendingCalls) != 1 || status.PendingCalls[0].References != 2 {
		t.Fatalf("child did not retain the original admission: %+v, %v", status, err)
	}
	repeated, err := s.BeginExecutionHold("original-maintenance-key")
	if err != nil || repeated.HoldID != status.HoldID || repeated.RequestedAtMS != status.RequestedAtMS {
		t.Fatalf("same-key begin replaced the original hold: %+v, %v", repeated, err)
	}
	if _, err := s.BeginExecutionHold("foreign-maintenance-key"); errorCode(err) != "execution_hold_conflict" {
		t.Fatalf("foreign key replaced the hold: %v", err)
	}
	for _, call := range []func(string) (ExecutionHoldStatus, error){s.HoldExecutionDispatch, s.ResumeExecution} {
		if _, err := call(strings.Repeat("f", 32)); errorCode(err) != "execution_hold_conflict" {
			t.Fatalf("foreign token was accepted: %v", err)
		}
	}
	if _, err := s.ResumeExecution(status.HoldID); errorCode(err) != "execution_hold_incomplete" {
		t.Fatalf("resume ignored retained child: %v", err)
	}
	releaseParent()
	if _, release, err := s.RetainExecution(parent); errorCode(err) != "execution_admission_invalid" || release != nil {
		t.Fatalf("released parent remained a capability: %v", err)
	}
	if _, release, err := foreign.AcquireExecution(child, scope, "bash", resolver.workspace.ID); errorCode(err) != "execution_admission_invalid" || release != nil {
		t.Fatalf("foreign service adopted an original capability: %v", err)
	}
	if _, release, err := s.RetainExecution(context.Background()); errorCode(err) != "execution_admission_invalid" || release != nil {
		t.Fatalf("retain minted a fresh admission: %v", err)
	}
	_, releaseGrandchild, err := s.AcquireExecution(child, scope, "write", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseGrandchild)
	releaseChild()
	releaseChild()
	if _, release, err := s.AcquireExecution(child, scope, "write", resolver.workspace.ID); errorCode(err) != "execution_admission_invalid" || release != nil {
		t.Fatalf("released child was revived using its live root: %v", err)
	}
	if got := s.ExecutionStatus(); got.DispatchHeld || len(got.PendingCalls) != 1 || got.PendingCalls[0].References != 1 {
		t.Fatalf("idempotent release lost the grandchild: %+v", got)
	}
	releaseGrandchild()
	held, err := s.HoldExecutionDispatch(status.HoldID)
	if err != nil || !held.DispatchHeld || len(held.PendingCalls) != 0 {
		t.Fatalf("last original reference did not finish: %+v, %v", held, err)
	}
	if resumed, err := s.ResumeExecution(status.HoldID); err != nil || resumed.AdmissionHeld || resumed.DispatchHeld {
		t.Fatalf("exact-token resume failed: %+v, %v", resumed, err)
	}
	if _, err := s.ResumeExecution(status.HoldID); errorCode(err) != "execution_hold_conflict" {
		t.Fatalf("consumed token remained usable: %v", err)
	}
	if _, release, err := s.AcquireExecution(parent, scope, "bash", resolver.workspace.ID); errorCode(err) != "execution_admission_invalid" || release != nil {
		t.Fatalf("resume revived a stale original capability: %v", err)
	}
	_, releaseFresh, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	releaseFresh()
	next, err := s.BeginExecutionHold("next-maintenance-key")
	if err != nil || next.HoldID == status.HoldID {
		t.Fatalf("next hold reused the consumed token: %+v, %v", next, err)
	}
}

func executionHoldTestIdentity(t *testing.T, before, after Record) {
	t.Helper()
	if before.ID != after.ID || before.WorkspaceID != after.WorkspaceID || before.Root != after.Root ||
		before.Command != after.Command || before.Cwd != after.Cwd || before.Agent != after.Agent ||
		!reflect.DeepEqual(before.Identity, after.Identity) || before.OwnerPrincipalID != after.OwnerPrincipalID ||
		before.OwnerSessionID != after.OwnerSessionID || before.RequestID != after.RequestID ||
		before.TimeoutMS != after.TimeoutMS || before.QueueDeadlineMS != after.QueueDeadlineMS ||
		before.StdoutPath != after.StdoutPath || before.StderrPath != after.StderrPath ||
		before.SpecPath != after.SpecPath || before.GrantPath != after.GrantPath ||
		before.ControlPath != after.ControlPath || before.ResultPath != after.ResultPath {
		t.Fatalf("original accepted identity/deadline/paths changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestExecutionHoldOriginalPreparedAndQueuedWorkFinishesOnceWithRetainedLogs(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobsPerWorkspace", 1)
	scope := executionHoldTestScope()
	ctx, release, err := s.AcquireExecution(context.Background(), scope, "batch", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	hold, err := s.BeginExecutionHold("original-work")
	if err != nil {
		t.Fatal(err)
	}
	preparedOption := queueOption(resolver, scope, "printf 'prepared\\n' >> once; printf prepared-output")
	preparedOption.admission = executionLeaseFrom(ctx)
	prepared := occupy(t, s, preparedOption)
	queuedOption := queueOption(resolver, scope, "printf 'queued\\n' >> once; printf queued-output")
	queuedOption.admission = executionLeaseFrom(ctx)
	queued := enqueueTest(t, s, []startOptions{queuedOption}, "original-queued-key")[0]
	grant := []byte("original retained grant\n")
	if err := os.WriteFile(queued.GrantPath, grant, 0600); err != nil {
		t.Fatal(err)
	}
	// Compare the same published representation before and after holding dispatch.
	originals := make([]Record, 0, 2)
	for _, accepted := range []Record{prepared, queued} {
		snapshot, ok := s.Get(accepted.ID, "")
		if !ok {
			t.Fatalf("accepted original was not published: %s", accepted.ID)
		}
		originals = append(originals, snapshot)
	}
	release()
	check, err := s.HoldExecutionDispatch(hold.HoldID)
	if err != nil || check.DispatchHeld || len(check.AcceptedJobs) != 2 {
		t.Fatalf("accepted rows after hold were not retained: %+v, %v", check, err)
	}
	for _, before := range originals {
		after, _ := s.Get(before.ID, "")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("hold changed the unlaunched original: before %+v, after %+v", before, after)
		}
	}
	if _, err := s.launch(context.Background(), preparedOption, prepared); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := s.wait(waitCtx, []string{prepared.ID, queued.ID}, "all", 5*time.Second, false); err != nil {
		t.Fatal(err)
	}
	for _, before := range []Record{prepared, queued} {
		after, _ := s.Get(before.ID, "")
		executionHoldTestIdentity(t, before, after)
		if after.Status != "succeeded" || after.ExitCode == nil || *after.ExitCode != 0 || !after.Quiescent {
			t.Fatalf("original work did not finish: %+v", after)
		}
		if before.ID == prepared.ID && (after.DeadlineMS != before.DeadlineMS || after.StartedAtMS != before.StartedAtMS) {
			t.Fatalf("prepared work received a renewed deadline: before %+v, after %+v", before, after)
		}
		if before.ID == queued.ID && after.DeadlineMS-after.StartedAtMS != before.TimeoutMS {
			t.Fatalf("queued execution changed its accepted timeout: %+v", after)
		}
		if output, err := s.read(after, 100, false); err != nil || !strings.Contains(output.Stdout, "-output") {
			t.Fatalf("original output unavailable: %+v, %v", output, err)
		}
	}
	freshRetry := queuedOption
	freshRetry.admission = nil
	if again := enqueueTest(t, s, []startOptions{freshRetry}, "original-queued-key"); len(again) != 1 || again[0].ID != queued.ID {
		t.Fatalf("held same-key receipt replayed the original: %+v", again)
	}
	if body, err := os.ReadFile(filepath.Join(resolver.workspace.Root, "once")); err != nil || string(body) != "prepared\nqueued\n" {
		t.Fatalf("original prepared/queued work did not execute exactly once: %q, %v", body, err)
	}
	if body, err := os.ReadFile(queued.GrantPath); err != nil || !bytes.Equal(body, grant) {
		t.Fatalf("hold replaced or removed the accepted grant: %q, %v", body, err)
	}
	var status ExecutionHoldStatus
	executionHoldTestWait(t, "original launchers to exit before holding dispatch", func() bool {
		status, err = s.HoldExecutionDispatch(hold.HoldID)
		if err != nil {
			t.Fatalf("original work could not advance dispatch: %v", err)
		}
		return status.DispatchHeld && len(status.OutstandingJobs) == 0
	})
	wantIDs := []string{prepared.ID, queued.ID}
	sort.Strings(wantIDs)
	if got := executionHoldTestIDs(status.AcceptedJobs); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("held cohort lost original IDs: got %v, want %v", got, wantIDs)
	}
	var saved executionHoldRecord
	if err := json.Unmarshal(executionHoldTestMarker(t, s), &saved); err != nil || !reflect.DeepEqual(saved.JobIDs, wantIDs) {
		t.Fatalf("accepted cohort was not durable: %+v, %v", saved, err)
	}
	cfg.Set("maxRetainedJobBytes", 0)
	cfg.Set("maxJobHistoryPerWorkspace", 0)
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	for _, id := range wantIDs {
		record, _ := s.Get(id, "")
		if record.OutputExpired {
			t.Fatalf("active hold lost original logs: %+v", record)
		}
		if output, err := s.read(record, 100, false); err != nil || !strings.Contains(output.Stdout, "-output") {
			t.Fatalf("retained original output was pruned: %+v, %v", output, err)
		}
	}
	if _, err := s.ResumeExecution(hold.HoldID); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	for _, id := range wantIDs {
		record, _ := s.Get(id, "")
		if !record.OutputExpired {
			t.Fatalf("resume did not release ordinary retention pins: %+v", record)
		}
	}
}

func TestExecutionHoldPersistenceFailureKeepsIngressClosed(t *testing.T) {
	s, _, resolver := queueFixture(t)
	if _, err := s.db.Exec("CREATE TRIGGER reject_hold_insert BEFORE INSERT ON execution_hold BEGIN SELECT RAISE(ABORT, 'fixture hold write failure'); END"); err != nil {
		t.Fatal(err)
	}
	status, err := s.BeginExecutionHold("persistence-failure")
	if err == nil || !status.AdmissionHeld || status.PersistenceError == "" {
		t.Fatalf("persistence failure reopened ingress or claimed success: %+v, %v", status, err)
	}
	scope := executionHoldTestScope()
	if _, release, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID); !IsExecutionHeld(err) || release != nil {
		t.Fatalf("failed durable hold allowed a fresh call: %v", err)
	}
	o := queueOption(resolver, scope, "printf forbidden")
	if _, _, err := s.admit(o); !IsExecutionHeld(err) {
		t.Fatalf("failed hold admitted a new row: %v", err)
	}
	if _, err := s.enqueue([]startOptions{o}, "forbidden", 3600000, []any{"forbidden"}); !IsExecutionHeld(err) {
		t.Fatalf("failed hold queued new work: %v", err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("persistence failure/refusal created jobs")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_hold_insert"); err != nil {
		t.Fatal(err)
	}
	retried, err := s.BeginExecutionHold("persistence-failure")
	if err != nil || retried.HoldID != status.HoldID || !retried.DispatchHeld || retried.PersistenceError != "" {
		t.Fatalf("retry did not persist the same held identity: %+v, %v", retried, err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER reject_hold_delete BEFORE DELETE ON execution_hold BEGIN SELECT RAISE(ABORT, 'fixture hold delete failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeExecution(status.HoldID); err == nil || !s.ExecutionStatus().AdmissionHeld {
		t.Fatalf("failed marker removal reopened ingress: %v", err)
	}
	if _, release, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID); !IsExecutionHeld(err) || release != nil {
		t.Fatalf("failed resume allowed execution: %v", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_hold_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeExecution(status.HoldID); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionHoldOriginalAdmissionAndCohortCommitAtomically(t *testing.T) {
	s, _, resolver := queueFixture(t)
	scope := executionHoldTestScope()
	ctx, release, err := s.AcquireExecution(context.Background(), scope, "bash", resolver.workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	hold, err := s.BeginExecutionHold("cohort-transaction")
	if err != nil {
		t.Fatal(err)
	}
	before := executionHoldTestMarker(t, s)
	if _, err := s.db.Exec("CREATE TRIGGER reject_hold_update BEFORE UPDATE ON execution_hold BEGIN SELECT RAISE(ABORT, 'fixture cohort write failure'); END"); err != nil {
		t.Fatal(err)
	}
	o := queueOption(resolver, scope, "printf original")
	o.admission = executionLeaseFrom(ctx)
	if _, _, err := s.admit(o); err == nil {
		t.Fatal("job admission ignored its failed cohort persistence")
	}
	var rows int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || len(s.Snapshot()) != 0 || len(s.ExecutionStatus().AcceptedJobs) != 0 ||
		!bytes.Equal(before, executionHoldTestMarker(t, s)) {
		t.Fatal("failed admission published a row or partially changed the durable cohort")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_hold_update"); err != nil {
		t.Fatal(err)
	}
	record := occupy(t, s, o)
	release()
	status, err := s.HoldExecutionDispatch(hold.HoldID)
	if err != nil || status.DispatchHeld || len(status.AcceptedJobs) != 1 || status.AcceptedJobs[0].ID != record.ID {
		t.Fatalf("original admitted row was not committed with its cohort: %+v, %v", status, err)
	}
}

func TestExecutionHoldTerminalAndUnknownRowsRemainOwnerBlockers(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		quiet        bool
	}{
		{"terminal_unquiescent", "failed", false},
		{"unsupported_status", "future_status", true},
		{"empty_status", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, resolver := queueFixture(t)
			scope := executionHoldTestScope()
			record := occupy(t, s, queueOption(resolver, scope, "original"))
			hold, err := s.BeginExecutionHold(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			s.entry(record.ID).launching.Store(false)
			record, err = s.update(record.ID, func(r *Record) bool {
				r.Status, r.LaunchState = tc.status, "launched"
				r.Quiescent, r.QuiescenceScope = tc.quiet, "process_group"
				r.ExitCode = intPointer(7)
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			status, err := s.HoldExecutionDispatch(hold.HoldID)
			if err != nil || status.DispatchHeld || len(status.OutstandingJobs) != 1 {
				t.Fatalf("unverified/unsupported terminal row closed dispatch: %+v, %v", status, err)
			}
			before := executionHoldTestPayload(t, s, record.ID)
			observed, err := s.ObserveExecutionHold(context.Background())
			if err != nil || observed.Complete || len(observed.Owners) != 1 || observed.Owners[0].OwnerQuiescentVerified {
				t.Fatalf("terminal or unsupported status became owner proof: %+v, %v", observed, err)
			}
			if !bytes.Equal(before, executionHoldTestPayload(t, s, record.ID)) {
				t.Fatal("read-only observation changed the original job")
			}
			if _, err := s.ResumeExecution(hold.HoldID); errorCode(err) != "execution_hold_incomplete" {
				t.Fatalf("unverified original allowed resume: %v", err)
			}
			if tc.status == "failed" {
				result, err := s.Call(context.Background(), scope, "jobs", map[string]any{
					"workspace_id": resolver.workspace.ID, "job_ids": []string{record.ID}, "output": "none", "wait_ms": 0,
				})
				if err != nil {
					t.Fatal(err)
				}
				data := resultData(t, result)
				if data["all_finished"] != true || data["all_quiescent"] != false {
					t.Fatalf("public observation collapsed completion and owner state: %#v", data)
				}
			}
		})
	}
}

func TestExecutionHoldObservationUsesSavedReadOnlyOwnerAndIndependentFacts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resource owner inspection requires Linux")
	}
	// These original owner facts also apply to accepted legacy jobs. A v1
	// launcher needs its own identity even when every SSD owner fact is true.
	for _, tc := range []struct {
		name, receiptStatus, allocation  string
		published, alive, quiet, durable bool
		launcherVersion                  int
		launcherState                    string
		launcherQuiet                    bool
	}{
		{"terminal_owner_alive", "failed", "active", false, true, false, false, 0, "legacy", true},
		{"quiet_without_durable_receipt", "running", "settled", true, false, true, false, 0, "legacy", true},
		{"durable_owner_without_jobs_publication", "failed", "settled", false, false, true, true, 0, "legacy", true},
		{"both_published_and_verified", "failed", "settled", true, false, true, true, 0, "legacy", true},
		{"verified_owner_without_launcher_identity", "failed", "settled", true, false, true, true, process.LaunchSpecVersion, "unclaimed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, resolver := queueFixture(t)
			scope := executionHoldTestScope()
			record := occupy(t, s, queueOption(resolver, scope, "original"))
			unit := "codexpro-" + record.ID + "-" + strings.Repeat("4", 32) + ".scope"
			boot, invocation := "12345678-1234-1234-1234-123456789abc", strings.Repeat("3", 32)
			group := "/user.slice/user-1000.slice/user@1000.service/app.slice/" + unit
			helper := filepath.Join(t.TempDir(), "saved-owner")
			claim := process.ResourceClaim{Version: 1, Helper: helper, RunID: strings.Repeat("1", 32), RegistrationID: strings.Repeat("2", 32)}
			var receiptExit any
			if tc.receiptStatus == "failed" {
				receiptExit = 7
			}
			envelope := map[string]any{
				"receipt": map[string]any{
					"run_id": claim.RunID, "registration_id": claim.RegistrationID,
					"boot_id": boot, "invocation_id": invocation, "status": tc.receiptStatus,
					"exit_code": receiptExit, "owner_quiescent_verified": tc.durable,
					"owner": map[string]any{
						"kind": "unit", "unit": unit, "invocation_id": invocation,
						"cgroup": map[string]any{"path": "/sys/fs/cgroup" + group, "device": 29, "inode": 12345, "boot": boot},
					},
				},
				"alive": tc.alive, "pending": false, "quiescent": tc.quiet, "allocation_claim": tc.allocation,
				"unit_state": map[string]any{},
			}
			body, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(helper+".response", body, 0600); err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\numask 077\nprintf '%s\\n' \"$@\" >> \"$0.args\"\nexec /bin/cat \"$0.response\"\n"
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			hold, err := s.BeginExecutionHold(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			s.entry(record.ID).launching.Store(false)
			record, err = s.update(record.ID, func(r *Record) bool {
				r.Status, r.LaunchState = "failed", "launched"
				r.LauncherVersion = tc.launcherVersion
				r.ExitCode, r.Quiescent, r.QuiescenceScope = intPointer(7), tc.published, "systemd_scope"
				r.ScopeUnit, r.ResourceClaim = strings.TrimSuffix(unit, ".scope"), &claim
				r.ScopeIdentity = &process.ScopeIdentity{Version: 1, Unit: unit, BootID: boot, InvocationID: invocation, Cgroup: group}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.HoldExecutionDispatch(hold.HoldID); err != nil {
				t.Fatal(err)
			}
			rowBefore, markerBefore := executionHoldTestPayload(t, s, record.ID), executionHoldTestMarker(t, s)
			observed, err := s.ObserveExecutionHold(context.Background())
			if err != nil || len(observed.Owners) != 1 {
				t.Fatalf("read-only owner observation failed: %+v, %v", observed, err)
			}
			owner := observed.Owners[0]
			if owner.Error != "" || !owner.CommandTerminal || owner.CommandStatus != "failed" ||
				owner.PublishedQuiescent != tc.published || owner.Quiescent != tc.quiet ||
				owner.LauncherState != tc.launcherState || owner.LauncherQuiescent != tc.launcherQuiet ||
				owner.OwnerQuiescentVerified != tc.durable || owner.Resource == nil ||
				owner.Resource.ReceiptStatus != tc.receiptStatus || owner.Resource.Alive != tc.alive ||
				owner.Resource.RunID != claim.RunID || owner.Resource.RegistrationID != claim.RegistrationID ||
				observed.Complete != (tc.published && tc.quiet && tc.durable && tc.launcherQuiet) {
				t.Fatalf("command, launcher, durable publication, and fresh owner facts collapsed: %+v", observed)
			}
			if !bytes.Equal(rowBefore, executionHoldTestPayload(t, s, record.ID)) ||
				!bytes.Equal(markerBefore, executionHoldTestMarker(t, s)) {
				t.Fatal("owner inspection mutated an original row or hold marker")
			}
			if args, err := os.ReadFile(helper + ".args"); err != nil || string(args) != "inspect\n"+claim.RunID+"\n" {
				t.Fatalf("owner observation invoked an allocating/settling command: %q, %v", args, err)
			}
		})
	}
}

func TestExecutionHoldObservationRejectsMismatchedScopeWithoutInspection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd scope identity requires Linux")
	}
	s, _, resolver := queueFixture(t)
	boot, err := process.BootID()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nprintf called > \"$0.called\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// The marker is the helper's own path; no real systemd command is permitted.
	marker := filepath.Join(bin, "systemctl.called")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	record := occupy(t, s, queueOption(resolver, executionHoldTestScope(), "original"))
	hold, err := s.BeginExecutionHold("mismatched-owner-unit")
	if err != nil {
		t.Fatal(err)
	}
	unitA := "codexpro-" + record.ID + "-" + strings.Repeat("a", 32)
	unitB := "codexpro-" + record.ID + "-" + strings.Repeat("b", 32) + ".scope"
	s.entry(record.ID).launching.Store(false)
	record, err = s.update(record.ID, func(r *Record) bool {
		r.Status, r.LaunchState, r.Quiescent = "succeeded", "launched", true
		r.ScopeUnit, r.QuiescenceScope = unitA, "systemd_scope"
		r.ScopeIdentity = &process.ScopeIdentity{Version: 1, Unit: unitB, BootID: boot,
			InvocationID: strings.Repeat("c", 32), Cgroup: "/user.slice/" + unitB}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !process.ValidScopeIdentity(record.ScopeIdentity, unitB) {
		t.Fatal("fixture must be a valid same-boot identity for a different unit")
	}
	if _, err := s.HoldExecutionDispatch(hold.HoldID); err != nil {
		t.Fatal(err)
	}
	before := executionHoldTestPayload(t, s, record.ID)
	observed, err := s.ObserveExecutionHold(context.Background())
	if err != nil || observed.Complete || len(observed.Owners) != 1 ||
		observed.Owners[0].Error == "" || observed.Owners[0].Quiescent || observed.Owners[0].OwnerQuiescentVerified {
		t.Fatalf("different valid unit became original owner proof: %+v, %v", observed, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("mismatched owner reached systemctl inspection: %v", err)
	}
	if !bytes.Equal(before, executionHoldTestPayload(t, s, record.ID)) {
		t.Fatal("mismatched-owner observation changed the accepted job")
	}
}
