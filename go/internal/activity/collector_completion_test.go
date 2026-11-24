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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// A selected-label collector must not acquire historical records or output.
// Observation channels synchronize wait tests without polling or sleep loops.
type completionJobSource struct {
	fakeJobs
	mu       sync.Mutex
	known    bool
	counts   map[string]jobs.ActivityCounts
	changed  chan struct{}
	observed chan struct{}
}

func newCompletionJobs() *completionJobSource {
	return &completionJobSource{known: true, counts: map[string]jobs.ActivityCounts{},
		changed: make(chan struct{})}
}

func (*completionJobSource) Snapshot() []jobs.Record {
	panic("completion collector scanned historical jobs")
}

func (s *completionJobSource) AgentActivity(label string) (jobs.ActivityCounts, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.observed != nil {
		select {
		case s.observed <- struct{}{}:
		default:
		}
	}
	return s.counts[label], s.known
}

func (s *completionJobSource) ActivityChanged() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *completionJobSource) set(label string, counts jobs.ActivityCounts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[label] = counts
	close(s.changed)
	s.changed = make(chan struct{})
}

func completionHandler(t *testing.T, clock func() time.Time, metrics *Metrics, source *completionJobSource) *Handler {
	t.Helper()
	h := New(Options{Clock: clock, Metrics: metrics, Jobs: source,
		Work: &WorkHistory{Runs: func(context.Context, string) ([]map[string]any, error) {
			panic("completion collector consulted managed work")
		}}})
	journal := h.options.Audit
	t.Cleanup(func() { h.Close(); journal.Close() })
	return h
}

func completionAgent(t *testing.T, value CollectorStatus, label string) CollectorAgent {
	t.Helper()
	if value.Version != 2 || value.Epoch == "" || !value.Activity.Known ||
		len(value.Activity.Agents) != 1 || value.Activity.Agents[0].Label != label {
		t.Fatalf("unavailable or incorrectly scoped completion status: %+v", value)
	}
	return value.Activity.Agents[0]
}

func TestCollectorCompletionLabelsCountersAndScalarPrivacy(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics, source := NewMetrics(clock), newCompletionJobs()
	h := completionHandler(t, clock, metrics, source)
	labels := []string{"FP5", "SP18", "RDP3", "ap-1", "Z"}
	caller := metrics.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return wire.Result{Content: []wire.Content{{"type": "text", "text": "private-output"}},
			StructuredContent: map[string]any{"stdout": "private-output"}}, nil
	}))
	for _, label := range labels {
		_, err := caller.Call(context.Background(), wire.Scope{
			Agent: label, StartedAt: now, RequestID: "private-request", PrincipalID: "private-principal",
			SessionID: "private-session", TransportID: "private-transport",
		}, "read", map[string]any{"command": "private-command", "workspace_id": "private-workspace"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, label := range labels {
		value := h.CollectAgentStatus(context.Background(), label)
		agent := completionAgent(t, value, label)
		if agent.StartedCalls != 1 || agent.CompletedCalls != 1 || agent.FailedCalls != 0 ||
			agent.InFlightCalls != 0 || agent.Revision != 2 ||
			agent.LastToolName != "read" || agent.LastToolError == nil || *agent.LastToolError ||
			agent.LastToolStartedAt == nil || agent.LastToolFinishedAt == nil ||
			*agent.LastToolStartedAt != timestamp(now) || *agent.LastToolFinishedAt != timestamp(now) {
			t.Fatalf("label %q counters: %+v", label, agent)
		}
		body, err := json.Marshal(value)
		if err != nil || strings.Contains(string(body), "private-") || len(body) > 16<<10 {
			t.Fatalf("completion response leaked request state or exceeded its bound: %s, %v", body, err)
		}
	}
	empty := completionAgent(t, h.CollectAgentStatus(context.Background(), "NEW"), "NEW")
	if empty.StartedCalls != 0 || empty.CompletedCalls != 0 || empty.FailedCalls != 0 ||
		empty.InFlightCalls != 0 || empty.LastActivityAt != nil || empty.LastToolError != nil {
		t.Fatalf("unseen valid label inherited another label's activity: %+v", empty)
	}
	if value := h.CollectAgentStatus(context.Background(), "invalid-label"); value.Activity.Known {
		t.Fatalf("invalid label yielded a usable baseline: %+v", value)
	}
}

func TestCollectorCompletionFinishesExactlyOnceAndSurvivesDashboardExpiry(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics := NewMetrics(clock)
	h := completionHandler(t, clock, metrics, newCompletionJobs())
	first := metrics.begin("FP5", "read", now)
	second := metrics.begin("FP5", "bash", now)
	first(false)
	first(true)
	got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.StartedCalls != 2 || got.CompletedCalls != 1 || got.FailedCalls != 0 ||
		got.InFlightCalls != 1 || got.Revision != 3 {
		t.Fatalf("duplicate finish changed counters or drained another call: %+v", got)
	}
	now = now.Add(7 * time.Hour)
	metrics.Snapshot() // The ordinary dashboard's six-hour window has expired.
	got = completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.CompletedCalls != 1 || got.InFlightCalls != 1 {
		t.Fatalf("dashboard expiry discarded completion baseline or current call: %+v", got)
	}
	second(true)
	second(false)
	got = completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.StartedCalls != 2 || got.CompletedCalls != 2 || got.FailedCalls != 1 ||
		got.InFlightCalls != 0 || got.Revision != 4 || got.LastToolError == nil || !*got.LastToolError ||
		got.LastToolFinishedAt == nil || *got.LastToolFinishedAt != timestamp(now) {
		t.Fatalf("late terminal callback was lost or applied twice: %+v", got)
	}
}

func TestCollectorCompletionWrappedFailureAndBackgroundSemantics(t *testing.T) {
	tests := []struct {
		name, tool string
		result     wire.Result
		err        error
		failed     bool
	}{
		{name: "successful read", tool: "read"},
		{name: "tool error", tool: "edit", result: wire.Result{IsError: true}, failed: true},
		{name: "bash exit", tool: "bash", result: wire.Result{StructuredContent: map[string]any{"exit_code": 9}}, failed: true},
		{name: "cancelled", tool: "read", err: context.Canceled, failed: true},
		{name: "queued submission", tool: "queue_jobs", result: wire.Result{StructuredContent: map[string]any{"job_status": "queued"}}},
		{name: "old failed job", tool: "jobs", result: wire.Result{StructuredContent: map[string]any{"jobs": []any{map[string]any{"status": "failed"}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := metricTestStart()
			clock := func() time.Time { return now }
			metrics := NewMetrics(clock)
			h := completionHandler(t, clock, metrics, newCompletionJobs())
			caller := metrics.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
				current := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
				if current.StartedCalls != 1 || current.CompletedCalls != 0 || current.InFlightCalls != 1 {
					t.Fatalf("request was invisible while executing: %+v", current)
				}
				return test.result, test.err
			}))
			_, err := caller.Call(context.Background(), wire.Scope{Agent: "FP5", StartedAt: now}, test.tool, nil)
			if !errors.Is(err, test.err) {
				t.Fatalf("wrapper changed request error: %v", err)
			}
			got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
			wantFailed := uint64(0)
			if test.failed {
				wantFailed = 1
			}
			if got.CompletedCalls != 1 || got.FailedCalls != wantFailed || got.InFlightCalls != 0 ||
				got.LastToolError == nil || *got.LastToolError != test.failed {
				t.Fatalf("request terminal classification changed: %+v", got)
			}
		})
	}
	t.Run("panic", func(t *testing.T) {
		now := metricTestStart()
		clock := func() time.Time { return now }
		metrics := NewMetrics(clock)
		h := completionHandler(t, clock, metrics, newCompletionJobs())
		caller := metrics.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
			panic("private-panic")
		}))
		func() {
			defer func() {
				if recover() != "private-panic" {
					t.Fatal("caller panic was swallowed or changed")
				}
			}()
			caller.Call(context.Background(), wire.Scope{Agent: "FP5", StartedAt: now}, "read", nil)
		}()
		got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
		if got.CompletedCalls != 1 || got.FailedCalls != 1 || got.InFlightCalls != 0 {
			t.Fatalf("panic left a request in flight: %+v", got)
		}
	})
}

func TestCollectorCompletionDoesNotTreatSubmissionAsJobCompletion(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics, source := NewMetrics(clock), newCompletionJobs()
	h := completionHandler(t, clock, metrics, source)
	caller := metrics.Wrap(metricCallerFunc(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		source.set("FP5", jobs.ActivityCounts{InFlightJobs: 1, QueuedJobs: 2, Revision: 3, LastActivityAt: now})
		return wire.Result{StructuredContent: map[string]any{"job_status": "running", "job_id": "private-job"}}, nil
	}))
	caller.Call(context.Background(), wire.Scope{Agent: "FP5", StartedAt: now}, "start_jobs", nil)
	got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.CompletedCalls != 1 || got.InFlightCalls != 0 || got.InFlightJobs != 1 ||
		got.QueuedJobs != 2 || got.Revision != 5 {
		t.Fatalf("accepted jobs disappeared when their submission returned: %+v", got)
	}
	source.set("FP5", jobs.ActivityCounts{Revision: 6, LastActivityAt: now})
	got = completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.CompletedCalls != 1 || got.InFlightJobs != 0 || got.QueuedJobs != 0 || got.Revision != 8 {
		t.Fatalf("job completion counted as another MCP call or did not drain: %+v", got)
	}
}

func TestCollectorCompletionIndependentOfDisabledMissingAndCorruptAudit(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics := NewMetrics(clock)
	h := completionHandler(t, clock, metrics, newCompletionJobs())
	metrics.begin("FP5", "read", now)(false)
	if h.options.Audit.Enabled() {
		t.Fatal("disabled-audit fixture is enabled")
	}
	completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	disabled := h.options.Audit
	h.options.Audit = nil
	completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	h.options.Audit = disabled

	dir := t.TempDir()
	journal, err := audit.Open(audit.Options{Directory: dir, Enabled: true, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	receipt, err := journal.Record(context.Background(), audit.RecordInput{
		ToolName: "read", Scope: wire.Scope{Agent: "FP5", RequestID: "private-request"},
		Args: map[string]any{"agent": "FP5"}, Result: wire.Result{}, StartedAt: now, FinishedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := journal.Get(context.Background(), receipt.ActionID); err != nil || !exists {
		t.Fatalf("audit fixture did not become readable: %v", err)
	}
	segments, err := filepath.Glob(filepath.Join(dir, "segment-*.jsonl"))
	if err != nil || len(segments) != 1 {
		t.Fatalf("unexpected audit fixture segments: %v %v", segments, err)
	}
	file, err := os.OpenFile(segments[0], os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteAt([]byte("!"), 0)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("could not corrupt private audit fixture: %v %v", writeErr, closeErr)
	}
	if journal.CollectEventTimes(context.Background(), now).Known {
		t.Fatal("corrupted audit fixture unexpectedly usable")
	}
	h.options.Audit = journal
	got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	h.options.Audit = disabled
	if got.CompletedCalls != 1 || got.InFlightCalls != 0 {
		t.Fatalf("unusable historical audit changed runtime completion: %+v", got)
	}
}

func TestCollectorCompletionConcurrentWrappedCalls(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics := NewMetrics(clock)
	h := completionHandler(t, clock, metrics, newCompletionJobs())
	const count = 32
	entered := make(chan struct{}, count)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var wg sync.WaitGroup
	defer func() { unblock(); wg.Wait() }()
	caller := metrics.Wrap(metricCallerFunc(func(_ context.Context, _ wire.Scope, _ string, args map[string]any) (wire.Result, error) {
		entered <- struct{}{}
		<-release
		return wire.Result{IsError: args["failed"].(bool)}, nil
	}))
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(failed bool) {
			defer wg.Done()
			caller.Call(context.Background(), wire.Scope{Agent: "FP5", StartedAt: now}, "read", map[string]any{"failed": failed})
		}(i%2 == 0)
	}
	for i := 0; i < count; i++ {
		<-entered
	}
	active := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if active.StartedCalls != count || active.CompletedCalls != 0 || active.InFlightCalls != count {
		t.Fatalf("concurrent in-flight requests lost: %+v", active)
	}
	unblock()
	wg.Wait()
	got := completionAgent(t, h.CollectAgentStatus(context.Background(), "FP5"), "FP5")
	if got.StartedCalls != count || got.CompletedCalls != count || got.FailedCalls != count/2 ||
		got.InFlightCalls != 0 || got.Revision != count*2 {
		t.Fatalf("concurrent terminal counters lost or duplicated: %+v", got)
	}
}

func awaitCompletionObservation(t *testing.T, source *completionJobSource) {
	t.Helper()
	select {
	case <-source.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not acquire the selected label")
	}
}

func serveCompletionAsync(h *Handler, request *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		h.serveCollector(recorder, request)
		done <- recorder
	}()
	return done
}

func awaitCompletionHTTP(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case recorder := <-done:
		return recorder
	case <-time.After(5 * time.Second):
		t.Fatal("collector wait did not return")
		return nil
	}
}

func decodeCompletionHTTP(t *testing.T, recorder *httptest.ResponseRecorder) CollectorStatus {
	t.Helper()
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("invalid collector HTTP response: %d %v %s", recorder.Code, recorder.Header(), recorder.Body)
	}
	var value CollectorStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCollectorCompletionWaitWakesOnTerminalAndJobRevision(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		now := metricTestStart()
		clock := func() time.Time { return now }
		metrics, source := NewMetrics(clock), newCompletionJobs()
		h := completionHandler(t, clock, metrics, source)
		source.observed = make(chan struct{}, 8)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=20s&since_completed=0", nil).WithContext(ctx)
		done := serveCompletionAsync(h, request)
		awaitCompletionObservation(t, source)
		finish := metrics.begin("FP5", "read", now)
		awaitCompletionObservation(t, source) // A start alone cannot satisfy a completion wait.
		select {
		case recorder := <-done:
			t.Fatalf("completion wait returned for a start: %s", recorder.Body)
		default:
		}
		finish(false)
		got := completionAgent(t, decodeCompletionHTTP(t, awaitCompletionHTTP(t, done)), "FP5")
		if got.CompletedCalls != 1 || got.InFlightCalls != 0 {
			t.Fatalf("completion wait returned stale counters: %+v", got)
		}
	})
	t.Run("job drain", func(t *testing.T) {
		now := metricTestStart()
		clock := func() time.Time { return now }
		metrics, source := NewMetrics(clock), newCompletionJobs()
		h := completionHandler(t, clock, metrics, source)
		source.set("FP5", jobs.ActivityCounts{InFlightJobs: 1, QueuedJobs: 1, Revision: 2, LastActivityAt: now})
		source.observed = make(chan struct{}, 8)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := serveCompletionAsync(h, httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=20s&since_revision=2", nil).WithContext(ctx))
		awaitCompletionObservation(t, source)
		source.set("FP5", jobs.ActivityCounts{Revision: 4, LastActivityAt: now})
		got := completionAgent(t, decodeCompletionHTTP(t, awaitCompletionHTTP(t, done)), "FP5")
		if got.CompletedCalls != 0 || got.InFlightJobs != 0 || got.QueuedJobs != 0 || got.Revision != 4 {
			t.Fatalf("job-only revision did not wake wait or invented a tool call: %+v", got)
		}
	})
	t.Run("request start revision", func(t *testing.T) {
		now := metricTestStart()
		clock := func() time.Time { return now }
		metrics, source := NewMetrics(clock), newCompletionJobs()
		h := completionHandler(t, clock, metrics, source)
		source.observed = make(chan struct{}, 8)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := serveCompletionAsync(h, httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=20s&since_revision=0", nil).WithContext(ctx))
		awaitCompletionObservation(t, source)
		finish := metrics.begin("FP5", "read", now)
		defer finish(false)
		got := completionAgent(t, decodeCompletionHTTP(t, awaitCompletionHTTP(t, done)), "FP5")
		if got.CompletedCalls != 0 || got.InFlightCalls != 1 || got.Revision != 1 {
			t.Fatalf("call start revision did not wake wait: %+v", got)
		}
	})
}

func TestCollectorCompletionWaitCancellationTimeoutHeadAndRestart(t *testing.T) {
	now := metricTestStart()
	clock := func() time.Time { return now }
	metrics, source := NewMetrics(clock), newCompletionJobs()
	h := completionHandler(t, clock, metrics, source)
	baseline := h.CollectAgentStatus(context.Background(), "FP5")
	t.Run("cancel", func(t *testing.T) {
		source.observed = make(chan struct{}, 8)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := serveCompletionAsync(h, httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=20s&since_completed=0", nil).WithContext(ctx))
		awaitCompletionObservation(t, source)
		cancel()
		value := decodeCompletionHTTP(t, awaitCompletionHTTP(t, done))
		if value.Activity.Known || len(value.Activity.Agents) != 0 {
			t.Fatalf("cancelled acquisition invented known idle activity: %+v", value)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		done := serveCompletionAsync(h, httptest.NewRequest("GET", "/status/collector?agent=FP5&wait=1ms&since_completed=0", nil))
		got := completionAgent(t, decodeCompletionHTTP(t, awaitCompletionHTTP(t, done)), "FP5")
		if got.CompletedCalls != 0 || got.InFlightCalls != 0 || got.Revision != 0 {
			t.Fatalf("wait timeout changed counters: %+v", got)
		}
	})
	t.Run("head does not wait", func(t *testing.T) {
		done := serveCompletionAsync(h, httptest.NewRequest("HEAD", "/status/collector?agent=FP5&wait=20s&since_completed=0", nil))
		recorder := awaitCompletionHTTP(t, done)
		if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HEAD waited or emitted a body: %d %s", recorder.Code, recorder.Body)
		}
	})
	t.Run("epoch reset returns immediately", func(t *testing.T) {
		restarted := completionHandler(t, clock, NewMetrics(clock), newCompletionJobs())
		current := restarted.CollectAgentStatus(context.Background(), "FP5")
		if current.Epoch == baseline.Epoch {
			t.Fatal("new runtime reused a completion epoch")
		}
		request := httptest.NewRequest("GET", fmt.Sprintf("/status/collector?agent=FP5&wait=20s&since_completed=0&epoch=%s", baseline.Epoch), nil)
		got := decodeCompletionHTTP(t, awaitCompletionHTTP(t, serveCompletionAsync(restarted, request)))
		completionAgent(t, got, "FP5")
		if got.Epoch != current.Epoch {
			t.Fatalf("restart response lost its new epoch: %+v", got)
		}
	})
}
