// Disposable Linux/systemd rehearsal. Never reads or writes the live job store.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { loadConfig } from '../dist/config.js';
import { JobManager } from '../dist/jobs.js';
import { makeRestrictedBashEnv } from '../dist/bashOps.js';
import { inspectScope, signalScope } from '../dist/jobScope.js';
import { runWithToolContext } from '../dist/toolContext.js';

function run(command, args) {
  const result = spawnSync(command, args, {encoding:'utf8', timeout:20000});
  if (result.error || result.status !== 0) throw new Error(`${path.basename(command)} failed: ${result.stderr}`);
  return result.stdout;
}
const quote = text => `'${text.replaceAll("'", "'\\''")}'`;
const environmentCommand = filename => `node -e ${quote(`require('fs').writeFileSync(${JSON.stringify(filename)},JSON.stringify({tmp:process.env.TMPDIR,goTmp:process.env.GOTMPDIR,cache:process.env.GOCACHE,output:process.env.CODEXPRO_JOB_OUTPUT_DIR}))`)}`;
function configFor(root, helper) {
  return {...loadConfig(['--root',root,'--bash','full','--audit','off']),jobsDir:path.join(root,'jobs'),hostResourcesHelper:helper};
}
function common(root, config) {
  return {workspaceId:'ws_resource_rehearsal', root, cwdAbs:root, cwdLabel:'.',
    env:makeRestrictedBashEnv(config,{...process.env,TMPDIR:'/tmp',GOTMPDIR:'/tmp/stale-go-tmp',GOCACHE:'/tmp/stale-go-cache'}),
    origin:'background', outputLimitBytes:65536};
}
async function readWhenPresent(filename) {
  for (let n=0;n<150;n++) { try { return JSON.parse(fs.readFileSync(filename,'utf8')); } catch {} await delay(100); }
  throw new Error(`Timed out waiting for ${path.basename(filename)}`);
}
function inspect(helper, claim) { return JSON.parse(run(helper,['inspect',claim.run_id])); }
async function quiet(manager, id) {
  const job = await manager.wait(id,20000);
  for(let n=0;n<100&&!job.quiescent;n++) await delay(100);
  assert.equal(job.quiescent,true,JSON.stringify({id,status:job.status,quiescent:job.quiescent}));
  return job;
}

if (process.argv[2] === '--parent') {
  const [root, helper] = process.argv.slice(3);
  const config = configFor(root,helper);
  const manager = new JobManager(config);
  const jobs = [
    manager.start({...common(root,config),command:'sleep 2; echo survived-resource-restart',timeoutMs:8000}),
    manager.start({...common(root,config),command:'sleep 20',timeoutMs:1200}),
    manager.start({...common(root,config),command:`${environmentCommand('terminal.env.json')}; sleep 30`,timeoutMs:40000})
  ];
  await readWhenPresent(path.join(root,'terminal.env.json'));
  // Isolated fault injection: persist command completion before owner quiescence
  // and then let the external controller remove this manager process.
  clearInterval(manager.poller); manager.poller=undefined;
  const terminal = jobs[2];
  terminal.status='failed'; terminal.exit_code=1; terminal.stop_reason='lost'; terminal.quiescent=false;
  terminal.finished_at_ms=Date.now(); terminal.finished_at=new Date(terminal.finished_at_ms).toISOString();
  fs.writeFileSync(path.join(config.jobsDir,'jobs.json'),JSON.stringify({version:1,jobs}),{mode:0o600});
  fs.writeFileSync(path.join(root,'ready.json'),JSON.stringify(jobs.map(job=>job.id)),{mode:0o600});
  setInterval(()=>{},1000);
} else {
  assert.equal(process.platform,'linux','Run this rehearsal on Linux');
  const helper = process.argv[2];
  assert.ok(helper && path.isAbsolute(helper),'Supply the absolute host helper path');
  const base = path.join(os.homedir(),'.cache','codexpro-resource-rehearsals');
  fs.mkdirSync(base,{recursive:true,mode:0o700});
  const root=fs.realpathSync(fs.mkdtempSync(path.join(base,'run-')));
  const wrapper=path.join(root,'isolated-helper');
  const policyFile=path.join(root,'host-policy.json');
  const policy={version:1,state_root:path.join(root,'host-state'),scratch_root:path.join(root,'host-scratch'),go_cache:path.join(root,'go-cache'),minimum_free_bytes:0,go_cache_eviction_enabled:true};
  fs.writeFileSync(policyFile,JSON.stringify(policy),{mode:0o600});
  fs.writeFileSync(wrapper,`#!/bin/sh\nexec ${quote(helper)} --config ${quote(policyFile)} "$@"\n`,{mode:0o700});
  process.env.CODEXPRO_JOB_SCOPES='1';
  const unit=`codexpro-resource-rehearsal-${process.pid}`;
  const all=[];
  let complete=false;
  try {
    const config=configFor(root,wrapper);
    const manager=new JobManager(config);
    const overlaps=['a','b'].map(name=>manager.start({...common(root,config),command:`${environmentCommand(`${name}.env.json`)}; sleep 3; echo ${name}`,timeoutMs:10000}));
    all.push(...overlaps);
    const environments=await Promise.all(['a','b'].map(name=>readWhenPresent(path.join(root,`${name}.env.json`))));
    assert.notEqual(environments[0].tmp,environments[1].tmp);
    for (let n=0;n<2;n++) {
      const env=environments[n]; const inspection=inspect(wrapper,overlaps[n].resource_claim);
      assert.ok(env.tmp.startsWith(policy.scratch_root+'/')); assert.ok(env.goTmp.startsWith(policy.scratch_root+'/'));
      assert.equal(path.dirname(env.tmp),path.dirname(env.goTmp));
      assert.equal(env.cache,policy.go_cache); assert.ok(env.output.startsWith(root+'/jobs/'));
      assert.equal(inspection.alive,true); assert.equal(inspection.receipt.cache_consumer,true);
      const registration=JSON.parse(fs.readFileSync(path.join(config.jobsDir,`${overlaps[n].id}.spec.json.started`),'utf8')).scope_identity;
      const foreign={...registration,invocation_id:'0'.repeat(32)};
      assert.equal(inspectScope(foreign),'mismatch'); assert.equal(signalScope(foreign,'SIGKILL'),false);
      assert.equal(inspect(wrapper,overlaps[n].resource_claim).alive,true,'Foreign invocation check signalled the active owner');
    }
    const maintenance=JSON.parse(run(wrapper,['cache-gc','--apply']));
    assert.ok(JSON.stringify(maintenance).includes('consumer')||JSON.stringify(maintenance).includes('active'),JSON.stringify(maintenance));
    for (const job of overlaps) { await quiet(manager,job.id); assert.equal(job.status,'succeeded'); assert.equal(inspect(wrapper,job.resource_claim).quiescent,true); }
    for (const env of environments) assert.equal(fs.existsSync(env.tmp),false,'Successful scratch remained after whole-owner settlement');
    console.log('✓ overlapping restricted jobs use unique disk scratch, shared cache and durable whole-scope claims');

    const program="const fs=require('fs');const {spawn}=require('child_process');const c=spawn('/bin/sh',['-c','while true; do sleep 1; done'],{detached:true,stdio:'ignore'});fs.writeFileSync('daemon.pid',String(c.pid));c.unref();";
    const daemon=manager.start({...common(root,config),command:`node -e ${quote(program)}`,timeoutMs:8000}); all.push(daemon);
    await quiet(manager,daemon.id); assert.equal(daemon.status,'succeeded');
    const pid=fs.readFileSync(path.join(root,'daemon.pid'),'utf8');
    const state=spawnSync('ps',['-p',pid,'-o','stat='],{encoding:'utf8'}).stdout.trim();
    assert.ok(!state||state.startsWith('Z'),'Detached descendant survived whole-scope settlement');
    console.log('✓ detached descendants are drained before host scratch cleanup');

    const captureFailure=runWithToolContext({principalId:'rehearsal',requestId:'rehearsal',signal:new AbortController().signal,
      workJobPrepared:id=>fs.writeFileSync(path.join(config.jobsDir,`${id}.spec.json.started`),'{}',{mode:0o600})},
      ()=>manager.start({...common(root,config),command:'echo must-not-run',timeoutMs:8000}));
    all.push(captureFailure); await quiet(manager,captureFailure.id);
    assert.equal(captureFailure.status,'failed'); assert.equal(captureFailure.scope_identity,undefined);
    assert.equal(inspect(wrapper,captureFailure.resource_claim).quiescent,true);
    assert.ok(!manager.readTail(captureFailure,1024).stdout.includes('must-not-run'));
    console.log('✓ failed local scope-proof publication recovers through the original host registration');

    const restartRoot=path.join(root,'restart'); fs.mkdirSync(restartRoot,{mode:0o700});
    run('systemd-run',['--user','--quiet','--collect',`--unit=${unit}`,'--property=KillMode=mixed','--property=TimeoutStopSec=5s',`--working-directory=${process.cwd()}`,'--setenv=CODEXPRO_JOB_SCOPES=1',process.execPath,path.resolve('scripts/job-resources-smoke.mjs'),'--parent',restartRoot,wrapper]);
    const ids=await readWhenPresent(path.join(restartRoot,'ready.json'));
    run('systemctl',['--user','stop',`${unit}.service`]);
    await delay(3000);
    const recovered=new JobManager(configFor(restartRoot,wrapper));
    const jobs=[];
    for(const id of ids) jobs.push(await quiet(recovered,id));
    all.push(...jobs);
    assert.equal(jobs[0].status,'succeeded'); assert.equal(jobs[1].status,'timed_out'); assert.equal(jobs[2].status,'failed');
    assert.ok(recovered.readTail(jobs[0],1024).stdout.includes('survived-resource-restart'));
    for(const job of jobs) assert.equal(inspect(wrapper,job.resource_claim).quiescent,true);
    console.log('✓ restart preserves independent deadline/completion and reconciles terminal unverified scope/resources');
    complete=true;
  } finally {
    spawnSync('systemctl',['--user','stop',`${unit}.service`],{stdio:'ignore'});
    for(const job of all) if(job.scope_unit) spawnSync('systemctl',['--user','stop',`${job.scope_unit}.scope`],{stdio:'ignore'});
    if(complete) fs.rmSync(root,{recursive:true,force:true});
    else console.error(`Failed disposable rehearsal retained at ${root}`);
  }
}
