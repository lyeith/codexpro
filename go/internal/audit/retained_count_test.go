package audit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func retainedCountEntry(sequence int64, bucket string, length int64) entry {
	project := ""
	if strings.HasPrefix(bucket, "project:") {
		project = strings.TrimPrefix(bucket, "project:")
	}
	return entry{
		Sequence: sequence, ActionID: fmt.Sprintf("count-action-%d", sequence),
		ProjectID: project, Bucket: bucket, Fingerprint: fmt.Sprintf("count-fingerprint-%d", sequence),
		Segment: "count-fixture.jsonl", Offset: sequence * 100, Length: length,
	}
}

func assertRetainedCountSequences(t *testing.T, label string, entries []entry, want []int64) {
	t.Helper()
	got := make([]int64, len(entries))
	for i, e := range entries {
		got[i] = e.Sequence
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s sequences: got %v, want %v", label, got, want)
	}
}

type retainedCountMemorySnapshot struct {
	Entries       []entry
	ByID          map[string]entry
	ByFingerprint map[string]entry
	Counts        map[string]int
	Bytes         int64
	Remembered    operationalLimits
	State         state
}

func captureRetainedCountMemory(j *Journal) retainedCountMemorySnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := retainedCountMemorySnapshot{
		Entries:       append([]entry(nil), j.entries...),
		ByID:          make(map[string]entry, len(j.byID)),
		ByFingerprint: make(map[string]entry, len(j.byFingerprint)),
		Counts:        make(map[string]int, len(j.retainedCounts)),
		Bytes:         j.retainedBytes, Remembered: j.retentionLimits, State: j.state,
	}
	for key, value := range j.byID {
		out.ByID[key] = value
	}
	for key, value := range j.byFingerprint {
		out.ByFingerprint[key] = value
	}
	for key, value := range j.retainedCounts {
		out.Counts[key] = value
	}
	return out
}

func TestAuditCountRetentionPlanner(t *testing.T) {
	const alpha = "project:alpha"
	const delta = "project:delta"
	limits := func(maxBytes int64, retainActions int) operationalLimits {
		return operationalLimits{maxBytes: maxBytes, retainActions: retainActions, maxOutputBytes: 4096}
	}
	byteHistory := []entry{
		retainedCountEntry(1, alpha, 15),
		retainedCountEntry(2, alpha, 40),
		retainedCountEntry(3, "server", 25),
	}
	cases := []struct {
		name                  string
		committed, added      []entry
		remembered, current   operationalLimits
		wantKept, wantDropped []int64
	}{
		{
			name: "multiple_buckets_and_additions",
			committed: []entry{
				retainedCountEntry(1, alpha, 10),
				retainedCountEntry(2, "server", 10),
				retainedCountEntry(3, alpha, 10),
				retainedCountEntry(4, "unattributed", 10),
				retainedCountEntry(5, "server", 10),
			},
			added: []entry{
				retainedCountEntry(6, alpha, 10),
				retainedCountEntry(7, "server", 10),
				retainedCountEntry(8, alpha, 10),
				retainedCountEntry(9, delta, 10),
				retainedCountEntry(10, alpha, 10),
			},
			remembered: limits(1000, 2), current: limits(1000, 2),
			wantKept: []int64{4, 5, 7, 8, 9, 10}, wantDropped: []int64{1, 2, 3, 6},
		},
		{
			// At exactly MaxBytes, count pruning leaves 85 bytes. The
			// 80-percent byte-pressure target must not apply.
			name:      "exact_byte_boundary",
			committed: byteHistory, added: []entry{retainedCountEntry(4, alpha, 20)},
			remembered: limits(100, 2), current: limits(100, 2),
			wantKept: []int64{2, 3, 4}, wantDropped: []int64{1},
		},
		{
			// The pre-pruning total is 105. Count pruning alone would
			// leave 90, but the full selector still applies its 80-byte target.
			name:      "byte_hysteresis_after_count_pruning",
			committed: byteHistory, added: []entry{retainedCountEntry(4, alpha, 25)},
			remembered: limits(100, 2), current: limits(100, 2),
			wantKept: []int64{3, 4}, wantDropped: []int64{1, 2},
		},
		{
			name: "changed_count_limit_prunes_untouched_buckets",
			committed: []entry{
				retainedCountEntry(1, alpha, 10),
				retainedCountEntry(2, "server", 10),
				retainedCountEntry(3, alpha, 10),
				retainedCountEntry(4, "unattributed", 10),
				retainedCountEntry(5, "server", 10),
			},
			added:      []entry{retainedCountEntry(6, delta, 10)},
			remembered: limits(1000, 2), current: limits(1000, 1),
			wantKept: []int64{3, 4, 5, 6}, wantDropped: []int64{1, 2},
		},
		{
			name:      "changed_byte_limit",
			committed: byteHistory, added: []entry{retainedCountEntry(4, alpha, 25)},
			remembered: limits(200, 2), current: limits(100, 2),
			wantKept: []int64{3, 4}, wantDropped: []int64{1, 2},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{
				options: Options{
					MaxBytes: tc.remembered.maxBytes, RetainActions: tc.remembered.retainActions,
					MaxOutputBytes: tc.remembered.maxOutputBytes,
				},
				// Deliberate spare capacity also exposes a planner that
				// overwrites committed entries while filtering in place.
				entries: make([]entry, len(tc.committed), len(tc.committed)+len(tc.added)+2),
			}
			copy(j.entries, tc.committed)
			j.state.Latest = j.entries[len(j.entries)-1].Sequence
			j.SetLimits(tc.remembered.maxBytes, tc.remembered.retainActions, tc.remembered.maxOutputBytes)
			j.rebuildMaps()
			j.SetLimits(tc.current.maxBytes, tc.current.retainActions, tc.current.maxOutputBytes)
			before := captureRetainedCountMemory(j)
			addedBefore := append([]entry(nil), tc.added...)

			// The reference selector receives independent storage. Explicit
			// sequences below keep a shared selector mistake from defining success.
			all := append(append([]entry(nil), before.Entries...), tc.added...)
			options := Options{MaxBytes: tc.current.maxBytes, RetainActions: tc.current.retainActions}
			wantKept, wantDropped := selectRetained(all, options)
			j.mu.Lock()
			kept, dropped, captured := j.retainedAfterAppend(tc.added)
			j.mu.Unlock()

			assertRetainedCountSequences(t, "retained", kept, tc.wantKept)
			assertRetainedCountSequences(t, "dropped", dropped, tc.wantDropped)
			if !reflect.DeepEqual(kept, wantKept) || !reflect.DeepEqual(dropped, wantDropped) {
				t.Fatalf("full selector mismatch:\nretained: got %#v, want %#v\ndropped: got %#v, want %#v",
					kept, wantKept, dropped, wantDropped)
			}
			if captured != tc.current {
				t.Fatalf("captured limits: got %+v, want %+v", captured, tc.current)
			}
			if after := captureRetainedCountMemory(j); !reflect.DeepEqual(after, before) {
				t.Fatalf("planning changed committed memory:\nbefore: %#v\nafter: %#v", before, after)
			}
			if !reflect.DeepEqual(tc.added, addedBefore) {
				t.Fatalf("planning changed the supplied additions: got %#v, want %#v", tc.added, addedBefore)
			}
		})
	}
}

type retainedCountSegment struct {
	Name           string
	CommittedBytes int64
}

type retainedCountSQLProjection struct {
	Entries  []entry
	Segments []retainedCountSegment
	Metadata string
}

// Call outside the fault callback: that callback runs with the writer's SQL
// transaction active, and this journal intentionally has only one DB connection.
func captureRetainedCountSQL(t *testing.T, j *Journal) retainedCountSQLProjection {
	t.Helper()
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := retainedCountSQLProjection{Entries: []entry{}, Segments: []retainedCountSegment{}}
	rows, err := j.db.Query("SELECT sequence,action_id,project_id,bucket,fingerprint,segment,offset,length FROM entries ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.Sequence, &e.ActionID, &e.ProjectID, &e.Bucket, &e.Fingerprint, &e.Segment, &e.Offset, &e.Length); err != nil {
			t.Fatal(err)
		}
		out.Entries = append(out.Entries, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	rows, err = j.db.Query("SELECT name,committed_bytes FROM segments ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var segment retainedCountSegment
		if err := rows.Scan(&segment.Name, &segment.CommittedBytes); err != nil {
			t.Fatal(err)
		}
		out.Segments = append(out.Segments, segment)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if err := j.db.QueryRow("SELECT body FROM metadata WHERE id=1").Scan(&out.Metadata); err != nil {
		t.Fatal(err)
	}
	return out
}

func retainedCountInput(project, requestID string) RecordInput {
	return RecordInput{
		ToolName: "read",
		Args:     map[string]any{"path": "count-fixture.txt", "max_bytes": 64},
		Result:   wire.Success("read", "Read File", map[string]any{"path": "count-fixture.txt", "bytes": 5}, "count"),
		Scope:    wire.Scope{PrincipalID: "count-principal", TransportID: "count-transport", RequestID: requestID},
		Target:   &Target{Scope: "project", ProjectID: project, WorkspaceID: "ws_count_fixture"},
	}
}

func TestAuditCountRetentionRollbackPreservesCommittedProjection(t *testing.T) {
	var armed atomic.Bool
	var faultCalls atomic.Int32
	injected := errors.New("count-retention before_index_commit")
	options := Options{
		Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20,
		RetainActions: 2, MaxOutputBytes: 4096, SegmentBytes: 1 << 20,
		Clock: func() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) },
		Fault: func(stage string) error {
			if stage == "before_index_commit" && armed.Load() {
				faultCalls.Add(1)
				return injected
			}
			return nil
		},
	}
	j, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := j.Close(); err != nil {
			t.Error(err)
		}
	})
	for i, project := range []string{"alpha", "beta", "alpha"} {
		receipt, err := j.Record(context.Background(), retainedCountInput(project, fmt.Sprintf("seed-%d", i)))
		if err != nil || !receipt.Recorded || receipt.Duplicate {
			t.Fatalf("seed %d: receipt=%+v err=%v", i, receipt, err)
		}
	}
	// Acquiring the writer lock also waits for the final seed's maintenance.
	j.mu.Lock()
	withSpareCapacity := make([]entry, len(j.entries), len(j.entries)+8)
	copy(withSpareCapacity, j.entries)
	j.entries = withSpareCapacity
	j.mu.Unlock()
	before := captureRetainedCountMemory(j)
	beforeSQL := captureRetainedCountSQL(t, j)
	assertRetainedCountSequences(t, "committed seed", before.Entries, []int64{1, 2, 3})
	if !reflect.DeepEqual(before.Entries, beforeSQL.Entries) {
		t.Fatal("seed memory differs from its committed index")
	}

	// Only the output limit changes. The count-only path remains eligible,
	// while premature publication of the remembered limits becomes observable.
	j.SetLimits(options.MaxBytes, options.RetainActions, options.MaxOutputBytes*2)
	configured := j.currentLimits()
	if configured.maxBytes != before.Remembered.maxBytes ||
		configured.retainActions != before.Remembered.retainActions ||
		configured.maxOutputBytes == before.Remembered.maxOutputBytes {
		t.Fatal("fixture did not change only MaxOutputBytes")
	}
	armed.Store(true)
	receipt, err := j.Record(context.Background(), retainedCountInput("alpha", "failed-append"))
	if !errors.Is(err, injected) || receipt.Recorded || receipt.Duplicate || faultCalls.Load() != 1 {
		t.Fatalf("fault outcome: receipt=%+v err=%v calls=%d", receipt, err, faultCalls.Load())
	}
	if receipt.Sequence != 4 || receipt.ActionID == "" {
		t.Fatalf("fault did not follow the fourth append: %+v", receipt)
	}

	// Inspect before reopening: the failed transaction must neither publish
	// retention changes in memory nor change the durable indexed projection.
	if after := captureRetainedCountMemory(j); !reflect.DeepEqual(after, before) {
		t.Fatalf("rollback changed committed memory:\nbefore: %#v\nafter: %#v", before, after)
	}
	if afterSQL := captureRetainedCountSQL(t, j); !reflect.DeepEqual(afterSQL, beforeSQL) {
		t.Fatalf("rollback changed the indexed projection:\nbefore: %#v\nafter: %#v", beforeSQL, afterSQL)
	}
	if j.currentLimits() != configured {
		t.Fatal("rollback changed the separately published runtime limits")
	}

	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	options.Fault = nil
	options.MaxOutputBytes = configured.maxOutputBytes
	reopened, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	})
	// The JSONL append was synced before the injected fault. Existing recovery
	// legitimately adopts it, then applies retention through the full selector.
	recovered := captureRetainedCountMemory(reopened)
	assertRetainedCountSequences(t, "recovered retention", recovered.Entries, []int64{2, 3, 4})
	if recovered.State.Gap || recovered.State.Latest != 4 || recovered.Entries[2].ActionID != receipt.ActionID {
		t.Fatalf("synced-tail recovery: state=%+v entries=%#v", recovered.State, recovered.Entries)
	}
	if recoveredSQL := captureRetainedCountSQL(t, reopened); !reflect.DeepEqual(recoveredSQL.Entries, recovered.Entries) {
		t.Fatalf("recovered memory differs from index: %#v != %#v", recovered.Entries, recoveredSQL.Entries)
	}
}
