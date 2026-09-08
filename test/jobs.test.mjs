import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { loadConfig } from '../dist/config.js';
import { AuditJournal } from '../dist/audit.js';
import { createCodexProServer } from '../dist/server.js';
import { JobManager } from '../dist/jobs.js';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function fixture(env = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-jobs-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-jobs-home-'));
  const previous = {};
  for (const [key, value] of Object.entries({ CODEXPRO_JOBS_DIR: path.join(home, 'jobs'), CODEXPRO_MAX_JOBS: '4', CODEXPRO_MAX_JOBS_PER_WORKSPACE: '2', CODEXPRO_JOB_TIMEOUT_MS: '10000', ...env })) {
    previous[key] = process.env[key];
    process.env[key] = value;
  }
  const config = loadConfig(['--root', root, '--bash', 'full', '--audit', 'metadata', '--audit-log', path.join(home, 'audit', 'tool-calls.jsonl')]);
  for (const [key, value] of Object.entries(previous)) {
    if (value === undefined) delete process.env[key]; else process.env[key] = value;
  }
  const server = createCodexProServer(config);
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  const client = new Client({ name: 'jobs-test', version: '0' });
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  return {
    config, client, root, home,
    workspaceId: opened.structuredContent.workspace_id,
    async close() {
      await client.close();
      await fs.rm(root, { recursive: true, force: true });
      await fs.rm(home, { recursive: true, force: true });
    }
  };
}

test('explicit background jobs run detached and are collected with jobs(job_ids, wait_ms)', async () => {
  const f = await fixture();
  try {
    const bashSchema = (await f.client.listTools()).tools.find((tool) => tool.name === 'bash').inputSchema;
    assert.equal(bashSchema.properties.background, undefined, 'background is a hidden compatibility parameter');
    assert.ok(bashSchema.properties.on_timeout);
    const started = await f.client.callTool({
      name: 'bash',
      arguments: { workspace_id: f.workspaceId, command: 'sleep 2; echo background-done', background: true }
    });
    assert.notEqual(started.isError, true);
    assert.equal(started.structuredContent.job_status, 'running');
    assert.equal(started.structuredContent.job_origin, 'background');
    const jobId = started.structuredContent.job_id;
    assert.match(jobId, /^job_[0-9a-f]{8}$/);
    assert.match(started.content[0].text, /background job/);

    // The status line rides along on unrelated tool calls while the job runs.
    const tree = await f.client.callTool({ name: 'tree', arguments: { workspace_id: f.workspaceId, max_depth: 1 } });
    assert.equal(tree.structuredContent.background_jobs[0].job_id, jobId);
    assert.match(tree.content[0].text, /Background jobs: job_/);

    const collected = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId, job_ids: [jobId], wait_ms: 8000 } });
    assert.notEqual(collected.isError, true);
    assert.equal(collected.structuredContent.all_finished, true);
    assert.equal(collected.structuredContent.all_succeeded, true);
    assert.equal(collected.structuredContent.jobs[0].status, 'succeeded');
    assert.equal(collected.structuredContent.jobs[0].exit_code, 0);
    assert.match(collected.structuredContent.jobs[0].stdout_tail, /background-done/);

    // Once collected, the finished job no longer decorates other results.
    const tree2 = await f.client.callTool({ name: 'tree', arguments: { workspace_id: f.workspaceId, max_depth: 1 } });
    assert.equal(tree2.structuredContent.background_jobs, undefined);

    const listed = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId } });
    assert.equal(listed.structuredContent.jobs.length, 1);
    assert.equal(listed.structuredContent.jobs[0].job_id, jobId);
  } finally {
    await f.close();
  }
});

test('quick background commands return complete; foreground commands keep their result shape', async () => {
  const f = await fixture();
  try {
    const quick = await f.client.callTool({ name: 'bash', arguments: { workspace_id: f.workspaceId, command: 'echo quick', background: true } });
    assert.equal(quick.structuredContent.job_status, 'succeeded');
    assert.equal(quick.structuredContent.exit_code, 0);
    assert.match(quick.structuredContent.stdout, /quick/);

    const fg = await f.client.callTool({ name: 'bash', arguments: { workspace_id: f.workspaceId, command: 'echo out; echo err 1>&2; exit 3' } });
    assert.equal(fg.structuredContent.exit_code, 3);
    assert.equal(fg.structuredContent.job_origin, 'foreground');
    assert.match(fg.structuredContent.stdout, /out/);
    assert.match(fg.structuredContent.stderr, /err/);
    assert.equal(fg.structuredContent.timed_out, false);
    assert.equal(fg.structuredContent.background_jobs, undefined);
  } finally {
    await f.close();
  }
});

test('a foreground command that outruns timeout_ms is promoted to the background instead of killed', async () => {
  const f = await fixture();
  try {
    const startedAt = Date.now();
    const promoted = await f.client.callTool({
      name: 'bash',
      arguments: { workspace_id: f.workspaceId, command: 'echo early; sleep 2; echo late', timeout_ms: 1000 }
    });
    assert.ok(Date.now() - startedAt < 2000, 'call returned at the timeout');
    assert.equal(promoted.structuredContent.job_status, 'running');
    assert.equal(promoted.structuredContent.job_origin, 'promoted');
    assert.equal(promoted.structuredContent.timed_out, false);
    assert.match(promoted.structuredContent.stdout, /early/);
    assert.match(promoted.content[0].text, /moved to the background/);
    const done = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId, job_ids: [promoted.structuredContent.job_id], wait_ms: 8000, full_output: true } });
    assert.equal(done.structuredContent.jobs[0].status, 'succeeded');
    assert.match(done.structuredContent.jobs[0].stdout, /early\nlate/);

    const killed = await f.client.callTool({
      name: 'bash',
      arguments: { workspace_id: f.workspaceId, command: 'sleep 5; echo never', timeout_ms: 1000, on_timeout: 'kill' }
    });
    assert.equal(killed.structuredContent.timed_out, true);
    assert.equal(killed.structuredContent.job_status, 'timed_out');
    assert.match(killed.structuredContent.stderr, /timed out after 1000 ms/);
  } finally {
    await f.close();
  }
});

test('start_jobs fans out, wait_for=any returns the first finisher, stop_jobs ends the rest, caps are per workspace', async () => {
  const f = await fixture();
  try {
    const started = await f.client.callTool({
      name: 'start_jobs',
      arguments: { workspace_id: f.workspaceId, commands: [{ label: 'fast', command: 'sleep 3; echo fast-done' }, { label: 'slow', command: 'sleep 30' }] }
    });
    assert.notEqual(started.isError, true);
    assert.equal(started.structuredContent.jobs.length, 2);
    assert.deepEqual(started.structuredContent.jobs.map((job) => job.label), ['fast', 'slow']);
    const [fastId, slowId] = started.structuredContent.job_ids;

    const overCap = await f.client.callTool({ name: 'start_jobs', arguments: { workspace_id: f.workspaceId, commands: [{ command: 'sleep 5' }] } });
    assert.equal(overCap.isError, true);
    assert.equal(overCap.structuredContent.error_code, 'job_limit_reached');
    assert.equal(overCap.structuredContent.capacity, 0);

    const first = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId, job_ids: [fastId, slowId], wait_for: 'any', wait_ms: 10000 } });
    assert.equal(first.structuredContent.all_finished, false);
    assert.equal(first.structuredContent.running_count, 1);
    assert.equal(first.structuredContent.all_succeeded, false);
    assert.equal(first.structuredContent.requested_wait_ms, 10000);
    assert.ok(first.structuredContent.waited_ms < 9000);
    const fast = first.structuredContent.jobs.find((job) => job.job_id === fastId);
    assert.equal(fast.status, 'succeeded');
    assert.match(fast.stdout_tail, /fast-done/);

    const stopped = await f.client.callTool({ name: 'stop_jobs', arguments: { workspace_id: f.workspaceId, job_ids: [slowId, fastId] } });
    assert.notEqual(stopped.isError, true);
    assert.deepEqual(stopped.structuredContent.stopped_ids, [slowId]);
    assert.deepEqual(stopped.structuredContent.already_finished_ids, [fastId]);
    assert.equal(stopped.structuredContent.jobs.find((job) => job.job_id === slowId).status, 'stopped');

    const missing = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId, job_ids: ['job_00000000'] } });
    assert.equal(missing.structuredContent.error_code, 'job_not_found');

    const batch = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, mode: 'serial', operations: [{ id: 'bg', tool: 'bash', args: { command: 'echo x', background: true } }] }
    });
    assert.equal(batch.isError, true);
    assert.match(batch.structuredContent.error, /background bash is not allowed inside a batch/);
  } finally {
    await f.close();
  }
});

test('listing preserves completion reminders; collection reports actual wait and recoverable output bounds', async () => {
  const f = await fixture();
  try {
    const started = await f.client.callTool({ name: 'start_jobs', arguments: {
      workspace_id: f.workspaceId, commands: [{ command: "printf beginning; head -c 130000 /dev/zero | tr '\\0' x; printf ending; exit 3" }]
    } });
    const [jobId] = started.structuredContent.job_ids;
    const listed = await f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId } });
    assert.equal(listed.structuredContent.jobs[0].status, 'failed');
    const before = await f.client.callTool({ name: 'tree', arguments: { workspace_id: f.workspaceId } });
    assert.ok(before.structuredContent.background_jobs.some((job) => job.job_id === jobId));
    const full = await f.client.callTool({ name: 'jobs', arguments: {
      workspace_id: f.workspaceId, job_ids: [jobId], wait_ms: 30000, full_output: true
    } });
    assert.equal(full.structuredContent.all_finished, true);
    assert.equal(full.structuredContent.all_succeeded, false);
    assert.equal(full.structuredContent.requested_wait_ms, 30000);
    assert.ok(full.structuredContent.waited_ms < 1000);
    assert.equal(full.structuredContent.jobs[0].output_mode, 'head');
    assert.equal(full.structuredContent.jobs[0].output_truncated, true);
    assert.match(full.structuredContent.jobs[0].stdout, /^beginning/);
    assert.match(full.content[0].text, /full_output=false for the ending/);
    const tail = await f.client.callTool({ name: 'jobs', arguments: {
      workspace_id: f.workspaceId, job_ids: [jobId], wait_ms: 0
    } });
    assert.equal(tail.structuredContent.jobs[0].output_mode, 'tail');
    assert.match(tail.structuredContent.jobs[0].stdout_tail, /ending$/);
    const after = await f.client.callTool({ name: 'tree', arguments: { workspace_id: f.workspaceId } });
    assert.equal(after.structuredContent.background_jobs, undefined);
  } finally {
    await f.close();
  }
});

test('a drain interrupts pending collects immediately and reports server_restarting', async () => {
  const f = await fixture();
  try {
    const started = await f.client.callTool({ name: 'start_jobs', arguments: { workspace_id: f.workspaceId, commands: [{ command: 'sleep 20' }] } });
    const [jobId] = started.structuredContent.job_ids;
    const manager = (await import('../dist/jobs.js')).getJobManager(f.config);
    const collecting = f.client.callTool({ name: 'jobs', arguments: { workspace_id: f.workspaceId, job_ids: [jobId], wait_ms: 20000 } });
    await sleep(300);
    const t0 = Date.now();
    manager.interruptWaits();
    const result = await collecting;
    assert.ok(Date.now() - t0 < 2000, 'collect returned promptly on drain');
    assert.equal(result.structuredContent.server_restarting, true);
    assert.equal(result.structuredContent.jobs[0].status, 'running');
    await f.client.callTool({ name: 'stop_jobs', arguments: { workspace_id: f.workspaceId, job_ids: [jobId] } });
  } finally {
    await f.close();
  }
});

test('job table survives a new manager instance and completions are journaled', async () => {
  const f = await fixture();
  try {
    const started = await f.client.callTool({ name: 'bash', arguments: { workspace_id: f.workspaceId, command: 'sleep 1; echo persisted', background: true } });
    const jobId = started.structuredContent.job_id;
    await sleep(2500);
    const fresh = new JobManager(f.config);
    const job = fresh.get(jobId);
    assert.equal(job.status, 'succeeded');
    assert.equal(job.exit_code, 0);
    assert.match(fresh.readTail(job, 1024).stdout, /persisted/);

    const journal = new AuditJournal(f.config);
    const actions = journal.list({ limit: 50 }).actions;
    const completion = actions.find((action) => action.tool_name === 'bash_job');
    assert.ok(completion, 'bash_job completion record');
    assert.equal(completion.status, 'succeeded');
    assert.equal(completion.result_metadata.job_id, jobId);
    assert.equal(completion.operation, 'command.background');
  } finally {
    await f.close();
  }
});

test('timeout classification only fires on genuine timeouts', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-classify-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-classify-home-'));
  try {
    const config = loadConfig(['--root', root, '--audit', 'metadata', '--audit-log', path.join(home, 'audit', 'tool-calls.jsonl')]);
    const journal = new AuditJournal(config);
    journal.record({
      toolName: 'batch',
      args: { workspace_id: 'ws' },
      error: new Error('Invalid arguments for batch: operations.0.args.timeout_ms: Number must be less than or equal to 600000'),
      startedAtMs: Date.now() - 10,
      finishedAtMs: Date.now(),
      mutating: false
    });
    const [action] = journal.list({ limit: 5 }).actions;
    assert.equal(action.status, 'failed');
    assert.notEqual(action.error_code, 'timeout');
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});
