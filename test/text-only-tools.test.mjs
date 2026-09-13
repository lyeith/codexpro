import test from 'node:test';
import assert from 'node:assert/strict';
import os from 'node:os';
import fs from 'node:fs/promises';
import path from 'node:path';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';

test('all tool modes stay text-only even with legacy card settings and retain attachment metadata', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-text-only-'));
  try {
    for (const mode of ['minimal','standard','full']) {
      const config = loadConfig(['--root',root,'--tool-mode',mode,'--tool-cards','on','--bash','full']);
      assert.equal(config.toolCards,false);
      config.jobsDir=path.join(root,'jobs',mode);
      config.toolCards=true; // Old embedding code must not re-enable the removed renderer either.
      const server=createCodexProServer(config), client=new Client({name:'text-only-test',version:'1'});
      const [a,b]=InMemoryTransport.createLinkedPair();await server.connect(a);await client.connect(b);
      try {
        assert.equal(client.getServerCapabilities()?.resources,undefined);
        const {tools}=await client.listTools();
        for (const tool of tools) {
          for (const key of ['ui','openai/outputTemplate','openai/toolInvocation/invoking','openai/toolInvocation/invoked']) assert.equal(tool._meta?.[key],undefined,`${mode}/${tool.name}/${key}`);
        }
        assert.deepEqual(tools.find(t=>t.name==='import_file')._meta['openai/fileParams'],['file']);
        const opened=await client.callTool({name:'open_current_workspace',arguments:{}});
        assert.ok(opened.content.some(item=>item.type==='text'));assert.ok(opened.structuredContent.workspace_id);
        assert.equal(opened.content.some(item=>item.type==='resource'),false);
      } finally {await client.close();await server.close();}
    }
  } finally {await fs.rm(root,{recursive:true,force:true});}
});
