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

function git(root, args) {
  const result = spawnSync('git', args, {
    cwd: root,
    encoding: 'utf8',
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', LC_ALL: 'C' }
  });
  assert.equal(result.status, 0, result.stderr || result.stdout || `git ${args.join(' ')} failed`);
  return result.stdout.trim();
}

async function connect(root, options = {}) {
  const args = [
    '--root', root,
    '--tool-mode', 'full',
    '--bash', 'off',
    '--write', options.write ?? 'workspace'
  ];
  const config = loadConfig(args);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-batch-store-edge-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  assert.notEqual(opened.isError, true);
  return {
    config,
    server,
    client,
    workspaceId: opened.structuredContent.workspace_id,
    close: async () => {
      await client.close();
      await server.close();
    }
  };
}

async function generatedFiles(root) {
  try {
    return (await fs.readdir(path.join(root, '.codexpro-batches')))
      .filter((name) => /^[0-9A-F]{4}\.json$/i.test(name))
      .sort();
  } catch (error) {
    if (error?.code === 'ENOENT') return [];
    throw error;
  }
}

test('generated batch definitions normalize ids, use restricted permissions, and scope Git exclusion to a nested workspace', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-nested-git-'));
  const repo = path.join(temp, 'repo');
  const workspace = path.join(repo, 'packages', 'app');
  await fs.mkdir(workspace, { recursive: true });
  await fs.writeFile(path.join(workspace, 'present.txt'), 'present\n', 'utf8');
  git(repo, ['init']);
  git(repo, ['config', 'user.email', 'batch@example.test']);
  git(repo, ['config', 'user.name', 'Batch Test']);

  const f = await connect(workspace);
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [{ tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.git_excluded, true);
    assert.equal(result.structuredContent.auto_stored, true);
    assert.equal(result.structuredContent.results[0].id, 'op_1');

    const batchPath = path.join(workspace, result.structuredContent.batch_path);
    const definition = JSON.parse(await fs.readFile(batchPath, 'utf8'));
    assert.equal(definition.operations[0].id, 'op_1');
    assert.equal(definition.version, 1);
    if (process.platform !== 'win32') {
      const stat = await fs.stat(batchPath);
      assert.equal(stat.mode & 0o777, 0o600);
    }

    const exclude = await fs.readFile(path.join(repo, '.git', 'info', 'exclude'), 'utf8');
    assert.match(exclude, /^\/packages\/app\/\.codexpro-batches\/$/m);
    assert.doesNotMatch(git(repo, ['status', '--short']), /codexpro-batches/);
  } finally {
    await f.close();
    await fs.rm(temp, { recursive: true, force: true });
  }
});

test('non-Git workspaces retain batch files without claiming Git exclusion', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-nongit-'));
  await fs.writeFile(path.join(root, 'present.txt'), 'present\n', 'utf8');
  const f = await connect(root);
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.persisted, true);
    assert.equal(result.structuredContent.git_excluded, false);
    assert.equal((await generatedFiles(root)).length, 1);
  } finally {
    await f.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('custom JSON inside the batch directory remains an ordinary file outside automatic retention', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-custom-'));
  await fs.mkdir(path.join(root, '.codexpro-batches'));
  await fs.writeFile(path.join(root, 'present.txt'), 'present\n', 'utf8');
  const customPath = '.codexpro-batches/custom.json';
  await fs.writeFile(path.join(root, customPath), `${JSON.stringify({
    version: 1,
    mode: 'serial',
    continue_on_error: false,
    operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
  }, null, 2)}\n`, 'utf8');

  const f = await connect(root);
  try {
    const custom = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: customPath }
    });
    assert.notEqual(custom.isError, true);
    assert.equal(custom.structuredContent.persisted, true);
    assert.equal(custom.structuredContent.auto_stored, false);
    assert.equal(custom.structuredContent.batch_tag, undefined);
    assert.equal(custom.structuredContent.pruned_batch_count, 0);

    for (let index = 0; index < 21; index += 1) {
      const result = await f.client.callTool({
        name: 'batch',
        arguments: {
          workspace_id: f.workspaceId,
          persist: true,
          operations: [{ id: `read_${index}`, tool: 'read', args: { path: 'present.txt' } }]
        }
      });
      assert.notEqual(result.isError, true);
    }
    assert.equal((await generatedFiles(root)).length, 20);
    assert.equal(await fs.readFile(path.join(root, customPath), 'utf8').then(() => true), true);
  } finally {
    await f.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('read-only mode executes an existing stored definition without touching or duplicating it', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-readonly-file-'));
  await fs.mkdir(path.join(root, '.codexpro-batches'));
  await fs.writeFile(path.join(root, 'present.txt'), 'present\n', 'utf8');
  const batchPath = '.codexpro-batches/A1B2.json';
  await fs.writeFile(path.join(root, batchPath), `${JSON.stringify({
    version: 1,
    mode: 'serial',
    continue_on_error: false,
    operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
  }, null, 2)}\n`, 'utf8');
  const before = await fs.stat(path.join(root, batchPath));

  const f = await connect(root, { write: 'off' });
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: batchPath }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.persisted, true);
    assert.equal(result.structuredContent.auto_stored, true);
    assert.equal(result.structuredContent.git_excluded, false);
    assert.equal(result.structuredContent.pruned_batch_count, 0);
    assert.deepEqual(await generatedFiles(root), ['A1B2.json']);
    const after = await fs.stat(path.join(root, batchPath));
    assert.equal(after.mtimeMs, before.mtimeMs);
  } finally {
    await f.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('invalid resume anchors fail before a stored mutation can execute', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-resume-errors-'));
  const batchPath = 'danger.json';
  await fs.writeFile(path.join(root, batchPath), `${JSON.stringify({
    version: 1,
    mode: 'serial',
    continue_on_error: false,
    operations: [
      { id: 'change', tool: 'write', args: { path: 'must-not-exist.txt', content: 'blocked\n' } },
      { id: 'inspect', tool: 'read', args: { path: 'must-not-exist.txt' } }
    ]
  }, null, 2)}\n`, 'utf8');
  const f = await connect(root);
  try {
    const missingId = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: batchPath, from: 'missing' }
    });
    assert.equal(missingId.isError, true);
    assert.match(missingId.structuredContent.error, /operation id not found/i);
    await assert.rejects(fs.stat(path.join(root, 'must-not-exist.txt')), /ENOENT/);

    const badIndex = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: batchPath, from_index: 2 }
    });
    assert.equal(badIndex.isError, true);
    assert.match(badIndex.structuredContent.error, /outside this 2-operation batch/i);
    await assert.rejects(fs.stat(path.join(root, 'must-not-exist.txt')), /ENOENT/);
  } finally {
    await f.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('invalid auto-stored JSON is rejected before Git exclusion or retention maintenance', async () => {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-invalid-auto-'));
  const repo = path.join(temp, 'repo');
  await fs.mkdir(path.join(repo, '.codexpro-batches'), { recursive: true });
  await fs.writeFile(path.join(repo, 'present.txt'), 'present\n', 'utf8');
  git(repo, ['init']);
  git(repo, ['config', 'user.email', 'batch@example.test']);
  git(repo, ['config', 'user.name', 'Batch Test']);
  const batchPath = '.codexpro-batches/A1B2.json';
  await fs.writeFile(path.join(repo, batchPath), `${JSON.stringify({
    version: 1,
    mode: 'serial',
    continue_on_error: false,
    unexpected_field: true,
    operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
  }, null, 2)}\n`, 'utf8');
  const excludePath = path.join(repo, '.git', 'info', 'exclude');
  const excludeBefore = await fs.readFile(excludePath, 'utf8');

  const f = await connect(repo);
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: batchPath }
    });
    assert.equal(result.isError, true);
    assert.match(result.structuredContent.error, /unrecognized key|unexpected_field/i);
    assert.equal(await fs.readFile(excludePath, 'utf8'), excludeBefore);
    assert.doesNotMatch(await fs.readFile(excludePath, 'utf8'), /codexpro-batches/);
    assert.equal(await fs.readFile(path.join(repo, batchPath), 'utf8').then(() => true), true);

    await fs.writeFile(path.join(repo, batchPath), `${JSON.stringify({
      version: 1,
      mode: 'serial',
      continue_on_error: false,
      operations: [{ id: 'invalid_read', tool: 'read', args: {} }]
    }, null, 2)}\n`, 'utf8');
    const invalidChild = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: batchPath }
    });
    assert.equal(invalidChild.isError, true);
    assert.match(invalidChild.structuredContent.error, /invalid arguments for read|path/i);
    assert.equal(await fs.readFile(excludePath, 'utf8'), excludeBefore);
    assert.doesNotMatch(await fs.readFile(excludePath, 'utf8'), /codexpro-batches/);
  } finally {
    await f.close();
    await fs.rm(temp, { recursive: true, force: true });
  }
});

test('large retention cleanup bounds the returned pruned path list', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-prune-bounds-'));
  const store = path.join(root, '.codexpro-batches');
  await fs.mkdir(store);
  await fs.writeFile(path.join(root, 'present.txt'), 'present\n', 'utf8');
  for (let index = 0; index < 45; index += 1) {
    const name = `${index.toString(16).padStart(4, '0').toUpperCase()}.json`;
    const filePath = path.join(store, name);
    await fs.writeFile(filePath, '{}\n', 'utf8');
    const timestamp = new Date(Date.now() - (60_000 + index * 1000));
    await fs.utimes(filePath, timestamp, timestamp);
  }

  const f = await connect(root);
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.pruned_batch_count, 26);
    assert.equal(result.structuredContent.pruned_batch_paths.length, 20);
    assert.equal(result.structuredContent.pruned_batch_paths_truncated, true);
    assert.equal((await generatedFiles(root)).length, 20);
  } finally {
    await f.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});
