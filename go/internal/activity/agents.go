package activity

import (
	"context"
	"strings"
	"time"
)

const agentActivityWindow = 6 * time.Hour

// AgentDashboard is a metadata-only projection. It deliberately has no command,
// project, workspace, identity, argument, output, or file fields.
type AgentDashboard struct {
	GeneratedAt     string         `json:"generatedAt"`
	WindowStartedAt string         `json:"windowStartedAt"`
	Known           bool           `json:"known"`
	Partial         bool           `json:"partial"`
	Note            string         `json:"note"`
	Agents          []AgentSummary `json:"agents"`
	Totals          AgentTotals    `json:"totals"`
}

type AgentTotals struct {
	ActiveAgents  int
	CallsLastHour int
	CallsSixHours int
	RunningCalls  int
}

type AgentSummary struct {
	Agent          string
	LastCommandAt  string
	FirstCommandAt string
	CallsLastHour  int
	CallsSixHours  int
	Errors         int
	RunningCalls   int
	CallsPerHour   *float64
	Tools          []AgentToolTally
}

type AgentToolTally struct {
	Tool  string
	Count int
}

func unknownAgents(now time.Time, note string) AgentDashboard {
	return AgentDashboard{
		GeneratedAt: timestamp(now), WindowStartedAt: timestamp(now.Add(-agentActivityWindow)),
		Note: note, Agents: []AgentSummary{},
	}
}

// CollectAgents reads only the compact metrics owner. It never reads the audit
// journal, job output, project repositories, or command history.
func (h *Handler) CollectAgents(ctx context.Context) AgentDashboard {
	if ctx.Err() != nil || h.options.Metrics == nil {
		return unknownAgents(h.options.Clock(), "Metrics are unavailable. Counts and running state are unknown.")
	}
	return h.options.Metrics.Snapshot()
}

// Labels retain their case-sensitive identity. Natural display ordering compares
// numeric runs by value, then uses the exact label as a deterministic tie-break.
func agentLabelLess(a, b string) bool {
	if a == "" || b == "" {
		return a != "" && b == ""
	}
	left, right := strings.ToLower(a), strings.ToLower(b)
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	for i, j := 0, 0; i < len(left) || j < len(right); {
		if i == len(left) || j == len(right) {
			if len(left)-i != len(right)-j {
				return i == len(left)
			}
			break
		}
		if digit(left[i]) && digit(right[j]) {
			ii, jj := i, j
			for ii < len(left) && digit(left[ii]) {
				ii++
			}
			for jj < len(right) && digit(right[jj]) {
				jj++
			}
			x, y := strings.TrimLeft(left[i:ii], "0"), strings.TrimLeft(right[j:jj], "0")
			if len(x) != len(y) {
				return len(x) < len(y)
			}
			if x != y {
				return x < y
			}
			i, j = ii, jj
			continue
		}
		if left[i] != right[j] {
			return left[i] < right[j]
		}
		i++
		j++
	}
	return a < b
}
