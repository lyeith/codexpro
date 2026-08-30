import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';

test('tool descriptions steer agents toward high-acceptance edit, patch, and batch shapes', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-tool-guidance-'));
  const config = loadConfig([
    '--root', root,
    '--tool-mode', 'full',
    '--bash', 'full',
    '--write', 'workspace'
  ]);
  const server = createCodexProServer(config);
  const client = new Client({ name: 'codexpro-tool-guidance-test', version: '0.0.0' });
  const [serverTransport, clientTransport] = InMemoryTransport.createLinkedPair();
  await server.connect(serverTransport);
  await client.connect(clientTransport);

  try {
    const listed = await client.listTools();
    const byName = new Map(listed.tools.map((tool) => [tool.name, tool]));
    const read = byName.get('read');
    const astGrep = byName.get('ast_grep');
    const edit = byName.get('edit');
    const patch = byName.get('apply_patch');
    const batch = byName.get('batch');
    assert.ok(read && astGrep && edit && patch && batch);

    assert.match(read.description, /read every range.*one combined multi-hunk edit/is);
    assert.match(astGrep.description, /structurally.*syntax|syntax-aware/is);
    assert.match(astGrep.description, /rather than type-aware semantic navigation/i);
    assert.match(astGrep.description, /edit_tag provenance|edited directly/i);
    assert.match(edit.description, /all intended changes.*single tagged multi-hunk call/is);
    assert.match(edit.description, /do not reuse the tag/is);
    assert.match(patch.description, /standard unified diff/i);
    assert.match(patch.description, /not \*\*\* Begin Patch/i);
    assert.match(patch.description, /use edit for every single-file change/i);
    assert.match(batch.description, /direct tools for one or two simple read-only calls/i);
    assert.match(batch.description, /three or more related reads/i);
    assert.match(batch.description, /distinct file/i);

    const batchProperties = batch.inputSchema?.properties ?? {};
    assert.ok(batchProperties.persist);
    assert.match(batchProperties.persist.description, /1-2 read-only operations/i);
    assert.match(batchProperties.operations.description, /direct tools for 1-2 simple read-only calls/i);
    assert.match(batchProperties.mode.description, /3\+ independent read-only operations/i);
  } finally {
    await client.close();
    await server.close();
    await fs.rm(root, { recursive: true, force: true });
  }
});
