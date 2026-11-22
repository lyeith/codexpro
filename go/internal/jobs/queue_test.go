package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func queueFixture(t *testing.T) (*Service, *config.Config, testResolver) {
	t.Helper()
	t.Setenv("CODEXPRO_JOB_SCOPES", "0")
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(base, "state"))
	cfg.Set("maxJobs", 2)
	cfg.Set("maxJobsPerWorkspace", 1)
	cfg.Set("jobTimeoutMs", 5000)
	resolver := testResolver{wire.Workspace{ID: "test-workspace", ProjectID: "test", Root: root}}
	s, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	s.backgroundActive.Store(true)
	t.Cleanup(func() {
		if !s.closed.Load() {
			_ = s.Close()
		}
	})
	return s, cfg, resolver
}
func queueOwner(agent string) wire.Scope {
	return wire.Scope{Agent: agent, Identity: wire.Identity{Source: "named", CredentialID: "fleet", PrincipalID: "fleet", AgentID: agent, AgentKind: "leaf"}}
}
func queueOption(resolver testResolver, scope wire.Scope, command string) startOptions {
	return startOptions{Workspace: resolver.workspace, Scope: scope, CwdAbs: resolver.workspace.Root, Cwd: ".", Command: command, Origin: "background", TimeoutMS: 5000, OutputLimit: 8192}
}
func enqueueTest(t *testing.T, s *Service, o []startOptions, key string) []Record {
	t.Helper()
	payload := []any{}
	for _, x := range o {
		payload = append(payload, []any{x.Command, x.Cwd, x.InputJobIDs})
	}
	r, err := s.enqueue(o, key, 3600000, payload)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func occupy(t *testing.T, s *Service, o startOptions) Record {
	t.Helper()
	r, _, err := s.admit(o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func completeAdmission(t *testing.T, s *Service, r Record) {
	t.Helper()
	code := 0
	if err := s.finalize(r.ID, &process.Result{ExitCode: &code, Quiescent: true}, ""); err != nil {
		t.Fatal(err)
	}
}
func errorCode(err error) string {
	var e *wire.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestCapacityQueuePreservesRejectAndCallerScopedRetry(t *testing.T) {
	s, _, r := queueFixture(t)
	o := queueOption(r, queueOwner("sp1"), "printf held")
	occupy(t, s, o)
	if _, _, err := s.admit(queueOption(r, queueOwner("sp5"), "printf newcomer")); errorCode(err) != "job_limit_reached" {
		t.Fatalf("old rejection changed: %v", err)
	}
	o = queueOption(r, queueOwner("sp5"), "printf queued")
	first := enqueueTest(t, s, []startOptions{o}, "stable")
	again := enqueueTest(t, s, []startOptions{o}, "stable")
	if first[0].ID != again[0].ID {
		t.Fatal("duplicate queue request allocated another ID")
	}
	o.Command = "printf changed"
	_, err := s.enqueue([]startOptions{o}, "stable", 3600000, []any{"different"})
	if errorCode(err) != "job_submission_conflict" {
		t.Fatalf("changed payload reused key: %v", err)
	}
	o.Scope = queueOwner("sp7")
	other := enqueueTest(t, s, []startOptions{o}, "stable")
	if other[0].ID == first[0].ID {
		t.Fatal("named-agent identity leaked retry")
	}
	if _, err = s.lookup(first[0].ID, r.workspace.ID, &o.Scope.Identity); errorCode(err) != "job_not_found" {
		t.Fatalf("cross-agent receipt admitted: %v", err)
	}
	if _, err = s.Stop(first[0].ID, r.workspace.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	o = queueOption(r, queueOwner("sp5"), "printf queued")
	done := enqueueTest(t, s, []startOptions{o}, "stable")
	if done[0].ID != first[0].ID || done[0].Status != "stopped" {
		t.Fatal("terminal retry replayed command")
	}
}

func TestCapacityQueueHasNoProcessResourcesAndStopSettlesWithoutLaunch(t *testing.T) {
	s, _, r := queueFixture(t)
	occupy(t, s, queueOption(r, queueOwner("sp1"), "held"))
	o := queueOption(r, queueOwner("sp5"), "touch must-not-exist")
	queued := enqueueTest(t, s, []startOptions{o}, "no-effect")[0]
	if queued.PID != 0 || queued.ScopeUnit != "" || queued.ResourceClaim != nil || queued.DeadlineMS != 0 {
		t.Fatalf("queued request allocated execution: %+v", queued)
	}
	for _, path := range []string{queued.SpecPath, queued.SpecPath + ".started", queued.GrantPath, queued.ControlPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("queued execution file exists: %s: %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.wait(ctx, []string{queued.ID}, "all", time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Call(ctx, o.Scope, "jobs", map[string]any{"workspace_id": r.workspace.ID, "job_ids": []string{queued.ID}, "output": "none", "wait_ms": 0})
	if err != nil {
		t.Fatal(err)
	}
	data := resultData(t, result)
	if data["all_finished"] != false || data["queued_count"] != float64(1) || data["running_count"] != float64(0) {
		t.Fatalf("queued reported finished/running: %#v", data)
	}
	result, err = s.Call(ctx, o.Scope, "stop_jobs", map[string]any{"workspace_id": r.workspace.ID, "job_ids": []string{queued.ID}})
	if err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get(queued.ID, r.workspace.ID)
	if final.Status != "stopped" || !final.Quiescent || final.QuiescenceScope != "not_launched" || final.Signal != "" {
		t.Fatalf("wrong no-launch settlement: %+v", final)
	}
	if _, err = os.Stat(filepath.Join(r.workspace.Root, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatal("cancelled queue command executed")
	}
}

func TestCapacityQueueFIFOAndNewcomerCannotOvertake(t *testing.T) {
	s, _, r := queueFixture(t)
	held := occupy(t, s, queueOption(r, queueOwner("sp1"), "held"))
	a := enqueueTest(t, s, []startOptions{queueOption(r, queueOwner("sp5"), "oldest")}, "older")[0]
	b := enqueueTest(t, s, []startOptions{queueOption(r, queueOwner("sp2"), "newer")}, "newer")[0]
	completeAdmission(t, s, held)
	if _, _, err := s.admit(queueOption(r, queueOwner("sp1"), "replacement")); errorCode(err) != "job_limit_reached" {
		t.Fatalf("replacement overtook waiting owner: %v", err)
	}
	s.admission.Lock()
	next := s.nextQueued()
	s.admission.Unlock()
	if next == nil || next.ID != a.ID {
		t.Fatalf("oldest owner not selected: %+v", next)
	}
	_, _ = s.Stop(a.ID, r.workspace.ID, "stopped")
	s.admission.Lock()
	next = s.nextQueued()
	s.admission.Unlock()
	if next == nil || next.ID != b.ID {
		t.Fatal("cancellation did not advance FIFO")
	}
}

func TestCapacityQueueSkipsFullWorkspaceAndRespectsGlobalCap(t *testing.T) {
	s, _, r := queueFixture(t)
	held := occupy(t, s, queueOption(r, queueOwner("sp1"), "held"))
	_ = enqueueTest(t, s, []startOptions{queueOption(r, queueOwner("sp5"), "blocked")}, "blocked")
	other := queueOption(r, queueOwner("sp7"), "eligible")
	other.Workspace.ID = "other"
	want := enqueueTest(t, s, []startOptions{other}, "other")[0]
	s.admission.Lock()
	next := s.nextQueued()
	s.admission.Unlock()
	if next == nil || next.ID != want.ID {
		t.Fatal("full workspace blocked unrelated eligible owner")
	}
	// An ordinary running admission in the other workspace consumes the global slot.
	other.Command = "held-other"
	other.Origin = "foreground"
	two := occupy(t, s, other)
	_, _ = s.update(two.ID, func(r *Record) bool { r.Origin = "background"; return true })
	s.admission.Lock()
	next = s.nextQueued()
	s.admission.Unlock()
	if next != nil {
		t.Fatal("global execution cap exceeded")
	}
	completeAdmission(t, s, held)
	completeAdmission(t, s, two)
}

func TestCapacityQueueBatchAtomicityBoundsAndExpiry(t *testing.T) {
	s, _, r := queueFixture(t)
	o := queueOption(r, queueOwner("sp5"), "never")
	bad := o
	bad.InputJobIDs = []string{"job_missing"}
	if _, err := s.enqueue([]startOptions{o, bad}, "bad-batch", 1000, "payload"); err == nil {
		t.Fatal("missing input accepted")
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("part of invalid batch accepted")
	}
	for i := 0; i < 4; i++ {
		enqueueTest(t, s, []startOptions{o}, string(rune('a'+i)))
	}
	if _, err := s.enqueue([]startOptions{o}, "too-many", 1000, "payload"); errorCode(err) != "job_queue_full" {
		t.Fatalf("pending bound not enforced: %v", err)
	}
	for _, q := range s.Snapshot() {
		_, _ = s.update(q.ID, func(r *Record) bool { r.QueueDeadlineMS = time.Now().Add(-time.Second).UnixMilli(); return true })
	}
	s.dispatchQueued()
	for _, q := range s.Snapshot() {
		if q.Status != "timed_out" || q.PID != 0 || q.Signal != "" {
			t.Fatalf("expired command launched: %+v", q)
		}
	}
}

func TestCapacityQueueConcurrentRetryIsOneSubmission(t *testing.T) {
	s, _, r := queueFixture(t)
	o := queueOption(r, queueOwner("sp5"), "one")
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := s.enqueue([]startOptions{o}, "concurrent", 1000, "same")
			if err != nil {
				errs <- err
				return
			}
			ids <- rows[0].ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("concurrent retry duplicated ID")
		}
		first = id
	}
	if len(s.Snapshot()) != 1 {
		t.Fatal("concurrent retry allocated multiple jobs")
	}
}

func TestCapacityQueueRestartRetainsUnstartedIDsAndNeverRequeuesActive(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	o := queueOption(resolver, queueOwner("sp5"), "printf accepted-once")
	// Simulate the committed pre-launch transition.
	active := occupy(t, s, queueOption(resolver, queueOwner("sp1"), "active-before-restart"))
	q := enqueueTest(t, s, []startOptions{o}, "restart")[0]
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	fresh.backgroundActive.Store(true)
	got, _ := fresh.Get(q.ID, resolver.workspace.ID)
	old, _ := fresh.Get(active.ID, resolver.workspace.ID)
	if got.Status != "queued" || old.Status != "running" {
		t.Fatalf("restart rewrote lifecycle: %s %s", got.Status, old.Status)
	}
	again := enqueueTest(t, fresh, []startOptions{o}, "restart")
	if again[0].ID != q.ID {
		t.Fatal("restart retry allocated another job")
	}
}

func TestCapacityQueueActualDispatchUsesOriginalIDAndExecutesOnce(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobsPerWorkspace", 1)
	held := occupy(t, s, queueOption(resolver, queueOwner("sp1"), "held"))
	o := queueOption(resolver, queueOwner("sp5"), "printf 'once\\n' >> marker; printf ok")
	q := enqueueTest(t, s, []startOptions{o}, "native-once")[0]
	completeAdmission(t, s, held)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := s.wait(ctx, []string{q.ID}, "all", 5*time.Second, false); err != nil {
		t.Fatal(err)
	}
	done, _ := s.Get(q.ID, resolver.workspace.ID)
	if done.Status != "succeeded" || done.PID == 0 || done.DeadlineMS-done.StartedAtMS != done.TimeoutMS {
		t.Fatalf("wrong execution admission: %+v", done)
	}
	again := enqueueTest(t, s, []startOptions{o}, "native-once")
	if again[0].ID != q.ID {
		t.Fatal("finished retry replayed")
	}
	b, err := os.ReadFile(filepath.Join(resolver.workspace.Root, "marker"))
	if err != nil || string(b) != "once\n" {
		t.Fatalf("effect was replayed: %q %v", b, err)
	}
	if output, err := s.read(done, 100, false); err != nil || !strings.Contains(output.Stdout, "ok") {
		t.Fatalf("ordinary output lost: %+v %v", output, err)
	}
}
