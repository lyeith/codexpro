import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { WorktreeManager } from '../dist/worktrees/manager.js';

function git(cwd, args) {
  const result = spawnSync('git', args, { cwd, encoding: 'utf8', shell: false });
  if (result.status !== 0) throw new Error(result.stderr || result.stdout || `git ${args.join(' ')} failed`);
  return result.stdout.trim();
}

async function fixture(options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-worktree-repo-'));
  const state = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-worktree-state-'));
  git(root, ['init', '-q']);
  git(root, ['config', 'user.email', 'worktree-test@example.com']);
  git(root, ['config', 'user.name', 'Worktree Test']);
  await fs.writeFile(path.join(root, 'shared.txt'), 'committed\n', 'utf8');
  await fs.mkdir(path.join(root, 'packages', 'app'), { recursive: true });
  await fs.writeFile(path.join(root, 'packages', 'app', 'app.txt'), 'app committed\n', 'utf8');
  git(root, ['add', '.']);
  git(root, ['commit', '-q', '-m', 'initial']);
  const scopeRoot = options.subdirectory ? path.join(root, 'packages', 'app') : root;
  const config = {
    defaultRoot: scopeRoot,
    allowedRoots: [scopeRoot],
    projects: [{ id: 'default', label: 'Default project', root: scopeRoot }],
    defaultProjectId: 'default',
    connectorId: 'test-connector',
    host: '127.0.0.1',
    port: 8787,
    widgetDomain: 'https://widgets.example.test',
    requireHttpToken: false,
    bashMode: 'safe',
    bashTranscript: 'compact',
    requireBashSession: false,
    codexSessions: 'off',
    codexDir: path.join(state, 'codex'),
    writeMode: 'workspace',
    toolMode: 'full',
    inheritEnv: false,
    maxReadBytes: 180_000,
    maxWriteBytes: 1_000_000,
    maxOutputBytes: 120_000,
    maxSearchResults: 200,
    maxHttpSessions: 64,
    httpSessionTtlMs: 30 * 60_000,
    blockedGlobs: ['.git', '.git/**', '**/.git/**', '.env', '.env.*', '**/.env', '**/.env.*'],
    contextDir: '.ai-bridge',
    worktreeMode: 'mcp',
    worktreeRoot: path.join(state, 'worktrees'),
    worktreeBaseRef: 'HEAD',
    maxWorktrees: options.maxWorktrees ?? 64
  };
  const cleanup = async () => {
    await fs.rm(state, { recursive: true, force: true });
    await fs.rm(root, { recursive: true, force: true });
  };
  return { root, state, scopeRoot, config, cleanup };
}

function context(principalId = 'principal-a', requestId = randomUUID()) {
  return {
    principalId,
    requestId,
    transportSessionId: randomUUID(),
    signal: new AbortController().signal
  };
}

test('creates isolated durable worktrees and reuses idempotency keys', async () => {
  const f = await fixture();
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    await fs.writeFile(path.join(f.root, 'shared.txt'), 'dirty source checkout\n', 'utf8');

    const owner = context();
    const first = await manager.createWorkspace(owner, { idempotencyKey: 'task-a', label: '../../not-a-path' });
    const retried = await manager.createWorkspace({ ...owner, requestId: randomUUID() }, { idempotencyKey: 'task-a', label: 'ignored retry label' });
    const second = await manager.createWorkspace(context(), { idempotencyKey: 'task-b' });

    assert.equal(retried.created, false);
    assert.equal(retried.workspace.id, first.workspace.id);
    assert.notEqual(first.workspace.id, second.workspace.id);
    assert.notEqual(first.workspace.root, second.workspace.root);
    assert.match(first.workspace.id, /^wt_[A-Za-z0-9_-]{32}$/);
    assert.match(first.branch, /^codexpro\/mcp\/[0-9a-f]{24}$/);
    assert.equal(await fs.readFile(path.join(first.workspace.root, 'shared.txt'), 'utf8'), 'committed\n');
    assert.equal(await fs.readFile(path.join(second.workspace.root, 'shared.txt'), 'utf8'), 'committed\n');

    await fs.writeFile(path.join(first.workspace.root, 'shared.txt'), 'first\n', 'utf8');
    await fs.writeFile(path.join(second.workspace.root, 'shared.txt'), 'second\n', 'utf8');
    assert.equal(await fs.readFile(path.join(first.workspace.root, 'shared.txt'), 'utf8'), 'first\n');
    assert.equal(await fs.readFile(path.join(second.workspace.root, 'shared.txt'), 'utf8'), 'second\n');

    assert.throws(() => manager.getWorkspace(context('principal-b'), first.workspace.id), /access denied/i);
    assert.throws(() => manager.getWorkspace(owner), /workspace_id is required/i);
    assert.throws(() => manager.getWorkspace(owner, 'wt_not-real'), /unknown workspace_id/i);

    await manager.releaseWorkspace(owner, first.workspace.id);
    assert.equal(await fs.readFile(path.join(first.workspace.root, 'shared.txt'), 'utf8'), 'first\n');

    const restarted = new WorktreeManager(f.config);
    await restarted.initialize();
    assert.equal(restarted.getWorkspace(owner, first.workspace.id).root, first.workspace.root);
    await assert.rejects(() => restarted.removeWorkspace(owner, first.workspace.id), /dirty worktree/i);
    assert.equal(restarted.getWorkspace(owner, first.workspace.id).root, first.workspace.root);
  } finally {
    await f.cleanup();
  }
});

test('serializes concurrent provisioning and enforces retained limits', async () => {
  const f = await fixture({ maxWorktrees: 6 });
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const owner = context();
    const handles = await Promise.all(
      Array.from({ length: 6 }, (_, index) => manager.createWorkspace(
        { ...owner, requestId: `request-${index}` },
        { idempotencyKey: `concurrent-${index}` }
      ))
    );
    assert.equal(new Set(handles.map((handle) => handle.workspace.id)).size, 6);
    assert.equal(new Set(handles.map((handle) => handle.workspace.root)).size, 6);
    const worktreeList = git(f.root, ['worktree', 'list', '--porcelain']);
    for (const handle of handles) assert.ok(worktreeList.includes(handle.workspace.root));
    await assert.rejects(
      () => manager.createWorkspace({ ...owner, requestId: 'overflow' }, { idempotencyKey: 'overflow' }),
      /worktree limit reached/i
    );
  } finally {
    await f.cleanup();
  }
});

test('preserves configured monorepo subdirectory scope', async () => {
  const f = await fixture({ subdirectory: true });
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(context(), { idempotencyKey: 'subdir' });
    assert.equal(path.basename(handle.workspace.root), 'app');
    assert.equal(path.basename(path.dirname(handle.workspace.root)), 'packages');
    assert.equal(await fs.readFile(path.join(handle.workspace.root, 'app.txt'), 'utf8'), 'app committed\n');
    await assert.rejects(fs.stat(path.join(handle.workspace.root, 'shared.txt')), { code: 'ENOENT' });
  } finally {
    await f.cleanup();
  }
});

test('removes only clean worktrees and preserves their branches', async () => {
  const f = await fixture();
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const owner = context();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'clean-remove' });
    await manager.removeWorkspace(owner, handle.workspace.id);
    await assert.rejects(fs.stat(handle.workspace.root), { code: 'ENOENT' });
    assert.equal(git(f.root, ['show-ref', '--verify', `refs/heads/${handle.branch}`]).length > 0, true);
    assert.throws(() => manager.getWorkspace(owner, handle.workspace.id), /unknown workspace_id/i);
  } finally {
    await f.cleanup();
  }
});

test('does not deduplicate unrelated transports that reuse JSON-RPC request ids', async () => {
  const f = await fixture();
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const first = await manager.createWorkspace(context('principal-a', '1'));
    const second = await manager.createWorkspace(context('principal-a', '1'));
    assert.notEqual(first.workspace.id, second.workspace.id);
  } finally {
    await f.cleanup();
  }
});

test('serializes mutations within one workspace', async () => {
  const f = await fixture();
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const owner = context();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'mutation-lock' });
    let active = 0;
    let maxActive = 0;
    await Promise.all(Array.from({ length: 8 }, (_, index) => manager.execute(owner, handle.workspace.id, true, async () => {
      active += 1;
      maxActive = Math.max(maxActive, active);
      const file = path.join(handle.workspace.root, `mutation-${index}.txt`);
      await new Promise((resolve) => setTimeout(resolve, 5));
      await fs.writeFile(file, String(index), 'utf8');
      active -= 1;
    })));
    assert.equal(maxActive, 1);
  } finally {
    await f.cleanup();
  }
});

test('does not remove a worktree while a read operation is active', async () => {
  const f = await fixture();
  try {
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const owner = context();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'active-read' });
    let operationStarted;
    const started = new Promise((resolve) => { operationStarted = resolve; });
    let finishOperation;
    const finish = new Promise((resolve) => { finishOperation = resolve; });
    const activeRead = manager.execute(owner, handle.workspace.id, false, async () => {
      operationStarted();
      await finish;
    });
    await started;
    await assert.rejects(() => manager.removeWorkspace(owner, handle.workspace.id), /active operations/i);
    assert.equal(manager.getWorkspace(owner, handle.workspace.id).root, handle.workspace.root);
    finishOperation();
    await activeRead;
  } finally {
    await f.cleanup();
  }
});

test('reconciliation fails closed when a managed checkout disappears', async () => {
  const f = await fixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'missing-checkout' });
    await fs.rm(handle.workspace.root, { recursive: true, force: true });

    const restarted = new WorktreeManager(f.config);
    await restarted.initialize();
    assert.throws(() => restarted.getWorkspace(owner, handle.workspace.id), /state: orphaned/i);
  } finally {
    await f.cleanup();
  }
});

test('rejects managed storage that overlaps the source repository', async () => {
  const f = await fixture();
  try {
    const config = { ...f.config, worktreeRoot: path.join(f.root, '.managed-worktrees') };
    const manager = new WorktreeManager(config);
    await assert.rejects(() => manager.initialize(), /must not overlap/i);
  } finally {
    await f.cleanup();
  }
});

test('rejects a persisted lease whose paths are tampered', async () => {
  const f = await fixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'tampered-path' });
    const hash = createHash('sha256').update(handle.workspace.id).digest('hex');
    const leasePath = path.join(f.config.worktreeRoot, 'leases', `${hash}.json`);
    const lease = JSON.parse(await fs.readFile(leasePath, 'utf8'));
    lease.workspaceRoot = f.root;
    await fs.writeFile(leasePath, `${JSON.stringify(lease, null, 2)}\n`, 'utf8');

    const restarted = new WorktreeManager(f.config);
    await restarted.initialize();
    assert.throws(() => restarted.getWorkspace(owner, handle.workspace.id), /state: orphaned/i);
  } finally {
    await f.cleanup();
  }
});

async function multiProjectFixture() {
  const first = await fixture();
  const secondRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-worktree-repo-b-'));
  git(secondRoot, ['init', '-q']);
  git(secondRoot, ['config', 'user.email', 'worktree-test@example.com']);
  git(secondRoot, ['config', 'user.name', 'Worktree Test']);
  await fs.writeFile(path.join(secondRoot, 'shared.txt'), 'second project\n', 'utf8');
  git(secondRoot, ['add', '.']);
  git(secondRoot, ['commit', '-q', '-m', 'initial']);
  const config = {
    ...first.config,
    defaultRoot: first.root,
    allowedRoots: [first.root, secondRoot],
    projects: [
      { id: 'alpha', label: 'Alpha', root: first.root },
      { id: 'beta', label: 'Beta', root: secondRoot }
    ],
    defaultProjectId: 'alpha'
  };
  const cleanup = async () => {
    await first.cleanup();
    await fs.rm(secondRoot, { recursive: true, force: true });
  };
  return { ...first, secondRoot, config, cleanup };
}

test('selects named projects once and routes later calls only by workspace handle', async () => {
  const f = await multiProjectFixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    assert.deepEqual(manager.listProjects().map((project) => project.id), ['alpha', 'beta']);
    await assert.rejects(() => manager.createWorkspace(owner, { idempotencyKey: 'missing-project' }), /project_id is required/i);

    const alpha = await manager.createWorkspace(owner, { projectId: 'alpha', idempotencyKey: 'same-task' });
    const beta = await manager.createWorkspace(owner, { projectId: 'beta', idempotencyKey: 'same-task' });
    assert.notEqual(alpha.workspace.id, beta.workspace.id);
    assert.equal(alpha.projectId, 'alpha');
    assert.equal(beta.projectId, 'beta');
    assert.equal(alpha.workspace.projectId, 'alpha');
    assert.equal(beta.workspace.projectId, 'beta');
    assert.equal(await fs.readFile(path.join(alpha.workspace.root, 'shared.txt'), 'utf8'), 'committed\n');
    assert.equal(await fs.readFile(path.join(beta.workspace.root, 'shared.txt'), 'utf8'), 'second project\n');
    assert.equal(manager.getWorkspace(owner, beta.workspace.id).projectId, 'beta');

    const reordered = new WorktreeManager({ ...f.config, projects: [...f.config.projects].reverse() });
    await reordered.initialize();
    assert.equal(reordered.getWorkspace(owner, alpha.workspace.id).projectId, 'alpha');
    assert.equal(reordered.getWorkspace(owner, beta.workspace.id).projectId, 'beta');
  } finally {
    await f.cleanup();
  }
});

test('enforces project quotas independently from the global retained limit', async () => {
  const f = await multiProjectFixture();
  try {
    const config = {
      ...f.config,
      maxWorktrees: 3,
      projects: [
        { ...f.config.projects[0], maxWorktrees: 1 },
        { ...f.config.projects[1], maxWorktrees: 3 }
      ]
    };
    const manager = new WorktreeManager(config);
    await manager.initialize();
    const owner = context();
    await manager.createWorkspace(owner, { projectId: 'alpha', idempotencyKey: 'alpha-1' });
    await assert.rejects(
      () => manager.createWorkspace(owner, { projectId: 'alpha', idempotencyKey: 'alpha-2' }),
      /limit reached for project alpha/i
    );
    await manager.createWorkspace(owner, { projectId: 'beta', idempotencyKey: 'beta-1' });
    await manager.createWorkspace(owner, { projectId: 'beta', idempotencyKey: 'beta-2' });
    await assert.rejects(
      () => manager.createWorkspace(owner, { projectId: 'beta', idempotencyKey: 'beta-3' }),
      /global worktree limit reached/i
    );
  } finally {
    await f.cleanup();
  }
});

test('supports concurrent project scopes that share one Git common directory', async () => {
  const f = await fixture();
  try {
    const appRoot = path.join(f.root, 'packages', 'app');
    const config = {
      ...f.config,
      projects: [
        { id: 'monorepo', label: 'Monorepo', root: f.root },
        { id: 'app', label: 'App scope', root: appRoot }
      ],
      defaultProjectId: 'monorepo',
      allowedRoots: [f.root, appRoot]
    };
    const manager = new WorktreeManager(config);
    await manager.initialize();
    const owner = context();
    const [whole, app] = await Promise.all([
      manager.createWorkspace(owner, { projectId: 'monorepo', idempotencyKey: 'whole' }),
      manager.createWorkspace(owner, { projectId: 'app', idempotencyKey: 'app' })
    ]);
    assert.equal(whole.workspace.projectId, 'monorepo');
    assert.equal(app.workspace.projectId, 'app');
    assert.equal(await fs.readFile(path.join(app.workspace.root, 'app.txt'), 'utf8'), 'app committed\n');
    assert.notEqual(path.dirname(app.workspace.root), path.dirname(whole.workspace.root));
  } finally {
    await f.cleanup();
  }
});

test('migrates legacy single-project leases without changing workspace handles', async () => {
  const f = await fixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(owner, { idempotencyKey: 'legacy' });
    const hash = createHash('sha256').update(handle.workspace.id).digest('hex');
    const leasePath = path.join(f.config.worktreeRoot, 'leases', `${hash}.json`);
    const lease = JSON.parse(await fs.readFile(leasePath, 'utf8'));
    lease.version = 1;
    delete lease.projectId;
    await fs.writeFile(leasePath, `${JSON.stringify(lease, null, 2)}\n`, 'utf8');

    const restarted = new WorktreeManager(f.config);
    await restarted.initialize();
    assert.equal(restarted.getWorkspace(owner, handle.workspace.id).projectId, 'default');
    const migrated = JSON.parse(await fs.readFile(leasePath, 'utf8'));
    assert.equal(migrated.version, 2);
    assert.equal(migrated.projectId, 'default');
  } finally {
    await f.cleanup();
  }
});

test('fails closed when a durable lease is rebound to another configured project', async () => {
  const f = await multiProjectFixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(owner, { projectId: 'alpha', idempotencyKey: 'project-tamper' });
    const hash = createHash('sha256').update(handle.workspace.id).digest('hex');
    const leasePath = path.join(f.config.worktreeRoot, 'leases', `${hash}.json`);
    const lease = JSON.parse(await fs.readFile(leasePath, 'utf8'));
    lease.projectId = 'beta';
    await fs.writeFile(leasePath, `${JSON.stringify(lease, null, 2)}\n`, 'utf8');

    const restarted = new WorktreeManager(f.config);
    await restarted.initialize();
    assert.throws(() => restarted.getWorkspace(owner, handle.workspace.id), /unknown workspace_id/i);
  } finally {
    await f.cleanup();
  }
});

test('keeps removed-project leases durable but unavailable until the project returns', async () => {
  const f = await multiProjectFixture();
  try {
    const owner = context();
    const manager = new WorktreeManager(f.config);
    await manager.initialize();
    const handle = await manager.createWorkspace(owner, { projectId: 'beta', idempotencyKey: 'temporarily-removed' });

    const withoutBeta = new WorktreeManager({
      ...f.config,
      projects: [f.config.projects[0]],
      allowedRoots: [f.root]
    });
    await withoutBeta.initialize();
    assert.throws(() => withoutBeta.getWorkspace(owner, handle.workspace.id), /unknown workspace_id/i);

    const restored = new WorktreeManager(f.config);
    await restored.initialize();
    assert.equal(restored.getWorkspace(owner, handle.workspace.id).projectId, 'beta');
  } finally {
    await f.cleanup();
  }
});
