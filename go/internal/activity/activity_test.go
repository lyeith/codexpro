package activity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	r, e := h.options.Audit.Record(context.Background(), audit.RecordInput{ToolName: tool, Args: args, Result: wire.Success(tool, tool, result, ""), Scope: wire.Scope{PrincipalID: "operator", TransportID: "http-test", RequestID: request, Agent: stringValue(args["agent"])}, Target: &audit.Target{Scope: "project", ProjectID: "alpha", WorkspaceID: "ws_alpha"}, StartedAt: h.options.Clock().Add(-time.Second), FinishedAt: h.options.Clock(), Mutating: tool == "bash"})
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
func TestHTTPValidationAndAgentDashboardSection(t *testing.T) {
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
		{"/activity/data?before_sequence=2", 200, "generatedAt"},
		{"/activity/data?before_sequence=9007199254740991", 200, "generatedAt"},
		{"/activity/data?before_sequence=9007199254740992", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=9223372036854775808", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=1&before_sequence=2", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=0x2", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=0b10", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=0o2", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=2e0", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=2.0", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=%2B2", 400, "invalid_dashboard_options"},
		{"/activity/data?before_sequence=%202", 400, "invalid_dashboard_options"},
		{"/activity?before_sequence=-1", 400, "Invalid history cursor"},
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
	if len(data.Sections) != 1 || data.Sections[0].Key != "agents" {
		t.Fatalf("expected one metadata-only agent section: %+v", data)
	}
	for _, section := range data.Sections {
		if !strings.Contains(section.HTML, `data-dashboard-section="`+section.Key+`"`) {
			t.Fatalf("refresh section boundary missing: %s", section.Key)
		}
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/activity", nil))
	for _, needle := range []string{"Active agents · last 6 hours", "data-agents-refresh", "codexpro.activity.credential", "refreshAgents", "/activity/data", "window.setInterval"} {
		if !strings.Contains(page.Body.String(), needle) {
			t.Fatalf("functional page missing %q", needle)
		}
	}
	if page.Header().Get("Content-Security-Policy") != dashboardCSP || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing dashboard HTTP policy")
	}
}
func TestSavedBatchFragmentEscapesContent(t *testing.T) {
	batch := RenderBatchFragment(map[string]any{"definition": map[string]any{"mode": "sequence", "operations": []any{map[string]any{"id": "x", "tool": "write", "args": map[string]any{"content": "</pre><script>bad</script>"}}}}})
	if strings.Contains(batch, "<script>bad</script>") || !strings.Contains(batch, "&lt;/pre&gt;") {
		t.Fatal("saved batch content was not escaped")
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
