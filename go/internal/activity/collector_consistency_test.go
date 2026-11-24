package activity

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
)

type interleavedCollectorJobs struct {
	legacyCollectorJobs
	duringRead func()
}

func (s *interleavedCollectorJobs) AgentActivity(label string) (jobs.ActivityCounts, bool) {
	if s.duringRead != nil {
		s.duringRead()
	}
	return s.legacyCollectorJobs.AgentActivity(label)
}

func TestCollectorDoesNotCombineDifferentInstantsIntoIdle(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics := NewMetrics(clock)
	metrics.begin("FP5", "read", now)(false) // Prior success must not hide active work.
	source := &interleavedCollectorJobs{legacyCollectorJobs: legacyCollectorJobs{
		activity: map[string]jobs.ActivityCounts{"FP5": {InFlightJobs: 1, LastActivityAt: now}},
	}}
	finishes := []func(bool){}
	defer func() {
		for _, finish := range finishes {
			finish(false)
		}
	}()
	source.duringRead = func() {
		// Work remains continuously active: a new call starts before the old job
		// finishes, exactly between the collector's two owner acquisitions.
		finishes = append(finishes, metrics.begin("FP5", "bash", now))
		source.activity["FP5"] = jobs.ActivityCounts{LastActivityAt: now, Revision: 2}
	}
	h := New(Options{Metrics: metrics, Jobs: source, Clock: clock})
	defer h.Close()
	value := h.CollectAgentStatus(context.Background(), "FP5")
	if value.Activity.Known && value.Activity.Agents[0].InFlightCalls == 0 && value.Activity.Agents[0].InFlightJobs == 0 {
		t.Fatal("collector combined different instants into false idle")
	}
	source.duringRead = nil
	known := h.CollectAgentStatus(context.Background(), "FP5")
	if !known.Activity.Known || known.Activity.Agents[0].InFlightCalls == 0 {
		t.Fatal("stable subsequent read did not report active work")
	}
}

func TestCollectorRejectsCallWallTimeAfterObservation(t *testing.T) {
	at := metricTestStart()
	metrics := NewMetrics(func() time.Time { return at })
	metrics.begin("FP5", "read", at)(false)
	// Model a backward wall adjustment independently of the metrics monotonic
	// elapsed clock. Counter equality alone must not validate future timestamps.
	h := New(Options{Metrics: metrics, Jobs: &legacyCollectorJobs{}, Clock: func() time.Time { return at.Add(-time.Second) }})
	defer h.Close()
	value := h.CollectAgentStatus(context.Background(), "FP5")
	if value.Activity.Known || value.Activity.Agents != nil {
		t.Fatal("future call timestamp became valid completion evidence")
	}
}

func TestCollectorLabelCapacityWakesUnknownWaiter(t *testing.T) {
	now := metricTestStart()
	metrics := NewMetrics(func() time.Time { return now })
	// Populate only scalar state; the bound applies to lifetime labels rather
	// than to the ordinary six-hour dashboard's cells.
	for n := 0; n < maxMetricCells; n++ {
		metrics.collectorCalls[fmt.Sprintf("%04x", n)] = &collectorCalls{}
	}
	_, _, known, changed := metrics.collectorSnapshot("FP5")
	if !known {
		t.Fatal("unused label should have a valid baseline before overflow")
	}
	metrics.begin("FP5", "read", now)(false)
	select {
	case <-changed:
	default:
		t.Fatal("capacity overflow did not wake a waiting collector")
	}
	_, _, known, _ = metrics.collectorSnapshot("FP5")
	if known {
		t.Fatal("untracked label became known zero after overflow")
	}
	if len(metrics.collectorCalls) != maxMetricCells {
		t.Fatal("counter labels exceeded their bound")
	}
	_, _, known, _ = metrics.collectorSnapshot("0000")
	if !known {
		t.Fatal("overflow invalidated an already tracked label")
	}
}

func TestCollectorQueryRejectsAmbiguousAndOversizedParameters(t *testing.T) {
	for _, query := range []string{
		"agent=FP5&since_completed=18446744073709551616",
		"agent=FP5&since_completed=1.0", "agent=FP5&since_completed=%2B1",
		"agent=FP5&wait=-1s&since_completed=0", "agent=FP5&wait=31s&since_completed=0",
		"agent=FP5&since_completed=0&since_revision=0", "agent=FP5&epoch=bad-token",
		"agent=FP5&wait=1s", "agent=FP5&token=private", "agent=FP5&agent=SP1",
		"agent=FP5&unused=" + strings.Repeat("x", 600),
	} {
		if _, err := ParseCollectorQuery(&url.URL{RawQuery: query}); err == nil {
			t.Fatalf("invalid collector query accepted: %s", query)
		}
	}
	request := httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=20s&since_revision=1", nil)
	if _, err := ParseCollectorQuery(request.URL); err != nil {
		t.Fatal(err)
	}
}
