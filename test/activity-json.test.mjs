import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import net from 'node:net';
import { spawn } from 'node:child_process';
import test from 'node:test';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { loadConfig } from '../dist/config.js';
import { AuditJournal } from '../dist/audit.js';
import { collectActivityJson } from '../dist/activityDashboard/json.js';

async function fixture() {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-activity-json-')));
  const config = loadConfig(['--root', root, '--audit', 'metadata', '--audit-log', path.join(root, 'state/audit.jsonl')]);
  config.projects = [{ id: 'alpha', label: 'Alpha', root }, { id: 'beta', label: 'Beta', root: path.join(root, 'beta') }];
  config.jobsDir = path.join(root, 'jobs');
  const journal = new AuditJournal(config);
  const now = Date.now();
  const iso = offset => new Date(now + offset).toISOString();
  const record = (index, project = 'alpha', result = {}, tool = 'bash') => journal.record({ toolName: tool,
    args: { project_id: project, workspace_id: `wt_${project}`, command: `printf 'command-${index}'` },
    result: { structuredContent: result }, startedAtMs: now - 20_000 + index * 100, finishedAtMs: now - 19_000 + index * 100, mutating: true });
  const job = async (id, overrides = {}) => {
    const stdout = path.join(root, `${id}.out`), stderr = path.join(root, `${id}.err`);
    await fs.writeFile(stdout, 'old output\n'); await fs.writeFile(stderr, '');
    await fs.utimes(stdout, new Date(now - 400_000), new Date(now - 400_000));
    return { id, project_id: 'alpha', workspace_id: 'wt_alpha', root: path.join(root, 'worktree'),
      command: 'sleep 500', cwd: '.', status: 'running', origin: 'background', started_at: iso(-500_000), started_at_ms: now - 500_000,
      deadline_ms: now + 500_000, stdout_path: stdout, stderr_path: stderr, exit_code: null, ...overrides };
  };
  return { root, config, journal, now, iso, record, job, cleanup: () => fs.rm(root, {recursive: true, force: true}) };
}

function manager(jobs, reads = []) {
  return { list: () => jobs, readTail: (job, budget) => {
    reads.push(job.id);
    return { stdout: 'A'.repeat(budget), stderr: 'B'.repeat(budget), stdout_bytes: 5000, stderr_bytes: 5000, truncated: true };
  } };
}

test('global summaries avoid output; project JSON is isolated, bounded and exposes quiet evidence', async () => {
  const f = await fixture();
  try {
    for (let i = 0; i < 18; i++) { f.record(i, 'alpha', {job_id: `job_${i}`}); f.record(i, 'beta'); }
    const quiet = await f.job('job_11111111');
    const fresh = await f.job('job_22222222', {project_id: 'beta', workspace_id: 'wt_beta'});
    await fs.utimes(fresh.stdout_path, new Date(f.now), new Date(f.now));
    const reads = [];
    const dependencies = { journal: f.journal, manager: manager([quiet, fresh], reads), nowMs: f.now };
    const global = collectActivityJson(f.config, {}, dependencies);
    assert.equal(reads.length, 0, 'summary never reads output bodies');
    assert.equal(global.projects.length, 2);
    assert.equal(global.projects[0].has_inflight_work, true);
    assert.equal(global.projects[0].review_recommended, true);
    assert.equal(global.projects[1].review_recommended, false);
    assert.equal(global.projects[0].recent_commands, undefined);
    const detail = collectActivityJson(f.config, {projectId: 'alpha', limit: 5, outputBytes: 100}, dependencies);
    assert.equal(detail.project.recent_commands.length, 5);
    assert.equal(detail.project.inflight.jobs.length, 1);
    assert.equal(detail.project.inflight.jobs[0].quiet_for_ms, 400_000);
    assert.equal(detail.project.inflight.jobs[0].output.returned_bytes, 100);
    assert.equal(detail.project.last_command_started_age_ms, 18_300);
    assert.equal(detail.project.last_command_finished_age_ms, 17_300);
    assert.ok(detail.project.recent_commands.every(item => item.workspace_id === 'wt_alpha'));
    assert.doesNotMatch(JSON.stringify(detail), /wt_beta|job_22222222/);
    assert.equal(detail.project.recent_commands[0].output.available, false, 'missing output is explicit');
    assert.throws(() => collectActivityJson(f.config, {projectId: 'unknown'}, dependencies), /unknown_project/);
    for (const options of [{limit: 11}, {limit: 0}, {outputBytes: -1}, {outputBytes: 4097}, {quietAfterMs: NaN}]) {
      assert.throws(() => collectActivityJson(f.config, options, dependencies), /invalid_options/);
    }
  } finally { await f.cleanup(); }
});

test('JSON projects queued calls, stale claims and unclaimed ready runs without touching coordinator state', async () => {
  const f = await fixture();
  let id;
  try {
    id = f.journal.begin({toolName:'bash', args:{command:'sleep 900'}, before:{project_id:'alpha',workspace_id:'wt_alpha',paths:[],targets:[]}, startedAtMs:f.now-600_000,mutating:true});
    const work = { coordinator: { store: {
      runs: () => [{id:'run_active',project_id:'alpha',state:'active',iteration_id:'iteration-1',updated_at:f.iso(-600_000)}, {id:'run_ready',project_id:'alpha',state:'ready',updated_at:f.iso(-600_000)}],
      get: () => ({last_contact_at:f.iso(-350_000),last_progress_at:f.iso(-450_000)})
    } } };
    const detail = collectActivityJson(f.config, {projectId:'alpha'}, {journal:f.journal,manager:manager([]),work,nowMs:f.now});
    assert.equal(detail.project.inflight.calls[0].state,'queued');
    assert.equal(detail.project.inflight_counts.claims,1);
    assert.deepEqual(detail.project.signals.map(signal => signal.kind), ['long_running_call','claim_contact_quiet','run_ready_unclaimed']);
    assert.equal(detail.project.work_runs[0].last_contact_age_ms,350_000);
    assert.equal(detail.project.last_command_started_age_ms,600_000);
    assert.doesNotMatch(JSON.stringify(detail),/token_hash|principal_id|attempt_token/);
  } finally { f.journal.end(id); await f.cleanup(); }
});

test('command and output budgets hold, duplicates fold, expired logs and disabled auditing are explicit', async () => {
  const f = await fixture();
  try {
    const jobs = await Promise.all(Array.from({length:12},(_,i)=>f.job(`job_${i}`,{status:'succeeded',exit_code:0,finished_at:f.iso(-1000+i),finished_at_ms:f.now-1000+i,command:'😀'.repeat(1000),output_expired:i===11})));
    f.record(17,'alpha',{job_id:'job_0',job_status:'succeeded'});
    f.record(18,'alpha',{job_id:'job_0',job_status:'succeeded'},'bash_job');
    let detail=collectActivityJson(f.config,{projectId:'alpha',limit:10,outputBytes:4096},{journal:f.journal,manager:manager(jobs),nowMs:f.now});
    assert.equal(detail.project.recent_commands.length,10);
    assert.ok(detail.project.recent_commands.every(item=>Buffer.byteLength(item.command)<=1024 && !item.command.includes('�')));
    assert.equal(detail.project.recent_commands[0].output.reason,'expired');
    assert.ok(detail.project.recent_commands.reduce((sum,item)=>sum+(item.output.returned_bytes??0),0)<=32768);
    assert.equal(detail.project.recent_commands.filter(item=>item.job_ids.includes('job_0')).length,0, 'older job omitted by recency');
    const only=collectActivityJson(f.config,{projectId:'alpha'},{journal:f.journal,manager:manager([jobs[0]]),nowMs:f.now});
    assert.equal(only.project.recent_commands.filter(item=>item.job_ids.includes('job_0')).length,1);
    detail=collectActivityJson({...f.config,auditMode:'off'},{projectId:'alpha',outputBytes:0},{manager:manager(jobs),nowMs:f.now});
    assert.equal(detail.coverage.audit_enabled,false);
    assert.equal(detail.project.recent_commands.length,8);
    assert.equal(detail.project.recent_commands[1].output.reason,'not_requested');
    assert.equal(detail.project.recent_commands[1].source,'job_store');
  } finally { await f.cleanup(); }
});

test('authenticated HTTP JSON is available before completion, validates filters and returns bounded output without acknowledgement', async () => {
  const f = await fixture();
  const listener=net.createServer(); await new Promise(resolve=>listener.listen(0,'127.0.0.1',resolve));
  const port=listener.address().port; await new Promise(resolve=>listener.close(resolve));
  const base=`http://127.0.0.1:${port}`, token='activity-json-fixture-auth-1234';
  const child=spawn(process.execPath,['dist/http.js'],{env:{...process.env,CODEXPRO_HOME:path.join(f.root,'home'),CODEXPRO_ROOT:f.root,CODEXPRO_HOST:'127.0.0.1',CODEXPRO_PORT:String(port),CODEXPRO_HTTP_TOKEN:token,CODEXPRO_BASH_MODE:'full',CODEXPRO_TOOL_MODE:'full',CODEXPRO_AUDIT_MODE:'metadata',CODEXPRO_AUDIT_LOG:f.config.auditLogPath,CODEXPRO_JOBS_DIR:f.config.jobsDir},stdio:['ignore','ignore','pipe']});
  let diagnostics='';child.stderr.on('data',data=>{diagnostics+=data;});
  const headers={Authorization:`Bearer ${token}`};
  let client,pending;
  try {
    for(let i=0;i<150;i++) {try {if((await fetch(base+'/healthz',{headers})).ok)break;}catch{} if(i===149)assert.fail(diagnostics);await new Promise(resolve=>setTimeout(resolve,50));}
    for(const route of ['/activity.json','/activity/projects/default.json'])assert.equal((await fetch(base+route)).status,401);
    assert.equal((await fetch(base+'/activity/projects/absent.json',{headers})).status,404);
    for(const query of ['limit=11','output_bytes=NaN','limit=1&limit=2','project_id='])assert.equal((await fetch(base+'/activity.json?'+query,{headers})).status,400);
    client=new Client({name:'activity-json-test',version:'1'});
    await client.connect(new StreamableHTTPClientTransport(new URL(base+'/mcp'),{requestInit:{headers}}));
    const opened=await client.callTool({name:'open_current_workspace',arguments:{}});
    const workspace=opened.structuredContent.workspace_id;
    const result=await client.callTool({name:'start_jobs',arguments:{workspace_id:workspace,commands:[{command:'printf "ready\\n"; while [ ! -f finish ]; do sleep 0.05; done; printf "done\\n"'}]}});
    const jobId=result.structuredContent.job_ids[0];
    pending=client.callTool({name:'bash',arguments:{workspace_id:workspace,command:'while [ ! -f finish ]; do sleep 0.05; done',timeout_ms:30000}});
    let detail;
    for(let i=0;i<100;i++){detail=await(await fetch(base+'/activity/projects/default.json?output_bytes=80',{headers})).json();if(detail.project.inflight_counts.tool_calls)break;await new Promise(resolve=>setTimeout(resolve,20));}
    assert.equal(detail.project.inflight_counts.jobs,2);
    assert.equal(detail.project.inflight_counts.tool_calls,1);
    assert.equal(detail.project.inflight.jobs.find(job=>job.job_id===jobId).output.stdout,'ready\n');
    assert.equal(detail.project.has_inflight_work,true);
    await fs.writeFile(path.join(f.root,'finish'),'');await pending;
    for(let i=0;i<100;i++){detail=await(await fetch(base+'/activity.json?project_id=default',{headers})).json();if(!detail.project.has_inflight_work)break;await new Promise(resolve=>setTimeout(resolve,20));}
    assert.equal(detail.project.has_inflight_work,false);
    assert.ok(detail.project.recent_commands.some(item=>item.output.stdout?.includes('done')));
    const table=JSON.parse(await fs.readFile(path.join(f.config.jobsDir,'jobs.json'),'utf8'));
    assert.equal(table.jobs.find(job=>job.id===jobId).acknowledged,false);
    assert.equal((await fetch(base+'/activity.json',{headers})).headers.get('cache-control'),'no-store');
  } finally {
    await fs.writeFile(path.join(f.root,'finish'),'');await pending;await client?.close();
    child.kill('SIGTERM');if(child.exitCode===null)await new Promise(resolve=>child.once('exit',resolve));await f.cleanup();
  }
});
