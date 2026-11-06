package audit

import (
	"context"
	"time"
)

// CollectorEventTimes describes only the currently retained journal. A nil
// timestamp means no retained observation, not that a label was never active.
// It deliberately carries no receipt, project, request or identity metadata.
type CollectorEventTimes struct {
	Known  bool
	Labels []CollectorLabelTime
}

type CollectorLabelTime struct {
	Label string
	Last  *time.Time
}

// CollectEventTimes makes one pass over retained receipts for all fixed SP
// labels. Sequence order is not event-time order: delayed job completions can
// be appended or recovered after newer calls. No all-history claim is made
// across intentional retention. Missing or damaged retained data is unknown.
func (j *Journal) CollectEventTimes(ctx context.Context, observed time.Time) CollectorEventTimes {
	unknown := CollectorEventTimes{}
	if ctx.Err() != nil || observed.IsZero() || !j.options.Enabled || !j.admitMu.TryRLock() {
		return unknown
	}
	defer j.admitMu.RUnlock()
	if j.closed || !j.mu.TryRLock() {
		return unknown
	}
	defer j.mu.RUnlock()
	if j.fatal != nil || j.state.Gap || j.state.Malformed != 0 {
		return unknown
	}
	labels := make([]CollectorLabelTime, 9)
	for i := range labels {
		labels[i].Label = "SP" + string(rune('1'+i))
	}
	for _, entry := range j.entries {
		if ctx.Err() != nil {
			return unknown
		}
		action, err := j.readEntry(entry)
		if err != nil {
			return unknown
		}
		label, _ := action["agent"].(string)
		if len(label) != 3 || label[:2] != "SP" || label[2] < '1' || label[2] > '9' {
			continue
		}
		start, okStart := action["occurred_at"].(string)
		finish, okFinish := action["finished_at"].(string)
		started, startErr := time.Parse(time.RFC3339Nano, start)
		finished, finishErr := time.Parse(time.RFC3339Nano, finish)
		if !okStart || !okFinish || startErr != nil || finishErr != nil || started.IsZero() ||
			finished.Before(started) || started.After(observed) || finished.After(observed) {
			return unknown
		}
		i := int(label[2] - '1')
		if labels[i].Last == nil || finished.After(*labels[i].Last) {
			at := finished.UTC()
			labels[i].Last = &at
		}
	}
	if ctx.Err() != nil {
		return unknown
	}
	return CollectorEventTimes{Known: true, Labels: labels}
}
