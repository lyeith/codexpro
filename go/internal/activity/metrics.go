package activity

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const maxMetricCells = 65536

type metricKey struct{ agent, tool string }

// Elapsed time orders observations; wall time is used only for UTC labels.
// Sub preserves the standard Go monotonic clock carried by transport starts.
type metricMoment struct {
	elapsed time.Duration
	wall    time.Time
}

type metricCounts struct {
	calls, errors int
	first, last   metricMoment
}
type metricBucket map[metricKey]*metricCounts

// Metrics keeps only bounded counters at millisecond elapsed-time precision.
// There are no call IDs, request metadata, audit records or persisted files.
// Current in-flight counts follow accepted caller lifetimes independently of
// history expiry and audit enablement.
type Metrics struct {
	mu       sync.Mutex
	clock    func() time.Time
	started  time.Time
	observed time.Duration
	lostMS   int64
	lost     bool
	invalid  bool
	maxCells int
	cells    int
	buckets  map[int64]metricBucket
	times    metricTimes
	active   map[string]int
}

func NewMetrics(clock func() time.Time) *Metrics {
	if clock == nil {
		clock = time.Now
	}
	return &Metrics{clock: clock, started: clock(), maxCells: maxMetricCells,
		buckets: map[int64]metricBucket{}, active: map[string]int{}}
}

func (m *Metrics) moment(at time.Time) metricMoment {
	return metricMoment{elapsed: at.Sub(m.started), wall: at.UTC()}
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

// nowLocked serializes clock observations without discarding monotonic time.
// A clock seam without monotonic readings may temporarily reverse elapsed time;
// retain its latest observation so snapshots stay unknown only until catch-up.
func (m *Metrics) nowLocked() metricMoment {
	now := m.moment(m.clock())
	if now.elapsed > m.observed {
		m.observed = now.elapsed
	}
	return now
}

// begin is called once at admission. Its terminal callback carries only an
// aggregate key/counter, and sync.Once prevents a second terminal update.
func (m *Metrics) begin(agent, tool string, started time.Time) func(bool) {
	key := metricKey{agent: agent, tool: tool}
	m.mu.Lock()
	now := m.nowLocked()
	stamp := now
	if !started.IsZero() {
		stamp = m.moment(started)
	}
	nowMS, at := now.elapsed.Milliseconds(), stamp.elapsed.Milliseconds()
	m.expire(nowMS)
	m.active[agent]++
	var cell *metricCounts
	if stamp.elapsed < 0 || stamp.elapsed > now.elapsed || tool == "" {
		// Accepted transport calls cannot supply an invalid admission instant.
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
			cell = &metricCounts{first: stamp, last: stamp}
			bucket[key] = cell
			m.cells++
		}
		cell.calls++
		if stamp.elapsed < cell.first.elapsed {
			cell.first = stamp
		}
		if stamp.elapsed > cell.last.elapsed {
			cell.last = stamp
		}
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
			m.expire(m.nowLocked().elapsed.Milliseconds())
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
		finish(!returned || unsuccessfulCall(name, result, err))
	}()
	result, err = c.next.Call(ctx, scope, name, args)
	returned = true
	return result, err
}

func (m *Metrics) Snapshot() AgentDashboard {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowLocked()
	out := unknownAgents(now.wall, "")
	if m.invalid || now.elapsed < m.observed {
		out.Note = "Metrics timing is unavailable. Counts and running state are unknown."
		return out
	}
	nowMS := now.elapsed.Milliseconds()
	m.expire(nowMS)
	cutoff, hour := nowMS-agentActivityWindow.Milliseconds(), nowMS-time.Hour.Milliseconds()
	out.Known = true
	out.Partial = cutoff < 0 || m.lost && cutoff <= m.lostMS
	out.Note = "Accepted CodexPro tool calls only. Agent tags are activity labels, not identities."
	if cutoff < 0 {
		out.Note = fmt.Sprintf("Metrics have been collecting since %s. Counts cover the observed interval; calls/hour is unavailable until six hours are covered.", timestamp(m.started))
	}
	if m.lost && cutoff <= m.lostMS {
		out.Note = "Metrics capacity was reached within this window. Counts are observed lower bounds and calls/hour is unavailable."
	}

	type accumulator struct {
		summary     AgentSummary
		first, last metricMoment
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
				group = &accumulator{summary: AgentSummary{Agent: key.agent}, first: count.first, last: count.last, tools: map[string]int{}}
				groups[key.agent] = group
			}
			group.summary.CallsSixHours += count.calls
			group.summary.Errors += count.errors
			if at >= hour {
				group.summary.CallsLastHour += count.calls
			}
			if count.first.elapsed < group.first.elapsed {
				group.first = count.first
			}
			if count.last.elapsed > group.last.elapsed {
				group.last = count.last
			}
			group.tools[key.tool] += count.calls
		}
	}
	for agent, group := range groups {
		summary := group.summary
		summary.FirstCommandAt = timestamp(group.first.wall)
		summary.LastCommandAt = timestamp(group.last.wall)
		age := now.elapsed - group.last.elapsed
		summary.LastCommandAge = &age
		summary.RunningCalls = m.active[agent]
		if span := now.elapsed - group.first.elapsed; !out.Partial && span > 0 {
			rate := float64(summary.CallsSixHours) / span.Hours()
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
