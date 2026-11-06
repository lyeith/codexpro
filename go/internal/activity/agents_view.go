package activity

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const agentUnavailable = `<span class="unavailable" aria-label="Unavailable">—</span>`

func agentDisplayLabel(agent string) string {
	if agent == "" {
		return "Untagged"
	}
	return agent
}

func agentDisplayNumber(value int) string {
	s := strconv.Itoa(value)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func agentCountHTML(value int, known, partial bool) string {
	if !known || value < 0 {
		return agentUnavailable
	}
	number := agentDisplayNumber(value)
	if partial {
		return `<span class="observed-count" aria-label="At least ` + number + ` observed">≥ ` + number + `</span>`
	}
	return number
}

func agentRateHTML(value *float64, partial bool) string {
	if partial || value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return agentUnavailable
	}
	return strconv.FormatFloat(*value, 'f', 1, 64)
}

func agentUTC(value string) string {
	at, ok := parseTime(value)
	if !ok {
		return "Time unavailable"
	}
	return at.UTC().Format("02 Jan 2006, 15:04:05 UTC")
}

func agentRelativeTime(at, observed time.Time) string {
	elapsed := observed.Sub(at)
	switch {
	case elapsed < 0:
		return "Time unavailable"
	case elapsed < 5*time.Second:
		return "Just now"
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	default:
		hours, minutes := int(elapsed.Hours()), int(elapsed.Minutes())%60
		if minutes == 0 {
			return fmt.Sprintf("%dh ago", hours)
		}
		return fmt.Sprintf("%dh %dm ago", hours, minutes)
	}
}

func agentLastCommandHTML(value, generated string) string {
	at, validTime := parseTime(value)
	observed, validObservation := parseTime(generated)
	if !validTime || !validObservation || at.After(observed) {
		return agentUnavailable
	}
	return `<span class="last-command"><span class="agent-relative">` + escape(agentRelativeTime(at, observed)) +
		`</span><time class="agent-utc" datetime="` + escape(value) + `">` + escape(agentUTC(value)) + `</time></span>`
}

func agentWindowLabel(started, ended string) string {
	start, startOK := parseTime(started)
	end, endOK := parseTime(ended)
	if !startOK || !endOK || end.Before(start) {
		return "Window unavailable"
	}
	start, end = start.UTC(), end.UTC()
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return start.Format("02 Jan 2006, 15:04") + "–" + end.Format("15:04 UTC")
	}
	return start.Format("02 Jan 2006, 15:04") + " – " + end.Format("02 Jan 2006, 15:04 UTC")
}

func renderAgentMetric(label, key string, value int, snapshot AgentDashboard) string {
	return fmt.Sprintf(`<div class="agent-total"><dt>%s</dt><dd data-agent-metric="%s">%s</dd></div>`,
		escape(label), escape(key), agentCountHTML(value, snapshot.Known, snapshot.Partial))
}

func renderAgentEntry(agent AgentSummary, snapshot AgentDashboard) string {
	label := agentDisplayLabel(agent.Agent)
	detailID := "agent-tools-untagged"
	if agent.Agent != "" {
		detailID = fmt.Sprintf("agent-tools-%x", []byte(agent.Agent))
	}
	state := `<span class="agent-state idle"><span class="state-dot" aria-hidden="true"></span>Idle</span>`
	if agent.RunningCalls > 0 {
		state = fmt.Sprintf(`<span class="agent-state running" title="%s in-flight calls"><span class="state-dot" aria-hidden="true"></span>Running now<span class="running-count" aria-label="%s calls">%s</span></span>`,
			escape(agentDisplayNumber(agent.RunningCalls)), escape(agentDisplayNumber(agent.RunningCalls)), escape(agentDisplayNumber(agent.RunningCalls)))
	}
	var tools strings.Builder
	for _, tool := range agent.Tools {
		fmt.Fprintf(&tools, `<li><code>%s</code><span>%s</span></li>`, escape(tool.Tool), agentCountHTML(tool.Count, true, snapshot.Partial))
	}
	firstLabel := "First call in window"
	if snapshot.Partial {
		firstLabel = "First observed call in window"
	}
	first := agentUnavailable
	if _, ok := parseTime(agent.FirstCommandAt); ok {
		first = `<time datetime="` + escape(agent.FirstCommandAt) + `">` + escape(agentUTC(agent.FirstCommandAt)) + `</time>`
	}
	return fmt.Sprintf(`<tbody class="agent-entry" data-agent-entry data-agent-key="%s">
<tr class="agent-row">
<th class="agent-name" scope="row"><button class="agent-toggle" type="button" data-agent-toggle aria-expanded="false" aria-controls="%s" aria-label="Show tool counts for %s"><span class="agent-chevron" aria-hidden="true"></span><code>%s</code></button></th>
<td class="agent-status" data-label="State">%s</td>
<td class="agent-last" data-label="Last command">%s</td>
<td class="agent-number" data-label="Calls · last hour" data-agent-value="last-hour">%s</td>
<td class="agent-number" data-label="Calls/hour" data-agent-value="rate">%s</td>
<td class="agent-number" data-label="Calls · 6 hours" data-agent-value="six-hours">%s</td>
<td class="agent-number" data-label="Errors" data-agent-value="errors">%s</td>
</tr>
<tr class="agent-detail-row" id="%s" data-agent-tools hidden><td colspan="7"><div class="agent-tools-panel" role="region" aria-label="Tool counts for %s"><div class="agent-tools-heading"><h2>Tool calls · last 6 hours</h2><p>%s: %s</p></div><ul class="tool-tally">%s</ul></div></td></tr>
</tbody>`,
		escape(agent.Agent), detailID, escape(label), escape(label), state, agentLastCommandHTML(agent.LastCommandAt, snapshot.GeneratedAt),
		agentCountHTML(agent.CallsLastHour, true, snapshot.Partial), agentRateHTML(agent.CallsPerHour, snapshot.Partial),
		agentCountHTML(agent.CallsSixHours, true, snapshot.Partial), agentCountHTML(agent.Errors, true, snapshot.Partial),
		detailID, escape(label), escape(firstLabel), first, tools.String())
}

func renderAgentsSection(snapshot AgentDashboard) string {
	coverage, coverageClass := "Recorded activity", ""
	if snapshot.Partial {
		coverage, coverageClass = "Incomplete history", " partial"
	}
	if !snapshot.Known {
		coverage, coverageClass = "Unavailable", " unknown"
	}
	var body strings.Builder
	fmt.Fprintf(&body, `<section class="agents-section" data-dashboard-section="agents" data-generated-at="%s" data-known="%t" data-partial="%t" aria-labelledby="agents-title">
<div class="agents-heading"><div><p class="eyebrow">Activity by agent tag</p><h1 id="agents-title">Active agents · last 6 hours</h1><p class="window-range">%s</p></div><span class="coverage-chip%s">%s</span></div>`,
		escape(snapshot.GeneratedAt), snapshot.Known, snapshot.Partial, escape(agentWindowLabel(snapshot.WindowStartedAt, snapshot.GeneratedAt)),
		coverageClass, escape(coverage))
	if !snapshot.Known {
		body.WriteString(`<p class="coverage-note unknown-note">Activity could not be read. Agent states and counts are unavailable.</p>`)
	} else if snapshot.Partial {
		body.WriteString(`<p class="coverage-note partial-note">Metrics history is incomplete. Observed counts are lower bounds; full six-hour totals and first-call rates are unavailable.</p>`)
	}
	if snapshot.Note != "" {
		body.WriteString(`<p class="source-note">` + escape(snapshot.Note) + `</p>`)
	}
	body.WriteString(`<dl class="agent-totals" aria-label="Activity totals">`)
	body.WriteString(renderAgentMetric("Active agents", "agents", snapshot.Totals.ActiveAgents, snapshot))
	body.WriteString(renderAgentMetric("Calls · last hour", "last-hour", snapshot.Totals.CallsLastHour, snapshot))
	body.WriteString(renderAgentMetric("Calls · 6 hours", "six-hours", snapshot.Totals.CallsSixHours, snapshot))
	body.WriteString(renderAgentMetric("Calls running now", "running", snapshot.Totals.RunningCalls, snapshot))
	body.WriteString("</dl>")
	switch {
	case !snapshot.Known:
		body.WriteString(`<div class="agents-empty"><h2>Activity unavailable</h2><p>Refresh to try again. No activity state can be inferred from this snapshot.</p></div>`)
	case len(snapshot.Agents) == 0:
		if snapshot.Partial {
			body.WriteString(`<div class="agents-empty"><h2>No calls observed yet</h2><p>Metrics history is incomplete.</p></div>`)
		} else {
			body.WriteString(`<div class="agents-empty"><h2>No calls in this window</h2><p>No recorded tool calls started in the last six hours.</p></div>`)
		}
	default:
		body.WriteString(`<div class="agents-table-wrap"><table class="agents-table" aria-describedby="agent-counts-note"><caption class="visually-hidden">Tool-call activity by agent during the last six hours</caption><thead><tr><th scope="col">Agent</th><th scope="col">State</th><th scope="col">Last command</th><th scope="col" class="number-heading">Last hour</th><th scope="col" class="number-heading">Calls/hour</th><th scope="col" class="number-heading">6-hour calls</th><th scope="col" class="number-heading">Errors</th></tr></thead>`)
		for _, agent := range snapshot.Agents {
			body.WriteString(renderAgentEntry(agent, snapshot))
		}
		body.WriteString("</table></div>")
	}
	body.WriteString(`<p class="counts-note" id="agent-counts-note">Calls are counted when they start. Errors include failed, timed out, cancelled and blocked calls. Calls/hour uses the time since the first call in this window. Running now includes all in-flight calls for the agents shown.</p>`)
	updatedLabel := "Updated"
	if !snapshot.Known {
		updatedLabel = "Checked"
	}
	fmt.Fprintf(&body, `<div class="agents-updated"><span>%s <time data-agents-updated datetime="%s">%s</time></span><span>Agent tags are caller-supplied labels.</span></div></section>`,
		updatedLabel, escape(snapshot.GeneratedAt), escape(agentUTC(snapshot.GeneratedAt)))
	return body.String()
}

// RenderAgentsData contains only the narrow agent projection. Existing command,
// project, and job views keep their separate HTTP owners.
func RenderAgentsData(snapshot AgentDashboard) DashboardData {
	return DashboardData{GeneratedAt: snapshot.GeneratedAt, Sections: []DashboardSection{
		{Key: "agents", HTML: renderAgentsSection(snapshot)},
	}}
}

func RenderAgentsPage(snapshot AgentDashboard) string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><link rel="icon" href="/favicon.ico"><title>Active agents · CodexPro</title><style>` +
		agentsStyle + `</style></head><body><main class="agents-main"><header class="page-bar"><span class="brand"><span class="brand-mark" aria-hidden="true"></span>CodexPro<span class="brand-divider" aria-hidden="true">/</span><span class="brand-area">Activity</span></span><button class="refresh-button" type="button" data-agents-refresh>Refresh</button></header>` +
		renderAgentsSection(snapshot) + `<footer class="page-foot"><p data-agents-refresh-status role="status" aria-live="polite">Refreshes every 60 seconds while this page is visible.</p></footer></main><script>` +
		agentsScript + `</script></body></html>`
}
