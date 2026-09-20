import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { PathGuard } from '../dist/guard.js';
import { readTextFile } from '../dist/fsOps.js';

test('bounded read pages reconstruct UTF-8 text without skips and retain file identity', async () => {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(),'codexpro-read-pages-')));
  try {
    const config = loadConfig(['--root',root]);
    const guard = new PathGuard(config), workspace = {id:'fixture',root};
    const content = Array.from({length:600},(_,i)=>`row ${i+1}: 日本語 😀 evidence remains available`).join('\n');
    await fs.writeFile(path.join(root,'STATE.md'),content);
    let next=1, tag, sha, output=[];
    while(next!==null){
      const page=await readTextFile(config,guard,workspace,'STATE.md',{startLine:next,maxBytes:1000});
      assert.equal(page.startLine,next);
      assert.ok(page.returnedBytes<=1000);
      assert.equal(page.returnedBytes,Buffer.byteLength(page.text));
      assert.doesNotMatch(page.text,/�/);
      tag??=page.editTag;sha??=page.sha256;
      assert.equal(page.editTag,tag);assert.equal(page.sha256,sha);
      output.push(...page.text.split('\n').map(line=>line.replace(/^\s*\d+ \| /,'')));
      next=page.nextStartLine;
    }
    assert.equal(output.join('\n'),content);
    await fs.writeFile(path.join(root,'long.txt'),'x'.repeat(2000));
    await assert.rejects(readTextFile(config,guard,workspace,'long.txt',{maxBytes:1000}),error=>error.code==='read_line_too_large');
    await fs.writeFile(path.join(root,'binary.txt'),Buffer.from([65,0,66]));
    await assert.rejects(readTextFile(config,guard,workspace,'binary.txt'),error=>error.code==='file_binary');
  }finally{await fs.rm(root,{recursive:true,force:true});}
});

test('large continuity docs read successfully in a batch, return continuation metadata and do not authorize unseen edits', async () => {
  const root=await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(),'codexpro-read-batch-')));
  const config=loadConfig(['--root',root,'--tool-mode','full','--bash','off']);
  const server=createCodexProServer(config),client=new Client({name:'read-pages-test',version:'1'});
  const [st,ct]=InMemoryTransport.createLinkedPair();
  try{
    const text=Array.from({length:5000},(_,i)=>`Line ${i+1}: preserved checkpoint evidence with pending acceptance criteria.`).join('\n');
    assert.ok(Buffer.byteLength(text)>config.maxReadBytes);
    for(const file of ['STATE.md','HANDOFF.md','BACKLOG.md'])await fs.writeFile(path.join(root,file),text);
    await server.connect(st);await client.connect(ct);
    const opened=await client.callTool({name:'open_current_workspace',arguments:{include_tree:false}});
    const workspace_id=opened.structuredContent.workspace_id;
    const result=await client.callTool({name:'batch',arguments:{workspace_id,mode:'parallel',operations:
      ['STATE.md','HANDOFF.md','BACKLOG.md'].map((file,i)=>({id:`read${i}`,tool:'read',args:{path:file,max_bytes:2000}}))}});
    assert.notEqual(result.isError,true,JSON.stringify(result.structuredContent));
    assert.equal(result.structuredContent.succeeded_count,3);
    assert.equal(result.structuredContent.failed_count,0);
    const first=await client.callTool({name:'read',arguments:{workspace_id,path:'STATE.md',max_bytes:2000}});
    assert.notEqual(first.isError,true);
    const page=first.structuredContent;
    assert.equal(page.has_more,true);assert.equal(page.next_start_line,page.end_line+1);
    assert.ok(page.returned_bytes<=2000);
    const edit=await client.callTool({name:'edit',arguments:{workspace_id,path:'STATE.md',edit_tag:page.edit_tag,
      edits:[{op:'replace',start_line:4000,end_line:4000,content:'Do not allow unseen edit'}]}});
    assert.equal(edit.isError,true);
    assert.equal(await fs.readFile(path.join(root,'STATE.md'),'utf8'),text);
    const second=await client.callTool({name:'read',arguments:{workspace_id,path:'STATE.md',start_line:page.next_start_line,max_bytes:2000}});
    assert.equal(second.structuredContent.start_line,page.next_start_line);
    assert.equal(second.structuredContent.edit_tag,page.edit_tag);
    config.exposeAbsolutePaths=false;
    const token='sk-'+'z'.repeat(32);
    await fs.writeFile(path.join(root,'labels.txt'),Array.from({length:100},()=>`Path ${root}; sample ${token}`).join('\n'));
    const labelled=await client.callTool({name:'read',arguments:{workspace_id,path:'labels.txt',max_bytes:1000}});
    assert.notEqual(labelled.isError,true);
    const visible=labelled.structuredContent;
    assert.equal(visible.returned_bytes,Buffer.byteLength(visible.text));
    assert.ok(visible.returned_bytes<=1000);
    assert.ok(visible.text.includes(`[workspace:${workspace_id}]`));
    assert.ok(!visible.text.includes(root) && !visible.text.includes(token));
  }finally{await client.close();await server.close();await fs.rm(root,{recursive:true,force:true});}
});
