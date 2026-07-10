import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

function git(cwd, args) {
  const result = spawnSync('git', args, { cwd, encoding: 'utf8', shell: false });
  if (result.status !== 0) throw new Error(result.stderr || result.stdout || `git ${args.join(' ')} failed`);
}

async function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      server.close(() => typeof address === 'object' && address ? resolve(address.port) : reject(new Error('no port')));
    });
    server.on('error', reject);
  });
}

function startServer(root, home, port, token) {
  return spawn('node', ['dist/http.js'], {
    cwd: path.resolve('.'),
    env: {
      ...process.env,
      CODEXPRO_ROOT: root,
      CODEXPRO_ALLOWED_ROOTS: root,
      CODEXPRO_HOME: home,
      CODEXPRO_PORT: String(port),
      CODEXPRO_HTTP_TOKEN: token,
      CODEXPRO_BASH_MODE: 'safe',
      CODEXPRO_WRITE_MODE: 'workspace',
      CODEXPRO_TOOL_MODE: 'full',
      CODEXPRO_WORKTREE_MODE: 'mcp',
      CODEXPRO_MAX_WORKTREES: '12'
    },
    stdio: ['ignore', 'pipe', 'pipe']
  });
}

async function waitForServer(child) {
  return new Promise((resolve, reject) => {
    let stderr = '';
    const timer = setTimeout(() => reject(new Error(`server timeout\n${stderr}`)), 15_000);
    child.stderr.on('data', (chunk) => {
      stderr += String(chunk);
      if (stderr.includes('HTTP MCP listening')) {
        clearTimeout(timer);
        resolve();
      }
    });
    child.once('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`server exited ${code}\n${stderr}`));
    });
  });
}

async function stopServer(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGTERM');
  await new Promise((resolve) => child.once('exit', resolve));
}

async function withClient(url, token, operation) {
  const client = new Client({ name: 'codexpro-worktree-smoke', version: '0.1.0' });
  const transport = new StreamableHTTPClientTransport(new URL(url), {
    requestInit: { headers: { Authorization: `Bearer ${token}` } }
  });
  try {
    await client.connect(transport);
    return await operation(client);
  } finally {
    await client.close();
  }
}

async function tool(client, name, args = {}, expectError = false) {
  const result = await client.callTool({ name, arguments: args });
  if (Boolean(result.isError) !== expectError) {
    throw new Error(`${name} unexpected result: ${JSON.stringify(result)}`);
  }
  return result;
}

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-worktree-http-repo-'));
const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-worktree-http-home-'));
const port = await freePort();
const token = 'worktree-http-smoke-token';
const url = `http://127.0.0.1:${port}/mcp`;
git(root, ['init', '-q']);
git(root, ['config', 'user.email', 'http-smoke@example.com']);
git(root, ['config', 'user.name', 'HTTP Smoke']);
await fs.writeFile(path.join(root, 'shared.txt'), 'base\n', 'utf8');
git(root, ['add', '.']);
git(root, ['commit', '-q', '-m', 'initial']);

let child = startServer(root, home, port, token);
try {
  await waitForServer(child);
  let workspaceA;
  await withClient(url, token, async (client) => {
    const names = (await client.listTools()).tools.map((item) => item.name);
    if (!names.includes('create_workspace') || names.includes('open_current_workspace') || names.includes('list_workspaces')) {
      throw new Error(`unexpected MCP worktree tool surface: ${names.join(', ')}`);
    }
    const created = await tool(client, 'create_workspace', { idempotency_key: 'http-a', include_skills: false });
    workspaceA = created.structuredContent.workspace_id;
    if (!/^wt_/.test(workspaceA) || !created.structuredContent.branch) throw new Error('create_workspace omitted handle metadata');
    await tool(client, 'write', { workspace_id: workspaceA, path: 'shared.txt', content: 'workspace A\n' });
    const missing = await tool(client, 'read', { path: 'shared.txt' }, true);
    if (!JSON.stringify(missing).includes('workspace_id')) throw new Error('missing workspace_id did not fail closed');
    await tool(client, 'read', { workspace_id: workspaceA, path: '.git' }, true);
  });

  await withClient(url, token, async (client) => {
    const resumed = await tool(client, 'open_workspace', { workspace_id: workspaceA, include_tree: false, include_skills: false });
    if (resumed.structuredContent.workspace_id !== workspaceA) throw new Error('fresh transport did not resume workspace A');
    const read = await tool(client, 'read', { workspace_id: workspaceA, path: 'shared.txt' });
    if (!read.structuredContent.text.includes('workspace A')) throw new Error('fresh transport read wrong workspace A contents');
  });

  let workspaceB;
  await withClient(url, token, async (client) => {
    const created = await tool(client, 'create_workspace', { idempotency_key: 'http-b', include_skills: false });
    workspaceB = created.structuredContent.workspace_id;
    await tool(client, 'write', { workspace_id: workspaceB, path: 'shared.txt', content: 'workspace B\n' });
    const a = await tool(client, 'read', { workspace_id: workspaceA, path: 'shared.txt' });
    if (!a.structuredContent.text.includes('workspace A')) throw new Error('workspace B write leaked into workspace A');
    await tool(client, 'remove_workspace', { workspace_id: workspaceB }, true);
    await tool(client, 'release_workspace', { workspace_id: workspaceB });
  });

  await stopServer(child);
  const rotatedToken = 'worktree-http-smoke-rotated-token';
  child = startServer(root, home, port, rotatedToken);
  await waitForServer(child);
  await withClient(url, rotatedToken, async (client) => {
    const read = await tool(client, 'read', { workspace_id: workspaceA, path: 'shared.txt' });
    if (!read.structuredContent.text.includes('workspace A')) throw new Error('process restart did not preserve workspace handle');
  });

  console.log('✓ MCP worktree HTTP smoke test passed');
} finally {
  await stopServer(child).catch(() => {});
  await fs.rm(home, { recursive: true, force: true });
  await fs.rm(root, { recursive: true, force: true });
}
