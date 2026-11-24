package jobs

import (
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Match the existing dashboard metric-cell bound without evicting counters.
const maxActivityLabels = 65536

// ActivityCounts is the scalar job projection for one caller label. Revision is
// process-local and advances only when this projection changes after a commit.
type ActivityCounts struct {
	InFlightJobs   int
	QueuedJobs     int
	LastActivityAt time.Time
	Revision       uint64
}

type jobActivity struct {
	ActivityCounts
	invalid int
}

type jobActivities struct {
	mu       sync.RWMutex
	ready    bool
	overflow bool
	byAgent  map[string]jobActivity
}

func (a *jobActivities) markReady() {
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
}

func (a *jobActivities) insert(records []Record, advance bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range records {
		a.changeLocked(nil, &records[i], advance)
	}
}

func (a *jobActivities) change(previous, next *Record) bool {
	if previous != nil && next != nil && previous.Agent == next.Agent &&
		previous.Status == next.Status && previous.StartedAt == next.StartedAt &&
		previous.FinishedAt == next.FinishedAt {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.changeLocked(previous, next, true)
}

func (a *jobActivities) changeLocked(previous, next *Record, advance bool) bool {
	var labels [2]string
	if previous != nil {
		labels[0] = previous.Agent
	}
	if next != nil {
		labels[1] = next.Agent
	}
	if a.byAgent == nil {
		a.byAgent = make(map[string]jobActivity)
	}
	changed := false
	for i, label := range labels {
		if !wire.ValidAgent(label) || i == 1 && label == labels[0] {
			continue
		}
		value, found := a.byAgent[label]
		if !found && len(a.byAgent) >= maxActivityLabels {
			a.overflow = true
			continue
		}
		before := value
		if previous != nil && previous.Agent == label {
			value.apply(*previous, -1)
		}
		if next != nil && next.Agent == label {
			value.apply(*next, 1)
		}
		if value.InFlightJobs != before.InFlightJobs ||
			value.QueuedJobs != before.QueuedJobs ||
			!value.LastActivityAt.Equal(before.LastActivityAt) ||
			(value.invalid == 0) != (before.invalid == 0) {
			changed = true
			if advance {
				value.Revision++
			}
		}
		a.byAgent[label] = value
	}
	return changed
}

func (a *jobActivity) apply(r Record, direction int) {
	switch r.Status {
	case "running":
		a.InFlightJobs += direction
	case "queued":
		a.QueuedJobs += direction
	}
	start, err := time.Parse(time.RFC3339Nano, r.StartedAt)
	valid := err == nil && !start.IsZero()
	last := start
	if r.Unfinished() {
		valid = valid && r.FinishedAt == ""
	} else {
		finish, err := time.Parse(time.RFC3339Nano, r.FinishedAt)
		valid = valid && err == nil && !finish.IsZero() && !finish.Before(start)
		last = finish
	}
	if !valid {
		a.invalid += direction
		return
	}
	// Retention must not erase the latest activity observed in this process.
	if direction > 0 && last.After(a.LastActivityAt) {
		a.LastActivityAt = last.UTC()
	}
}

func (a jobActivity) known(now time.Time) bool {
	return a.invalid == 0 && a.InFlightJobs >= 0 && a.QueuedJobs >= 0 &&
		!a.LastActivityAt.After(now)
}

// AgentActivity is O(1) regardless of retained job history. An unseen valid
// label is known empty unless the label bound was exceeded. Unavailable or
// malformed source state returns false, never an invented empty observation.
func (s *Service) AgentActivity(label string) (ActivityCounts, bool) {
	if s == nil || !wire.ValidAgent(label) || s.closed.Load() {
		return ActivityCounts{}, false
	}
	s.activity.mu.RLock()
	defer s.activity.mu.RUnlock()
	value, found := s.activity.byAgent[label]
	return value.ActivityCounts, s.activity.ready && (found || !s.activity.overflow) && value.known(time.Now())
}

// ActivityChanged must be captured before reading the aggregate, so a state
// transition between the read and the wait cannot be missed.
func (s *Service) ActivityChanged() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.changeSignal()
}
