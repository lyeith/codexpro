package activity

import (
	"crypto/rand"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Completion counters live for one Metrics instance, independently of dashboard
// windows and audit retention. The existing metrics capacity bounds label memory;
// once full, untracked labels are unknown rather than falsely reported as idle.
type collectorCalls struct {
	started, completed, failed, revision uint64
	lastStart, lastFinish                time.Time
	lastName                             string
	lastError                            bool
}

func newCollectorEpoch() string { return rand.Text() }

func (m *Metrics) startCollectorLocked(agent, tool string, at time.Time) {
	if !wire.ValidAgent(agent) {
		return
	}
	calls := m.collectorCalls[agent]
	if calls == nil {
		if len(m.collectorCalls) >= maxMetricCells {
			if !m.collectorFull {
				m.collectorFull = true
				m.notifyCollectorLocked()
			}
			return
		}
		calls = &collectorCalls{}
		m.collectorCalls[agent] = calls
	}
	calls.started++
	calls.revision++
	if at.After(calls.lastStart) {
		calls.lastStart = at
	}
	if calls.completed == 0 {
		calls.lastName = tool
	}
	m.notifyCollectorLocked()
}

func (m *Metrics) finishCollectorLocked(agent, tool string, at time.Time, failed bool) {
	calls := m.collectorCalls[agent]
	if calls == nil {
		return
	}
	calls.completed++
	if failed {
		calls.failed++
	}
	calls.revision++
	calls.lastFinish, calls.lastName, calls.lastError = at, tool, failed
	m.notifyCollectorLocked()
}

func (m *Metrics) notifyCollectorLocked() {
	close(m.collectorChanged)
	m.collectorChanged = make(chan struct{})
}

// collectorSnapshot is O(1) for a selected label.
// The returned values are detached and the signal is captured under the same lock.
func (m *Metrics) collectorSnapshot(label string) (string, CollectorAgent, bool, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowLocked()
	if !wire.ValidAgent(label) || m.invalid || now.elapsed < m.observed || m.collectorFull && m.collectorCalls[label] == nil {
		return m.collectorEpoch, CollectorAgent{}, false, m.collectorChanged
	}
	value := CollectorAgent{Label: label, InFlightCalls: m.active[label]}
	if calls := m.collectorCalls[label]; calls != nil {
		value.StartedCalls, value.CompletedCalls, value.FailedCalls = calls.started, calls.completed, calls.failed
		value.Revision, value.LastToolName = calls.revision, calls.lastName
		if !calls.lastStart.IsZero() {
			stamp := timestamp(calls.lastStart)
			value.LastToolStartedAt = &stamp
			value.LastActivityAt = &stamp
		}
		if !calls.lastFinish.IsZero() {
			stamp := timestamp(calls.lastFinish)
			value.LastToolFinishedAt = &stamp
			failed := calls.lastError
			value.LastToolError = &failed
			if !calls.lastFinish.Before(calls.lastStart) {
				value.LastActivityAt = &stamp
			}
		}
	}
	return m.collectorEpoch, value, true, m.collectorChanged
}
