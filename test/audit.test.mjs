import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { ACTION_NAMESPACE, ACTION_SCHEMA_VERSION, AuditJournal } from '../dist/audit.js';
import { loadConfig } from '../dist/config.js';
import { PathGuard } from '../dist/guard.js';
import { createCodexProServer } from '../dist/server.js';

async function fixture(options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-audit-'));
  const repo = path.join(root, 'repo');
  const log = options.logInsideRepo
    ? path.join(repo, 'state', 'action-journal.jsonl')
    : path.join(root, 'state', 'action-journal.jsonl');
  await fs.mkdir(repo);

  if (options.git) {
    execFileSync('git', ['init', '-q', '-b', 'main'], { cwd: repo });
    await fs.writeFile(path.join(repo, 'tracked.txt'), 'initial\n', 'utf8');
    execFileSync('git', ['add', 'tracked.txt'], { cwd: repo });
    execFileSync(
      'git',
      ['-c', 'user.name=CodexPro Test', '-c', 'user.email=codexpro@example.invalid', 'commit', '-q', '-m', 'initial'],
      { cwd: repo }
    );
  }

  const args = [
    '--root', repo,
    '--audit', 'metadata',
    '--audit-log', log,
    '--bash', options.bash ?? 'full'
  ];
  if (options.auditMaxBytes !== undefined) args.push('--audit-max-bytes', String(options.auditMaxBytes));
  if (options.auditRetainActions !== undefined) args.push('--audit-retain-actions', String(options.auditRetainActions));
  const config = loadConfig(args);
  return {
    root,
    repo,
    log,
    config,
    cleanup: () => fs.rm(root, { recursive: true, force: true })
  };
}

function context(requestId, transportSessionId = 'transport_test') {
  return {
    principalId: 'principal_test',
    requestId,
    transportSessionId,
    signal: new AbortController().signal
  };
}

function record(journal, input) {
  return journal.record({
    startedAtMs: input.startedAtMs ?? 1_000,
    finishedAtMs: input.finishedAtMs ?? 1_025,
    mutating: input.mutating ?? false,
    context: input.context ?? context(`request_${input.toolName}`),
    ...input
  });
}

async function connect(config) {
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-audit-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);
  return {
    server,
    client,
    close: async () => {
      await client.close();
      await server.close();
    }
  };
}

test('codexpro.action.v1 stores redacted metadata, stable source references, and no payload text', async () => {
  const f = await fixture();
  try {
    const journal = new AuditJournal(f.config);
    record(journal, {
      toolName: 'write',
      args: {
        workspace_id: 'ws_test',
        path: 'src/example.ts',
        content: 'TOP_SECRET_BODY',
        overwrite: true,
        expected_sha256: 'a'.repeat(64)
      },
      result: {
        structuredContent: {
          workspace_id: 'ws_test',
          root: f.repo,
          path: 'src/example.ts',
          changed: true,
          bytes: 15
        }
      },
      mutating: true,
      context: context('request_write'),
      startedAtMs: 1_000,
      finishedAtMs: 1_025
    });
    record(journal, {
      toolName: 'bash',
      args: {
        workspace_id: 'ws_test',
        cwd: '.',
        command: 'API_TOKEN=TOP_SECRET_TOKEN node scripts/check.mjs --password TOP_SECRET_PASSWORD'
      },
      result: {
        structuredContent: {
          workspace_id: 'ws_test',
          root: f.repo,
          exitCode: 0,
          durationMs: 50,
          stdout: 'TOP_SECRET_OUTPUT',
          stderr: ''
        }
      },
      mutating: true,
      context: context('request_bash'),
      startedAtMs: 2_000,
      finishedAtMs: 2_050
    });
    record(journal, {
      toolName: 'search',
      args: {
        workspace_id: 'ws_test',
        path: 'src',
        query: 'TOP_SECRET_SEARCH_QUERY'
      },
      result: { structuredContent: { workspace_id: 'ws_test', root: f.repo, matches: [{ path: 'src/example.ts' }] } },
      context: context('request_search'),
      startedAtMs: 3_000,
      finishedAtMs: 3_010
    });

    const raw = await fs.readFile(f.log, 'utf8');
    for (const forbidden of [
      'TOP_SECRET_BODY',
      'TOP_SECRET_TOKEN',
      'TOP_SECRET_PASSWORD',
      'TOP_SECRET_OUTPUT',
      'TOP_SECRET_SEARCH_QUERY',
      'scripts/check.mjs',
      'principal_test',
      'request_write',
      f.repo
    ]) {
      assert.equal(raw.includes(forbidden), false, `journal leaked ${forbidden}`);
    }

    const actions = raw.trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(actions.length, 3);
    assert.deepEqual(actions.map((action) => action.sequence), [1, 2, 3]);
    assert.equal(actions[0].schema_version, ACTION_SCHEMA_VERSION);
    assert.equal(actions[0].namespace, ACTION_NAMESPACE);
    assert.equal(actions[0].tool_name, 'write');
    assert.equal(actions[0].operation, 'file.write');
    assert.equal(actions[0].operation_class, 'write');
    assert.equal(actions[0].status, 'succeeded');
    assert.equal(actions[0].request_metadata.path, 'src/example.ts');
    assert.equal(actions[0].request_metadata.content_bytes, 15);
    assert.equal(actions[0].request_metadata.expected_sha256_supplied, true);
    assert.match(actions[0].actor_ref, /^actor_[0-9a-f]{32}$/);
    assert.match(actions[0].request_ref, /^request_[0-9a-f]{32}$/);
    assert.match(actions[0].request_fingerprint, /^[0-9a-f]{64}$/);
    assert.match(actions[0].result_ref, /^codexpro:\/\/actions\/cpa_[0-9a-f]{32}$/);
    assert.equal(actions[0].duration_ms, 25);
    assert.equal(actions[1].request_metadata.command_name, 'node');
    assert.match(actions[1].request_metadata.command_digest, /^[0-9a-f]{64}$/);
    assert.equal(actions[1].result_metadata.stdout_bytes, 17);
    assert.match(actions[2].request_metadata.query_digest, /^[0-9a-f]{64}$/);
    assert.equal(actions[2].request_metadata.query_bytes, Buffer.byteLength('TOP_SECRET_SEARCH_QUERY'));

    if (process.platform !== 'win32') {
      assert.equal((await fs.stat(f.log)).mode & 0o777, 0o600);
      assert.equal((await fs.stat(path.dirname(f.log))).mode & 0o777, 0o700);
    }
  } finally {
    await f.cleanup();
  }
});

test('blocked and cancelled outcomes are represented without leaking refusal or request text', async () => {
  const f = await fixture();
  try {
    const journal = new AuditJournal(f.config);
    record(journal, {
      toolName: 'bash',
      args: { workspace_id: 'ws_test', command: 'TOP_SECRET_BLOCKED_COMMAND' },
      error: new Error('Safe bash allowlist blocked TOP_SECRET_POLICY_DETAIL'),
      mutating: true,
      context: context('request_blocked')
    });

    const controller = new AbortController();
    controller.abort();
    record(journal, {
      toolName: 'write',
      args: { workspace_id: 'ws_test', path: 'cancelled.txt', content: 'TOP_SECRET_CANCELLED_BODY' },
      error: new Error('TOP_SECRET_CANCELLED_ERROR'),
      mutating: true,
      context: { ...context('request_cancelled'), signal: controller.signal }
    });

    const actions = journal.list({ afterSequence: 0, limit: 10 }).actions;
    assert.deepEqual(actions.map((action) => action.status), ['blocked', 'cancelled']);
    assert.deepEqual(actions.map((action) => action.error_code), ['policy_blocked', 'cancelled']);
    const raw = await fs.readFile(f.log, 'utf8');
    for (const forbidden of [
      'TOP_SECRET_BLOCKED_COMMAND',
      'TOP_SECRET_POLICY_DETAIL',
      'TOP_SECRET_CANCELLED_BODY',
      'TOP_SECRET_CANCELLED_ERROR'
    ]) {
      assert.equal(raw.includes(forbidden), false, `outcome journal leaked ${forbidden}`);
    }
  } finally {
    await f.cleanup();
  }
});

test('sequence cursor, get, restart recovery, request dedupe, transport isolation, and gaps are durable', async () => {
  const f = await fixture();
  try {
    const journal = new AuditJournal(f.config);
    for (let index = 0; index < 5; index += 1) {
      record(journal, {
        toolName: index % 2 === 0 ? 'read' : 'write',
        args: { workspace_id: 'ws_test', path: `src/${index}.txt`, content: `body-${index}` },
        result: index === 3
          ? undefined
          : { structuredContent: { workspace_id: 'ws_test', path: `src/${index}.txt` } },
        error: index === 3 ? new Error('controlled failure') : undefined,
        mutating: index % 2 === 1,
        context: context(`request_${index}`),
        startedAtMs: 10_000 + index,
        finishedAtMs: 10_001 + index
      });
    }

    const tail = journal.list({ limit: 2 });
    assert.deepEqual(tail.actions.map((action) => action.sequence), [4, 5]);
    assert.equal(tail.next_sequence, 5);
    assert.equal(tail.latest_sequence, 5);
    assert.equal(tail.has_more, false);

    const first = journal.list({ afterSequence: 0, limit: 2 });
    assert.deepEqual(first.actions.map((action) => action.sequence), [1, 2]);
    assert.equal(first.next_sequence, 2);
    assert.equal(first.has_more, true);
    const second = journal.list({ afterSequence: first.next_sequence, limit: 10 });
    assert.deepEqual(second.actions.map((action) => action.sequence), [3, 4, 5]);
    assert.equal(second.next_sequence, 5);
    assert.equal(second.has_more, false);

    const failures = journal.list({ afterSequence: 0, limit: 10, status: 'failed', mutatingOnly: true });
    assert.deepEqual(failures.actions.map((action) => action.sequence), [4]);
    const writes = journal.list({ afterSequence: 0, limit: 10, toolName: 'write', workspaceId: 'ws_test' });
    assert.deepEqual(writes.actions.map((action) => action.sequence), [2, 4]);

    const action = journal.get(first.actions[0].action_id);
    assert.equal(action?.sequence, 1);
    assert.equal(action?.result_ref, `codexpro://actions/${action.action_id}`);
    assert.deepEqual(journal.status(), {
      enabled: true,
      mode: 'metadata',
      namespace: ACTION_NAMESPACE,
      schema_version: ACTION_SCHEMA_VERSION,
      storage_format: 'jsonl',
      journal_ref: 'codexpro://actions',
      retained_from_sequence: 1,
      latest_sequence: 5,
      next_sequence: 6,
      action_count: 5,
      malformed_records: 0,
      gap_detected: false,
      storage_bytes: (await fs.stat(f.log)).size,
      deduplicated_requests: 0,
      retention: {
        max_bytes: 64 * 1024 * 1024,
        retain_actions: 50_000,
        rotation_count: 0,
        dropped_through_sequence: 0
      }
    });

    const restarted = new AuditJournal(f.config);
    assert.equal(restarted.status().latest_sequence, 5);
    const duplicate = record(restarted, {
      toolName: 'read',
      args: { workspace_id: 'ws_test', path: 'different-request-body.txt' },
      result: { structuredContent: { workspace_id: 'ws_test', path: 'different-request-body.txt' } },
      context: context('request_4')
    });
    assert.equal(duplicate.duplicate, true);
    assert.equal(duplicate.recorded, false);
    assert.equal(duplicate.sequence, 5);
    assert.equal(restarted.status().action_count, 5);
    assert.equal(restarted.status().deduplicated_requests, 1);

    const otherTransport = record(restarted, {
      toolName: 'read',
      args: { workspace_id: 'ws_test', path: 'transport-isolated.txt' },
      result: { structuredContent: { workspace_id: 'ws_test', path: 'transport-isolated.txt' } },
      context: context('request_4', 'transport_other')
    });
    assert.equal(otherTransport.duplicate, false);
    assert.equal(otherTransport.sequence, 6);

    const lines = (await fs.readFile(f.log, 'utf8')).trim().split('\n');
    await fs.writeFile(f.log, `${lines.slice(1).join('\n')}\n`, 'utf8');
    const truncated = new AuditJournal(f.config);
    const status = truncated.status();
    assert.equal(status.retained_from_sequence, 2);
    assert.equal(status.latest_sequence, 6);
    assert.equal(status.gap_detected, true);
    assert.throws(() => truncated.list({ afterSequence: 0 }), /gap detected.*forward cursor reads are disabled/i);
  } finally {
    await f.cleanup();
  }
});

test('the journal index preserves the source high-water mark across tail loss', async () => {
  const f = await fixture();
  try {
    const journal = new AuditJournal(f.config);
    const recorded = [];
    for (let index = 1; index <= 3; index += 1) {
      recorded.push(record(journal, {
        toolName: 'write',
        args: { workspace_id: 'ws_test', path: `high-water/${index}.txt`, content: `payload-${index}` },
        result: { structuredContent: { workspace_id: 'ws_test', path: `high-water/${index}.txt`, changed: true } },
        mutating: true,
        context: context(`high_water_request_${index}`)
      }));
    }
    assert.deepEqual(recorded.map((item) => item.sequence), [1, 2, 3]);

    const beforeDamageIndex = JSON.parse(await fs.readFile(`${f.log}.index.json`, 'utf8'));
    assert.equal(beforeDamageIndex.retained_from_sequence, 1);
    assert.equal(beforeDamageIndex.dropped_through_sequence, 0);
    assert.equal(beforeDamageIndex.latest_sequence, 3);
    assert.equal(beforeDamageIndex.rotation_count, 0);
    assert.ok(beforeDamageIndex.updated_at);

    const lines = (await fs.readFile(f.log, 'utf8')).trim().split('\n');
    await fs.writeFile(f.log, `${lines.slice(0, 2).join('\n')}\n`, 'utf8');

    const damaged = new AuditJournal(f.config);
    const damagedStatus = damaged.status();
    assert.equal(damagedStatus.retained_from_sequence, 1);
    assert.equal(damagedStatus.latest_sequence, 3);
    assert.equal(damagedStatus.next_sequence, 4);
    assert.equal(damagedStatus.action_count, 2);
    assert.equal(damagedStatus.gap_detected, true);
    assert.deepEqual(damaged.list({ limit: 10 }).actions.map((action) => action.sequence), [1, 2]);
    assert.throws(
      () => damaged.list({ afterSequence: 2, limit: 10 }),
      /gap detected.*forward cursor reads are disabled/i
    );

    const fourth = record(damaged, {
      toolName: 'write',
      args: { workspace_id: 'ws_test', path: 'high-water/4.txt', content: 'payload-4' },
      result: { structuredContent: { workspace_id: 'ws_test', path: 'high-water/4.txt', changed: true } },
      mutating: true,
      context: context('high_water_request_4')
    });
    assert.equal(fourth.sequence, 4);
    assert.equal(damaged.status().latest_sequence, 4);

    const persistedIndex = JSON.parse(await fs.readFile(`${f.log}.index.json`, 'utf8'));
    assert.equal(persistedIndex.latest_sequence, 4);
    assert.equal(persistedIndex.retained_from_sequence, 1);
    assert.equal(persistedIndex.dropped_through_sequence, 0);

    const restarted = new AuditJournal(f.config);
    const restartedStatus = restarted.status();
    assert.equal(restartedStatus.latest_sequence, 4);
    assert.equal(restartedStatus.next_sequence, 5);
    assert.equal(restartedStatus.gap_detected, true);
    assert.deepEqual(
      restarted.list({ limit: 10 }).actions.map((action) => action.sequence),
      [1, 2, 4]
    );
  } finally {
    await f.cleanup();
  }
});

test('retention preserves source sequences and makes expired consumer cursors explicit', async () => {
  const f = await fixture({ auditMaxBytes: 100_000, auditRetainActions: 3 });
  try {
    const journal = new AuditJournal(f.config);
    const recorded = [];
    for (let index = 1; index <= 6; index += 1) {
      recorded.push(record(journal, {
        toolName: 'write',
        args: { workspace_id: 'ws_test', path: `retained/${index}.txt`, content: `payload-${index}` },
        result: { structuredContent: { workspace_id: 'ws_test', path: `retained/${index}.txt`, changed: true } },
        mutating: true,
        context: context(`retention_request_${index}`),
        startedAtMs: 20_000 + index,
        finishedAtMs: 20_001 + index
      }));
    }

    assert.deepEqual(recorded.map((item) => item.sequence), [1, 2, 3, 4, 5, 6]);
    const status = journal.status();
    assert.equal(status.retained_from_sequence, 4);
    assert.equal(status.latest_sequence, 6);
    assert.equal(status.next_sequence, 7);
    assert.equal(status.action_count, 3);
    assert.equal(status.gap_detected, false);
    assert.ok(status.retention.rotation_count >= 1);
    assert.equal(status.retention.dropped_through_sequence, 3);
    assert.equal(status.retention.max_bytes, 100_000);
    assert.equal(status.retention.retain_actions, 3);
    assert.ok(status.retention.compacted_at);

    const retentionIndex = JSON.parse(await fs.readFile(`${f.log}.index.json`, 'utf8'));
    assert.equal(retentionIndex.retained_from_sequence, 4);
    assert.equal(retentionIndex.dropped_through_sequence, 3);
    assert.throws(
      () => journal.list({ afterSequence: 0 }),
      /expired because retention dropped actions through sequence 3.*earliest retained action sequence is 4/i
    );
    assert.deepEqual(
      journal.list({ afterSequence: 3, limit: 100 }).actions.map((action) => action.sequence),
      [4, 5, 6]
    );
    assert.equal(journal.get(recorded[0].action_id), undefined);

    const restarted = new AuditJournal(f.config);
    const restartedStatus = restarted.status();
    assert.equal(restartedStatus.retained_from_sequence, 4);
    assert.equal(restartedStatus.latest_sequence, 6);
    assert.equal(restartedStatus.gap_detected, false);
    const seventh = record(restarted, {
      toolName: 'read',
      args: { workspace_id: 'ws_test', path: 'retained/6.txt' },
      result: { structuredContent: { workspace_id: 'ws_test', path: 'retained/6.txt' } },
      context: context('retention_request_7')
    });
    assert.equal(seventh.sequence, 7);
    assert.equal(restarted.status().latest_sequence, 7);
  } finally {
    await f.cleanup();
  }
});

test('auditing is opt-in, invalid modes fail closed, and in-workspace journal paths are guarded', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-audit-config-'));
  try {
    const disabled = loadConfig(['--root', root, '--audit', 'off']);
    assert.equal(disabled.auditMode, 'off');
    assert.equal(new AuditJournal(disabled).status().enabled, false);
    assert.throws(() => loadConfig(['--root', root, '--audit', 'payloads']), /audit_mode must be off or metadata/i);
    const boundedRetention = loadConfig([
      '--root', root,
      '--audit', 'metadata',
      '--audit-max-bytes', '1',
      '--audit-retain-actions', '0'
    ]);
    assert.equal(boundedRetention.auditMaxBytes, 4096);
    assert.equal(boundedRetention.auditRetainActions, 1);

    const journalPath = path.join(root, 'state', 'action-journal.jsonl');
    const protectedConfig = loadConfig(['--root', root, '--audit', 'metadata', '--audit-log', journalPath]);
    const guard = new PathGuard(protectedConfig);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl'), true);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl.lock'), true);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl.index.json'), true);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl.index.json.tmp-1-example'), true);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl.compact-1-example'), true);
    assert.equal(guard.isBlockedRelativePath('state/action-journal.jsonl.backup-1-example'), true);
    assert.equal(guard.isBlockedRelativePath('state/ordinary.jsonl'), false);
    assert.throws(
      () => loadConfig(['--root', root, '--audit', 'metadata', '--audit-log', root]),
      /must name a file, not an allowed workspace root/i
    );
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('central dispatch records direct and supertool actions, outcomes, mutation evidence, and non-recursive activity reads', async () => {
  const f = await fixture({ git: true, bash: 'full' });
  const connection = await connect(f.config);
  try {
    const opened = await connection.client.callTool({ name: 'open_current_workspace', arguments: {} });
    assert.notEqual(opened.isError, true);
    const workspaceId = opened.structuredContent.workspace_id;

    const written = await connection.client.callTool({
      name: 'write',
      arguments: { workspace_id: workspaceId, path: 'notes.txt', content: 'alpha timeout marker\n' }
    });
    assert.notEqual(written.isError, true);

    const editRead = await connection.client.callTool({
      name: 'read',
      arguments: { workspace_id: workspaceId, path: 'notes.txt' }
    });
    assert.notEqual(editRead.isError, true);
    const editTag = editRead.structuredContent.edit_tag;

    const edited = await connection.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: workspaceId,
        path: 'notes.txt',
        edit_tag: editTag,
        edits: [{ op: 'replace', start_line: 1, content: 'beta timeout marker' }]
      }
    });
    assert.notEqual(edited.isError, true);

    const read = await connection.client.callTool({
      name: 'read',
      arguments: { workspace_id: workspaceId, path: 'notes.txt' }
    });
    assert.notEqual(read.isError, true);

    const failedEdit = await connection.client.callTool({
      name: 'edit',
      arguments: {
        workspace_id: workspaceId,
        path: 'notes.txt',
        edit_tag: editTag,
        edits: [{ op: 'replace', start_line: 1, content: 'unused' }]
      }
    });
    assert.equal(failedEdit.isError, true);

    const supertoolRead = await connection.client.callTool({
      name: 'codexpro',
      arguments: { action: 'read', args: { workspace_id: workspaceId, path: 'notes.txt' } }
    });
    assert.notEqual(supertoolRead.isError, true);

    const supertoolWrite = await connection.client.callTool({
      name: 'codexpro',
      arguments: { action: 'write', args: { workspace_id: workspaceId, path: 'supertool.txt', content: 'gamma\n' } }
    });
    assert.notEqual(supertoolWrite.isError, true);

    const failedCommand = await connection.client.callTool({
      name: 'bash',
      arguments: { workspace_id: workspaceId, command: 'node -e "process.exit(3)"' }
    });
    assert.notEqual(failedCommand.isError, true);
    assert.equal(failedCommand.structuredContent.exitCode, 3);

    const timedOutCommand = await connection.client.callTool({
      name: 'bash',
      arguments: {
        workspace_id: workspaceId,
        command: 'node -e "setTimeout(() => {}, 5000)"',
        timeout_ms: 1000
      }
    });
    assert.notEqual(timedOutCommand.isError, true);

    const timeoutTextCommand = await connection.client.callTool({
      name: 'bash',
      arguments: {
        workspace_id: workspaceId,
        command: 'node -e "console.log(\'timeout text is data\')"'
      }
    });
    assert.notEqual(timeoutTextCommand.isError, true);
    assert.equal(timeoutTextCommand.structuredContent.exitCode, 0);

    const beforeActivityReads = (await fs.stat(f.log)).size;
    const listed = await connection.client.callTool({
      name: 'activity_list',
      arguments: { after_sequence: 0, limit: 100 }
    });
    assert.notEqual(listed.isError, true);
    const actions = listed.structuredContent.actions;
    assert.equal(actions.length, 11);
    assert.deepEqual(actions.map((action) => action.sequence), [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]);
    assert.deepEqual(actions.map((action) => action.tool_name), [
      'open_current_workspace',
      'write',
      'read',
      'edit',
      'read',
      'edit',
      'read',
      'write',
      'bash',
      'bash',
      'bash'
    ]);

    const writeAction = actions.find((action) => action.tool_name === 'write');
    assert.equal(writeAction.status, 'succeeded');
    assert.equal(writeAction.operation_class, 'write');
    assert.equal(writeAction.project_id, 'default');
    assert.equal(writeAction.workspace_id, workspaceId);
    assert.deepEqual(writeAction.changed_paths, ['notes.txt']);
    assert.equal(writeAction.changed_path_count, 1);
    assert.equal(writeAction.git_before.dirty, false);
    assert.equal(writeAction.git_after.dirty, true);
    assert.ok(writeAction.path_evidence_before.some((item) => item.path === 'notes.txt' && item.exists === false));
    assert.ok(writeAction.path_evidence_after.some((item) => item.path === 'notes.txt' && item.exists === true));

    const editActions = actions.filter((action) => action.tool_name === 'edit');
    assert.deepEqual(editActions.map((action) => action.status), ['succeeded', 'failed']);
    assert.deepEqual(editActions[0].changed_paths, ['notes.txt']);
    assert.deepEqual(editActions[1].changed_paths, []);
    assert.equal(editActions[1].error_code, 'edit_tag_stale');
    assert.equal(editActions[1].result_metadata.retry_unchanged, false);
    assert.equal(editActions[1].result_metadata.recovery_tool, 'read');

    const readActions = actions.filter((action) => action.tool_name === 'read');
    assert.deepEqual(readActions.map((action) => action.status), ['succeeded', 'succeeded', 'succeeded']);

    const supertoolAction = actions[6];
    assert.equal(supertoolAction.tool_name, 'read');
    assert.equal(supertoolAction.invocation_surface, 'codexpro');

    const supertoolWriteAction = actions[7];
    assert.equal(supertoolWriteAction.tool_name, 'write');
    assert.equal(supertoolWriteAction.invocation_surface, 'codexpro');
    assert.deepEqual(supertoolWriteAction.changed_paths, ['supertool.txt']);
    assert.ok(supertoolWriteAction.git_before.changed_paths.includes('notes.txt'));
    assert.equal(supertoolWriteAction.git_before.changed_paths.includes('supertool.txt'), false);
    assert.ok(supertoolWriteAction.git_after.changed_paths.includes('notes.txt'));
    assert.ok(supertoolWriteAction.git_after.changed_paths.includes('supertool.txt'));
    assert.equal(actions.some((action) => action.tool_name === 'codexpro'), false);

    const bashActions = actions.filter((action) => action.tool_name === 'bash');
    assert.deepEqual(bashActions.map((action) => action.status), ['failed', 'timed_out', 'succeeded']);
    assert.equal(bashActions[0].error_code, 'command_exit_3');
    assert.equal(bashActions[1].error_code, 'timeout');
    assert.equal(bashActions[1].result_metadata.timed_out, true);
    assert.equal(bashActions[2].error_code, undefined);
    assert.equal(bashActions[2].result_metadata.timed_out, false);
    assert.equal(bashActions.every((action) => action.mutating), true);

    const got = await connection.client.callTool({
      name: 'activity_get',
      arguments: { action_id: writeAction.action_id }
    });
    assert.notEqual(got.isError, true);
    assert.equal(got.structuredContent.action.action_id, writeAction.action_id);

    const status = await connection.client.callTool({ name: 'activity_status', arguments: {} });
    assert.notEqual(status.isError, true);
    assert.equal(status.structuredContent.latest_sequence, 11);
    assert.equal(status.structuredContent.next_sequence, 12);
    assert.equal(status.structuredContent.gap_detected, false);

    const exported = await connection.client.callTool({
      name: 'activity_export',
      arguments: { after_sequence: 0, limit: 3, format: 'jsonl' }
    });
    assert.notEqual(exported.isError, true);
    const exportText = exported.content[0].text;
    const exportedActions = exportText.trim().split('\n').map((line) => JSON.parse(line));
    assert.deepEqual(exportedActions.map((action) => action.sequence), [1, 2, 3]);
    assert.equal(exported.structuredContent.action_count, 3);
    assert.equal(exported.structuredContent.next_sequence, 3);
    assert.equal(exported.structuredContent.has_more, true);
    assert.equal(exported.structuredContent.export_bytes, Buffer.byteLength(exportText));
    assert.equal(exported.structuredContent.export_sha256, createHash('sha256').update(exportText).digest('hex'));
    assert.equal((await fs.stat(f.log)).size, beforeActivityReads);

    const raw = await fs.readFile(f.log, 'utf8');
    for (const forbidden of ['alpha', 'beta', 'gamma', 'process.exit(3)', 'setTimeout(() => {}, 5000)', 'timeout text is data']) {
      assert.equal(raw.includes(forbidden), false, `central journal leaked ${forbidden}`);
    }
  } finally {
    await connection.close();
    await f.cleanup();
  }
});
