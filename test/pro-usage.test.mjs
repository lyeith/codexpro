import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import vm from 'node:vm';
import test from 'node:test';
import express from 'express';
import {spawnSync} from 'node:child_process';
import {ProUsageStore} from '../dist/usage/store.js';
import {registerProUsage} from '../dist/usage/routes.js';
import {proUsageScript,proUsageHtml} from '../dist/usage/view.js';
const WEEK=604800000;
const coverage={unconfirmed:2,failed_before_send:3,unknown_mode:1,oldest_record_at:null};
const batch=(submissions)=>({schema_version:1,collector_id:'fixture',submissions,coverage});
const receipt=(turn,at,project='default')=>({query_id:'query',turn_id:turn,project_id:project,submitted_at:new Date(at).toISOString(),mode_evidence:'selected'});
const limits=(now,anchor,extra={})=>({schema_version:1,checked_at:new Date(now).toISOString(),weekly:{resets_at:new Date(anchor).toISOString(),window_minutes:10080,used_percent:21},...extra});
function fixture(fn){const root=fs.mkdtempSync(path.join(os.tmpdir(),'pro-usage-'));let now=Date.parse('2026-09-21T00:00:00Z');const store=new ProUsageStore(root,()=>now);try{fn(store,v=>now=v,now,root);}finally{store.close();fs.rmSync(root,{recursive:true,force:true});}}

test('replayed receipts are exactly once, retain historical projects, and can enrich evidence',()=>fixture((store,_set,now)=>{
 const a=receipt('a',now-5000,null);a.mode_evidence='requested';
 assert.equal(store.ingest(batch([a])).inserted,1);
 assert.equal(store.ingest(batch([a])).inserted,0);
 store.ingest(batch([{...a,project_id:'removed-project',mode_evidence:'selected'}]));
 store.ingest(batch([a]));
 assert.equal(store.snapshot().count,1);assert.equal(store.snapshot().requested_mode_count,0);
 assert.equal(store.snapshot('removed-project').count,1);assert.equal(store.snapshot('default').count,0);
 assert.throws(()=>store.ingest(batch([{...a,submitted_at:new Date(now).toISOString()}])),/Conflicting timestamp/);
 assert.throws(()=>store.ingest(batch([{...a,project_id:'different'}])),/Conflicting project/);
 assert.equal(store.snapshot().count,1);
}));

test('weekly rollover uses API dates, catches up after downtime, and validates the exact datetime',()=>fixture((store,set,now)=>{
 const anchor=now+1000;
 store.ingest(batch([receipt('before',now-1),receipt('at',anchor),receipt('old',anchor-WEEK-1)]));
 store.syncLimits(limits(now,anchor));assert.equal(store.snapshot().count,1);
 set(anchor);assert.equal(store.snapshot().count,1);assert.equal(store.snapshot().next_weekly_reset_at,null);
 assert.equal(store.snapshot().reset_date_stale,true);
 store.syncLimits(limits(anchor,anchor+WEEK));assert.equal(store.snapshot().count,1);
 set(anchor+3*WEEK+1000);store.syncLimits(limits(anchor+3*WEEK+1000,anchor+4*WEEK));
 assert.equal(store.snapshot().count,0);assert.equal(store.snapshot().all_time_count,3);
 assert.throws(()=>store.syncLimits(limits(now,anchor)),/stale/);
 assert.throws(()=>store.syncLimits(limits(anchor+3*WEEK+1000,anchor+3*WEEK)),/outside/);
}));

test('manual reset waits for a fresh successful API check, preserves post-click requests and weekly schedule',()=>fixture((store,set,now,root)=>{
 const anchor=now+100000;store.syncLimits(limits(now,anchor));
 store.ingest(batch([receipt('before',now-1000)]));
 const request={schema_version:1,request_id:'manual-1',expected_revision:store.settings().revision};
 const pending=store.reset(request,'operator');assert.equal(store.snapshot().count,1);
 assert.deepEqual(store.reset(request,'operator'),pending);
 assert.throws(()=>store.reset({...request,request_id:'manual-2'},'operator'),/refresh/);
 set(now+2000);store.ingest(batch([receipt('after',now+1000)]));
 store.syncLimits({schema_version:1,checked_at:new Date(now+2000).toISOString(),weekly:null,reset_request_id:'manual-1'});
 assert.equal(store.snapshot().count,2);assert.ok(store.settings().pending_reset);assert.ok(store.settings().limits_error);
 store.syncLimits(limits(now+2000,anchor));assert.ok(store.settings().pending_reset,'normal cached sync cannot fulfill reset');
 store.syncLimits(limits(now+2000,anchor,{reset_request_id:'manual-1'}));
 assert.equal(store.snapshot().count,1);assert.equal(store.settings().pending_reset,null);
 assert.equal(store.snapshot().next_weekly_reset_at,new Date(anchor).toISOString());
 store.ingest(batch([receipt('late-backfill',now-2000)]));assert.equal(store.snapshot().count,1);assert.equal(store.snapshot().all_time_count,3);
 store.syncLimits(limits(now+2000,anchor,{reset_request_id:'manual-1'}));
 assert.equal(store.history().filter(x=>x.kind==='reset.completed').length,1);
 const reopened=new ProUsageStore(root,()=>now+2000);try{assert.equal(reopened.snapshot().count,1);}finally{reopened.close();}
 set(anchor);assert.equal(store.snapshot().count,0);
}));

test('older valid observation cannot roll the reset date backward',()=>fixture((store,set,now)=>{
 store.syncLimits(limits(now,now+100000));set(now+2000);
 store.syncLimits(limits(now+2000,now+110000));
 assert.throws(()=>store.syncLimits(limits(now+1000,now+100000)),/newer/);
 assert.equal(store.settings().weekly_anchor_at,new Date(now+110000).toISOString());
}));

test('usage routes require the enclosing auth and reject cross-origin writes and invalid schemas',async()=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'pro-http-'));const app=express();
 app.use((req,res,next)=>req.headers.authorization==='Bearer test-token'?next():res.sendStatus(401));
 const sameOrigin=(req,res,next)=>!req.headers.origin||req.headers.origin==='http://allowed'?next():res.sendStatus(403);
 registerProUsage(app,{auditLogPath:path.join(root,'audit.jsonl'),projects:[{id:'default'}]},sameOrigin,()=> 'fixture');
 const server=app.listen(0,'127.0.0.1');await new Promise(r=>server.once('listening',r));const base='http://127.0.0.1:'+server.address().port;
 const headers={Authorization:'Bearer test-token','Content-Type':'application/json'};
 const post=(route,body,extra={})=>fetch(base+'/usage/v1/pro'+route,{method:'POST',headers:{...headers,...extra},body:JSON.stringify(body)});
 try{
  assert.equal((await fetch(base+'/usage/v1/pro')).status,401);
  assert.equal((await post('/submissions',batch([]),{Origin:'https://evil.example'})).status,403);
  assert.equal((await post('/submissions',{...batch([]),prompt:'do not accept prompts'})).status,400);
  assert.equal((await post('/submissions',batch([receipt('http',Date.now()-1000)]))).status,200);
  const status=await (await fetch(base+'/usage/v1/pro?project_id=default',{headers})).json();assert.equal(status.count,1);
  assert.equal((await fetch(base+'/usage/v1/pro?project_id=missing',{headers})).status,404);
  const request={schema_version:1,request_id:'http-reset',expected_revision:status.settings.revision};
  assert.equal((await post('/reset',request)).status,200);assert.equal((await post('/reset',request)).status,200);
  assert.equal((await post('/limits',limits(Date.now(),Date.now()+10000,{reset_request_id:'http-reset'}))).status,200);
  const after=await (await fetch(base+'/usage/v1/pro',{headers})).json();assert.equal(after.count,0);assert.equal(after.all_time_count,1);
 }finally{await new Promise(r=>server.close(r));fs.rmSync(root,{recursive:true,force:true});}
});

test('usage component has valid script and safe dynamic text',()=>{
 new vm.Script(proUsageScript);assert.doesNotMatch(proUsageScript,/innerHTML/);
 assert.match(proUsageHtml,/data-pro-reset/);assert.match(proUsageScript,/expected_revision/);
});

test('collector recovery, history parsing and API window selection',()=>{
 const result=spawnSync('python3',['test/pro_usage_test.py'],{encoding:'utf8'});
 assert.equal(result.status,0,result.stdout+result.stderr);
});
