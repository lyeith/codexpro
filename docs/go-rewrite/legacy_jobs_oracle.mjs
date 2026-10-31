#!/usr/bin/env node
// Read the exact release's exported JobOutputStore against a private snapshot.
// Never instantiate JobManager: that would reconcile/adopt persisted jobs.
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const args = new Map();
for (let i=2;i<process.argv.length;i+=2) {
  if (!process.argv[i]?.startsWith('--') || process.argv[i+1]===undefined) throw Error('Flags require values');
  if (args.has(process.argv[i])) throw Error('Duplicate flag');
  args.set(process.argv[i],process.argv[i+1]);
}
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const inside = (base,value) => value===base || value.startsWith(base+path.sep);
function privatePath(value,existing=true) {
  if (!value || !path.isAbsolute(value) || path.normalize(value)!==value) throw Error('Absolute clean private path required');
  const resolved = existing?fs.realpathSync(value):path.join(fs.realpathSync(path.dirname(value)),path.basename(value));
  if (resolved!==value) throw Error('Symlinked paths are not accepted');
  for (const home of new Set([os.homedir(),os.userInfo().homedir])) {
    for (const suffix of ['.codexpro','.config/codexpro']) {
      const blocked=path.join(home,suffix);
      if (inside(blocked,value)) throw Error('Protected live state refused');
      if (fs.existsSync(blocked)&&inside(fs.realpathSync(blocked),value)) throw Error('Protected live state alias refused');
    }
  }
  return value;
}
function readFile(file,maximum=64*1024*1024) {
  if (fs.realpathSync(file)!==file) throw Error('Snapshot symlink refused');
  const before=fs.statSync(file);
  if (!before.isFile()||before.nlink!==1||before.size>maximum) throw Error('Snapshot file is not bounded and private');
  const fd=fs.openSync(file,fs.constants.O_RDONLY|fs.constants.O_NOFOLLOW|fs.constants.O_NONBLOCK);
  try {
    const actual=fs.fstatSync(fd);
    if (actual.ino!==before.ino||actual.dev!==before.dev) throw Error('Source identity changed');
    const bytes=fs.readFileSync(fd);
    const after=fs.fstatSync(fd);
    if (bytes.length!==before.size||after.size!==before.size||after.mtimeMs!==before.mtimeMs||after.ctimeMs!==before.ctimeMs) throw Error('Source file changed');
    return bytes;
  } finally { fs.closeSync(fd); }
}
const snapshot=privatePath(args.get('--snapshot'));
const output=privatePath(args.get('--output'),false);
const original=args.get('--original-jobs-dir');
const expected=args.get('--metadata-sha256');
const release=args.get('--release');
if (!original||!path.isAbsolute(original)||!/^[0-9a-f]{64}$/.test(expected??'')||!release||!path.isAbsolute(release)) throw Error('Missing original-directory, metadata digest or release');
if (inside(snapshot,output)||fs.existsSync(output)) throw Error('Oracle output must be fresh and outside snapshot');
const metadata=readFile(path.join(snapshot,'jobs.json'));
if (hash(metadata)!==expected) throw Error('Metadata SHA-256 mismatch');
const table=JSON.parse(metadata);
if (table.version!==1||!Array.isArray(table.jobs)||table.jobs.length>10000) throw Error('Invalid v1 jobs table');
const moduleFile=path.join(fs.realpathSync(release),'dist/jobOutput.js');
const moduleDigest=hash(readFile(moduleFile,1024*1024));
const { JobOutputStore }=await import(pathToFileURL(moduleFile).href);
const retention=Number(args.get('--retention-ms')??86400000);
if (!Number.isSafeInteger(retention)||retention<1||retention>31536000000) throw Error('Invalid retention');
const store=new JobOutputStore({jobsDir:snapshot,jobRetentionMs:retention,bashMode:'full',exposeAbsolutePaths:false});
const jobs=[];const files=[];const ids=new Set();
for (const record of table.jobs) {
  if (!/^job_[0-9a-f]{8}$/.test(record.id)||ids.has(record.id)) throw Error('Invalid duplicate job identity');
  ids.add(record.id);
  if (record.stdout_path!==path.join(original,record.id+'.out')||record.stderr_path!==path.join(original,record.id+'.err')) throw Error('Source output path mismatch');
  const eligible=record.status!=='running'&&record.launch_state!=='prepared'&&(!Object.hasOwn(record,'quiescent')||record.quiescent===true)&&(!(record.scope_unit||record.resource_claim)||record.quiescent===true);
  if (!eligible) { jobs.push({id:record.id,workspace_id:record.workspace_id,disposition:'archive_only'}); continue; }
  if (!record.runner_version&&!record.output_expired) throw Error('Oracle needs an already-rendered v2 private record; it will not run the legacy rendering writer');
  const job={...record,stdout_path:path.join(snapshot,record.id+'.out'),stderr_path:path.join(snapshot,record.id+'.err')};
  const lengths=[];
  if (!job.output_expired) {
    const workspace=createHash('sha256').update(job.workspace_id).digest('hex').slice(0,24);
    for (const relative of [job.id+'.out',job.id+'.err',`output/${workspace}/${job.id}/stdout.log`,`output/${workspace}/${job.id}/stderr.log`]) {
      const bytes=readFile(path.join(snapshot,relative));
      files.push({path:relative,bytes:bytes.length,sha256:hash(bytes)});
      if (relative.endsWith('.log')) lengths.push(bytes.length);
    }
  }
  const expectedJob={id:job.id,workspace_id:job.workspace_id,disposition:'imported_history',metadata:store.metadata(job),
    view:{job_id:job.id,status:job.status,origin:job.origin,command:job.command_label,cwd:job.cwd,started_at:job.started_at,
      finished_at:job.finished_at??null,elapsed_ms:job.finished_at_ms-job.started_at_ms,exit_code:job.exit_code,signal:job.signal,
      stop_reason:job.stop_reason??null,deadline_in_ms:null,returned_bytes:0},
    head:store.read(job,256,'head'),tail:store.read(job,256,'tail'),pages:[]};
  if (job.output_expired) {
    try { store.page(job,undefined,256); throw Error('Expired output unexpectedly returned'); }
    catch(error) { if(error.code!=='job_output_expired') throw error; expectedJob.page_error={code:error.code,message:error.message}; }
  } else {
    let cursor;
    for (let i=0;i<2;i++) {
      const result=store.page(job,cursor,256);
      expectedJob.pages.push({cursor:cursor??'',budget:256,result});
      cursor=result.next_cursor;
      if(!result.has_more)break;
    }
    const finalCursor=Buffer.from(JSON.stringify({v:1,id:`${job.workspace_id}:${job.id}:${job.started_at}`,offsets:lengths})).toString('base64url');
    expectedJob.pages.push({cursor:finalCursor,budget:256,result:store.page(job,finalCursor,256)});
  }
  jobs.push(expectedJob);
}
for (const item of files) {
  const bytes=readFile(path.join(snapshot,item.path));
  if(bytes.length!==item.bytes||hash(bytes)!==item.sha256)throw Error('Snapshot changed during oracle reads');
}
if(hash(readFile(path.join(snapshot,'jobs.json')))!==expected)throw Error('Snapshot metadata changed');
const result={schema_version:1,source_commit:'44bb76bfa671e4aad540a8976f0fa6e6d5c2507a',snapshot_dir:snapshot,
  original_jobs_dir:original,metadata_sha256:expected,reference_module:moduleFile,reference_module_sha256:moduleDigest,
  retention_ms:retention,source_process_operations:0,files,jobs};
const encoded=JSON.stringify(result,null,2)+'\n';
if(Buffer.byteLength(encoded)>64*1024*1024)throw Error('Oracle output exceeds bound');
const fd=fs.openSync(output,fs.constants.O_WRONLY|fs.constants.O_CREAT|fs.constants.O_EXCL,0o600);
try { fs.writeFileSync(fd,encoded);fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
console.log(JSON.stringify({oracle:output,sha256:hash(encoded),source_records:jobs.length,imported:jobs.filter(j=>j.disposition==='imported_history').length,archive_only:jobs.filter(j=>j.disposition==='archive_only').length}));
