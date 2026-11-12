package activity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
)

func TestCapacityQueuedJobIsPendingWithoutRunningOrTerminalClaims(t *testing.T) {
	store := &fakeJobs{}
	h := testHandler(t, func(o *Options) { o.Jobs = store })
	now := h.options.Clock()
	start := now.Add(-time.Minute)
	q := jobs.Record{ID: "job_1234abcd", Agent: "SP5", ProjectID: "alpha", WorkspaceID: "ws_alpha", Status: "queued", Origin: "background", Command: "printf pending", Cwd: ".", StartedAt: timestamp(start), StartedAtMS: start.UnixMilli(), QueueDeadlineMS: now.Add(time.Hour).UnixMilli()}
	store.records = []jobs.Record{q}
	status := h.CollectStatus(context.Background())
	if !status.Activity.Known {
		t.Fatal("queued owner made activity unknown")
	}
	agent := status.Activity.Agents[4]
	if agent.InFlightJobs != 0 || agent.QueuedJobs != 1 {
		t.Fatalf("queued work claimed a running process: %+v", agent)
	}
	live := h.CollectLive("alpha", "SP5")
	if len(array(live["jobs"])) != 1 {
		t.Fatal("pending job disappeared from live owner view")
	}
	html := renderLive(live)
	for _, want := range []string{"0 running jobs · 1 queued jobs", ">Queued</span>", "queue deadline", "have not started"} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing pending distinction %q", want)
		}
	}
	if len(array(h.CollectLive("alpha", "SP1")["jobs"])) != 0 {
		t.Fatal("agent filter lost")
	}
	packet, err := h.CollectJSON(context.Background(), JSONOptions{ProjectID: "alpha", Limit: 8, OutputBytes: 1024, QuietAfterMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	project := object(packet["project"])
	counts := object(project["inflight_counts"])
	inflight := object(project["inflight"])
	if counts["jobs"] != 0 || counts["queued_jobs"] != 1 || len(array(inflight["jobs"])) != 0 {
		t.Fatalf("pending work classified as running: %#v", project)
	}
	pending := array(inflight["queued_jobs"])
	if len(pending) != 1 {
		t.Fatal("missing queued JSON projection")
	}
	output := object(object(pending[0])["output"])
	if output["complete"] != false || output["reason"] != "not_started" {
		t.Fatalf("queued output claimed complete: %#v", output)
	}
	for _, raw := range array(project["recent_commands"]) {
		if object(raw)["job_id"] == q.ID {
			t.Fatal("queued job appeared as terminal")
		}
	}
	encoded, _ := json.Marshal(project["signals"])
	if strings.Contains(string(encoded), "job_past_deadline") || strings.Contains(string(encoded), "job_output_quiet") {
		t.Fatalf("queued wait fabricated a running-process signal: %s", encoded)
	}
	if store.reads.Load() != 0 {
		t.Fatal("read never-started job output")
	}
}
