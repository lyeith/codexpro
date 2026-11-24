package jobs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func activityRecord(id, label, status string, at time.Time) Record {
	r := Record{ID: id, WorkspaceID: "test-workspace", Agent: label, Status: status,
		StartedAt: at.UTC().Format(time.RFC3339Nano), StartedAtMS: at.UnixMilli(), Origin: "background"}
	if !r.Unfinished() {
		r.FinishedAt = at.Add(time.Second).UTC().Format(time.RFC3339Nano)
		r.FinishedAtMS = at.Add(time.Second).UnixMilli()
		r.Quiescent = true
	}
	return r
}

func TestJobActivityCommittedTransitionsAndReplay(t *testing.T) {
	s, _, resolver := queueFixture(t)
	if got, known := s.AgentActivity("SP12"); !known || got != (ActivityCounts{}) {
		t.Fatalf("unseen valid label: %+v known=%v", got, known)
	}
	wake := s.ActivityChanged()
	option := queueOption(resolver, queueOwner("SP12"), "printf private")
	first := enqueueTest(t, s, []startOptions{option}, "activity-replay")
	got, known := s.AgentActivity("SP12")
	if !known || got.QueuedJobs != 1 || got.InFlightJobs != 0 || got.Revision != 1 || got.LastActivityAt.IsZero() {
		t.Fatalf("queued: %+v known=%v", got, known)
	}
	select {
	case <-wake:
	default:
		t.Fatal("committed admission did not wake collector")
	}
	enqueueTest(t, s, []startOptions{option}, "activity-replay")
	if again, _ := s.AgentActivity("SP12"); again != got {
		t.Fatalf("queue replay changed activity: before=%+v after=%+v", got, again)
	}
	if _, err := s.update(first[0].ID, func(r *Record) bool { r.Status = "running"; return true }); err != nil {
		t.Fatal(err)
	}
	running, known := s.AgentActivity("SP12")
	if !known || running.InFlightJobs != 1 || running.QueuedJobs != 0 || running.Revision != 2 {
		t.Fatalf("running: %+v known=%v", running, known)
	}
	wake = s.ActivityChanged()
	completeAdmission(t, s, first[0])
	finished, known := s.AgentActivity("SP12")
	if !known || finished.InFlightJobs != 0 || finished.QueuedJobs != 0 || finished.Revision != 3 ||
		finished.LastActivityAt.Before(running.LastActivityAt) {
		t.Fatalf("finished: %+v known=%v", finished, known)
	}
	select {
	case <-wake:
	default:
		t.Fatal("terminal commit did not wake collector")
	}
	if _, err := s.update(first[0].ID, func(r *Record) bool { r.Acknowledged = true; return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update(first[0].ID, func(*Record) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.AgentActivity("SP12"); again != finished {
		t.Fatalf("acknowledgment or no-op changed activity: %+v", again)
	}
}

func TestJobActivityDoesNotPublishFailedWrites(t *testing.T) {
	s, _, _ := queueFixture(t)
	r := activityRecord("job_activity_failure", "FP5", "queued", time.Now().Add(-time.Minute))
	if err := s.insertRecords([]Record{r}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.AgentActivity("FP5")
	wake := s.ActivityChanged()
	if err := s.insertRecords([]Record{r}); err == nil {
		t.Fatal("duplicate admission unexpectedly committed")
	}
	if _, err := s.db.Exec("CREATE TRIGGER refuse_activity BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'fixture'); END;"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update(r.ID, func(r *Record) bool { r.Status = "running"; return true }); err == nil {
		t.Fatal("injected update failure did not fail")
	}
	if got, known := s.AgentActivity("FP5"); !known || got != before {
		t.Fatalf("failed commit changed activity: %+v known=%v", got, known)
	}
	select {
	case <-wake:
		t.Fatal("failed commit emitted an activity change")
	default:
	}
	if _, err := s.db.Exec("DROP TRIGGER refuse_activity"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update(r.ID, func(r *Record) bool { r.Status = "running"; return true }); err != nil {
		t.Fatal(err)
	}
	if got, known := s.AgentActivity("FP5"); !known || got.Revision != before.Revision+1 ||
		got.QueuedJobs != 0 || got.InFlightJobs != 1 {
		t.Fatalf("successful retry: %+v known=%v", got, known)
	}
}

func TestJobActivityRecoveryInitializesWithoutNewRevisions(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	now := time.Now().Add(-time.Minute)
	rows := []Record{
		activityRecord("job_recovered_running", "SP12", "running", now),
		activityRecord("job_recovered_queued", "SP12", "queued", now.Add(time.Second)),
		activityRecord("job_recovered_finished", "SP12", "succeeded", now.Add(2*time.Second)),
		activityRecord("job_invalid_label", "toolong", "running", now),
	}
	bad := activityRecord("job_recovered_bad", "RDP2", "running", now)
	bad.StartedAt = "not-a-time"
	rows = append(rows, bad)
	if err := s.insertRecords(rows); err != nil {
		t.Fatal(err)
	}
	before, _ := s.AgentActivity("SP12")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	got, known := recovered.AgentActivity("SP12")
	before.Revision = 0
	if !known || got != before {
		t.Fatalf("recovered aggregate: %+v want=%+v known=%v", got, before, known)
	}
	if _, known := recovered.AgentActivity("RDP2"); known {
		t.Fatal("malformed recovered timestamp became known zero")
	}
	if _, known := recovered.AgentActivity("toolong"); known {
		t.Fatal("invalid label accepted")
	}
	if _, err := recovered.update(bad.ID, func(r *Record) bool {
		r.StartedAt = now.UTC().Format(time.RFC3339Nano)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if got, known := recovered.AgentActivity("RDP2"); !known || got.InFlightJobs != 1 || got.Revision != 1 {
		t.Fatalf("repaired source did not recover: %+v known=%v", got, known)
	}
}

func TestJobActivityPruningPreservesLastObservation(t *testing.T) {
	s, _, _ := queueFixture(t)
	at := time.Now().Add(-time.Hour)
	rows := []Record{activityRecord("job_expiring_only_label", "RDP2", "succeeded", at)}
	for i := 0; i < 500; i++ {
		rows = append(rows, activityRecord(fmt.Sprintf("job_retained_%d", i), "SP12", "succeeded", at.Add(time.Duration(i+1)*time.Second)))
	}
	for i := range rows {
		rows[i].OutputExpired = true
	}
	if err := s.insertRecords(rows); err != nil {
		t.Fatal(err)
	}
	before, known := s.AgentActivity("RDP2")
	if !known || before.LastActivityAt.IsZero() {
		t.Fatalf("invalid retention fixture: %+v", before)
	}
	if err := s.pruneTombstones(); err != nil {
		t.Fatal(err)
	}
	if _, found := s.Get(rows[0].ID, ""); found {
		t.Fatal("fixture tombstone was not removed")
	}
	if after, known := s.AgentActivity("RDP2"); !known || after != before {
		t.Fatalf("prune erased activity or changed revision: %+v known=%v", after, known)
	}
}

func TestJobActivityQueryDoesNotAcquireRecordHistory(t *testing.T) {
	s, _, _ := queueFixture(t)
	r := activityRecord("job_query", "CO", "running", time.Now().Add(-time.Second))
	if err := s.insertRecords([]Record{r}); err != nil {
		t.Fatal(err)
	}
	// Snapshot requires this lock. Scalar queries must not inspect retained jobs.
	s.mu.Lock()
	defer s.mu.Unlock()
	done := make(chan bool, 1)
	go func() {
		got, known := s.AgentActivity("CO")
		done <- known && got.InFlightJobs == 1
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("scalar query lost current state")
		}
	case <-time.After(time.Second):
		t.Fatal("scalar query waited for the retained-record lock")
	}
}

func TestJobActivityConcurrentCommitsAndReads(t *testing.T) {
	s, _, _ := queueFixture(t)
	at := time.Now().Add(-time.Minute)
	rows := make([]Record, 32)
	for i := range rows {
		rows[i] = activityRecord(fmt.Sprintf("job_concurrent_%d", i), "FP5", "queued", at)
	}
	if err := s.insertRecords(rows); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				s.AgentActivity("FP5")
				s.ActivityChanged()
			}
			if _, err := s.update(row.ID, func(r *Record) bool {
				r.Status, r.FinishedAt = "succeeded", at.Add(time.Second).Format(time.RFC3339Nano)
				return true
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, known := s.AgentActivity("FP5"); !known || got.InFlightJobs != 0 || got.QueuedJobs != 0 ||
		got.Revision != uint64(2*len(rows)) {
		t.Fatalf("concurrent aggregate: %+v known=%v", got, known)
	}
}

func TestJobActivityRejectsMalformedAndFutureTimes(t *testing.T) {
	now := time.Now().Add(-time.Minute)
	base := activityRecord("job_invalid_time", "FP5", "running", now)
	for name, change := range map[string]func(*Record){
		"missing start":  func(r *Record) { r.StartedAt = "" },
		"invalid start":  func(r *Record) { r.StartedAt = "invalid" },
		"future start":   func(r *Record) { r.StartedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano) },
		"running finish": func(r *Record) { r.FinishedAt = now.Format(time.RFC3339Nano) },
		"missing finish": func(r *Record) { r.Status = "succeeded" },
		"finish before start": func(r *Record) {
			r.Status, r.FinishedAt = "succeeded", now.Add(-time.Second).Format(time.RFC3339Nano)
		},
		"future finish": func(r *Record) {
			r.Status, r.FinishedAt = "succeeded", time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := queueFixture(t)
			r := base
			change(&r)
			if err := s.insertRecords([]Record{r}); err != nil {
				t.Fatal(err)
			}
			if _, known := s.AgentActivity("FP5"); known {
				t.Fatal("invalid source was reported as known")
			}
			if got, known := s.AgentActivity("CO"); !known || got != (ActivityCounts{}) {
				t.Fatal("another label lost its independent empty observation")
			}
		})
	}
}

func TestJobActivityLabelBoundPreservesExistingCounters(t *testing.T) {
	s := &Service{}
	s.activity.byAgent = make(map[string]jobActivity, maxActivityLabels)
	for i := 0; i < maxActivityLabels; i++ {
		s.activity.byAgent[fmt.Sprintf("%04X", i)] = jobActivity{}
	}
	s.activity.markReady()
	r := activityRecord("job_overflow", "ZZZZ", "running", time.Now().Add(-time.Second))
	s.activity.insert([]Record{r}, true)
	if len(s.activity.byAgent) != maxActivityLabels {
		t.Fatal("label counters exceeded the memory bound")
	}
	if _, known := s.AgentActivity("ZZZZ"); known {
		t.Fatal("untracked accepted label became known zero")
	}
	if _, known := s.AgentActivity("CO"); known {
		t.Fatal("absent label became known after overflow")
	}
	if got, known := s.AgentActivity("0000"); !known || got != (ActivityCounts{}) {
		t.Fatal("capacity pressure erased an existing counter")
	}
}

func BenchmarkJobActivityHistory(b *testing.B) {
	for _, count := range []int{500, 5000, 50000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := &Service{records: make(map[string]*jobEntry, count)}
			at := time.Now().Add(-time.Hour)
			rows := make([]Record, count)
			for i := range rows {
				rows[i] = activityRecord(fmt.Sprintf("job_benchmark_%d", i), "FP5", "succeeded", at)
				s.records[rows[i].ID] = newEntry(rows[i], false)
			}
			s.activity.insert(rows, false)
			s.activity.markReady()
			b.Run("snapshot", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if len(s.Snapshot()) != count {
						b.Fatal("lost history")
					}
				}
			})
			b.Run("aggregate", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, known := s.AgentActivity("FP5"); !known {
						b.Fatal("unavailable aggregate")
					}
				}
			})
		})
	}
}
