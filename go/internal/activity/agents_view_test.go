package activity

import (
	"math"
	"strings"
	"testing"
)

func agentsViewFixture() AgentDashboard {
	rate1, rate2, rate3 := 12.0, 10.0, 8.0
	return AgentDashboard{
		GeneratedAt:     "2026-11-06T16:00:00.000Z",
		WindowStartedAt: "2026-11-06T10:00:00.000Z",
		Known:           true,
		Agents: []AgentSummary{
			{Agent: "SP1", FirstCommandAt: "2026-11-06T11:00:00.000Z", LastCommandAt: "2026-11-06T15:59:52.000Z",
				CallsLastHour: 18, CallsSixHours: 60, Errors: 2, RunningCalls: 2, CallsPerHour: &rate1,
				Tools: []AgentToolTally{{Tool: "read", Count: 42}, {Tool: "bash", Count: 18}}},
			{Agent: "SP2", FirstCommandAt: "2026-11-06T14:00:00.000Z", LastCommandAt: "2026-11-06T15:45:00.000Z",
				CallsLastHour: 8, CallsSixHours: 20, CallsPerHour: &rate2,
				Tools: []AgentToolTally{{Tool: "search", Count: 20}}},
			{Agent: "", FirstCommandAt: "2026-11-06T15:30:00.000Z", LastCommandAt: "2026-11-06T15:30:00.000Z",
				CallsLastHour: 4, CallsSixHours: 4, CallsPerHour: &rate3,
				Tools: []AgentToolTally{{Tool: "list_projects", Count: 4}}},
		},
		Totals: AgentTotals{ActiveAgents: 3, CallsLastHour: 30, CallsSixHours: 84, RunningCalls: 2},
	}
}

func TestAgentPageShowsOnlyAgentMetricsAndInlineTools(t *testing.T) {
	snapshot := agentsViewFixture()
	page := RenderAgentsPage(snapshot)
	for _, expected := range []string{
		"Active agents · last 6 hours",
		`<dd data-agent-metric="agents">3</dd>`,
		`<dd data-agent-metric="last-hour">30</dd>`,
		`<dd data-agent-metric="six-hours">84</dd>`,
		`<dd data-agent-metric="running">2</dd>`,
		`data-agent-key="SP1"`, `data-agent-key="SP2"`, `data-agent-key=""`,
		`aria-controls="agent-tools-535031"`,
		`id="agent-tools-535031" data-agent-tools hidden`,
		`aria-label="Show tool counts for Untagged"`,
		"Running now", ">Idle</span>", "8s ago", "15m ago",
		"06 Nov 2026, 15:59:52 UTC", "06 Nov 2026, 10:00–16:00 UTC",
		`data-agent-value="rate">12.0</td>`,
		`<li><code>read</code><span>42</span></li>`,
		`<li><code>bash</code><span>18</span></li>`,
		`data-label="Calls · last hour"`,
		`<th scope="col">Top tools</th>`, `data-label="Top tools · 6 hours"`,
		"failed, timed out, cancelled and blocked",
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("page is missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"Command history", "Project working trees", "data-project-filter",
		"/activity/live", "/activity/job", "/activity/diff", "/activity.json",
		"/inbox/v1", "/usage/v1/pro", "data-local-time",
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("metadata-only page contains %q", forbidden)
		}
	}
	one, two, untagged := strings.Index(page, `data-agent-key="SP1"`), strings.Index(page, `data-agent-key="SP2"`), strings.Index(page, `data-agent-key=""`)
	if !(one < two && two < untagged) {
		t.Fatal("the supplied natural agent order was not preserved")
	}
}

func TestAgentViewCollapsedToolsKeepTopThreeAndFullExpansion(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Agents[0].Tools = []AgentToolTally{
		{Tool: "zulu", Count: 42}, {Tool: "alpha", Count: 12},
		{Tool: "beta", Count: 4}, {Tool: "search", Count: 2},
	}
	entry := renderAgentEntry(snapshot.Agents[0], snapshot)
	_, summary, ok := strings.Cut(entry, `data-agent-tool-summary aria-label="Top tool counts for SP1">`)
	if !ok {
		t.Fatal("collapsed row has no tool summary")
	}
	summary, _, _ = strings.Cut(summary, "</ul>")
	for _, expected := range []string{
		`<code>zulu</code><span class="tool-summary-count">42</span>`,
		`<code>alpha</code><span class="tool-summary-count">12</span>`,
		`<code>beta</code><span class="tool-summary-count">4</span>`,
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("collapsed tally is missing %q", expected)
		}
	}
	if strings.Count(summary, "<li>") != 3 || strings.Contains(summary, "search") {
		t.Fatal("collapsed tally must contain only the first three supplied tools")
	}
	if !(strings.Index(summary, "zulu") < strings.Index(summary, "alpha") &&
		strings.Index(summary, "alpha") < strings.Index(summary, "beta")) {
		t.Fatal("collapsed tally changed the aggregate's tool order")
	}
	_, details, ok := strings.Cut(entry, `<ul class="tool-tally">`)
	if !ok {
		t.Fatal("full tool tally is missing")
	}
	details, _, _ = strings.Cut(details, "</ul>")
	if strings.Count(details, "<li>") != 4 || !strings.Contains(details, `<code>search</code><span>2</span>`) {
		t.Fatal("expansion must retain the complete tally")
	}
	if !strings.Contains(entry, `data-agent-tools hidden><td colspan="8">`) ||
		strings.Index(entry, "data-agent-tool-summary") > strings.Index(entry, "data-agent-tools hidden") {
		t.Fatal("the summary must stay in the collapsed row, outside hidden details")
	}
}

func TestAgentViewEmptyToolSummaryDoesNotInventCalls(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Agents[0].Tools = nil
	snapshot.Agents[0].CallsLastHour = 0
	snapshot.Agents[0].CallsSixHours = 0
	for _, partial := range []bool{false, true} {
		snapshot.Partial = partial
		entry := renderAgentEntry(snapshot.Agents[0], snapshot)
		expected := "No calls in window"
		if partial {
			expected = "No calls observed"
		}
		if !strings.Contains(entry, `<li class="tool-summary-empty">`+expected+"</li>") ||
			strings.Contains(entry, `class="tool-summary-count"`) {
			t.Fatalf("empty tool summary invented counts or coverage: partial=%t", partial)
		}
	}
}

func TestAgentDataIsOneConsistentMetricsSection(t *testing.T) {
	snapshot := agentsViewFixture()
	data := RenderAgentsData(snapshot)
	if data.GeneratedAt != "2026-11-06T16:00:00.000Z" || len(data.Sections) != 1 || data.Sections[0].Key != "agents" {
		t.Fatalf("unexpected metadata-only response: %+v", data)
	}
	if !strings.HasPrefix(data.Sections[0].HTML, `<section class="agents-section" data-dashboard-section="agents"`) {
		t.Fatal("agent data is not a single agents section")
	}
	if !strings.Contains(RenderAgentsPage(snapshot), data.Sections[0].HTML) {
		t.Fatal("initial page and refresh response disagree")
	}
}

func TestAgentViewUnknownDoesNotRenderZeroOrStaleAgentState(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Known = false
	snapshot.Note = "Metrics are unavailable."
	section := RenderAgentsData(snapshot).Sections[0].HTML
	for _, key := range []string{"agents", "last-hour", "six-hours", "running"} {
		if !strings.Contains(section, `data-agent-metric="`+key+`">`+agentUnavailable+`</dd>`) {
			t.Fatalf("unknown %s did not render unavailable", key)
		}
	}
	for _, forbidden := range []string{`data-agent-key="SP1"`, `data-agent-key="SP2"`, `data-agent-tool-summary`, ">Idle</span>", ">Running now", "No calls in this window"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("unknown snapshot invented or reused activity: %s", forbidden)
		}
	}
	if !strings.Contains(section, "<h2>Activity unavailable</h2>") || !strings.Contains(section, "Metrics are unavailable.") {
		t.Fatal("unknown snapshot needs a visible explanation")
	}
}

func TestAgentViewIncompleteHistoryUsesObservedCountsAndNoRate(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Partial = true
	snapshot.Note = "Metrics collection started within this window."
	section := RenderAgentsData(snapshot).Sections[0].HTML
	for _, expected := range []string{
		"Metrics history is incomplete.",
		"Incomplete history",
		`aria-label="At least 84 observed">≥ 84</span>`,
		`aria-label="At least 42 observed">≥ 42</span>`,
		`<code>read</code><span class="tool-summary-count"><span class="observed-count" aria-label="At least 42 observed">≥ 42</span></span>`,
		`data-agent-value="rate">` + agentUnavailable + "</td>",
		"First observed call in window",
		"Metrics collection started within this window.",
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("incomplete history is missing %q", expected)
		}
	}
	if strings.Contains(section, `data-agent-value="rate">12.0</td>`) {
		t.Fatal("an unprovable first-call rate was displayed")
	}
	for _, forbidden := range []string{"retained history", "retained call", "retained sample", "journal"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("metrics view assumes an audit source: %q", forbidden)
		}
	}
}

func TestAgentViewDistinguishesEmptyHistoryFromUnavailableHistory(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Agents = nil
	snapshot.Totals = AgentTotals{}
	section := RenderAgentsData(snapshot).Sections[0].HTML
	if !strings.Contains(section, "<h2>No calls in this window</h2>") ||
		!strings.Contains(section, `data-agent-metric="six-hours">0</dd>`) {
		t.Fatal("known empty history should report zero observed calls")
	}
	snapshot.Partial = true
	section = RenderAgentsData(snapshot).Sections[0].HTML
	if !strings.Contains(section, "<h2>No calls observed yet</h2>") ||
		strings.Contains(section, "<h2>No calls in this window</h2>") {
		t.Fatal("an incomplete empty history claimed the full window was empty")
	}
}

func TestAgentViewEscapesLabelsToolsNotesAndUsesSafeDetailIDs(t *testing.T) {
	snapshot := agentsViewFixture()
	snapshot.Note = `<script>alert("note")</script>`
	snapshot.Agents[0].Agent = `"><img src=x onerror="alert(1)">`
	snapshot.Agents[0].Tools[0].Tool = `read</code><script>alert("tool")</script>`
	section := RenderAgentsData(snapshot).Sections[0].HTML
	for _, forbidden := range []string{"<script>", "<img ", "</code><script>", `data-agent-key=""><img`} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("unescaped source content reached HTML: %q", forbidden)
		}
	}
	for _, expected := range []string{"&lt;script&gt;alert(&quot;note&quot;)&lt;/script&gt;", "&lt;img src=x onerror=&quot;alert(1)&quot;&gt;", "read&lt;/code&gt;&lt;script&gt;"} {
		if !strings.Contains(section, expected) {
			t.Fatalf("escaped text was lost: %q", expected)
		}
	}
	escapedTool := `read&lt;/code&gt;&lt;script&gt;alert(&quot;tool&quot;)&lt;/script&gt;`
	if strings.Count(section, escapedTool) != 2 {
		t.Fatal("tool name must remain escaped in both the summary and full tally")
	}
	if strings.Contains(section, `id="agent-tools-"><img`) {
		t.Fatal("agent tag was used directly as a detail id")
	}
}

func TestAgentViewUndefinedRatesAndTimesStayUnavailable(t *testing.T) {
	nan, infinity := math.NaN(), math.Inf(1)
	for _, value := range []*float64{nil, &nan, &infinity} {
		snapshot := agentsViewFixture()
		snapshot.Agents[0].CallsPerHour = value
		snapshot.Agents[0].LastCommandAt = "invalid"
		section := RenderAgentsData(snapshot).Sections[0].HTML
		if !strings.Contains(section, `data-agent-value="rate">`+agentUnavailable+"</td>") {
			t.Fatal("undefined rate must not become zero or infinity")
		}
		if !strings.Contains(section, `class="agent-last" data-label="Last command">`+agentUnavailable+"</td>") {
			t.Fatal("invalid command time must be unavailable")
		}
	}
}
