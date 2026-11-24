package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func completionReceiptCount(t *testing.T, j *Journal) int {
	t.Helper()
	var count int
	if err := j.db.QueryRow("SELECT COUNT(*) FROM completion_receipts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCompletionRetentionRequiresDurableAcknowledgement(t *testing.T) {
	ctx := context.Background()
	options := Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 1}
	j, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	old := privateCompletionInput("old-pending-incarnation")
	first, err := j.Record(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Record(ctx, privateCompletionInput("retained-incarnation")); err != nil {
		t.Fatal(err)
	}
	pending := func(context.Context) ([]string, error) { return []string{old.CompletionDedupKey}, nil }
	if n, err := j.PruneAcknowledgedCompletions(ctx, pending); err != nil || n != 0 {
		t.Fatalf("pending or visible receipt pruned: %d %v", n, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	retry, err := j.Record(ctx, old)
	if err != nil || !retry.Duplicate || retry.ActionID != first.ActionID || retry.Sequence != first.Sequence {
		t.Fatalf("lost acknowledgement duplicated after restart: %+v %v", retry, err)
	}
	acknowledged := func(context.Context) ([]string, error) { return nil, nil }
	if n, err := j.PruneAcknowledgedCompletions(ctx, acknowledged); err != nil || n != 1 {
		t.Fatalf("acknowledged expired receipt not released: %d %v", n, err)
	}
	if count := completionReceiptCount(t, j); count != 1 {
		t.Fatalf("visible receipt must remain: %d", count)
	}
	if n, err := j.PruneAcknowledgedCompletions(ctx, acknowledged); err != nil || n != 0 {
		t.Fatalf("unchanged maintenance not idempotent: %d %v", n, err)
	}
}

func TestCompletionRetentionRejectsIncompleteSource(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	for _, key := range []string{"old", "new"} {
		if _, err := j.Record(ctx, privateCompletionInput(key)); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("producer unavailable")
	for _, source := range []func(context.Context) ([]string, error){
		nil,
		func(context.Context) ([]string, error) { return []string{"new"}, failure },
		func(context.Context) ([]string, error) { return []string{""}, nil },
	} {
		if n, err := j.PruneAcknowledgedCompletions(ctx, source); err == nil || n != 0 {
			t.Fatalf("invalid source accepted: %d %v", n, err)
		}
		if count := completionReceiptCount(t, j); count != 2 {
			t.Fatalf("receipts changed: %d", count)
		}
	}
	if _, err := j.db.Exec("INSERT INTO completion_receipts VALUES ('legacy-request','legacy-action',999)"); err != nil {
		t.Fatal(err)
	}
	if n, err := j.PruneAcknowledgedCompletions(ctx, func(context.Context) ([]string, error) { return nil, nil }); err != nil || n != 1 {
		t.Fatalf("unexpected release: %d %v", n, err)
	}
	var legacy int
	if err := j.db.QueryRow("SELECT COUNT(*) FROM completion_receipts WHERE fingerprint='legacy-request'").Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("legacy contract was removed: %d %v", legacy, err)
	}
}

func TestCompletionRetentionFencesNewAuditReceipts(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	for _, key := range []string{"old", "visible"} {
		if _, err := j.Record(ctx, privateCompletionInput(key)); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	pruned := make(chan error, 1)
	go func() {
		_, err := j.PruneAcknowledgedCompletions(ctx, func(context.Context) ([]string, error) {
			close(entered)
			<-release
			return nil, nil
		})
		pruned <- err
	}()
	<-entered
	recorded := make(chan response, 1)
	go func() {
		r, err := j.Record(ctx, privateCompletionInput("new-after-source-snapshot"))
		recorded <- response{r, err}
	}()
	select {
	case <-recorded:
		close(release)
		t.Fatal("new receipt escaped the journal write fence")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-pruned; err != nil {
		t.Fatal(err)
	}
	result := <-recorded
	if result.err != nil {
		t.Fatal(result.err)
	}
	// The new pending receipt is preserved even when a later visible action
	// evicts it before producer acknowledgement.
	if _, err := j.Record(ctx, privateCompletionInput("newest-visible")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.PruneAcknowledgedCompletions(ctx, func(context.Context) ([]string, error) {
		return []string{"new-after-source-snapshot"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	retry, err := j.Record(ctx, privateCompletionInput("new-after-source-snapshot"))
	if err != nil || !retry.Duplicate {
		t.Fatalf("new pending receipt lost: %+v %v", retry, err)
	}
}
