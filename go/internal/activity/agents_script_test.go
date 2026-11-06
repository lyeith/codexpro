package activity

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedAgentsScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only JavaScript syntax verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--check", "--input-type=commonjs")
	command.Stdin = strings.NewReader(agentsScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("agents JavaScript does not parse: %v\n%s", err, output)
	}
}

func TestAgentsVisiblePollingPreservesDetailsAndReportsFailures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only agents DOM verification")
	}
	domModule, err := filepath.Abs("../../../node_modules/happy-dom/lib/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(domModule); err != nil {
		t.Skip("happy-dom is unavailable; run npm ci for development-only agents DOM verification")
	}
	initial := agentsViewFixture()
	initial.Agents[0].Tools = []AgentToolTally{{Tool: "read", Count: 42}, {Tool: "bash", Count: 12}, {Tool: "jobs", Count: 4}, {Tool: "search", Count: 2}}
	updated := agentsViewFixture()
	updated.GeneratedAt = "2026-11-06T16:01:00.000Z"
	updated.WindowStartedAt = "2026-11-06T10:01:00.000Z"
	updated.Agents[0].LastCommandAt = "2026-11-06T16:00:55.000Z"
	updated.Agents[0].CallsLastHour = 30
	updated.Agents[0].CallsSixHours = 72
	updated.Agents[0].Tools = []AgentToolTally{{Tool: "read", Count: 54}, {Tool: "bash", Count: 12}, {Tool: "jobs", Count: 4}, {Tool: "search", Count: 2}}
	updated.Totals.CallsLastHour = 42
	updated.Totals.CallsSixHours = 96
	rate1, rate2, rate3 := 14.352159, 9.917355, 7.741935
	updated.Agents[0].CallsPerHour = &rate1
	updated.Agents[1].CallsPerHour = &rate2
	updated.Agents[2].CallsPerHour = &rate3
	partial := updated
	partial.Partial = true
	partial.Note = "Metrics collection began within this window."
	unknown := updated
	unknown.Known = false
	unknown.GeneratedAt = "2026-11-06T16:02:00.000Z"
	unknown.WindowStartedAt = "2026-11-06T10:02:00.000Z"
	unknown.Note = "Metrics could not be read."
	fixtures, err := json.Marshal(map[string]any{
		"page":    RenderAgentsPage(initial),
		"updated": RenderAgentsData(updated),
		"partial": RenderAgentsData(partial),
		"unknown": RenderAgentsData(unknown),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-", domModule)
	command.Stdin = strings.NewReader("const fixtures = " + string(fixtures) + ";\n" + agentsDOMTest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("agents page behavior failed: %v\n%s", err, output)
	}
}

const agentsDOMTest = `
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const { Window } = await import(pathToFileURL(process.argv[2]).href);
const window = new Window({ url: 'https://example.test/activity?token=[REDACTED_SECRET]' });
window.location.hash = 'agents';
const document = window.document;
document.body.innerHTML = fixtures.page;
let hidden = false;
Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
const timers = [];
window.setInterval = (callback, delay) => { timers.push({ callback, delay }); return timers.length; };
const requested = [];
let next = fixtures.updated;
window.fetch = async (url) => {
  requested.push(new URL(url, 'https://example.test'));
  return { ok: true, json: async () => next };
};
window.eval(fixtures.page.match(/<script>([\s\S]*?)<\/script>/)[1]);
const section = () => document.querySelector('[data-dashboard-section="agents"]');
const status = () => document.querySelector('[data-agents-refresh-status]');
const entry = () => [...document.querySelectorAll('[data-agent-entry]')].find(row => row.dataset.agentKey === 'SP1');
const toggle = () => entry().querySelector('[data-agent-toggle]');
const sixHours = () => entry().querySelector('[data-agent-value="six-hours"]').textContent.trim();
const refresh = () => document.querySelector('[data-agents-refresh]');
const summary = () => entry().querySelector('[data-agent-tool-summary]');
const summaryValues = () => [...summary().querySelectorAll('li')].map(item => [item.querySelector('code').textContent, item.querySelector('.tool-summary-count').textContent.trim()]);

assert.equal(new URL(window.location.href).search, '', 'credential query must be removed from the address');
assert.equal(new URL(window.location.href).hash, '#agents');
assert.equal(requested.length, 0, 'initial server render needs no data fetch');
assert.equal(timers.length, 1, 'one polling owner');
assert.equal(timers[0].delay, 60000, 'the page polls at exactly 60 seconds');
assert.equal(sixHours(), '60');
assert.equal(toggle().getAttribute('aria-expanded'), 'false');
assert.equal(entry().querySelector('[data-agent-tools]').hidden, true);
assert.equal(summary().closest('[hidden]'), null, 'top tool counts are visible before expansion');
assert.equal(summary().closest('.agent-row'), entry().querySelector('.agent-row'));
assert.deepEqual(summaryValues(), [['read', '42'], ['bash', '12'], ['jobs', '4']]);

toggle().click();
toggle().focus();
assert.equal(toggle().getAttribute('aria-expanded'), 'true');
assert.equal(entry().querySelector('[data-agent-tools]').hidden, false);
assert.equal(requested.length, 0, 'tool details use the already-rendered tally');
assert.deepEqual([...entry().querySelectorAll('.tool-tally code')].map(tool => tool.textContent), ['read', 'bash', 'jobs', 'search'], 'expansion includes tools beyond the compact summary');

hidden = true;
assert.equal(await timers[0].callback(), false);
assert.equal(requested.length, 0, 'hidden pages do not poll');

hidden = false;
assert.equal(await timers[0].callback(), true);
assert.equal(requested.length, 1);
assert.equal(requested[0].pathname, '/activity/data', 'only the metrics projection is fetched');
assert.equal(requested[0].searchParams.get('codexpro_token'), '[REDACTED_SECRET]');
assert.equal(sixHours(), '72');
assert.deepEqual(summaryValues(), [['read', '54'], ['bash', '12'], ['jobs', '4']], 'top tool counts refresh with the row');
assert.equal(toggle().getAttribute('aria-expanded'), 'true', 'expanded tools survive refresh');
assert.equal(entry().querySelector('[data-agent-tools]').hidden, false);
assert.equal(document.activeElement, toggle(), 'keyboard focus survives refresh');
assert.equal(status().dataset.state, 'current');
assert.equal(section().querySelector('[data-agents-updated]').textContent, '06 Nov 2026, 16:01:00 UTC');

const lastGood = section().outerHTML;
window.fetch = async () => { throw new Error('private failure text must not be displayed'); };
assert.equal(await window.refreshAgents(), false);
assert.equal(section().outerHTML, lastGood, 'read failure must retain the last good snapshot');
assert.equal(status().dataset.state, 'failed');
assert.match(status().textContent, /Update failed\. Showing the last snapshot/);
assert.equal(status().textContent.includes('private failure text'), false);
assert.equal(sixHours(), '72', 'failed acquisition must not become zero');
assert.equal(refresh().disabled, false);

next = fixtures.partial;
window.fetch = async (url) => {
  requested.push(new URL(url, 'https://example.test'));
  return { ok: true, json: async () => next };
};
refresh().click();
await window.happyDOM.waitUntilComplete();
assert.equal(section().dataset.partial, 'true');
assert.equal(sixHours(), '≥ 72');
assert.deepEqual(summaryValues(), [['read', '≥ 54'], ['bash', '≥ 12'], ['jobs', '≥ 4']], 'compact counts preserve incomplete coverage');
assert.equal(entry().querySelector('[data-agent-value="rate"]').textContent.trim(), '—');
assert.match(section().textContent, /Metrics history is incomplete/);
assert.equal(toggle().getAttribute('aria-expanded'), 'true');

next = fixtures.unknown;
assert.equal(await window.refreshAgents(), true);
assert.equal(section().dataset.known, 'false');
assert.equal(document.querySelectorAll('[data-agent-entry]').length, 0);
assert.equal(status().dataset.state, 'unknown');
for (const metric of section().querySelectorAll('[data-agent-metric]')) assert.equal(metric.textContent.trim(), '—');
assert.match(section().textContent, /Activity unavailable/);

next = fixtures.updated;
assert.equal(await window.refreshAgents(), true);
assert.equal(sixHours(), '72');
assert.equal(toggle().getAttribute('aria-expanded'), 'true', 'temporary unavailability does not lose expansion preference');
assert.equal(status().dataset.state, 'current');

const beforeMalformed = section().outerHTML;
next = { generatedAt: '2026-11-06T16:03:00.000Z', sections: [] };
assert.equal(await window.refreshAgents(), false);
assert.equal(section().outerHTML, beforeMalformed, 'invalid data is an explicit read failure');
assert.equal(status().dataset.state, 'failed');

let complete;
window.fetch = async (url) => {
  requested.push(new URL(url, 'https://example.test'));
  return new Promise(resolve => { complete = resolve; });
};
const pending = window.refreshAgents();
await Promise.resolve();
const accepted = requested.length;
assert.equal(await window.refreshAgents(), false, 'overlapping ticks share the active request');
assert.equal(requested.length, accepted);
assert.equal(refresh().disabled, true);
complete({ ok: true, json: async () => fixtures.updated });
assert.equal(await pending, true);
assert.equal(refresh().disabled, false);
toggle().click();
assert.equal(toggle().getAttribute('aria-expanded'), 'false');
assert.equal(entry().querySelector('[data-agent-tools]').hidden, true);
assert.equal(summary().closest('[hidden]'), null, 'collapsing details keeps top tool counts visible');
assert.deepEqual(summaryValues(), [['read', '54'], ['bash', '12'], ['jobs', '4']]);
await window.happyDOM.close();
`
