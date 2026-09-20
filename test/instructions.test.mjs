import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { createWorkspaceAccess } from '../dist/workspaceAccess.js';

async function fixture(options = {}) {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-instructions-')));
  const global = path.join(root, 'codex-home');
  const alpha = path.join(root, 'alpha'), beta = path.join(root, 'beta');
  for (const dir of [global, alpha, beta]) await fs.mkdir(dir);
  await fs.writeFile(path.join(global, 'AGENTS.md'), 'GLOBAL_RULE: shared operator guidance.\n');
  await fs.writeFile(path.join(alpha, 'AGENTS.md'), 'ALPHA_RULE: project alpha guidance.\n');
  await fs.writeFile(path.join(beta, 'AGENTS.md'), 'BETA_RULE: project beta guidance.\n');
  const catalog = path.join(root, 'projects.json');
  await fs.writeFile(catalog, JSON.stringify({ version: 1, defaultProject: 'alpha', projects: [{ id: 'alpha', root: alpha }, { id: 'beta', root: beta }] }));
  const args = ['--projects-file', catalog, '--codex-dir', global, '--tool-mode', 'full', '--handoff-mode', 'on', '--bash', 'off'];
  if (options.worktree) {
    for (const dir of [alpha, beta]) {
      const git = (...args) => execFileSync('git', ['-C', dir, ...args], { stdio: 'pipe' });
      git('init', '-b', 'main'); git('add', '.');
      git('-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'Instructions fixture');
    }
    args.push('--worktree-mode', 'mcp', '--worktree-root', path.join(root, 'worktrees'));
  }
  if (options.single) args.splice(0, 2, '--root', alpha);
  const config = loadConfig(args);
  if (options.maxOutputBytes) config.maxOutputBytes = options.maxOutputBytes;
  const server = createCodexProServer(config, await createWorkspaceAccess(config));
  const client = new Client({ name: 'instructions-test', version: '1' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport); await client.connect(clientTransport);
  return { root, global, alpha, beta, config,
    call: async (name, args = {}) => {
      const result = await client.callTool({ name, arguments: args });
      assert.notEqual(result.isError, true, JSON.stringify(result));
      return { data: result.structuredContent, text: result.content.filter(c => c.type === 'text').map(c => c.text).join('\n') };
    },
    close: async () => { await client.close(); await server.close(); await fs.rm(root, { recursive: true, force: true }); }
  };
}

test('normal and repeated opens attach global then project contents and accurate sources', async () => {
  const f = await fixture();
  try {
    for (const name of ['open_workspace', 'open_workspace']) {
      const { data, text } = await f.call(name, name === 'open_workspace' ? { project_id: 'alpha', include_tree: false } : {});
      assert.ok(text.indexOf('GLOBAL_RULE') < text.indexOf('ALPHA_RULE'));
      assert.match(text, /ALPHA_RULE/); assert.doesNotMatch(text, /BETA_RULE/);
      assert.equal(data.agents_loaded, true); assert.equal(data.agents_complete, true);
      assert.equal(data.agents_path, 'AGENTS.md');
      assert.deepEqual(data.agents_sources.map(s => s.scope), ['global', 'workspace']);
      assert.ok(data.agents_files[0].endsWith('/AGENTS.md'));
    }
    await fs.writeFile(path.join(f.global, 'AGENTS.md'), 'GLOBAL_UPDATED\n');
    const refreshed = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.match(refreshed.text, /GLOBAL_UPDATED/); assert.doesNotMatch(refreshed.text, /GLOBAL_RULE/);
  } finally { await f.close(); }
});

test('default workspace opens attach global and local instructions', async () => {
  const f = await fixture({ single: true });
  try {
    const result = await f.call('open_current_workspace', { include_tree: false });
    assert.match(result.text, /GLOBAL_RULE/); assert.match(result.text, /ALPHA_RULE/);
    assert.equal(result.data.agents_complete, true);
  } finally { await f.close(); }
});

test('multi-open attaches shared global contents once and scopes each project correctly', async () => {
  const f = await fixture();
  try {
    const { data, text } = await f.call('open_workspace', { project_ids: ['beta', 'alpha'] });
    assert.equal(text.split('GLOBAL_RULE').length - 1, 1);
    assert.ok(text.indexOf('GLOBAL_RULE') < text.indexOf('BETA_RULE'));
    assert.ok(text.indexOf('BETA_RULE') < text.indexOf('ALPHA_RULE'));
    assert.equal(data.agents_complete, true);
    for (const entry of data.workspaces) {
      assert.equal(entry.agents_complete, true);
      assert.deepEqual(entry.agents_sources.map(s => s.scope), ['global', 'workspace']);
    }
  } finally { await f.close(); }
});

test('overrides replace regular files independently in global and project scopes', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.global, 'AGENTS.override.md'), 'GLOBAL_OVERRIDE\n');
    await fs.writeFile(path.join(f.alpha, 'AGENTS.override.md'), 'PROJECT_OVERRIDE\n');
    const { data, text } = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.match(text, /GLOBAL_OVERRIDE/); assert.match(text, /PROJECT_OVERRIDE/);
    assert.doesNotMatch(text, /GLOBAL_RULE|ALPHA_RULE/);
    assert.equal(data.agents_path, 'AGENTS.override.md');
    assert.equal(data.agents_files.length, 2);
  } finally { await f.close(); }
});

test('missing project instructions still attach global rules; no files means not loaded', async () => {
  const f = await fixture();
  try {
    await fs.rm(path.join(f.alpha, 'AGENTS.md'));
    let result = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.match(result.text, /GLOBAL_RULE/); assert.match(result.text, /Workspace instructions: none/);
    assert.equal(result.data.agents_loaded, true); assert.equal(result.data.agents_path, undefined);
    await fs.rm(path.join(f.global, 'AGENTS.md'));
    result = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.equal(result.data.agents_loaded, false); assert.deepEqual(result.data.agents_sources, []);
  } finally { await f.close(); }
});

test('unreadable and escaping project instructions are explicit and never read outside the workspace', async () => {
  const f = await fixture();
  try {
    await fs.rm(path.join(f.alpha, 'AGENTS.md'));
    await fs.writeFile(path.join(f.root, 'outside.md'), 'OUTSIDE_SECRET_DO_NOT_ATTACH');
    await fs.symlink(path.join(f.root, 'outside.md'), path.join(f.alpha, 'AGENTS.md'));
    let result = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.doesNotMatch(result.text, /OUTSIDE_SECRET_DO_NOT_ATTACH/);
    assert.equal(result.data.agents_complete, false);
    assert.equal(result.data.agents_sources[1].loaded, false);
    assert.match(result.text, /Instructions unreadable/);
    await fs.rm(path.join(f.global, 'AGENTS.md'));
    await fs.mkdir(path.join(f.global, 'AGENTS.md'));
    result = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.equal(result.data.agents_loaded, false);
    assert.equal(result.data.agents_complete, false);
  } finally { await f.close(); }
});

test('large instructions have UTF-8-safe bounded previews, explicit truncation, and room for local rules', async () => {
  const f = await fixture({ maxOutputBytes: 4000 });
  try {
    await fs.writeFile(path.join(f.global, 'AGENTS.md'), '🧭'.repeat(10000));
    const { data, text } = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.equal(data.agents_complete, false); assert.equal(data.agents_sources[0].truncated, true);
    assert.match(text, /ALPHA_RULE/); assert.match(text, /Instructions truncated/);
    assert.doesNotMatch(text, /\uFFFD/); assert.ok(Buffer.byteLength(text) < 4000);
  } finally { await f.close(); }
});

test('codex_context includes global and nested rules for dotted directories and extensionless files', async () => {
  const f = await fixture();
  try {
    await fs.mkdir(path.join(f.alpha, 'v1.2'));
    await fs.writeFile(path.join(f.alpha, 'v1.2', 'agents.md'), 'NESTED_RULE\n');
    await fs.writeFile(path.join(f.alpha, 'v1.2', 'Makefile'), 'all:\n');
    const opened = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    for (const target_path of ['v1.2', 'v1.2/Makefile', 'v1.2/new-file']) {
      const { data, text } = await f.call('codex_context', { workspace_id: opened.data.workspace_id, target_path, include_git: false, include_ai_bridge: false });
      assert.ok(text.indexOf('GLOBAL_RULE') < text.indexOf('ALPHA_RULE'));
      assert.ok(text.indexOf('ALPHA_RULE') < text.indexOf('NESTED_RULE'));
      assert.equal(data.agents_files.length, 3); assert.equal(data.agents_complete, true);
    }
  } finally { await f.close(); }
});

test('created and resumed isolated worktrees attach their own instructions plus the global file', async () => {
  const f = await fixture({ worktree: true });
  try {
    const created = await f.call('create_workspace', { project_id: 'alpha', idempotency_key: 'instruction-check', include_skills: false });
    assert.match(created.text, /GLOBAL_RULE/); assert.match(created.text, /ALPHA_RULE/);
    assert.equal(created.data.agents_complete, true);
    const reopened = await f.call('open_workspace', { workspace_id: created.data.workspace_id, include_tree: false });
    assert.match(reopened.text, /GLOBAL_RULE/); assert.match(reopened.text, /ALPHA_RULE/);
    assert.equal(reopened.data.agents_complete, true);
  } finally { await f.close(); }
});

test('attached contents preserve existing secret redaction and do not scan ancestor instructions', async () => {
  const f = await fixture();
  try {
    const secret = 'sk-' + 'x9'.repeat(20);
    await fs.writeFile(path.join(f.root, 'AGENTS.md'), 'PARENT_RULE_NOT_REQUESTED\n');
    await fs.writeFile(path.join(f.global, 'AGENTS.md'), `GLOBAL_RULE\n${secret}\n`);
    const result = await f.call('open_workspace', { project_id: 'alpha', include_tree: false });
    assert.match(result.text, /GLOBAL_RULE/); assert.match(result.text, /ALPHA_RULE/);
    assert.match(result.text, /REDACTED_SECRET/);
    assert.ok(!JSON.stringify(result).includes(secret));
    assert.doesNotMatch(result.text, /PARENT_RULE_NOT_REQUESTED/);
  } finally { await f.close(); }
});

test('Codex directory honors explicit config before CODEX_HOME', async () => {
  const f = await fixture();
  const saved = { home: process.env.CODEX_HOME, explicit: process.env.CODEXPRO_CODEX_DIR };
  try {
    process.env.CODEX_HOME = f.global;
    delete process.env.CODEXPRO_CODEX_DIR;
    assert.equal(loadConfig(['--root', f.alpha]).codexDir, f.global);
    process.env.CODEXPRO_CODEX_DIR = f.beta;
    assert.equal(loadConfig(['--root', f.alpha]).codexDir, f.beta);
    assert.equal(loadConfig(['--root', f.alpha, '--codex-dir', f.alpha]).codexDir, f.alpha);
  } finally {
    for (const [key, value] of [['CODEX_HOME', saved.home], ['CODEXPRO_CODEX_DIR', saved.explicit]]) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    await f.close();
  }
});
