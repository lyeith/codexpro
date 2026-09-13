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
}

async function fixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-tool-recovery-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  await fs.writeFile(path.join(repo, 'one.txt'), 'current\n', 'utf8');
  git(repo, ['init']);
  git(repo, ['config', 'user.email', 'recovery@example.test']);
  git(repo, ['config', 'user.name', 'Recovery Test']);
  git(repo, ['add', 'one.txt']);
  git(repo, ['commit', '-m', 'initial']);

  const auditPath = path.join(root, 'audit', 'actions.jsonl');
  const config = loadConfig([
    '--root', repo,
    '--tool-mode', 'full',
    '--bash', 'off',
    '--write', 'workspace',
    '--audit', 'metadata',
    '--audit-log', auditPath
  ]);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-tool-recovery-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  assert.notEqual(opened.isError, true);

  return {
    config,
    root,
    repo,
    auditPath,
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

test('commit returns a full SHA and distinguishes clean, dirty and unavailable post-commit status', async () => {
  const f = await fixture();
  try {
    const commit = async (message) => f.client.callTool({ name: 'commit_changes', arguments: {
      workspace_id: f.workspaceId, message, paths: ['one.txt']
    } });
    await fs.writeFile(path.join(f.repo, 'one.txt'), 'first change\n');
    const clean = await commit('first change');
    assert.notEqual(clean.isError, true);
    const head = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: f.repo, encoding: 'utf8' }).stdout.trim();
    assert.equal(clean.structuredContent.commit, head);
    assert.match(head, /^[a-f0-9]{40}$/);
    assert.equal(clean.structuredContent.commit_short, head.slice(0, 12));
    assert.equal(clean.structuredContent.working_tree_clean, true);
    assert.equal(clean.structuredContent.status, '');
    assert.equal(clean.structuredContent.status_error, null);
    assert.ok(clean.content[0].text.includes(head));
    assert.match(clean.content[0].text, /Working tree: clean/);

    await fs.writeFile(path.join(f.repo, 'other.txt'), 'uncommitted\n');
    await fs.writeFile(path.join(f.repo, 'one.txt'), 'second change\n');
    const dirty = await commit('second change');
    assert.notEqual(dirty.isError, true);
    assert.equal(dirty.structuredContent.working_tree_clean, false);
    assert.match(dirty.structuredContent.status, /\?\? other.txt/);
    assert.match(dirty.content[0].text, /changes remain/);

    // A status failure after a successful commit must not be represented as clean.
    for (let i = 0; i < 40; i++) await fs.writeFile(path.join(f.repo, `untracked-${i}.txt`), 'x');
    f.config.maxOutputBytes = 256;
    await fs.writeFile(path.join(f.repo, 'one.txt'), 'third change\n');
    const unknown = await commit('third change');
    assert.notEqual(unknown.isError, true);
    assert.equal(unknown.structuredContent.working_tree_clean, null);
    assert.ok(unknown.structuredContent.status_error);
    assert.match(unknown.content[0].text, /status unavailable/);
  } finally {
    await f.close();
  }
});

test('apply_patch classifies stale context and points to a fresh read', async () => {
  const f = await fixture();
  try {
    const result = await f.client.callTool({
      name: 'apply_patch',
      arguments: {
        workspace_id: f.workspaceId,
        patch: [
          'diff --git a/one.txt b/one.txt',
          '--- a/one.txt',
          '+++ b/one.txt',
          '@@ -1 +1 @@',
          '-not-current',
          '+after',
          ''
        ].join('\n')
      }
    });

    assert.equal(result.isError, true);
    assert.equal(result.structuredContent.error_code, 'patch_context_stale');
    assert.equal(result.structuredContent.retry_unchanged, false);
    assert.equal(result.structuredContent.recovery.tool, 'read');
    assert.deepEqual(result.structuredContent.recovery.args, { path: 'one.txt' });
    assert.match(result.structuredContent.recovery.message, /tagged edit|regenerate/i);
    assert.equal(await fs.readFile(path.join(f.repo, 'one.txt'), 'utf8'), 'current\n');

    const activity = await f.client.callTool({
      name: 'activity_list',
      arguments: { limit: 20 }
    });
    assert.notEqual(activity.isError, true);
    const patchAction = activity.structuredContent.actions.find((action) => action.tool_name === 'apply_patch');
    assert.ok(patchAction);
    assert.equal(patchAction.status, 'failed');
    assert.equal(patchAction.error_code, 'patch_context_stale');
    assert.equal(patchAction.result_metadata.error_code, 'patch_context_stale');
    assert.equal(patchAction.result_metadata.retry_unchanged, false);

    const journal = await fs.readFile(f.auditPath, 'utf8');
    assert.doesNotMatch(journal, /not-current|\+after/);
  } finally {
    await f.close();
  }
});
