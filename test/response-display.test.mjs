import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { execFileSync, spawn } from 'node:child_process';
import net from 'node:net';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { loadConfig } from '../dist/config.js';
import { createCodexProServer } from '../dist/server.js';
import { AuditJournal } from '../dist/audit.js';
import { getJobManager } from '../dist/jobs.js';
import { collectActivityDashboard, renderActivityDashboardPage, renderActivityJobFragment } from '../dist/activityDashboard.js';

async function fixture() {
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-display-'));
  const root = path.join(home, 'repo'); await fs.mkdir(root);
  const git = (...args) => execFileSync('git', args, {cwd: root, encoding: 'utf8'}).trim();
  git('init', '-q'); git('config', 'user.name', 'Display Test'); git('config', 'user.email', 'display@example.test');
  await fs.writeFile(path.join(root, 'doc.md'), 'initial\n'); git('add', 'doc.md'); git('commit', '-qm', 'initial');
  const previous = process.env.CODEXPRO_JOBS_DIR; process.env.CODEXPRO_JOBS_DIR = path.join(home, 'jobs');
  const config = loadConfig(['--root', root, '--bash', 'full', '--audit', 'metadata', '--audit-log', path.join(home, 'audit.jsonl')]);
  if (previous === undefined) delete process.env.CODEXPRO_JOBS_DIR; else process.env.CODEXPRO_JOBS_DIR = previous;
  const server = createCodexProServer(config);
  const [ct, st] = InMemoryTransport.createLinkedPair(); await server.connect(st);
  const client = new Client({name:'display-test', version:'1'}); await client.connect(ct);
  const opened = await client.callTool({name:'open_current_workspace', arguments:{}});
  const workspace_id = opened.structuredContent.workspace_id;
  return {home, root, config, git, workspace_id, client, journal: new AuditJournal(config),
    call: (name, args = {}) => client.callTool({name, arguments:{workspace_id, ...args}}),
    close: async () => {await client.close(); await server.close(); await fs.rm(home,{recursive:true,force:true});}};
}

test('actual commit and job results survive journal and expanded HTML; output reads are scoped and non-acknowledging', async () => {
  const f = await fixture();
  try {
    await fs.writeFile(path.join(f.root, 'doc.md'), 'changed\n');
    await fs.writeFile(path.join(f.root, 'leftover.txt'), 'keep uncommitted\n');
    const commit = await f.call('commit_changes', {message:'display commit', paths:['doc.md']});
    assert.notEqual(commit.isError,true);
    const sha = f.git('rev-parse','HEAD');
    const started = await f.call('start_jobs', {commands:[{command:"printf '<script>bad()</script>'; printf 'failure detail' >&2; exit 3", label:'test command'}]});
    const [id] = started.structuredContent.job_ids;
    const manager = getJobManager(f.config);
    assert.equal(manager.require(id).acknowledged,false);
    const fragment = renderActivityJobFragment(f.config,id,f.workspace_id);
    assert.match(fragment,/failure detail/); assert.match(fragment,/&lt;script&gt;/); assert.doesNotMatch(fragment,/<script>bad/);
    assert.equal(manager.require(id).acknowledged,false);
    assert.throws(()=>renderActivityJobFragment(f.config,id,'wrong-workspace'));
    assert.throws(()=>renderActivityJobFragment(f.config,'job_deadbeef',f.workspace_id));
    const collected = await f.call('jobs',{job_ids:[id],wait_ms:30000});
    assert.equal(collected.structuredContent.all_succeeded,false);
    const actions = f.journal.list({limit:30}).actions;
    const receipt = actions.find(a=>a.tool_name==='commit_changes');
    assert.equal(receipt.result_metadata.commit,sha);
    assert.equal(receipt.result_metadata.working_tree_clean,false);
    const collection = actions.find(a=>a.tool_name==='jobs');
    assert.equal(collection.result_metadata.jobs[0].job_id,id);
    assert.equal(collection.result_metadata.jobs[0].exit_code,3);
    assert.equal(collection.result_metadata.all_succeeded,false);
    assert.ok(collection.result_metadata.waited_ms<1000);
    assert.equal(collection.result_metadata.jobs[0].stdout,undefined);
    assert.equal(actions.find(a=>a.tool_name==='start_jobs').dashboard_metadata,undefined);
    const snapshot = collectActivityDashboard(f.config,f.journal);
    const html = renderActivityDashboardPage(snapshot);
    assert.ok(html.includes(sha)); assert.ok(html.includes(id));
    assert.match(html,/changes remain/); assert.match(html,/Actual wait/); assert.match(html,/exit 3/);
    assert.match(html,/Git evidence/); assert.match(html,/data-job-refresh/);
    assert.match(html,/may modify files/); assert.doesNotMatch(html,/>wrote files</);
    assert.ok(snapshot.recentActions.find(a=>a.toolName==='start_jobs').shellScripts.length);
    const batch=await f.call('batch',{persist:false,mode:'serial',operations:[{id:'inspect',tool:'read',args:{path:'doc.md'}}]});
    assert.notEqual(batch.isError,true);
    const batchAction=f.journal.list({limit:30}).actions.find(a=>a.tool_name==='batch');
    assert.equal(batchAction.result_metadata.child_results[0].id,'inspect');
    assert.equal(batchAction.result_metadata.child_results[0].ok,true);
    assert.match(renderActivityDashboardPage(collectActivityDashboard(f.config,f.journal)),/Historical operation outcomes/);
    const verification=await f.call('batch',{persist:false,mode:'serial',operations:[{id:'verify',tool:'bash',args:{command:'printf batch-verification-output'}}]});
    assert.notEqual(verification.isError,true);
    const verificationAction=f.journal.list({limit:30}).actions.filter(a=>a.tool_name==='batch').at(-1);
    assert.ok(verificationAction.result_metadata.child_results[0].job_id);
    assert.match(renderActivityDashboardPage(collectActivityDashboard(f.config,f.journal)),/data-job-href/);
    const failed = await f.call('read',{path:'missing-file.txt'});
    assert.equal(failed.isError,true);
    const failure=f.journal.listForDashboard({limit:30}).actions.find(a=>a.tool_name==='read' && a.status==='failed');
    assert.ok(failure.dashboard_metadata.error_message);
    assert.equal(f.journal.get(failure.action_id).dashboard_metadata,undefined);
    const failureView=collectActivityDashboard(f.config,f.journal).recentActions.find(a=>a.actionId===failure.action_id);
    assert.ok(failureView.errorMessage);
    assert.match(renderActivityDashboardPage(collectActivityDashboard(f.config,f.journal)),/missing-file.txt/);
  } finally {await f.close();}
});

test('history uses stable sequence cursors with project filters and exposes retained counts', async () => {
  const f=await fixture();
  try {
    for(let i=0;i<40;i++) f.journal.record({toolName:'read',args:{project_id:'default',workspace_id:f.workspace_id,path:'doc.md'},result:{structuredContent:{bytes:i}},startedAtMs:Date.now(),finishedAtMs:Date.now(),mutating:false});
    const first=collectActivityDashboard(f.config,f.journal,Date.now(),{projectId:'default'});
    assert.equal(first.recentActions.length,30);
    assert.ok(first.history.nextBeforeSequence);
    const second=collectActivityDashboard(f.config,f.journal,Date.now(),{projectId:'default',beforeSequence:first.history.nextBeforeSequence});
    assert.ok(second.recentActions.length>0);
    assert.ok(second.recentActions.every(a=>a.sequence<first.history.nextBeforeSequence));
    assert.equal(new Set([...first.recentActions,...second.recentActions].map(a=>a.sequence)).size,first.recentActions.length+second.recentActions.length);
    assert.match(renderActivityDashboardPage(first),/Older actions/);
    assert.match(renderActivityDashboardPage(first),/project_id=default/);
  } finally {await f.close();}
});


test('job output HTTP route requires authentication and rejects a mismatched workspace', async () => {
  const f=await fixture();
  let child;
  try {
    const started=await f.call('start_jobs',{commands:[{command:"printf 'http-output-marker'"}]});
    const [id]=started.structuredContent.job_ids;
    const port=await new Promise((resolve,reject)=>{const server=net.createServer();server.on('error',reject);server.listen(0,'127.0.0.1',()=>{const port=server.address().port;server.close(()=>resolve(port));});});
    const token='display-test-only-credential-123456789';
    child=spawn(process.execPath,['dist/http.js'],{cwd:process.cwd(),env:{...process.env,CODEXPRO_ROOT:f.root,CODEXPRO_ALLOWED_ROOTS:f.root,CODEXPRO_HOME:f.home,CODEXPRO_JOBS_DIR:f.config.jobsDir,CODEXPRO_PORT:String(port),CODEXPRO_HOST:'127.0.0.1',CODEXPRO_HTTP_TOKEN:token,CODEXPRO_AUTH_MODE:'static-token',CODEXPRO_BASH_MODE:'off'},stdio:['ignore','ignore','pipe']});
    await new Promise((resolve,reject)=>{
      let log='';const timer=setTimeout(()=>reject(new Error('HTTP fixture did not start: '+log)),15000);
      child.stderr.on('data',chunk=>{log+=chunk;if(log.includes('HTTP MCP listening')){clearTimeout(timer);resolve();}});
      child.once('exit',code=>{clearTimeout(timer);reject(new Error('HTTP fixture exited '+code+': '+log));});
    });
    const base=`http://127.0.0.1:${port}/activity/job?job_id=${id}&workspace_id=`;
    assert.equal((await fetch(base+f.workspace_id)).status,401);
    const headers={Authorization:`Bearer ${token}`};
    const response=await fetch(base+f.workspace_id,{headers});
    assert.equal(response.status,200,await response.clone().text());
    assert.match(await response.text(),/http-output-marker/);
    assert.match(response.headers.get('cache-control'),/no-store/);
    assert.equal((await fetch(base+'wrong-workspace',{headers})).status,404);
    assert.equal(getJobManager(f.config).require(id).acknowledged,false);
  } finally {
    if(child && child.exitCode===null){child.kill('SIGTERM');await new Promise(resolve=>{const timer=setTimeout(()=>{child.kill('SIGKILL');resolve();},5000);child.once('exit',()=>{clearTimeout(timer);resolve();});});}
    await f.close();
  }
});
