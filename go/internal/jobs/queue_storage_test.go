package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapacityQueueRetryFindsOriginalReceiptAfterCwdDisappears(t *testing.T) {
	s, _, resolver := queueFixture(t)
	scope := queueOwner("sp5")
	child := filepath.Join(resolver.workspace.Root, "removed")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"workspace_id": resolver.workspace.ID, "commands": []map[string]any{{"command": "printf once", "cwd": "removed"}}, "queue_key": "lost-response"}
	first, err := s.Call(context.Background(), scope, "queue_jobs", args)
	if err != nil {
		t.Fatal(err)
	}
	id := resultData(t, first)["job_ids"].([]any)[0].(string)
	if err = os.Remove(child); err != nil {
		t.Fatal(err)
	}
	again, err := s.Call(context.Background(), scope, "queue_jobs", args)
	if err != nil {
		t.Fatal("original receipt depended on changed working path:", err)
	}
	if resultData(t, again)["job_ids"].([]any)[0] != id {
		t.Fatal("changed path caused a new submission")
	}
	s.dispatchQueued()
	r, _ := s.Get(id, resolver.workspace.ID)
	if r.Status != "failed" || r.StopReason != "queued_workspace_changed" || r.PID != 0 {
		t.Fatalf("changed queued path did not fail before launch: %+v", r)
	}
}

func TestCapacityQueueKeepsInputPinsUntilCancellation(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	o := queueOption(resolver, queueOwner("sp5"), "retained-output")
	source := occupy(t, s, o)
	dir := s.outputDir(source)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte("retained fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	completeAdmission(t, s, source)
	_, _ = s.update(source.ID, func(r *Record) bool { r.FinishedAtMS = time.Now().Add(-time.Hour).UnixMilli(); return true })
	cfg.Set("jobRetentionMs", 1)
	o.Command = "inspect pinned input"
	o.InputJobIDs = []string{source.ID}
	q := enqueueTest(t, s, []startOptions{o}, "pinned")[0]
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(source.ID, resolver.workspace.ID)
	if r.OutputExpired {
		t.Fatal("queued consumer lost its retained input")
	}
	if _, err := s.Stop(q.ID, resolver.workspace.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(source.ID, resolver.workspace.ID)
	if !r.OutputExpired {
		t.Fatal("cancelled consumer indefinitely retained its input")
	}
}

func TestCapacityQueueDoesNotAdoptOrReplayUncertainExecution(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	o := queueOption(resolver, queueOwner("sp1"), "touch must-not-replay")
	prior := occupy(t, s, o)
	_, err := s.update(prior.ID, func(r *Record) bool {
		r.Status = "failed"
		r.Quiescent = false
		r.ScopeUnit = "unsettled-original"
		r.LaunchState = "launched"
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set("maxActiveCaptures", 1)
	q := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("sp5"), "printf waiting")}, "blocked-on-unknown")[0]
	s.dispatchQueued()
	old, _ := s.Get(prior.ID, resolver.workspace.ID)
	waiting, _ := s.Get(q.ID, resolver.workspace.ID)
	if old.Status != "failed" || old.LaunchState != "launched" || waiting.Status != "queued" {
		t.Fatalf("uncertain ownership was replayed or bypassed: %+v %+v", old, waiting)
	}
	if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "must-not-replay")); !os.IsNotExist(err) {
		t.Fatal("prior effect was replayed")
	}
}

func TestCapacityQueueDeadlineCanSettleWithoutHostAdmissionCapability(t *testing.T) {
	s, _, resolver := queueFixture(t)
	q := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("sp5"), "never")}, "not-ready")[0]
	_, _ = s.update(q.ID, func(r *Record) bool { r.QueueDeadlineMS = time.Now().Add(-time.Second).UnixMilli(); return true })
	s.backgroundActive.Store(false)
	s.dispatchQueued()
	result, _ := s.Get(q.ID, resolver.workspace.ID)
	if result.Status != "timed_out" || !result.Quiescent || result.PID != 0 {
		t.Fatalf("host readiness stranded a bounded capacity wait: %+v", result)
	}
}

func TestCapacityQueueReservesFuturePinnedOutput(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	o := queueOption(resolver, queueOwner("sp5"), "future-output")
	o.OutputLimit = 1000000
	first := enqueueTest(t, s, []startOptions{o}, "future-source")[0]
	cfg.Set("maxRetainedJobBytes", 900000)
	o.Command = "inspect-future-output"
	o.InputJobIDs = []string{first.ID}
	_, err := s.enqueue([]startOptions{o}, "future-reader", 1000, "payload")
	if errorCode(err) != "job_storage_limit" {
		t.Fatalf("queued source bypassed future output reservation: %v", err)
	}
	if len(s.Snapshot()) != 1 {
		t.Fatal("over-budget input consumer was partially accepted")
	}
}
