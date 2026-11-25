package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Listing needs configuration and a resolver, but no process workers or database.
// Every possible output metadata path stays inside the fixture's temporary root.
func newListHistoryService(tb testing.TB) (*Service, wire.Workspace, wire.Identity) {
	tb.Helper()
	base := tb.TempDir()
	cfg := config.New(filepath.Join(base, "state"))
	w := wire.Workspace{
		ID: "bench-workspace", ProjectID: "bench-project",
		Root: filepath.Join(base, "workspace"),
	}
	s := &Service{
		cfg: cfg, resolver: testResolver{workspace: w},
		dir: filepath.Join(cfg.Home, "jobs"), records: make(map[string]*jobEntry),
	}
	identity := wire.Identity{
		Source: "named", PrincipalType: "human", PrincipalID: "list-principal",
		CredentialID: "list-credential", AgentID: "FP5", AgentKind: "fleet",
	}
	return s, w, identity
}

func listHistoryRecord(i int, identity wire.Identity, at time.Time, status string) Record {
	r := attachStatusHistoryRecord(i, identity, at)
	r.Status = status
	if unfinished(r) {
		r.Quiescent = false
		r.ExitCode = nil
		r.FinishedAt = ""
		r.FinishedAtMS = 0
	}
	return r
}

func putListHistoryRecord(s *Service, w wire.Workspace, r Record) Record {
	r.Root = w.Root
	r.StdoutPath = filepath.Join(s.dir, r.ID, "stdout.capture")
	r.StderrPath = filepath.Join(s.dir, r.ID, "stderr.capture")
	r.ResultPath = filepath.Join(s.dir, r.ID, "result.json")
	s.records[r.ID] = newEntry(r, false)
	return r
}

func callListHistory(tb testing.TB, s *Service, w wire.Workspace, identity wire.Identity, args map[string]any) wire.Result {
	tb.Helper()
	request := map[string]any{"workspace_id": w.ID, "output": "none", "wait_ms": 0}
	for key, value := range args {
		request[key] = value
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.Call(ctx, wire.Scope{Agent: "FP5", Identity: identity}, "jobs", request)
	if err != nil || got.IsError {
		tb.Fatalf("public jobs call failed: result=%#v error=%v", got, err)
	}
	return got
}

func requireListHistoryIDs(tb testing.TB, result wire.Result, want []string) {
	tb.Helper()
	rows, ok := result.StructuredContent["jobs"].([]any)
	if !ok || len(rows) != len(want) {
		tb.Fatalf("jobs rows=%#v, want IDs %v", result.StructuredContent["jobs"], want)
	}
	if number(result.StructuredContent["count"]) != int64(len(want)) {
		tb.Fatalf("count=%v, want %d", result.StructuredContent["count"], len(want))
	}
	for i, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["job_id"] != want[i] {
			tb.Fatalf("row %d=%#v, want job %s", i, value, want[i])
		}
	}
}

func requireListHistoryUnchanged(tb testing.TB, s *Service, before map[string]Record) {
	tb.Helper()
	for id, original := range before {
		got, ok := s.Get(id, "")
		if !ok || !reflect.DeepEqual(got, original) {
			tb.Fatalf("listing mutated retained record %s: got=%+v want=%+v", id, got, original)
		}
	}
}

func TestJobsListHistoryOrderingAndFinishedModes(t *testing.T) {
	s, w, identity := newListHistoryService(t)
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	before := make(map[string]Record)
	statuses := []string{"succeeded", "failed", "stopped", "queued"}
	for i := 0; i < 24; i++ {
		r := listHistoryRecord(i, identity, at.Add(time.Duration(i)*time.Second), statuses[i%4])
		r.ID = fmt.Sprintf("list-order-%02d", i)
		r.Acknowledged = i%2 == 0
		r.OutputExpired = !unfinished(r) && i%5 == 0
		if i == 21 {
			r.Origin = "promoted"
		}
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	// All running jobs are older than the queued and terminal records.
	for i := 0; i < 3; i++ {
		r := listHistoryRecord(100+i, identity, at.Add(-time.Minute+time.Duration(i)*time.Second), "running")
		r.ID = fmt.Sprintf("list-running-%d", i)
		r.Acknowledged = false
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	wantAll := []string{"list-running-2", "list-running-1", "list-running-0"}
	for i := 23; i >= 7; i-- {
		wantAll = append(wantAll, fmt.Sprintf("list-order-%02d", i))
	}
	wantUnfinished := []string{
		"list-running-2", "list-running-1", "list-running-0",
		"list-order-23", "list-order-19", "list-order-15",
		"list-order-11", "list-order-07", "list-order-03",
	}
	cases := []struct {
		name   string
		args   map[string]any
		want   []string
		queued int64
	}{
		{"default_finished", nil, wantAll, 5},
		{"include_finished", map[string]any{"include_finished": true}, wantAll, 5},
		{"unfinished_only", map[string]any{"include_finished": false}, wantUnfinished, 6},
		{"tail_listing", map[string]any{"output": "tail"}, wantAll, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := callListHistory(t, s, w, identity, tc.args)
			requireListHistoryIDs(t, got, tc.want)
			if number(got.StructuredContent["running_count"]) != 3 ||
				number(got.StructuredContent["queued_count"]) != tc.queued ||
				got.StructuredContent["all_finished"] != false ||
				number(got.StructuredContent["requested_wait_ms"]) != 0 {
				t.Fatalf("listing summary changed: %#v", got.StructuredContent)
			}
			requireListHistoryUnchanged(t, s, before)
		})
	}
}

func TestJobsListHistoryFiltersBeforeCap(t *testing.T) {
	s, w, identity := newListHistoryService(t)
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	before := make(map[string]Record)
	for i := 0; i < 24; i++ {
		r := listHistoryRecord(i, identity, at.Add(time.Duration(i)*time.Second), "succeeded")
		r.ID = fmt.Sprintf("list-owned-%02d", i)
		r.Agent = "different-display-label"
		r.Identity.Role = "different-role"
		r.Identity.PrincipalType = "service"
		r.Acknowledged = i%2 == 0
		r.OutputExpired = i%3 == 0
		r.Origin = "promoted"
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	// Every excluded job is newer and running. Filtering after the cap would
	// hide the eligible terminal records altogether.
	for i := 0; i < 48; i++ {
		r := listHistoryRecord(100+i, identity, at.Add(time.Hour+time.Duration(i)*time.Second), "running")
		r.ID = fmt.Sprintf("list-excluded-%02d", i)
		r.Agent = "FP5"
		r.Acknowledged = false
		switch i % 8 {
		case 0:
			r.Origin = "foreground"
		case 1:
			r.WorkspaceID = "other-workspace"
		case 2:
			r.Identity.CredentialID = "other-credential"
		case 3:
			r.Identity.PrincipalID = "other-principal"
		case 4:
			r.Identity.AgentID = "other-agent"
		case 5:
			r.Identity.AgentKind = "other-kind"
		case 6:
			r.Identity = wire.Identity{}
		case 7:
			r.Identity.Source = "legacy"
		}
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	want := make([]string, 0, 20)
	for i := 23; i >= 4; i-- {
		want = append(want, fmt.Sprintf("list-owned-%02d", i))
	}
	requireListHistoryIDs(t, callListHistory(t, s, w, identity, nil), want)
	requireListHistoryIDs(t, callListHistory(t, s, w, identity, map[string]any{"include_finished": false}), nil)
	requireListHistoryUnchanged(t, s, before)
}

func TestJobsListHistoryNonNamedCompatibility(t *testing.T) {
	s, w, identity := newListHistoryService(t)
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	before := make(map[string]Record)
	for i, source := range []string{"", "legacy", "named"} {
		r := listHistoryRecord(i, identity, at.Add(time.Duration(i)*time.Second), "succeeded")
		r.ID = []string{"list-unnamed", "list-legacy", "list-named"}[i]
		r.Identity.Source = source
		r.Acknowledged = false
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	for _, caller := range []wire.Identity{{}, {Source: "legacy", CredentialID: "different-legacy"}} {
		requireListHistoryIDs(t, callListHistory(t, s, w, caller, nil), []string{"list-legacy", "list-unnamed"})
	}
	requireListHistoryUnchanged(t, s, before)
}

func TestJobsListHistoryExplicitIDsKeepOrderAndLimit(t *testing.T) {
	s, w, identity := newListHistoryService(t)
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	before := make(map[string]Record)
	for i := 0; i < 25; i++ {
		r := listHistoryRecord(i, identity, at.Add(time.Duration(i)*time.Second), "succeeded")
		r.ID = fmt.Sprintf("list-explicit-%02d", i)
		r.Acknowledged = false
		r.OutputExpired = true
		if i%2 == 0 {
			r.Origin = "foreground"
		}
		before[r.ID] = putListHistoryRecord(s, w, r)
	}
	// A permutation distinct from either chronological ordering.
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = fmt.Sprintf("list-explicit-%02d", (i*7)%25)
	}
	got := callListHistory(t, s, w, identity, map[string]any{
		"job_ids": ids, "include_finished": false,
	})
	requireListHistoryIDs(t, got, ids)
	if got.StructuredContent["all_finished"] != true {
		t.Fatalf("explicit finished records reported unfinished: %#v", got.StructuredContent)
	}
	requireListHistoryUnchanged(t, s, before)
}

func TestJobsListHistoryConcurrentPublication(t *testing.T) {
	s, w, identity := newListHistoryService(t)
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	allowed := make(map[string]bool)
	var changing *jobEntry
	for i := 0; i < 24; i++ {
		r := listHistoryRecord(i, identity, at.Add(time.Duration(i)*time.Second), "queued")
		r.ID = fmt.Sprintf("list-concurrent-%02d", i)
		r.Acknowledged = false
		putListHistoryRecord(s, w, r)
		allowed[r.ID] = true
		if i == 0 {
			changing = s.records[r.ID]
		}
	}
	start, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for i := 0; i < 256; i++ {
			changing.mu.Lock()
			next := copyRecord(changing.record)
			next.Status = "queued"
			if i%2 == 0 {
				next.Status = "running"
			}
			next.StartedAtMS = at.Add(time.Duration(i) * time.Millisecond).UnixMilli()
			next.StartedAt = time.UnixMilli(next.StartedAtMS).UTC().Format(time.RFC3339Nano)
			next.InputJobIDs[0] = fmt.Sprintf("published-input-%d", i)
			publishEntry(changing, next)
			changing.mu.Unlock()
			runtime.Gosched()
		}
	}()
	close(start)
	defer func() { <-done }()
	for i := 0; i < 64; i++ {
		got := callListHistory(t, s, w, identity, map[string]any{"include_finished": false})
		rows, ok := got.StructuredContent["jobs"].([]any)
		if !ok || len(rows) != 20 {
			t.Fatalf("concurrent listing rows=%#v", got.StructuredContent["jobs"])
		}
		seen := make(map[string]bool, len(rows))
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("invalid concurrent row: %#v", value)
			}
			id, _ := row["job_id"].(string)
			if !allowed[id] || seen[id] {
				t.Fatalf("foreign or duplicate concurrent row: %#v", row)
			}
			seen[id] = true
		}
	}
}
