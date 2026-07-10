import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { loadConfig } from '../dist/config.js';
import { WorkspaceManager } from '../dist/guard.js';
import { loadProjectCatalog } from '../dist/projects/catalog.js';

async function catalogFixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-project-catalog-'));
  const alpha = path.join(root, 'alpha');
  const beta = path.join(root, 'beta');
  const state = path.join(root, 'state');
  await fs.mkdir(alpha);
  await fs.mkdir(beta);
  const file = path.join(root, 'projects.json');
  const write = async (value) => fs.writeFile(file, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
  await write({
    version: 1,
    defaultProject: 'beta',
    projects: [
      { id: 'alpha', label: 'Alpha project', root: './alpha', baseRef: 'main', maxWorktrees: 3 },
      { id: 'beta', root: './beta' }
    ]
  });
  return { root, alpha, beta, state, file, write, cleanup: () => fs.rm(root, { recursive: true, force: true }) };
}

test('loads a canonical named project catalog with a selected default', async () => {
  const f = await catalogFixture();
  try {
    const catalog = loadProjectCatalog(f.file);
    assert.equal(catalog.defaultProjectId, 'beta');
    assert.equal(catalog.filePath, await fs.realpath(f.file));
    assert.deepEqual(catalog.projects.map((project) => project.id), ['alpha', 'beta']);
    assert.equal(catalog.projects[0].root, await fs.realpath(f.alpha));
    assert.equal(catalog.projects[1].root, await fs.realpath(f.beta));
    assert.equal(catalog.projects[1].label, 'beta');

    const config = loadConfig(['--projects-file', f.file, '--worktree-root', f.state]);
    assert.equal(config.defaultRoot, await fs.realpath(f.beta));
    assert.equal(config.defaultProjectId, 'beta');
    assert.equal(config.projectsFile, await fs.realpath(f.file));
    assert.deepEqual(config.allowedRoots.sort(), [await fs.realpath(f.alpha), await fs.realpath(f.beta)].sort());
    const workspaces = new WorkspaceManager(config);
    assert.deepEqual(workspaces.listProjects().map((project) => project.id), ['alpha', 'beta']);
    assert.equal(workspaces.defaultWorkspace().projectId, 'beta');
    assert.equal(workspaces.openProject('alpha').projectId, 'alpha');
    assert.throws(() => workspaces.openProject('missing'), /unknown project_id/i);
  } finally {
    await f.cleanup();
  }
});

test('keeps the connector identity stable across catalog edits and restarts', async () => {
  const f = await catalogFixture();
  try {
    const args = ['--projects-file', f.file, '--worktree-mode', 'mcp', '--worktree-root', f.state, '--bash', 'safe'];
    const first = loadConfig(args);
    const parsed = JSON.parse(await fs.readFile(f.file, 'utf8'));
    parsed.projects.reverse();
    await f.write(parsed);
    const second = loadConfig(args);
    assert.equal(second.connectorId, first.connectorId);
    assert.match(second.connectorId, /^[0-9a-f]{48}$/);
    assert.equal(await fs.readFile(path.join(f.state, 'connector-id'), 'utf8'), `${first.connectorId}\n`);
  } finally {
    await f.cleanup();
  }
});

test('rejects ambiguous, malformed, or path-based catalog configuration', async () => {
  const f = await catalogFixture();
  try {
    assert.throws(
      () => loadConfig(['--projects-file', f.file, '--root', f.alpha]),
      /cannot be combined/i
    );
    await f.write({
      version: 1,
      projects: [
        { id: 'same', root: './alpha' },
        { id: 'same', root: './beta' }
      ]
    });
    assert.throws(() => loadProjectCatalog(f.file), /duplicate project id/i);
    await f.write({
      version: 1,
      projects: [
        { id: 'alpha', root: './alpha' },
        { id: 'alias', root: './alpha' }
      ]
    });
    assert.throws(() => loadProjectCatalog(f.file), /duplicate canonical project root/i);
    await f.write({ version: 1, projects: [{ id: '../escape', root: './alpha' }] });
    assert.throws(() => loadProjectCatalog(f.file), /id must be/i);
    await f.write({ version: 1, projects: [{ id: 'alpha', root: './alpha', unexpected: true }] });
    assert.throws(() => loadProjectCatalog(f.file), /unknown field/i);
    await f.write({ version: 1, projects: [{ id: 'alpha', label: 'bad\nlabel', root: './alpha' }] });
    assert.throws(() => loadProjectCatalog(f.file), /control characters/i);
  } finally {
    await f.cleanup();
  }
});
