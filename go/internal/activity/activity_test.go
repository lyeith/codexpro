package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type fakeResolver struct {
	projects []wire.Project
	resolves atomic.Int64
}

func (f *fakeResolver) Resolve(context.Context, wire.Scope, string) (wire.Workspace, error) {
	f.resolves.Add(1)
	return wire.Workspace{}, errors.New("monitor must not open a workspace")
}
func (f *fakeResolver) Projects() []wire.Project { return append([]wire.Project(nil), f.projects...) }
func (f *fakeResolver) Project(id string) (wire.Project, bool) {
	for _, p := range f.projects {
		if p.ID == id {
			return p, true
		}
	}
	return wire.Project{}, false
}

type fakeJobs struct {
	records   []jobs.Record
	tails     map[string]jobs.Output
	reads     atomic.Int64
	snapshots atomic.Int64
}

func (f *fakeJobs) Snapshot() []jobs.Record {
	f.snapshots.Add(1)
	return append([]jobs.Record(nil), f.records...)
}
func (f *fakeJobs) Get(id, workspace string) (jobs.Record, bool) {
	for _, r := range f.records {
		if r.ID == id && r.WorkspaceID == workspace {
			return r, true
		}
	}
	return jobs.Record{}, false
}
func (f *fakeJobs) OutputMetadata(r jobs.Record) map[string]any {
	return map[string]any{"output_available": !r.OutputExpired, "available_stdout_bytes": int64(8), "available_stderr_bytes": int64(0)}
}
func (f *fakeJobs) ReadTail(ctx context.Context, id, workspace string, maxBytes int) (jobs.Output, error) {
	f.reads.Add(1)
	if _, ok := f.Get(id, workspace); !ok {
		return jobs.Output{}, errors.New("unknown job")
	}
	result := f.tails[id]
	truncate := func(s string) string {
		if len(s) > maxBytes {
			result.Truncated = true
			return s[len(s)-maxBytes:]
		}
		return s
	}
	result.Stdout = truncate(result.Stdout)
	result.Stderr = truncate(result.Stderr)
	return result, nil
}
func testHandler(t *testing.T, extra func(*Options)) *Handler {
	t.Helper()
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	journal, e := audit.Open(audit.Options{Directory: t.TempDir(), Enabled: true, Clock: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	options := Options{Audit: journal, Clock: func() time.Time { return now }, Resolver: &fakeResolver{projects: []wire.Project{{ID: "alpha", Label: "Alpha", Root: t.TempDir()}}}, Git: func(context.Context, wire.Project, bool) (map[string]any, error) {
		return map[string]any{"available": true, "dirty": false, "head": "0123456789ab", "branch": "main", "trackedChangedPaths": []string{}, "untrackedPaths": []string{}, "additions": 0, "deletions": 0, "diffAvailable": false}, nil
	}}
	if extra != nil {
		extra(&options)
	}
	h := New(options)
	t.Cleanup(func() { h.Close(); journal.Close() })
	return h
}
func record(t *testing.T, h *Handler, tool, request string, args, result map[string]any) string {
	t.Helper()
	r, e := h.options.Audit.Record(context.Background(), audit.RecordInput{ToolName: tool, Args: args, Result: wire.Success(tool, tool, result, ""), Scope: wire.Scope{PrincipalID: "operator", TransportID: "http-test", RequestID: request}, Target: &audit.Target{Scope: "project", ProjectID: "alpha", WorkspaceID: "ws_alpha"}, StartedAt: h.options.Clock().Add(-time.Second), FinishedAt: h.options.Clock(), Mutating: tool == "bash"})
	if e != nil {
		t.Fatal(e)
	}
	return r.ActionID
}
func TestReadOnlyMonitoringUsesNoGitOrWorkspaceMutation(t *testing.T) {
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	stdout, stderr := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	os.WriteFile(stdout, []byte("abcdefgh"), 0600)
	os.WriteFile(stderr, []byte("ijklmnop"), 0600)
	os.Chtimes(stdout, now.Add(-time.Minute), now.Add(-time.Minute))
	os.Chtimes(stderr, now.Add(-time.Minute), now.Add(-time.Minute))
	store := &fakeJobs{records: []jobs.Record{{ID: "job_1234abcd", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "running", Origin: "bash", Command: "echo running", Cwd: ".", StartedAt: timestamp(now.Add(-time.Minute)), StartedAtMS: now.Add(-time.Minute).UnixMilli(), DeadlineMS: now.Add(time.Minute).UnixMilli(), StdoutPath: stdout, StderrPath: stderr}}, tails: map[string]jobs.Output{"job_1234abcd": {Stdout: "abcdefgh", Stderr: "ijklmnop", StdoutBytes: 8, StderrBytes: 8}}}
	before := append([]jobs.Record(nil), store.records...)
	var gitCalls atomic.Int64
	h := testHandler(t, func(o *Options) {
		o.Jobs = store
		o.Git = func(context.Context, wire.Project, bool) (map[string]any, error) {
			gitCalls.Add(1)
			return nil, errors.New("JSON monitor cannot inspect Git")
		}
	})
	global, e := h.CollectJSON(context.Background(), JSONOptions{Limit: 8, OutputBytes: 4, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	if len(array(global["projects"])) != 1 || store.reads.Load() != 0 {
		t.Fatalf("global projection read output: %v", global)
	}
	selected, e := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Limit: 8, OutputBytes: 4, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	project := object(selected["project"])
	active := array(object(project["inflight"])["jobs"])
	if len(active) != 1 {
		t.Fatalf("jobs=%v", active)
	}
	output := object(object(active[0])["output"])
	if number(output["returned_bytes"]) != 4 || !boolean(output["truncated"]) || boolean(output["complete"]) {
		t.Fatalf("tail budget incorrect: %+v", output)
	}
	if !boolean(project["review_recommended"]) || project["has_inflight_work"] != true {
		t.Fatalf("quiet signal missing: %+v", project)
	}
	if gitCalls.Load() != 0 || h.options.Resolver.(*fakeResolver).resolves.Load() != 0 || !reflect.DeepEqual(before, store.records) {
		t.Fatal("monitor caused a stateful operation")
	}
}
func TestRecentCommandsFoldCompletionAndMergeRetainedJobs(t *testing.T) {
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	exit := 0
	store := &fakeJobs{records: []jobs.Record{{ID: "job_1234abcd", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "succeeded", Command: "echo done", StartedAt: timestamp(now.Add(-time.Second)), FinishedAt: timestamp(now), StartedAtMS: now.Add(-time.Second).UnixMilli(), FinishedAtMS: now.UnixMilli(), ExitCode: &exit}, {ID: "job_ffffabcd", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "succeeded", Command: "echo from-store", StartedAt: timestamp(now.Add(-2 * time.Second)), FinishedAt: timestamp(now.Add(-time.Second)), StartedAtMS: now.Add(-2 * time.Second).UnixMilli(), FinishedAtMS: now.Add(-time.Second).UnixMilli(), ExitCode: &exit}}, tails: map[string]jobs.Output{}}
	h := testHandler(t, func(o *Options) { o.Jobs = store })
	args := map[string]any{"command": "echo done", "workspace_id": "ws_alpha"}
	result := map[string]any{"job_id": "job_1234abcd", "job_status": "running", "exit_code": nil}
	record(t, h, "bash", "initial", args, result)
	record(t, h, "bash_job", "completion", args, map[string]any{"job_id": "job_1234abcd", "job_status": "succeeded", "exit_code": 0})
	packet, e := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Limit: 8, OutputBytes: 0, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	recent := array(object(packet["project"])["recent_commands"])
	if len(recent) != 2 {
		t.Fatalf("completion duplicated invocation: %+v", recent)
	}
	if object(recent[0])["tool"] != "bash_job" || object(recent[1])["source"] != "job_store" {
		t.Fatalf("wrong receipt ordering/merge: %+v", recent)
	}
	if store.reads.Load() != 0 {
		t.Fatal("output_bytes=0 read output")
	}
}
func TestArchiveRunProjectionAndAmbiguousReference(t *testing.T) {
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	runs := []map[string]any{
		{"id": "run_abcdefgh1111", "project_id": "alpha", "mode": "ralph", "state": "blocked", "revision": 3, "title": "Historical run", "updated_at": timestamp(now.Add(-time.Hour)), "iteration_id": "iteration-old", "workspace": map[string]any{"id": "ws_archive"}, "todos": []any{map[string]any{"id": "a", "title": "Pending", "status": "blocked"}, map[string]any{"id": "b", "title": "Done", "status": "done"}}},
		{"id": "run_abcdefgh2222", "project_id": "alpha", "mode": "manual", "state": "complete", "revision": 4, "title": "Completed", "updated_at": timestamp(now), "todos": []any{}, "completion": map[string]any{"completed_at": timestamp(now), "spec_revision": 2, "evidence_ids": []string{"evidence-one"}}},
	}
	var counts atomic.Int64
	h := testHandler(t, func(o *Options) {
		o.Work = &WorkHistory{Runs: func(context.Context, string) ([]map[string]any, error) { return runs, nil }, Iteration: func(context.Context, string) (map[string]any, error) {
			return map[string]any{"last_contact_at": timestamp(now.Add(-time.Hour)), "last_progress_at": timestamp(now.Add(-2 * time.Hour))}, nil
		}, OperationCount: func(context.Context, string) (int, error) { counts.Add(1); return 2, nil }}
	})
	packet, e := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", RunID: "run_abcdefgh1111", Limit: 8, OutputBytes: 0, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	selected := object(packet["run"])
	if selected["unresolved_operations"] != 2 || object(selected["todos"])["unfinished_total"] != 1 {
		t.Fatalf("archive packet=%v", selected)
	}
	if len(array(object(packet["project"])["work_runs"])) != 1 {
		t.Fatal("terminal run appeared in nonterminal summary")
	}
	_, e = h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", RunID: "run_abcdefgh", Limit: 8, OutputBytes: 0, QuietAfterMS: 30000})
	var typed *HTTPError
	if !errors.As(e, &typed) || typed.Code != "ambiguous_run" || typed.Status != 409 {
		t.Fatalf("ambiguous=%v", e)
	}
	if counts.Load() != 1 {
		t.Fatal("ambiguous lookup computed or mutated a run")
	}
}
func TestHTTPValidationAndCompleteDashboardSections(t *testing.T) {
	h := testHandler(t, nil)
	cases := []struct {
		path     string
		status   int
		contains string
	}{
		{"/activity.json?project_id=", 400, "invalid_project_id"},
		{"/activity.json?run_id=run_abcdefgh", 400, "invalid_run_id"},
		{"/activity.json?limit=0", 400, "invalid_options"},
		{"/activity.json?limit=1&limit=2", 400, "invalid_options"},
		{"/activity.json?output_bytes=-1", 400, "invalid_options"},
		{"/activity/projects/missing.json", 404, "unknown_project"},
		{"/activity/data?before_sequence=0", 400, "invalid_dashboard_options"},
		{"/activity?project_id=missing", 400, "Invalid history cursor"},
		{"/activity/data?before_sequence=0x2", 200, "generatedAt"},
		{"/activity/live?project_id=missing", 200, "No calls or jobs in flight"},
		{"/activity/job?job_id=invalid&workspace_id=w", 404, "Job output unavailable"},
		{"/activity/diff?project_id=missing", 404, "Unknown project"},
		{"/activity/batch?project_id=alpha", 400, "saved batch path"},
	}
	for _, test := range cases {
		t.Run(test.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
			if rec.Code != test.status || !strings.Contains(rec.Body.String(), test.contains) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity/data", nil))
	var data DashboardData
	if json.Unmarshal(rec.Body.Bytes(), &data) != nil {
		t.Fatal(rec.Body.String())
	}
	if len(data.Sections) != 7 {
		t.Fatalf("missing sections %+v", data)
	}
	for _, section := range data.Sections {
		if !strings.Contains(section.HTML, `data-dashboard-section="`+section.Key+`"`) {
			t.Fatalf("refresh section boundary missing: %s", section.Key)
		}
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/activity", nil))
	for _, needle := range []string{"data-project-filter", "data-refresh", "codexpro.activity.credential", "reconcileDashboardElement", "/usage/v1/pro", "/inbox/v1/items", "refreshLive", "window.setInterval"} {
		if !strings.Contains(page.Body.String(), needle) {
			t.Fatalf("functional page missing %q", needle)
		}
	}
	if page.Header().Get("Content-Security-Policy") != dashboardCSP || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing dashboard HTTP policy")
	}
}
func TestGitCacheCoalescesConcurrentReaders(t *testing.T) {
	var reads atomic.Int64
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixMilli())
	h := testHandler(t, func(o *Options) {
		o.Clock = func() time.Time { return time.UnixMilli(clock.Load()) }
		o.Git = func(ctx context.Context, p wire.Project, diff bool) (map[string]any, error) {
			reads.Add(1)
			select {
			case <-time.After(5 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return map[string]any{"available": true, "dirty": false}, nil
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := h.CollectDashboard(context.Background(), "", 0); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if reads.Load() != 1 {
		t.Fatalf("concurrent readers spawned %d Git snapshots", reads.Load())
	}
	clock.Add(10001)
	if _, e := h.CollectDashboard(context.Background(), "", 0); e != nil {
		t.Fatal(e)
	}
	if reads.Load() != 2 {
		t.Fatalf("TTL did not refresh: %d", reads.Load())
	}
}
func TestHTMLContainsEscapedEvidenceAndLazyBatchDetails(t *testing.T) {
	h := testHandler(t, func(o *Options) {
		o.Resolver = &fakeResolver{projects: []wire.Project{{ID: "alpha", Label: "<img src=x onerror=alert(1)>", Root: t.TempDir()}}}
		o.SafePath = func(s string) bool { return !strings.Contains(s, "blocked") }
	})
	record(t, h, "batch", "batch", map[string]any{"workspace_id": "ws_alpha", "mode": "sequence", "operations": []any{}}, map[string]any{"batch_path": ".codexpro/batches/example.json", "operation_count": 1, "succeeded": false, "failed_count": 1, "results": []any{map[string]any{"id": "operation", "tool": "write", "ok": false, "structured": map[string]any{"path": "a.txt"}}}, "error": "<script>alert(2)</script>"})
	snapshot, e := h.CollectDashboard(context.Background(), "", 0)
	if e != nil {
		t.Fatal(e)
	}
	page := RenderDashboardPage(snapshot)
	if strings.Contains(page, "<img src=x") || strings.Contains(page, "<script>alert(2)</script>") {
		t.Fatal("unescaped dynamic HTML")
	}
	for _, needle := range []string{"&lt;img src=x", "&lt;script&gt;alert(2)", "data-batch-href", "Historical operation outcomes", "operation", "batch.execute"} {
		if !strings.Contains(page, needle) {
			t.Fatalf("missing useful evidence %q", needle)
		}
	}
	batch := RenderBatchFragment(map[string]any{"definition": map[string]any{"mode": "sequence", "operations": []any{map[string]any{"id": "x", "tool": "write", "args": map[string]any{"content": "</pre><script>bad</script>"}}}}})
	if strings.Contains(batch, "<script>bad</script>") || !strings.Contains(batch, "&lt;/pre&gt;") {
		t.Fatal("saved batch content was not escaped")
	}
}
func TestMetricsAndTimelineRespectRetainedSample(t *testing.T) {
	now := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	views := []map[string]any{}
	for i, tool := range []string{"read", "write", "bash_job", "read"} {
		views = append(views, map[string]any{"actionId": fmt.Sprint(i), "toolName": tool, "operationClass": "read", "status": "succeeded", "durationMs": float64(10 * (i + 1)), "finishedAt": timestamp(now.Add(-time.Duration(i) * time.Minute)), "projectId": "alpha", "projectLabel": "Alpha", "attribution": "recorded", "sequence": i + 1})
	}
	views[1]["status"] = "failed"
	views[3]["finishedAt"] = timestamp(now.Add(-2 * time.Hour))
	metrics := collectMetrics(views, now, "alpha")
	if metrics["count"] != 2 || metrics["failedCount"] != 1 || number(metrics["meanLatencyMs"]) != 15 || number(metrics["medianLatencyMs"]) != 15 {
		t.Fatalf("metrics=%+v", metrics)
	}
	timeline := buildTimeline(views, now.UnixMilli(), 2)
	if timeline == nil || timeline.ActionCount != 2 || len(timeline.Lanes) != 1 || timeline.Lanes[0].Total != 2 {
		t.Fatalf("lane bound=%+v", timeline)
	}
	if !strings.Contains(renderTimeline(timeline, "sample"), "data-bin-summary") {
		t.Fatal("timeline lacks accessible details")
	}
}
func TestSplitDiffPairsReplacementsAndEscapesText(t *testing.T) {
	view := map[string]any{"diff": "diff --git a/a.txt b/a.txt\nindex abc..def 100644\n--- a/a.txt\n+++ b/a.txt\n@@ -2,2 +2,3 @@\n-<old>\n+<new>\n+extra\n same\n", "truncated": true}
	parsed := parseUnifiedDiff(stringValue(view["diff"]))
	if len(parsed) != 1 || len(parsed[0].Hunks) != 1 || len(parsed[0].Hunks[0].Rows) != 3 {
		t.Fatalf("diff model %+v", parsed)
	}
	rows := parsed[0].Hunks[0].Rows
	if rows[0].Before.Line != 2 || rows[0].After.Line != 2 || rows[1].Before.Tone != "empty" || rows[2].After.Line != 4 {
		t.Fatalf("line pairing %+v", rows)
	}
	html := RenderDiff(view)
	if strings.Contains(html, "<old>") || !strings.Contains(html, "&lt;new&gt;") || !strings.Contains(html, "diff output truncated") {
		t.Fatal(html)
	}
}
