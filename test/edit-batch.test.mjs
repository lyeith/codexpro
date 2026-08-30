import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { EditSnapshotStore, editTextFileByLines, readTextFile } from '../dist/fsOps.js';
import { PathGuard } from '../dist/guard.js';
import { createCodexProServer } from '../dist/server.js';
import { runWithToolContext } from '../dist/toolContext.js';

function sha256(text) {
  return createHash('sha256').update(text).digest('hex');
}

function editTag(text) {
  return sha256(text).slice(0, 4).toUpperCase();
}

async function fixture(options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-edit-batch-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  const args = ['--root', repo, '--tool-mode', 'full', '--bash', options.bash ?? 'off'];
  if (options.audit) {
    args.push('--audit', 'metadata', '--audit-log', path.join(root, 'audit', 'actions.jsonl'));
  }
  const config = loadConfig(args);
  if (options.maxOutputBytes) config.maxOutputBytes = options.maxOutputBytes;
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-edit-batch-test', version: '0.0.0' });
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

async function readTag(f, filePath, options = {}) {
  const result = await f.client.callTool({
    name: 'read',
    arguments: { workspace_id: f.workspaceId, path: filePath, ...options }
  });
  assert.notEqual(result.isError, true);
  assert.match(result.structuredContent.edit_tag, /^[0-9A-F]{4}$/);
  assert.equal(Object.hasOwn(result.structuredContent, 'editTag'), false);
  return result.structuredContent.edit_tag;
}

test('four-hex tagged edit applies non-adjacent operations against original line coordinates', async () => {
  const f = await fixture();
  try {
    const before = 'one\ntwo\nthree\nfour\nfive\n';
    await fs.writeFile(path.join(f.repo, 'sample.txt'), before, 'utf8');
    const tag = await readTag(f, 'sample.txt');
    assert.equal(tag, editTag(before));

    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'sample.txt',
        edit_tag: tag,
        edits: [
          { op: 'replace', start_line: 2, content: 'TWO\nTWO-B' },
          { op: 'delete', start_line: 3 },
          { op: 'insert_after', line: 4, content: 'after-four' },
          { op: 'replace', start_line: 5, content: 'FIVE' }
        ]
      }
    });

    const after = 'one\nTWO\nTWO-B\nfour\nafter-four\nFIVE\n';
    assert.notEqual(edited.isError, true);
    assert.equal(edited.structuredContent.mode, 'tagged_lines');
    assert.equal(edited.structuredContent.edits_applied, 4);
    assert.equal(edited.structuredContent.additions, 4);
    assert.equal(edited.structuredContent.deletions, 3);
    assert.equal(edited.structuredContent.base_edit_tag, tag);
    assert.equal(edited.structuredContent.edit_tag, editTag(after));
    assert.equal(await fs.readFile(path.join(f.repo, 'sample.txt'), 'utf8'), after);

    const stale = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'sample.txt',
        edit_tag: tag,
        edits: [{ op: 'replace', start_line: 1, content: 'stale' }]
      }
    });
    assert.equal(stale.isError, true);
    assert.match(stale.structuredContent.error, /changed since edit tag/i);
    assert.equal(stale.structuredContent.error_code, 'edit_tag_stale');
    assert.equal(stale.structuredContent.retry_unchanged, false);
    assert.equal(stale.structuredContent.recovery.tool, 'read');
    assert.deepEqual(stale.structuredContent.recovery.args, { path: 'sample.txt' });
    assert.equal(await fs.readFile(path.join(f.repo, 'sample.txt'), 'utf8'), after);
  } finally {
    await f.close();
  }
});


test('tagged edit stats count changed lines rather than the span between distant hunks', async () => {
  const f = await fixture();
  try {
    const lines = Array.from({ length: 400 }, (_, index) => `line-${index + 1}`);
    const before = `${lines.join('\n')}\n`;
    await fs.writeFile(path.join(f.repo, 'distant.txt'), before, 'utf8');
    const tag = await readTag(f, 'distant.txt');

    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'distant.txt',
        edit_tag: tag,
        edits: [
          { op: 'replace', start_line: 10, content: 'line-ten' },
          { op: 'insert_after', line: 200, content: 'inserted-after-200' },
          { op: 'delete', start_line: 390 }
        ]
      }
    });

    assert.notEqual(edited.isError, true);
    assert.equal(edited.structuredContent.edits_applied, 3);
    assert.equal(edited.structuredContent.additions, 2);
    assert.equal(edited.structuredContent.deletions, 2);
    assert.match(edited.content[0].text, /Diff stats: \+2 -2/);
  } finally {
    await f.close();
  }
});

test('four-hex tags remain safe when two different file bodies collide', async () => {
  const seen = new Map();
  let first;
  let second;
  let collisionTag;
  for (let index = 0; index < 20_000; index += 1) {
    const candidate = `collision-${index}\n`;
    const tag = editTag(candidate);
    const previous = seen.get(tag);
    if (previous && previous !== candidate) {
      first = previous;
      second = candidate;
      collisionTag = tag;
      break;
    }
    seen.set(tag, candidate);
  }
  assert.ok(first && second && collisionTag, 'expected to find a 16-bit edit-tag collision');

  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'collision.txt'), first, 'utf8');
    assert.equal(await readTag(f, 'collision.txt'), collisionTag);
    assert.equal(editTag(second), collisionTag);

    await fs.writeFile(path.join(f.repo, 'collision.txt'), second, 'utf8');
    assert.equal(await readTag(f, 'collision.txt'), collisionTag);
    const latest = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'collision.txt',
        edit_tag: collisionTag,
        edits: [{ op: 'replace', start_line: 1, content: 'latest-collision-body' }]
      }
    });
    assert.notEqual(latest.isError, true);

    await fs.writeFile(path.join(f.repo, 'collision.txt'), first, 'utf8');
    const staleCollision = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'collision.txt',
        edit_tag: collisionTag,
        edits: [{ op: 'replace', start_line: 1, content: 'must-not-apply' }]
      }
    });
    assert.equal(staleCollision.isError, true);
    assert.match(staleCollision.structuredContent.error, /changed since edit tag/i);
    assert.equal(await fs.readFile(path.join(f.repo, 'collision.txt'), 'utf8'), first);
  } finally {
    await f.close();
  }
});

test('edit tags survive separate MCP server instances for the same connector principal', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-edit-tag-session-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  await fs.writeFile(path.join(repo, 'session.txt'), 'before\n', 'utf8');
  const config = loadConfig(['--root', repo, '--tool-mode', 'full', '--bash', 'off']);

  async function connectSession(name) {
    const server = createCodexProServer(config);
    const client = new Client({ name, version: '0.0.0' });
    const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
    await server.connect(serverTransport);
    await client.connect(clientTransport);
    const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
    return { server, client, workspaceId: opened.structuredContent.workspace_id };
  }

  const first = await connectSession('codexpro-edit-tag-session-one');
  const second = await connectSession('codexpro-edit-tag-session-two');
  try {
    const read = await first.client.callTool({
      name: 'read',
      arguments: { workspace_id: first.workspaceId, path: 'session.txt' }
    });
    const tag = read.structuredContent.edit_tag;

    const editFromAnotherServer = await second.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: second.workspaceId,
        path: 'session.txt',
        edit_tag: tag,
        edits: [{ op: 'replace', start_line: 1, content: 'second-server' }]
      }
    });
    assert.notEqual(editFromAnotherServer.isError, true);
    assert.equal(await fs.readFile(path.join(repo, 'session.txt'), 'utf8'), 'second-server\n');
  } finally {
    await first.client.close();
    await first.server.close();
    await second.client.close();
    await second.server.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('process-wide edit snapshot cache remains isolated by connector principal', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-edit-tag-principal-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  await fs.writeFile(path.join(repo, 'principal.txt'), 'before\n', 'utf8');
  const config = loadConfig(['--root', repo, '--tool-mode', 'full', '--bash', 'off']);
  const guard = new PathGuard(config);
  const workspace = {
    id: 'ws_principal',
    root: repo,
    openedAt: new Date().toISOString(),
    kind: 'direct'
  };
  const snapshots = new EditSnapshotStore();
  const context = (principalId, requestId) => ({
    principalId,
    requestId,
    signal: new AbortController().signal
  });

  try {
    const read = await runWithToolContext(context('principal_a', 'read_a'), () =>
      readTextFile(config, guard, workspace, 'principal.txt', { editSnapshots: snapshots })
    );

    await assert.rejects(
      runWithToolContext(context('principal_b', 'edit_b'), () =>
        editTextFileByLines(
          config,
          guard,
          workspace,
          'principal.txt',
          [{ op: 'replace', startLine: 1, content: 'foreign' }],
          snapshots,
          read.editTag
        )
      ),
      (error) => error?.code === 'edit_tag_unknown'
    );
    assert.equal(await fs.readFile(path.join(repo, 'principal.txt'), 'utf8'), 'before\n');

    const ownEdit = await runWithToolContext(context('principal_a', 'edit_a'), () =>
      editTextFileByLines(
        config,
        guard,
        workspace,
        'principal.txt',
        [{ op: 'replace', startLine: 1, content: 'owner' }],
        snapshots,
        read.editTag
      )
    );
    assert.equal(ownEdit.baseTag, read.editTag);
    assert.equal(await fs.readFile(path.join(repo, 'principal.txt'), 'utf8'), 'owner\n');
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('tagged edit accepts only line ranges displayed by read', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'seen.txt'), 'one\ntwo\nthree\nfour\n', 'utf8');
    const tag = await readTag(f, 'seen.txt', { start_line: 1, end_line: 2 });
    const unseen = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'seen.txt',
        edit_tag: tag,
        edits: [{ op: 'replace', start_line: 3, content: 'THREE' }]
      }
    });
    assert.equal(unseen.isError, true);
    assert.match(unseen.structuredContent.error, /not displayed/i);
    assert.equal(unseen.structuredContent.error_code, 'edit_range_unseen');
    assert.equal(unseen.structuredContent.retry_unchanged, false);
    assert.equal(unseen.structuredContent.recovery.tool, 'read');
    assert.deepEqual(unseen.structuredContent.recovery.args, { path: 'seen.txt', start_line: 3, end_line: 3 });
    assert.match(unseen.content[0].text, /Do not retry the same request unchanged/i);

    assert.equal(await readTag(f, 'seen.txt', { start_line: 3, end_line: 3 }), tag);
    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'seen.txt',
        edit_tag: tag,
        edits: [{ op: 'replace', start_line: 3, content: 'THREE' }]
      }
    });
    assert.notEqual(edited.isError, true);
    assert.equal(await fs.readFile(path.join(f.repo, 'seen.txt'), 'utf8'), 'one\ntwo\nTHREE\nfour\n');
  } finally {
    await f.close();
  }
});

test('legacy exact-text edit fields are no longer part of the public contract', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'legacy.txt'), 'before\n', 'utf8');
    const result = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'legacy.txt',
        old_text: 'before',
        new_text: 'after'
      }
    });
    assert.equal(result.isError, true);
    const errorText = result.content?.find((item) => item.type === 'text')?.text ?? '';
    assert.match(errorText, /edit_tag|edits/i);
    assert.equal(await fs.readFile(path.join(f.repo, 'legacy.txt'), 'utf8'), 'before\n');
  } finally {
    await f.close();
  }
});

test('tagged edit preflights overlaps and preserves uniform CRLF plus final newline', async () => {
  const f = await fixture();
  try {
    const crlf = 'alpha\r\nbeta\r\ngamma\r\n';
    await fs.writeFile(path.join(f.repo, 'crlf.txt'), crlf, 'utf8');
    const tag = await readTag(f, 'crlf.txt');

    const overlap = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'crlf.txt',
        edit_tag: tag,
        edits: [
          { op: 'replace', start_line: 1, end_line: 2, content: 'x' },
          { op: 'delete', start_line: 2, end_line: 3 }
        ]
      }
    });
    assert.equal(overlap.isError, true);
    assert.match(overlap.structuredContent.error, /overlaps/i);
    assert.equal(await fs.readFile(path.join(f.repo, 'crlf.txt'), 'utf8'), crlf);

    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'crlf.txt',
        edit_tag: tag,
        edits: [
          { op: 'replace', start_line: 2, content: 'BETA' },
          { op: 'insert_before', line: 3, content: 'between' }
        ]
      }
    });
    assert.notEqual(edited.isError, true);
    assert.equal(await fs.readFile(path.join(f.repo, 'crlf.txt'), 'utf8'), 'alpha\r\nBETA\r\nbetween\r\ngamma\r\n');
  } finally {
    await f.close();
  }
});

test('batch supports parallel reads and a serial read-edit-read sequence', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'a.txt'), 'alpha\n', 'utf8');
    await fs.writeFile(path.join(f.repo, 'b.txt'), 'beta\n', 'utf8');

    const parallel = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'parallel',
        operations: [
          { id: 'read_a', tool: 'read', args: { path: 'a.txt' } },
          { id: 'read_b', tool: 'read', args: { path: 'b.txt' } },
          { id: 'find_alpha', tool: 'search', args: { query: 'alpha', path: '.' } }
        ]
      }
    });
    assert.notEqual(parallel.isError, true);
    assert.equal(parallel.structuredContent.succeeded_count, 3);
    assert.deepEqual(parallel.structuredContent.results.map((result) => result.id), ['read_a', 'read_b', 'find_alpha']);

    const aTag = parallel.structuredContent.results.find((result) => result.id === 'read_a').structured.edit_tag;
    const serial = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'before', tool: 'read', args: { path: 'a.txt' } },
          {
            id: 'change',
            tool: 'edit',
            args: {
              path: 'a.txt',
              edit_tag: aTag,
              edits: [{ op: 'replace', start_line: 1, content: 'ALPHA' }]
            }
          },
          { id: 'after', tool: 'read', args: { path: 'a.txt' } }
        ]
      }
    });
    assert.notEqual(serial.isError, true);
    assert.equal(serial.structuredContent.mutating, true);
    assert.deepEqual(serial.structuredContent.changed_paths, ['a.txt']);
    assert.equal(await fs.readFile(path.join(f.repo, 'a.txt'), 'utf8'), 'ALPHA\n');
    const afterResult = serial.structuredContent.results.find((result) => result.id === 'after');
    assert.equal(afterResult.structured.sha256, sha256('ALPHA\n'));
  } finally {
    await f.close();
  }
});

test('serial batch supports a tagged edit followed by verification-only Bash', async () => {
  const f = await fixture({ bash: 'full' });
  try {
    await fs.writeFile(path.join(f.repo, 'verify.txt'), 'before\n', 'utf8');
    await fs.writeFile(path.join(f.repo, 'package.json'), JSON.stringify({
      scripts: {
        test: `node -e "const fs=require('fs');process.exit(fs.readFileSync('verify.txt','utf8').startsWith('after')?0:2)"`
      }
    }), 'utf8');
    const tag = await readTag(f, 'verify.txt');

    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'serial',
        operations: [
          {
            id: 'edit_file',
            tool: 'edit',
            args: {
              path: 'verify.txt',
              edit_tag: tag,
              edits: [{ op: 'replace', start_line: 1, content: 'after' }]
            }
          },
          { id: 'verify', tool: 'bash', args: { command: 'npm test' } },
          { id: 'read_after', tool: 'read', args: { path: 'verify.txt' } }
        ]
      }
    });
    assert.notEqual(result.isError, true);
    assert.equal(result.structuredContent.succeeded_count, 3);
    assert.equal(result.structuredContent.results.find((child) => child.id === 'verify').structured.exitCode, 0);
    assert.equal(await fs.readFile(path.join(f.repo, 'verify.txt'), 'utf8'), 'after\n');

    await fs.writeFile(path.join(f.repo, 'unsafe.txt'), 'before\n', 'utf8');
    const unsafeTag = await readTag(f, 'unsafe.txt');
    const unsafe = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          {
            id: 'must_not_apply',
            tool: 'edit',
            args: {
              path: 'unsafe.txt',
              edit_tag: unsafeTag,
              edits: [{ op: 'replace', start_line: 1, content: 'changed' }]
            }
          },
          { id: 'unsafe_shell', tool: 'bash', args: { command: 'node -e "process.exit(0)"' } }
        ]
      }
    });
    assert.equal(unsafe.isError, true);
    assert.match(unsafe.structuredContent.error, /verification-only|verification allowlist/i);
    assert.equal(await fs.readFile(path.join(f.repo, 'unsafe.txt'), 'utf8'), 'before\n');
  } finally {
    await f.close();
  }
});

test('serial batch allows distinct-file mutations and rejects ambiguous mutation combinations before execution', async () => {
  const f = await fixture({ bash: 'full' });
  try {
    const distinct = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'first', tool: 'write', args: { path: 'first.txt', content: 'first\n' } },
          { id: 'second', tool: 'write', args: { path: 'second.txt', content: 'second\n' } }
        ]
      }
    });
    assert.notEqual(distinct.isError, true);
    assert.equal(distinct.structuredContent.succeeded_count, 2);
    assert.deepEqual(new Set(distinct.structuredContent.changed_paths), new Set(['first.txt', 'second.txt']));
    assert.equal(await fs.readFile(path.join(f.repo, 'first.txt'), 'utf8'), 'first\n');
    assert.equal(await fs.readFile(path.join(f.repo, 'second.txt'), 'utf8'), 'second\n');

    const duplicate = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'duplicate_first', tool: 'write', args: { path: 'duplicate.txt', content: 'first\n' } },
          { id: 'duplicate_second', tool: 'write', args: { path: './duplicate.txt', content: 'second\n' } }
        ]
      }
    });
    assert.equal(duplicate.isError, true);
    assert.match(duplicate.structuredContent.error, /both mutate duplicate\.txt|combine all changes to one file/i);
    await assert.rejects(fs.stat(path.join(f.repo, 'duplicate.txt')), /ENOENT/);

    if (process.platform !== 'win32') {
      await fs.mkdir(path.join(f.repo, 'real-parent'));
      await fs.symlink('real-parent', path.join(f.repo, 'alias-parent'), 'dir');
      const symlinkDuplicate = await f.client.callTool({
        name: 'batch',
        arguments: {
          workspace_id: f.workspaceId,
          operations: [
            { id: 'real_parent', tool: 'write', args: { path: 'real-parent/future.txt', content: 'first\n' } },
            { id: 'alias_parent', tool: 'write', args: { path: 'alias-parent/future.txt', content: 'second\n' } }
          ]
        }
      });
      assert.equal(symlinkDuplicate.isError, true);
      assert.match(symlinkDuplicate.structuredContent.error, /both mutate|combine all changes to one file/i);
      await assert.rejects(fs.stat(path.join(f.repo, 'real-parent', 'future.txt')), /ENOENT/);
    }

    const mixedPatch = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'must_not_write', tool: 'write', args: { path: 'patch-mix.txt', content: 'blocked\n' } },
          { id: 'patch', tool: 'apply_patch', args: { patch: 'not executed' } }
        ]
      }
    });
    assert.equal(mixedPatch.isError, true);
    assert.match(mixedPatch.structuredContent.error, /apply_patch.*only file-mutation child|patch may already span/i);
    await assert.rejects(fs.stat(path.join(f.repo, 'patch-mix.txt')), /ENOENT/);

    const earlyVerification = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'before_verify', tool: 'write', args: { path: 'before-verify.txt', content: 'blocked\n' } },
          { id: 'verify_too_soon', tool: 'bash', args: { command: 'npm test' } },
          { id: 'after_verify', tool: 'write', args: { path: 'after-verify.txt', content: 'blocked\n' } }
        ]
      }
    });
    assert.equal(earlyVerification.isError, true);
    assert.match(earlyVerification.structuredContent.error, /before the final file mutation/i);
    await assert.rejects(fs.stat(path.join(f.repo, 'before-verify.txt')), /ENOENT/);
    await assert.rejects(fs.stat(path.join(f.repo, 'after-verify.txt')), /ENOENT/);

    const parallelMutation = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'parallel',
        operations: [
          { tool: 'write', args: { path: 'parallel.txt', content: 'blocked\n' } }
        ]
      }
    });
    assert.equal(parallelMutation.isError, true);
    assert.match(parallelMutation.structuredContent.error, /must use mode=serial/i);
    await assert.rejects(fs.stat(path.join(f.repo, 'parallel.txt')), /ENOENT/);
  } finally {
    await f.close();
  }
});

test('batch validates every child schema before the first operation can mutate', async () => {
  const f = await fixture();
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'would_write', tool: 'write', args: { path: 'must-not-exist.txt', content: 'blocked\n' } },
          { id: 'invalid_read', tool: 'read', args: {} }
        ]
      }
    });
    assert.equal(result.isError, true);
    assert.match(result.structuredContent.error, /invalid arguments for read|path/i);
    await assert.rejects(fs.stat(path.join(f.repo, 'must-not-exist.txt')), /ENOENT/);
  } finally {
    await f.close();
  }
});

test('batch reports partial completion without rolling back a successful mutation', async () => {
  const f = await fixture();
  try {
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'apply', tool: 'write', args: { path: 'applied.txt', content: 'applied\n' } },
          { id: 'verify_missing', tool: 'read', args: { path: 'missing.txt' } }
        ]
      }
    });
    assert.equal(result.isError, true);
    assert.equal(result.structuredContent.succeeded_count, 1);
    assert.equal(result.structuredContent.failed_count, 1);
    assert.equal(result.structuredContent.skipped_count, 0);
    assert.deepEqual(result.structuredContent.changed_paths, ['applied.txt']);
    assert.equal(await fs.readFile(path.join(f.repo, 'applied.txt'), 'utf8'), 'applied\n');
    assert.equal(result.structuredContent.results.find((child) => child.id === 'apply').ok, true);
    assert.equal(result.structuredContent.results.find((child) => child.id === 'verify_missing').ok, false);
  } finally {
    await f.close();
  }
});

test('batch treats a non-zero Bash exit as a failed child and skips later operations', async () => {
  const f = await fixture({ bash: 'full' });
  try {
    await fs.writeFile(path.join(f.repo, 'after.txt'), 'after\n', 'utf8');
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          { id: 'failing_check', tool: 'bash', args: { command: 'npm test' } },
          { id: 'must_skip', tool: 'read', args: { path: 'after.txt' } }
        ]
      }
    });
    assert.equal(result.isError, true);
    assert.equal(result.structuredContent.failed_count, 1);
    assert.equal(result.structuredContent.skipped_count, 1);
    const failed = result.structuredContent.results.find((child) => child.id === 'failing_check');
    assert.equal(failed.ok, false);
    assert.match(failed.error, /exited with code/i);
    assert.notEqual(failed.structured.exitCode, 0);
    assert.equal(result.structuredContent.results.find((child) => child.id === 'must_skip').skipped, true);
  } finally {
    await f.close();
  }
});

test('read-only batch can continue after an error and reports skipped or failed children explicitly', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'present.txt'), 'present\n', 'utf8');
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        continue_on_error: true,
        operations: [
          { id: 'missing', tool: 'read', args: { path: 'missing.txt' } },
          { id: 'present', tool: 'read', args: { path: 'present.txt' } }
        ]
      }
    });
    assert.equal(result.isError, true);
    assert.equal(result.structuredContent.failed_count, 1);
    assert.equal(result.structuredContent.succeeded_count, 1);
    assert.equal(result.structuredContent.results.find((child) => child.id === 'present').ok, true);
  } finally {
    await f.close();
  }
});

test('batch bounds aggregate child text and structured payloads', async () => {
  const f = await fixture({ maxOutputBytes: 5000 });
  try {
    const body = `${'large-line-'.repeat(900)}\n`;
    for (const name of ['one.txt', 'two.txt', 'three.txt', 'four.txt']) {
      await fs.writeFile(path.join(f.repo, name), body, 'utf8');
    }
    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'parallel',
        operations: ['one.txt', 'two.txt', 'three.txt', 'four.txt'].map((file, index) => ({
          id: `read_${index + 1}`,
          tool: 'read',
          args: { path: file }
        }))
      }
    });
    assert.notEqual(result.isError, true);
    assert.ok(Buffer.byteLength(result.content[0].text, 'utf8') <= 5000);
    assert.ok(result.structuredContent.child_text_truncated_count > 0);
    assert.ok(result.structuredContent.child_structured_truncated_count > 0);
    assert.ok(Buffer.byteLength(JSON.stringify(result.structuredContent.results), 'utf8') <= 5000);
    for (const child of result.structuredContent.results) {
      assert.equal(Object.hasOwn(child, 'text'), false);
      assert.match(child.structured.sha256, /^[a-f0-9]{64}$/);
    }
  } finally {
    await f.close();
  }
});

test('metadata audit records tagged edits and dynamic batch mutation without payload text', async () => {
  const f = await fixture({ audit: true });
  try {
    const before = 'safe body\n';
    await fs.writeFile(path.join(f.repo, 'audit.txt'), before, 'utf8');
    const tag = await readTag(f, 'audit.txt');
    const batch = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          {
            id: 'edit_audit',
            tool: 'edit',
            args: {
              path: 'audit.txt',
              edit_tag: tag,
              edits: [{ op: 'replace', start_line: 1, content: 'PRIVATE_BATCH_BODY' }]
            }
          }
        ]
      }
    });
    assert.notEqual(batch.isError, true);

    const activity = await f.client.callTool({
      name: 'activity_list',
      arguments: { limit: 20 }
    });
    assert.notEqual(activity.isError, true);
    const action = activity.structuredContent.actions.find((item) => item.tool_name === 'batch');
    assert.equal(action.operation, 'batch.execute');
    assert.equal(action.operation_class, 'write');
    assert.equal(action.mutating, true);
    assert.equal(action.request_metadata.operation_count, 1);
    assert.equal(action.request_metadata.file_mutation_count, 1);
    assert.equal(action.request_metadata.verification_command_count, 0);
    assert.deepEqual(action.changed_paths, ['audit.txt']);

    const journal = await fs.readFile(f.config.auditLogPath, 'utf8');
    assert.doesNotMatch(journal, /PRIVATE_BATCH_BODY/);
  } finally {
    await f.close();
  }
});

test('batch preserves actionable retry metadata from a failed child', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'batch-stale.txt'), 'before\n', 'utf8');
    const tag = await readTag(f, 'batch-stale.txt');
    await fs.writeFile(path.join(f.repo, 'batch-stale.txt'), 'changed elsewhere\n', 'utf8');

    const result = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        operations: [
          {
            id: 'stale_edit',
            tool: 'edit',
            args: {
              path: 'batch-stale.txt',
              edit_tag: tag,
              edits: [{ op: 'replace', start_line: 1, content: 'must-not-apply' }]
            }
          },
          { id: 'after', tool: 'read', args: { path: 'batch-stale.txt' } }
        ]
      }
    });
    assert.equal(result.isError, true);
    const failed = result.structuredContent.results.find((child) => child.id === 'stale_edit');
    assert.equal(failed.ok, false);
    assert.equal(failed.structured.error_code, 'edit_tag_stale');
    assert.equal(failed.structured.retry_unchanged, false);
    assert.equal(failed.structured.recovery.tool, 'read');
    assert.deepEqual(failed.structured.recovery.args, { path: 'batch-stale.txt' });
    assert.match(failed.structured.recovery.message, /read .* again/i);
    assert.equal(result.structuredContent.results.find((child) => child.id === 'after').skipped, true);
    assert.equal(await fs.readFile(path.join(f.repo, 'batch-stale.txt'), 'utf8'), 'changed elsewhere\n');
  } finally {
    await f.close();
  }
});
