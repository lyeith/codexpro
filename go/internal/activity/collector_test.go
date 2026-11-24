package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
)

type legacyCollectorJobs struct {
	fakeJobs
	activity map[string]jobs.ActivityCounts
	unknown  bool
}

func (s *legacyCollectorJobs) AgentActivity(label string) (jobs.ActivityCounts, bool) {
	return s.activity[label], !s.unknown
}
func (s *legacyCollectorJobs) ActivityChanged() <-chan struct{} { return nil }

func collectorKeys(t *testing.T, value map[string]any, keys ...string) {
	t.Helper()
	if len(value) != len(keys) {
		t.Fatalf("collector keys: %+v, want %v", value, keys)
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing collector key %s", key)
		}
	}
}

func TestCollectorPreservesExactLegacyMonitorShape(t *testing.T) {
	now := metricTestStart()
	metrics := NewMetrics(func() time.Time { return now })
	store := &legacyCollectorJobs{activity: map[string]jobs.ActivityCounts{
		"SP1": {InFlightJobs: 2, QueuedJobs: 3, LastActivityAt: now},
		"FP5": {InFlightJobs: 10, LastActivityAt: now},
	}}
	h := New(Options{Metrics: metrics, Jobs: store, Clock: func() time.Time { return now },
		Work: &WorkHistory{Runs: func(context.Context, string) ([]map[string]any, error) {
			t.Fatal("collector consulted work history")
			return nil, nil
		}}})
	defer h.Close()
	metrics.begin("SP1", "read", now)
	metrics.begin("SP2", "write", now)(false)
	metrics.begin("FP5", "bash", now)
	rec := httptest.NewRecorder()
	h.serveCollector(rec, httptest.NewRequest("GET", "/status/collector", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() > 16<<10 ||
		rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("legacy HTTP contract: %d %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	collectorKeys(t, got, "version", "observed_at", "health", "activity")
	if got["version"] != float64(1) {
		t.Fatal("legacy version changed")
	}
	collectorKeys(t, got["health"].(map[string]any), "known", "ok")
	activity := got["activity"].(map[string]any)
	collectorKeys(t, activity, "known", "scope", "agents")
	if activity["known"] != true || activity["scope"] != "all_projects" {
		t.Fatalf("legacy activity unavailable: %+v", activity)
	}
	agents := activity["agents"].([]any)
	if len(agents) != 9 {
		t.Fatal("legacy collector no longer has nine labels")
	}
	for i, raw := range agents {
		agent := raw.(map[string]any)
		collectorKeys(t, agent, "label", "inflight_calls", "inflight_jobs", "last_activity_at")
		if agent["label"] != "SP"+string(rune('1'+i)) {
			t.Fatal("legacy labels changed")
		}
		if i == 0 && (agent["inflight_calls"] != float64(1) || agent["inflight_jobs"] != float64(5)) {
			t.Fatal("queued work became idle or current work was lost")
		}
		if i >= 2 && (agent["inflight_calls"] != float64(0) || agent["inflight_jobs"] != float64(0) || agent["last_activity_at"] != nil) {
			t.Fatal("unrelated labels changed legacy zero observation")
		}
	}
	if store.reads.Load() != 0 || store.snapshots.Load() != 0 {
		t.Fatal("collector read output or scanned job history")
	}
	head := httptest.NewRecorder()
	h.serveCollector(head, httptest.NewRequest("HEAD", "/status/collector", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatal("legacy HEAD contract changed")
	}
}

func TestCollectorUnknownDoesNotInventZeroActivity(t *testing.T) {
	now := metricTestStart()
	metrics := NewMetrics(func() time.Time { return now })
	store := &legacyCollectorJobs{}
	h := New(Options{Metrics: metrics, Jobs: store, Clock: func() time.Time { return now }})
	defer h.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := h.CollectStatus(ctx); got.Activity.Known || got.Activity.Agents != nil {
		t.Fatal("cancelled acquisition became zero activity")
	}
	store.unknown = true
	if got := h.CollectStatus(context.Background()); got.Activity.Known || got.Activity.Agents != nil {
		t.Fatal("invalid job source became zero activity")
	}
	rec := httptest.NewRecorder()
	h.serveCollector(rec, httptest.NewRequest("GET", "/status/collector", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	collectorKeys(t, body["activity"].(map[string]any), "known", "scope")
	store.unknown = false
	store.activity = map[string]jobs.ActivityCounts{"SP1": {LastActivityAt: now.Add(time.Second)}}
	if got := h.CollectStatus(context.Background()); got.Activity.Known {
		t.Fatal("future job time became known activity")
	}
	h.options.Jobs = &fakeJobs{}
	if got := h.CollectStatus(context.Background()); got.Activity.Known {
		t.Fatal("missing job counters silently fell back to scanning")
	}
	h.options.Jobs, h.options.Metrics = store, nil
	if got := h.CollectStatus(context.Background()); got.Activity.Known {
		t.Fatal("missing call counters became zero activity")
	}
}

func TestCollectorCombinesRuntimeCallAndRecoveredJobTimes(t *testing.T) {
	now := metricTestStart()
	metrics := NewMetrics(func() time.Time { return now })
	store := &legacyCollectorJobs{activity: map[string]jobs.ActivityCounts{
		"SP2": {InFlightJobs: 1, LastActivityAt: now.Add(-2 * time.Second)},
		"SP3": {LastActivityAt: now.Add(-time.Second)},
	}}
	h := New(Options{Metrics: metrics, Jobs: store, Clock: func() time.Time { return now }})
	defer h.Close()
	metrics.begin("SP1", "read", now)
	got := h.CollectStatus(context.Background())
	if !got.Activity.Known {
		t.Fatal("current owner counters unavailable without audit history")
	}
	for i, at := range []time.Time{now, now.Add(-2 * time.Second), now.Add(-time.Second)} {
		if got.Activity.Agents[i].LastActivityAt == nil || *got.Activity.Agents[i].LastActivityAt != timestamp(at) {
			t.Fatalf("wrong activity time at label %d: %+v", i+1, got.Activity.Agents[i])
		}
	}
}
