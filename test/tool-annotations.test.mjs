import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { createWorkspaceAccess } from '../dist/workspaceAccess.js';

async function withTools({ bash = 'full', write = 'workspace', worktree = 'off' } = {}, inspect) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-tool-annotations-'));
  let server, client;
  try {
    const repo = path.join(root, 'repo');
    await fs.mkdir(repo);
    if (worktree === 'mcp') {
      for (const args of [['init', '-q'], ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-q', '--allow-empty', '-m', 'initial']]) {
        const result = spawnSync('git', args, { cwd: repo, encoding: 'utf8' });
        assert.equal(result.status, 0, result.stderr);
      }
    }
    const config = loadConfig([
      '--root', repo, '--tool-mode', 'full', '--bash', bash, '--write', write,
      '--work', 'off', '--worktree-mode', worktree, '--worktree-root', path.join(root, 'worktrees'),
      '--handoff-mode', 'on', '--codex-sessions', 'read',
      '--audit', 'metadata', '--audit-log', path.join(root, 'audit.jsonl')
    ]);
    config.jobsDir = path.join(root, 'jobs');
    config.codexDir = path.join(root, 'codex');
    server = createCodexProServer(config, await createWorkspaceAccess(config));
    client = new Client({ name: 'tool-annotations-test', version: '1' });
    const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
    await server.connect(serverTransport);
    await client.connect(clientTransport);
    const { tools } = await client.listTools();
    for (const tool of tools) {
      for (const hint of ['readOnlyHint', 'destructiveHint', 'openWorldHint']) {
        assert.equal(typeof tool.annotations?.[hint], 'boolean', `${tool.name}.${hint} must be explicit`);
      }
    }
    await inspect(new Map(tools.map(tool => [tool.name, tool])));
  } finally {
    await client?.close();
    await server?.close();
    await fs.rm(root, { recursive: true, force: true });
  }
}

function expectHints(tools, names, expected) {
  for (const name of names) {
    const tool = tools.get(name);
    assert.ok(tool, `missing tool ${name}`);
    for (const [hint, value] of Object.entries(expected)) {
      assert.equal(tool.annotations[hint], value, `${name}.${hint}`);
    }
  }
}

const readOnly = { readOnlyHint: true, destructiveHint: false, openWorldHint: false };
const localWrite = { readOnlyHint: false, destructiveHint: true, openWorldHint: false };
const execution = { readOnlyHint: false, destructiveHint: true, openWorldHint: true };

test('tools/list distinguishes read tools, local overwrites, cancellation and command execution', async () => {
  await withTools({}, tools => {
    expectHints(tools, [
      'server_config', 'activity_list', 'activity_get', 'activity_status', 'activity_export',
      'open_current_workspace', 'open_workspace', 'list_workspaces', 'inspect_workspace',
      'tree', 'search', 'ast_grep', 'read', 'view_image', 'show_changes', 'jobs',
      'read_handoff', 'wait_for_handoff', 'codex_context', 'codex_sessions',
      'read_codex_session', 'codexpro_inventory', 'load_skill'
    ], readOnly);
    expectHints(tools, [
      'write', 'edit', 'apply_patch', 'import_file', 'stop_jobs',
      'export_pro_context', 'handoff_to_agent', 'codexpro_self_test'
    ], localWrite);
    expectHints(tools, ['bash', 'start_jobs', 'commit_changes', 'codexpro', 'batch'], execution);
  });
});

test('worktree metadata distinguishes hook execution, release and removal', async () => {
  await withTools({ bash: 'safe', worktree: 'mcp' }, tools => {
    expectHints(tools, ['create_workspace'], execution);
    expectHints(tools, ['release_workspace'], { readOnlyHint: false, destructiveHint: false, openWorldHint: false });
    expectHints(tools, ['remove_workspace'], localWrite);
  });
});

for (const [bash, write, expected] of [
  ['off', 'off', readOnly],
  ['off', 'workspace', localWrite],
  ['safe', 'off', execution],
  ['full', 'off', execution]
]) {
  test(`batch annotations cover enabled capabilities (bash=${bash}, write=${write})`, async () => {
    await withTools({ bash, write }, tools => expectHints(tools, ['batch'], expected));
  });
}
