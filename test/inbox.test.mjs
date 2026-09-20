import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import net from 'node:net';
import vm from 'node:vm';
import {spawn} from 'node:child_process';
import test from 'node:test';
import {InboxStore} from '../dist/inbox/store.js';
import {renderInboxPage} from '../dist/inbox/view.js';
const q=(id='delivery',project_id='default')=>({schema_version:1,id,project_id,source:'worker',title:'Choose delivery',question:'Keep floor delivery?',context:'Item cannot be carried.',options:['Floor','Refuse'],recommendation:'Floor',blocking_scope:'ticket',blocked_work:['COM-04']});

test('questions are idempotent and immutable; answers survive restart with revision conflicts and delivery receipts',()=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'inbox-store-'));let store=new InboxStore(root);
 try{
  const item=store.publish(q());assert.equal(item.status,'pending');assert.deepEqual(store.publish(q()),item);assert.equal(store.events().length,1);
  assert.throws(()=>store.publish({...q(),question:'Changed?'}),/different content/);
  const request={schema_version:1,request_id:'answer-1',expected_revision:1,answer:'Floor delivery is approved.'};
  const answered=store.answer('default','delivery',request,'operator');assert.equal(answered.revision,2);
  assert.deepEqual(store.answer('default','delivery',request,'operator'),answered);assert.equal(store.events().length,2);
  assert.throws(()=>store.answer('default','delivery',{...request,answer:'Refuse'},'operator'),/different content/);
  assert.throws(()=>store.answer('default','delivery',{...request,request_id:'answer-2'},'operator'),/refresh/);
  assert.throws(()=>store.delivered('default','delivery',{schema_version:1,consumer:'loop',revision:1}),/revision changed/);
  store.delivered('default','delivery',{schema_version:1,consumer:'loop',revision:2});store.delivered('default','delivery',{schema_version:1,consumer:'loop',revision:2});assert.equal(store.events().length,3);
  store.publish(q('other','other'));assert.equal(store.list('default').total,1);assert.equal(store.list(undefined,undefined,1).next_offset,1);
  store.close();store=new InboxStore(root);assert.equal(store.get('default','delivery').answer.text,request.answer);
  assert.equal(store.publish(q()).status,'answered','reposts never reopen answered questions');
  const edited=store.answer('default','delivery',{...request,request_id:'answer-2',expected_revision:2,answer:'Updated instruction'},'operator');assert.equal(edited.revision,3);
  assert.equal(edited.deliveries.some(d=>d.revision===edited.revision),false,'new answer needs new delivery');
  assert.equal(store.events('default').filter(e=>e.kind==='question.answered').length,2);
  assert.equal(store.list('default','pending').total,0);
 }finally{store.close();fs.rmSync(root,{recursive:true,force:true});}
});

test('inbox view embeds safe project data and syntactically valid script; question content uses text nodes',()=>{
 const html=renderInboxPage([{id:'evil',label:'</script><img src=x onerror=alert(1)>'}]);
 assert.equal((html.match(/<script>/g)||[]).length,1);assert.doesNotMatch(html,/<img src=x/);
 for(const script of html.matchAll(/<script>([\s\S]*?)<\/script>/g))new vm.Script(script[1]);
 assert.match(html,/textContent=text/);assert.doesNotMatch(html,/innerHTML/);
 assert.match(html,/expected_revision:item.revision/);
});

test('authenticated inbox HTTP supports portable publish, answer, project filters and stale-answer rejection',async()=>{
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'inbox-http-'));
 const listener=net.createServer();await new Promise(r=>listener.listen(0,'127.0.0.1',r));const port=listener.address().port;await new Promise(r=>listener.close(r));
 const base=`http://127.0.0.1:${port}`,token='inbox-fixture-credential-12345678';
 const child=spawn(process.execPath,['dist/http.js'],{env:{...process.env,CODEXPRO_HOME:root,CODEXPRO_ROOT:root,CODEXPRO_HOST:'127.0.0.1',CODEXPRO_PORT:String(port),CODEXPRO_HTTP_TOKEN:token,CODEXPRO_AUDIT_LOG:path.join(root,'audit.jsonl'),CODEXPRO_JOBS_DIR:path.join(root,'jobs')},stdio:['ignore','ignore','pipe']});let diagnostics='';child.stderr.on('data',x=>diagnostics+=x);
 const headers={Authorization:`Bearer ${token}`,'Content-Type':'application/json'};
 const post=(route,body,extra={})=>fetch(base+route,{method:'POST',headers:{...headers,...extra},body:JSON.stringify(body)});
 try{
  for(let i=0;i<150;i++){try{if((await fetch(base+'/healthz',{headers})).ok)break;}catch{}if(i===149)assert.fail(diagnostics);await new Promise(r=>setTimeout(r,40));}
  assert.equal((await fetch(base+'/inbox/v1/items')).status,401);
  assert.equal((await fetch(base+'/activity/inbox')).status,401);
  assert.equal((await post('/inbox/v1/items',q(),{Origin:'https://evil.example'})).status,403);
  assert.equal((await post('/inbox/v1/items',{...q(),answer:'forged answer'})).status,400);
  assert.equal((await post('/inbox/v1/items',{...q(),source_url:'javascript:alert(1)'})).status,400);
  assert.equal((await post('/inbox/v1/items',q('x','absent'))).status,404);
  const result=await post('/inbox/v1/items',q());assert.equal(result.status,200);assert.equal((await result.json()).revision,1);
  const answer={schema_version:1,request_id:'http-1',expected_revision:1,answer:'Proceed'};
  assert.equal((await post('/inbox/v1/items/default/delivery/answers',answer)).status,200);
  assert.equal((await post('/inbox/v1/items/default/delivery/answers',answer)).status,200);
  assert.equal((await post('/inbox/v1/items/default/delivery/answers',{...answer,request_id:'http-2'})).status,409);
  const list=await(await fetch(base+'/inbox/v1/items?project_id=default&status=answered',{headers})).json();assert.equal(list.total,1);assert.equal(list.items[0].answer.text,'Proceed');
  for(const query of ['status=unknown','limit=101','offset=-1','project_id=default&project_id=other'])assert.ok((await fetch(base+'/inbox/v1/items?'+query,{headers})).status>=400);
  assert.match(await(await fetch(base+'/activity/inbox',{headers})).text(),/Decision inbox/);
  assert.match(await(await fetch(base+'/activity',{headers})).text(),/data-inbox-count/);
  assert.equal((await fetch(base+'/inbox/v1/items',{headers})).headers.get('cache-control'),'no-store');
 }finally{child.kill('SIGTERM');if(child.exitCode===null)await new Promise(r=>child.once('exit',r));fs.rmSync(root,{recursive:true,force:true});}
});
