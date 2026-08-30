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
      args: { project_id: 'default', workspace_id: 'ws_activity', path: 'tracked.txt', content: 'after\n' },
      result: { structuredContent: { path: 'tracked.txt', changed: true, bytes: 6, additions: 1, deletions: 1 } },
      startedAtMs: started,
      finishedAtMs: started + 20,
      mutating: true,
      before: evidence('default', 'ws_activity', 'tracked.txt'),
      after: evidence('default', 'ws_activity', 'tracked.txt')
    });
    const second = journal.record({
      toolName: 'read',
      args: { project_id: 'default', workspace_id: 'ws_activity', path: 'tracked.txt', start_line: 1, end_line: 40 },
      result: { structuredContent: { path: 'tracked.txt', bytes: 35, truncated: false } },
      startedAtMs: started + 30,
      finishedAtMs: started + 40,
      mutating: false
    });
    const third = journal.record({
      toolName: 'edit',
      args: {
        project_id: 'default',
        workspace_id: 'ws_activity',
        path: 'tracked.txt',
        edit_tag: 'A1B2',
        edits: [{ op: 'replace', start_line: 1, content: 'after <script>alert(1)</script>' }]
      },
      result: {
        structuredContent: {
          path: 'tracked.txt',
          mode: 'tagged_lines',
          changed: true,
          bytes: 35,
          additions: 1,
          deletions: 1,
          edits_applied: 1
        }
      },
      startedAtMs: started + 50,
      finishedAtMs: started + 70,
      mutating: true,
      before: {
        ...evidence('default', 'ws_activity', 'tracked.txt'),
        paths: [{ path: 'tracked.txt', exists: true, kind: 'file', size: 7 }]
      },
      after: {
        ...evidence('default', 'ws_activity', 'tracked.txt'),
        paths: [{ path: 'tracked.txt', exists: true, kind: 'file', size: 35 }]
      }
    });
    const fourth = journal.record({
      toolName: 'bash',
      args: {
        project_id: 'default',
        workspace_id: 'ws_activity',
        command: 'npm run verify -- --report private-command-argument',
        cwd: '.',
        timeout_ms: 120000
      },
      result: {
        structuredContent: {
          exitCode: 0,
          durationMs: 32,
          stdout: 'verification passed\n',
          stderr: '',
          timedOut: false
        }
      },
      startedAtMs: started + 80,
      finishedAtMs: started + 112,
      mutating: true
    });
    assert.equal(first.recorded, true);
    assert.equal(second.recorded, true);
    assert.equal(third.recorded, true);
    assert.equal(fourth.recorded, true);
    const rawJournal = await fs.readFile(journalPath, 'utf8');
    assert.match(rawJournal, /"command_label":"npm run verify"/);
    assert.doesNotMatch(rawJournal, /private-command-argument/);
    assert.doesNotMatch(rawJournal, /--report/);

    const snapshot = collectActivityDashboard(config, journal);
    assert.equal(snapshot.projects.length, 1);
    const project = snapshot.projects[0];
    assert.equal(project.id, 'default');
    assert.deepEqual(project.actions.map((action) => action.toolName), ['bash', 'edit', 'read', 'write']);
    assert.equal(project.actions[0].headline, 'npm run verify · exit 0');
    assert.equal(project.actions[0].requestFields.find((field) => field.key === 'command_label')?.value, 'npm run verify');
    assert.match(project.actions[1].headline, /tracked\.txt · \+1 −1 · 1 operation/);
    assert.deepEqual(project.actions[1].pathEvidence, [{ label: 'tracked.txt', value: 'file · 7 B → file · 35 B' }]);
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
    assert.match(html, /<details class="action-card"/);
    assert.match(html, /npm run verify · exit 0/);
    assert.match(html, /Safe command label/);
    assert.match(html, /Lines added/);
    assert.match(html, /file · 7 B → file · 35 B/);
    assert.match(html, /raw shell command text is not retained/i);
    assert.doesNotMatch(html, /private-command-argument/);
    assert.doesNotMatch(html, /--report/);
    assert.match(html, /Raw shell command text/);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});

test('activity dashboard renders tagged edits and serial edit-plus-verification batches', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-batch-project-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-batch-home-'));
  try {
    const journalPath = path.join(home, 'audit', 'tool-calls.jsonl');
    const config = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-log', journalPath
    ]);
    const journal = new AuditJournal(config);
    const started = Date.now() - 100;
    const anchored = journal.record({
      toolName: 'edit',
      args: {
        project_id: 'default',
        workspace_id: 'ws_activity_batch',
        path: 'tracked.txt',
        edit_tag: 'A1B2',
        edits: [
          { op: 'replace', start_line: 1, content: 'PRIVATE_EDIT_BODY' },
          { op: 'insert_after', line: 4, content: 'PRIVATE_INSERT_BODY' },
          { op: 'delete', start_line: 9 }
        ]
      },
      result: {
        structuredContent: {
          path: 'tracked.txt',
          mode: 'tagged_lines',
          changed: true,
          edits_applied: 3,
          bytes: 40,
          additions: 2,
          deletions: 1
        }
      },
      startedAtMs: started,
      finishedAtMs: started + 10,
      mutating: true,
      before: evidence('default', 'ws_activity_batch', 'tracked.txt'),
      after: evidence('default', 'ws_activity_batch', 'tracked.txt')
    });
    const batch = journal.record({
      toolName: 'batch',
      args: {
        project_id: 'default',
        workspace_id: 'ws_activity_batch',
        mode: 'serial',
        operations: [
          { id: 'before', tool: 'read', args: { path: 'tracked.txt' } },
          {
            id: 'change',
            tool: 'edit',
            args: {
              path: 'tracked.txt',
              edit_tag: 'A1B2',
              edits: [{ op: 'replace', start_line: 1, content: 'b' }]
            }
          },
          { id: 'verify', tool: 'bash', args: { command: 'npm test' } },
          { id: 'after', tool: 'read', args: { path: 'tracked.txt' } }
        ]
      },
      result: {
        structuredContent: {
          operation_count: 4,
          succeeded_count: 4,
          failed_count: 0,
          skipped_count: 0,
          succeeded: true,
          changed_paths: ['tracked.txt']
        }
      },
      startedAtMs: started + 20,
      finishedAtMs: started + 40,
      mutating: true,
      before: evidence('default', 'ws_activity_batch', 'tracked.txt'),
      after: evidence('default', 'ws_activity_batch', 'tracked.txt')
    });
    assert.equal(anchored.recorded, true);
    assert.equal(batch.recorded, true);

    const project = collectActivityDashboard(config, journal).projects[0];
    assert.deepEqual(project.actions.map((action) => action.toolName), ['batch', 'edit']);
    assert.equal(project.actions[0].headline, '4 operations · completed · 1 changed path');
    assert.match(project.actions[1].headline, /tracked\.txt · \+2 −1 · 3 operations/);
    assert.equal(project.actions[1].requestFields.find((field) => field.key === 'edit_mode')?.value, 'tagged_lines');
    assert.equal(project.actions[1].requestFields.find((field) => field.key === 'edit_operations')?.value, '3');
    assert.equal(project.actions[0].requestFields.find((field) => field.key === 'file_mutation_count')?.value, '1');
    assert.equal(project.actions[0].requestFields.find((field) => field.key === 'verification_command_count')?.value, '1');

    const html = renderActivityDashboardPage(collectActivityDashboard(config, journal));
    assert.match(html, /4 operations · completed · 1 changed path/);
    assert.match(html, /Edit operations/);
    const raw = await fs.readFile(journalPath, 'utf8');
    assert.doesNotMatch(raw, /PRIVATE_EDIT_BODY|PRIVATE_INSERT_BODY/);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});

test('activity dashboard summarizes multi-workspace open operations', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-open-projects-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-open-home-'));
  try {
    const journalPath = path.join(home, 'audit', 'tool-calls.jsonl');
    const config = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-log', journalPath
    ]);
    const journal = new AuditJournal(config);
    const recorded = journal.record({
      toolName: 'open_workspace',
      args: {
        project_ids: ['default', 'api', 'default'],
        include_tree: false
      },
      result: {
        structuredContent: {
          selected_project_id: 'default',
          selected_workspace_id: 'ws_default',
          primary_workspace_id: 'ws_default',
          include_tree: false,
          count: 2,
          already_open_count: 1,
          workspaces: [
            { project_id: 'default', workspace_id: 'ws_default' },
            { project_id: 'api', workspace_id: 'ws_api' }
          ]
        }
      },
      startedAtMs: Date.now() - 20,
      finishedAtMs: Date.now(),
      mutating: false
    });
    assert.equal(recorded.recorded, true);

    const project = collectActivityDashboard(config, journal).projects[0];
    assert.equal(project.actions.length, 1);
    assert.equal(project.actions[0].headline, '2 workspaces · 1 workspace reused');
    assert.equal(project.actions[0].requestFields.find((field) => field.key === 'project_ids_count')?.value, '2');
    assert.equal(project.actions[0].resultFields.find((field) => field.key === 'workspaces_count')?.value, '2');
    assert.equal(project.actions[0].resultFields.find((field) => field.key === 'already_open_count')?.value, '1');

    const html = renderActivityDashboardPage(collectActivityDashboard(config, journal));
    assert.match(html, /2 workspaces/);
    assert.match(html, /1 workspace reused/);
    assert.match(html, /Projects requested/);
    const raw = await fs.readFile(journalPath, 'utf8');
    assert.doesNotMatch(raw, /"project_ids":/);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
    await fs.rm(home, { recursive: true, force: true });
  }
});

test('Bash command labels reveal only a bounded non-sensitive command shape', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-command-label-project-'));
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-command-label-home-'));
  try {
    const journalPath = path.join(home, 'audit', 'tool-calls.jsonl');
    const config = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-log', journalPath
    ]);
    const journal = new AuditJournal(config);
    const commands = [
      'npm run verify -- --report private-command-argument',
      'npm run credentials-report -- --format private-format-argument',
      'npm run verify && echo compound-private-argument',
      'node -e "console.log(\'private-node-argument\')"'
    ];
    for (let index = 0; index < commands.length; index += 1) {
      const recorded = journal.record({
        toolName: 'bash',
        args: { project_id: 'default', command: commands[index] },
        result: { structuredContent: { exitCode: 0, stdout: '', stderr: '', timedOut: false } },
        startedAtMs: Date.now() + index,
        finishedAtMs: Date.now() + index + 1,
        mutating: true
      });
      assert.equal(recorded.recorded, true);
    }

    const actions = journal.list({ limit: 10 }).actions;
    assert.deepEqual(actions.map((action) => action.request_metadata.command_label), [
      'npm run verify',
      'npm run',
      'npm',
      'node'
    ]);
    const raw = await fs.readFile(journalPath, 'utf8');
    for (const hidden of [
      'private-command-argument',
      'credentials-report',
      'private-format-argument',
      'compound-private-argument',
      'private-node-argument'
    ]) {
      assert.doesNotMatch(raw, new RegExp(hidden));
    }
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
