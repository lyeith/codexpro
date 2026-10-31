package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func testOptions(dir string) Options {
	return Options{Directory: dir, Enabled: true, MaxBytes: 8 * 1024 * 1024, RetainActions: 350, SegmentBytes: 4096, Clock: func() time.Time { return time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC) }}
}
func recordInput(project, request string) RecordInput {
	return RecordInput{ToolName: "read", Args: map[string]any{"workspace_id": "ws_test", "path": "safe.txt", "max_bytes": 12000}, Result: wire.Success("read", "Read File", map[string]any{"path": "safe.txt", "bytes": 12, "truncated": false}, "hello"), Scope: wire.Scope{PrincipalID: "principal", TransportID: "transport", RequestID: request}, Target: &Target{Scope: "project", ProjectID: project, WorkspaceID: "ws_test"}}
}
func openTest(t *testing.T, options Options) *Journal {
	t.Helper()
	j, e := Open(options)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := j.Close(); e != nil {
			t.Error(e)
		}
	})
	return j
}
func writeTest(t *testing.T, j *Journal, input RecordInput) Receipt {
	t.Helper()
	r, e := j.Record(context.Background(), input)
	if e != nil {
		t.Fatal(e)
	}
	if !r.Recorded {
		t.Fatalf("not recorded: %+v", r)
	}
	return r
}
func TestConcurrentDedupDurableAcrossRestart(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	var wg sync.WaitGroup
	var recorded, duplicates atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := j.Record(context.Background(), recordInput("alpha", "same"))
			if e != nil {
				t.Error(e)
				return
			}
			if r.Recorded {
				recorded.Add(1)
			}
			if r.Duplicate {
				duplicates.Add(1)
			}
			if r.Sequence != 1 {
				t.Errorf("sequence=%d", r.Sequence)
			}
		}()
	}
	wg.Wait()
	if recorded.Load() != 1 || duplicates.Load() != 31 {
		t.Fatalf("recorded=%d duplicate=%d", recorded.Load(), duplicates.Load())
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	r, e := j.Record(context.Background(), recordInput("alpha", "same"))
	if e != nil || !r.Duplicate || r.Recorded || r.Sequence != 1 {
		t.Fatalf("retry %+v %v", r, e)
	}
	status, e := j.Status(context.Background())
	if e != nil || status["action_count"] != 1 {
		t.Fatalf("status=%v %v", status, e)
	}
}
func TestDurabilityBoundariesNeverAcknowledgeBeforeCommit(t *testing.T) {
	for _, stage := range []string{"before_append", "after_append", "after_sync", "before_index_commit", "after_index_commit"} {
		t.Run(stage, func(t *testing.T) {
			options := testOptions(t.TempDir())
			options.Fault = func(at string) error {
				if at == stage {
					return errors.New("simulated crash")
				}
				return nil
			}
			j := openTest(t, options)
			result, e := j.Record(context.Background(), recordInput("alpha", "request"))
			if e == nil || result.Recorded {
				t.Fatalf("false durable receipt %+v %v", result, e)
			}
			if e = j.Close(); e != nil {
				t.Fatal(e)
			}
			options.Fault = nil
			j = openTest(t, options)
			list, e := j.List(context.Background(), Filter{})
			if e != nil {
				t.Fatal(e)
			}
			expected := 1
			if stage == "before_append" {
				expected = 0
			}
			if len(list.Actions) != expected {
				t.Fatalf("replayed %d, expected %d", len(list.Actions), expected)
			}
			if list.GapDetected {
				t.Fatal("valid durable append replay created a gap")
			}
			if expected == 1 {
				r, e := j.Record(context.Background(), recordInput("alpha", "request"))
				if e != nil || !r.Duplicate || r.Sequence != 1 {
					t.Fatalf("replay dedup %+v %v", r, e)
				}
			}
		})
	}
}
func TestProjectRetentionCursorAndFilteredForwardProgress(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	writeTest(t, j, recordInput("quiet", "q"))
	for i := 0; i < 4; i++ {
		writeTest(t, j, recordInput("busy", fmt.Sprint(i)))
	}
	zero := int64(0)
	if _, e := j.List(context.Background(), Filter{AfterSequence: &zero}); e == nil || !strings.Contains(e.Error(), "oldest safe forward cursor is 3") {
		t.Fatalf("expected explicit planned expiry, got %v", e)
	}
	recovered, e := j.List(context.Background(), Filter{AfterSequence: &zero, CursorRecovery: true})
	if e != nil || len(recovered.Actions) != 3 || recovered.GapDetected || recovered.CursorRecovery["reason"] != "expired" {
		t.Fatalf("recovery=%+v %v", recovered, e)
	}
	three := int64(3)
	list, e := j.List(context.Background(), Filter{AfterSequence: &three, ProjectID: "quiet", Limit: 1})
	if e != nil || len(list.Actions) != 0 || list.NextSequence != 5 || list.HasMore {
		t.Fatalf("filtered cursor failed: %+v %v", list, e)
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	list, e = j.List(context.Background(), Filter{})
	if e != nil || len(list.Actions) != 3 || list.LatestSequence != 5 || list.GapDetected {
		t.Fatalf("retention restart %+v %v", list, e)
	}
}
func TestPublicBoundaryAndOutcomeClassification(t *testing.T) {
	options := testOptions(t.TempDir())
	options.Redact = func(s string) string { return strings.ReplaceAll(s, "dashboard-only-value", "[REDACTED_SECRET]") }
	j := openTest(t, options)
	input := recordInput("alpha", "boundary")
	input.ToolName = "bash"
	input.Mutating = true
	input.Args = map[string]any{"workspace_id": "ws_test", "command": "echo dashboard-only-value"}
	input.Result = wire.Success("bash", "Run Bash", map[string]any{"exit_code": 7, "stderr": "failed", "stdout": ""}, "")
	r := writeTest(t, j, input)
	if r.Status != "failed" {
		t.Fatalf("outcome %s", r.Status)
	}
	public, ok, e := j.Get(context.Background(), r.ActionID)
	if e != nil || !ok {
		t.Fatal(e)
	}
	if _, exists := public["dashboard_metadata"]; exists {
		t.Fatal("public action leaked private metadata")
	}
	body, _ := json.Marshal(public)
	if strings.Contains(string(body), "dashboard-only-value") {
		t.Fatal("public action leaked script")
	}
	private, ok, e := j.GetDashboard(context.Background(), r.ActionID)
	if e != nil || !ok {
		t.Fatal(e)
	}
	body, _ = json.Marshal(private)
	if strings.Contains(string(body), "dashboard-only-value") || !strings.Contains(string(body), "[REDACTED_SECRET]") {
		t.Fatal("authenticated script did not pass configured redaction")
	}
	input.Error = context.Canceled
	input.ToolName = "read"
	input.Result = wire.Result{}
	if status, code := classify(input); status != "cancelled" || code != "cancelled" {
		t.Fatalf("cancellation %s %s", status, code)
	}
}
func TestTornTailRecoveryAndCorruptionDiagnostics(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	r := writeTest(t, j, recordInput("alpha", "1"))
	name := j.currentName
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(filepath.Join(options.Directory, name), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString("{\"sequence\":2")
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	j = openTest(t, options)
	list, e := j.List(context.Background(), Filter{})
	if e != nil || len(list.Actions) != 1 || list.GapDetected || list.MalformedRecords != 1 {
		t.Fatalf("torn tail %+v %v", list, e)
	}
	if _, e = os.Stat(filepath.Join(options.Directory, "recovery.jsonl")); e != nil {
		t.Fatal("missing explicit recovery receipt", e)
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	f, e = os.OpenFile(filepath.Join(options.Directory, name), os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteAt([]byte("!"), 0)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	status, e := j.Status(context.Background())
	if e != nil || status["gap_detected"] != true {
		t.Fatalf("corruption hidden %+v %v", status, e)
	}
	seq := r.Sequence
	if _, e = j.List(context.Background(), Filter{AfterSequence: &seq}); e == nil || !strings.Contains(e.Error(), "gap") {
		t.Fatalf("corrupt forward cursor accepted: %v", e)
	}
	receipt, e := j.Record(context.Background(), recordInput("alpha", "next"))
	if e == nil || receipt.Recorded {
		t.Fatal("corruption accepted an append")
	}
}
func TestCompactionRetainsQuietProjectAndBoundsDisk(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	options.SegmentBytes = 4096
	j := openTest(t, options)
	quiet := writeTest(t, j, recordInput("quiet", "q"))
	for i := 0; i < 60; i++ {
		writeTest(t, j, recordInput("busy", fmt.Sprint(i)))
	}
	if _, ok, e := j.Get(context.Background(), quiet.ActionID); e != nil || !ok {
		t.Fatalf("quiet action lost: %v", e)
	}
	status, e := j.Status(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if status["storage_bytes"].(int64) > 6*options.SegmentBytes {
		t.Fatalf("unbounded old segments: %v", status)
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	list, e := j.List(context.Background(), Filter{})
	if e != nil || list.GapDetected || len(list.Actions) != 3 {
		t.Fatalf("compaction restart %+v %v", list, e)
	}
}
func TestLegacyImportPreservesVerifiedRetentionAndIsIdempotent(t *testing.T) {
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "tool-calls.jsonl")
	sidecar := source + ".index.json"
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	actions := []Action{}
	for _, seq := range []int64{2, 4, 5} {
		a, e := j.build(recordInput("alpha", fmt.Sprint(seq)), seq)
		if e != nil {
			t.Fatal(e)
		}
		actions = append(actions, a)
	}
	var journal, digestSource strings.Builder
	for _, a := range actions {
		b, e := serializeAction(a)
		if e != nil {
			t.Fatal(e)
		}
		journal.Write(b)
		fmt.Fprintf(&digestSource, "%d\x00%s\n", int64(number(a["sequence"])), text(a["action_id"]))
	}
	if e := os.WriteFile(source, []byte(journal.String()), 0600); e != nil {
		t.Fatal(e)
	}
	index := legacyIndex{Schema: "1.0", Journal: "codexpro://actions", Rotation: 7, Earliest: 2, Dropped: 1, Latest: 5, Mode: "per_project", Through: 5, Count: 3, Digest: digest(digestSource.String()), Gaps: 1}
	b, _ := json.Marshal(index)
	if e := os.WriteFile(sidecar, b, 0600); e != nil {
		t.Fatal(e)
	}
	imported, e := j.ImportLegacy(context.Background(), source, sidecar)
	if e != nil || !imported.Imported || imported.GapDetected || imported.LatestSequence != 5 || imported.ActionCount != 3 {
		t.Fatalf("import %+v %v", imported, e)
	}
	again, e := j.ImportLegacy(context.Background(), source, sidecar)
	if e != nil || again.Imported || again.SourceSHA256 != imported.SourceSHA256 {
		t.Fatalf("repeat %+v %v", again, e)
	}
	unchanged, e := os.ReadFile(source)
	if e != nil || string(unchanged) != journal.String() {
		t.Fatal("legacy source changed")
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	list, e := j.List(context.Background(), Filter{})
	if e != nil || list.LatestSequence != 5 || list.GapDetected || len(list.Actions) != 3 {
		t.Fatalf("import restart %+v %v", list, e)
	}
	r := writeTest(t, j, recordInput("alpha", "new"))
	if r.Sequence != 6 {
		t.Fatalf("source sequence reset to %d", r.Sequence)
	}
}
func TestDisabledAndExclusiveWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	j := openTest(t, Options{Directory: dir})
	r, e := j.Record(context.Background(), recordInput("alpha", "1"))
	if e != nil || r.Enabled || r.Recorded {
		t.Fatalf("disabled %+v %v", r, e)
	}
	if _, e = os.Stat(dir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("disabled audit created state")
	}
	active := openTest(t, testOptions(t.TempDir()))
	second, e := Open(active.options)
	if e == nil {
		second.Close()
		t.Fatal("second writer acquired a live journal")
	}
}
