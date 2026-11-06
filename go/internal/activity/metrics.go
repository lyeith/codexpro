package activity

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const maxMetricCells = 65536

type metricKey struct{ agent, tool string }
type metricCounts struct{ calls, errors int }
type metricBucket map[metricKey]*metricCounts

// Metrics keeps only bounded counters at the existing millisecond time precision.
// There are no call IDs, request metadata, audit records or persisted files.
// Current in-flight counts follow accepted caller lifetimes independently of
// history expiry and audit enablement.
type Metrics struct {
	mu         sync.Mutex
	clock      func() time.Time
	startedMS  int64
	observedMS int64
	lostMS     int64
	lost       bool
	invalid    bool
	maxCells   int
	cells      int
	buckets    map[int64]metricBucket
	times      metricTimes
	active     map[string]int
}

func NewMetrics(clock func() time.Time) *Metrics {
	if clock == nil {
		clock = time.Now
	}
	startedMS := clock().UnixMilli()
	return &Metrics{clock: clock, startedMS: startedMS, observedMS: startedMS, maxCells: maxMetricCells,
		buckets: map[int64]metricBucket{}, active: map[string]int{}}
}

type metricTimes []int64

func (h metricTimes) Len() int           { return len(h) }
func (h metricTimes) Less(i, j int) bool { return h[i] < h[j] }
func (h metricTimes) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *metricTimes) Push(v any)        { *h = append(*h, v.(int64)) }
func (h *metricTimes) Pop() any {
	old := *h
	value := old[len(old)-1]
	*h = old[:len(old)-1]
	return value
}

func (m *Metrics) dropOldest(pressure bool) {
	at := heap.Pop(&m.times).(int64)
	m.cells -= len(m.buckets[at])
	delete(m.buckets, at)
	if pressure && (!m.lost || at > m.lostMS) {
		m.lost, m.lostMS = true, at
	}
}

func (m *Metrics) expire(nowMS int64) {
	cutoff := nowMS - agentActivityWindow.Milliseconds()
	for len(m.times) > 0 && m.times[0] < cutoff {
		m.dropOldest(false)
	}
}

// nowLocked serializes clock observations so normal concurrency cannot look like
// a backwards clock step. Once time regresses, expired history is unknowable.
func (m *Metrics) nowLocked() int64 {
	nowMS := m.clock().UnixMilli()
	if nowMS < m.observedMS {
		m.invalid = true
	} else {
		m.observedMS = nowMS
	}
	return nowMS
}

// begin is called once at admission. Its terminal callback carries only an
// aggregate key/counter, and sync.Once prevents a second terminal update.
func (m *Metrics) begin(agent, tool string, started time.Time) func(bool) {
	key := metricKey{agent: agent, tool: tool}
	m.mu.Lock()
	nowMS := m.nowLocked()
	at := started.UnixMilli()
	if started.IsZero() {
		at = nowMS
	}
	m.expire(nowMS)
	m.active[agent]++
	var cell *metricCounts
	if at > nowMS || at < m.startedMS || tool == "" {
		// The observer must not manufacture a known window from invalid clock
		// observations. Accepted transport calls cannot supply these values.
		m.invalid = true
	} else if at >= nowMS-agentActivityWindow.Milliseconds() {
		bucket := m.buckets[at]
		if bucket == nil {
			bucket = metricBucket{}
			m.buckets[at] = bucket
			heap.Push(&m.times, at)
		}
		cell = bucket[key]
		if cell == nil {
			cell = &metricCounts{}
			bucket[key] = cell
			m.cells++
		}
		cell.calls++
		for m.cells > m.maxCells {
			m.dropOldest(true)
		}
	}
	m.mu.Unlock()
	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.expire(m.nowLocked())
			if m.active[agent] <= 1 {
				delete(m.active, agent)
			} else {
				m.active[agent]--
			}
			// Never recreate an expired/evicted cell, or charge a later cell
			// that happens to reuse the same timestamp/key.
			if failed && cell != nil && m.buckets[at][key] == cell {
				cell.errors++
			}
		})
	}
}

// Wrap instruments only the top-level transport caller. Batch execution keeps
// its bare engine/raw-child interface, and background completion receipts never
// pass through this wrapper.
func (m *Metrics) Wrap(next wire.Caller) wire.Caller {
	return &metricsCaller{metrics: m, next: next}
}

type metricsCaller struct {
	metrics *Metrics
	next    wire.Caller
}

func (c *metricsCaller) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (result wire.Result, err error) {
	if name == "bash_job" {
		return c.next.Call(ctx, scope, name, args)
	}
	agent := wire.String(args, "agent", scope.Agent)
	if scope.Identity.Restricted() || !wire.ValidAgent(agent) {
		agent = ""
	}
	finish := c.metrics.begin(agent, name, scope.StartedAt)
	returned := false
	defer func() {
		finish(!returned || audit.UnsuccessfulCall(name, result, err))
	}()
	result, err = c.next.Call(ctx, scope, name, args)
	returned = true
	return result, err
}

func (m *Metrics) Snapshot() AgentDashboard {
	m.mu.Lock()
	defer m.mu.Unlock()
	nowMS := m.nowLocked()
	now := time.UnixMilli(nowMS)
	out := unknownAgents(now, "")
	if m.invalid || nowMS < m.startedMS {
		out.Note = "Metrics timing is unavailable. Counts and running state are unknown."
		return out
	}
	m.expire(nowMS)
	cutoff, hour := nowMS-agentActivityWindow.Milliseconds(), nowMS-time.Hour.Milliseconds()
	out.Known = true
	out.Partial = cutoff < m.startedMS || m.lost && cutoff <= m.lostMS
	out.Note = "Accepted CodexPro tool calls only. Agent tags are activity labels, not identities."
	if cutoff < m.startedMS {
		out.Note = fmt.Sprintf("Metrics have been collecting since %s. Counts cover the observed interval; calls/hour is unavailable until six hours are covered.", timestamp(time.UnixMilli(m.startedMS)))
	}
	if m.lost && cutoff <= m.lostMS {
		out.Note = "Metrics capacity was reached within this window. Counts are observed lower bounds and calls/hour is unavailable."
	}

	type accumulator struct {
		summary     AgentSummary
		first, last int64
		tools       map[string]int
	}
	groups := map[string]*accumulator{}
	for at, bucket := range m.buckets {
		if at < cutoff || at > nowMS {
			continue
		}
		for key, count := range bucket {
			group := groups[key.agent]
			if group == nil {
				group = &accumulator{summary: AgentSummary{Agent: key.agent}, first: at, last: at, tools: map[string]int{}}
				groups[key.agent] = group
			}
			group.summary.CallsSixHours += count.calls
			group.summary.Errors += count.errors
			if at >= hour {
				group.summary.CallsLastHour += count.calls
			}
			if at < group.first {
				group.first = at
			}
			if at > group.last {
				group.last = at
			}
			group.tools[key.tool] += count.calls
		}
	}
	for agent, group := range groups {
		summary := group.summary
		summary.FirstCommandAt = timestamp(time.UnixMilli(group.first))
		summary.LastCommandAt = timestamp(time.UnixMilli(group.last))
		summary.RunningCalls = m.active[agent]
		if span := nowMS - group.first; !out.Partial && span > 0 {
			rate := float64(summary.CallsSixHours) * float64(time.Hour.Milliseconds()) / float64(span)
			summary.CallsPerHour = &rate
		}
		for tool, count := range group.tools {
			summary.Tools = append(summary.Tools, AgentToolTally{Tool: tool, Count: count})
		}
		sort.Slice(summary.Tools, func(i, j int) bool {
			if summary.Tools[i].Count != summary.Tools[j].Count {
				return summary.Tools[i].Count > summary.Tools[j].Count
			}
			return summary.Tools[i].Tool < summary.Tools[j].Tool
		})
		out.Agents = append(out.Agents, summary)
		out.Totals.CallsLastHour += summary.CallsLastHour
		out.Totals.CallsSixHours += summary.CallsSixHours
		out.Totals.RunningCalls += summary.RunningCalls
	}
	sort.Slice(out.Agents, func(i, j int) bool { return agentLabelLess(out.Agents[i].Agent, out.Agents[j].Agent) })
	out.Totals.ActiveAgents = len(out.Agents)
	return out
}
