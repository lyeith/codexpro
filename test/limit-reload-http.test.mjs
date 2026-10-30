import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import { createHash, randomBytes } from 'node:crypto';
import { spawn } from 'node:child_process';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function eventually(check, label, diagnostics, timeoutMs = 10000) {
  const end = Date.now() + timeoutMs;
  while (Date.now() < end) {
    const value = await check();
    if (value) return value;
    await delay(30);
  }
  assert.fail(`${label}\n${diagnostics()}`);
}

async function freePort() {
  const listener = net.createServer();
  await new Promise((resolve, reject) => { listener.once('error', reject); listener.listen(0, '127.0.0.1', resolve); });
  const port = listener.address().port;
  await new Promise((resolve, reject) => listener.close(error => error ? reject(error) : resolve()));
  return port;
}

test('launcher SIGHUP reloads HTTP limits and retention in the existing process and MCP session', { timeout: 30000, skip: process.platform === 'win32' }, async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-http-limit-reload-'));
  const repo = path.join(root, 'repo');
  const home = path.join(root, 'home');
  await fs.mkdir(repo);
  const envFile = path.join(root, 'env');
  const tokenFile = path.join(root, 'token');
  const auditFile = path.join(root, 'audit.jsonl');
  const token = randomBytes(32).toString('hex');
  await fs.writeFile(tokenFile, token, { mode: 0o600 });
  await fs.writeFile(envFile, '', { mode: 0o600 });
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const headers = { Authorization: `Bearer ${token}` };
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(CODEXPRO_|CODEBASE_BRIDGE_|LISTEN_)/.test(key)));
  Object.assign(env, {
    CODEXPRO_HOME: home, CODEXPRO_ENV_FILE: envFile, CODEXPRO_JOBS_DIR: path.join(root, 'jobs'),
    CODEXPRO_CODEX_DIR: path.join(root, 'codex'), CODEXPRO_JOB_SCOPES: '0',
    CODEXPRO_JOB_TIMEOUT_MS: '10000', CODEXPRO_MAX_BASH_TIMEOUT_MS: '1000', CODEXPRO_BASH_TIMEOUT_MS: '1000',
    CODEXPRO_MAX_JOBS: '1', CODEXPRO_MAX_JOBS_PER_WORKSPACE: '1',
    CODEXPRO_AUDIT_RETAIN_ACTIONS: '20', CODEXPRO_DRAIN_TIMEOUT_MS: '1000'
  });
  const child = spawn(process.execPath, [
    'scripts/codexpro.mjs', 'start', '--root', repo, '--tunnel', 'none', '--headless',
    '--port', String(port), '--token-file', tokenFile, '--bash', 'full', '--audit', 'metadata', '--audit-log', auditFile
  ], { cwd: process.cwd(), env, stdio: ['ignore', 'ignore', 'pipe'] });
  let stderr = '';
  child.stderr.on('data', chunk => { stderr += chunk; });
  const closed = new Promise(resolve => child.once('close', (code, signal) => resolve({ code, signal })));
  const diagnostics = () => stderr.replaceAll(token, '<redacted>');
  const client = new Client({ name: 'http-limit-reload-test', version: '1' });
  let runtimePid;
  try {
    await eventually(async () => {
      assert.equal(child.exitCode, null, diagnostics());
      try { return (await fetch(base + '/healthz', { headers, signal: AbortSignal.timeout(1000) })).ok; } catch { return false; }
    }, 'HTTP server did not start', diagnostics);
    const realRepo = await fs.realpath(repo);
    const profileId = createHash('sha256').update(realRepo).digest('hex').slice(0, 24);
    const runtimePath = path.join(home, 'runtime', `${profileId}.json`);
    const runtime = await eventually(async () => {
      try {
        const value = JSON.parse(await fs.readFile(runtimePath, 'utf8'));
        return value.pid === child.pid && Number.isInteger(value.runtimePid) ? value : false;
      } catch { return false; }
    }, 'launcher did not publish runtime identity', diagnostics);
    runtimePid = runtime.runtimePid;
    assert.notEqual(runtimePid, child.pid);
    const transport = new StreamableHTTPClientTransport(new URL(base + '/mcp'), { requestInit: { headers } });
    await client.connect(transport);
    const originalSession = transport.sessionId;
    assert.ok(originalSession);
    const call = (name, args = {}) => client.callTool({ name, arguments: args });
    const initial = await call('server_config');
    assert.equal(initial.structuredContent.jobTimeoutMs, 10000);
    assert.equal(initial.structuredContent.maxCommandsPerCall, 1);
    const tools = await client.listTools();
    assert.ok(tools.tools.some(tool => tool.name === 'start_jobs'));

    // Produce enough completed server actions to require compaction on reload.
    for (let i = 0; i < 4; i++) await call('server_config');
    const before = (await fs.readFile(auditFile, 'utf8')).trim().split('\n');
    assert.ok(before.length > 2);
    const ignoredSecret = 'never-log-this-reload-token';
    await fs.writeFile(envFile, [
      'CODEXPRO_JOB_TIMEOUT_MS=20000',
      'CODEXPRO_MAX_BASH_TIMEOUT_MS=2000',
      'CODEXPRO_BASH_TIMEOUT_MS=1500',
      'CODEXPRO_MAX_JOBS=3',
      'CODEXPRO_MAX_JOBS_PER_WORKSPACE=3',
      'CODEXPRO_AUDIT_RETAIN_ACTIONS=2',
      'CODEXPRO_PORT=12345',
      `CODEXPRO_HTTP_TOKEN=${ignoredSecret}`,
      ''
    ].join('\n'));
    assert.equal(child.kill('SIGHUP'), true);
    // No MCP call intervenes: the reload callback itself must compact the journal.
    await eventually(async () => {
      if (!stderr.includes('limits reload CODEXPRO_')) return false;
      const lines = (await fs.readFile(auditFile, 'utf8')).trim().split('\n');
      return lines.length === 2;
    }, 'reload did not enforce retention without another tool call', diagnostics);
    const reloaded = await call('server_config');
    assert.equal(reloaded.structuredContent.jobTimeoutMs, 20000);
    assert.equal(reloaded.structuredContent.bashTimeoutMs, 1500);
    assert.equal(reloaded.structuredContent.maxBashTimeoutMs, 2000);
    assert.equal(reloaded.structuredContent.auditRetainActions, 2);
    assert.equal(reloaded.structuredContent.maxCommandsPerCall, 3);
    assert.equal(reloaded.structuredContent.maxActiveCaptures, 6);
    assert.equal(reloaded.structuredContent.reloadableLimits.CODEXPRO_JOB_TIMEOUT_MS, 20000);
    assert.equal(reloaded.structuredContent.port, port, 'startup-only port is ignored');
    assert.equal(stderr.includes(ignoredSecret), false);
    assert.match(stderr, /ignored startup-only keys.*CODEXPRO_HTTP_TOKEN/);

    const opened = await call('open_current_workspace');
    const workspaceId = opened.structuredContent.workspace_id;
    const bash = await call('bash', { workspace_id: workspaceId, command: 'echo increased-wait-limit', timeout_ms: 1500 });
    assert.notEqual(bash.isError, true, JSON.stringify(bash));
    const fanout = await call('start_jobs', { workspace_id: workspaceId, commands: [{ command: 'echo first' }, { command: 'echo second' }] });
    assert.notEqual(fanout.isError, true, JSON.stringify(fanout));
    assert.equal(fanout.structuredContent.jobs.length, 2, 'an established MCP session accepts increased command limits');

    const rejectionCount = stderr.split('limits reload rejected').length;
    await fs.writeFile(envFile, 'CODEXPRO_JOB_TIMEOUT_MS=25000\nCODEXPRO_MAX_JOBS=\n');
    child.kill('SIGHUP');
    await eventually(() => stderr.split('limits reload rejected').length > rejectionCount, 'invalid reload did not report rejection', diagnostics);
    const rejected = await call('server_config');
    assert.equal(rejected.structuredContent.jobTimeoutMs, 20000, 'valid changes are rolled back with invalid peers');
    assert.equal(rejected.structuredContent.maxJobs, 3);

    await fs.writeFile(envFile, 'CODEXPRO_MAX_JOBS_PER_WORKSPACE=1\nCODEXPRO_MAX_BASH_TIMEOUT_MS=1000\n');
    child.kill('SIGHUP');
    await eventually(async () => (await call('server_config')).structuredContent.maxCommandsPerCall === 1, 'second reload did not apply', diagnostics);
    const tooMany = await call('start_jobs', { workspace_id: workspaceId, commands: [{ command: 'echo third' }, { command: 'echo fourth' }] });
    assert.equal(tooMany.isError, true);
    assert.equal(tooMany.structuredContent.error_code, 'args_invalid');
    const tooLong = await call('bash', { workspace_id: workspaceId, command: 'echo excessive-wait', timeout_ms: 1500 });
    assert.equal(tooLong.isError, true);
    assert.equal(tooLong.structuredContent.error_code, 'args_invalid');

    assert.equal(transport.sessionId, originalSession);
    assert.equal(child.exitCode, null);
    assert.equal(child.signalCode, null);
    process.kill(child.pid, 0);
    process.kill(runtimePid, 0);
    const after = JSON.parse(await fs.readFile(runtimePath, 'utf8'));
    assert.equal(after.pid, runtime.pid);
    assert.equal(after.runtimePid, runtime.runtimePid);
    assert.equal((await fetch(base + '/healthz', { headers })).status, 200);
  } finally {
    await client.close().catch(() => {});
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    let timer;
    const result = await Promise.race([closed, new Promise(resolve => { timer = setTimeout(() => resolve(false), 5000); })]);
    clearTimeout(timer);
    if (!result) {
      child.kill('SIGKILL');
      if (runtimePid) { try { process.kill(runtimePid, 'SIGTERM'); } catch {} }
      await closed;
    } else if (runtimePid) {
      // If a regression kills the launcher on HUP, still remove its owned child.
      try { process.kill(runtimePid, 'SIGTERM'); } catch {}
    }
    await fs.rm(root, { recursive: true, force: true });
  }
});
