import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { getJobManager, JobManager } from '../dist/jobs.js';
import { patchPaths } from '../dist/patchSyntax.js';

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function fixture(extra = {}) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-surface-'));
  const repo = path.join(root, 'repo'); await fs.mkdir(repo);
  const config = { ...loadConfig(['--root', repo, '--bash', 'full', '--write', 'workspace', '--tool-mode', 'full', '--audit', 'metadata', '--audit-log', path.join(root, 'audit.jsonl')]),
    jobsDir: path.join(root, 'jobs'), ...extra };
  for (const args of [['init'], ['config','user.email','surface@example.test'], ['config','user.name','Surface'], ['commit','--allow-empty','-m','initial']]) {
    const result = spawnSync('git', args, { cwd: repo }); assert.equal(result.status, 0, String(result.stderr));
  }
  const server = createCodexProServer(config); const client = new Client({ name:'surface-test', version:'0' });
  const [a,b] = InMemoryTransport.createLinkedPair(); await server.connect(a); await client.connect(b);
  const open = await client.callTool({ name:'open_current_workspace', arguments:{} }); const id = open.structuredContent.workspace_id;
  return { root, repo, config, id, client, manager:getJobManager(config),
    call: (name,args={}) => client.callTool({name,arguments:{workspace_id:id,...args}}),
    async close() { for (const j of getJobManager(config).runningJobs()) getJobManager(config).stop(j.id); await getJobManager(config).waitFor(getJobManager(config).runningJobs().map(j=>j.id),'all',6000); await client.close(); await server.close(); await fs.rm(root,{recursive:true,force:true}); }
  };
}

test('native patches share guarded multi-file application, audit targets and stale edit detection', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.repo,'one.txt'),'before\n');
    const read = await f.call('read',{path:'one.txt'});
    const patch = '*** Begin Patch\n*** Update File: one.txt\n@@\n-before\n+after\n*** Add File: spaced name.txt\n+new\n*** End Patch';
    const result = await f.call('apply_patch',{patch}); assert.notEqual(result.isError,true,JSON.stringify(result));
    assert.equal(await fs.readFile(path.join(f.repo,'one.txt'),'utf8'),'after\n');
    assert.equal(await fs.readFile(path.join(f.repo,'spaced name.txt'),'utf8'),'new\n');
    assert.match(result.structuredContent.diff,/diff --git/);
    const stale = await f.call('edit',{path:'one.txt',edit_tag:read.structuredContent.edit_tag,edits:[{op:'replace',start_line:1,content:'bad'}]}); assert.equal(stale.isError,true);
    const actions = (await f.call('activity_list',{limit:20})).structuredContent.actions;
    const action = actions.find(a=>a.tool_name==='apply_patch'); assert.equal(action.request_metadata.target_path_count,2);
    const moved = await f.call('apply_patch',{patch:'*** Begin Patch\n*** Update File: one.txt\n*** Move to: moved.txt\n@@\n-after\n+done\n*** Delete File: spaced name.txt\n*** End Patch'});
    assert.notEqual(moved.isError,true,JSON.stringify(moved));
    assert.equal(await fs.readFile(path.join(f.repo,'moved.txt'),'utf8'),'done\n');
    assert.equal(await fs.stat(path.join(f.repo,'one.txt')).catch(()=>null),null);
  } finally { await f.close(); }
});

test('patch preflight rejects stale/ambiguous context, traversal, symlinks and invalid mixtures without partial writes',async()=>{
  const f=await fixture();
  try {
    await fs.writeFile(path.join(f.repo,'one.txt'),'same\nsame\n');
    await fs.symlink(path.join(f.root,'outside'),path.join(f.repo,'link'));
    for(const second of ['*** Update File: one.txt\n@@\n-same\n+bad','*** Add File: ../outside\n+bad','*** Add File: .env\n+bad','*** Add File: link\n+bad']) {
      const result=await f.call('apply_patch',{patch:`*** Begin Patch\n*** Add File: first.txt\n+first\n${second}\n*** End Patch`});
      assert.equal(result.isError,true); assert.equal(await fs.stat(path.join(f.repo,'first.txt')).catch(()=>null),null);
    }
    assert.deepEqual(patchPaths('diff --git a/one.txt b/one.txt\n--- a/one.txt\n+++ b/one.txt\n@@ -1 +1 @@\n--- payload\n+++ payload\n'),['one.txt']);
    await fs.writeFile(path.join(f.repo,'crlf.txt'),'hello\r\n');
    const crlf=await f.call('apply_patch',{patch:'*** Begin Patch\n*** Update File: crlf.txt\n@@\n-hello\n+bye\n*** End Patch'});
    assert.notEqual(crlf.isError,true,JSON.stringify(crlf)); assert.equal(await fs.readFile(path.join(f.repo,'crlf.txt'),'utf8'),'bye\r\n');
  } finally {await f.close();}
});

test('native patches handle empty files, final lines and guarded mode-only Git targets through dispatch', async () => {
  const f = await fixture();
  try {
    const empty = await f.call('apply_patch', {patch:'*** Begin Patch\n*** Add File: empty.txt\n*** End Patch'});
    assert.notEqual(empty.isError,true,JSON.stringify(empty)); assert.equal(await fs.readFile(path.join(f.repo,'empty.txt'),'utf8'),'');
    await fs.writeFile(path.join(f.repo,'last.txt'),'header\nlast');
    const result = await f.call('batch',{operations:[
      {id:'patch',tool:'apply_patch',args:{patch:'*** Begin Patch\n*** Update File: last.txt\n@@ header\n-last\n+final\n*** End of File\n*** End Patch'}},
      {id:'check',tool:'bash',args:{command:'git diff --check'}}
    ]});
    assert.notEqual(result.isError,true,JSON.stringify(result)); assert.equal(await fs.readFile(path.join(f.repo,'last.txt'),'utf8'),'header\nfinal');
    const superPatch = await f.client.callTool({name:'codexpro',arguments:{action:'apply_patch',args:{workspace_id:f.id,patch:'*** Begin Patch\n*** Add File: via-dispatch.txt\n+dispatch\n*** End Patch'}}});
    assert.notEqual(superPatch.isError,true,JSON.stringify(superPatch));assert.equal(await fs.readFile(path.join(f.repo,'via-dispatch.txt'),'utf8'),'dispatch\n');
    await fs.writeFile(path.join(f.repo,'.env'),'dummy\n');
    const mode = await f.call('apply_patch',{patch:'diff --git a/.env b/.env\nold mode 100644\nnew mode 100755\n'});
    assert.equal(mode.isError,true); assert.equal((await fs.stat(path.join(f.repo,'.env'))).mode & 0o111,0);
    assert.deepEqual(patchPaths('diff --git old/.env new/.env\nnew file mode 100644\n'),['.env']);
    assert.deepEqual(patchPaths('diff --git a/spaced file b/spaced file\nold mode 100644\nnew mode 100755\n'),['spaced file']);
  } finally { await f.close(); }
});
