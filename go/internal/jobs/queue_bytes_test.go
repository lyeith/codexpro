package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func payloadRequest(s *Service, resolver testResolver, key string, commands []map[string]any) ([]Record, error) {
	_, err := s.Call(context.Background(), queueOwner("sp5"), "queue_jobs", map[string]any{"workspace_id": resolver.workspace.ID, "commands": commands, "queue_key": key})
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, r := range s.Snapshot() {
		if r.SubmissionKey == key {
			records = append(records, r)
		}
	}
	return records, nil
}

func TestCapacityQueuePayloadOversizedPublicRefusal(t *testing.T) {
	for _, tc := range []struct{ name, command string }{
		{"plain", strings.Repeat(":", 700000)},
		{"JSON-escaped", strings.Repeat("\\", 350000)},
		{"UTF8-bytes", strings.Repeat("界", 234000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg, resolver := queueFixture(t)
			cfg.Set("maxRetainedJobBytes", 1000000)
			_, err := payloadRequest(s, resolver, "oversized", []map[string]any{{"command": tc.command}})
			if errorCode(err) != "job_storage_limit" {
				t.Fatalf("oversized submission was accepted: %v", err)
			}
			if len(s.Snapshot()) != 0 {
				t.Fatal("rejected payload left accepted jobs")
			}
			if _, err = payloadRequest(s, resolver, "fitting-control", []map[string]any{{"command": "true"}}); err != nil {
				t.Fatalf("small valid control also refused: %v", err)
			}
		})
	}
}

func TestCapacityQueuePayloadBatchIsAtomic(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("maxJobsPerWorkspace", 2)
	cfg.Set("maxRetainedJobBytes", 1000000)
	command := strings.Repeat(":", 200000)
	_, err := payloadRequest(s, resolver, "batch", []map[string]any{{"command": command}, {"command": command}})
	if errorCode(err) != "job_storage_limit" || len(s.Snapshot()) != 0 {
		t.Fatalf("aggregate byte refusal was not atomic: %v", err)
	}
	single, err := payloadRequest(s, resolver, "one", []map[string]any{{"command": command}})
	if err != nil || len(single) != 1 {
		t.Fatalf("the individually fitting control did not fit: %v", err)
	}
}

func TestCapacityQueuePayloadSQLiteUTF8BytesAndFullBudgetRetry(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("maxRetainedJobBytes", 1000000)
	commands := []map[string]any{{"command": strings.Repeat("界", 4000)}}
	first, err := payloadRequest(s, resolver, "stored-UTF8", commands)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the encoded bytes identical but exercise SQLite TEXT's character/byte
	// distinction. Counting characters would admit the new small submission.
	if _, err = s.db.Exec("UPDATE jobs SET payload=CAST(payload AS TEXT) WHERE id=?", first[0].ID); err != nil {
		t.Fatal(err)
	}
	var chars, bytes int64
	if err = s.db.QueryRow("SELECT length(payload),length(CAST(payload AS BLOB)) FROM jobs WHERE id=?", first[0].ID).Scan(&chars, &bytes); err != nil {
		t.Fatal(err)
	}
	if bytes-chars < 7000 {
		t.Fatal("fixture did not separate encoded bytes from characters")
	}
	expectedReserved := s.pendingPayloadBytes(bytes)
	gotReserved, err := s.submissionPayloadBytes()
	if err != nil || gotReserved != expectedReserved {
		t.Fatalf("stored byte reservation differs: got %d want %d err %v", gotReserved, expectedReserved, err)
	}
	cfg.Set("maxRetainedJobBytes", expectedReserved-1)
	_, err = payloadRequest(s, resolver, "new-small", []map[string]any{{"command": "true"}})
	if errorCode(err) != "job_storage_limit" {
		t.Fatalf("actual stored byte budget was bypassed: %v", err)
	}
	retry, err := payloadRequest(s, resolver, "stored-UTF8", commands)
	if err != nil || len(retry) != 1 || retry[0].ID != first[0].ID || len(s.Snapshot()) != 1 {
		t.Fatalf("full byte budget prevented exact receipt recovery: %v", err)
	}
}

func TestCapacityQueuePayloadRetainsTerminalReceiptAndCountsOutbox(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	commands := []map[string]any{{"command": strings.Repeat(":", 6000)}}
	first, err := payloadRequest(s, resolver, "terminal", commands)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Stop(first[0].ID, resolver.workspace.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	var recordBytes, completionBytes int64
	if err = s.db.QueryRow("SELECT length(CAST(payload AS BLOB)) FROM jobs WHERE id=?", first[0].ID).Scan(&recordBytes); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT length(CAST(payload AS BLOB)) FROM job_completions WHERE job_id=?", first[0].ID).Scan(&completionBytes); err != nil {
		t.Fatal(err)
	}
	cfg.Set("maxRetainedJobBytes", recordBytes+completionBytes-1)
	_, err = payloadRequest(s, resolver, "new-after-terminal", []map[string]any{{"command": "true"}})
	if errorCode(err) != "job_storage_limit" {
		t.Fatalf("retained terminal/outbox payload was omitted: %v", err)
	}
	if err = s.prune(); err != nil {
		t.Fatal(err)
	}
	retry, err := payloadRequest(s, resolver, "terminal", commands)
	if err != nil || len(retry) != 1 || retry[0].ID != first[0].ID || retry[0].Status != "stopped" {
		t.Fatalf("pruning or quota destroyed the original terminal receipt: %v", err)
	}
	if retry[0].SubmissionExpiresMS != first[0].SubmissionExpiresMS {
		t.Fatal("retry changed receipt lifetime")
	}
}

func TestCapacityQueuePayloadBudgetPreservesPendingInput(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	o := queueOption(resolver, queueOwner("sp5"), "original-output")
	source := occupy(t, s, o)
	dir := s.outputDir(source)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(strings.Repeat("x", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	completeAdmission(t, s, source)
	commands := []map[string]any{{"command": "read-original", "input_job_ids": []string{source.ID}}}
	reader, err := payloadRequest(s, resolver, "pending-reader", commands)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := s.submissionPayloadBytes()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Set("maxRetainedJobBytes", metadata+5000)
	if err = s.checkInputPins(nil, s.Snapshot(), 0); errorCode(err) != "job_storage_limit" {
		t.Fatalf("protected input alone bypassed remaining byte budget: %v", err)
	}
	_, err = payloadRequest(s, resolver, "would-displace-input", []map[string]any{{"command": strings.Repeat(":", 3000)}})
	if errorCode(err) != "job_storage_limit" {
		t.Fatalf("payload displaced protected input budget: %v", err)
	}
	if err = s.prune(); err != nil {
		t.Fatal(err)
	}
	kept, _ := s.Get(source.ID, resolver.workspace.ID)
	pending, _ := s.Get(reader[0].ID, resolver.workspace.ID)
	body, err := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if err != nil || len(body) != 10000 || kept.OutputExpired || pending.Status != "queued" {
		t.Fatalf("pruning evicted a protected input: bytes=%d expired=%v status=%s err=%v", len(body), kept.OutputExpired, pending.Status, err)
	}
	retry, err := payloadRequest(s, resolver, "pending-reader", commands)
	if err != nil || retry[0].ID != reader[0].ID {
		t.Fatalf("input-pinned retry was not preserved: %v", err)
	}
}

func TestCapacityQueuePayloadDoesNotImposeAnOrdinaryCommandLimit(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("maxRetainedJobBytes", 8192)
	original, _, err := s.admit(queueOption(resolver, queueOwner("sp5"), strings.Repeat(":", 20000)))
	if err != nil {
		t.Fatalf("ordinary reject-mode admission gained a command-byte limit: %v", err)
	}
	completeAdmission(t, s, original)
}

func TestCapacityQueuePayloadRetrySurvivesFullPendingAndByteCapacity(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	commands := []map[string]any{{"command": "true"}}
	first, err := payloadRequest(s, resolver, "first", commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"second", "third", "fourth"} {
		if _, err = payloadRequest(s, resolver, key, commands); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Set("maxRetainedJobBytes", 1)
	retry, err := payloadRequest(s, resolver, "first", commands)
	if err != nil || len(retry) != 1 || retry[0].ID != first[0].ID {
		t.Fatalf("full capacity stranded an accepted key: %v", err)
	}
	_, err = payloadRequest(s, resolver, "fifth", commands)
	if errorCode(err) != "job_queue_full" || len(s.Snapshot()) != 4 {
		t.Fatalf("new payload bypassed full pending count: %v", err)
	}
}

func TestCapacityQueueReservesCompletionCopyBeforeAcceptance(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	cfg.Set("maxRetainedJobBytes", 500000)
	commands := []map[string]any{{"command": strings.Repeat(":", 350000)}}
	if _, err := payloadRequest(s, resolver, "near-budget", commands); errorCode(err) != "job_storage_limit" || len(s.Snapshot()) != 0 {
		t.Fatalf("single row fit but its required completion copy was not reserved: %v", err)
	}
	cfg.Set("maxRetainedJobBytes", 2000000)
	rows, err := payloadRequest(s, resolver, "fitting-copy", commands)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Stop(rows[0].ID, resolver.workspace.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	var stored int64
	if err = s.db.QueryRow("SELECT length(CAST(j.payload AS BLOB))+length(CAST(c.payload AS BLOB)) FROM jobs j JOIN job_completions c ON c.job_id=j.id WHERE j.id=?", rows[0].ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored > 2000000 {
		t.Fatalf("terminal payload exceeded its admitted budget: %d", stored)
	}
	retry, err := payloadRequest(s, resolver, "fitting-copy", commands)
	if err != nil || retry[0].ID != rows[0].ID {
		t.Fatalf("completion reservation lost original retry: %v", err)
	}
}
