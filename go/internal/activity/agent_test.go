package activity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func beginCall(h *Handler, agent string) {
	h.options.Audit.Begin(audit.RecordInput{ToolName: "read", Scope: wire.Scope{Agent: agent}, Target: &audit.Target{ProjectID: "alpha", WorkspaceID: "ws_alpha"}, StartedAt: h.options.Clock().Add(-time.Second)})
}

func TestAgentFilterScopesActivityBeforeLimitsAndCounts(t *testing.T) {
	store := &fakeJobs{tails: map[string]jobs.Output{}}
	h := testHandler(t, func(o *Options) { o.Jobs = store })
	now := h.options.Clock()
	for i := 0; i < 12; i++ {
		store.records = append(store.records, jobs.Record{ID: fmt.Sprintf("job_%08x", i), Agent: "SP-2", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "running", StartedAt: timestamp(now), StartedAtMS: now.UnixMilli()})
	}
	store.records = append(store.records,
		jobs.Record{ID: "job_aaaaaaaa", Agent: "SP-1", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "running", StartedAt: timestamp(now), StartedAtMS: now.UnixMilli()},
		jobs.Record{ID: "job_bbbbbbbb", Agent: "SP-1", WorkspaceID: "ws_alpha", ProjectID: "alpha", Status: "succeeded", StartedAt: timestamp(now.Add(-time.Second)), StartedAtMS: now.Add(-time.Second).UnixMilli(), FinishedAt: timestamp(now.Add(-time.Millisecond))})
	beginCall(h, "SP-1")
	beginCall(h, "SP-2")
	beginCall(h, "")
	labeled := record(t, h, "read", "labeled", map[string]any{"agent": "SP-1", "path": "a.txt"}, map[string]any{"bytes": 1})
	// Newer unrelated actions must not consume the JSON receipt scan or history
	// limits before the exact caller filter is applied.
	for i := 0; i < 35; i++ {
		record(t, h, "read", fmt.Sprint(i), map[string]any{"agent": "SP-2", "path": "b.txt"}, map[string]any{"bytes": 1})
	}
	packet, err := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Agent: "SP-1", Limit: 1, QuietAfterMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	p := object(packet["project"])
	counts, inflight := object(p["inflight_counts"]), object(p["inflight"])
	if counts["tool_calls"] != 1 || counts["jobs"] != 1 || inflight["truncated"] != false {
		t.Fatalf("counts were computed before filtering: counts=%v inflight=%v", counts, inflight)
	}
	for _, key := range []string{"calls", "jobs"} {
		views := array(inflight[key])
		if len(views) != 1 || object(views[0])["agent"] != "SP-1" {
			t.Fatalf("inflight %s=%v", key, views)
		}
	}
	recent := array(p["recent_commands"])
	if len(recent) != 1 || object(recent[0])["action_id"] != labeled || object(recent[0])["agent"] != "SP-1" {
		t.Fatalf("older matching receipt lost: %v", recent)
	}
	packet, err = h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Agent: "SP-1", Limit: 8, QuietAfterMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	recent = array(object(packet["project"])["recent_commands"])
	if len(recent) != 2 || object(recent[1])["source"] != "job_store" || object(recent[1])["agent"] != "SP-1" {
		t.Fatalf("retained job label lost: %v", recent)
	}
	live := h.CollectLive("", "SP-1")
	if len(array(live["calls"])) != 1 || len(array(live["jobs"])) != 1 || !strings.Contains(renderLive(live), `class="tool-badge agent-label"`) {
		t.Fatalf("live filter or label missing: %v", live)
	}
	snapshot, err := h.CollectDashboard(context.Background(), "alpha", "SP-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RecentActions) != 1 || snapshot.History["matchedCount"] != 1 || snapshot.Metrics["count"] != 1 || snapshot.Timeline.ActionCount != 1 || len(maps(snapshot.Projects[0]["actions"])) != 1 {
		t.Fatalf("dashboard sections disagree on label scope: %+v", snapshot)
	}
	page := RenderDashboardPage(snapshot)
	for _, needle := range []string{`data-agent-filter`, `value="SP-1"`, `agents SP-1`, `>SP-1</code>`} {
		if !strings.Contains(page, needle) {
			t.Fatalf("dashboard missing %q", needle)
		}
	}
}

func TestActivityWithoutAgentOmitsOptionalFields(t *testing.T) {
	h := testHandler(t, func(o *Options) {
		o.Jobs = &fakeJobs{records: []jobs.Record{{ID: "job_aaaaaaaa", ProjectID: "alpha", WorkspaceID: "ws_alpha", Status: "running", StartedAt: timestamp(o.Clock()), StartedAtMS: o.Clock().UnixMilli()}}, tails: map[string]jobs.Output{}}
	})
	beginCall(h, "")
	record(t, h, "read", "plain", map[string]any{"path": "a.txt"}, map[string]any{"bytes": 1})
	packet, err := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Limit: 8, QuietAfterMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	p := object(packet["project"])
	views := []any{array(p["recent_commands"])[0]}
	inflight := object(p["inflight"])
	views = append(views, array(inflight["calls"])...)
	views = append(views, array(inflight["jobs"])...)
	live := h.CollectLive("", "")
	views = append(views, array(live["calls"])...)
	views = append(views, array(live["jobs"])...)
	for _, view := range views {
		if _, exists := object(view)["agent"]; exists {
			t.Fatalf("unlabeled activity gained optional field: %v", view)
		}
	}
	for _, route := range []string{"/activity.json?agent=", "/activity/projects/alpha.json?agent=SP_1", "/activity/data?agent=SP-1&agent=SP-2", "/activity/live?agent=bad%20label", "/activity.json?agent=ABCDE", "/activity/live?agent=ABCDE", "/activity?agent=ABCDE"} {
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, route, nil))
		if res.Code != 400 || !strings.Contains(strings.ToLower(res.Body.String()), "agent") && !strings.Contains(res.Body.String(), "invalid_dashboard_options") {
			t.Fatalf("invalid filter accepted: %s => %d %s", route, res.Code, res.Body.String())
		}
	}
	for _, route := range []string{"/activity.json", "/activity/projects/alpha.json", "/activity/data", "/activity/live", "/activity"} {
		res := httptest.NewRecorder()
		h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, route+"?agent=SP-1", nil))
		if res.Code != 200 {
			t.Fatalf("four-character filter refused: %s => %d %s", route, res.Code, res.Body.String())
		}
	}
}

func TestAgentFilteredReceiptKeepsOtherCallersJobOutput(t *testing.T) {
	for _, tool := range []string{"jobs", "stop_jobs"} {
		t.Run(tool, func(t *testing.T) {
			store := &fakeJobs{records: []jobs.Record{{ID: "job_aaaaaaaa", Agent: "SP-2", ProjectID: "alpha", WorkspaceID: "ws_alpha", Status: "succeeded"}}, tails: map[string]jobs.Output{"job_aaaaaaaa": {Stdout: "retained output"}}}
			h := testHandler(t, func(o *Options) { o.Jobs = store })
			record(t, h, tool, "inspect-other-job", map[string]any{"agent": "SP-1", "job_ids": []string{"job_aaaaaaaa"}}, map[string]any{"jobs": []map[string]any{{"job_id": "job_aaaaaaaa", "agent": "SP-2", "status": "succeeded"}}})
			packet, err := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Agent: "SP-1", Limit: 8, OutputBytes: 1024, QuietAfterMS: 30000})
			if err != nil {
				t.Fatal(err)
			}
			p := object(packet["project"])
			recent := array(p["recent_commands"])
			if len(recent) != 1 || object(recent[0])["agent"] != "SP-1" {
				t.Fatalf("receipt must match its caller: %v", recent)
			}
			output := object(object(recent[0])["output"])
			if output["available"] != true || output["stdout"] != "retained output" || output["agent"] != "SP-2" {
				t.Fatalf("referenced output lost its owner or availability: %v", output)
			}
			if object(p["inflight_counts"])["jobs"] != 0 {
				t.Fatalf("inspected job appeared as caller-owned work: %v", p)
			}
		})
	}
}
