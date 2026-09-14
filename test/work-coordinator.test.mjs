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

function fixture() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codexpro-work-'));
  const repo = path.join(dir, 'repo'); fs.mkdirSync(repo); fs.writeFileSync(path.join(repo, 'hello.txt'), 'original\n');
  for (const argv of [['init', '-b', 'main'], ['add', '.'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'initial']]) {
    const p = spawnSync('git', argv, { cwd: repo, encoding: 'utf8' }); assert.equal(p.status, 0, p.stderr);
  }
  const config = loadConfig(['--root', repo, '--bash', 'full', '--write', 'workspace', '--tool-mode', 'full', '--work', 'on', '--work-dir', path.join(dir, 'work'), '--worktree-root', path.join(dir, 'legacy'), '--worktree-base-ref', 'main', '--audit', 'off']);
  config.jobsDir = path.join(dir, 'jobs'); config.work.sweepMs = 250; config.worktreeBaseRef = 'main';
  return { dir, repo, config };
}
async function setup() {
  const f = fixture(); const server = createCodexProServer(f.config), client = new Client({ name: 'work-test', version: '1' });
  const [a, b] = InMemoryTransport.createLinkedPair(); await server.connect(a); await client.connect(b);
  const runtime = getWorkRuntime(f.config); await runtime.ready;
  const call = async (name, args, error = false) => { const result = await client.callTool({ name, arguments: args });
    if (!error) assert.ok(!result.isError, JSON.stringify(result)); return result; };
  return { ...f, server, client, runtime, call, async close() { for (const j of getJobManager(f.config).runningJobs()) getJobManager(f.config).stop(j.id); await delay(200); await client.close(); await server.close(); runtime.close(); fs.rmSync(f.dir, { recursive: true, force: true }); } };
}
async function create(f, mode = 'ralph') {
  const r = await f.call('work_manage', { action: 'create', request_key: `create-${mode}`, project_id: f.config.defaultProjectId, mode, title: 'Test loop', objective: 'Test durable work', scope: 'hello.txt', ready: true,
    acceptance: [{ id: 'check', description: 'file exists', command: 'test -f hello.txt', required: true }],
    todos: [{ id: 'a', title: 'First packet', status: 'pending' }, { id: 'b', title: 'Second packet', status: 'pending' }] });
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
