import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { randomBytes } from 'node:crypto';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const quote = s => `'${s.replaceAll("'", "'\\''")}'`;
async function command(args, env) {
  const p = spawn(process.execPath, args, { env, stdio: ['ignore', 'pipe', 'pipe'] }); let out = '', err = '';
  p.stdout.on('data', b => out += b); p.stderr.on('data', b => err += b);
  const [code] = await once(p, 'exit'); return { code, out, err };
}
async function fixture() {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'work-http-'))), repo = path.join(root, 'repo'); fs.mkdirSync(repo);
  fs.writeFileSync(path.join(repo, 'hello.txt'), 'original\n');
  fs.writeFileSync(path.join(repo, 'executor.mjs'), "import fs from 'node:fs';fs.writeFileSync('hello.txt','changed\\n');\n");
  fs.writeFileSync(path.join(repo, 'reviewer.mjs'), "console.log('CODEXPRO_REVIEW=PASS');\n");
  for (const args of [['init', '-b', 'main'], ['add', '.'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'init']]) assert.equal(spawnSync('git', args, { cwd: repo }).status, 0);
  const socket = net.createServer(); socket.listen(0, '127.0.0.1'); await once(socket, 'listening'); const port = socket.address().port; await new Promise(resolve => socket.close(resolve));
  const bin = path.join(root, 'bin'); fs.mkdirSync(bin); fs.writeFileSync(path.join(bin, 'codexpro'), `#!/bin/sh\nexec ${quote(process.execPath)} ${quote(path.resolve('scripts/codexpro.mjs'))} "$@"\n`, { mode: 0o700 });
  const token = randomBytes(32).toString('hex');
  const env = { ...process.env, CODEXPRO_ROOT: repo, CODEXPRO_ALLOWED_ROOTS: repo, CODEXPRO_HTTP_TOKEN: token, CODEXPRO_HOST: '127.0.0.1', CODEXPRO_PORT: String(port), CODEXPRO_WORK_MODE: 'on', CODEXPRO_WORK_DIR: path.join(root, 'work'),
    CODEXPRO_WORKTREE_ROOT: path.join(root, 'legacy'), CODEXPRO_JOBS_DIR: path.join(root, 'jobs'), CODEXPRO_AUDIT_MODE: 'off', CODEXPRO_WRITE_MODE: 'workspace', CODEXPRO_BASH_MODE: 'full', CODEXPRO_WORK_SWEEP_MS: '250', CODEXPRO_HOME: path.join(root, 'config'), PATH: `${bin}:${process.env.PATH}` };
  let child, stderr = '';
  const start = async () => { stderr = ''; child = spawn(process.execPath, ['dist/http.js'], { env, stdio: ['ignore', 'ignore', 'pipe'] }); child.stderr.on('data', b => stderr += b);
    for (let n = 0; n < 100; n++) { if (stderr.includes('HTTP MCP listening')) return; if (child.exitCode !== null) throw new Error(stderr); await delay(50); } throw new Error(stderr || 'startup timeout'); };
  await start(); const url = `http://127.0.0.1:${port}/mcp`;
  const clients = [];
  const connect = async () => { const client = new Client({ name: 'work-http-test', version: '1' }); await client.connect(new StreamableHTTPClientTransport(new URL(url), { requestInit: { headers: { Authorization: `Bearer ${token}` } } })); clients.push(client);
    return async (name, args, allowError = false) => { const r = await client.callTool({ name, arguments: args }); if (!allowError) assert.ok(!r.isError, JSON.stringify(r)); return r; }; };
  return { root, repo, env, url, start, connect,
    async kill() { if (child.exitCode === null) { child.kill('SIGKILL'); await once(child, 'exit'); } },
    async close() { for (const c of clients) await c.close().catch(() => {}); if (child.exitCode === null) { child.kill('SIGTERM'); await once(child, 'exit'); } fs.rmSync(root, { recursive: true, force: true }); } };
}
const createArgs = key => ({ action: 'create', request_key: key, project_id: 'default', mode: 'ralph', title: 'HTTP loop', objective: 'Change hello', scope: 'hello.txt', ready: true,
  acceptance: [{ id: 'check', description: 'File changed', command: 'grep -q changed hello.txt', required: true }], todos: [{ id: 'a', title: 'Change hello', status: 'pending', acceptance: 'hello.txt contains changed' }] });

test('HTTP claims race across transports and a fresh server recovers a vanished agent', { timeout: 30000 }, async () => {
  const f = await fixture(); try {
    const a = await f.connect(), b = await f.connect(); const run = (await a('work_manage', createArgs('http'))).structuredContent;
    const claimArgs = { run_id: run.run_id, expected_revision: run.revision, objective: 'Change', todo_ids: ['a'], check_plan: 'grep' };
    const race = await Promise.all([a('work_claim', { ...claimArgs, request_key: 'a', worker_label: 'a' }, true), b('work_claim', { ...claimArgs, request_key: 'b', worker_label: 'b' }, true)]);
    assert.equal(race.filter(r => !r.isError).length, 1); const c = race.find(r => !r.isError).structuredContent;
    await a('start_jobs', { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'partial' }, commands: [{ command: 'printf partial > hello.txt; sleep 20' }] });
    await f.kill(); await f.start(); const fresh = await f.connect();
    const list = (await fresh('work_status', { action: 'list' })).structuredContent; assert.ok(list.runs.some(r => r.run_id === run.run_id));
    let status; for (let n = 0; n < 100; n++) { status = (await fresh('work_status', { action: 'get', run_id: run.run_id, section: 'summary' })).structuredContent; if (status.state === 'blocked') break; await delay(100); }
    assert.equal(status.state, 'blocked');
    const read = await fresh('read', { workspace_id: c.workspace_id, path: 'hello.txt' }); assert.match(JSON.stringify(read), /partial/);
    const stale = await fresh('bash', { workspace_id: c.workspace_id, command: 'printf stale > hello.txt', execution: { attempt_token: c.attempt_token, operation_key: 'late' } }, true); assert.equal(stale.isError, true);
  } finally { await f.close(); }
});

test('managed CLI adapter runs the legacy engine under a server claim and closes its packet', { timeout: 45000 }, async () => {
  const f = await fixture(); try {
    const call = await f.connect(); const run = (await call('work_manage', createArgs('cli'))).structuredContent;
    const args = ['scripts/codexpro.mjs', 'loop-handoff', '--run-id', run.run_id, '--mcp-url', f.url, '--claim-key', 'cli-packet', '--command', 'node executor.mjs {{plan_file}}', '--review-command', 'node reviewer.mjs {{status_file}} {{diff_file}} {{plan_file}}', '--max-iters', '1'];
    const dry = await command([...args, '--dry-run'], f.env); assert.equal(dry.code, 0, dry.err); assert.equal(JSON.parse(dry.out).mutates, false);
    const executed = await command(args, f.env);
    if (executed.code !== 0) {
      const status = (await call('work_status', { action: 'get', run_id: run.run_id, section: 'summary' })).structuredContent;
      const jobs = (await call('jobs', { workspace_id: status.workspace_id, output: 'none' })).structuredContent.jobs;
      const logs = jobs?.length ? await call('jobs', { workspace_id: status.workspace_id, job_ids: jobs.map(j => j.id ?? j.job_id), output: 'head', wait_ms: 0 }) : jobs;
      assert.fail(`${executed.out}\n${executed.err}\n${JSON.stringify(logs)}`);
    }
    let status; for (let n = 0; n < 100; n++) { status = (await call('work_status', { action: 'get', run_id: run.run_id, section: 'summary' })).structuredContent; if (status.state === 'complete') break; await delay(100); }
    assert.equal(status.state, 'complete', JSON.stringify(status)); assert.equal(status.claimed, false);
    const original = JSON.parse(executed.out);
    const cacheDir = path.join(f.env.CODEXPRO_HOME, 'work-clients'); const cacheFile = path.join(cacheDir, fs.readdirSync(cacheDir).find(n => n.endsWith('.json')));
    assert.equal(fs.statSync(cacheFile).mode & 0o777, 0o600);
    const cached = JSON.parse(fs.readFileSync(cacheFile, 'utf8')); delete cached.final; delete cached.calls['work_update:cli-packet:finish'].reply;
    fs.writeFileSync(cacheFile, JSON.stringify(cached));
    const repeated = await command(args, f.env); assert.equal(repeated.code, 0, repeated.err); assert.equal(JSON.parse(repeated.out).checkpoint_id, original.checkpoint_id, 'A lost finish reply must replay after its claim is closed.');
    const argsFile = path.join(f.root, 'status.json'); fs.writeFileSync(argsFile, JSON.stringify({ action: 'get', run_id: run.run_id, section: 'summary' }));
    const cli = await command(['scripts/codexpro.mjs', 'work', 'status', '--mcp-url', f.url, '--args-file', argsFile], f.env); assert.equal(cli.code, 0, cli.err); assert.equal(JSON.parse(cli.out).state, 'complete');
  } finally { await f.close(); }
});

test('saved batch viewer resolves the managed worktree and preserves the checkpoint return', { timeout: 30000 }, async () => {
  const f = await fixture(); try {
    const call = await f.connect(); const run = (await call('work_manage', createArgs('viewer'))).structuredContent;
    const c = (await call('work_claim', { run_id: run.run_id, expected_revision: run.revision, request_key: 'viewer-claim', worker_label: 'viewer', objective: 'Change', todo_ids: ['a'], check_plan: 'test' })).structuredContent;
    const batch = (await call('batch', { workspace_id: c.workspace_id, execution: { attempt_token: c.attempt_token, operation_key: 'viewer-batch' }, operations: [{ id: 'saved-check', tool: 'bash', args: { command: 'test -f hello.txt' } }], checkpoint: { expected_revision: c.revision, summary: 'checked', next_action: 'continue' } })).structuredContent;
    assert.equal(batch.checkpoint.status, 'succeeded'); assert.ok(batch.batch_path);
    const query = new URLSearchParams({ project_id: 'default', workspace_id: c.workspace_id, path: batch.batch_path });
    const url = `${f.url.replace('/mcp', '/activity/batch')}?${query}`;
    const headers = { Authorization: `Bearer ${f.env.CODEXPRO_HTTP_TOKEN}` };
    assert.equal((await fetch(url)).status, 401);
    const viewed = await fetch(url, { headers }); const html = await viewed.text();
    assert.equal(viewed.status, 200, html); assert.match(html, /saved-check/); assert.match(html, /test -f hello.txt/);
    for (const secret of [c.attempt_token, c.session_token]) assert.ok(!html.includes(secret));
    // The same path does not exist in the source checkout.
    query.delete('workspace_id'); const source = await fetch(`${f.url.replace('/mcp', '/activity/batch')}?${query}`, { headers }); assert.equal(source.status, 404);
    await call('work_update', { action: 'finish_iteration', run_id: run.run_id, expected_revision: batch.checkpoint.revision, attempt_token: c.attempt_token, request_key: 'viewer-finish', summary: 'checked', next_action: 'continue', outcome: 'yielded' });
  } finally { await f.close(); }
});
