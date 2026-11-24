package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// CollectorStatus contains only scalar activity. Labels are caller attribution,
// not proof of identity; counters are comparable only within the same epoch.
type CollectorStatus struct {
	Version    int               `json:"version"`
	Epoch      string            `json:"epoch"`
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
	Label              string  `json:"label"`
	InFlightCalls      int     `json:"inflight_calls"`
	InFlightJobs       int     `json:"inflight_jobs"`
	QueuedJobs         int     `json:"queued_jobs"`
	StartedCalls       uint64  `json:"started_calls"`
	CompletedCalls     uint64  `json:"completed_calls"`
	FailedCalls        uint64  `json:"failed_calls"`
	Revision           uint64  `json:"revision"`
	LastActivityAt     *string `json:"last_activity_at"`
	LastToolStartedAt  *string `json:"last_tool_started_at"`
	LastToolFinishedAt *string `json:"last_tool_finished_at"`
	LastToolName       string  `json:"last_tool_name"`
	LastToolError      *bool   `json:"last_tool_error"`
}

type collectorJobSource interface {
	AgentActivity(string) (jobs.ActivityCounts, bool)
	ActivityChanged() <-chan struct{}
}

// CollectStatus keeps the original nine-label inventory for the Plans monitor.
// Both this inventory and the selected-label protocol use current owner counters,
// never audit files, output files, or retained job inventories.
func (h *Handler) CollectStatus(ctx context.Context) CollectorStatus {
	value, _, _ := h.collectStatus(ctx, "")
	return value
}

func (h *Handler) CollectAgentStatus(ctx context.Context, agent string) CollectorStatus {
	value, _, _ := h.collectStatus(ctx, agent)
	return value
}

func (h *Handler) collectStatus(ctx context.Context, agent string) (CollectorStatus, <-chan struct{}, <-chan struct{}) {
	result := CollectorStatus{Version: 2, ObservedAt: timestamp(h.options.Clock()),
		Health: CollectorHealth{Known: true, OK: true}, Activity: CollectorActivity{Scope: "all_projects"}}
	source, ok := h.options.Jobs.(collectorJobSource)
	if ctx.Err() != nil || h.options.Metrics == nil || !ok || agent != "" && !wire.ValidAgent(agent) {
		return result, nil, nil
	}
	// Capture the job signal before its counters: a concurrent commit either
	// appears in the snapshot or closes this channel. Calls capture both together.
	jobChanged := source.ActivityChanged()
	labels := []string{agent}
	if agent == "" {
		labels = []string{"SP1", "SP2", "SP3", "SP4", "SP5", "SP6", "SP7", "SP8", "SP9"}
	}
	agents := make([]CollectorAgent, 0, len(labels))
	var callChanged <-chan struct{}
	for _, label := range labels {
		var value CollectorAgent
		var state jobs.ActivityCounts
		for attempt := 0; ; attempt++ {
			epoch, current, known, changed := h.options.Metrics.collectorSnapshot(label)
			result.Epoch, callChanged = epoch, changed
			jobState, jobsKnown := source.AgentActivity(label)
			if !known || !jobsKnown {
				return result, callChanged, jobChanged
			}
			// Calls must stay stable across the job read. Otherwise the two
			// owners can report zero at different instants during continuous work.
			_, confirmed, stillKnown, changed := h.options.Metrics.collectorSnapshot(label)
			callChanged = changed
			if !stillKnown {
				return result, callChanged, jobChanged
			}
			if confirmed.Revision == current.Revision {
				value, state = current, jobState
				break
			}
			// Retry once for an ordinary crossing transition, then remain unknown
			// under sustained change. Never spin or report a mixed idle snapshot.
			if attempt == 1 {
				return result, callChanged, jobChanged
			}
		}
		value.InFlightJobs, value.QueuedJobs = state.InFlightJobs, state.QueuedJobs
		value.Revision += state.Revision
		observed := h.options.Clock()
		result.ObservedAt = timestamp(observed)
		if state.LastActivityAt.After(observed) {
			return result, callChanged, jobChanged
		}
		last := time.Time{}
		if value.LastActivityAt != nil {
			last, _ = time.Parse(time.RFC3339Nano, *value.LastActivityAt)
			if last.After(observed) {
				return result, callChanged, jobChanged
			}
		}
		if !state.LastActivityAt.IsZero() {
			if state.LastActivityAt.After(last) {
				stamp := timestamp(state.LastActivityAt)
				value.LastActivityAt = &stamp
			}
		}
		agents = append(agents, value)
	}
	if ctx.Err() == nil {
		result.Activity.Known, result.Activity.Agents = true, agents
	}
	return result, callChanged, jobChanged
}

func (h *Handler) waitCollector(ctx context.Context, query CollectorQuery) CollectorStatus {
	value, calls, jobs := h.collectStatus(ctx, query.Agent)
	if query.Wait <= 0 || !query.unchanged(value) {
		return value
	}
	timer := time.NewTimer(query.Wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			value, _, _ = h.collectStatus(ctx, query.Agent)
			return value
		case <-h.ctx.Done():
			value.Activity.Known, value.Activity.Agents = false, nil
			return value
		case <-timer.C:
			value, _, _ = h.collectStatus(ctx, query.Agent)
			return value
		case <-calls:
		case <-jobs:
		}
		value, calls, jobs = h.collectStatus(ctx, query.Agent)
		if !query.unchanged(value) {
			return value
		}
	}
}

// The original collector consumer requires exact keys, version 1 and nine rows.
// Keep that wire shape; queued work counts as in-flight there so it cannot appear
// idle. Version 2 separates running from queued work and adds completion fields.
func legacyCollector(value CollectorStatus) map[string]any {
	activity := map[string]any{"known": value.Activity.Known, "scope": value.Activity.Scope}
	if value.Activity.Known {
		agents := make([]map[string]any, 0, len(value.Activity.Agents))
		for _, agent := range value.Activity.Agents {
			agents = append(agents, map[string]any{"label": agent.Label,
				"inflight_calls": agent.InFlightCalls, "inflight_jobs": agent.InFlightJobs + agent.QueuedJobs,
				"last_activity_at": agent.LastActivityAt})
		}
		activity["agents"] = agents
	}
	return map[string]any{"version": 1, "observed_at": value.ObservedAt, "health": value.Health, "activity": activity}
}

func (h *Handler) serveCollector(w http.ResponseWriter, r *http.Request) {
	query, err := ParseCollectorQuery(r.URL)
	if err != nil {
		http.Error(w, "Invalid collector query", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodHead {
		query.Wait = 0
	}
	value := h.waitCollector(r.Context(), query)
	var response any = value
	if query.Agent == "" {
		response = legacyCollector(value)
	}
	body, err := json.Marshal(response)
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
