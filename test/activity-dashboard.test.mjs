import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { AuditJournal } from '../dist/audit.js';
import {
  collectActivityDashboard,
  collectProjectGit,
  renderActivityDashboardPage
} from '../dist/activityDashboard.js';
import { loadConfig } from '../dist/config.js';

function git(root, args) {
  const result = spawnSync('git', args, {
    cwd: root,
    encoding: 'utf8',
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', LC_ALL: 'C' }
  });
  assert.equal(result.status, 0, result.stderr || result.stdout || `git ${args.join(' ')} failed`);
  return result.stdout.trim();
}

function evidence(projectId, workspaceId, target) {
  return {
    project_id: projectId,
    workspace_id: workspaceId,
    targets: [target],
    paths: []
  };
}

test('activity dashboard groups recent actions and renders a safety-filtered HEAD diff', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-project-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-home-'));
  try {
    git(root, ['init']);
    git(root, ['config', 'user.email', 'activity@example.test']);
    git(root, ['config', 'user.name', 'Activity Test']);
    await fs.writeFile(path.join(root, 'tracked.txt'), 'before\n');
    await fs.writeFile(path.join(root, '.env'), 'HIDDEN_MARKER=before\n');
    git(root, ['add', 'tracked.txt', '.env']);
    git(root, ['commit', '-m', 'initial']);

    await fs.writeFile(path.join(root, 'tracked.txt'), 'after <script>alert(1)</script>\n');
    await fs.writeFile(path.join(root, '.env'), 'HIDDEN_MARKER=private-diff-line\n');
    await fs.writeFile(path.join(root, 'untracked.txt'), 'untracked body must stay out of the page\n');

    const journalPath = path.join(home, 'audit', 'tool-calls.jsonl');
    const config = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-log', journalPath
    ]);
    const journal = new AuditJournal(config);
    const started = Date.now() - 100;
    const first = journal.record({
      toolName: 'write',
      args: { project_id: 'default', workspace_id: 'ws_activity', path: 'tracked.txt' },
      startedAtMs: started,
      finishedAtMs: started + 20,
      mutating: true,
      before: evidence('default', 'ws_activity', 'tracked.txt'),
      after: evidence('default', 'ws_activity', 'tracked.txt')
    });
    const second = journal.record({
      toolName: 'read',
      args: { project_id: 'default', workspace_id: 'ws_activity', path: 'tracked.txt' },
      result: { structuredContent: { path: 'tracked.txt', bytes: 35 } },
      startedAtMs: started + 30,
      finishedAtMs: started + 40,
      mutating: false
    });
    assert.equal(first.recorded, true);
    assert.equal(second.recorded, true);

    const snapshot = collectActivityDashboard(config, journal);
    assert.equal(snapshot.projects.length, 1);
    const project = snapshot.projects[0];
    assert.equal(project.id, 'default');
    assert.deepEqual(project.actions.map((action) => action.toolName), ['read', 'write']);
    assert.equal(project.git.available, true);
    assert.equal(project.git.dirty, true);
    assert.deepEqual(project.git.trackedChangedPaths, ['tracked.txt']);
    assert.deepEqual(project.git.untrackedPaths, ['untracked.txt']);
    assert.equal(project.git.hiddenPathCount, 1);
    assert.match(project.git.diff, /after <script>alert\(1\)<\/script>/);
    assert.doesNotMatch(project.git.diff, /private-diff-line/);
    assert.doesNotMatch(project.git.diff, /untracked body/);

    const html = renderActivityDashboardPage(snapshot);
    assert.match(html, /Activity & changes/);
    assert.match(html, /after &lt;script&gt;alert\(1\)&lt;\/script&gt;/);
    assert.doesNotMatch(html, /<script>alert\(1\)<\/script>/);
    assert.match(html, /Untracked files \(contents not rendered\)/);
    assert.match(html, /Raw shell command text/);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});

test('activity dashboard reports non-Git projects without failing the page', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-nongit-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-nongit-home-'));
  try {
    const config = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-log', path.join(home, 'audit', 'tool-calls.jsonl')
    ]);
    const state = collectProjectGit(config, config.projects[0]);
    assert.equal(state.available, false);
    assert.equal(state.message, 'Not a Git working tree.');

    const snapshot = collectActivityDashboard(config, new AuditJournal(config));
    const html = renderActivityDashboardPage(snapshot);
    assert.match(html, /Not a Git working tree\./);
    assert.match(html, /No retained activity for this project\./);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});
