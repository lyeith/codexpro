import assert from 'node:assert/strict';
import test from 'node:test';
import { Window } from 'happy-dom';
import { reconcileDashboardElement } from '../dist/activityDashboard/refresh.js';
import { renderActivityDashboardData, renderActivityDashboardPage } from '../dist/activityDashboard/page.js';

function documentWith(html) {
  const window = new Window({ url: 'https://example.test/activity' });
  window.document.body.innerHTML = html;
  return window.document;
}

test('dashboard reconciliation updates changed rows, retains unchanged nodes, inserts new rows and removes expired rows', () => {
  const document = documentWith('<section><div data-refresh-key="rows"><p data-action-id="same"><b>Unchanged</b></p><p data-action-id="changed">Before</p><p data-action-id="expired">Expired</p></div></section>');
  const current = document.querySelector('section');
  const same = current.querySelector('[data-action-id="same"]');
  const sameText = same.querySelector('b').firstChild;
  const changed = current.querySelector('[data-action-id="changed"]');
  const incoming = document.createElement('section');
  incoming.innerHTML = '<div data-refresh-key="rows"><p data-action-id="new">New</p><p data-action-id="same"><b>Unchanged</b></p><p data-action-id="changed">After</p></div>';
  reconcileDashboardElement(current, incoming);
  assert.equal(current.querySelector('[data-action-id="same"]'), same);
  assert.equal(same.querySelector('b').firstChild, sameText);
  assert.equal(current.querySelector('[data-action-id="changed"]'), changed);
  assert.equal(changed.textContent, 'After');
  assert.deepEqual([...current.querySelectorAll('[data-action-id]')].map((row) => row.dataset.actionId), ['new', 'same', 'changed']);
  assert.equal(current.querySelector('[data-action-id="expired"]'), null);
});

test('dashboard reconciliation preserves expanded details, project filters and loaded islands until their revision changes', () => {
  const document = documentWith('<section><select data-project-filter><option value="">All</option><option value="alpha">Alpha</option></select><details data-live-id="job" open><summary>Before</summary><div data-refresh-preserve data-job-href="/activity/job"><pre>Loaded output</pre></div><div data-refresh-preserve data-refresh-revision="one" data-diff-state="loaded"><pre>Loaded diff</pre></div></details></section>');
  const current = document.querySelector('section');
  const filter = current.querySelector('select');
  filter.value = 'alpha';
  const card = current.querySelector('details');
  const output = current.querySelector('[data-job-href] pre');
  const diff = current.querySelector('[data-diff-state] pre');
  const incoming = document.createElement('section');
  incoming.innerHTML = '<select data-project-filter><option value="" selected>All</option><option value="alpha">Alpha</option></select><details data-live-id="job"><summary>After</summary><div data-refresh-preserve data-job-href="/activity/job">Expand to load</div><div data-refresh-preserve data-refresh-revision="one" data-diff-state="idle">Loading diff</div></details>';
  reconcileDashboardElement(current, incoming);
  assert.equal(current.querySelector('details'), card);
  assert.equal(card.open, true);
  assert.equal(card.querySelector('summary').textContent, 'After');
  assert.equal(current.querySelector('select'), filter);
  assert.equal(filter.value, 'alpha');
  assert.equal(current.querySelector('[data-job-href] pre'), output);
  assert.equal(current.querySelector('[data-diff-state] pre'), diff);
  incoming.querySelector('[data-diff-state]').dataset.refreshRevision = 'two';
  reconcileDashboardElement(current, incoming);
  assert.equal(current.querySelector('[data-diff-state]').textContent, 'Loading diff');
  assert.equal(current.querySelector('[data-diff-state]').dataset.diffState, 'idle');
  assert.equal(current.querySelector('[data-job-href] pre'), output);
});

function action(id, headline = id) {
  return {
    actionId: id, sequence: 1, finishedAt: '2026-10-30T01:00:00.000Z', startedAt: '2026-10-30T00:59:59.999Z',
    projectId: 'alpha', projectLabel: 'Alpha', attribution: 'recorded', toolName: 'read', operation: 'file.read',
    operationClass: 'read', status: 'succeeded', durationMs: 1, mutating: false, headline,
    changedPaths: [], readPaths: [], hiddenPathCount: 0, changedPathsTruncated: false,
    facts: [], requestFields: [], resultFields: [], pathEvidence: [], shellScripts: []
  };
}

function snapshot(actions, generatedAt = '2026-10-30T01:00:01.000Z') {
  return {
    generatedAt,
    audit: { enabled: true, action_count: actions.length, latest_sequence: actions.length },
    projects: [{
      id: 'alpha', label: 'Alpha', latestActivityAt: actions[0]?.finishedAt, actions,
      git: { available: false, message: 'Not a Git working tree.', dirty: false, trackedChangedPaths: [], untrackedPaths: [], hiddenPathCount: 0, omittedPathCount: 0, additions: 0, deletions: 0, diffAvailable: false }
    }],
    recentActions: actions, timelineNote: 'Retained actions',
    history: { matchedCount: actions.length, retainedCount: actions.length },
    live: { generatedAt, calls: [], jobs: [] }
  };
}

function dashboardWindow(initial, respond) {
  const window = new Window({ url: 'https://example.test/activity' });
  window.document.body.innerHTML = renderActivityDashboardPage(initial);
  window.setInterval = () => 0;
  window.fetch = async (url) => {
    if (String(url).startsWith('/activity/')) return respond(String(url));
    return { ok: false, status: 503, json: async () => ({}) };
  };
  const script = renderActivityDashboardPage(initial).match(/<script>([\s\S]*?)<\/script>/)[1];
  window.eval(script);
  return window;
}

test('dashboard refresh fetches JSON and patches retained nodes without losing scroll, selection or open details', async () => {
  const initial = snapshot([action('same'), action('changed', 'Before'), action('expired')]);
  const next = snapshot([action('new'), action('same'), action('changed', 'After')], '2026-10-30T01:00:16.000Z');
  const window = dashboardWindow(initial, async () => ({ ok: true, json: async () => renderActivityDashboardData(next) }));
  const document = window.document;
  const table = document.querySelector('.command-table-scroll');
  table.scrollLeft = 28;
  table.scrollTop = 12;
  const same = document.querySelector('[data-command-id="same"]');
  const sameText = same.querySelector('.action-title strong').firstChild;
  const card = same.querySelector('details.action-card');
  card.open = true;
  const selection = document.getSelection();
  selection.setBaseAndExtent(sameText, 0, sameText, 2);
  const changed = document.querySelector('[data-command-id="changed"]');
  assert.equal(await window.refreshDashboard(undefined, true), true);
  assert.equal(document.querySelector('[data-command-id="same"]'), same);
  assert.equal(same.querySelector('.action-title strong').firstChild, sameText);
  assert.equal(card.open, true);
  assert.equal(document.querySelector('[data-command-id="changed"]'), changed);
  assert.equal(changed.querySelector('.action-title strong').textContent, 'After');
  assert.equal(document.querySelector('[data-command-id="expired"]'), null);
  assert.ok(document.querySelector('[data-command-id="new"]'));
  assert.equal(table.scrollLeft, 28);
  assert.equal(table.scrollTop, 12);
  assert.equal(selection.anchorNode, sameText);
  assert.equal(selection.anchorOffset, 0);
  assert.equal(selection.focusNode, sameText);
  assert.equal(selection.focusOffset, 2);
  assert.equal(document.querySelector('[data-refresh-status]').dataset.state, 'current');
  assert.equal(window.location.href, 'https://example.test/activity');
  assert.doesNotMatch(renderActivityDashboardPage(initial), /location\.(reload|assign)|http-equiv=["']refresh/i);
});

test('failed or incomplete dashboard refresh keeps the last content and reports stale state', async () => {
  const initial = snapshot([action('retained')]);
  let failure = 'network';
  const window = dashboardWindow(initial, async () => {
    if (failure === 'network') throw new Error('Connection lost');
    return { ok: true, json: async () => ({ generatedAt: '2026-10-30T01:01:00Z', sections: [{ key: 'summary', html: '<section data-dashboard-section="summary">Should not apply</section>' }] }) };
  });
  const document = window.document;
  const retained = document.querySelector('[data-command-id="retained"]');
  const summary = document.querySelector('[data-dashboard-section="summary"]').innerHTML;
  assert.equal(await window.refreshDashboard(undefined, true), false);
  assert.equal(document.querySelector('[data-command-id="retained"]'), retained);
  assert.equal(document.querySelector('[data-refresh-status]').dataset.state, 'failed');
  assert.match(document.querySelector('[data-refresh-status]').textContent, /keeping the last content/);
  failure = 'incomplete';
  assert.equal(await window.refreshDashboard(undefined, true), false);
  assert.equal(document.querySelector('[data-dashboard-section="summary"]').innerHTML, summary);
  assert.equal(document.querySelector('[data-command-id="retained"]'), retained);
});

test('live refresh retains an expanded job when the tool-call section disappears', async () => {
  const initial = snapshot([]);
  initial.live.calls = [{
    actionId: 'call', toolName: 'bash', projectId: 'alpha', projectLabel: 'Alpha', workspaceId: 'ws_alpha',
    elapsedMs: 1000, startedAt: initial.generatedAt, state: 'in_flight', shellScripts: [], requestMetadata: {}
  }];
  const job = {
    jobId: 'job', workspaceId: 'ws_alpha', projectId: 'alpha', projectLabel: 'Alpha',
    startedAt: initial.generatedAt, elapsedMs: 1000, origin: 'bash_job', command: 'sleep 20', cwd: '.', deadlineAt: '2026-10-30T01:45:00Z'
  };
  initial.live.jobs = [job];
  const { renderActivityLiveFragment } = await import('../dist/activityDashboard/page.js');
  const window = dashboardWindow(initial, async (url) => ({
    ok: true, text: async () => url.startsWith('/activity/job')
      ? '<pre>Previously fetched output</pre>'
      : renderActivityLiveFragment({ ...initial.live, calls: [], jobs: [{ ...job, elapsedMs: 6000 }] })
  }));
  const document = window.document;
  const card = document.querySelector('[data-live-id="job"]');
  card.open = true;
  const output = card.querySelector('.job-live');
  output.innerHTML = '<pre>Previously fetched output</pre>';
  const outputText = output.querySelector('pre').firstChild;
  await window.refreshLive();
  assert.equal(document.querySelector('[data-live-id="call"]'), null);
  assert.equal(document.querySelector('[data-live-id="job"]'), card);
  assert.equal(card.open, true);
  assert.equal(card.querySelector('.job-live pre').firstChild, outputText);
  assert.match(card.querySelector('.action-subtitle').textContent, /6(?:\.0)? ?s elapsed/);
});

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}
const settled = () => new Promise((resolve) => setImmediate(resolve));

function filterSnapshot(projectId) {
  const value = snapshot([action('alpha-action')]);
  value.projects.push({ ...value.projects[0], id: 'beta', label: 'Beta', actions: [] });
  value.history.projectId = projectId || undefined;
  return value;
}

test('dashboard history navigation synchronizes the project filter with the restored URL', async () => {
  const initial = filterSnapshot('alpha');
  const window = dashboardWindow(initial, async () => ({
    ok: true, json: async () => renderActivityDashboardData(filterSnapshot('beta'))
  }));
  window.history.pushState(null, '', '/activity?project_id=beta');
  window.dispatchEvent(new window.PopStateEvent('popstate'));
  await settled();
  assert.equal(window.document.querySelector('[data-project-filter]').value, 'beta');
  assert.equal(window.location.search, '?project_id=beta');
});

test('an older project-filter response cannot roll back the latest selection', async () => {
  const alpha = deferred();
  const beta = deferred();
  const window = dashboardWindow(filterSnapshot(), async (url) => (
    url.includes('project_id=alpha') ? alpha.promise : beta.promise
  ));
  const filter = window.document.querySelector('[data-project-filter]');
  filter.value = 'alpha';
  filter.dispatchEvent(new window.Event('change', { bubbles: true }));
  filter.value = 'beta';
  filter.dispatchEvent(new window.Event('change', { bubbles: true }));
  beta.resolve({ ok: true, json: async () => renderActivityDashboardData(filterSnapshot('beta')) });
  await settled();
  assert.equal(filter.value, 'beta');
  assert.equal(window.location.search, '?project_id=beta');
  alpha.resolve({ ok: true, json: async () => renderActivityDashboardData(filterSnapshot('alpha')) });
  await settled();
  assert.equal(filter.value, 'beta');
  assert.equal(window.location.search, '?project_id=beta');
});

function inFlightSnapshot(generatedAt) {
  const value = snapshot([], generatedAt);
  value.live.calls = [{
    actionId: 'in-flight', toolName: 'read', projectId: 'alpha', projectLabel: 'Alpha', workspaceId: 'ws_alpha',
    elapsedMs: 1000, startedAt: generatedAt, state: 'in_flight', shellScripts: [], requestMetadata: {}
  }];
  return value;
}

test('an older live response cannot resurrect a call removed by a completed dashboard refresh', async () => {
  const { renderActivityLiveFragment } = await import('../dist/activityDashboard/page.js');
  const initial = inFlightSnapshot('2026-10-30T01:00:00.000Z');
  const completed = snapshot([], '2026-10-30T01:00:15.000Z');
  const pending = deferred();
  const window = dashboardWindow(initial, async (url) => url.startsWith('/activity/live')
    ? pending.promise
    : { ok: true, json: async () => renderActivityDashboardData(completed) });
  const live = window.refreshLive();
  assert.equal(await window.refreshDashboard(undefined, true), true);
  pending.resolve({ ok: true, text: async () => renderActivityLiveFragment(initial.live) });
  await live;
  assert.equal(window.document.querySelector('[data-live-id="in-flight"]'), null);
});

test('an older dashboard snapshot keeps a newer live snapshot that has already completed', async () => {
  const { renderActivityLiveFragment } = await import('../dist/activityDashboard/page.js');
  const initial = inFlightSnapshot('2026-10-30T01:00:00.000Z');
  const older = inFlightSnapshot('2026-10-30T01:00:15.000Z');
  const completed = snapshot([], '2026-10-30T01:00:16.000Z');
  const pending = deferred();
  const window = dashboardWindow(initial, async (url) => url.startsWith('/activity/data')
    ? pending.promise
    : { ok: true, text: async () => renderActivityLiveFragment(completed.live) });
  const dashboard = window.refreshDashboard(undefined, true);
  await window.refreshLive();
  assert.equal(window.document.querySelector('[data-live-id="in-flight"]'), null);
  pending.resolve({ ok: true, json: async () => renderActivityDashboardData(older) });
  assert.equal(await dashboard, true);
  assert.equal(window.document.querySelector('[data-live-id="in-flight"]'), null);
});

test('expanded diffs refresh through their owner without replacing unchanged nodes or accepting stale responses', async () => {
  const value = snapshot([]);
  value.projects[0].git = {
    available: true, branch: 'main', head: 'abc123', dirty: true,
    trackedChangedPaths: ['tracked.txt'], untrackedPaths: [], hiddenPathCount: 0,
    omittedPathCount: 0, additions: 1, deletions: 1, diffAvailable: true
  };
  const requests = [];
  const window = dashboardWindow(value, async (url) => {
    if (url.startsWith('/activity/diff')) {
      const request = deferred();
      requests.push(request);
      return request.promise;
    }
    return { ok: true, json: async () => renderActivityDashboardData(value) };
  });
  const document = window.document;
  document.querySelector('details.git-details').open = true;
  assert.equal(requests.length, 1);
  requests[0].resolve({ ok: true, text: async () => '<pre>Original diff</pre>' });
  await settled();
  const pre = document.querySelector('.git-diff pre');
  const text = pre.firstChild;
  await window.refreshDashboard(undefined, true);
  assert.equal(requests.length, 2);
  requests[1].resolve({ ok: true, text: async () => '<pre>Original diff</pre>' });
  await settled();
  assert.equal(document.querySelector('.git-diff pre'), pre);
  assert.equal(pre.firstChild, text);

  // HEAD, paths and +/- counts are unchanged. Content must still refresh.
  await window.refreshDashboard(undefined, true);
  assert.equal(requests.length, 3);
  await window.refreshDashboard(undefined, true);
  assert.equal(requests.length, 4);
  requests[3].resolve({ ok: true, text: async () => '<pre>Newest diff</pre>' });
  await settled();
  assert.equal(document.querySelector('.git-diff pre'), pre);
  assert.equal(pre.textContent, 'Newest diff');
  requests[2].resolve({ ok: true, text: async () => '<pre>Superseded diff</pre>' });
  await settled();
  assert.equal(pre.textContent, 'Newest diff');

  await window.refreshDashboard(undefined, true);
  assert.equal(requests.length, 5);
  requests[4].resolve({ ok: false, status: 503 });
  await settled();
  assert.equal(pre.textContent, 'Newest diff');
  assert.match(document.querySelector('[data-diff-error]').textContent, /keeping the last content/);
});
