import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { getWorkRuntime, WorkRuntime } from '../dist/work/runtime.js';
import { runWithToolContext, principalIdFromAuthInfo } from '../dist/toolContext.js';
import { getJobManager } from '../dist/jobs.js';
import { AuditJournal } from '../dist/audit.js';
import { collectActivityDashboard } from '../dist/activityDashboard.js';

function fixture(options = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codexpro-work-'));
  const repo = path.join(dir, 'repo'); fs.mkdirSync(repo); fs.writeFileSync(path.join(repo, 'hello.txt'), 'original\n');
  for (const argv of [['init', '-b', 'main'], ['add', '.'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'initial']]) {
    const p = spawnSync('git', argv, { cwd: repo, encoding: 'utf8' }); assert.equal(p.status, 0, p.stderr);
  }
  let projectArgs = ['--root', repo];
  if (options.multi) {
    const projects = [{ id: 'source', label: 'CodexPro source', root: repo, baseRef: 'main' }];
    for (const id of ['alpha', 'beta']) {
      const root = path.join(dir, id); const p = spawnSync('git', ['clone', '-q', repo, root], { encoding: 'utf8' }); assert.equal(p.status, 0, p.stderr);
      projects.push({ id, label: id, root, baseRef: 'main' });
    }
    const file = path.join(dir, 'projects.json'); fs.writeFileSync(file, JSON.stringify({ version: 1, defaultProject: 'source', projects }));
    projectArgs = ['--projects-file', file];
  }
  const config = loadConfig([...projectArgs, '--bash', 'full', '--write', 'workspace', '--tool-mode', 'full', '--work', 'on', '--work-dir', path.join(dir, 'work'), '--worktree-root', path.join(dir, 'legacy'), '--worktree-base-ref', 'main', '--audit', options.audit ? 'metadata' : 'off', '--audit-log', path.join(dir, 'audit', 'calls.jsonl')]);
  config.jobsDir = path.join(dir, 'jobs'); config.work.sweepMs = 250; config.worktreeBaseRef = 'main';
  return { dir, repo, config };
}
async function setup(options = {}) {
  const f = fixture(options); const server = createCodexProServer(f.config), client = new Client({ name: 'work-test', version: '1' });
  const [a, b] = InMemoryTransport.createLinkedPair(); await server.connect(a); await client.connect(b);
  const runtime = getWorkRuntime(f.config); await runtime.ready;
  const call = async (name, args, error = false) => { const result = await client.callTool({ name, arguments: args });
    if (!error) assert.ok(!result.isError, JSON.stringify(result)); return result; };
  return { ...f, server, client, runtime, call, async close() { for (const j of getJobManager(f.config).runningJobs()) getJobManager(f.config).stop(j.id); await delay(200); await client.close(); await server.close(); runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); } };
}
async function create(f, mode = 'ralph', extras = {}) {
  const r = await f.call('work_manage', { action: 'create', request_key: `create-${mode}`, project_id: f.config.defaultProjectId, mode, title: 'Test loop', objective: 'Test durable work', scope: 'hello.txt', ready: true,
    acceptance: [{ id: 'check', description: 'file exists', command: 'test -f hello.txt', required: true }],
    todos: [{ id: 'a', title: 'First packet', status: 'pending' }, { id: 'b', title: 'Second packet', status: 'pending' }], ...extras });
  return r.structuredContent;
}
async function inspectRun(f, run) { return status(f, run.run_id); }
async function status(f, id) { return (await f.call('work_status', { action: 'get', run_id: id })).structuredContent; }

test('ambiguous run prefixes never mutate, respect project and principal scope, and keep full IDs usable', async () => {
  const f = await setup({ multi: true });
  try {
    const r = await create(f, 'ralph', { project_id: 'alpha' });
    const store = f.runtime.coordinator.store, coordinator = f.runtime.coordinator;
    const first = store.get('runs', r.run_id), prefix = r.run_id.slice(0, 12);
    const second = { ...first, id: prefix + '-another-run', project_id: 'beta', workspace: undefined, state: 'cancelled' };
    store.saveRun(second);
    const before = store.runs().map(run => [run.id, run.revision, run.state]);
    const ambiguous = await f.call('work_manage', { action: 'pause', run_id: prefix, expected_revision: first.revision, request_key: 'ambiguous', reason: 'Must not execute' }, true);
    assert.equal(ambiguous.isError, true); assert.match(JSON.stringify(ambiguous), /work_run_ambiguous/);
    assert.deepEqual(store.runs().map(run => [run.id, run.revision, run.state]), before);
    assert.equal((await status(f, r.run_id)).run_id, r.run_id);
    assert.equal((await f.call('work_status', { action: 'get', run_id: prefix, project_id: 'alpha', section: 'summary' })).structuredContent.run_id, r.run_id);
    assert.equal((await f.call('work_status', { action: 'get', run_id: r.run_id, project_id: 'beta' })).structuredContent.run_id, r.run_id, 'Exact work-tool IDs keep precedence over project list filters');
    second.principal_id = 'other-user'; store.saveRun(second);
    assert.equal((await status(f, prefix)).run_id, r.run_id, 'Other principals do not contribute candidates or ambiguity');
    assert.equal((await f.call('work_status', { action: 'get', run_id: second.id }, true)).isError, true);
    assert.throws(() => coordinator.require('nobody', prefix), /inaccessible/);
  } finally { await f.close(); }
});

test('competing coordinator processes have one owner and recover after abrupt death', async () => {
  const f = fixture(); const children = [];
  const moduleUrl = new URL('../dist/work/runtime.js', import.meta.url).href;
  const launch = () => new Promise((resolve, reject) => {
    const child = spawn(process.execPath, ['--input-type=module', '-e', `
      import { WorkRuntime } from ${JSON.stringify(moduleUrl)};
      try {
        const runtime = new WorkRuntime(JSON.parse(process.env.WORK_OWNER_CONFIG));
        await runtime.ready;
        console.log('owned'); setInterval(() => {}, 1000);
      } catch (error) { console.log('rejected:' + error.message); process.exitCode = 1; }
    `], { env: { ...process.env, WORK_OWNER_CONFIG: JSON.stringify(f.config) }, stdio: ['ignore', 'pipe', 'pipe'] });
    children.push(child); let output = '', errors = '';
    child.stderr.on('data', chunk => { errors += chunk; }); child.on('error', reject);
    child.stdout.on('data', chunk => { output += chunk; if (output.includes('\n')) resolve({ child, output: output.trim() }); });
    child.on('exit', () => { if (!output) reject(new Error(errors || 'Coordinator exited without reporting ownership')); });
  });
  try {
    const contenders = await Promise.all([launch(), launch()]);
    const winners = contenders.filter(result => result.output === 'owned'); assert.equal(winners.length, 1);
    assert.match(contenders.find(result => result !== winners[0]).output, /rejected:(Another coordinator|Work storage is busy)/);
    const exited = once(winners[0].child, 'exit'); winners[0].child.kill('SIGKILL'); await exited;
    assert.equal((await launch()).output, 'owned', 'A fresh process must reclaim without predecessor cooperation.');
  } finally {
    await Promise.all(children.map(async child => { if (child.exitCode === null && child.signalCode === null) { const exited = once(child, 'exit'); child.kill('SIGKILL'); await exited; } }));
    fs.rmSync(f.dir, { recursive: true, force: true });
  }
});

test('batch and supertool share retry receipts; documents page without data loss; lost finish reply is idempotent', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await inspectRun(f, run);
    const envelope = { operation_key: 'batch-1' };
    const batch = await f.call('batch', { workspace_id: c.workspace_id, execution: envelope, persist: false, operations: [
      { id: 'one', tool: 'bash', args: { command: 'printf one > one.txt' } }, { id: 'two', tool: 'bash', args: { command: 'printf two > two.txt' } }
    ] }); assert.ok(batch.structuredContent.work_receipt);
    const superResult = await f.call('codexpro', { action: 'bash', args: { workspace_id: c.workspace_id, command: 'test -f one.txt && test -f two.txt' }, execution: { operation_key: 'wrapped' } });
    assert.ok(superResult.structuredContent.work_receipt);
    const text = 'Memory café 😀\n'.repeat(500);
    const put = (await f.call('work_update', { action: 'put_document', run_id: run.run_id, expected_revision: c.revision, request_key: 'doc', kind: 'project_memory', title: 'Reusable memory', content: text })).structuredContent;
    let content = '', offset = 0;
    do { const page = (await f.call('work_status', { action: 'read_document', run_id: run.run_id, document_id: put.document_id, document_revision: put.document_revision, offset, max_bytes: 1024 })).structuredContent;
      assert.equal(page.return_size.truncated, false); content += page.content; offset = page.next_offset;
    } while (offset !== null);
    assert.equal(content, text);
    const search = (await f.call('work_status', { action: 'search_memory', project_id: f.config.defaultProjectId, query: 'café' })).structuredContent; assert.equal(search.total_matches, 1);
    const finishArgs = { action: 'finish_iteration', run_id: run.run_id, expected_revision: put.revision, request_key: 'finish-lost', summary: 'Changed source', next_action: 'Continue', outcome: 'yielded' };
    const first = (await f.call('work_update', finishArgs)).structuredContent;
    const duplicate = (await f.call('work_update', finishArgs)).structuredContent; assert.equal(first.checkpoint_id, duplicate.checkpoint_id);
    const current = await status(f, run.run_id);
    await f.call('work_manage', { action: 'pause', run_id: run.run_id, expected_revision: current.revision, request_key: 'pause-after-finish', reason: 'Stop writer' });
    const staleBatch = await f.call('batch', { workspace_id: c.workspace_id, execution: envelope, persist: false, operations: [{ id: 'three', tool: 'bash', args: { command: 'touch stale.txt' } }] }, true); assert.equal(staleBatch.isError, true);
    const staleWrapper = await f.call('codexpro', { action: 'bash', args: { workspace_id: c.workspace_id, command: 'touch stale.txt', execution: envelope } }, true); assert.equal(staleWrapper.isError, true);
  } finally { await f.close(); }
});

test('verification rejects changed source, and draft runs cannot execute effects', async () => {
  const f = await setup(); try {
    const r = (await f.call('work_manage', { action: 'create', request_key: 'draft', project_id: f.config.defaultProjectId, mode: 'manual', title: 'Draft', objective: 'plan', scope: 'file' })).structuredContent;
    const p = await inspectRun(f, r);
    const denied = await f.call('bash', { workspace_id: p.workspace_id, command: 'true', execution: { operation_key: 'no' } }, true); assert.equal(denied.isError, true);
    await f.call('work_update', { action: 'finish_iteration', run_id: r.run_id, expected_revision: p.revision, request_key: 'planned', summary: 'Spec ready', next_action: 'Verify', outcome: 'completed', acceptance: [{ id: 'badcheck', description: 'Changes source', command: 'printf altered > hello.txt', required: true }], todos: [] });
    const s = await status(f, r.run_id);
    await f.call('work_manage', { action: 'finish_run', run_id: r.run_id, expected_revision: s.revision, request_key: 'verify' });
    let final; for (let n = 0; n < 50; n++) { final = await status(f, r.run_id); if (final.state === 'blocked') break; await delay(100); }
    assert.equal(final.state, 'blocked'); assert.match(final.recovery_reason ?? '', /Source changed/);
  } finally { await f.close(); }
});

test('control-store failure before job registration cannot execute a command', async () => {
  const f = await setup(); const store = f.runtime.coordinator.store; const original = store.saveJob;
  try {
    const run = await create(f); const c = await inspectRun(f, run);
    store.saveJob = () => { throw new Error('Simulated work receipt storage failure'); };
    const reply = await f.call('bash', { workspace_id: c.workspace_id, command: 'printf unsafe > must-not-exist.txt', execution: { operation_key: 'fault' } }, true);
    assert.equal(reply.isError, true);
    store.saveJob = original;
    const workspace = store.get('runs', run.run_id).workspace;
    assert.equal(fs.existsSync(path.join(workspace.root, 'must-not-exist.txt')), false);
    assert.ok(getJobManager(f.config).list(c.workspace_id).every(j => j.status !== 'running' && j.quiescent));
  } finally { store.saveJob = original; await f.close(); }
});

test('escaped document content remains lossless at the smallest response budget', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await inspectRun(f, run);
    const content = '\u0000😀'.repeat(400);
    const doc = (await f.call('work_update', { action: 'put_document', run_id: run.run_id, expected_revision: c.revision, request_key: 'escaped-doc', title: 'Control bytes', content })).structuredContent;
    f.config.work.packetBytes = 4096;
    let collected = '', offset = 0;
    do {
      const reply = await f.call('work_status', { action: 'read_document', run_id: run.run_id, document_id: doc.document_id, document_revision: doc.document_revision, max_bytes: 32000, offset });
      const page = reply.structuredContent; assert.equal(page.return_size.truncated, false); assert.ok(Buffer.byteLength(JSON.stringify(reply)) <= 4096, 'Encoded response must fit the response budget');
      collected += page.content; offset = page.next_offset;
    } while (offset !== null);
    assert.equal(collected, content);
  } finally { await f.close(); }
});

test('existing exhausted runs retain history and resume without a cumulative time limit', async () => {
  const f = fixture(); let mono = 0;
  const clock = { sample: () => ({ epoch: 'retired-budget', monotonic_ms: mono, wall_ms: 1_800_000_000_000 + mono }) };
  let runtime = new WorkRuntime(f.config, clock); await runtime.ready;
  const who = principalIdFromAuthInfo(f.config);
  try {
    const created = await runtime.coordinator.create(who, { request_key: 'legacy', project_id: f.config.defaultProjectId, mode: 'ralph', title: 'Legacy run', objective: 'Continue', scope: 'file', ready: true,
      acceptance: [{ id: 'check', description: 'file exists', command: 'test -f hello.txt', required: true }], todos: [{ id: 'a', title: 'Next packet', status: 'pending', evidence_ids: [] }] });
    const old = runtime.coordinator.store.get('runs', created.run_id);
    Object.assign(old.limits, { active_ms: 7_200_000, attempt_ms: 1_500_000, max_attempts: 20, no_progress_attempts: 3 });
    old.measured_active_ms = 7_200_000; old.attempt_count = 20; old.no_progress_count = 3;
    old.state = 'blocked'; old.recovery_reason = 'Worker stopped because the cumulative allowance was exhausted.';
    runtime.coordinator.store.saveRun(old);
    runtime.coordinator.store.setMeta('schema', '1');
    const documents = runtime.coordinator.store.documents(old.id);
    runtime.close(); runtime = new WorkRuntime(f.config, clock); await runtime.ready;
    const s = runtime.coordinator;
    assert.equal(s.store.meta('schema'), '3');
    const migrated = s.store.get('runs', old.id);
    for (const key of ['active_ms', 'attempt_ms', 'max_attempts', 'no_progress_attempts']) assert.equal(key in migrated.limits, false);
    assert.equal(migrated.measured_active_ms, old.measured_active_ms);
    assert.equal(migrated.attempt_count, 20); assert.equal(migrated.no_progress_count, 3);
    assert.equal(migrated.state, 'blocked', 'Migration must not clear a worker or operator blocker.');
    assert.deepEqual(s.store.documents(old.id), documents);
    const revision = migrated.revision; s.removeLegacyRunLimits();
    assert.equal(s.store.get('runs', old.id).revision, revision, 'Migration is idempotent.');
    const resumed = s.manage(who, { action: 'resume', run_id: old.id, expected_revision: revision, request_key: 'resume' });
    mono += 10_000;
    s.checkpoint(who, { action: 'finish_iteration', run_id: old.id, expected_revision: resumed.revision, request_key: 'finish', summary: 'Useful checkpoint', next_action: 'Next packet', outcome: 'yielded' }); s.sweep();
    const next = s.status(who, old.id);
    assert.equal(next.state, 'ready'); assert.equal(next.limits.active_ms, null);
    assert.equal(next.health.state, 'no_progress_advisory');
    assert.equal(s.writable(who, old.id).state, 'ready');

  } finally { runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); }
});

test('managed commands and legacy limit requests work beyond the former run cap', async () => {
  const f = await setup(); try {
    const r = await create(f);
    const store = f.runtime.coordinator.store; const run = store.get('runs', r.run_id);
    run.limits.active_ms = 7_200_000; run.measured_active_ms = 7_199_999; store.saveRun(run);
    run.attempt_count = 20; run.limits.max_attempts = 20; store.saveRun(run);
    const revised = (await f.call('work_manage', { action: 'revise_limits', run_id: r.run_id, expected_revision: r.revision, request_key: 'old-client-limit', reason: 'Legacy client asks for more work', active_ms: 14_400_000, max_attempts: 40 })).structuredContent;
    assert.deepEqual(revised.ignored_fields, ['active_ms', 'max_attempts']); assert.equal(revised.limits.active_ms, null); assert.equal(revised.limits.max_attempts, null);
    assert.match(revised.guidance, /retired/);
    const c = await inspectRun(f, { ...r, revision: revised.revision });
    const job = (await f.call('bash', { workspace_id: c.workspace_id, execution: { operation_key: 'past-budget' }, command: 'sleep 0.15; printf continued', timeout_ms: 2000 })).structuredContent;
    assert.equal(job.exit_code, 0, JSON.stringify(job));
    assert.ok(getJobManager(f.config).list(c.workspace_id).some(j => j.timeout_ms > 1000), 'Old remaining time must not shorten a command.');
    const finished = (await f.call('work_update', { action: 'finish_iteration', run_id: r.run_id, expected_revision: c.revision, request_key: 'past-budget-finish', summary: 'Command completed', next_action: 'Continue', outcome: 'yielded' })).structuredContent;
    assert.equal(finished.timing, undefined);
  } finally { await f.close(); }
});

test('final acceptance keeps per-job deadlines and completes beyond the former run cap', async () => {
  const f = await setup(); try {
    f.config.jobTimeoutMs = 3000;
    const r = (await f.call('work_manage', { ...{ action: 'create', request_key: 'budget-check', project_id: f.config.defaultProjectId, mode: 'manual', title: 'Budget', objective: 'Verify', scope: 'file', ready: true }, acceptance: [{ id: 'one', description: 'First check', command: 'sleep 0.15', required: true }, { id: 'two', description: 'Next check', command: 'test -f hello.txt', required: true }], todos: [] })).structuredContent;
    const store = f.runtime.coordinator.store; const run = store.get('runs', r.run_id); run.limits.active_ms = 7_200_000; run.measured_active_ms = 7_200_000; store.saveRun(run);
    await f.call('work_manage', { action: 'finish_run', run_id: r.run_id, expected_revision: r.revision, request_key: 'budget-verify' });
    let final; for (let n = 0; n < 60; n++) { final = await status(f, r.run_id); if (final.state === 'complete') break; await delay(100); }
    assert.equal(final.state, 'complete', JSON.stringify(final));
    assert.ok(store.get("runs", r.run_id).measured_active_ms > 7_200_000);
    const jobs = getJobManager(f.config).list(run.workspace.id); assert.equal(jobs.length, 2);
    for (const job of jobs) { assert.ok(job.timeout_ms > 2000 && job.timeout_ms <= 3000, 'Launch preparation consumes part of the finite job deadline.'); assert.equal(job.status, 'succeeded'); }
  } finally { await f.close(); }
});

test('retained receipts and documents do not exhaust work admission or checkpoint capacity', async () => {
  const f = await setup(); try {
    const r = await create(f), store = f.runtime.coordinator.store, s = f.runtime.coordinator;
    store.db.exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000) INSERT INTO requests SELECT 'history', 'request-'||x, 'historical', '{}' FROM n");
    store.db.prepare("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO operations SELECT 'old-op-'||x, ?, 'old-key-'||x, json_object('id','old-op-'||x,'run_id',?,'operation_key','old-key-'||x,'state','succeeded','job_ids',json('[]')) FROM n").run(r.run_id, r.run_id);
    const row = store.get('runs', r.run_id), content = 'retained evidence\n'.repeat(5400);
    store.transaction(() => { for (let i = 0; i < 205; i++) s.document(row, 'note', `Evidence ${i}`, content, 'test'); });
    const c = await inspectRun(f, r);
    const result = await f.call('bash', { workspace_id: c.workspace_id, execution: { operation_key: 'after-history' }, command: 'printf history-preserved' });
    assert.equal(result.structuredContent.exit_code, 0);
    await f.call('work_update', { action: 'finish_iteration', run_id: r.run_id, expected_revision: c.revision, request_key: 'finish-after-history', summary: 'Finished packet', next_action: 'Continue', outcome: 'yielded' });
    assert.equal((await status(f, r.run_id)).state, 'ready');
    assert.equal(store.documents(r.run_id).filter(d => d.kind === 'note').length, 205);
    assert.ok(store.operationCount(r.run_id) > 10000);
  } finally { await f.close(); }
});

test('paged plans grow beyond per-call limits and managed run history does not consume workspace quota', async () => {
  const f = await setup(); try {
    f.config.maxWorktrees = 1; f.config.projects[0].maxWorktrees = 1;
    const r = await create(f, 'manual', { todos: Array.from({ length: 200 }, (_, i) => ({ id: `t${i}`, title: `Task ${i}`, status: 'pending' })), acceptance: Array.from({ length: 50 }, (_, i) => ({ id: `a${i}`, description: 'e'.repeat(1800), command: 'true', required: true })) });
    const second = await create(f, 'ralph'); assert.equal(second.state, 'ready');
    const c = await inspectRun(f, r);
    const cp = (await f.call('work_update', { action: 'revise_plan', run_id: r.run_id, expected_revision: c.revision, request_key: 'pages', summary: 'Extended plan', next_action: 'Work',
      todo_updates: Array.from({ length: 200 }, (_, i) => ({ id: `t${i+200}`, title: `Task ${i+200}`, status: 'pending' })),
      acceptance_updates: Array.from({ length: 50 }, (_, i) => ({ id: `a${i+50}`, description: 'e'.repeat(1800), command: 'true', required: true })) })).structuredContent;
    const stored = f.runtime.coordinator.store.get('runs', r.run_id); assert.equal(stored.todos.length, 400); assert.equal(stored.acceptance.length, 100);
    const before = JSON.stringify({ revision: stored.revision, todos: stored.todos, acceptance: stored.acceptance });
    const failed = await f.call('work_update', { action: 'checkpoint', run_id: r.run_id, expected_revision: cp.revision, request_key: 'bad-page', summary: 'Bad', next_action: 'Work',
      todo_updates: [{ id: 't0', title: 'Done', status: 'done', evidence_ids: ['missing'] }] }, true);
    assert.equal(failed.isError, true);
    const unchanged = f.runtime.coordinator.store.get('runs', r.run_id);
    assert.equal(JSON.stringify({ revision: unchanged.revision, todos: unchanged.todos, acceptance: unchanged.acceptance }), before);
    const page = (await f.call('work_status', { action: 'get', run_id: r.run_id, section: 'todos', offset: 350, limit: 50 })).structuredContent;
    assert.equal(page.total_items, 400); assert.equal(page.next_offset, null);
    assert.ok(f.runtime.coordinator.store.documents(r.run_id).find(d => d.kind === 'spec').bytes > 131072);
  } finally { await f.close(); }
});

test('work audit follows authenticated run identity across projects and groups server activity', async () => {
  const f = await setup({ multi: true, audit: true }); try {
    const journal = new AuditJournal(f.config);
    const last = () => journal.listForDashboard().actions.at(-1);
    await f.call('server_config', {});
    assert.equal(last().audit_scope, 'server'); assert.equal(last().project_id, undefined);
    await f.call('work_status', { action: 'list' }); assert.equal(last().audit_scope, 'server');
    for (const project of ['alpha', 'beta']) {
      const run = await create(f, 'manual', { project_id: project, request_key: `create-${project}` });
      assert.equal(last().project_id, project); assert.equal(last().run_id, run.run_id);
      const c = await inspectRun(f, run);
      assert.equal(last().workspace_id, c.workspace_id); assert.equal(last().operation, 'work.get');
      const base = { run_id: run.run_id, expected_revision: c.revision };
      await f.call('work_status', { action: 'get', run_id: run.run_id, section: 'todos', project_id: 'source' });
      assert.equal(last().project_id, project); assert.equal(last().workspace_id, c.workspace_id);
      await f.call('work_status', { action: 'list', project_id: project });
      assert.equal(last().project_id, project); assert.equal(last().workspace_id, undefined); assert.equal(last().audit_scope, 'project');
      const update = await f.call('work_update', { ...base, action: 'checkpoint', request_key: `cp-${project}`, summary: 'PRIVATE MEMORY SUMMARY', next_action: 'next', documents: [{ title: 'Private', content: 'PRIVATE DOCUMENT TEXT' }] });
      assert.equal(last().operation, 'work.checkpoint'); assert.equal(last().result_metadata.documents_count, 1);
      assert.equal(last().project_id, project); assert.equal(last().request_metadata.run_id, run.run_id);
      const bad = await f.call('work_update', { ...base, action: 'checkpoint', request_key: `stale-${project}`, summary: 'stale', next_action: 'next' }, true);
      assert.equal(bad.isError, true); assert.equal(last().project_id, project); assert.equal(last().status, 'failed');
      const doc = update.structuredContent.documents[0];
      const put = await f.call('work_update', { ...base, expected_revision: update.structuredContent.revision, action: 'put_document', request_key: `put-${project}`, ...doc, title: 'Private', content: 'PRIVATE DOCUMENT TEXT TWO' });
      assert.equal(last().operation, 'work.put_document'); assert.equal(last().project_id, project);
      await f.call('work_update', { ...base, expected_revision: put.structuredContent.revision, action: 'finish_iteration', request_key: `finish-${project}`, summary: 'ready', next_action: 'next', outcome: 'yielded' });
      assert.equal(last().operation, 'work.finish_iteration'); assert.equal(last().project_id, project);
      const log = fs.readFileSync(f.config.auditLogPath, 'utf8');
      for (const secret of ['PRIVATE MEMORY SUMMARY', 'PRIVATE DOCUMENT TEXT']) assert.ok(!log.includes(secret));
    }
    await f.call('work_status', { action: 'get', run_id: 'missing', project_id: 'alpha' }, true);
    assert.equal(last().project_id, undefined); assert.equal(last().audit_scope, 'unattributed');
    const foreign = f.runtime.coordinator.store.runs()[0]; foreign.principal_id = 'someone-else'; f.runtime.coordinator.store.saveRun(foreign);
    const denied = await f.call('work_status', { action: 'get', run_id: foreign.id }, true);
    assert.equal(denied.isError, true); assert.equal(last().project_id, undefined); assert.equal(last().run_id, undefined);
    // Retained pre-fix records cannot be repaired from their default workspace.
    journal.record({ toolName: 'work_update', args: {}, result: { project_id: 'source' }, mutating: true, startedAtMs: Date.now(), finishedAtMs: Date.now() });
    const snapshot = collectActivityDashboard(f.config, journal);
    assert.equal(snapshot.recentActions[0].headline, 'Work update');
    assert.ok(snapshot.timeline.lanes.some(l => l.label === 'Server'));
    assert.ok(snapshot.timeline.lanes.some(l => l.label === 'Unattributed'));
    assert.ok(snapshot.projects.find(p => p.id === 'alpha').actions.some(a => a.headline.startsWith('Finish iteration')));
    assert.equal(snapshot.projects.find(p => p.id === 'source').actions.length, 0);
    assert.ok(snapshot.recentActions.some(a => a.headline.startsWith('Update document')));
  } finally { await f.close(); }
});

test('bulk checkpoint documents, todos and handoff commit once or all roll back', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await inspectRun(f, run);
    const store = f.runtime.coordinator.store;
    const base = { run_id: run.run_id, expected_revision: c.revision, action: 'checkpoint', request_key: 'bulk', summary: 'Planned', next_action: 'Execute' };
    const args = { ...base, todos: [{ id: 'new', title: 'New work', status: 'pending' }], documents: [
      { kind: 'decision', title: 'One', content: 'first', todo_ids: ['new'], reference_path: 'hello.txt' },
      { kind: 'project_memory', title: 'Two', content: 'second' }
    ] };
    const before = JSON.stringify(store.documentManifest(run.run_id));
    const events = store.events(run.run_id, 0, 100).length;
    const bad = await f.call('work_update', { ...args, documents: [args.documents[0], { ...args.documents[1], todo_ids: ['missing'] }] }, true);
    assert.equal(bad.isError, true); assert.equal(store.get('runs', run.run_id).revision, c.revision);
    assert.equal(JSON.stringify(store.documentManifest(run.run_id)), before); assert.equal(store.events(run.run_id, 0, 100).length, events);
    const cp = (await f.call('work_update', args)).structuredContent;
    assert.equal(cp.revision, c.revision + 1); assert.equal(cp.documents.length, 2);
    const duplicate = (await f.call('work_update', args)).structuredContent; assert.equal(duplicate.checkpoint_id, cp.checkpoint_id);
    assert.deepEqual(duplicate.documents, cp.documents); assert.equal(store.get('runs', run.run_id).revision, cp.revision);
    const doc = store.document(run.run_id, cp.documents[0].document_id);
    assert.equal(doc.reference.path, 'hello.txt'); assert.deepEqual(doc.todo_ids, ['new']);
    const revisionArgs = { ...base, request_key: 'bulk-revision', expected_revision: cp.revision, documents: cp.documents.map((d, i) => ({ document_id: d.document_id, document_revision: 1, title: `updated ${i}`, content: 'new' })) };
    const stale = await f.call('work_update', { ...revisionArgs, documents: [revisionArgs.documents[0], { ...revisionArgs.documents[1], document_revision: 9 }] }, true);
    assert.equal(stale.isError, true); assert.equal(store.document(run.run_id, doc.id).revision, 1);
    const escape = await f.call('work_update', { ...revisionArgs, documents: [{ ...revisionArgs.documents[0], reference_path: '../outside.txt' }] }, true); assert.equal(escape.isError, true);
    const generated = store.documentManifest(run.run_id).find(d => d.kind === 'handoff');
    const overwrite = await f.call('work_update', { ...revisionArgs, documents: [{ document_id: generated.id, document_revision: generated.revision, title: 'bad', content: 'bad' }] }, true); assert.equal(overwrite.isError, true);
    const countBefore = store.documentManifest(run.run_id).length;
    const originalCap = f.config.work.maxDocumentBytes; f.config.work.maxDocumentBytes = 3;
    const capacity = await f.call('work_update', { ...base, expected_revision: cp.revision, request_key: 'capacity', documents: [{ title: 'three', content: '3' }, { title: 'four', content: 'too large' }] }, true);
    assert.equal(capacity.isError, true); assert.equal(store.documentManifest(run.run_id).length, countBefore); f.config.work.maxDocumentBytes = originalCap;
    const final = (await f.call('work_update', { ...revisionArgs, action: 'finish_iteration', outcome: 'yielded' })).structuredContent;
    assert.equal(final.documents[0].document_revision, 2); assert.equal(store.document(run.run_id, doc.id).kind, 'decision');
    assert.equal(store.get('runs', run.run_id).iteration_id, undefined);
  } finally { await f.close(); }
});

test('managed batch edits, verifies and checkpoints without persisting credentials or replaying effects', async () => {
  const f = await setup({ audit: true }); try {
    const run = await create(f); const c = await inspectRun(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { operation_key: 'edit-checkpoint' },
      operations: [{ id: 'write', tool: 'write', args: { path: 'hello.txt', content: 'changed\n' } }, { id: 'verify', tool: 'bash', args: { command: 'test "$(cat hello.txt)" = changed' } }],
      checkpoint: { expected_revision: c.revision, summary: 'SECRET HANDOFF', next_action: 'Next packet', documents: [{ title: 'Decision', content: 'SECRET DOCUMENT' }], todos: [{ id: 'a', title: 'First', status: 'done' }, { id: 'b', title: 'Second', status: 'pending' }] }
    };
    const cp = (await f.call('batch', args)).structuredContent;
    assert.equal(cp.checkpoint.status, 'succeeded'); assert.equal(cp.checkpoint.revision, c.revision + 1);
    assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'changed\n');
    const definition = fs.readFileSync(path.join(root, cp.batch_path), 'utf8');
    for (const secret of ['SECRET HANDOFF', 'SECRET DOCUMENT', 'checkpoint', 'execution']) assert.ok(!definition.includes(secret), secret);
    const savedRun = store.get('runs', run.run_id); assert.equal(savedRun.todos[0].status, 'done');
    const operationCount = store.operationCount(run.run_id);
    fs.writeFileSync(path.join(root, 'hello.txt'), 'later repair\n');
    const replay = (await f.call('batch', args)).structuredContent;
    assert.equal(replay.checkpoint.checkpoint_id, cp.checkpoint.checkpoint_id); assert.equal(store.operationCount(run.run_id), operationCount);
    assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'later repair\n');
    const audit = new AuditJournal(f.config).listForDashboard().actions.at(-1);
    assert.equal(audit.result_metadata.checkpoint_status, 'succeeded'); assert.equal(audit.result_metadata.checkpoint_id, cp.checkpoint.checkpoint_id);
    assert.ok(!fs.readFileSync(f.config.auditLogPath, 'utf8').includes('SECRET'));
  } finally { await f.close(); }
});

test('batch checkpoint preflight is rollback-only; failed and unfinished verification skip it', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await inspectRun(f, run); const s = f.runtime.coordinator; const store = s.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const base = { workspace_id: c.workspace_id, execution: { operation_key: 'bad-preflight' }, persist: false,
      operations: [{ id: 'write', tool: 'write', args: { path: 'hello.txt', content: 'changed' } }, { id: 'verify', tool: 'bash', args: { command: 'true' } }],
      checkpoint: { expected_revision: c.revision + 10, summary: 'done', next_action: 'next' } };
    const invalid = await f.call('batch', base, true); assert.equal(invalid.isError, true);
    assert.equal(store.operation(run.run_id, 'bad-preflight').state, 'failed'); assert.equal(s.unresolved(store.get('runs', run.run_id)).length, 0);
    assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'original\n');
    assert.equal(store.get('runs', run.run_id).checkpoint, undefined);
    const docsBefore = store.documentManifest(run.run_id).length;
    for (const [key, command, extra] of [['failure', 'false', {}], ['timeout', 'sleep 3', { timeout_ms: 1000 }]]) {
      const failed = await f.call('batch', { ...base, execution: { ...base.execution, operation_key: key }, checkpoint: { ...base.checkpoint, expected_revision: c.revision, documents: [{ title: 'Should not be saved', content: 'no' }] }, operations: [base.operations[0], { id: 'verify', tool: 'bash', args: { command, ...extra } }] }, true);
      assert.equal(failed.isError, true); assert.equal(failed.structuredContent.checkpoint.status, 'skipped');
      assert.equal(store.get('runs', run.run_id).revision, c.revision); assert.equal(store.documentManifest(run.run_id).length, docsBefore);
    }
    const reads = await f.call('batch', { ...base, execution: { ...base.execution, operation_key: 'read-failure' }, continue_on_error: true, checkpoint: { ...base.checkpoint, expected_revision: c.revision }, operations: [{ tool: 'read', args: { path: 'missing.txt' } }, { tool: 'read', args: { path: 'hello.txt' } }] }, true);
    assert.equal(reads.structuredContent.checkpoint.status, 'skipped'); assert.equal(reads.structuredContent.succeeded_count, 1);
    const tokenChild = await f.call('batch', { ...base, checkpoint: undefined, execution: { ...base.execution, operation_key: 'child-token' }, operations: [{ tool: 'read', args: { path: 'hello.txt', execution: base.execution } }] }, true);
    assert.equal(tokenChild.isError, true); assert.match(JSON.stringify(tokenChild), /must not contain credentials/);
    const parallel = await f.call('batch', { ...base, execution: { ...base.execution, operation_key: 'parallel-cp' }, mode: 'parallel', operations: [{ tool: 'read', args: { path: 'hello.txt' } }], checkpoint: { ...base.checkpoint, expected_revision: c.revision } }, true); assert.equal(parallel.isError, true);
  } finally { await f.close(); }
});

test('checkpoint conflict after successful verification preserves effects and supports explicit repair', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await inspectRun(f, run); const s = f.runtime.coordinator; const store = s.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { operation_key: 'race' }, persist: false,
      operations: [{ tool: 'write', args: { path: 'hello.txt', content: 'kept' } }, { tool: 'bash', args: { command: 'sleep 1; test -f hello.txt' } }],
      checkpoint: { expected_revision: c.revision, summary: 'Batch done', next_action: 'next' } };
    const pending = f.call('batch', args, true);
    for (let i = 0; i < 100 && fs.readFileSync(path.join(root, 'hello.txt'), 'utf8') !== 'kept'; i++) await delay(20);
    const other = (await f.call('work_update', { action: 'checkpoint', run_id: run.run_id, expected_revision: c.revision, request_key: 'concurrent', summary: 'Concurrent checkpoint', next_action: 'next' })).structuredContent;
    const result = await pending; assert.equal(result.isError, true); assert.equal(result.structuredContent.checkpoint.status, 'failed');
    assert.equal(store.operation(run.run_id, 'race').state, 'failed'); assert.equal(s.unresolved(store.get('runs', run.run_id)).length, 0);
    fs.writeFileSync(path.join(root, 'hello.txt'), 'repaired');
    const retry = await f.call('batch', args, true); assert.equal(retry.structuredContent.checkpoint.status, 'failed'); assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'repaired');
    const repaired = await f.call('work_update', { action: 'checkpoint', run_id: run.run_id, expected_revision: other.revision, request_key: 'repair-cp', summary: 'Repaired checkpoint', next_action: 'next' });
    assert.equal(repaired.structuredContent.revision, other.revision + 1);
  } finally { await f.close(); }
});

test('large batch replay retains the checkpoint receipt even when child outputs do not fit', async () => {
  const f = await setup(); try {
    f.config.maxOutputBytes = 100_000;
    const run = await create(f); const c = await inspectRun(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    fs.writeFileSync(path.join(root, 'large.txt'), 'data line\n'.repeat(5000));
    const args = { workspace_id: c.workspace_id, execution: { operation_key: 'large-batch' }, persist: false,
      operations: [{ tool: 'read', args: { path: 'large.txt', max_bytes: 60_000 } }, { tool: 'bash', args: { command: 'test -f large.txt' } }],
      checkpoint: { expected_revision: c.revision, summary: 'Read large input', next_action: 'Continue' } };
    const first = (await f.call('batch', args)).structuredContent;
    assert.equal(first.checkpoint.status, 'succeeded');
    const saved = store.operation(run.run_id, 'large-batch').result;
    assert.equal(saved.structuredContent.results_omitted, true); assert.ok(Buffer.byteLength(JSON.stringify(saved)) < 24_000);
    const replay = (await f.call('batch', args)).structuredContent;
    assert.equal(replay.checkpoint.checkpoint_id, first.checkpoint.checkpoint_id); assert.equal(replay.results_omitted, true);
    assert.equal(store.get('runs', run.run_id).revision, first.checkpoint.revision);
  } finally { await f.close(); }
});

test('batch Bash session credentials are preflighted and supplied outside persisted definitions', async () => {
  const f = await setup(); try {
    f.config.requireBashSession = true; f.config.bashSessionId = 'private-bash-session';
    const run = await create(f); const c = await inspectRun(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { operation_key: 'session-missing' },
      operations: [{ tool: 'write', args: { path: 'hello.txt', content: 'changed' } }, { tool: 'bash', args: { command: 'test -f hello.txt', unused_field: 'must-not-persist' } }] };
    const denied = await f.call('batch', args, true); assert.equal(denied.isError, true); assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'original\n');
    const result = (await f.call('batch', { ...args, session_id: f.config.bashSessionId, execution: { ...args.execution, operation_key: 'session-supplied' } })).structuredContent;
    const definition = fs.readFileSync(path.join(root, result.batch_path), 'utf8');
    for (const value of ['private-bash-session', 'unused_field', 'session_id']) assert.ok(!definition.includes(value));
    const resume = await f.call('batch', { workspace_id: c.workspace_id, path: result.batch_path, from: 'op_2', session_id: f.config.bashSessionId, execution: { ...args.execution, operation_key: 'session-resume' } }); assert.ok(!resume.isError);
  } finally { await f.close(); }
});

test('managed work edits and checkpoints without claims while retaining revision and retry checks', async () => {
  const f = await setup();
  try {
    const tools = (await f.client.listTools()).tools;
    assert.equal(tools.some(t => t.name === 'work_claim'), false);
    assert.equal(tools.find(t => t.name === 'work_update').inputSchema.properties.attempt_token, undefined);
    assert.equal(tools.find(t => t.name === 'bash').inputSchema.properties.execution.properties.attempt_token, undefined);
    const r = await create(f), prefix = r.run_id.slice(0, 12);
    const args = { workspace_id: r.workspace_id, path: 'hello.txt', content: 'changed\n', execution: { operation_key: 'change' } };
    const first = (await f.call('write', args)).structuredContent;
    const replay = (await f.call('write', args)).structuredContent;
    assert.equal(replay.work_receipt.operation_id, first.work_receipt.operation_id);
    assert.equal((await f.call('write', { ...args, content: 'different' }, true)).isError, true);
    const cpArgs = { action: 'checkpoint', run_id: prefix, expected_revision: r.revision, request_key: 'save', summary: 'Changed file', next_action: 'Verify', documents: [{ title: 'Evidence', content: 'Source inspected' }] };
    const cp = (await f.call('work_update', cpArgs)).structuredContent;
    const duplicate = (await f.call('work_update', { ...cpArgs, run_id: r.run_id })).structuredContent;
    assert.equal(duplicate.checkpoint_id, cp.checkpoint_id);
    assert.equal((await f.call('work_update', { ...cpArgs, request_key: 'stale' }, true)).isError, true);
    const snapshot = await status(f, r.run_id);
    assert.equal(snapshot.claimed, false); assert.equal(snapshot.timing, undefined);
    assert.equal(f.runtime.coordinator.store.children('sessions', r.run_id).length, 0);
    assert.equal(f.runtime.coordinator.store.children('iterations', r.run_id).length, 0);
    const doc = cp.documents[0];
    const read = (await f.call('work_status', { action: 'read_document', run_id: prefix, document_id: doc.document_id, document_revision: doc.document_revision })).structuredContent;
    assert.equal(read.content, 'Source inspected');
  } finally { await f.close(); }
});

test('idle time never expires work, while pause stops jobs and requires an explicit resume', async () => {
  const f = await setup();
  try {
    f.config.work.idleMs = 1;
    const r = await create(f);
    await delay(20); f.runtime.coordinator.sweep();
    const started = (await f.call('start_jobs', { workspace_id: r.workspace_id, execution: { operation_key: 'long-job' }, commands: [{ command: 'sleep 20' }] })).structuredContent;
    await delay(20); f.runtime.coordinator.sweep();
    assert.equal(getJobManager(f.config).require(started.job_ids[0]).status, 'running');
    const current = await status(f, r.run_id);
    await f.call('work_manage', { action: 'pause', run_id: r.run_id, expected_revision: current.revision, request_key: 'pause', reason: 'Operator stop' });
    let paused;
    for (let i = 0; i < 80; i++) { paused = await status(f, r.run_id); if (paused.state === 'paused') break; await delay(50); }
    assert.equal(paused.state, 'paused');
    assert.equal(getJobManager(f.config).require(started.job_ids[0]).quiescent, true);
    assert.equal((await f.call('write', { workspace_id: r.workspace_id, path: 'late.txt', content: 'late', execution: { operation_key: 'late' } }, true)).isError, true);
    assert.equal((await f.call('work_update', { action: 'finish_iteration', run_id: r.run_id, expected_revision: paused.revision, request_key: 'no-unpause', outcome: 'yielded', summary: 'Save', next_action: 'Wait' }, true)).isError, true);
    await f.call('work_manage', { action: 'resume', run_id: r.run_id, expected_revision: paused.revision, request_key: 'resume' });
    await f.call('write', { workspace_id: r.workspace_id, path: 'hello.txt', content: 'resumed', execution: { operation_key: 'resumed' } });
  } finally { await f.close(); }
});

test('queued mutations cannot cross a pause and resume generation', async () => {
  const f = await setup();
  try {
    const r = await create(f), s = f.runtime.coordinator;
    const ctx = { principalId: principalIdFromAuthInfo(f.config), requestId: 'queue', signal: new AbortController().signal };
    let release, entered;
    const gate = new Promise(resolve => { release = resolve; });
    const started = new Promise(resolve => { entered = resolve; });
    const first = runWithToolContext(ctx, () => f.runtime.invoke('write', { workspace_id: r.workspace_id, execution: { operation_key: 'first' } }, async () => { entered(); await gate; return { content: [], structuredContent: {} }; }));
    await started;
    let executed = false;
    const second = runWithToolContext(ctx, () => f.runtime.invoke('write', { workspace_id: r.workspace_id, execution: { operation_key: 'queued' } }, async () => { executed = true; return {}; }));
    const rejected = assert.rejects(second, /stopped|ready/);
    s.manage(ctx.principalId, { action: 'pause', run_id: r.run_id, expected_revision: r.revision, request_key: 'stop-queue', reason: 'Stop' });
    release(); await first; await rejected; s.sweep();
    const paused = s.require(ctx.principalId, r.run_id);
    s.manage(ctx.principalId, { action: 'resume', run_id: r.run_id, expected_revision: paused.revision, request_key: 'resume-queue' });
    assert.equal(executed, false);
  } finally { await f.close(); }
});

test('large run briefings preserve the assignment and expose lossless documents and real task cursors', async () => {
  const f = await setup({ audit: true, multi: true });
  try {
    const objective = 'Inspect the assigned owners. '.repeat(65);
    const r = await create(f, 'ralph', { project_id: 'alpha', objective, scope: 'Only the agreed audit', todos: Array.from({ length: 38 }, (_, i) => ({ id: `task-${i}`, title: `Task ${i}`, status: 'pending' })) });
    const s = f.runtime.coordinator, store = s.store;
    await f.call('work_update', { action: 'checkpoint', run_id: r.run_id, expected_revision: r.revision, request_key: 'handoff', summary: 'The last owner was inspected.', next_action: 'Inspect the next owner.' });
    const row = store.get('runs', r.run_id);
    f.config.auditRetainActions = 2;
    const journal = new AuditJournal(f.config);
    for (let i = 0; i < 20; i++) journal.record({ toolName: 'write', args: { project_id: 'beta', path: `file-${i}` }, result: { structuredContent: { project_id: 'beta', changed: true } }, mutating: true, startedAtMs: i, finishedAtMs: i + 1 });
    store.transaction(() => { for (let i = 0; i < 300; i++) s.document(row, 'note', `Historical note ${i} ${'x'.repeat(200)}`, 'Historical evidence', 'test'); });
    for (const budget of [16384, 4096]) {
      f.config.work.packetBytes = budget;
      const reply = await f.call('work_status', { action: 'get', run_id: r.run_id, section: 'packet', offset: 5, limit: 1 });
      assert.equal(reply.structuredContent.activity_warning.reason, 'expired');
      const brief = reply.structuredContent.packet;
      assert.ok(brief.objective.startsWith('Inspect')); assert.equal(brief.scope, 'Only the agreed audit');
      assert.ok(brief.checkpoint.summary); assert.ok(brief.checkpoint.next_action);
      assert.equal(brief.pages.todos.offset, 5);
      assert.ok(brief.todos.length <= 1);
      if (brief.todos.length) { assert.equal(brief.todos[0].id, 'task-5'); assert.equal(brief.pages.todos.next_offset, 6); }
      else assert.equal(brief.pages.todos.next_offset, 5);
      assert.ok(Buffer.byteLength(JSON.stringify(reply)) <= budget, `Response exceeded ${budget} bytes`);
      const spec = brief.documents.find(d => d.kind === 'spec');
      let full = '', offset = 0;
      do {
        const page = (await f.call('work_status', { action: 'read_document', run_id: r.run_id, document_id: spec.id, document_revision: spec.revision, offset, max_bytes: 2000 })).structuredContent;
        assert.equal(page.return_size.truncated, false); full += page.content; offset = page.next_offset;
      } while (offset !== null);
      assert.equal(JSON.parse(full).objective, objective);
      const task = (await f.call('work_status', { action: 'get', run_id: r.run_id, section: 'todos', offset: 5, limit: 1 })).structuredContent;
      assert.equal(task.items[0].id, 'task-5'); assert.equal(task.next_offset, 6);
    }
  } finally { await f.close(); }
});

// Stored claims are historical input to the upgrade, never a new admission path.
test('restart retires a legacy claim without deleting its checkpoint or history', async () => {
  const f = fixture(); let runtime = new WorkRuntime(f.config); await runtime.ready;
  const who = principalIdFromAuthInfo(f.config);
  try {
    const s = runtime.coordinator;
    const r = await s.create(who, { request_key: 'old-run', project_id: f.config.defaultProjectId, mode: 'ralph', title: 'Legacy', objective: 'Keep history', scope: 'file', ready: true, acceptance: [{ id: 'check', description: 'Exists', command: 'true', required: true }], todos: [] });
    const row = s.store.get('runs', r.run_id);
    s.checkpoint(who, { action: 'checkpoint', run_id: r.run_id, expected_revision: row.revision, request_key: 'old-checkpoint', summary: 'Retained handoff', next_action: 'Continue' });
    const saved = s.store.get('runs', r.run_id), checkpoint = saved.checkpoint;
    const session = { id: 'old-session', run_id: r.run_id, principal_id: who, token_hash: 'old-secret-hash', measured_ms: 100, clock_gap: false, created_at: s.now() };
    const it = { id: 'old-iteration', run_id: r.run_id, principal_id: who, session_id: session.id, token_hash: 'old-claim-hash', generation: 1, state: 'active', phase: 'execute', todo_ids: [], started_at: s.now(), measured_ms: 100, idle_ms: 0, clock: { epoch: 'old', monotonic_ms: 0, wall_ms: Date.now() }, clock_gap: false };
    s.store.save('sessions', session); s.store.save('iterations', it); saved.state = 'active'; saved.iteration_id = it.id; saved.generation = 1; s.store.saveRun(saved);
    runtime.close(); runtime = new WorkRuntime(f.config); await runtime.ready; runtime.coordinator.sweep();
    const next = runtime.coordinator.require(who, r.run_id);
    assert.equal(next.state, 'blocked'); assert.equal(next.iteration_id, undefined);
    assert.deepEqual(next.checkpoint, checkpoint);
    assert.equal(runtime.coordinator.store.get('iterations', it.id).state, 'abandoned');
    runtime.coordinator.manage(who, { action: 'resume', run_id: r.run_id, expected_revision: next.revision, request_key: 'resume-old' });
    assert.equal(runtime.coordinator.writable(who, r.run_id).state, 'ready');
  } finally { runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); }
});
