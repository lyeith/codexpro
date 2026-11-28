package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
)

// A full disk must not latch the writer: once space returns, the next append
// succeeds without a restart, keeps sequence order and never appends twice.
func TestAppendFailureReopensInsteadOfLatching(t *testing.T) {
	options := testOptions(t.TempDir())
	var j *Journal
	var writeFailures, syncFailures atomic.Int32
	writeFailures.Store(2)
	enospc := func(name string) error {
		return &os.PathError{Op: "write", Path: filepath.Join(options.Directory, name), Err: syscall.ENOSPC}
	}
	options.Fault = func(at string) error {
		switch {
		case at == "segment_write" && writeFailures.Add(-1) >= 0:
			// The failed write leaves part of a record behind.
			f, err := os.OpenFile(filepath.Join(options.Directory, j.currentName), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err = f.WriteString(`{"schema_version":"1.0","sequence":`); err != nil {
				return err
			}
			return enospc(j.currentName)
		case at == "after_append" && syncFailures.Add(-1) >= 0:
			// The record is complete in the segment, but the batch fails before
			// its index commit.
			return enospc(j.currentName)
		}
		return nil
	}
	j = openTest(t, options)
	ctx := context.Background()

	for attempt := 0; attempt < 2; attempt++ {
		r, e := j.Record(ctx, recordInput("alpha", "first"))
		if !errors.Is(e, syscall.ENOSPC) || r.Recorded {
			t.Fatalf("attempt %d: expected ENOSPC without a receipt, got %+v %v", attempt, r, e)
		}
		// Completion retention runs between appends and must reopen too.
		if _, e = j.PruneAcknowledgedCompletions(ctx, func(context.Context) ([]string, error) { return nil, nil }); e != nil {
			t.Fatalf("attempt %d: retention kept the append error: %v", attempt, e)
		}
	}
	if r := writeTest(t, j, recordInput("alpha", "first")); r.Sequence != 1 {
		t.Fatalf("first record after recovery: %+v", r)
	}

	syncFailures.Store(1)
	if r, e := j.Record(ctx, recordInput("alpha", "second")); !errors.Is(e, syscall.ENOSPC) || r.Recorded {
		t.Fatalf("expected ENOSPC after the append, got %+v %v", r, e)
	}
	r, e := j.Record(ctx, recordInput("alpha", "second"))
	if e != nil || !r.Duplicate || r.Sequence != 2 {
		t.Fatalf("retry must deduplicate against the recovered record: %+v %v", r, e)
	}
	if r := writeTest(t, j, recordInput("alpha", "third")); r.Sequence != 3 {
		t.Fatalf("third record: %+v", r)
	}

	assertSequences := func(stage string, j *Journal) {
		t.Helper()
		list, e := j.List(ctx, Filter{})
		if e != nil {
			t.Fatal(stage, e)
		}
		if list.GapDetected || len(list.Actions) != 3 {
			t.Fatalf("%s: gap=%v actions=%d", stage, list.GapDetected, len(list.Actions))
		}
		seen := map[int64]bool{}
		for _, a := range list.Actions {
			seen[int64(number(a["sequence"]))] = true
		}
		if !seen[1] || !seen[2] || !seen[3] {
			t.Fatalf("%s: sequences %v", stage, seen)
		}
	}
	assertSequences("live", j)
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	options.Fault = nil
	reopened := openTest(t, options)
	assertSequences("restart", reopened)
	if r := writeTest(t, reopened, recordInput("alpha", "fourth")); r.Sequence != 4 {
		t.Fatalf("append after restart: %+v", r)
	}
}
