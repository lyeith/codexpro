package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestCollectorProjectsOnlyFixedLabelScalarsAcrossAllProjects(t *testing.T) {
	store := &fakeJobs{}
	h := testHandler(t, func(o *Options) {
		o.Jobs = store
		o.Work = &WorkHistory{Runs: func(context.Context, string) ([]map[string]any, error) {
			t.Fatal("collector consulted unlabelled work contacts")
			return nil, nil
		}}
	})
	now := h.options.Clock()
	for _, project := range []string{"alpha-private", "other-private", ""} {
		h.options.Audit.Begin(audit.RecordInput{ToolName: "bash", Scope: wire.Scope{Agent: "SP1"},
			Target:    &audit.Target{ProjectID: project, WorkspaceID: "workspace-private"},
			StartedAt: now.Add(-4 * time.Second), Args: map[string]any{"command": "PRIVATE-COMMAND"}})
	}
	h.options.Audit.Begin(audit.RecordInput{ToolName: "read", Scope: wire.Scope{Agent: "SP2"}, StartedAt: now.Add(-5 * time.Second)})
	h.options.Audit.Begin(audit.RecordInput{ToolName: "read", Scope: wire.Scope{Agent: "SP-1"}, StartedAt: now})
	h.options.Audit.Begin(audit.RecordInput{ToolName: "read", Scope: wire.Scope{}, StartedAt: now})
	store.records = []jobs.Record{
		{ID: "job_private", ProjectID: "foreign-private", Agent: "SP1", Status: "running", StartedAt: timestamp(now.Add(-3 * time.Second)), Command: "PRIVATE-COMMAND"},
		{ID: "job_finished", ProjectID: "foreign-private", Agent: "SP1", Status: "succeeded", StartedAt: timestamp(now.Add(-2 * time.Second)), FinishedAt: timestamp(now.Add(-time.Second))},
		{ID: "job_unlabelled", Status: "running", StartedAt: timestamp(now)},
		{ID: "job_outside", Agent: "SP10", Status: "running", StartedAt: timestamp(now)},
	}
	record(t, h, "bash", "private-action", map[string]any{"agent": "SP1", "command": "PRIVATE-COMMAND"}, map[string]any{"stdout": "PRIVATE-OUTPUT"})
	got := h.CollectStatus(context.Background())
	if !got.Activity.Known || got.Activity.Scope != "all_projects" || len(got.Activity.Agents) != 9 {
		t.Fatal("fixed all-project coverage missing")
	}
	one := got.Activity.Agents[0]
	if one.Label != "SP1" || one.InFlightCalls != 3 || one.InFlightJobs != 1 ||
		one.LastActivityAt == nil || *one.LastActivityAt != "2026-10-31T16:00:00.000Z" {
		t.Fatalf("SP1 projection: %+v", one)
	}
	two := got.Activity.Agents[1]
	if two.Label != "SP2" || two.InFlightCalls != 1 || two.InFlightJobs != 0 ||
		two.LastActivityAt == nil || *two.LastActivityAt != "2026-10-31T15:59:55.000Z" {
		t.Fatalf("SP2 projection: %+v", two)
	}
	for _, agent := range got.Activity.Agents[2:] {
		if agent.InFlightCalls != 0 || agent.InFlightJobs != 0 || agent.LastActivityAt != nil {
			t.Fatal("unrelated labels changed empty observation")
		}
	}
	body, err := json.Marshal(got)
	if err != nil || len(body) > 16<<10 {
		t.Fatal("projection exceeded JSON contract")
	}
	for _, secret := range []string{"private", "PRIVATE", "job_", "workspace", "command", "principal", "credential", "output"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("projection leaked field or value %s", secret)
		}
	}
	if store.reads.Load() != 0 || store.snapshots.Load() != 1 {
		t.Fatal("collector read output or duplicated job acquisition")
	}
	for _, method := range []string{"GET", "HEAD"} {
		rec := httptest.NewRecorder()
		h.serveCollector(rec, httptest.NewRequest(method, "/status/collector", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("collector HTTP contract missing")
		}
		if method == "HEAD" && rec.Body.Len() != 0 {
			t.Fatal("HEAD emitted a body")
		}
	}
}

func TestCollectorUnknownDoesNotInventZeroActivity(t *testing.T) {
	h := testHandler(t, func(o *Options) { o.Jobs = &fakeJobs{} })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, got := range []CollectorStatus{h.CollectStatus(ctx), func() CollectorStatus {
		h.options.Jobs = nil
		return h.CollectStatus(context.Background())
	}()} {
		if got.Activity.Known || got.Activity.Agents != nil {
			t.Fatal("unavailable acquisition became zero activity")
		}
	}
	h.options.Jobs = &fakeJobs{records: []jobs.Record{{Agent: "SP1", Status: "running", StartedAt: "invalid"}}}
	if got := h.CollectStatus(context.Background()); got.Activity.Known || got.Activity.Agents != nil {
		t.Fatal("malformed source became a healthy observation")
	}
	off := New(Options{Jobs: &fakeJobs{}})
	defer off.Close()
	if got := off.CollectStatus(context.Background()); got.Activity.Known {
		t.Fatal("disabled journal was reported as empty")
	}
}
