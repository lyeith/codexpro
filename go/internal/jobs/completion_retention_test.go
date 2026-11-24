package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCompletionRetentionSourceKeepsExactPendingIncarnation(t *testing.T) {
	s, _, resolver := queueFixture(t)
	ctx := context.Background()
	record := occupy(t, s, queueOption(resolver, queueOwner("FP5"), "private completion fixture"))
	completeAdmission(t, s, record)
	keys, err := s.PendingCompletionKeys(ctx)
	if err != nil || len(keys) != 1 || keys[0] != CompletionDedupKey(record) {
		t.Fatalf("wrong pending replay set: %v %v", keys, err)
	}
	// A durable producer acknowledgement removes the replay obligation.
	if _, err := s.db.Exec("DELETE FROM job_completions WHERE job_id=?", record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.update(record.ID, func(next *Record) bool {
		next.CommandLabel += " completed"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	keys, err = s.PendingCompletionKeys(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("terminal metadata update recreated acknowledged completion: %v %v", keys, err)
	}
}

func TestCompletionRetentionSourceFailsOnAnyCorruptOutbox(t *testing.T) {
	s, _, resolver := queueFixture(t)
	record := occupy(t, s, queueOption(resolver, queueOwner("FP5"), "private malformed fixture"))
	completeAdmission(t, s, record)
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM job_completions WHERE job_id=?", record.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var valid Record
	if err := json.Unmarshal(payload, &valid); err != nil {
		t.Fatal(err)
	}
	wrongID := valid
	wrongID.ID += "-mismatch"
	wrongIDBody, err := json.Marshal(wrongID)
	if err != nil {
		t.Fatal(err)
	}
	unfinishedRecord := valid
	unfinishedRecord.Status = "running"
	unfinishedBody, err := json.Marshal(unfinishedRecord)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{", "{}", string(wrongIDBody), string(unfinishedBody)} {
		if _, err := s.db.Exec("UPDATE job_completions SET payload=? WHERE job_id=?", body, record.ID); err != nil {
			t.Fatal(err)
		}
		keys, err := s.PendingCompletionKeys(context.Background())
		if err == nil || keys != nil {
			t.Fatalf("partial/invalid snapshot accepted: %v %v", keys, err)
		}
	}
}

func TestCompletionMaintenanceRunsWithoutProducerLocks(t *testing.T) {
	s, _, _ := queueFixture(t)
	called := make(chan error, 1)
	s.SetCompletionMaintenance(func(ctx context.Context) error {
		_, err := s.PendingCompletionKeys(ctx)
		called <- err
		return err
	})
	s.wg.Add(1)
	go s.completionWorker()
	select {
	case err := <-called:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion maintenance held an incompatible producer lock")
	}
}

func TestCompletionRetentionSourceSurvivesRestart(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	record := occupy(t, s, queueOption(resolver, queueOwner("FP5"), "private restart fixture"))
	completeAdmission(t, s, record)
	// Verify the persisted incarnation rather than a volatile job snapshot.
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM job_completions WHERE job_id=?", record.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var persisted Record
	if err := json.Unmarshal(payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	keys, err := reopened.PendingCompletionKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0] != CompletionDedupKey(persisted) {
		t.Fatalf("restart lost durable replay set: %v %v", keys, err)
	}
}
