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
import { OutputWriter, OUTPUT_RESPONSE_MAX } from '../dist/jobOutput.js';
import { patchPaths } from '../dist/patchSyntax.js';
import { serverGuidance } from '../dist/tools/guidance.js';
import { isToolAvailable } from '../dist/tools/registry.js';

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

test('large foreground output is captured, paged without loss, and searchable through pinned Bash files',async()=>{
  const f=await fixture({toolCards:true});
  try {
    const command=`python3 -c 'import sys,time; print("line\\n"*180000); print("MIDDLE_NEEDLE"); print("tail\\n"*20000); sys.stdout.flush(); time.sleep(1)'`;
    const started=await f.call('bash',{command,timeout_ms:15000}); assert.equal(started.structuredContent.exit_code,0,JSON.stringify(started.structuredContent));
    const id=started.structuredContent.job_id; assert.ok(started.structuredContent.stdout_bytes>720000);
    assert.ok(started.structuredContent.returned_bytes<=24576);
    const metadata=await f.call('jobs',{job_ids:[id],output:'none',wait_ms:0}); const m=metadata.structuredContent.jobs[0];
    assert.equal(m.returned_bytes,0); assert.ok(m.output_files.stdout.startsWith('$CODEXPRO_JOB_OUTPUT_DIR/'));
    assert.equal(m.stdout,undefined); assert.match(metadata.content[0].text,/retained rendered bytes/);
    let cursor, text='', pages=0;
    do {
      const response=await f.call('jobs',{job_ids:[id],output:'incremental',cursor,max_bytes:24576});
      assert.notEqual(response.isError,true,JSON.stringify(response)); assert.ok(Buffer.byteLength(JSON.stringify(response))<=OUTPUT_RESPONSE_MAX);
      const page=response.structuredContent.jobs[0]; text+=page.stdout; cursor=page.next_cursor; pages++;
      if(page.output_complete)break; assert.ok(pages<100);
    }while(true);
    assert.ok(text.includes('MIDDLE_NEEDLE')); assert.ok(text.endsWith('tail\n\n'));
    assert.equal(Buffer.byteLength(text),m.available_stdout_bytes);
    const filtered=await f.call('bash',{command:`grep -n MIDDLE_NEEDLE "$CODEXPRO_JOB_OUTPUT_DIR/${id}/stdout.log"`,input_job_ids:[id]});
    assert.equal(filtered.structuredContent.exit_code,0);assert.match(filtered.structuredContent.stdout,/MIDDLE_NEEDLE/);
    const bad=await f.call('jobs',{job_ids:[filtered.structuredContent.job_id],output:'incremental',cursor});assert.equal(bad.structuredContent.error_code,'job_cursor_invalid');
  }finally{await f.close();}
});

test('incremental reads with split UTF-8/secret writes are redacted, replayable, and do not acknowledge completion',async()=>{
  const f=await fixture();
  try {
    const started=await f.call('start_jobs',{commands:[{command:`python3 -c 'import sys,time; sys.stdout.write("API_KEY=abcdefghijkl"); sys.stdout.flush(); time.sleep(1.5); print("12345678901234567890"); print("😀中文"); sys.stdout.flush(); time.sleep(1)'`}]});
    const [id]=started.structuredContent.job_ids;
    const first=await f.call('jobs',{job_ids:[id],output:'incremental',wait_ms:0}); assert.equal(first.structuredContent.jobs[0].stdout,'');
    const cursor=first.structuredContent.jobs[0].next_cursor;
    const next=await f.call('jobs',{job_ids:[id],output:'incremental',cursor,wait_ms:5000});
    const page=next.structuredContent.jobs[0];assert.match(page.stdout,/REDACTED_SECRET/);assert.ok(page.stdout.includes('😀中文'));assert.doesNotMatch(page.stdout,/abcdefgh/);
    await f.manager.wait(id,5000); const again=await f.call('jobs',{job_ids:[id],output:'incremental',cursor}); assert.equal(again.structuredContent.jobs[0].stdout,page.stdout);
    assert.equal(f.manager.require(id).acknowledged,false);
    const shell=await f.call('bash',{command:`cat "$CODEXPRO_JOB_OUTPUT_DIR/${id}/stdout.log"`,input_job_ids:[id]}); assert.equal(shell.structuredContent.stdout,page.stdout);
  }finally{await f.close();}
});

test('retention separates foreground history, expires old output, and honors in-flight input leases',async()=>{
  const f=await fixture({maxJobHistoryPerWorkspace:1});
  try {
    const source=await f.call('start_jobs',{commands:[{command:'echo source'}]});const [id]=source.structuredContent.job_ids;
    await f.call('bash',{command:'echo foreground'});await f.call('bash',{command:'echo foreground2'});
    assert.equal(f.manager.require(id).output_expired,undefined);
    const reader=await f.call('start_jobs',{commands:[{command:`sleep 2; cat "$CODEXPRO_JOB_OUTPUT_DIR/${id}/stdout.log"`,input_job_ids:[id]}]});
    assert.equal(f.manager.require(id).output_expired,undefined);
    const [readerId]=reader.structuredContent.job_ids;await f.manager.wait(readerId,5000);
    assert.equal(f.manager.require(id).output_expired,true);
    const expired=await f.call('jobs',{job_ids:[id],output:'incremental'});assert.equal(expired.structuredContent.error_code,'job_output_expired');
    const out=f.manager.readTail(f.manager.require(readerId),1000);assert.match(out.stdout,/source/);
  }finally{await f.close();}
});

test('detached runner enforces its absolute deadline after the owning process exits',async()=>{
  const f=await fixture();
  try {
    const script=`import { JobManager } from './dist/jobs.js'; const c=${JSON.stringify(f.config)}; const m=new JobManager(c); const j=m.start({workspaceId:${JSON.stringify(f.id)},root:${JSON.stringify(f.repo)},cwdAbs:${JSON.stringify(f.repo)},cwdLabel:'.',command:'sleep 20',env:process.env,origin:'background',timeoutMs:700,outputLimitBytes:65536}); console.log(j.id);`;
    const parent=spawnSync(process.execPath,['--input-type=module','-e',script],{cwd:process.cwd(),encoding:'utf8',env:{...process.env,CODEXPRO_JOB_SCOPES:'0'},timeout:5000});
    assert.equal(parent.status,0,parent.stderr); const id=parent.stdout.trim();assert.match(id,/^job_/);
    await sleep(1600);
    const fresh=new JobManager(f.config);const record=fresh.require(id);assert.equal(record.status,'timed_out');assert.equal(record.stop_reason,'timeout');
  }finally{await f.close();}
});

test('guidance only recommends capabilities available in representative modes',()=>{
  const base=loadConfig(['--root',os.tmpdir()]);
  for(const toolMode of ['minimal','standard','full'])for(const writeMode of ['off','workspace'])for(const bashMode of ['off','safe','full']) {
    const config={...base,toolMode,writeMode,bashMode,handoffMode:'off'};const text=serverGuidance(config);
    for(const tool of ['batch','commit_changes','handoff_to_agent','ast_grep']) if(!isToolAvailable(config,tool)) assert.ok(!text.includes(tool),`${tool} in ${toolMode}/${writeMode}/${bashMode}`);
  }
});

test('complete-record rendering preserves split characters and explicitly omits unsafe records within a cap', async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(),'codexpro-render-'));
  try {
    const file = path.join(dir,'view'); const writer = new OutputWriter(file);
    const bytes = Buffer.from('😀中文\r\nAPI_KEY=abcdefghijkl12345678901234567890\nfinal');
    for (const byte of bytes) writer.write(Buffer.from([byte]));
    const running = await fs.readFile(file,'utf8'); assert.ok(running.startsWith('😀中文\r\n'));assert.ok(!running.includes('final'));assert.match(running,/REDACTED_SECRET/);
    writer.finish();assert.ok((await fs.readFile(file,'utf8')).endsWith('final'));
    const limited = path.join(dir,'limited');const budget={remaining:100,exhausted:false};const capped=new OutputWriter(limited,[],budget);
    capped.write(Buffer.from('x'.repeat(1024*1024+1)+'\n'));capped.write(Buffer.from('y'.repeat(200)+'\n'));capped.finish();
    const view=await fs.readFile(limited,'utf8');assert.match(view,/record omitted/);assert.ok(Buffer.byteLength(view)<=100);assert.equal(budget.exhausted,true);
  } finally {await fs.rm(dir,{recursive:true,force:true});}
});

test('deduplicated starts remain valid at capacity and log references work with restricted environment',async()=>{
  const f=await fixture({maxJobsPerWorkspace:1,inheritEnv:false});
  try {
    const first=await f.call('start_jobs',{commands:[{command:'sleep 5; echo done'}]});
    const again=await f.call('start_jobs',{commands:[{command:'sleep 5; echo done'}]});
    assert.notEqual(again.isError,true,JSON.stringify(again));assert.deepEqual(again.structuredContent.job_ids,first.structuredContent.job_ids);
    const [id]=first.structuredContent.job_ids;await f.manager.wait(id,6000);
    const inspected=await f.call('bash',{command:`cat "$CODEXPRO_JOB_OUTPUT_DIR/${id}/stdout.log"`,input_job_ids:[id]});
    assert.equal(inspected.structuredContent.stdout,'done\n');assert.doesNotMatch(JSON.stringify(inspected),new RegExp(f.config.jobsDir));
  }finally{await f.close();}
});

test('plural collections bound encoded escaping and legacy records remain readable',async()=>{
  const f=await fixture({maxJobs:32,maxJobsPerWorkspace:32,toolCards:true});
  try {
    const records=Array.from({length:32},(_,i)=>f.manager.start({workspaceId:f.id,root:f.repo,cwdAbs:f.repo,cwdLabel:'.',command:`python3 -c 'import sys; print(chr(1)*12000); print(chr(2)*12000,file=sys.stderr)' # ${i}`,env:process.env,origin:'background',timeoutMs:15000,outputLimitBytes:65536}));
    await f.manager.waitFor(records.map(j=>j.id),'all',15000);
    const result=await f.call('jobs',{job_ids:records.map(j=>j.id),output:'head',wait_ms:0});
    assert.notEqual(result.isError,true,JSON.stringify(result)); assert.ok(Buffer.byteLength(JSON.stringify(result))<=OUTPUT_RESPONSE_MAX);assert.ok(result.structuredContent.returned_output_bytes<=24576);
    const legacy={...records[0],runner_version:undefined};const output=f.manager.readOutput(legacy,100);
    assert.equal(output.stdout,'\x01'.repeat(100)); assert.ok(output.truncated);
    const unicode=await f.call('bash',{command:"printf '😀\\n'"}); const [page]=(await f.call('jobs',{job_ids:[unicode.structuredContent.job_id],output:'incremental'})).structuredContent.jobs;
    const token=JSON.parse(Buffer.from(page.next_cursor,'base64url').toString());token.offsets[0]=1;
    const bad=await f.call('jobs',{job_ids:[unicode.structuredContent.job_id],output:'incremental',cursor:Buffer.from(JSON.stringify(token)).toString('base64url')});assert.equal(bad.structuredContent.error_code,'job_cursor_invalid');
  }finally{await f.close();}
});

test('detached runner enforces capture limits without the owning MCP process',async()=>{
  const f=await fixture();
  try {
    const script=`import { JobManager } from './dist/jobs.js'; const m=new JobManager(${JSON.stringify(f.config)}); const j=m.start({workspaceId:${JSON.stringify(f.id)},root:${JSON.stringify(f.repo)},cwdAbs:${JSON.stringify(f.repo)},cwdLabel:'.',command:'yes noisy',env:process.env,origin:'background',timeoutMs:20000,outputLimitBytes:65536}); console.log(j.id);`;
    const parent=spawnSync(process.execPath,['--input-type=module','-e',script],{cwd:process.cwd(),encoding:'utf8',env:{...process.env,CODEXPRO_JOB_SCOPES:'0'},timeout:5000});assert.equal(parent.status,0,parent.stderr);
    await sleep(2500);const fresh=new JobManager(f.config);const job=fresh.require(parent.stdout.trim());assert.equal(job.stop_reason,'output_limit');assert.equal(job.status,'failed');
    const meta=fresh.output.metadata(job);assert.ok(meta.stdout_bytes+meta.stderr_bytes<65536+256);
  }finally{await f.close();}
});


test('explicit head reads the beginning of a running job while the legacy alias retains tail behavior',async()=>{
  const f=await fixture();
  try {
    const started=await f.call('start_jobs',{commands:[{command:`node -e 'console.log("A".repeat(20000));console.log("B".repeat(20000));setTimeout(()=>{},20000)'`}]});
    const [id]=started.structuredContent.job_ids;
    const head=await f.call('jobs',{job_ids:[id],output:'head',wait_ms:0});
    assert.equal(head.structuredContent.jobs[0].output_mode,'head');assert.ok(head.structuredContent.jobs[0].stdout.startsWith('AAAA'));
    const legacy=await f.call('jobs',{job_ids:[id],full_output:true,wait_ms:0});
    assert.equal(legacy.structuredContent.jobs[0].output_mode,'tail');assert.ok(legacy.structuredContent.jobs[0].stdout_tail.startsWith('BBBB'));
  }finally{await f.close();}
});
