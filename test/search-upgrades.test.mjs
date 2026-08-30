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

async function fixture({ gitRepo = false, maxOutputBytes, audit = false } = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-search-upgrades-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  if (gitRepo) {
    git(repo, ['init']);
    git(repo, ['config', 'user.email', 'search@example.test']);
    git(repo, ['config', 'user.name', 'Search Test']);
  }
  const auditPath = path.join(root, 'audit', 'actions.jsonl');
  const configArgs = [
    '--root', repo,
    '--tool-mode', 'full',
    '--bash', 'off',
    '--write', 'workspace'
  ];
  if (audit) configArgs.push('--audit', 'metadata', '--audit-log', auditPath);
  const config = loadConfig(configArgs);
  if (maxOutputBytes) config.maxOutputBytes = maxOutputBytes;
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-search-upgrades-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  assert.notEqual(opened.isError, true);
  return {
    root,
    repo,
    auditPath,
    client,
    server,
    workspaceId: opened.structuredContent.workspace_id,
    close: async () => {
      await client.close();
      await server.close();
      await fs.rm(root, { recursive: true, force: true });
    }
  };
}

async function search(f, args) {
  return f.client.callTool({
    name: 'search',
    arguments: { workspace_id: f.workspaceId, ...args }
  });
}

test('search returns grouped context, stable cursor pages, and edit provenance usable without a separate read', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'a.ts'), [
      'const header = 1;',
      'const firstNeedle = "needle-one";',
      'const between = 2;',
      'const secondNeedle = "needle-two";',
      'const footer = 3;',
      ''
    ].join('\n'));
    await fs.writeFile(path.join(f.repo, 'b.ts'), [
      'const before = true;',
      'const thirdNeedle = "needle-three";',
      'const after = true;',
      ''
    ].join('\n'));

    const first = await search(f, {
      query: 'needle-',
      path: '.',
      glob: '**/*.ts',
      max_results: 2,
      context_before: 1,
      context_after: 1,
      group_by_file: true
    });
    assert.notEqual(first.isError, true);
    assert.equal(first.structuredContent.matches.length, 2);
    assert.equal(first.structuredContent.contexts.length, 1, 'overlapping ranges in a.ts should merge');
    assert.equal(first.structuredContent.has_more, true);
    assert.match(first.structuredContent.next_cursor, /^[A-Za-z0-9_-]+$/);
    assert.match(first.structuredContent.contexts[0].edit_tag, /^[0-9A-F]{4}$/);
    assert.deepEqual(first.structuredContent.contexts[0].match_lines, [2, 4]);
    assert.match(first.content[0].text, /1 \| const header/);
    assert.match(first.content[0].text, /5 \| const footer/);

    const tag = first.structuredContent.contexts[0].edit_tag;
    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'a.ts',
        edit_tag: tag,
        edits: [{ op: 'replace', start_line: 2, content: 'const firstNeedle = "needle-edited";' }]
      }
    });
    assert.notEqual(edited.isError, true);
    assert.match(await fs.readFile(path.join(f.repo, 'a.ts'), 'utf8'), /needle-edited/);

    const second = await search(f, {
      query: 'needle-',
      path: '.',
      glob: '**/*.ts',
      max_results: 2,
      context_before: 1,
      context_after: 1,
      group_by_file: true,
      cursor: first.structuredContent.next_cursor
    });
    assert.notEqual(second.isError, true);
    assert.equal(second.structuredContent.matches.length, 1);
    assert.equal(second.structuredContent.matches[0].path, 'b.ts');
    assert.equal(second.structuredContent.has_more, false);

    const mismatched = await search(f, {
      query: 'different-query',
      path: '.',
      glob: '**/*.ts',
      max_results: 2,
      context_before: 1,
      context_after: 1,
      group_by_file: true,
      cursor: first.structuredContent.next_cursor
    });
    assert.equal(mismatched.isError, true);
    assert.equal(mismatched.structuredContent.error_code, 'search_cursor_mismatch');
    assert.equal(mismatched.structuredContent.recovery.tool, 'search');
  } finally {
    await f.close();
  }
});

test('structured configuration search queries JSONC, YAML, and TOML with source locations and edit tags', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo, 'package.json'), `{
  // JSONC comments and trailing commas are accepted.
  "scripts": {
    "test": "node --test",
  },
}
`);
    await fs.mkdir(path.join(f.repo, '.github', 'workflows'), { recursive: true });
    await fs.writeFile(path.join(f.repo, '.github', 'workflows', 'ci.yml'), `jobs:
  build:
    steps:
      - uses: actions/checkout@v4
      - name: Test
        uses: local/test-action@v1
`);
    await fs.writeFile(path.join(f.repo, 'services.toml'), `[[servers]]
name = "primary"
host = "one.example"

[[servers]]
name = "secondary"
host = "two.example"
`);

    const json = await search(f, {
      kind: 'config',
      query: 'scripts.test',
      path: 'package.json',
      context_before: 1,
      context_after: 1
    });
    assert.notEqual(json.isError, true);
    assert.equal(json.structuredContent.used, 'config');
    assert.equal(json.structuredContent.matches[0].address, '$.scripts.test');
    assert.equal(json.structuredContent.matches[0].line, 4);
    assert.match(json.structuredContent.matches[0].edit_tag, /^[0-9A-F]{4}$/);

    const yaml = await search(f, {
      kind: 'config',
      query: 'jobs.*.steps[*].uses',
      path: '.github/workflows/ci.yml',
      include_hidden: true,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(yaml.isError, true);
    assert.deepEqual(yaml.structuredContent.matches.map((match) => match.address), [
      '$.jobs.build.steps[0].uses',
      '$.jobs.build.steps[1].uses'
    ]);
    assert.deepEqual(yaml.structuredContent.matches.map((match) => match.line), [4, 6]);

    const toml = await search(f, {
      kind: 'config',
      query: 'servers[*].host',
      path: 'services.toml',
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(toml.isError, true);
    assert.deepEqual(toml.structuredContent.matches.map((match) => match.address), [
      '$.servers[0].host',
      '$.servers[1].host'
    ]);
    assert.deepEqual(toml.structuredContent.matches.map((match) => match.line), [3, 7]);
  } finally {
    await f.close();
  }
});

test('diff-aware search covers changed files, added lines, removed lines, staged state, and commit comparisons', async () => {
  const f = await fixture({ gitRepo: true });
  try {
    await fs.writeFile(path.join(f.repo, 'tracked.txt'), 'keep\nremoved-marker\nbase\n');
    await fs.writeFile(path.join(f.repo, 'stable.txt'), 'stable-marker\n');
    git(f.repo, ['add', '.']);
    git(f.repo, ['commit', '-m', 'base']);

    await fs.writeFile(path.join(f.repo, 'tracked.txt'), 'keep\nadded-marker\nbase\n');
    await fs.writeFile(path.join(f.repo, 'untracked.txt'), 'untracked-added-marker\n');

    const changed = await search(f, {
      query: 'marker',
      scope: 'changed_files',
      diff_target: 'worktree',
      include_untracked: true,
      max_results: 20,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(changed.isError, true);
    assert.deepEqual(new Set(changed.structuredContent.matches.map((match) => match.path)), new Set(['tracked.txt', 'untracked.txt']));
    assert.equal(changed.structuredContent.matches.some((match) => match.path === 'stable.txt'), false);

    const added = await search(f, {
      query: 'added-marker',
      scope: 'diff_added',
      diff_target: 'worktree',
      include_untracked: true,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(added.isError, true);
    assert.deepEqual(new Set(added.structuredContent.matches.map((match) => match.path)), new Set(['tracked.txt', 'untracked.txt']));
    assert.equal(added.structuredContent.matches.every((match) => match.source === 'diff_added' && match.editable), true);
    assert.equal(added.structuredContent.matches.every((match) => /^[0-9A-F]{4}$/.test(match.edit_tag)), true);

    const removed = await search(f, {
      query: 'removed-marker',
      scope: 'diff_removed',
      diff_target: 'worktree',
      context_before: 2,
      context_after: 2
    });
    assert.notEqual(removed.isError, true);
    assert.equal(removed.structuredContent.matches.length, 1);
    assert.equal(removed.structuredContent.matches[0].path, 'tracked.txt');
    assert.equal(removed.structuredContent.matches[0].source, 'diff_removed');
    assert.equal(removed.structuredContent.matches[0].editable, false);
    assert.equal(removed.structuredContent.matches[0].edit_tag, undefined);
    assert.match(removed.content[0].text, /read-only context/);

    git(f.repo, ['add', 'tracked.txt']);
    const staged = await search(f, {
      query: 'added-marker',
      scope: 'diff_added',
      diff_target: 'staged',
      include_untracked: false,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(staged.isError, true);
    assert.deepEqual(staged.structuredContent.matches.map((match) => match.path), ['tracked.txt']);

    git(f.repo, ['commit', '-m', 'second']);
    const committed = await search(f, {
      query: 'added-marker',
      scope: 'diff_added',
      diff_target: 'head',
      base_ref: 'HEAD~1',
      include_untracked: false,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(committed.isError, true);
    assert.deepEqual(committed.structuredContent.matches.map((match) => match.path), ['tracked.txt']);
  } finally {
    await f.close();
  }
});



test('search audit metadata stays useful without retaining query, cursor, context, or edit tags', async () => {
  const f = await fixture({ audit: true });
  try {
    const marker = 'PRIVATE_SEARCH_CONTEXT_MARKER_7f4e1a';
    await fs.writeFile(path.join(f.repo, 'first.ts'), `const first = '${marker}';\n`, 'utf8');
    await fs.writeFile(path.join(f.repo, 'second.ts'), `const second = '${marker}';\n`, 'utf8');

    const first = await search(f, {
      query: marker,
      kind: 'text',
      scope: 'workspace',
      context_before: 1,
      context_after: 1,
      group_by_file: true,
      max_results: 1
    });
    assert.notEqual(first.isError, true);
    assert.equal(first.structuredContent.has_more, true);
    assert.match(first.structuredContent.next_cursor, /^[A-Za-z0-9_-]+$/);
    assert.match(first.structuredContent.matches[0].edit_tag, /^[0-9A-F]{4}$/);

    const second = await search(f, {
      query: marker,
      kind: 'text',
      scope: 'workspace',
      context_before: 1,
      context_after: 1,
      group_by_file: true,
      max_results: 1,
      cursor: first.structuredContent.next_cursor
    });
    assert.notEqual(second.isError, true);

    const raw = await fs.readFile(f.auditPath, 'utf8');
    assert.equal(raw.includes(marker), false, 'journal retained search query or returned context');
    assert.equal(raw.includes(first.structuredContent.next_cursor), false, 'journal retained continuation cursor');
    assert.doesNotMatch(raw, /"contexts":|"edit_tag":|"next_cursor":/);

    const stored = raw.trim().split('\n').map((line) => JSON.parse(line));
    const searches = stored.filter((action) => action.tool_name === 'search');
    assert.equal(searches.length, 2);
    assert.deepEqual(searches.map((action) => action.request_metadata.cursor_supplied), [false, true]);
    assert.equal(searches[0].request_metadata.search_kind, 'text');
    assert.equal(searches[0].request_metadata.search_scope, 'workspace');
    assert.equal(searches[0].request_metadata.context_before, 1);
    assert.equal(searches[0].request_metadata.context_after, 1);
    assert.equal(searches[0].request_metadata.group_by_file, true);
    assert.match(searches[0].request_metadata.query_digest, /^[0-9a-f]{64}$/);
    assert.equal(searches[0].request_metadata.query_bytes, Buffer.byteLength(marker));
    assert.equal(searches[0].result_metadata.matches_count, 1);
    assert.equal(searches[0].result_metadata.contexts_count, 1);
    assert.equal(searches[0].result_metadata.editable_matches_count, 1);
    assert.equal(searches[0].result_metadata.has_more, true);
    assert.equal(searches[0].result_metadata.search_kind, 'text');
    assert.equal(searches[0].result_metadata.search_scope, 'workspace');
    assert.match(searches[0].result_metadata.query_fingerprint, /^[0-9a-f]{64}$/);
  } finally {
    await f.close();
  }
});