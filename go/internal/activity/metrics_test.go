package activity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func metricTestStart() time.Time {
	return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
}

func TestMetricsInclusiveStartWindowsAndOnceOnlyCompletion(t *testing.T) {
	base := metricTestStart()
	now := base
	m := NewMetrics(func() time.Time { return now })
	call := func(at time.Time, tool string, failed bool) {
		now = at
		m.begin("SP1", tool, now)(failed)
	}
	call(base.Add(time.Hour-time.Millisecond), "outside", false)
	call(base.Add(time.Hour), "read", false)
	call(base.Add(6*time.Hour-time.Millisecond), "search", true)
	now = base.Add(6 * time.Hour)
	finish := m.begin("SP1", "bash", now)
	call(base.Add(7*time.Hour), "read", false)

	got := m.Snapshot()
	if !got.Known || got.Partial || len(got.Agents) != 1 {
		t.Fatalf("expected a complete window: %+v", got)
	}
	a := got.Agents[0]
	if a.CallsSixHours != 4 || a.CallsLastHour != 2 || a.Errors != 1 || a.RunningCalls != 1 {
		t.Fatalf("inclusive start boundaries or running state: %+v", a)
	}
	if a.FirstCommandAt != "2026-11-06T01:00:00.000Z" || a.LastCommandAt != "2026-11-06T07:00:00.000Z" ||
		got.WindowStartedAt != "2026-11-06T01:00:00.000Z" {
		t.Fatalf("start timestamps changed: %+v", got)
	}
	if a.CallsPerHour == nil || math.Abs(*a.CallsPerHour-2.0/3.0) > 1e-12 {
		t.Fatalf("four calls over six hours must be 2/3 calls/hour: %v", a.CallsPerHour)
	}
	wantTools := []AgentToolTally{{Tool: "read", Count: 2}, {Tool: "bash", Count: 1}, {Tool: "search", Count: 1}}
	if !reflect.DeepEqual(a.Tools, wantTools) || got.Totals != (AgentTotals{ActiveAgents: 1, CallsLastHour: 2, CallsSixHours: 4, RunningCalls: 1}) {
		t.Fatalf("tool/totals mismatch: %+v", got)
	}
	finish(true)
	finish(true)
	finish(false)
	a = m.Snapshot().Agents[0]
	if a.CallsSixHours != 4 || a.Errors != 2 || a.RunningCalls != 0 {
		t.Fatalf("completion must only classify once, never add a call: %+v", a)
	}
}

func TestMetricsNaturalLabelsUntaggedAndZeroSpan(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	now = now.Add(6 * time.Hour)
	for _, label := range []string{"", "SP10", "sp1", "SP2", "SP1", "SP01"} {
		m.begin(label, "read", now)(false)
	}
	for _, tool := range []string{"write", "read", "bash"} {
		m.begin("SP2", tool, now)(false)
	}
	got := m.Snapshot()
	want := []string{"SP01", "SP1", "sp1", "SP2", "SP10", ""}
	if !got.Known || got.Partial || len(got.Agents) != len(want) {
		t.Fatalf("complete same-millisecond snapshot: %+v", got)
	}
	if got.Totals.ActiveAgents != 5 || got.Totals.CallsSixHours != 9 || got.Totals.CallsLastHour != 9 {
		t.Fatalf("untagged calls must count toward calls, but not active agents: %+v", got.Totals)
	}
	for i, label := range want {
		if got.Agents[i].Agent != label || got.Agents[i].CallsPerHour != nil {
			t.Fatalf("order, case identity, or zero-span rate at %d: %+v", i, got.Agents[i])
		}
	}
	if got.Agents[3].CallsSixHours != 4 ||
		!reflect.DeepEqual(got.Agents[3].Tools, []AgentToolTally{{Tool: "read", Count: 2}, {Tool: "bash", Count: 1}, {Tool: "write", Count: 1}}) {
		t.Fatalf("same-key starts must merge counts, not calls: %+v", got.Agents[3])
	}
	now = now.Add(time.Hour)
	got = m.Snapshot()
	if got.Partial || got.Agents[3].CallsPerHour == nil || *got.Agents[3].CallsPerHour != 4 {
		t.Fatalf("four calls since the first call one hour ago must be 4/hour, not a fixed six-hour rate: %+v", got.Agents[3])
	}
}

func TestMetricsOldRunningCallOnlyMarksEligibleAgent(t *testing.T) {
	base := metricTestStart()
	now := base
	m := NewMetrics(func() time.Time { return now })
	now = base.Add(time.Hour)
	oldEligible := m.begin("SP1", "bash", now)
	oldOnly := m.begin("SP9", "bash", now)
	now = base.Add(8 * time.Hour)
	m.begin("SP1", "read", now)(false)
	got := m.Snapshot()
	if !got.Known || got.Partial || len(got.Agents) != 1 || got.Agents[0].Agent != "SP1" ||
		got.Agents[0].CallsSixHours != 1 || got.Agents[0].CallsLastHour != 1 || got.Agents[0].RunningCalls != 1 ||
		!reflect.DeepEqual(got.Agents[0].Tools, []AgentToolTally{{Tool: "read", Count: 1}}) {
		t.Fatalf("old in-flight work changed window membership or totals: %+v", got)
	}
	oldEligible(true)
	oldOnly(true)
	got = m.Snapshot()
	if got.Agents[0].RunningCalls != 0 || got.Agents[0].Errors != 0 || m.cells != 1 {
		t.Fatalf("expired completion resurrected history: %+v cells=%d", got, m.cells)
	}
}

func TestMetricsStartupAndPressureCoverageAtExactBoundary(t *testing.T) {
	base := metricTestStart()
	now := base
	m := NewMetrics(func() time.Time { return now })
	m.maxCells = 2
	now = base.Add(time.Hour)
	evicted := m.begin("SP1", "read", now)
	if got := m.Snapshot(); !got.Known || !got.Partial || got.Agents[0].CallsPerHour != nil ||
		!strings.Contains(got.Note, "collecting since") {
		t.Fatalf("startup coverage must be explicit: %+v", got)
	}
	now = base.Add(2 * time.Hour)
	m.begin("SP2", "read", now)(false)
	now = base.Add(3 * time.Hour)
	m.begin("SP3", "read", now)(false)
	now = base.Add(7 * time.Hour)
	got := m.Snapshot()
	if !got.Known || !got.Partial || got.Totals.CallsSixHours != 2 || !strings.Contains(got.Note, "capacity") {
		t.Fatalf("loss at inclusive cutoff must remain partial: %+v", got)
	}
	for _, a := range got.Agents {
		if a.CallsPerHour != nil {
			t.Fatalf("capacity loss produced a first-call rate: %+v", a)
		}
	}
	evicted(true)
	if m.cells != 2 || m.Snapshot().Totals.CallsSixHours != 2 {
		t.Fatal("evicted completion recreated a cell")
	}
	now = now.Add(time.Millisecond)
	got = m.Snapshot()
	if !got.Known || got.Partial || got.Totals.CallsSixHours != 2 ||
		got.Agents[0].CallsPerHour == nil || got.Agents[1].CallsPerHour == nil {
		t.Fatalf("loss strictly outside the window should no longer hide coverage: %+v", got)
	}
}

func TestMetricsEvictedCallbackCannotChargeReusedKey(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	m.maxCells = 1
	now = now.Add(time.Hour)
	old := m.begin("SP1", "read", now)
	m.begin("SP2", "bash", now)(false) // Evicts the shared millisecond bucket.
	current := m.begin("SP1", "read", now)
	old(true)
	got := m.Snapshot()
	if m.cells != 1 || len(got.Agents) != 1 || got.Agents[0].CallsSixHours != 1 ||
		got.Agents[0].Errors != 0 || got.Agents[0].RunningCalls != 1 {
		t.Fatalf("an older callback charged a replacement aggregate: %+v", got)
	}
	current(false)
}

func TestMetricsGlobalCapacityIsBounded(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	for i := 0; i <= 65536; i++ {
		now = now.Add(time.Millisecond)
		m.begin("SP3", "read", now)(false)
	}
	got := m.Snapshot()
	if m.cells != 65536 || len(m.times) != 65536 || len(m.buckets) != 65536 ||
		got.Totals.CallsSixHours != 65536 || !got.Partial || !strings.Contains(got.Note, "capacity") {
		t.Fatalf("global history cap not enforced: cells=%d buckets=%d times=%d totals=%+v note=%s",
			m.cells, len(m.buckets), len(m.times), got.Totals, got.Note)
	}
}

func TestMetricsMonotonicElapsedIsIndependentOfWallLabels(t *testing.T) {
	base := time.Now()
	now := base
	m := NewMetrics(func() time.Time { return now })
	if m.started != base {
		t.Fatal("metrics origin discarded the clock's monotonic component")
	}
	now = base.Add(time.Hour)
	m.begin("SP1", "read", now)(false)
	now = base.Add(6 * time.Hour)
	finish := m.begin("SP1", "bash", now)
	defer finish(false)
	now = base.Add(7 * time.Hour)
	before := m.Snapshot()
	if !before.Known || before.Partial || before.Totals != (AgentTotals{ActiveAgents: 1, CallsLastHour: 1, CallsSixHours: 2, RunningCalls: 1}) {
		t.Fatalf("elapsed window before wall-label changes: %+v", before)
	}

	// These are display fields only. Public time.Time APIs cannot fabricate a
	// wall-only clock step while preserving an independent monotonic reading.
	// Changing the stored UTC labels pins that separation without unsafe code.
	first := m.buckets[time.Hour.Milliseconds()][metricKey{"SP1", "read"}]
	last := m.buckets[(6 * time.Hour).Milliseconds()][metricKey{"SP1", "bash"}]
	first.first.wall = base.Add(20 * time.Hour).UTC()
	first.last.wall = first.first.wall
	last.first.wall = base.Add(12 * time.Hour).UTC()
	last.last.wall = last.first.wall
	after := m.Snapshot()
	a := after.Agents[0]
	if !after.Known || after.Partial || after.Totals != before.Totals ||
		a.FirstCommandAt != timestamp(first.first.wall) || a.LastCommandAt != timestamp(last.last.wall) ||
		a.CallsPerHour == nil || math.Abs(*a.CallsPerHour-1.0/3.0) > 1e-12 {
		t.Fatalf("wall labels changed elapsed membership, start ordering, rate or running state: %+v", after)
	}
	if a.LastCommandAge == nil || *a.LastCommandAge != time.Hour ||
		!strings.Contains(RenderAgentsData(after).Sections[0].HTML, "1h ago") {
		t.Fatal("relative last-command age was reconstructed from corrected wall labels")
	}
	if !last.last.wall.Before(first.first.wall) {
		t.Fatal("fixture must reverse wall ordering while elapsed order stays forward")
	}
	now = now.Add(time.Millisecond)
	after = m.Snapshot()
	if !after.Known || after.Partial || after.Totals.CallsSixHours != 1 ||
		after.Agents[0].FirstCommandAt != timestamp(last.first.wall) || after.Agents[0].RunningCalls != 1 {
		t.Fatalf("expiry followed a wall label instead of elapsed time: %+v", after)
	}
}

func TestAgentViewElapsedAgeAllowsWallClockCorrection(t *testing.T) {
	wall, generated := "2026-11-06T16:30:00.000Z", "2026-11-06T16:00:00.000Z"
	age := 30 * time.Second
	html := agentLastCommandHTML(wall, generated, &age)
	if !strings.Contains(html, "30s ago") || !strings.Contains(html, "06 Nov 2026, 16:30:00 UTC") {
		t.Fatalf("valid elapsed age lost its wall UTC label after a clock correction: %s", html)
	}
	age = 0
	if html := agentLastCommandHTML(wall, generated, &age); !strings.Contains(html, "Just now") {
		t.Fatalf("zero elapsed age must remain available: %s", html)
	}
	age = -time.Nanosecond
	if agentLastCommandHTML(wall, generated, &age) != agentUnavailable {
		t.Fatal("negative elapsed age must remain unavailable")
	}
	age = time.Second
	if agentLastCommandHTML("invalid", generated, &age) != agentUnavailable {
		t.Fatal("elapsed age must not turn an invalid wall label into a timestamp")
	}
	if agentLastCommandHTML(wall, generated, nil) != agentUnavailable {
		t.Fatal("wall-only fallback must not invent an elapsed age for a future label")
	}
	if html := agentLastCommandHTML(generated, wall, nil); !strings.Contains(html, "30m ago") {
		t.Fatalf("wall-only fixture fallback changed: %s", html)
	}
}

func TestMetricsAcceptedStartOrderWithinElapsedMillisecond(t *testing.T) {
	base := time.Now()
	now := base
	m := NewMetrics(func() time.Time { return now })
	earlier := base.Add(6*time.Hour + 100*time.Microsecond)
	later := base.Add(6*time.Hour + 900*time.Microsecond)
	now = later
	m.begin("SP1", "read", later)(false)
	m.begin("SP1", "read", earlier)(false)
	if m.cells != 1 {
		t.Fatalf("same elapsed millisecond must share one counter: %d", m.cells)
	}
	cell := m.buckets[(6 * time.Hour).Milliseconds()][metricKey{"SP1", "read"}]
	if cell.first.elapsed != earlier.Sub(base) || cell.last.elapsed != later.Sub(base) ||
		!cell.first.wall.Equal(earlier) || !cell.last.wall.Equal(later) {
		t.Fatalf("admission lock order replaced accepted-start order: %+v", cell)
	}
	now = base.Add(7 * time.Hour)
	got := m.Snapshot()
	wantRate := 2 / now.Sub(earlier).Hours()
	if !got.Known || got.Partial || got.Agents[0].CallsPerHour == nil ||
		math.Abs(*got.Agents[0].CallsPerHour-wantRate) > 1e-12 {
		t.Fatalf("rate lost the accepted first-start instant: %+v", got)
	}
}

func TestMetricsElapsedClockReversalRecoversAtCatchUp(t *testing.T) {
	base := metricTestStart() // This injected clock has no monotonic component.
	now := base
	m := NewMetrics(func() time.Time { return now })
	now = base.Add(time.Hour)
	expired := m.begin("SP1", "read", now)
	now = base.Add(8 * time.Hour)
	if got := m.Snapshot(); !got.Known || got.Partial || got.Totals.CallsSixHours != 0 {
		t.Fatalf("expired window should be known empty: %+v", got)
	}
	current := m.begin("SP1", "bash", now)
	defer current(false)
	now = base.Add(7 * time.Hour)
	got := m.Snapshot()
	if got.Known || len(got.Agents) != 0 || !strings.Contains(got.Note, "timing") {
		t.Fatalf("reversed elapsed clock claimed expired history was complete: %+v", got)
	}
	expired(true)
	if m.active["SP1"] != 1 || m.cells != 1 {
		t.Fatalf("old completion changed current running state or restored history: active=%v cells=%d", m.active, m.cells)
	}
	now = base.Add(8 * time.Hour)
	got = m.Snapshot()
	if !got.Known || got.Partial || len(got.Agents) != 1 || got.Agents[0].CallsSixHours != 1 ||
		got.Agents[0].RunningCalls != 1 || got.Agents[0].Errors != 0 || got.Agents[0].CallsPerHour != nil {
		t.Fatalf("exact catch-up did not recover the retained current call: %+v", got)
	}
	current(true)
	current(true)
	now = base.Add(9 * time.Hour)
	got = m.Snapshot()
	if !got.Known || got.Partial || got.Agents[0].CallsSixHours != 1 ||
		got.Agents[0].RunningCalls != 0 || got.Agents[0].Errors != 1 ||
		got.Agents[0].CallsPerHour == nil || *got.Agents[0].CallsPerHour != 1 {
		t.Fatalf("clock reversal permanently poisoned metrics or double-counted completion: %+v", got)
	}
}

func TestMetricsFutureAdmissionIsUnknown(t *testing.T) {
	for _, offset := range []time.Duration{time.Millisecond, time.Nanosecond, -time.Nanosecond} {
		t.Run(offset.String(), func(t *testing.T) {
			now := metricTestStart()
			m := NewMetrics(func() time.Time { return now })
			m.begin("SP3", "read", now.Add(offset))(false)
			if got := m.Snapshot(); got.Known || len(got.Agents) != 0 {
				t.Fatalf("invalid timing must not round into a known count: %+v", got)
			}
		})
	}
}

type metricCallerFunc func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error)

func (f metricCallerFunc) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	return f(ctx, scope, name, args)
}

func TestMetricsCallerPreservesResultsAndExistingFailureUnion(t *testing.T) {
	tests := []struct {
		name, tool string
		result     wire.Result
		err        error
		failed     bool
	}{
		{name: "success", tool: "read"},
		{name: "reported failure", tool: "edit", result: wire.Result{IsError: true}, failed: true},
		{name: "nonzero exit", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"exit_code": 7}}, failed: true},
		{name: "timeout", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"timed_out": true}}, failed: true},
		{name: "cancelled", tool: "read", err: context.Canceled, failed: true},
		{name: "blocked", tool: "write", err: wire.Errorf("policy_blocked", "blocked"), failed: true},
		{name: "signal", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"signal": "SIGTERM"}}, failed: true},
		{name: "null terminal exit", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"exit_code": nil}}, failed: true},
		{name: "background accepted", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"exit_code": nil, "job_status": "running"}}},
		{name: "old failed job read", tool: "jobs", result: wire.Result{StructuredContent: map[string]any{"jobs": []any{map[string]any{"status": "failed", "exit_code": 7}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := metricTestStart()
			m := NewMetrics(func() time.Time { return now })
			now = now.Add(6 * time.Hour)
			args := map[string]any{"agent": "SP3", "command": "private-command-marker", "path": "private-path-marker"}
			test.result.Content = []wire.Content{{"type": "text", "text": "private-output-marker"}}
			nextCalls := 0
			caller := m.Wrap(metricCallerFunc(func(_ context.Context, scope wire.Scope, name string, forwarded map[string]any) (wire.Result, error) {
				nextCalls++
				if name != test.tool || scope.RequestID != "private-request-marker" || !reflect.DeepEqual(forwarded, args) {
					t.Fatal("caller changed transport inputs")
				}
				got := m.Snapshot()
				if got.Totals.CallsSixHours != 1 || got.Totals.RunningCalls != 1 {
					t.Fatalf("accepted call must be visible before completion: %+v", got)
				}
				return test.result, test.err
			}))
			result, err := caller.Call(context.Background(), wire.Scope{StartedAt: now, RequestID: "private-request-marker"}, test.tool, args)
			if nextCalls != 1 || !reflect.DeepEqual(result, test.result) || !errors.Is(err, test.err) {
				t.Fatalf("wrapped result changed: %+v %v", result, err)
			}
			got := m.Snapshot()
			wantErrors := 0
			if test.failed {
				wantErrors = 1
			}
			if len(got.Agents) != 1 || got.Agents[0].Agent != "SP3" || got.Agents[0].CallsSixHours != 1 ||
				got.Agents[0].RunningCalls != 0 || got.Agents[0].Errors != wantErrors {
				t.Fatalf("terminal classification mismatch: %+v", got)
			}
			data, err := json.Marshal(got)
			if err != nil || strings.Contains(string(data), "private-") {
				t.Fatalf("aggregate retained command metadata: %s %v", data, err)
			}
		})
	}
}

func TestMetricsCallerLabelsDebugToolsAndBackgroundReceipts(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	now = now.Add(6 * time.Hour)
	nextCalls := 0
	caller := m.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		nextCalls++
		return wire.Result{}, nil
	}))
	caller.Call(context.Background(), wire.Scope{Agent: "SP3"}, "activity_status", map[string]any{"agent": "SP2"})
	caller.Call(context.Background(), wire.Scope{}, "read", map[string]any{"agent": "invalid label"})
	caller.Call(context.Background(), wire.Scope{Agent: "SP3", Identity: wire.Identity{Source: "named", PrincipalType: "person", Role: "owner"}}, "list_projects", map[string]any{"agent": "SP3"})
	caller.Call(context.Background(), wire.Scope{Agent: "SP3"}, "bash_job", nil)
	got := m.Snapshot()
	if nextCalls != 4 || !got.Known || got.Partial || len(got.Agents) != 2 ||
		got.Agents[0].Agent != "SP2" || got.Agents[0].CallsSixHours != 1 ||
		got.Agents[0].Tools[0].Tool != "activity_status" ||
		got.Agents[1].Agent != "" || got.Agents[1].CallsSixHours != 2 || got.Totals.CallsSixHours != 3 {
		t.Fatalf("label/debug/background contract mismatch: %+v calls=%d", got, nextCalls)
	}
}

func TestMetricsCallerPanicClosesRunningState(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	caller := m.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		panic("caller panic")
	}))
	func() {
		defer func() {
			if recover() != "caller panic" {
				t.Fatal("wrapper swallowed or replaced caller panic")
			}
		}()
		caller.Call(context.Background(), wire.Scope{Agent: "SP3"}, "read", nil)
	}()
	got := m.Snapshot()
	if len(got.Agents) != 1 || got.Agents[0].Errors != 1 || got.Agents[0].RunningCalls != 0 {
		t.Fatalf("panic leaked running state or missed failure: %+v", got)
	}
}

func TestMetricsConcurrentCallsAndSnapshots(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	const count = 200
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	doneReading := make(chan struct{})
	var callers, reader sync.WaitGroup
	releaseCalls := sync.OnceFunc(func() { close(release) })
	stopReader := sync.OnceFunc(func() { close(doneReading) })
	defer func() {
		releaseCalls()
		callers.Wait()
		stopReader()
		reader.Wait()
	}()
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-doneReading:
				return
			default:
				m.Snapshot()
			}
		}
	}()
	caller := m.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		entered <- struct{}{}
		<-release
		return wire.Result{IsError: true}, nil
	}))
	for i := 0; i < count; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			caller.Call(context.Background(), wire.Scope{Agent: "SP3"}, "read", nil)
		}()
	}
	for i := 0; i < count; i++ {
		<-entered
	}
	got := m.Snapshot()
	if got.Totals.CallsSixHours != count || got.Totals.RunningCalls != count || got.Agents[0].Errors != 0 {
		t.Fatalf("concurrent starts were lost: %+v", got)
	}
	releaseCalls()
	callers.Wait()
	stopReader()
	reader.Wait()
	got = m.Snapshot()
	if got.Totals.CallsSixHours != count || got.Totals.RunningCalls != 0 || got.Agents[0].Errors != count || m.cells != 1 {
		t.Fatalf("concurrent completion double counted or lost outcomes: %+v cells=%d", got, m.cells)
	}
}

func TestAgentHTTPReadsOnlyMetricsAndDoesNotCountPolling(t *testing.T) {
	now := metricTestStart()
	m := NewMetrics(func() time.Time { return now })
	now = now.Add(6 * time.Hour)
	m.begin("SP3", "read", now)(false)
	jobs := &fakeJobs{}
	h := New(Options{Clock: func() time.Time { return now }, Metrics: m, Jobs: jobs,
		Git: func(context.Context, wire.Project, bool) (map[string]any, error) {
			t.Fatal("agent page attempted a repository read")
			return nil, nil
		}})
	defer h.Close()
	for _, path := range []string{"/activity", "/activity/data", "/activity", "/activity/data"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if path == "/activity/data" {
			var data DashboardData
			if json.Unmarshal(rec.Body.Bytes(), &data) != nil || len(data.Sections) != 1 || data.Sections[0].Key != "agents" {
				t.Fatalf("bad agent data: %s", body)
			}
			body = data.Sections[0].HTML
		}
		if rec.Code != 200 || !strings.Contains(body, "Active agents · last 6 hours") ||
			!strings.Contains(body, "SP3") || !strings.Contains(body, "data-agent-metric=\"six-hours\">1</dd>") {
			t.Fatalf("page without an audit journal lost metrics: %d %s", rec.Code, body)
		}
		for _, forbidden := range []string{"Command history", "Project working trees", "/activity/live", "/activity/job", "/activity/diff", "data-project-filter"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("agent page acquired another projection: %s", forbidden)
			}
		}
	}
	if jobs.snapshots.Load() != 0 || jobs.reads.Load() != 0 || m.Snapshot().Totals.CallsSixHours != 1 {
		t.Fatal("page polling inspected jobs or added tool calls")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := h.CollectAgents(ctx); got.Known || len(got.Agents) != 0 {
		t.Fatalf("cancelled acquisition must be unknown: %+v", got)
	}
	h.options.Metrics = nil
	if got := h.CollectAgents(context.Background()); got.Known || len(got.Agents) != 0 {
		t.Fatalf("missing owner must be unknown: %+v", got)
	}
}
