import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig, effectiveOperationalLimits, reloadOperationalLimits, installOperationalLimitReload } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { getJobManager } from '../dist/jobs.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function eventually(check, label, timeoutMs = 5000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const value = await check();
    if (value) return value;
    await delay(10);
  }
  assert.fail(label);
}

async function fixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-limit-reload-'));
  const previous = Object.fromEntries(Object.entries(process.env).filter(([key]) => /^(CODEXPRO_|CODEBASE_BRIDGE_)/.test(key)));
  for (const key of Object.keys(previous)) delete process.env[key];
  let config;
  try {
    process.env.CODEXPRO_HOME = root;
    process.env.CODEXPRO_ENV_FILE = path.join(root, 'env');
    config = loadConfig(['--root', root, '--bash', 'full', '--audit', 'off', '--tool-mode', 'full']);
  } finally {
    for (const key of Object.keys(process.env)) if (/^(CODEXPRO_|CODEBASE_BRIDGE_)/.test(key)) delete process.env[key];
    Object.assign(process.env, previous);
  }
  return { root, config, write: contents => fs.writeFile(config.limitsEnvFile, contents, { mode: 0o600 }),
    close: () => fs.rm(root, { recursive: true, force: true }) };
}

test('reload applies operational limits from quoted, continued and duplicate assignments without expanding secrets', async () => {
  const f = await fixture();
  const messages = [];
  try {
    const oldPort = f.config.port;
    const oldJobsDir = f.config.jobsDir;
    const oldRetention = f.config.jobRetentionMs;
    const secret = 'secret-value-that-must-never-be-logged';
    await f.write([
      '# Existing systemd EnvironmentFile, with a later duplicate taking precedence.',
      'CODEXPRO_JOB_TIMEOUT_MS=10000',
      'CODEXPRO_JOB_TIMEOUT_MS="20000"',
      "CODEXPRO_MAX_JOBS='3'",
      'CODEXPRO_MAX_JOBS_PER_WORKSPACE=\\',
      '2',
      'CODEXPRO_AUDIT_RETAIN_ACTIONS=350',
      'CODEXPRO_MAX_JOB_OUTPUT_BYTES=65536',
      'CODEXPRO_MAX_RETAINED_JOB_BYTES=131072',
      'CODEXPRO_MAX_JOB_HISTORY_PER_WORKSPACE=5',
      'CODEXPRO_PORT=12345',
      'CODEXPRO_JOBS_DIR=/ignored-private-path',
      'CODEXPRO_TUNNEL=cloudflare',
      `CODEXPRO_HTTP_TOKEN="${secret}"`,
      'CODEXPRO_UNKNOWN=$HOME/`touch should-not-execute`',
      ''
    ].join('\n'));
    const result = reloadOperationalLimits(f.config, message => messages.push(message));
    assert.equal(result.ok, true);
    assert.equal(f.config.jobTimeoutMs, 20000);
    assert.equal(f.config.maxJobs, 3);
    assert.equal(f.config.maxJobsPerWorkspace, 2);
    assert.equal(f.config.auditRetainActions, 350);
    assert.equal(f.config.maxJobOutputBytes, 65536);
    assert.equal(f.config.maxRetainedJobBytes, 131072);
    assert.equal(f.config.maxJobHistoryPerWorkspace, 5);
    assert.equal(f.config.jobRetentionMs, oldRetention, 'missing values retain their current effective limits');
    assert.equal(f.config.port, oldPort);
    assert.equal(f.config.jobsDir, oldJobsDir);
    assert.equal(f.config.authToken, undefined);
    assert.deepEqual(result.ignored.sort(), ['CODEXPRO_HTTP_TOKEN', 'CODEXPRO_JOBS_DIR', 'CODEXPRO_PORT', 'CODEXPRO_TUNNEL', 'CODEXPRO_UNKNOWN'].sort());
    assert.equal(effectiveOperationalLimits(f.config).CODEXPRO_JOB_TIMEOUT_MS, 20000);
    assert.match(messages.join('\n'), /CODEXPRO_JOB_TIMEOUT_MS: .* -> 20000/);
    assert.match(messages.join('\n'), /ignored startup-only keys/);
    for (const value of [secret, '/ignored-private-path', 'touch should-not-execute']) {
      assert.equal(messages.join('\n').includes(value), false, 'ignored values stay out of logs');
    }
    assert.equal(await fs.access(path.join(f.root, 'should-not-execute')).then(() => true, () => false), false);
  } finally { await f.close(); }
});

test('invalid numeric values reject the complete reload and preserve every old limit', async () => {
  const f = await fixture();
  try {
    const original = effectiveOperationalLimits(f.config);
    for (const invalid of ['not-a-number', '', '10000.5', '9999', '21600001']) {
      const messages = [];
      await f.write(`CODEXPRO_MAX_JOBS=2\nCODEXPRO_JOB_TIMEOUT_MS=${invalid}\n`);
      const result = reloadOperationalLimits(f.config, message => messages.push(message));
      assert.equal(result.ok, false, JSON.stringify(invalid));
      assert.deepEqual(result.changed, []);
      assert.deepEqual(effectiveOperationalLimits(f.config), original);
      assert.match(messages.join('\n'), /rejected; previous limits kept/);
      if (invalid === 'not-a-number') assert.equal(messages.join('\n').includes(invalid), false);
    }
  } finally { await f.close(); }
});

test('malformed or unreadable EnvironmentFiles reject atomically without revealing their contents', async () => {
  const f = await fixture();
  try {
    const original = effectiveOperationalLimits(f.config);
    const secret = 'private-malformed-token';
    for (const malformed of [
      `CODEXPRO_MAX_JOBS=2\nCODEXPRO_HTTP_TOKEN="${secret}\n`,
      `CODEXPRO_MAX_JOBS=2\nCODEXPRO_HTTP_TOKEN="\u0000${secret}"\n`,
      Buffer.from([0x43, 0x3d, 0xff])
    ]) {
      const messages = [];
      await f.write(malformed);
      assert.equal(reloadOperationalLimits(f.config, message => messages.push(message)).ok, false);
      assert.deepEqual(effectiveOperationalLimits(f.config), original);
      assert.equal(messages.join('\n').includes(secret), false);
    }
    await fs.rm(f.config.limitsEnvFile);
    assert.equal(reloadOperationalLimits(f.config, () => {}).ok, false);
    assert.deepEqual(effectiveOperationalLimits(f.config), original);
  } finally { await f.close(); }
});

test('SIGHUP installation runs maintenance only for accepted reloads and removes its listener', async () => {
  const f = await fixture();
  const listeners = process.listenerCount('SIGHUP');
  let maintained = 0;
  const cleanup = installOperationalLimitReload(f.config, () => { maintained++; }, () => {});
  try {
    assert.equal(process.listenerCount('SIGHUP'), listeners + 1);
    await f.write('CODEXPRO_JOB_TIMEOUT_MS=20000\n');
    process.emit('SIGHUP');
    assert.equal(f.config.jobTimeoutMs, 20000);
    assert.equal(maintained, 1);
    await f.write('CODEXPRO_JOB_TIMEOUT_MS=invalid\n');
    process.emit('SIGHUP');
    assert.equal(f.config.jobTimeoutMs, 20000);
    assert.equal(maintained, 1);
  } finally {
    cleanup();
    assert.equal(process.listenerCount('SIGHUP'), listeners);
    await f.close();
  }
});

test('reload during a foreground wait preserves launch deadlines and output budgets through promotion', { timeout: 15000 }, async () => {
  const f = await fixture();
  Object.assign(f.config, { jobTimeoutMs: 30000, maxJobOutputBytes: 131072, maxJobs: 4, maxJobsPerWorkspace: 4 });
  const previousScopes = process.env.CODEXPRO_JOB_SCOPES;
  process.env.CODEXPRO_JOB_SCOPES = '0';
  let manager;
  try { manager = getJobManager(f.config); }
  finally {
    if (previousScopes === undefined) delete process.env.CODEXPRO_JOB_SCOPES;
    else process.env.CODEXPRO_JOB_SCOPES = previousScopes;
  }
  const server = createCodexProServer(f.config);
  const client = new Client({ name: 'limit-reload-race-test', version: '1' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  let foreground;
  try {
    const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
    const workspaceId = opened.structuredContent.workspace_id;
    const existing = await client.callTool({ name: 'start_jobs', arguments: { workspace_id: workspaceId, commands: [{ command: 'sleep 22' }] } });
    assert.notEqual(existing.isError, true, JSON.stringify(existing));
    const existingId = existing.structuredContent.job_ids[0];
    foreground = client.callTool({ name: 'bash', arguments: { workspace_id: workspaceId, command: 'sleep 20', timeout_ms: 1000 } });
    const original = await eventually(() => manager.runningJobs().find(job => job.origin === 'foreground'), 'foreground job did not launch');
    const originalDeadline = original.deadline_ms;
    const originalLimit = original.output_limit_bytes;
    await f.write('CODEXPRO_JOB_TIMEOUT_MS=10000\nCODEXPRO_MAX_JOB_OUTPUT_BYTES=65536\nCODEXPRO_MAX_JOBS=1\nCODEXPRO_MAX_JOBS_PER_WORKSPACE=1\n');
    assert.equal(reloadOperationalLimits(f.config, () => {}).ok, true);
    const promoted = await foreground;
    assert.notEqual(promoted.isError, true, JSON.stringify(promoted));
    assert.equal(promoted.structuredContent.job_origin, 'promoted');
    const running = manager.require(original.id);
    assert.equal(running.status, 'running', 'lowered quotas do not revoke an already-admitted foreground job at promotion');
    assert.equal(manager.require(existingId).status, 'running');
    assert.equal(manager.backgroundRunningCount(workspaceId), 2, 'existing jobs can temporarily exceed a lowered background quota');
    assert.equal(manager.capacity(workspaceId), 0);
    assert.equal(running.deadline_ms, originalDeadline);
    assert.equal(running.timeout_ms, 30000);
    assert.equal(running.output_limit_bytes, originalLimit);
    const spec = JSON.parse(await fs.readFile(path.join(f.config.jobsDir, `${original.id}.spec.json`), 'utf8'));
    assert.equal(spec.deadline, originalDeadline);
    assert.equal(spec.limit, originalLimit);

    await f.write('CODEXPRO_MAX_JOBS=4\nCODEXPRO_MAX_JOBS_PER_WORKSPACE=4\n');
    assert.equal(reloadOperationalLimits(f.config, () => {}).ok, true);
    const started = await client.callTool({ name: 'start_jobs', arguments: { workspace_id: workspaceId, commands: [{ command: 'sleep 21' }] } });
    assert.notEqual(started.isError, true, JSON.stringify(started));
    const newJob = manager.require(started.structuredContent.job_ids[0]);
    assert.equal(newJob.timeout_ms, 10000);
    assert.equal(newJob.output_limit_bytes, 65536);
    assert.equal(newJob.deadline_ms - newJob.started_at_ms, 10000);

    await f.write('CODEXPRO_MAX_JOBS=1\nCODEXPRO_MAX_JOBS_PER_WORKSPACE=1\n');
    assert.equal(reloadOperationalLimits(f.config, () => {}).ok, true);
    assert.equal(manager.capacity(workspaceId), 0);
    assert.equal(manager.runningJobs(workspaceId).length, 3, 'lower capacities do not stop existing jobs');
    const rejected = await client.callTool({ name: 'start_jobs', arguments: { workspace_id: workspaceId, commands: [{ command: 'echo over-capacity' }] } });
    assert.equal(rejected.isError, true);
    assert.equal(rejected.structuredContent.error_code, 'job_limit_reached');
  } finally {
    for (const job of manager.runningJobs()) manager.stop(job.id);
    await manager.waitFor(manager.runningJobs().map(job => job.id), 'all', 6000);
    await foreground?.catch(() => {});
    await client.close();
    await server.close();
    await f.close();
  }
});
