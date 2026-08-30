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

async function fixture(options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-persistence-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  if (options.git !== false) {
    git(repo, ['init']);
    git(repo, ['config', 'user.email', 'batch@example.test']);
    git(repo, ['config', 'user.name', 'Batch Test']);
  }
  await fs.writeFile(path.join(repo, 'present.txt'), 'present\n', 'utf8');

  const config = loadConfig([
    '--root', repo,
    '--tool-mode', 'full',
    '--bash', options.bash ?? 'off',
    '--write', options.write ?? 'workspace'
  ]);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-batch-persistence-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  assert.notEqual(opened.isError, true);

  return {
    root,
    repo,
    config,
    server,
    client,
    workspaceId: opened.structuredContent.workspace_id,
    close: async () => {
      await client.close();
      await server.close();
      await fs.rm(root, { recursive: true, force: true });
    }
  };
}

function numberedLine(text, needle) {
  const line = text.split('\n').find((candidate) => candidate.includes(needle));
  assert.ok(line, `expected numbered batch line containing ${needle}`);
  const match = line.match(/^\s*(\d+)\s*\|/);
  assert.ok(match, `expected numbered line prefix: ${line}`);
  return Number(match[1]);
}

async function storedFiles(f) {
  const directory = path.join(f.repo, '.codexpro-batches');
  try {
    return (await fs.readdir(directory)).filter((name) => /^[0-9A-F]{4}\.json$/i.test(name)).sort();
  } catch (error) {
    if (error?.code === 'ENOENT') return [];
    throw error;
  }
}

test('inline batch is persisted, edited with normal read/edit, and resumed by operation id', async () => {
  const f = await fixture();
  try {
    const first = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [
          { id: 'before', tool: 'read', args: { path: 'present.txt' } },
          { id: 'broken', tool: 'read', args: { path: 'missing.txt' } },
          { id: 'after', tool: 'read', args: { path: 'present.txt' } }
        ]
      }
    });
    assert.equal(first.isError, true);
    assert.equal(first.structuredContent.persisted, true);
    assert.equal(first.structuredContent.auto_stored, true);
    assert.equal(first.structuredContent.batch_source, 'inline');
    assert.match(first.structuredContent.batch_tag, /^[0-9A-F]{4}$/);
    assert.match(first.structuredContent.batch_path, /^\.codexpro-batches\/[0-9A-F]{4}\.json$/);
    assert.equal(first.structuredContent.failed_operation_id, 'broken');
    assert.equal(first.structuredContent.failed_index, 1);
    assert.equal(first.structuredContent.resumable_from, 'broken');
    assert.equal(first.structuredContent.start_index, 0);
    assert.equal(first.structuredContent.total_operation_count, 3);
    assert.deepEqual(first.structuredContent.results.map((result) => result.index), [0, 1, 2]);

    const definitionText = await fs.readFile(path.join(f.repo, first.structuredContent.batch_path), 'utf8');
    assert.deepEqual(JSON.parse(definitionText), {
      version: 1,
      mode: 'serial',
      continue_on_error: false,
      operations: [
        { id: 'before', tool: 'read', args: { path: 'present.txt' } },
        { id: 'broken', tool: 'read', args: { path: 'missing.txt' } },
        { id: 'after', tool: 'read', args: { path: 'present.txt' } }
      ]
    });

    const exclude = await fs.readFile(path.join(f.repo, '.git', 'info', 'exclude'), 'utf8');
    assert.match(exclude, /^\/\.codexpro-batches\/$/m);
    assert.doesNotMatch(git(f.repo, ['status', '--short']), /codexpro-batches/);

    const readDefinition = await f.client.callTool({
      name: 'read',
      arguments: { workspace_id: f.workspaceId, path: first.structuredContent.batch_path }
    });
    assert.notEqual(readDefinition.isError, true);
    const targetLine = numberedLine(readDefinition.structuredContent.text, '"path": "missing.txt"');
    const amended = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: first.structuredContent.batch_path,
        edit_tag: readDefinition.structuredContent.edit_tag,
        edits: [{ op: 'replace', start_line: targetLine, content: '        "path": "present.txt"' }]
      }
    });
    assert.notEqual(amended.isError, true);

    const beforeCount = (await storedFiles(f)).length;
    const resumed = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: first.structuredContent.batch_path,
        from: 'broken'
      }
    });
    assert.notEqual(resumed.isError, true);
    assert.equal(resumed.structuredContent.batch_source, 'file');
    assert.equal(resumed.structuredContent.batch_path, first.structuredContent.batch_path);
    assert.equal(resumed.structuredContent.start_index, 1);
    assert.equal(resumed.structuredContent.start_operation_id, 'broken');
    assert.equal(resumed.structuredContent.operation_count, 2);
    assert.equal(resumed.structuredContent.total_operation_count, 3);
    assert.deepEqual(resumed.structuredContent.results.map((result) => result.index), [1, 2]);
    assert.equal((await storedFiles(f)).length, beforeCount);

    const finalOnly = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: first.structuredContent.batch_path,
        from_index: 2
      }
    });
    assert.notEqual(finalOnly.isError, true);
    assert.equal(finalOnly.structuredContent.start_operation_id, 'after');
    assert.deepEqual(finalOnly.structuredContent.results.map((result) => result.index), [2]);
  } finally {
    await f.close();
  }
});

test('repairing workspace state separately then resuming does not replay the successful prefix', async () => {
  const f = await fixture();
  try {
    const initial = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [
          { id: 'change', tool: 'write', args: { path: 'generated.txt', content: 'initial\n' } },
          { id: 'verify', tool: 'read', args: { path: 'missing.txt' } },
          { id: 'inspect', tool: 'read', args: { path: 'generated.txt' } }
        ]
      }
    });
    assert.equal(initial.isError, true);
    assert.equal(await fs.readFile(path.join(f.repo, 'generated.txt'), 'utf8'), 'initial\n');

    await fs.writeFile(path.join(f.repo, 'generated.txt'), 'repaired\n', 'utf8');
    await fs.writeFile(path.join(f.repo, 'missing.txt'), 'now present\n', 'utf8');

    const resumed = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: initial.structuredContent.batch_path,
        from: 'verify'
      }
    });
    assert.notEqual(resumed.isError, true);
    assert.equal(resumed.structuredContent.start_index, 1);
    assert.equal(resumed.structuredContent.mutating, false);
    assert.deepEqual(resumed.structuredContent.results.map((result) => result.id), ['verify', 'inspect']);
    assert.equal(await fs.readFile(path.join(f.repo, 'generated.txt'), 'utf8'), 'repaired\n');
  } finally {
    await f.close();
  }
});

test('batch retention keeps the twenty most recently created, amended, or run auto-stored files', async () => {
  const f = await fixture();
  try {
    const paths = [];
    for (let index = 0; index < 20; index += 1) {
      const result = await f.client.callTool({
        name: 'batch',
        arguments: {
          workspace_id: f.workspaceId,
          persist: true,
          operations: [{ id: `read_${index}`, tool: 'read', args: { path: 'present.txt' } }]
        }
      });
      assert.notEqual(result.isError, true);
      paths.push(result.structuredContent.batch_path);
    }

    const oldBaseMs = Date.now() - 120_000;
    for (let index = 0; index < paths.length; index += 1) {
      const timestamp = new Date(oldBaseMs + index * 1000);
      await fs.utimes(path.join(f.repo, paths[index]), timestamp, timestamp);
    }

    const oldestRead = await f.client.callTool({
      name: 'read',
      arguments: { workspace_id: f.workspaceId, path: paths[0] }
    });
    assert.notEqual(oldestRead.isError, true);
    const idLine = numberedLine(oldestRead.structuredContent.text, '"id": "read_0"');
    const amended = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: paths[0],
        edit_tag: oldestRead.structuredContent.edit_tag,
        edits: [{ op: 'replace', start_line: idLine, content: '      "id": "read_0_amended",' }]
      }
    });
    assert.notEqual(amended.isError, true);

    const newest = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [{ id: 'read_20', tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(newest.isError, true);

    const retained = await storedFiles(f);
    assert.equal(retained.length, 20);
    assert.equal(await fs.stat(path.join(f.repo, paths[0])).then(() => true), true);
    await assert.rejects(fs.stat(path.join(f.repo, paths[1])), /ENOENT/);
    assert.equal(await fs.stat(path.join(f.repo, newest.structuredContent.batch_path)).then(() => true), true);

    const rerunTarget = paths[2];
    const veryOld = new Date(oldBaseMs - 10_000);
    await fs.utimes(path.join(f.repo, rerunTarget), veryOld, veryOld);
    const rerun = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: rerunTarget
      }
    });
    assert.notEqual(rerun.isError, true);

    const newestAgain = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        persist: true,
        operations: [{ id: 'read_21', tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(newestAgain.isError, true);
    const retainedAgain = await storedFiles(f);
    assert.equal(retainedAgain.length, 20);
    assert.equal(await fs.stat(path.join(f.repo, rerunTarget)).then(() => true), true);
    await assert.rejects(fs.stat(path.join(f.repo, paths[3])), /ENOENT/);
    assert.equal(await fs.stat(path.join(f.repo, newestAgain.structuredContent.batch_path)).then(() => true), true);
  } finally {
    await f.close();
  }
});

test('tiny read-only batches stay one-shot by default and return efficiency guidance', async () => {
  const f = await fixture();
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'parallel',
        operations: [
          { id: 'first', tool: 'read', args: { path: 'present.txt' } },
          { id: 'second', tool: 'read', args: { path: 'present.txt' } }
        ]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.persisted, false);
    assert.equal(result.structuredContent.batch_path, undefined);
    assert.equal(result.structuredContent.persistence_default, false);
    assert.equal(result.structuredContent.persistence_requested, false);
    assert.match(result.structuredContent.efficiency_hint, /direct tool calls|three or more/i);
    assert.match(result.content[0].text, /Efficiency:/);
    assert.deepEqual(await storedFiles(f), []);
  } finally {
    await f.close();
  }
});

test('read-only mode runs inline batches one-shot without writing retained definitions', async () => {
  const f = await fixture({ write: 'off' });
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.persisted, false);
    assert.equal(result.structuredContent.batch_path, undefined);
    assert.deepEqual(await storedFiles(f), []);
  } finally {
    await f.close();
  }
});

test('stored definition errors and ambiguous invocation forms fail before child side effects', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'invalid-batch.json'), JSON.stringify({
      version: 1,
      mode: 'serial',
      continue_on_error: false,
      operations: [
        { id: 'would_write', tool: 'write', args: { path: 'must-not-exist.txt', content: 'no\n' } },
        { id: 'invalid', tool: 'read', args: {} }
      ]
    }), 'utf8');

    const invalid = await f.client.callTool({
      name: 'batch',
      arguments: { workspace_id: f.workspaceId, path: 'invalid-batch.json' }
    });
    assert.equal(invalid.isError, true);
    await assert.rejects(fs.stat(path.join(f.repo, 'must-not-exist.txt')), /ENOENT/);

    const ambiguous = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'invalid-batch.json',
        operations: [{ tool: 'read', args: { path: 'present.txt' } }]
      }
    });
    assert.equal(ambiguous.isError, true);
    assert.match(ambiguous.structuredContent.error, /exactly one of operations or path/i);

    const override = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'invalid-batch.json',
        mode: 'parallel'
      }
    });
    assert.equal(override.isError, true);
    assert.match(override.structuredContent.error, /come from the JSON file/i);
  } finally {
    await f.close();
  }
});
