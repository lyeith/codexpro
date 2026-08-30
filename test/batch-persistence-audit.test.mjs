import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';

test('batch persistence and resume emit bounded metadata without retaining definition payloads', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-batch-audit-'));
  const repo = path.join(root, 'repo');
  await fs.mkdir(repo);
  await fs.writeFile(path.join(repo, 'present.txt'), 'present\n', 'utf8');
  const auditPath = path.join(root, 'audit', 'actions.jsonl');
  const config = loadConfig([
    '--root', repo,
    '--tool-mode', 'full',
    '--bash', 'full',
    '--write', 'workspace',
    '--audit', 'metadata',
    '--audit-log', auditPath
  ]);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-batch-audit-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);

  try {
    const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
    const workspaceId = opened.structuredContent.workspace_id;
    const first = await client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: workspaceId,
        persist: true,
        operations: [
          {
            id: 'change',
            tool: 'write',
            args: { path: 'generated.txt', content: 'PRIVATE_BATCH_BODY_MUST_NOT_LEAK\n' }
          },
          { id: 'verify', tool: 'read', args: { path: 'missing.txt' } },
          {
            id: 'shell',
            tool: 'bash',
            args: { command: 'pwd' }
          },
          { id: 'after', tool: 'read', args: { path: 'generated.txt' } }
        ]
      }
    });
    assert.equal(first.isError, true);
    await fs.writeFile(path.join(repo, 'missing.txt'), 'now present\n', 'utf8');

    const resumed = await client.callTool({
      name: 'batch',
      arguments: {
        workspace_id: workspaceId,
        path: first.structuredContent.batch_path,
        from: 'verify'
      }
    });
    assert.notEqual(resumed.isError, true);

    const activity = await client.callTool({
      name: 'activity_list',
      arguments: { limit: 20 }
    });
    assert.notEqual(activity.isError, true);
    const batches = activity.structuredContent.actions.filter((action) => action.tool_name === 'batch');
    assert.equal(batches.length, 2);

    const initialAction = batches.find((action) => action.request_metadata.batch_source === 'inline');
    const resumedAction = batches.find((action) => action.request_metadata.batch_source === 'file');
    assert.ok(initialAction);
    assert.ok(resumedAction);
    assert.equal(initialAction.dashboard_metadata, undefined);
    assert.equal(resumedAction.dashboard_metadata, undefined);

    assert.equal(initialAction.request_metadata.operation_count, 4);
    assert.equal(initialAction.request_metadata.file_mutation_count, 1);
    assert.equal(initialAction.request_metadata.verification_command_count, 1);
    assert.equal(initialAction.result_metadata.persisted, true);
    assert.equal(initialAction.result_metadata.auto_stored, true);
    assert.equal(initialAction.result_metadata.failed_operation_id, 'verify');
    assert.equal(initialAction.result_metadata.failed_index, 1);
    assert.match(initialAction.result_metadata.batch_path, /^\.codexpro-batches\/[0-9A-F]{4}\.json$/);

    assert.equal(resumedAction.request_metadata.from_operation, 'verify');
    assert.equal(resumedAction.request_metadata.batch_path, first.structuredContent.batch_path);
    assert.equal(resumedAction.result_metadata.start_index, 1);
    assert.equal(resumedAction.result_metadata.total_operation_count, 4);
    assert.equal(resumedAction.result_metadata.operation_count, 3);
    assert.equal(resumedAction.result_metadata.succeeded, true);

    const rawJournal = await fs.readFile(auditPath, 'utf8');
    assert.doesNotMatch(rawJournal, /PRIVATE_BATCH_BODY_MUST_NOT_LEAK/);
    assert.doesNotMatch(rawJournal, /"content":"PRIVATE_BATCH_BODY/);
    assert.doesNotMatch(rawJournal, /"operations":/);
    assert.match(rawJournal, /"batch_path":"\.codexpro-batches\/[0-9A-F]{4}\.json"/);
    const storedBatches = rawJournal.trim().split('\n').map((line) => JSON.parse(line)).filter((action) => action.tool_name === 'batch');
    const storedInitial = storedBatches.find((action) => action.request_metadata.batch_source === 'inline');
    const storedResumed = storedBatches.find((action) => action.request_metadata.batch_source === 'file');
    assert.equal(storedInitial.dashboard_metadata, undefined);
    assert.deepEqual(storedResumed.dashboard_metadata.shell_scripts, [{
      operation_id: 'shell',
      script: 'pwd'
    }]);
  } finally {
    await client.close();
    await server.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});

test('audit mutation classification follows whether an inline read batch is persisted', async () => {
  for (const scenario of [
    {
      name: 'workspace-one-shot',
      writeMode: 'workspace',
      expectedMutating: false,
      expectedClass: 'read',
      expectedPersisted: false
    },
    {
      name: 'workspace-persisted',
      writeMode: 'workspace',
      persist: true,
      expectedMutating: true,
      expectedClass: 'write',
      expectedPersisted: true
    },
    {
      name: 'off-one-shot',
      writeMode: 'off',
      expectedMutating: false,
      expectedClass: 'read',
      expectedPersisted: false
    }
  ]) {
    const root = await fs.mkdtemp(path.join(os.tmpdir(), `codexpro-batch-audit-${scenario.name}-`));
    const repo = path.join(root, 'repo');
    await fs.mkdir(repo);
    await fs.writeFile(path.join(repo, 'present.txt'), 'present\n', 'utf8');
    const auditPath = path.join(root, 'audit', 'actions.jsonl');
    const config = loadConfig([
      '--root', repo,
      '--tool-mode', 'full',
      '--bash', 'off',
      '--write', scenario.writeMode,
      '--audit', 'metadata',
      '--audit-log', auditPath
    ]);
    const server = createCodexProServer(config);
    const client = new Client({ name: `codexpro-batch-audit-${scenario.name}`, version: '0.0.0' });
    const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
    await server.connect(serverTransport);
    await client.connect(clientTransport);

    try {
      const opened = await client.callTool({ name: 'open_current_workspace', arguments: {} });
      const batchArgs = {
        workspace_id: opened.structuredContent.workspace_id,
        operations: [{ id: 'read', tool: 'read', args: { path: 'present.txt' } }]
      };
      if (scenario.persist !== undefined) batchArgs.persist = scenario.persist;
      const result = await client.callTool({
        name: 'batch',
        arguments: batchArgs
      });
      assert.notEqual(result.isError, true);
      assert.equal(result.structuredContent.persisted, scenario.expectedPersisted);
      assert.equal(result.structuredContent.persistence_requested, scenario.expectedPersisted);

      const activity = await client.callTool({ name: 'activity_list', arguments: { limit: 20 } });
      const action = activity.structuredContent.actions.find((item) => item.tool_name === 'batch');
      assert.ok(action);
      assert.equal(action.mutating, scenario.expectedMutating);
      assert.equal(action.operation_class, scenario.expectedClass);
      assert.equal(action.result_metadata.persisted, scenario.expectedPersisted);
    } finally {
      await client.close();
      await server.close();
      await fs.rm(root, { recursive: true, force: true });
    }
  }
});
