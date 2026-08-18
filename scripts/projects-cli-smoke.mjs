import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

// These harnesses realpath and stat the paths CodexPro returns, so they need the raw
// absolute form. Production defaults to redacted labels; see the redaction assertions
// at the end of scripts/smoke.mjs for coverage of the default behaviour.
process.env.CODEXPRO_EXPOSE_ABSOLUTE_PATHS = '1';

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

async function waitForHealth(url, child, output) {
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`catalog CLI exited ${child.exitCode}\n${output.value}`);
    try {
      const response = await fetch(url);
      if (response.ok) return response.json();
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`catalog CLI health timeout\n${output.value}`);
}

async function stop(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 5_000))
  ]);
  if (child.exitCode === null) {
    child.kill('SIGKILL');
    await new Promise((resolve) => child.once('exit', resolve));
  }
}

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-projects-cli-'));
const alpha = path.join(root, 'alpha');
const beta = path.join(root, 'beta');
const home = path.join(root, 'home');
await fs.mkdir(alpha);
await fs.mkdir(beta);
await fs.mkdir(home);
await fs.writeFile(path.join(alpha, 'project.txt'), 'alpha\n', 'utf8');
await fs.writeFile(path.join(beta, 'project.txt'), 'beta\n', 'utf8');
const projectsFile = path.join(root, 'projects.json');
await fs.writeFile(projectsFile, `${JSON.stringify({
  version: 1,
  defaultProject: 'beta',
  projects: [
    { id: 'alpha', label: 'Alpha', root: alpha },
    { id: 'beta', label: 'Beta', root: beta }
  ]
}, null, 2)}\n`, 'utf8');

const port = await freePort();
const output = { value: '' };
const child = spawn(process.execPath, [
  'scripts/codexpro.mjs',
  'start',
  '--projects-file', projectsFile,
  '--tunnel', 'none',
  '--no-auth',
  '--no-profile',
  '--port', String(port)
], {
  cwd: path.resolve('.'),
  env: { ...process.env, CODEXPRO_HOME: home, CI: '1', NO_COLOR: '1' },
  stdio: ['ignore', 'pipe', 'pipe']
});
child.stdout.on('data', (chunk) => { output.value += String(chunk); });
child.stderr.on('data', (chunk) => { output.value += String(chunk); });

try {
  const health = await waitForHealth(`http://127.0.0.1:${port}/healthz`, child, output);
  if (health.defaultRoot !== await fs.realpath(beta) || health.defaultProjectId !== 'beta') {
    throw new Error(`catalog CLI selected the wrong default project: ${JSON.stringify(health)}`);
  }
  if (health.projects?.map((project) => project.id).join(',') !== 'alpha,beta') {
    throw new Error(`catalog CLI omitted project inventory: ${JSON.stringify(health)}`);
  }
  const client = new Client({ name: 'codexpro-projects-cli-smoke', version: '0.1.0' });
  const transport = new StreamableHTTPClientTransport(new URL(`http://127.0.0.1:${port}/mcp`));
  try {
    await client.connect(transport);
    const projects = await client.callTool({ name: 'list_projects', arguments: {} });
    if (projects.isError || projects.structuredContent?.count !== 2) throw new Error(`list_projects failed: ${JSON.stringify(projects)}`);
    const opened = await client.callTool({
      name: 'open_workspace',
      arguments: { project_id: 'beta', include_tree: false, include_skills: false }
    });
    const workspaceId = opened.structuredContent?.workspace_id;
    if (opened.isError || opened.structuredContent?.project_id !== 'beta' || !workspaceId) {
      throw new Error(`open_workspace selected the wrong project: ${JSON.stringify(opened)}`);
    }
    const missing = await client.callTool({ name: 'read', arguments: { path: 'project.txt' } });
    if (!missing.isError || !JSON.stringify(missing).includes('workspace_id')) {
      throw new Error(`multi-project read did not require workspace_id: ${JSON.stringify(missing)}`);
    }
    const read = await client.callTool({ name: 'read', arguments: { workspace_id: workspaceId, path: 'project.txt' } });
    if (read.isError || !read.structuredContent?.text?.includes('beta')) {
      throw new Error(`workspace handle routed to the wrong project: ${JSON.stringify(read)}`);
    }
  } finally {
    await client.close();
  }
  console.log('✓ projects CLI smoke test passed');
} finally {
  await stop(child);
  await fs.rm(root, { recursive: true, force: true });
}
