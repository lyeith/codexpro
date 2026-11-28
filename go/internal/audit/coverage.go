package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// The journal records every acknowledged action, but some intervals can miss
// actions: appends were failing (the writer was in its fail state), or the
// previous writer stopped without closing, so calls in flight then were never
// recorded. Each interval is kept, bounded, in the durable state. A consumer
// asking about a window that starts at or after complete_since gets a complete
// answer while appends are healthy; older intervals stay visible.

const maxIncompleteRanges = 32

type incompleteRange struct {
	From   string `json:"from,omitempty"` // empty when the start is unknown
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

func addIncomplete(st *state, r incompleteRange) {
	st.Incomplete = append(st.Incomplete, r)
	if n := len(st.Incomplete); n > maxIncompleteRanges {
		st.Incomplete = append([]incompleteRange(nil), st.Incomplete[n-maxIncompleteRanges:]...)
	}
}

func (j *Journal) persistState(st state) error {
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err = writeState(context.Background(), tx, st); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// noteStartup runs once per process after load. prior is the durable state
// before recovery: a writer that never closed, or a torn tail found by this
// recovery, leaves an interval ending now. A journal written before coverage
// tracking keeps its earlier recoveries visible, and completeness is claimed
// only from the first tracked start.
func (j *Journal) noteStartup(prior state) error {
	now := timestamp(j.options.Clock())
	st := j.state
	if prior.CoverageSince == "" {
		st.CoverageSince = now
		if prior.Malformed > 0 {
			until := j.lastRecoveryAt()
			if until == "" {
				until = now
			}
			addIncomplete(&st, incompleteRange{Until: until, Reason: "recovered_before_coverage_tracking"})
		}
	} else if prior.WriterOpen || st.Malformed > prior.Malformed {
		addIncomplete(&st, incompleteRange{From: j.latestOccurredAt(), Until: now, Reason: "unclean_shutdown"})
	}
	st.WriterOpen = true
	if err := j.persistState(st); err != nil {
		return err
	}
	j.state = st
	return nil
}

// closeCoverage records a clean stop. A writer still in its fail state closes
// its interval here, from the durable state rather than the in-memory one,
// which may have advanced past the index. If this write fails the durable
// writer_open flag remains and the next start records the interval instead.
func (j *Journal) closeCoverage() error {
	st, err := j.loadState()
	if err != nil {
		return err
	}
	if j.broken != nil {
		addIncomplete(&st, incompleteRange{From: j.brokenAt, Until: timestamp(j.options.Clock()), Reason: "append_failed"})
	}
	st.WriterOpen = false
	return j.persistState(st)
}

// latestOccurredAt is the start time of the newest durable record, the last
// point known to be recorded before an unclean stop.
func (j *Journal) latestOccurredAt() string {
	if len(j.entries) == 0 {
		return ""
	}
	a, err := j.readEntry(j.entries[len(j.entries)-1])
	if err != nil {
		return ""
	}
	return text(a["occurred_at"])
}

// lastRecoveryAt is the time of the newest torn-tail receipt.
func (j *Journal) lastRecoveryAt() string {
	f, err := os.Open(filepath.Join(j.options.Directory, "recovery.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	latest := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var receipt struct {
			At string `json:"at"`
		}
		if json.Unmarshal(scanner.Bytes(), &receipt) == nil && receipt.At > latest {
			latest = receipt.At
		}
	}
	return latest
}

// coverageLocked reports history completeness. complete_since is null while
// no window is complete: appends are failing, the journal has an unresolved
// gap, or auditing is disabled.
func (j *Journal) coverageLocked() map[string]any {
	ranges := []any{}
	for _, r := range j.state.Incomplete {
		ranges = append(ranges, map[string]any{"from": nullString(r.From), "until": r.Until, "reason": r.Reason})
	}
	healthy := j.options.Enabled && j.broken == nil && !j.state.Gap
	var completeSince any
	if healthy {
		since := j.state.CoverageSince
		if n := len(j.state.Incomplete); n > 0 && j.state.Incomplete[n-1].Until > since {
			since = j.state.Incomplete[n-1].Until
		}
		completeSince = nullString(since)
	}
	if j.broken != nil {
		ranges = append(ranges, map[string]any{"from": j.brokenAt, "until": nil, "reason": "append_failing"})
	}
	return map[string]any{
		"appends_healthy":      healthy,
		"append_failing_since": nullString(j.brokenAt),
		"complete_since":       completeSince,
		"incomplete_ranges":    ranges,
		"history_incomplete":   !healthy || len(ranges) > 0 || j.state.Malformed > 0,
	}
}

// Coverage is the history completeness report used by activity consumers.
func (j *Journal) Coverage() map[string]any {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.coverageLocked()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
