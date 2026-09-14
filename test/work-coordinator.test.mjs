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
async function claim(f, run, extras = {}) { return (await f.call('work_claim', { run_id: run.run_id, expected_revision: run.revision, request_key: 'claim-1', worker_label: 'worker 1', objective: 'First packet', todo_ids: ['a'], check_plan: 'Inspect file', ...extras })).structuredContent; }
async function status(f, id) { return (await f.call('work_status', { action: 'get', run_id: id })).structuredContent; }

test('managed MCP lifecycle, receipts, manual mode, ownership and stale writers', async () => {
  const f = await setup(); try {
    const listed = await f.client.listTools();
    for (const name of ['work_status', 'work_manage', 'work_claim', 'work_update']) assert.ok(listed.tools.some(t => t.name === name));
    for (const t of listed.tools) assert.ok(!t._meta?.['openai/outputTemplate']);
    const run = await create(f); assert.equal(run.state, 'ready');
    const c = await claim(f, run); assert.match(c.attempt_token, /^claim_/); assert.equal(c.timing.continuation_recommended, true);
    const duplicate = await claim(f, run); assert.equal(duplicate.attempt_token, c.attempt_token);
    const competing = await f.call('work_claim', { run_id: run.run_id, expected_revision: c.revision, request_key: 'other', worker_label: 'other', objective: 'Other packet', todo_ids: ['b'], check_plan: 'Inspect' }, true); assert.equal(competing.isError, true);
    const ws = c.workspace_id;
    const denied = await f.call('bash', { workspace_id: ws, command: 'echo denied > hello.txt' }, true); assert.equal(denied.isError, true);
    const execution = { attempt_token: c.attempt_token, operation_key: 'effect1' };
    const write = await f.call('bash', { workspace_id: ws, command: 'printf changed > hello.txt', execution });
    const replay = await f.call('bash', { workspace_id: ws, command: 'printf changed > hello.txt', execution });
    assert.equal(write.structuredContent.work_receipt.operation_id, replay.structuredContent.work_receipt.operation_id);
    const conflicting = await f.call('bash', { workspace_id: ws, command: 'printf DIFFERENT > hello.txt', execution }, true); assert.equal(conflicting.isError, true);
    const finish = await f.call('work_update', { action: 'finish_iteration', run_id: run.run_id, expected_revision: c.revision, attempt_token: c.attempt_token, request_key: 'finish-1', summary: 'First packet done', next_action: 'Do second packet', outcome: 'completed',
      todos: [{ id: 'a', title: 'First packet', status: 'done' }, { id: 'b', title: 'Second packet', status: 'pending' }] });
    assert.equal(finish.structuredContent.timing.continuation_recommended, true);
    const s = await status(f, run.run_id); assert.equal(s.state, 'ready'); assert.equal(s.claimed, false);
    const stale = await f.call('bash', { workspace_id: ws, command: 'printf STALE > hello.txt', execution: { ...execution, operation_key: 'stale' } }, true); assert.equal(stale.isError, true);
    const second = await claim(f, s, { request_key: 'claim-2', session_token: c.session_token, todo_ids: ['b'] }); assert.ok(second.timing.session_measured_ms > 0);
    const inheritedReceipt = await f.call('bash', { workspace_id: ws, command: 'printf changed > hello.txt', execution: { attempt_token: second.attempt_token, operation_key: 'effect1' } });
    assert.equal(inheritedReceipt.structuredContent.work_receipt.operation_id, write.structuredContent.work_receipt.operation_id, 'A run-scoped operation key must not execute again in a new iteration.');
    await f.call('work_update', { action: 'finish_iteration', run_id: run.run_id, expected_revision: second.revision, attempt_token: second.attempt_token, request_key: 'finish-2', summary: 'All done', next_action: 'Verify', outcome: 'completed', finish_run_if_ready: true,
      todos: [{ id: 'a', title: 'First packet', status: 'done' }, { id: 'b', title: 'Second packet', status: 'done' }] });
    let done; for (let i = 0; i < 40; i++) { done = await status(f, run.run_id); if (done.state === 'complete') break; await delay(100); }
    assert.equal(done.state, 'complete', JSON.stringify(done));
    const manual = await create(f, 'manual'); const mc = await claim(f, manual, { request_key: 'manual-claim' }); assert.equal('continuation_recommended' in mc.timing, false); assert.equal('session_measured_ms' in mc.timing, false);
  } finally { await f.close(); }
});

test('server monotonic clock expires dead agents and fences old claims after restart', async () => {
  const f = fixture(); let mono = 0, wall = Date.now(); const clock = { sample: () => ({ epoch: 'test', monotonic_ms: mono, wall_ms: wall }) };
  f.config.work.idleMs = 1000; f.config.work.attemptMs = 5000;
  let runtime = new WorkRuntime(f.config, clock); await runtime.ready;
  const who = principalIdFromAuthInfo(f.config); const ctx = { principalId: who, requestId: 'test', signal: new AbortController().signal };
  try {
    const s = runtime.coordinator;
    const run = await s.create(who, { request_key: 'c', project_id: f.config.defaultProjectId, mode: 'ralph', title: 'Clock', objective: 'Test clock', scope: 'file', ready: true, acceptance: [{ id: 'check', description: 'true', command: 'true', required: true }], todos: [{ id: 'x', title: 'x', status: 'pending', evidence_ids: [] }] });
    const c = s.claim(who, { run_id: run.run_id, expected_revision: run.revision, request_key: 'claim', worker_label: 'worker', objective: 'packet', check_plan: 'test', todo_ids: ['x'] });
    wall += 86400000; mono += 500; s.heartbeat(who, { run_id: run.run_id, attempt_token: c.attempt_token });
    assert.equal(s.status(who, run.run_id).state, 'active');
    wall -= 172800000; mono += 1100; s.sweep();
    const expired = s.status(who, run.run_id); assert.equal(expired.state, 'ready'); assert.equal(expired.recent_iterations.at(-1).state, 'abandoned');
    assert.throws(() => s.authorize(who, run.run_id, c.attempt_token), /current work_claim/);
    const next = s.claim(who, { run_id: run.run_id, expected_revision: expired.revision, request_key: 'claim2', worker_label: 'worker2', objective: 'packet2', check_plan: 'inspect', todo_ids: ['x'] });
    assert.throws(() => new WorkRuntime(f.config, clock), /Another coordinator/);
    runtime.close(); runtime = new WorkRuntime(f.config, clock); await runtime.ready;
    const recovered = runtime.coordinator.status(who, run.run_id); assert.equal(recovered.state, 'blocked'); assert.equal(recovered.recent_iterations.at(-1).clock_gap, true);
    assert.throws(() => runtime.coordinator.authorize(who, run.run_id, next.attempt_token), /current work_claim/);
    assert.throws(() => runtime.coordinator.status('different-principal', run.run_id), /inaccessible/);
  } finally { runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); }
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

test('batch and supertool share fencing; documents page without data loss; lost finish reply is idempotent', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await claim(f, run);
    const envelope = { attempt_token: c.attempt_token, operation_key: 'batch-1' };
    const batch = await f.call('batch', { workspace_id: c.workspace_id, execution: envelope, persist: false, operations: [
      { id: 'one', tool: 'bash', args: { command: 'printf one > one.txt' } }, { id: 'two', tool: 'bash', args: { command: 'printf two > two.txt' } }
    ] }); assert.ok(batch.structuredContent.work_receipt);
    const superResult = await f.call('codexpro', { action: 'bash', args: { workspace_id: c.workspace_id, command: 'test -f one.txt && test -f two.txt' }, execution: { attempt_token: c.attempt_token, operation_key: 'wrapped' } });
    assert.ok(superResult.structuredContent.work_receipt);
    const text = 'Memory café 😀\n'.repeat(500);
    const put = (await f.call('work_update', { action: 'put_document', run_id: run.run_id, expected_revision: c.revision, request_key: 'doc', attempt_token: c.attempt_token, kind: 'project_memory', title: 'Reusable memory', content: text })).structuredContent;
    let content = '', offset = 0;
    do { const page = (await f.call('work_status', { action: 'read_document', run_id: run.run_id, document_id: put.document_id, document_revision: put.document_revision, offset, max_bytes: 1024 })).structuredContent;
      assert.equal(page.return_size.truncated, false); content += page.content; offset = page.next_offset;
    } while (offset !== null);
    assert.equal(content, text);
    const search = (await f.call('work_status', { action: 'search_memory', project_id: f.config.defaultProjectId, query: 'café' })).structuredContent; assert.equal(search.total_matches, 1);
    const finishArgs = { action: 'finish_iteration', run_id: run.run_id, expected_revision: put.revision, request_key: 'finish-lost', attempt_token: c.attempt_token, summary: 'Changed source', next_action: 'Continue', outcome: 'yielded' };
    const first = (await f.call('work_update', finishArgs)).structuredContent;
    const duplicate = (await f.call('work_update', finishArgs)).structuredContent; assert.equal(first.checkpoint_id, duplicate.checkpoint_id);
    const staleBatch = await f.call('batch', { workspace_id: c.workspace_id, execution: envelope, persist: false, operations: [{ id: 'three', tool: 'bash', args: { command: 'touch stale.txt' } }] }, true); assert.equal(staleBatch.isError, true);
    const staleWrapper = await f.call('codexpro', { action: 'bash', args: { workspace_id: c.workspace_id, command: 'touch stale.txt', execution: envelope } }, true); assert.equal(staleWrapper.isError, true);
  } finally { await f.close(); }
});

test('expired agent with a live job remains unclaimable until the job is stopped', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await claim(f, run);
    const job = await f.call('start_jobs', { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'long' }, commands: [{ command: 'trap "" TERM; while true; do sleep 1; done' }] });
    const id = job.structuredContent.job_ids[0];
    const store = f.runtime.coordinator.store; const row = store.get('runs', run.run_id); row.limits.idle_ms = 1; store.saveRun(row);
    await delay(10); f.runtime.coordinator.sweep();
    let s = await status(f, run.run_id); assert.equal(s.state, 'recovering');
    const denied = await f.call('work_claim', { run_id: run.run_id, expected_revision: s.revision, request_key: 'too-early', worker_label: 'successor', objective: 'packet', todo_ids: ['a'], check_plan: 'inspect' }, true); assert.equal(denied.isError, true);
    for (let n = 0; n < 100; n++) { s = await status(f, run.run_id); if (s.state === 'ready') break; await delay(100); }
    assert.equal(s.state, 'ready', JSON.stringify(s));
    assert.equal(getJobManager(f.config).require(id).quiescent, true);
    const lateStop = await f.call('stop_jobs', { workspace_id: c.workspace_id, job_ids: [id], execution: { attempt_token: c.attempt_token, operation_key: 'late-stop' } }, true); assert.equal(lateStop.isError, true);
  } finally { await f.close(); }
});

test('verification rejects changed source, and planning claims cannot execute effects', async () => {
  const f = await setup(); try {
    const r = (await f.call('work_manage', { action: 'create', request_key: 'draft', project_id: f.config.defaultProjectId, mode: 'manual', title: 'Draft', objective: 'plan', scope: 'file' })).structuredContent;
    const p = await claim(f, r, { phase: 'plan', todo_ids: [], request_key: 'plan' });
    const denied = await f.call('bash', { workspace_id: p.workspace_id, command: 'true', execution: { attempt_token: p.attempt_token, operation_key: 'no' } }, true); assert.equal(denied.isError, true);
    await f.call('work_update', { action: 'finish_iteration', run_id: r.run_id, expected_revision: p.revision, attempt_token: p.attempt_token, request_key: 'planned', summary: 'Spec ready', next_action: 'Verify', outcome: 'completed', acceptance: [{ id: 'badcheck', description: 'Changes source', command: 'printf altered > hello.txt', required: true }], todos: [] });
    const s = await status(f, r.run_id);
    await f.call('work_manage', { action: 'finish_run', run_id: r.run_id, expected_revision: s.revision, request_key: 'verify' });
    let final; for (let n = 0; n < 50; n++) { final = await status(f, r.run_id); if (final.state === 'blocked') break; await delay(100); }
    assert.equal(final.state, 'blocked'); assert.match(final.recovery_reason ?? '', /Source changed/);
  } finally { await f.close(); }
});

test('Ralph recommendation boundary is server-measured and linked sessions accumulate', async () => {
  const f = fixture(); let mono = 0; const clock = { sample: () => ({ epoch: 'boundary', monotonic_ms: mono, wall_ms: 1_800_000_000_000 }) };
  f.config.work.attemptMs = 25 * 60_000; f.config.work.idleMs = 25 * 60_000;
  const runtime = new WorkRuntime(f.config, clock); await runtime.ready; const s = runtime.coordinator; const who = principalIdFromAuthInfo(f.config);
  try {
    const run = await s.create(who, { request_key: 'create', project_id: f.config.defaultProjectId, mode: 'ralph', title: 'Timing', objective: 'Timing', scope: 'file', ready: true,
      acceptance: [{ id: 'check', description: 'true', command: 'true', required: true }], todos: [{ id: 'a', title: 'a', status: 'pending', evidence_ids: [] }, { id: 'b', title: 'b', status: 'pending', evidence_ids: [] }] });
    const a = s.claim(who, { run_id: run.run_id, expected_revision: run.revision, request_key: 'a', worker_label: 'a', objective: 'a', todo_ids: ['a'], check_plan: 'test' });
    mono = 20 * 60_000;
    s.checkpoint(who, { action: 'finish_iteration', run_id: run.run_id, expected_revision: a.revision, request_key: 'done-a', attempt_token: a.attempt_token, summary: 'first', next_action: 'second', outcome: 'completed',
      todos: [{ id: 'a', title: 'a', status: 'done', evidence_ids: [] }, { id: 'b', title: 'b', status: 'pending', evidence_ids: [] }] }); s.sweep();
    const next = s.status(who, run.run_id); const b = s.claim(who, { run_id: run.run_id, expected_revision: next.revision, request_key: 'b', worker_label: 'a', objective: 'b', todo_ids: ['b'], check_plan: 'test', session_token: a.session_token });
    mono = 1_799_999; assert.equal(s.heartbeat(who, { run_id: run.run_id, attempt_token: b.attempt_token, elapsed_ms: 999999999 }).timing.continuation_recommended, true);
    mono++; assert.equal(s.heartbeat(who, { run_id: run.run_id, attempt_token: b.attempt_token }).timing.continuation_recommended, false);
    mono++; assert.equal(s.heartbeat(who, { run_id: run.run_id, attempt_token: b.attempt_token }).timing.session_measured_ms, 1_800_001);
    const before = s.status(who, run.run_id).timing.session_measured_ms;
    for (let n = 0; n < 10; n++) s.status(who, run.run_id);
    assert.equal(s.status(who, run.run_id).timing.session_measured_ms, before);
  } finally { runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); }
});

test('control-store failure before job registration cannot execute a command', async () => {
  const f = await setup(); const store = f.runtime.coordinator.store; const original = store.saveJob;
  try {
    const run = await create(f); const c = await claim(f, run);
    store.saveJob = () => { throw new Error('Simulated work receipt storage failure'); };
    const reply = await f.call('bash', { workspace_id: c.workspace_id, command: 'printf unsafe > must-not-exist.txt', execution: { attempt_token: c.attempt_token, operation_key: 'fault' } }, true);
    assert.equal(reply.isError, true);
    store.saveJob = original;
    const workspace = store.get('runs', run.run_id).workspace;
    assert.equal(fs.existsSync(path.join(workspace.root, 'must-not-exist.txt')), false);
    assert.ok(getJobManager(f.config).list(c.workspace_id).every(j => j.status !== 'running' && j.quiescent));
  } finally { store.saveJob = original; await f.close(); }
});

test('escaped document content remains lossless at the smallest response budget', async () => {
  const f = await setup(); try {
    const run = await create(f); const c = await claim(f, run);
    const content = '\u0000😀'.repeat(400);
    const doc = (await f.call('work_update', { action: 'put_document', run_id: run.run_id, expected_revision: c.revision, request_key: 'escaped-doc', attempt_token: c.attempt_token, title: 'Control bytes', content })).structuredContent;
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

test('final acceptance jobs cannot exceed the remaining run budget', async () => {
  const f = await setup(); try {
    const r = (await f.call('work_manage', { ...{ action: 'create', request_key: 'budget-check', project_id: f.config.defaultProjectId, mode: 'manual', title: 'Budget', objective: 'Verify', scope: 'file', ready: true }, acceptance: [{ id: 'slow', description: 'Slow check', command: 'sleep 10', required: true }], todos: [] })).structuredContent;
    const store = f.runtime.coordinator.store; const run = store.get('runs', r.run_id); run.limits.active_ms = 100; store.saveRun(run);
    await f.call('work_manage', { action: 'finish_run', run_id: r.run_id, expected_revision: r.revision, request_key: 'budget-verify' });
    const jobs = getJobManager(f.config).list(run.workspace.id); assert.equal(jobs.length, 1); assert.ok(jobs[0].timeout_ms <= 100);
    for (let n = 0; n < 40 && jobs[0].status === 'running'; n++) await delay(100);
    const s = await status(f, r.run_id); assert.notEqual(s.state, 'complete'); assert.ok(s.timing.run_measured_ms <= 100);
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
      const c = await claim(f, run, { request_key: `claim-${project}` });
      assert.equal(last().workspace_id, c.workspace_id); assert.equal(last().operation, 'work.claim');
      const base = { run_id: run.run_id, expected_revision: c.revision, attempt_token: c.attempt_token };
      await f.call('work_update', { ...base, action: 'heartbeat', request_key: `heart-${project}` });
      assert.equal(last().project_id, project); assert.equal(last().operation, 'work.heartbeat');
      assert.equal(last().git_before, undefined); assert.equal(last().git_after, undefined);
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
      for (const secret of [c.attempt_token, c.session_token, 'PRIVATE MEMORY SUMMARY', 'PRIVATE DOCUMENT TEXT']) assert.ok(!log.includes(secret));
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
    const run = await create(f); const c = await claim(f, run, { phase: 'plan' });
    const store = f.runtime.coordinator.store;
    const base = { run_id: run.run_id, expected_revision: c.revision, attempt_token: c.attempt_token, action: 'checkpoint', request_key: 'bulk', summary: 'Planned', next_action: 'Execute' };
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
    const originalCap = f.config.work.maxDocuments; f.config.work.maxDocuments = countBefore + 1;
    const capacity = await f.call('work_update', { ...base, expected_revision: cp.revision, request_key: 'capacity', documents: [{ title: 'three', content: '3' }, { title: 'four', content: '4' }] }, true);
    assert.equal(capacity.isError, true); assert.equal(store.documentManifest(run.run_id).length, countBefore); f.config.work.maxDocuments = originalCap;
    const final = (await f.call('work_update', { ...revisionArgs, action: 'finish_iteration', outcome: 'yielded' })).structuredContent;
    assert.equal(final.documents[0].document_revision, 2); assert.equal(store.document(run.run_id, doc.id).kind, 'decision');
    assert.equal(store.get('runs', run.run_id).iteration_id, undefined);
  } finally { await f.close(); }
});

test('managed batch edits, verifies and checkpoints without persisting credentials or replaying effects', async () => {
  const f = await setup({ audit: true }); try {
    const run = await create(f); const c = await claim(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'edit-checkpoint' },
      operations: [{ id: 'write', tool: 'write', args: { path: 'hello.txt', content: 'changed\n' } }, { id: 'verify', tool: 'bash', args: { command: 'test "$(cat hello.txt)" = changed' } }],
      checkpoint: { expected_revision: c.revision, summary: 'SECRET HANDOFF', next_action: 'Next packet', documents: [{ title: 'Decision', content: 'SECRET DOCUMENT' }], todos: [{ id: 'a', title: 'First', status: 'done' }, { id: 'b', title: 'Second', status: 'pending' }] }
    };
    const cp = (await f.call('batch', args)).structuredContent;
    assert.equal(cp.checkpoint.status, 'succeeded'); assert.equal(cp.checkpoint.revision, c.revision + 1);
    assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'changed\n');
    const definition = fs.readFileSync(path.join(root, cp.batch_path), 'utf8');
    for (const secret of [c.attempt_token, c.session_token, 'SECRET HANDOFF', 'SECRET DOCUMENT', 'checkpoint', 'execution']) assert.ok(!definition.includes(secret), secret);
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
    const run = await create(f); const c = await claim(f, run); const s = f.runtime.coordinator; const store = s.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const base = { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'bad-preflight' }, persist: false,
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
    const run = await create(f); const c = await claim(f, run); const s = f.runtime.coordinator; const store = s.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'race' }, persist: false,
      operations: [{ tool: 'write', args: { path: 'hello.txt', content: 'kept' } }, { tool: 'bash', args: { command: 'sleep 1; test -f hello.txt' } }],
      checkpoint: { expected_revision: c.revision, summary: 'Batch done', next_action: 'next' } };
    const pending = f.call('batch', args, true);
    for (let i = 0; i < 100 && fs.readFileSync(path.join(root, 'hello.txt'), 'utf8') !== 'kept'; i++) await delay(20);
    const other = (await f.call('work_update', { action: 'checkpoint', run_id: run.run_id, attempt_token: c.attempt_token, expected_revision: c.revision, request_key: 'concurrent', summary: 'Concurrent checkpoint', next_action: 'next' })).structuredContent;
    const result = await pending; assert.equal(result.isError, true); assert.equal(result.structuredContent.checkpoint.status, 'failed');
    assert.equal(store.operation(run.run_id, 'race').state, 'failed'); assert.equal(s.unresolved(store.get('runs', run.run_id)).length, 0);
    fs.writeFileSync(path.join(root, 'hello.txt'), 'repaired');
    const retry = await f.call('batch', args, true); assert.equal(retry.structuredContent.checkpoint.status, 'failed'); assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'repaired');
    const repaired = await f.call('work_update', { action: 'checkpoint', run_id: run.run_id, attempt_token: c.attempt_token, expected_revision: other.revision, request_key: 'repair-cp', summary: 'Repaired checkpoint', next_action: 'next' });
    assert.equal(repaired.structuredContent.revision, other.revision + 1);
  } finally { await f.close(); }
});

test('large batch replay retains the checkpoint receipt even when child outputs do not fit', async () => {
  const f = await setup(); try {
    f.config.maxOutputBytes = 100_000;
    const run = await create(f); const c = await claim(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    fs.writeFileSync(path.join(root, 'large.txt'), 'data line\n'.repeat(5000));
    const args = { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'large-batch' }, persist: false,
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
    const run = await create(f); const c = await claim(f, run); const store = f.runtime.coordinator.store;
    const root = store.get('runs', run.run_id).workspace.root;
    const args = { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'session-missing' },
      operations: [{ tool: 'write', args: { path: 'hello.txt', content: 'changed' } }, { tool: 'bash', args: { command: 'test -f hello.txt', unused_field: 'must-not-persist' } }] };
    const denied = await f.call('batch', args, true); assert.equal(denied.isError, true); assert.equal(fs.readFileSync(path.join(root, 'hello.txt'), 'utf8'), 'original\n');
    const result = (await f.call('batch', { ...args, session_id: f.config.bashSessionId, execution: { ...args.execution, operation_key: 'session-supplied' } })).structuredContent;
    const definition = fs.readFileSync(path.join(root, result.batch_path), 'utf8');
    for (const value of ['private-bash-session', c.attempt_token, 'unused_field', 'session_id']) assert.ok(!definition.includes(value));
    const resume = await f.call('batch', { workspace_id: c.workspace_id, path: result.batch_path, from: 'op_2', session_id: f.config.bashSessionId, execution: { ...args.execution, operation_key: 'session-resume' } }); assert.ok(!resume.isError);
  } finally { await f.close(); }
});
