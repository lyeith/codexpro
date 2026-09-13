// Isolated Linux/systemd rehearsal. Never uses the live job table or service.
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { loadConfig } from '../dist/config.js';
import { JobManager } from '../dist/jobs.js';

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const parent = process.argv[2] === '--parent';
if (parent) {
  const root = process.argv[3];
  const config = {...loadConfig(['--root',root,'--bash','full','--audit','off']),jobsDir:path.join(root,'jobs')};
  const manager = new JobManager(config);
  if (!manager.scopesEnabled) throw new Error('systemd scopes were not enabled');
  const common = {workspaceId:'restart-test',root,cwdAbs:root,cwdLabel:'.',env:process.env,origin:'background',outputLimitBytes:65536};
  const jobs = [manager.start({...common,command:'sleep 2; echo survived-restart',timeoutMs:6000}),manager.start({...common,command:'sleep 20',timeoutMs:1200})];
  await fs.writeFile(path.join(root,'ready.json'),JSON.stringify(jobs.map(j=>j.id)));
  setInterval(()=>{},1000);
} else {
  if (process.platform !== 'linux') throw new Error('Run this rehearsal on the isolated Linux host');
  const root=await fs.mkdtemp(path.join(os.tmpdir(),'codexpro-restart-rehearsal-'));
  const unit=`codexpro-rehearsal-${process.pid}`;
  const run=(cmd,args)=>{const r=spawnSync(cmd,args,{encoding:'utf8',timeout:15000});if(r.status!==0)throw new Error(`${cmd} failed: ${r.stderr}`);return r;};
  try {
    run('systemd-run',['--user','--quiet','--collect',`--unit=${unit}`,'--property=KillMode=mixed','--property=TimeoutStopSec=5s',`--working-directory=${process.cwd()}`,'--setenv=CODEXPRO_JOB_SCOPES=1',`--setenv=CODEXPRO_HOME=${root}`,process.execPath,path.resolve('scripts/job-restart-smoke.mjs'),'--parent',root]);
    let ids;
    for(let i=0;i<100;i++){try{ids=JSON.parse(await fs.readFile(path.join(root,'ready.json'),'utf8'));break;}catch{await sleep(100);}}
    if(!ids)throw new Error('Disposable service did not start jobs');
    run('systemctl',['--user','stop',`${unit}.service`]);
    await sleep(3000);
    const config={...loadConfig(['--root',root,'--bash','full','--audit','off']),jobsDir:path.join(root,'jobs')};
    const manager=new JobManager(config);
    const [done,timed]=ids.map(id=>manager.require(id));
    if(done.status!=='succeeded'||timed.status!=='timed_out')throw new Error(JSON.stringify({done:done.status,timed:timed.status}));
    if(!manager.readTail(done,1024).stdout.includes('survived-restart'))throw new Error('Surviving job output missing');
    console.log('✓ isolated systemd stop/reconnect: surviving job completed; other job enforced its deadline without MCP');
  }finally{
    spawnSync('systemctl',['--user','stop',`${unit}.service`],{stdio:'ignore'});
    try {const records=JSON.parse(await fs.readFile(path.join(root,'jobs','jobs.json'),'utf8')).jobs;
      for(const j of records)if(j.scope_unit)spawnSync('systemctl',['--user','stop',`${j.scope_unit}.scope`],{stdio:'ignore'});
    }catch{}
    await fs.rm(root,{recursive:true,force:true});
  }
}
