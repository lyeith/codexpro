import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { AuditJournal } from '../dist/audit.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { createDirectWorkspaceAccess } from '../dist/workspaceAccess.js';
import { getJobManager } from '../dist/jobs.js';
import { collectActivityLive, renderActivityLiveFragment, renderActivityJobFragment, collectActivityDashboard, renderActivityDashboardPage } from '../dist/activityDashboard.js';

async function fixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-inflight-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  const config = loadConfig(['--root', repo, '--bash', 'full', '--audit', 'metadata', '--audit-log', path.join(root, 'audit.jsonl')]);
  config.jobsDir = path.join(root, 'jobs');
  return { root, repo, config, cleanup: () => fs.rm(root, { recursive: true, force: true }) };
}

async function connect(config, access) {
  const server = createCodexProServer(config, access);
  const client = new Client({ name: 'live-test', version: '1' });
  const [a, b] = InMemoryTransport.createLinkedPair();
  await server.connect(a);
  await client.connect(b);
  return { client, close: async () => { await client.close(); await server.close(); } };
}

async function eventually(check) {
  const until = Date.now() + 10_000;
  while (!check()) {
    if (Date.now() > until) assert.fail('condition did not become true');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

test('queued calls are visible across sessions before receipts and removed on success and failure', async () => {
  const f = await fixture();
  const access = createDirectWorkspaceAccess(f.config);
  const workspace = access.defaultWorkspace();
  const journal = new AuditJournal(f.config);
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  access.execute = async (_id, _mutating, invoke) => { await gate; return invoke(); };
  const connection = await connect(f.config, access);
  let pending;
  try {
    await fs.writeFile(path.join(f.repo, 'ok.txt'), 'ok');
    pending = Promise.all(['ok.txt', 'missing.txt'].map(file => connection.client.callTool({ name: 'read', arguments: { workspace_id: workspace.id, path: file } })));
    await eventually(() => journal.listInFlight().length === 2);
    const active = journal.listInFlight();
    assert.ok(active.every(call => call.state === 'queued' && call.projectId === 'default' && call.workspaceId === workspace.id));
    assert.equal(journal.list().actions.length, 0);
    assert.equal(new AuditJournal({ ...f.config, auditLogPath: path.join(f.root, 'other.jsonl') }).listInFlight().length, 0);
    active[0].state = 'running';
    assert.equal(journal.listInFlight()[0].state, 'queued', 'callers cannot mutate live state');
    release();
    const results = await pending;
    assert.equal(results[0].isError, undefined);
    assert.equal(results[1].isError, true);
    assert.equal(journal.listInFlight().length, 0);
    assert.deepEqual(new Set(journal.list().actions.map(action => action.action_id)), new Set(active.map(call => call.actionId)));
  } finally { release(); await pending; await connection.close(); await f.cleanup(); }
});

test('foreground Bash appears as an in-flight call and durable running job before completion', async () => {
  const f = await fixture();
  const access = createDirectWorkspaceAccess(f.config);
  const connection = await connect(f.config, access);
  const journal = new AuditJournal(f.config);
  let pending;
  try {
    pending = connection.client.callTool({ name: 'bash', arguments: {
      workspace_id: access.defaultWorkspace().id,
      command: 'while [ ! -f finish ]; do sleep 0.05; done; printf "complete\\n"', timeout_ms: 30_000
    } });
    await eventually(() => journal.listInFlight().some(call => call.state === 'running') && getJobManager(f.config).runningJobs().length === 1);
    const live = collectActivityLive(f.config, journal);
    assert.equal(live.calls.length, 1);
    assert.equal(live.jobs.length, 1);
    assert.equal(live.jobs[0].origin, 'foreground');
    assert.equal(live.jobs[0].projectId, 'default');
    assert.match(live.calls[0].shellScripts[0].script, /while/);
    assert.match(renderActivityLiveFragment(live), /1 tool calls · 1 running jobs/);
    assert.equal(journal.list().actions.length, 0);
    await fs.writeFile(path.join(f.repo, 'finish'), '');
    await pending;
    assert.equal(collectActivityLive(f.config, journal).calls.length, 0);
    assert.equal(collectActivityLive(f.config, journal).jobs.length, 0);
    assert.equal(journal.list().actions.find(action => action.tool_name === 'bash').action_id, live.calls[0].actionId);
  } finally {
    await fs.writeFile(path.join(f.repo, 'finish'), '');
    await pending; await connection.close(); await f.cleanup();
  }
});

test('worktree jobs support live output without acknowledging completion; removed projects stay hidden', async () => {
  const f = await fixture();
  try {
    const job = { id: 'job_0123abcd', project_id: 'default', root: path.join(f.root, 'worktree'), workspace_id: 'wt_test',
      status: 'running', origin: 'background', command: 'printf "<script>"', cwd: '.', exit_code: null, started_at: new Date().toISOString(), started_at_ms: Date.now(), deadline_ms: Date.now() + 60_000 };
    const manager = {
      runningJobs: () => [job],
      require: (id, workspace) => { assert.equal(id, job.id); assert.equal(workspace, job.workspace_id); return job; },
      readTail: () => ({ stdout: '<script>output</script>', stderr: '', stdout_bytes: 23, stderr_bytes: 0, truncated: false }),
      output: { metadata: () => ({ output_available: true, available_stdout_bytes: 23, available_stderr_bytes: 0 }) }
    };
    assert.equal(collectActivityLive(f.config, new AuditJournal(f.config), Date.now(), manager).jobs.length, 1);
    const html = renderActivityJobFragment(f.config, job.id, job.workspace_id, manager);
    assert.match(html, /&lt;script&gt;output/);
    assert.match(html, /data-job-status="running"/);
    job.project_id = 'removed';
    assert.equal(collectActivityLive(f.config, new AuditJournal(f.config), Date.now(), manager).jobs.length, 0);
    assert.throws(() => renderActivityJobFragment(f.config, job.id, job.workspace_id, manager), /no longer in the catalog/);
  } finally { await f.cleanup(); }
});

test('batch details preserve actual job status and compact filtering retains the selected project', async () => {
  const f = await fixture();
  try {
    const journal = new AuditJournal(f.config);
    journal.record({ toolName: 'batch', args: { project_id: 'default', workspace_id: 'wt_test' }, startedAtMs: Date.now() - 100, finishedAtMs: Date.now(), mutating: true,
      result: { structuredContent: { results: [{ id: 'build', tool: 'bash', ok: true, structured: { job_id: 'job_0123abcd', job_status: 'running' } }] } } });
    const snapshot = collectActivityDashboard(f.config, journal, Date.now(), { projectId: 'default' });
    assert.equal(snapshot.recentActions[0].childResults[0].job_status, 'running');
    const html = renderActivityDashboardPage(snapshot);
    assert.match(html, /value="default" selected/);
    assert.doesNotMatch(html, /Filter project:/);
    assert.match(html, /status warn">running/);
    assert.match(html, /tool call succeeded/);
    assert.match(html, /wt_test/);
    assert.ok(html.indexOf('data-project-filter') > html.indexOf('Command history'));
    assert.equal(new AuditJournal({ ...f.config, auditMode: 'off' }).begin({ toolName: 'read', args: {}, startedAtMs: Date.now(), mutating: false }), undefined);
  } finally { await f.cleanup(); }
});
