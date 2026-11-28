package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func coverageAt(t *testing.T, j *Journal) (healthy bool, since any, ranges []any, incomplete bool) {
	t.Helper()
	c := j.Coverage()
	ranges, _ = c["incomplete_ranges"].([]any)
	return c["appends_healthy"] == true, c["complete_since"], ranges, c["history_incomplete"] == true
}

func editMetadata(t *testing.T, dir string, edit func(map[string]any)) {
	t.Helper()
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: filepath.Join(dir, "index.sqlite")}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	if err = db.QueryRow("SELECT body FROM metadata WHERE id=1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{}
	if err = json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	edit(st)
	raw, _ := json.Marshal(st)
	if _, err = db.Exec("UPDATE metadata SET body=? WHERE id=1", string(raw)); err != nil {
		t.Fatal(err)
	}
}

// Coverage is time-scoped: a failing writer reports no complete window, and
// after recovery every window starting at or after the end of the last
// incomplete interval is complete, while older intervals stay visible.
func TestCoverageScopesIncompleteHistoryToIntervals(t *testing.T) {
	var clock atomic.Int64
	at := func(minute int) time.Time { return time.Date(2026, 11, 28, 10, minute, 0, 0, time.UTC) }
	stamp := func(minute int) string { return timestamp(at(minute)) }
	clock.Store(at(0).UnixNano())
	options := testOptions(t.TempDir())
	options.Clock = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	var failWrites atomic.Bool
	options.Fault = func(stage string) error {
		if stage == "segment_write" && failWrites.Load() {
			return &os.PathError{Op: "write", Path: "segment", Err: syscall.ENOSPC}
		}
		return nil
	}
	j := openTest(t, options)
	if healthy, since, ranges, incomplete := coverageAt(t, j); !healthy || since != stamp(0) || len(ranges) != 0 || incomplete {
		t.Fatalf("fresh journal: %v %v %v %v", healthy, since, ranges, incomplete)
	}
	writeTest(t, j, recordInput("alpha", "before"))

	clock.Store(at(1).UnixNano())
	failWrites.Store(true)
	if _, e := j.Record(context.Background(), recordInput("alpha", "lost")); e == nil {
		t.Fatal("expected the injected append failure")
	}
	c := j.Coverage()
	if healthy, since, ranges, incomplete := coverageAt(t, j); healthy || since != nil || !incomplete || len(ranges) != 1 ||
		c["append_failing_since"] != stamp(1) {
		t.Fatalf("failing appends must report incomplete coverage: %v", c)
	}
	if status, _ := j.Status(context.Background()); status["coverage"].(map[string]any)["appends_healthy"] != false {
		t.Fatalf("status coverage: %v", status["coverage"])
	}

	clock.Store(at(2).UnixNano())
	failWrites.Store(false)
	writeTest(t, j, recordInput("alpha", "after"))
	healthy, since, ranges, incomplete := coverageAt(t, j)
	if !healthy || since != stamp(2) || !incomplete || len(ranges) != 1 {
		t.Fatalf("recovered journal: %v %v %v %v", healthy, since, ranges, incomplete)
	}
	if r := ranges[0].(map[string]any); r["from"] != stamp(1) || r["until"] != stamp(2) || r["reason"] != "append_failed" {
		t.Fatalf("append failure interval: %v", r)
	}

	// A clean restart adds nothing.
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	clock.Store(at(3).UnixNano())
	options.Fault = nil
	j = openTest(t, options)
	if _, since, ranges, _ := coverageAt(t, j); since != stamp(2) || len(ranges) != 1 {
		t.Fatalf("clean restart changed coverage: %v %v", since, ranges)
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}

	// A writer that never closed leaves an interval ending at the next start.
	editMetadata(t, options.Directory, func(st map[string]any) { st["writer_open"] = true })
	clock.Store(at(5).UnixNano())
	j = openTest(t, options)
	_, since, ranges, _ = coverageAt(t, j)
	if since != stamp(5) || len(ranges) != 2 {
		t.Fatalf("unclean stop: %v %v", since, ranges)
	}
	if r := ranges[1].(map[string]any); r["reason"] != "unclean_shutdown" || r["until"] != stamp(5) || r["from"] == nil {
		t.Fatalf("unclean stop interval: %v", r)
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}

	// A torn tail found at startup is also an unclean stop.
	segment := filepath.Join(options.Directory, j.currentName)
	f, err := os.OpenFile(segment, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"schema_version":"1.0"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	clock.Store(at(7).UnixNano())
	j = openTest(t, options)
	if _, since, ranges, _ = coverageAt(t, j); since != stamp(7) || len(ranges) != 3 {
		t.Fatalf("torn tail: %v %v", since, ranges)
	}
}

// A journal from before coverage tracking keeps its earlier torn-tail recovery
// visible and claims completeness only from the first tracked start.
func TestCoverageMigratesEarlierRecoveries(t *testing.T) {
	options := testOptions(t.TempDir())
	start := time.Date(2026, 11, 28, 10, 50, 0, 0, time.UTC)
	options.Clock = func() time.Time { return start }
	j := openTest(t, options)
	writeTest(t, j, recordInput("alpha", "legacy"))
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	editMetadata(t, options.Directory, func(st map[string]any) {
		for _, key := range []string{"coverage_since", "writer_open", "incomplete"} {
			delete(st, key)
		}
		st["malformed"] = 1
	})
	receipt := `{"at":"2026-11-28T09:18:07.570Z","kind":"torn_tail_truncated","offset":1,"removed_bytes":1,"segment":"segment-x.jsonl"}` + "\n"
	if err := os.WriteFile(filepath.Join(options.Directory, "recovery.jsonl"), []byte(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	j = openTest(t, options)
	healthy, since, ranges, incomplete := coverageAt(t, j)
	if !healthy || since != timestamp(start) || !incomplete || len(ranges) != 1 {
		t.Fatalf("migrated coverage: %v %v %v %v", healthy, since, ranges, incomplete)
	}
	if r := ranges[0].(map[string]any); r["until"] != "2026-11-28T09:18:07.570Z" || r["from"] != nil || r["reason"] != "recovered_before_coverage_tracking" {
		t.Fatalf("migrated interval: %v", r)
	}
}

// Retention removes records per bucket, so a caller's recent action can leave
// the retained journal and its sequence then looks skipped. Coverage says such
// a sequence was recorded and removed by retention, not lost, and the agent's
// own actions stay available.
func TestRetentionSkipsSequencesWithoutLosingAgentActivity(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	named := recordInput("alpha", "named-call")
	named.ToolName = "list_projects"
	named.Scope.Agent = "SP16"
	named.Scope.Identity = wire.Identity{Source: "named"}
	first := writeTest(t, j, named)
	for i := 0; i < 4; i++ {
		writeTest(t, j, recordInput("alpha", fmt.Sprintf("busy-%d", i)))
	}
	list, e := j.List(context.Background(), Filter{})
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range list.Actions {
		if a["action_id"] == first.ActionID {
			t.Fatal("fixture expected retention to drop the named call")
		}
	}
	c := j.Coverage()
	if c["missing_sequences"] != "retention_only" || c["latest_sequence"] != int64(5) ||
		c["retained_contiguous_from_sequence"] != int64(4) {
		t.Fatalf("sequence contract: %v", c)
	}
	activity, e := j.AgentActivity(context.Background(), "SP16")
	if e != nil {
		t.Fatal(e)
	}
	actions := activity["actions"].([]any)
	if len(actions) != 1 || activity["complete_since"] == nil {
		t.Fatalf("agent activity: %v", activity)
	}
	if a := actions[0].(map[string]any); a["action_id"] != first.ActionID || a["sequence"] != int64(1) ||
		a["identity_source"] != "named" || a["tool_name"] != "list_projects" || a["recorded_at"] == "" {
		t.Fatalf("agent action: %v", a)
	}

	// The list keeps the newest actions per agent.
	for i := 0; i < agentActionsPerAgent+3; i++ {
		input := named
		input.Scope.RequestID = fmt.Sprintf("named-%d", i)
		writeTest(t, j, input)
	}
	activity, _ = j.AgentActivity(context.Background(), "SP16")
	actions = activity["actions"].([]any)
	if len(actions) != agentActionsPerAgent || actions[0].(map[string]any)["sequence"] != j.Coverage()["latest_sequence"] {
		t.Fatalf("trimmed agent activity: %d %v", len(actions), actions[0])
	}
}

// A record recovered after a failed index commit enters the agent list too.
func TestRecoveredRecordEntersAgentActivity(t *testing.T) {
	options := testOptions(t.TempDir())
	options.Fault = func(stage string) error {
		if stage == "after_sync" {
			return errors.New("crash before index commit")
		}
		return nil
	}
	j := openTest(t, options)
	input := recordInput("alpha", "recovered")
	input.Scope.Agent = "SP16"
	if _, e := j.Record(context.Background(), input); e == nil {
		t.Fatal("expected the injected failure")
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	options.Fault = nil
	j = openTest(t, options)
	activity, e := j.AgentActivity(context.Background(), "SP16")
	if e != nil || len(activity["actions"].([]any)) != 1 {
		t.Fatalf("recovered agent activity: %v %v", activity, e)
	}
}
