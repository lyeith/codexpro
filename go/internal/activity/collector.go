package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// CollectorStatus deliberately contains no project, request, job or principal
// identifiers. Labels are caller attribution, not proof of identity or work.
type CollectorStatus struct {
	Version    int               `json:"version"`
	ObservedAt string            `json:"observed_at"`
	Health     CollectorHealth   `json:"health"`
	Activity   CollectorActivity `json:"activity"`
}

type CollectorHealth struct {
	Known bool `json:"known"`
	OK    bool `json:"ok"`
}

type CollectorActivity struct {
	Known  bool             `json:"known"`
	Scope  string           `json:"scope"`
	Agents []CollectorAgent `json:"agents,omitempty"`
}

type CollectorAgent struct {
	Label          string  `json:"label"`
	InFlightCalls  int     `json:"inflight_calls"`
	InFlightJobs   int     `json:"inflight_jobs"`
	LastActivityAt *string `json:"last_activity_at"`
}

func collectorLabel(s string) int {
	if len(s) == 3 && s[0:2] == "SP" && s[2] >= '1' && s[2] <= '9' {
		return int(s[2] - '1')
	}
	return -1
}

// CollectStatus reuses the journal's current calls and retained receipts and
// the job owner's snapshot. It neither reads output nor contacts work runs.
func (h *Handler) CollectStatus(ctx context.Context) CollectorStatus {
	observed := h.options.Clock()
	result := CollectorStatus{Version: 1, ObservedAt: timestamp(observed),
		Health: CollectorHealth{Known: true, OK: true}, Activity: CollectorActivity{Scope: "all_projects"}}
	if ctx.Err() != nil || !h.options.Audit.Enabled() || h.options.Jobs == nil {
		return result
	}
	agents := make([]CollectorAgent, 9)
	latest := make([]time.Time, 9)
	for i := range agents {
		agents[i].Label = "SP" + string(rune('1'+i))
	}
	observe := func(i int, raw string) bool {
		if raw == "" {
			return false
		}
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil || at.IsZero() || at.After(observed) {
			return false
		}
		if at.After(latest[i]) {
			latest[i] = at
		}
		return true
	}
	for _, call := range h.options.Audit.InFlight() {
		if i := collectorLabel(call.Agent); i >= 0 {
			agents[i].InFlightCalls++
			if !observe(i, call.StartedAt) {
				return result
			}
		}
	}
	for _, job := range h.options.Jobs.Snapshot() {
		if i := collectorLabel(job.Agent); i >= 0 {
			if !observe(i, job.StartedAt) {
				return result
			}
			if job.Status == "running" {
				if job.FinishedAt != "" {
					return result
				}
				agents[i].InFlightJobs++
			} else {
				if !observe(i, job.FinishedAt) {
					return result
				}
				start, _ := time.Parse(time.RFC3339Nano, job.StartedAt)
				finish, _ := time.Parse(time.RFC3339Nano, job.FinishedAt)
				if finish.Before(start) {
					return result
				}
			}
		}
	}
	retained := h.options.Audit.CollectEventTimes(ctx, observed)
	if !retained.Known || len(retained.Labels) != len(agents) {
		return result
	}
	for i, event := range retained.Labels {
		if event.Label != agents[i].Label {
			return result
		}
		if event.Last != nil && event.Last.After(latest[i]) {
			latest[i] = *event.Last
		}
		if !latest[i].IsZero() {
			at := timestamp(latest[i])
			agents[i].LastActivityAt = &at
		}
	}
	if ctx.Err() == nil {
		result.Activity.Known, result.Activity.Agents = true, agents
	}
	return result
}

func (h *Handler) serveCollector(w http.ResponseWriter, r *http.Request) {
	value := h.CollectStatus(r.Context())
	body, err := json.Marshal(value)
	if err != nil || len(body) > 16<<10 {
		http.Error(w, "Status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
