// Verifies graceful shutdown: SIGTERM lets an in-flight tool call finish,
// refuses new sessions with 503 meanwhile, then exits 0.
import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-drain-'));
const port = 20000 + Math.floor(Math.random() * 20000);
const token = 'drain-smoke-token-0123456789abcdef';
const baseUrl = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, ['dist/http.js'], {
  cwd: path.resolve('.'),
  env: {
    ...process.env,
    CODEXPRO_ROOT: tmp,
    CODEXPRO_ALLOWED_ROOTS: tmp,
    CODEXPRO_HOST: '127.0.0.1',
    CODEXPRO_PORT: String(port),
    CODEXPRO_BASH_MODE: 'full',
    CODEXPRO_HTTP_TOKEN: token,
    CODEXPRO_DRAIN_TIMEOUT_MS: '20000'
  },
  stdio: ['ignore', 'ignore', 'pipe']
});
let stderr = '';
server.stderr.on('data', (chunk) => { stderr += String(chunk); });
const exited = new Promise((resolve) => server.once('exit', (code, signal) => resolve({ code, signal })));

async function waitForHealth() {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    try {
      const response = await fetch(`${baseUrl}/healthz`);
      if (response.status === 401 || response.status === 200) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`server did not become healthy\n${stderr}`);
}

async function connect() {
  const transport = new StreamableHTTPClientTransport(new URL(`${baseUrl}/mcp`), {
    requestInit: { headers: { Authorization: `Bearer ${token}` } }
  });
  const client = new Client({ name: 'drain-smoke', version: '0.0.0' });
  await client.connect(transport);
  return client;
}

try {
  await waitForHealth();
  const client = await connect();
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: { include_tree: false } });
  const workspaceId = opened.structuredContent.workspace_id;
  const started = Date.now();
  const inFlight = client.callTool({
    name: 'bash',
    arguments: { workspace_id: workspaceId, command: 'sleep 3 && echo drained-ok', timeout_ms: 30000 }
  });
  await new Promise((resolve) => setTimeout(resolve, 500));
  server.kill('SIGTERM');
  await new Promise((resolve) => setTimeout(resolve, 300));

  // A new session during the drain must be refused, not hang.
  let refused = false;
  try {
    const late = await connect();
    await late.close();
  } catch (error) {
    refused = /503|restarting|ECONNREFUSED|fetch failed/i.test(String(error?.message ?? error));
    if (!refused) throw error;
  }
  if (!refused) throw new Error('new session was accepted while draining');

  const result = await inFlight;
  const text = result.content?.find((part) => part.type === 'text')?.text ?? '';
  if (result.isError || result.structuredContent.exitCode !== 0 || !text.includes('drained-ok')) {
    throw new Error(`in-flight bash call did not complete cleanly: ${JSON.stringify(result.structuredContent)}`);
  }
  if (Date.now() - started < 3000) throw new Error('bash call returned before the command could finish');

  const exit = await Promise.race([exited, new Promise((resolve) => setTimeout(() => resolve({ code: 'timeout' }), 15000))]);
  if (exit.code !== 0) throw new Error(`server did not exit 0 after draining: ${JSON.stringify(exit)}\n${stderr}`);
  if (!/draining 1 in-flight/.test(stderr) || !/drained; exiting/.test(stderr)) throw new Error(`drain log lines missing:\n${stderr}`);
  console.log('✓ drain smoke test passed');
} finally {
  if (server.exitCode === null) server.kill('SIGKILL');
  await fs.rm(tmp, { recursive: true, force: true });
}
