import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { loadConfig } from '../dist/config.js';
import { JobManager } from '../dist/jobs.js';
import { inspectScope, validScopeIdentity } from '../dist/jobScope.js';
import { beginResources, cancelResources, finishResources, resourceExec, resourceHelper } from '../dist/jobResources.js';

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'codexpro-resource-unit-'));
  const config = { ...loadConfig(['--root', root, '--bash', 'full', '--audit', 'off']), jobsDir: path.join(root, 'jobs'), hostResourcesHelper: undefined, jobRetentionMs: 1 };
  fs.mkdirSync(config.jobsDir, {mode:0o700});
  return {root, config, close: () => fs.rmSync(root, {recursive:true, force:true})};
}
function terminal(f, values = {}) {
  const id = 'job_aabbccdd';
  const files = ['out','err','exit','result.json','control','grant','spec.json','spec.json.started'];
  for (const suffix of files) fs.writeFileSync(path.join(f.config.jobsDir, `${id}.${suffix}`), suffix === 'spec.json.started' ? '{}' : 'evidence', {mode:0o600});
  const job = { id, workspace_id:'ws_test', root:f.root, cwd:'.', command:'true', command_label:'true', pid:0,
    scope_unit:`codexpro-${id}-${'a'.repeat(32)}`, origin:'background', status:'failed', started_at:new Date(0).toISOString(), started_at_ms:0,
    finished_at:new Date(1).toISOString(), finished_at_ms:1, exit_code:1, signal:null, deadline_ms:1, timeout_ms:1,
    output_limit_bytes:4096, stdout_path:path.join(f.config.jobsDir, `${id}.out`), stderr_path:path.join(f.config.jobsDir, `${id}.err`),
    exit_path:path.join(f.config.jobsDir, `${id}.exit`), result_path:path.join(f.config.jobsDir, `${id}.result.json`),
    control_path:path.join(f.config.jobsDir, `${id}.control`), grant_path:path.join(f.config.jobsDir, `${id}.grant`), acknowledged:true,
    runner_version:2, quiescent:false, ...values };
  fs.writeFileSync(path.join(f.config.jobsDir,'jobs.json'), JSON.stringify({version:1, jobs:[job]}), {mode:0o600});
  return job;
}
test('terminal scopes without incarnation proof retain recovery metadata despite expired logs', () => {
  const f = fixture();
  try {
    const original = terminal(f);
    const manager = new JobManager(f.config);
    const job = manager.require(original.id);
    assert.equal(job.quiescent, false);
    assert.notEqual(job.output_expired, true);
    for (const suffix of ['out','result.json','spec.json','spec.json.started','grant']) assert.ok(fs.existsSync(path.join(f.config.jobsDir, `${job.id}.${suffix}`)));
    const persisted = JSON.parse(fs.readFileSync(path.join(f.config.jobsDir,'jobs.json'),'utf8')).jobs[0];
    assert.equal(persisted.quiescent, false);
  } finally { f.close(); }
});
test('terminal scope recovery reconciles exact prior-boot owners before ordinary expiry', () => {
  if (process.platform !== 'linux') return;
  const f = fixture();
  try {
    const unit = `codexpro-job_aabbccdd-${'a'.repeat(32)}.scope`;
    const identity = {version:1, unit, boot_id:'00000000-0000-0000-0000-000000000000', invocation_id:'b'.repeat(32), cgroup:`/user.slice/${unit}`};
    assert.equal(validScopeIdentity(identity), true);
    const original = terminal(f, {scope_identity:identity});
    const manager = new JobManager(f.config);
    const job = manager.require(original.id);
    assert.equal(job.quiescent, true);
    assert.equal(job.output_expired, true);
    assert.ok(job.quiesced_at);
    assert.equal(fs.existsSync(path.join(f.config.jobsDir, `${job.id}.spec.json.started`)), false);
  } finally { f.close(); }
});
test('scope basename alone, traversal and a different registered unit never prove ownership', () => {
  const unit = `codexpro-job_aabbccdd-${'a'.repeat(32)}.scope`;
  const identity = {version:1, unit, boot_id:'00000000-0000-0000-0000-000000000000', invocation_id:'b'.repeat(32), cgroup:`/user.slice/${unit}`};
  assert.equal(validScopeIdentity(identity, `${unit}-replacement`), false);
  assert.equal(validScopeIdentity({...identity, invocation_id:''}), false);
  assert.equal(validScopeIdentity({...identity, cgroup:`/../${unit}`}), false);
  assert.equal(inspectScope({...identity, unit:'codexpro-job_aabbccdd.scope'}), 'unknown');
});
test('configured helper never falls back when absent and registration uses independent nonces', () => {
  const f = fixture();
  try {
    assert.throws(() => resourceHelper({...f.config, hostResourcesHelper:'ssd-dev'}), /absolute helper/);
    assert.throws(() => beginResources(path.join(f.root,'missing'), f.root, 'owner.scope'), /operation failed/);
    const helper = path.join(f.root,'helper');
    fs.writeFileSync(helper, `#!/usr/bin/node\nconst args=process.argv.slice(2); process.stdout.write(JSON.stringify(args[1]==='begin'?{run_id:'${'a'.repeat(32)}',registration_id:'${'b'.repeat(32)}'}:args[1]==='cancel'?{cancelled:true}:{quiescent:true}));\n`, {mode:0o700});
    const claim = beginResources(helper, f.root, 'owner.scope');
    assert.notEqual(claim.run_id, claim.registration_id);
    assert.deepEqual(resourceExec(claim,['/usr/bin/node','runner','spec']), [helper,['external','exec',claim.run_id,'--registration-id',claim.registration_id,'--','/usr/bin/node','runner','spec']]);
    assert.equal(cancelResources(claim), true);
    assert.equal(finishResources(claim, 0), true);
  } finally { f.close(); }
});
test('resource claims retain terminal recovery files when host settlement fails', () => {
  const f = fixture();
  try {
    const original = terminal(f, {resource_claim:{version:1, helper:path.join(f.root,'missing'), run_id:'a'.repeat(32), registration_id:'b'.repeat(32)}});
    const manager = new JobManager(f.config);
    assert.equal(manager.require(original.id).quiescent, false);
    assert.ok(fs.existsSync(original.result_path));
    assert.equal(JSON.parse(fs.readFileSync(path.join(f.config.jobsDir,'jobs.json'),'utf8')).jobs[0].resource_claim.run_id,'a'.repeat(32));
  } finally { f.close(); }
});
test('enabled resources refuse execution when scopes are explicitly disabled', () => {
  const f = fixture();
  const previous = process.env.CODEXPRO_JOB_SCOPES;
  try {
    process.env.CODEXPRO_JOB_SCOPES = '0';
    assert.throws(() => new JobManager({...f.config,hostResourcesHelper:'/missing/ssd-dev'}), /require systemd job scopes/);
    assert.equal(fs.existsSync(path.join(f.config.jobsDir,'jobs.json')),false);
  } finally { if (previous === undefined) delete process.env.CODEXPRO_JOB_SCOPES; else process.env.CODEXPRO_JOB_SCOPES=previous; f.close(); }
});
test('host proof can recover a completed registration even if supervisor scope proof was never written', () => {
  const f = fixture();
  try {
    const helper=path.join(f.root,'helper');
    fs.writeFileSync(helper,'#!/usr/bin/node\nprocess.stdout.write(JSON.stringify({quiescent:true,cancelled:false}));\n',{mode:0o700});
    const original=terminal(f,{resource_claim:{version:1,helper,run_id:'a'.repeat(32),registration_id:'b'.repeat(32)}});
    const manager=new JobManager(f.config);
    assert.equal(manager.require(original.id).quiescent,true);
    assert.equal(manager.require(original.id).output_expired,true);
  } finally { f.close(); }
});
