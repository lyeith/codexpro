// Run from either the checkout or an unpacked npm package with dependencies installed.
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro package output '));
const repo = path.join(root, 'repo'); await fs.mkdir(repo);
assert.equal(spawnSync('git',['init','-q'],{cwd:repo}).status,0);
const client = new Client({name:'packaged-output-smoke',version:'1'});
const transport = new StdioClientTransport({command:process.execPath,args:['dist/stdio.js','--root',repo,'--bash','full','--tool-mode','full','--tool-cards','on'],cwd:process.cwd(),
  env:{...process.env,CODEXPRO_HOME:path.join(root,'home'),CODEXPRO_JOBS_DIR:path.join(root,'job logs'),CODEXPRO_TOOL_CARDS:'1',CODEXPRO_INHERIT_ENV:'0',CODEXPRO_JOB_SCOPES:'0',CODEXPRO_AUDIT_MODE:'off',CODEXPRO_EXPOSE_ABSOLUTE_PATHS:'0'}});
try {
  await client.connect(transport);
  assert.equal(client.getServerCapabilities()?.resources,undefined);
  for (const tool of (await client.listTools()).tools) {assert.equal(tool._meta?.ui,undefined);assert.equal(tool._meta?.['openai/outputTemplate'],undefined);}
  const call = async (name,args={})=>{const result=await client.callTool({name,arguments:args});assert.notEqual(result.isError,true,JSON.stringify(result));return result;};
  const workspace_id=(await call('open_current_workspace')).structuredContent.workspace_id;
  await call('apply_patch',{workspace_id,patch:'*** Begin Patch\n*** Add File: check.txt\n+packaged native patch\n*** End Patch'});
  assert.equal(await fs.readFile(path.join(repo,'check.txt'),'utf8'),'packaged native patch\n');
  const run=await call('bash',{workspace_id,command:`node -e 'console.log("line\\n".repeat(180000)); console.log("PACKAGE_MIDDLE_MARKER"); console.log("tail\\n".repeat(20000))'`,timeout_ms:15000});
  assert.equal(run.structuredContent.exit_code,0); const id=run.structuredContent.job_id;
  assert.ok(run.structuredContent.stdout_bytes>720000);
  const meta=(await call('jobs',{workspace_id,job_ids:[id],output:'none',wait_ms:0})).structuredContent.jobs[0];assert.equal(meta.returned_bytes,0);
  let cursor, collected='';
  for(let i=0;i<100;i++){
    const response=await call('jobs',{workspace_id,job_ids:[id],output:'incremental',cursor,max_bytes:24576});
    assert.ok(Buffer.byteLength(JSON.stringify(response))<=192*1024);
    const page=response.structuredContent.jobs[0];collected+=page.stdout;cursor=page.next_cursor;if(page.output_complete)break;
  }
  assert.equal(Buffer.byteLength(collected),meta.available_stdout_bytes);assert.ok(collected.includes('PACKAGE_MIDDLE_MARKER'));
  const filtered=await call('bash',{workspace_id,command:`grep -n -m 1 PACKAGE_MIDDLE_MARKER "$CODEXPRO_JOB_OUTPUT_DIR/${id}/stdout.log"`,input_job_ids:[id]});
  assert.equal(filtered.structuredContent.exit_code,0);assert.match(filtered.structuredContent.stdout,/PACKAGE_MIDDLE_MARKER/);
  assert.deepEqual((await fs.readdir(repo)).sort(),['.git','check.txt']);
  console.log('✓ packaged stdio: text-only surface, native patch, >1 MB capture, complete pagination and pinned shell inspection');
} finally {await client.close();await transport.close();await fs.rm(root,{recursive:true,force:true});}
