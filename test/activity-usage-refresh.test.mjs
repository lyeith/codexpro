import assert from 'node:assert/strict';
import test from 'node:test';
import { Window } from 'happy-dom';
import { proUsageHtml, proUsageScript } from '../dist/usage/view.js';

const tick = () => new Promise(resolve => setImmediate(resolve));
const usage = (count = 2) => ({
  count, all_time_count: count, settings: { revision: 1 }, projects: [{ project_id: 'alpha', count }],
  collectors: [], coverage_note: 'Retained history', requested_mode_count: 0
});
function fixture(respond) {
  const window = new Window({ url: 'https://example.test/activity' });
  window.document.body.innerHTML = proUsageHtml;
  let timer;
  window.setInterval = callback => { timer = callback; return 0; };
  window.authenticatedLocalUrl = path => path;
  window.fetch = async path => ({ ok: true, json: async () => respond(path) });
  window.eval(proUsageScript);
  return { window, refresh: () => timer() };
}

test('usage refresh preserves unchanged project nodes and follows in-place dashboard filters', async () => {
  const requests = [];
  const { window, refresh } = fixture(path => { requests.push(path); return usage(); });
  await tick();
  const row = window.document.querySelector('[data-pro-project="alpha"]');
  const text = row.firstChild;
  const countText = window.document.querySelector('[data-pro-count]').firstChild;
  await refresh();
  assert.equal(window.document.querySelector('[data-pro-project="alpha"]'), row);
  assert.equal(row.firstChild, text);
  assert.equal(window.document.querySelector('[data-pro-count]').firstChild, countText);
  window.history.pushState(null, '', '/activity?project_id=alpha');
  window.dispatchEvent(new window.Event('activityfilterchange'));
  await tick();
  assert.equal(requests.at(-1), '/usage/v1/pro?project_id=alpha');
  assert.match(window.document.querySelector('[data-pro-period]').textContent, /This project/);
  assert.equal(window.document.querySelector('[data-pro-json]').getAttribute('href'), '/usage/v1/pro?project_id=alpha');
  await window.happyDOM.close();
});

test('usage discards an old project response and fetches the current filter after an in-flight change', async () => {
  let resolveOld;
  const requests = [];
  const { window } = fixture(path => {
    requests.push(path);
    return requests.length === 1 ? new Promise(resolve => { resolveOld = resolve; }) : usage(7);
  });
  window.history.pushState(null, '', '/activity?project_id=alpha');
  window.dispatchEvent(new window.Event('activityfilterchange'));
  await tick();
  resolveOld(usage(99));
  await tick();
  assert.deepEqual(requests, ['/usage/v1/pro', '/usage/v1/pro?project_id=alpha']);
  assert.equal(window.document.querySelector('[data-pro-count]').textContent, '7');
  await window.happyDOM.close();
});
