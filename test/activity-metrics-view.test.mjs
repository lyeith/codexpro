import assert from 'node:assert/strict';
import test from 'node:test';
import { Window } from 'happy-dom';
import { collectToolCallMetrics } from '../dist/activityDashboard/metrics.js';
import { renderToolCallMetrics } from '../dist/activityDashboard/metricsView.js';
import { reconcileDashboardElement } from '../dist/activityDashboard/refresh.js';
import { renderActivityDashboardData, renderActivityDashboardPage } from '../dist/activityDashboard/page.js';

const now = Date.parse('2026-10-30T04:00:00Z');
function snapshot(actions, projectId) {
  return {
    generatedAt: new Date(now).toISOString(), audit: { enabled: true, action_count: actions.length, latest_sequence: actions.length },
    projects: [{ id: 'alpha', label: 'Alpha & Co', actions: [], git: { available: false, message: 'Not a repository', trackedChangedPaths: [], untrackedPaths: [] } }],
    recentActions: [], timelineNote: 'No retained actions',
    metrics: collectToolCallMetrics(actions, now, projectId)
  };
}
const action = (durationMs, status = 'succeeded') => ({ toolName: 'read', status, durationMs, projectId: 'alpha', finishedAt: new Date(now - 1).toISOString() });

test('metrics strip labels retained scope, outcomes and latency n in initial HTML and refresh JSON', () => {
  const data = snapshot([action(100), action(300, 'failed')], 'alpha');
  const html = renderToolCallMetrics(data);
  assert.match(html, /Last 60 min · retained journal · all outcomes · Alpha &amp; Co/);
  assert.match(html, /2 retained completed calls · 1 unsuccessful/);
  assert.match(html, /latency n=2/);
  assert.match(html, /Average tool-call latency<\/span><strong>200 ms/);
  assert.match(html, /Median tool-call latency<\/span><strong>200 ms/);
  assert.equal(renderActivityDashboardData(data).sections.find(section => section.key === 'metrics').html, html);
  assert.ok(renderActivityDashboardPage(data).includes(html));
  const empty = renderToolCallMetrics(snapshot([]));
  assert.match(empty, /Tool calls \/ hour<\/span><strong>0/);
  assert.match(empty, /Average tool-call latency<\/span><strong>—/);
  assert.match(empty, /latency n=0/);
});

test('metrics refresh updates changed values and retains unchanged metric nodes', async () => {
  const window = new Window();
  const document = window.document;
  document.body.innerHTML = renderToolCallMetrics(snapshot([action(100)]));
  const current = document.querySelector('[data-dashboard-section="metrics"]');
  const rate = current.querySelector('[data-refresh-key="call-rate"]');
  const meanText = current.querySelector('[data-refresh-key="call-mean"] strong').firstChild;
  const incoming = document.createElement('template');
  incoming.innerHTML = renderToolCallMetrics(snapshot([action(100), action(100)]));
  reconcileDashboardElement(current, incoming.content.firstElementChild);
  assert.equal(current.querySelector('[data-refresh-key="call-rate"]'), rate);
  assert.equal(rate.querySelector('strong').textContent, '2');
  assert.equal(current.querySelector('[data-refresh-key="call-mean"] strong').firstChild, meanText);
  await window.happyDOM.close();
});
