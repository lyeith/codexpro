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

const codexproCli = path.resolve('scripts/codexpro.mjs');

async function fixture({ audit = false, toolMode = 'full' } = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-ast-grep-'));
  const repo = path.join(root, 'repo');
  const auditPath = path.join(root, 'audit', 'actions.jsonl');
  await fs.mkdir(repo);
  const args = [
    '--root', repo,
    '--tool-mode', toolMode,
    '--bash', 'off',
    '--write', 'workspace'
  ];
  if (audit) args.push('--audit', 'metadata', '--audit-log', auditPath);
  const config = loadConfig(args);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-ast-grep-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
  assert.notEqual(opened.isError, true);
  return {
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

async function astGrep(f, args) {
  return f.client.callTool({
    name: 'ast_grep',
    arguments: { workspace_id: f.workspaceId, ...args }
  });
}

test('MCP ast_grep paginates structural patterns and its returned context can be edited directly', async () => {
  const f = await fixture({ toolMode: 'standard' });
  try {
    await fs.mkdir(path.join(f.repo, 'src'));
    await fs.writeFile(path.join(f.repo, 'src', 'sample.ts'), [
      'export function first() {',
      '  console.log("one");',
      '}',
      '',
      'export function second() {',
      '  console.log("two");',
      '}',
      ''
    ].join('\n'));

    const query = {
      pattern: 'console.log($ARG)',
      language: 'ts',
      path: 'src',
      globs: ['**/*.ts'],
      max_results: 1,
      context_before: 1,
      context_after: 1,
      group_by_file: true
    };
    const first = await astGrep(f, query);
    assert.notEqual(first.isError, true, first.content?.[0]?.text);
    assert.equal(first.structuredContent.provider, 'ast-grep-cli');
    assert.match(first.structuredContent.provider_version, /^\d+\.\d+\.\d+/);
    assert.equal(first.structuredContent.mode, 'pattern');
    assert.equal(first.structuredContent.matches.length, 1);
    assert.equal(first.structuredContent.contexts.length, 1);
    assert.equal(first.structuredContent.has_more, true);
    assert.match(first.structuredContent.next_cursor, /^[A-Za-z0-9_-]+$/);
    assert.equal(first.structuredContent.matches[0].path, 'src/sample.ts');
    assert.equal(first.structuredContent.matches[0].start_line, 2);
    assert.equal(first.structuredContent.matches[0].editable, true);
    assert.match(first.structuredContent.matches[0].edit_tag, /^[0-9A-F]{4}$/);
    assert.equal(first.structuredContent.contexts[0].edit_tag, first.structuredContent.matches[0].edit_tag);
    assert.match(first.content[0].text, /\$ARG="one"/);
    assert.match(first.content[0].text, /1 \| export function first/);

    const second = await astGrep(f, { ...query, cursor: first.structuredContent.next_cursor });
    assert.notEqual(second.isError, true, second.content?.[0]?.text);
    assert.equal(second.structuredContent.matches.length, 1);
    assert.equal(second.structuredContent.matches[0].start_line, 6);
    assert.equal(second.structuredContent.has_more, false);
    assert.equal(second.structuredContent.next_cursor, null);

    const mismatched = await astGrep(f, {
      ...query,
      pattern: 'console.info($ARG)',
      cursor: first.structuredContent.next_cursor
    });
    assert.equal(mismatched.isError, true);
    assert.equal(mismatched.structuredContent.error_code, 'ast_grep_cursor_mismatch');
    assert.equal(mismatched.structuredContent.recovery.tool, 'ast_grep');

    const edited = await f.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: f.workspaceId,
        path: 'src/sample.ts',
        edit_tag: first.structuredContent.matches[0].edit_tag,
        edits: [{
          op: 'replace',
          start_line: first.structuredContent.matches[0].start_line,
          content: '  console.info("one");'
        }]
      }
    });
    assert.notEqual(edited.isError, true, edited.content?.[0]?.text);
    assert.match(await fs.readFile(path.join(f.repo, 'src', 'sample.ts'), 'utf8'), /console\.info\("one"\)/);
  } finally {
    await f.close();
  }
});

test('ast_grep supports syntax-kind mode, blocked-path filtering, validation, and parallel batch children', async () => {
  const f = await fixture();
  try {
    await fs.mkdir(path.join(f.repo, 'src'));
    await fs.writeFile(path.join(f.repo, 'src', 'one.ts'), 'export function one() {\n  console.warn("one");\n}\n');
    await fs.writeFile(path.join(f.repo, 'src', 'two.ts'), 'export function two() {\n  console.warn("two");\n}\n');
    await fs.writeFile(path.join(f.repo, '.env.ts'), 'export function hidden() { console.warn("hidden"); }\n');

    const kind = await astGrep(f, {
      kind: 'function_declaration',
      language: 'ts',
      path: '.',
      include_hidden: true,
      max_results: 20,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(kind.isError, true, kind.content?.[0]?.text);
    assert.equal(kind.structuredContent.mode, 'kind');
    assert.deepEqual(new Set(kind.structuredContent.matches.map((match) => match.path)), new Set([
      'src/one.ts',
      'src/two.ts'
    ]));
    assert.equal(kind.structuredContent.matches.some((match) => match.path === '.env.ts'), false);

    const invalid = await astGrep(f, {
      pattern: 'console.warn($ARG)',
      kind: 'call_expression',
      language: 'ts'
    });
    assert.equal(invalid.isError, true);
    assert.equal(invalid.structuredContent.error_code, 'ast_grep_query_invalid');

    const batch = await f.client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: f.workspaceId,
        mode: 'parallel',
        operations: [
          {
            id: 'functions',
            tool: 'ast_grep',
            args: {
              kind: 'function_declaration',
              language: 'ts',
              path: 'src',
              max_results: 10,
              context_before: 0,
              context_after: 0
            }
          },
          {
            id: 'warnings',
            tool: 'ast_grep',
            args: {
              pattern: 'console.warn($ARG)',
              language: 'ts',
              path: 'src',
              max_results: 10,
              context_before: 0,
              context_after: 0
            }
          }
        ]
      }
    });
    assert.notEqual(batch.isError, true, batch.content?.[0]?.text);
    assert.equal(batch.structuredContent.succeeded_count, 2);
    assert.equal(batch.structuredContent.results.every((child) => child.ok), true);
    assert.deepEqual(batch.structuredContent.results.map((child) => child.tool), ['ast_grep', 'ast_grep']);
  } finally {
    await f.close();
  }
});

test('ast_grep audit records bounded operational metadata without pattern, cursor, captures, contexts, or edit tags', async () => {
  const f = await fixture({ audit: true });
  try {
    const privateName = 'privateAstCall_7f1b2d';
    const privateArgument = 'PRIVATE_AST_ARGUMENT_31c9a4';
    await fs.writeFile(path.join(f.repo, 'calls.ts'), [
      `${privateName}("${privateArgument}-one");`,
      `${privateName}("${privateArgument}-two");`,
      ''
    ].join('\n'));
    const pattern = `${privateName}($ARG)`;
    const first = await astGrep(f, {
      pattern,
      language: 'ts',
      path: 'calls.ts',
      max_results: 1,
      context_before: 0,
      context_after: 0
    });
    assert.notEqual(first.isError, true, first.content?.[0]?.text);
    assert.equal(first.structuredContent.has_more, true);
    assert.match(first.structuredContent.next_cursor, /^[A-Za-z0-9_-]+$/);

    const second = await astGrep(f, {
      pattern,
      language: 'ts',
      path: 'calls.ts',
      max_results: 1,
      context_before: 0,
      context_after: 0,
      cursor: first.structuredContent.next_cursor
    });
    assert.notEqual(second.isError, true, second.content?.[0]?.text);

    const raw = await fs.readFile(f.auditPath, 'utf8');
    assert.equal(raw.includes(privateName), false, 'journal retained the structural pattern or returned source');
    assert.equal(raw.includes(privateArgument), false, 'journal retained captures or context');
    assert.equal(raw.includes(first.structuredContent.next_cursor), false, 'journal retained the cursor');
    assert.doesNotMatch(raw, /"captures":|"contexts":|"edit_tag":|"next_cursor":/);

    const records = raw.trim().split('\n').map((line) => JSON.parse(line)).filter((action) => action.tool_name === 'ast_grep');
    assert.equal(records.length, 2);
    assert.deepEqual(records.map((action) => action.request_metadata.cursor_supplied), [false, true]);
    assert.match(records[0].request_metadata.pattern_digest, /^[0-9a-f]{64}$/);
    assert.equal(records[0].request_metadata.pattern_bytes, Buffer.byteLength(pattern));
    assert.equal(records[0].request_metadata.ast_language, 'ts');
    assert.equal(records[0].request_metadata.context_before, 0);
    assert.equal(records[0].request_metadata.context_after, 0);
    assert.equal(records[0].result_metadata.matches_count, 1);
    assert.equal(records[0].result_metadata.contexts_count, 1);
    assert.equal(records[0].result_metadata.editable_matches_count, 1);
    assert.equal(records[0].result_metadata.has_more, true);
    assert.equal(records[0].result_metadata.ast_provider, 'ast-grep-cli');
    assert.match(records[0].result_metadata.ast_provider_version, /^\d+\.\d+\.\d+/);
    assert.equal(records[0].result_metadata.ast_mode, 'pattern');
    assert.match(records[0].result_metadata.query_fingerprint, /^[0-9a-f]{64}$/);
  } finally {
    await f.close();
  }
});

test('codexpro ast-grep CLI uses the shared provider in pattern and kind modes', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-ast-grep-cli-'));
  try {
    await fs.mkdir(path.join(root, 'src'));
    await fs.writeFile(path.join(root, 'src', 'sample.ts'), [
      'export function sample() {',
      '  console.error("boom");',
      '}',
      ''
    ].join('\n'));

    const pattern = spawnSync(process.execPath, [
      codexproCli,
      'ast-grep',
      '--root', root,
      '--pattern', 'console.error($ARG)',
      '--lang', 'ts',
      '--path', 'src',
      '--max-results', '10',
      '--context-before', '0',
      '--context-after', '0',
      '--json'
    ], { encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' } });
    assert.equal(pattern.status, 0, pattern.stderr || pattern.stdout);
    const patternResult = JSON.parse(pattern.stdout);
    assert.equal(patternResult.provider, 'ast-grep-cli');
    assert.match(patternResult.providerVersion, /^\d+\.\d+\.\d+/);
    assert.equal(patternResult.mode, 'pattern');
    assert.equal(patternResult.matches.length, 1);
    assert.equal(patternResult.matches[0].path, 'src/sample.ts');
    assert.equal(patternResult.matches[0].editable, false, 'standalone CLI must not claim MCP edit provenance');

    const kind = spawnSync(process.execPath, [
      codexproCli,
      'ast',
      '--root', root,
      '--kind', 'function_declaration',
      '--lang', 'ts',
      '--path', 'src',
      '--json'
    ], { encoding: 'utf8', env: { ...process.env, NO_COLOR: '1' } });
    assert.equal(kind.status, 0, kind.stderr || kind.stdout);
    const kindResult = JSON.parse(kind.stdout);
    assert.equal(kindResult.mode, 'kind');
    assert.equal(kindResult.matches.length, 1);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});
