import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { loadConfig } from '../dist/config.js';
import { WorkspaceManager } from '../dist/guard.js';
import { loadProjectCatalog } from '../dist/projects/catalog.js';
import { createCatalogProject } from '../dist/projects/create.js';
import { WorktreeManager } from '../dist/worktrees/manager.js';

function git(cwd, args) {
  const result = spawnSync('git', args, {
    cwd,
    encoding: 'utf8',
    shell: false,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', NO_COLOR: '1' }
  });
  if (result.status !== 0) throw new Error(result.stderr || result.stdout || `git ${args.join(' ')} failed`);
  return result.stdout.trim();
}

async function directFixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-project-create-'));
  const container = path.join(root, 'projects');
  const existing = path.join(container, 'existing');
  const state = path.join(root, 'state');
  await fs.mkdir(existing, { recursive: true });
  const file = path.join(root, 'projects.json');
  await fs.writeFile(file, `${JSON.stringify({
    version: 1,
    defaultProject: 'existing',
    projects: [{ id: 'existing', label: 'Existing project', root: existing }],
    creationRoots: [{ id: 'projects', label: 'Projects directory', root: container }]
  }, null, 2)}\n`, 'utf8');
  const config = loadConfig(['--projects-file', file, '--worktree-root', state]);
  const manager = new WorkspaceManager(config);
  const register = async (project) => manager.addProject(project);
  return {
    root,
    container,
    existing,
    state,
    file,
    config,
    manager,
    register,
    cleanup: () => fs.rm(root, { recursive: true, force: true })
  };
}

async function worktreeFixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-project-create-worktree-'));
  const container = path.join(root, 'projects');
  const parent = path.join(container, 'parent');
  const state = path.join(root, 'state');
  await fs.mkdir(parent, { recursive: true });
  git(parent, ['init', '-q']);
  git(parent, ['config', 'user.email', 'project-create@example.com']);
  git(parent, ['config', 'user.name', 'Project Create Test']);
  await fs.writeFile(path.join(parent, 'parent.txt'), 'parent\n', 'utf8');
  git(parent, ['add', '.']);
  git(parent, ['commit', '-q', '-m', 'parent initial']);
  const file = path.join(root, 'projects.json');
  await fs.writeFile(file, `${JSON.stringify({
    version: 1,
    defaultProject: 'parent',
    projects: [{ id: 'parent', label: 'Parent project', root: parent }],
    creationRoots: [{ id: 'projects', label: 'Projects directory', root: container }]
  }, null, 2)}\n`, 'utf8');
  const config = loadConfig([
    '--projects-file', file,
    '--worktree-mode', 'mcp',
    '--worktree-root', state
  ]);
  const manager = new WorktreeManager(config);
  await manager.initialize();
  return {
    root,
    container,
    parent,
    state,
    file,
    config,
    manager,
    cleanup: () => fs.rm(root, { recursive: true, force: true })
  };
}

function context(principalId = 'project-create-principal') {
  return {
    principalId,
    requestId: randomUUID(),
    transportSessionId: randomUUID(),
    signal: new AbortController().signal
  };
}

test('creates and immediately opens a persistent raw project under a non-runnable creation root', async () => {
  const f = await directFixture();
  try {
    assert.deepEqual(f.config.projectCreationRoots.map((creationRoot) => creationRoot.id), ['projects']);
    assert.throws(() => f.manager.openProject('projects'), /unknown project_id/i);

    const created = await createCatalogProject(f.config, {
      projectId: 'raw-project',
      parentId: 'projects',
      label: 'Raw project',
      source: 'empty'
    }, f.register);

    const expectedRoot = path.join(f.container, 'raw-project');
    assert.equal(created.project.root, await fs.realpath(expectedRoot));
    assert.equal(created.source, 'empty');
    assert.equal(created.cloned, false);
    assert.equal(created.gitInitialized, false);
    assert.deepEqual(await fs.readdir(expectedRoot), []);
    assert.deepEqual(f.manager.listProjects().map((project) => project.id), ['existing', 'raw-project']);
    assert.equal(f.manager.openProject('raw-project').root, await fs.realpath(expectedRoot));
    const catalog = loadProjectCatalog(f.file);
    assert.deepEqual(catalog.projects.map((project) => project.id), ['existing', 'raw-project']);
    assert.deepEqual(catalog.creationRoots.map((creationRoot) => creationRoot.id), ['projects']);

    await assert.rejects(
      () => createCatalogProject(f.config, {
        projectId: 'raw-project',
        parentId: 'projects',
        source: 'empty'
      }, f.register),
      /already exists/i
    );
    await assert.rejects(
      () => createCatalogProject(f.config, {
        projectId: 'projects',
        parentId: 'projects',
        source: 'empty'
      }, f.register),
      /already exists/i
    );
  } finally {
    await f.cleanup();
  }
});

test('initializes Git with a usable initial commit and configured base ref', async () => {
  const f = await directFixture();
  try {
    const created = await createCatalogProject(f.config, {
      projectId: 'git-project',
      parentId: 'projects',
      label: 'Git project',
      source: 'git',
      initialBranch: 'trunk',
      baseRef: 'trunk',
      maxWorktrees: 7
    }, f.register);

    assert.equal(created.project.root, path.join(f.container, 'git-project'));
    assert.equal(created.gitInitialized, true);
    assert.equal(created.initialCommitCreated, true);
    assert.equal(created.cloned, false);
    assert.equal(git(created.project.root, ['branch', '--show-current']), 'trunk');
    assert.match(git(created.project.root, ['rev-parse', 'HEAD']), /^[0-9a-f]{40,64}$/i);
    assert.equal(git(created.project.root, ['log', '-1', '--pretty=%s']), 'Initial commit');
    const stored = loadProjectCatalog(f.file).projects.find((project) => project.id === 'git-project');
    assert.equal(stored?.baseRef, 'trunk');
    assert.equal(stored?.maxWorktrees, 7);
  } finally {
    await f.cleanup();
  }
});

test('clones an allowed local Git repository without running source hooks', async () => {
  const f = await directFixture();
  try {
    const source = path.join(f.container, 'seed source');
    await fs.mkdir(source);
    git(source, ['init', '-q']);
    git(source, ['config', 'user.email', 'seed@example.com']);
    git(source, ['config', 'user.name', 'Seed Test']);
    await fs.writeFile(path.join(source, 'seed.txt'), 'seeded\n', 'utf8');
    git(source, ['add', '.']);
    git(source, ['commit', '-q', '-m', 'seed']);

    const created = await createCatalogProject(f.config, {
      projectId: 'cloned-project',
      parentId: 'projects',
      source: 'git',
      repository: './seed source'
    }, f.register);

    assert.equal(created.project.root, path.join(f.container, 'cloned-project'));
    assert.equal(created.cloned, true);
    assert.equal(created.initialCommitCreated, false);
    assert.equal(await fs.readFile(path.join(created.project.root, 'seed.txt'), 'utf8'), 'seeded\n');
    assert.equal(git(created.project.root, ['log', '-1', '--pretty=%s']), 'seed');
  } finally {
    await f.cleanup();
  }
});

test('rolls back the catalog and owned directory when runtime registration fails', async () => {
  const f = await directFixture();
  try {
    const before = await fs.readFile(f.file, 'utf8');
    await assert.rejects(
      () => createCatalogProject(f.config, {
        projectId: 'rolled-back',
        parentId: 'projects',
        source: 'empty'
      }, async () => {
        throw new Error('registration failed');
      }),
      /registration failed/i
    );
    assert.equal(await fs.readFile(f.file, 'utf8'), before);
    await assert.rejects(() => fs.stat(path.join(f.container, 'rolled-back')), /ENOENT/);
    assert.deepEqual(f.config.projects.map((project) => project.id), ['existing']);
  } finally {
    await f.cleanup();
  }
});

test('rejects traversal, stale catalogs, and raw projects in worktree mode without side effects', async () => {
  const f = await directFixture();
  try {
    await assert.rejects(
      () => createCatalogProject(f.config, {
        projectId: 'escape',
        parentId: 'projects',
        directory: '../escape',
        source: 'empty'
      }, f.register),
      /directory/i
    );
    await assert.rejects(() => fs.stat(path.join(f.root, 'escape')), /ENOENT/);

    const document = JSON.parse(await fs.readFile(f.file, 'utf8'));
    document.creationRoots[0].label = 'Changed elsewhere';
    await fs.writeFile(f.file, `${JSON.stringify(document, null, 2)}\n`, 'utf8');
    await assert.rejects(
      () => createCatalogProject(f.config, {
        projectId: 'stale-catalog',
        parentId: 'projects',
        source: 'empty'
      }, f.register),
      /changed after this CodexPro server started/i
    );
    await assert.rejects(() => fs.stat(path.join(f.container, 'stale-catalog')), /ENOENT/);
  } finally {
    await f.cleanup();
  }

  const worktree = await worktreeFixture();
  try {
    await assert.rejects(
      () => createCatalogProject(worktree.config, {
        projectId: 'raw-project',
        parentId: 'projects',
        source: 'empty'
      }, (project) => worktree.manager.addProject(project)),
      /raw empty projects cannot be added/i
    );
    await assert.rejects(() => fs.stat(path.join(worktree.container, 'raw-project')), /ENOENT/);
  } finally {
    await worktree.cleanup();
  }
});

test('registers a sibling Git project with the live worktree manager', async () => {
  const f = await worktreeFixture();
  try {
    const created = await createCatalogProject(f.config, {
      projectId: 'dynamic-git',
      parentId: 'projects',
      source: 'git',
      baseRef: 'main',
      maxWorktrees: 2
    }, (project) => f.manager.addProject(project));

    assert.equal(created.project.root, path.join(f.container, 'dynamic-git'));
    assert.equal(path.dirname(created.project.root), f.container);
    assert.notEqual(path.dirname(created.project.root), f.parent);
    assert.deepEqual(f.manager.listProjects().map((project) => project.id), ['parent', 'dynamic-git']);
    assert.equal(created.summary.maxWorktrees, 2);
    const workspace = await f.manager.createWorkspace(context(), {
      projectId: 'dynamic-git',
      idempotencyKey: 'dynamic-project-workspace'
    });
    assert.equal(workspace.projectId, 'dynamic-git');
    assert.match(git(workspace.workspace.root, ['rev-parse', 'HEAD']), /^[0-9a-f]{40,64}$/i);
    assert.equal(git(workspace.workspace.root, ['log', '-1', '--pretty=%s']), 'Initial commit');
  } finally {
    await f.cleanup();
  }
});
