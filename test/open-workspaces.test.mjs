import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';

async function fixture(options = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-open-workspaces-'));
  const projects = {};
  for (const id of ['alpha', 'beta', 'gamma']) {
    const projectRoot = path.join(root, id);
    await fs.mkdir(projectRoot);
    await fs.writeFile(path.join(projectRoot, `${id}.txt`), `${id}\n`, 'utf8');
    await fs.mkdir(path.join(projectRoot, 'src'));
    await fs.writeFile(path.join(projectRoot, 'src', `${id}.js`), `export const name = '${id}';\n`, 'utf8');
    projects[id] = projectRoot;
  }

  const projectsFile = path.join(root, 'projects.json');
  await fs.writeFile(projectsFile, `${JSON.stringify({
    version: 1,
    defaultProject: 'alpha',
    projects: Object.entries(projects).map(([id, projectRoot]) => ({
      id,
      label: `${id.toUpperCase()} project`,
      root: projectRoot
    }))
  }, null, 2)}\n`, 'utf8');

  const args = [
    '--projects-file', projectsFile,
    '--tool-mode', 'full',
    '--bash', 'off'
  ];
  if (options.audit) {
    args.push('--audit', 'metadata', '--audit-log', path.join(root, 'audit', 'actions.jsonl'));
  }
  const config = loadConfig(args);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-open-workspaces-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);

  return {
    root,
    projects,
    config,
    server,
    client,
    close: async () => {
      await client.close();
      await server.close();
      await fs.rm(root, { recursive: true, force: true });
    }
  };
}

async function listOpened(f) {
  const result = await f.client.callTool({ name: 'list_workspaces', arguments: {} });
  assert.notEqual(result.isError, true);
  return result.structuredContent;
}

test('open_workspace opens and resolves several named projects in one call', async () => {
  const f = await fixture();
  try {
    const opened = await f.client.callTool({
      name: 'open_workspace',
      arguments: {
        project_ids: ['beta', 'alpha', 'beta']
      }
    });
    assert.notEqual(opened.isError, true);
    assert.equal(opened.structuredContent.count, 2);
    assert.equal(opened.structuredContent.include_tree, false);
    assert.deepEqual(opened.structuredContent.project_ids, ['beta', 'alpha']);
    assert.deepEqual(
      opened.structuredContent.workspaces.map((workspace) => workspace.project_id),
      ['beta', 'alpha']
    );
    assert.equal(opened.structuredContent.selected_project_id, 'beta');
    assert.equal(
      opened.structuredContent.primary_workspace_id,
      opened.structuredContent.workspaces[0].workspace_id
    );
    assert.equal(
      opened.structuredContent.selected_workspace_id,
      opened.structuredContent.workspaces[0].workspace_id
    );
    assert.match(opened.content[0].text, /Reuse the returned workspace_ids/i);
    assert.equal(opened.structuredContent.already_open_count, 0);
    assert.equal(opened.structuredContent.workspaces.some((workspace) => workspace.tree), false);

    const repeated = await f.client.callTool({
      name: 'open_workspace',
      arguments: { project_ids: ['beta', 'alpha'] }
    });
    assert.notEqual(repeated.isError, true);
    assert.equal(repeated.structuredContent.already_open_count, 2);
    assert.equal(repeated.structuredContent.include_tree, false);

    const listed = await listOpened(f);
    assert.equal(listed.count, 2);
    assert.equal(listed.selected_workspace_id, opened.structuredContent.primary_workspace_id);

    for (const workspace of opened.structuredContent.workspaces) {
      const read = await f.client.callTool({
        name: 'read',
        arguments: {
          workspace_id: workspace.workspace_id,
          path: `${workspace.project_id}.txt`
        }
      });
      assert.notEqual(read.isError, true);
      assert.match(read.content[0].text, new RegExp(workspace.project_id));
    }
  } finally {
    await f.close();
  }
});

test('multi-open preflights every project id before opening any requested workspace', async () => {
  const f = await fixture();
  try {
    const before = await listOpened(f);
    assert.deepEqual(before.workspaces.map((workspace) => workspace.projectId), ['alpha']);

    const failed = await f.client.callTool({
      name: 'open_workspace',
      arguments: {
        project_ids: ['gamma', 'missing']
      }
    });
    assert.equal(failed.isError, true);
    assert.match(failed.structuredContent.error, /Unknown project_id.*missing/i);

    const listed = await listOpened(f);
    assert.deepEqual(listed.workspaces.map((workspace) => workspace.projectId), ['alpha']);
    assert.equal(listed.workspaces.some((workspace) => workspace.projectId === 'gamma'), false);
  } finally {
    await f.close();
  }
});

test('reopening one workspace is compact by default but can explicitly refresh its tree', async () => {
  const f = await fixture();
  try {
    const first = await f.client.callTool({
      name: 'open_workspace',
      arguments: { project_id: 'gamma' }
    });
    assert.notEqual(first.isError, true);
    assert.equal(first.structuredContent.already_open, false);
    assert.equal(first.structuredContent.include_tree, true);
    assert.match(first.structuredContent.tree, /gamma\.txt/);

    const repeated = await f.client.callTool({
      name: 'open_workspace',
      arguments: { project_id: 'gamma' }
    });
    assert.notEqual(repeated.isError, true);
    assert.equal(repeated.structuredContent.workspace_id, first.structuredContent.workspace_id);
    assert.equal(repeated.structuredContent.already_open, true);
    assert.equal(repeated.structuredContent.include_tree, false);
    assert.equal(repeated.structuredContent.tree, undefined);
    assert.match(repeated.content[0].text, /already open.*reuse workspace_id/is);

    const refreshed = await f.client.callTool({
      name: 'open_workspace',
      arguments: { project_id: 'gamma', include_tree: true }
    });
    assert.notEqual(refreshed.isError, true);
    assert.equal(refreshed.structuredContent.already_open, true);
    assert.equal(refreshed.structuredContent.include_tree, true);
    assert.match(refreshed.structuredContent.tree, /gamma\.txt/);
  } finally {
    await f.close();
  }
});

test('multi-open uses a shared tree budget and rejects ambiguous mixed target forms', async () => {
  const f = await fixture();
  try {
    const opened = await f.client.callTool({
      name: 'open_workspace',
      arguments: {
        project_ids: ['alpha', 'beta'],
        include_tree: true,
        max_files: 6
      }
    });
    assert.notEqual(opened.isError, true);
    assert.equal(opened.structuredContent.tree_max_files_per_workspace, 3);
    assert.equal(opened.structuredContent.workspaces.every((workspace) => typeof workspace.tree === 'string'), true);

    const mixed = await f.client.callTool({
      name: 'open_workspace',
      arguments: {
        project_id: 'alpha',
        project_ids: ['beta']
      }
    });
    assert.equal(mixed.isError, true);
    assert.match(mixed.structuredContent.error, /project_ids or one project_id\/root\/path target/i);
  } finally {
    await f.close();
  }
});

test('multi-open audit metadata records counts without retaining the project-id array', async () => {
  const f = await fixture({ audit: true });
  try {
    const opened = await f.client.callTool({
      name: 'open_workspace',
      arguments: {
        project_ids: ['alpha', 'beta', 'alpha']
      }
    });
    assert.notEqual(opened.isError, true);

    const activity = await f.client.callTool({
      name: 'activity_list',
      arguments: { limit: 20 }
    });
    assert.notEqual(activity.isError, true);
    const action = activity.structuredContent.actions.find((item) => item.tool_name === 'open_workspace');
    assert.equal(action.operation, 'workspace.open');
    assert.equal(action.project_id, 'alpha');
    assert.equal(action.workspace_id, opened.structuredContent.primary_workspace_id);
    assert.deepEqual(action.targets, ['project:alpha']);
    assert.equal(action.mutating, false);
    assert.equal(action.request_metadata.project_ids_count, 2);
    assert.equal(action.result_metadata.workspaces_count, 2);
    assert.equal(action.result_metadata.already_open_count, 0);

    const raw = await fs.readFile(f.config.auditLogPath, 'utf8');
    assert.doesNotMatch(raw, /"project_ids":/);
  } finally {
    await f.close();
  }
});
