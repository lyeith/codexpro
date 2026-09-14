import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { loadConfig } from '../dist/config.js';
import { JobManager } from '../dist/jobs.js';
if (process.platform !== 'linux') { console.log('Systemd scope check requires Linux.'); process.exit(0); }
const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'work-scope-')));
const config = loadConfig(['--root', root, '--bash', 'full']); config.jobsDir = path.join(root, 'jobs');
const jobs = new JobManager(config); assert.equal(jobs.scopesEnabled, true, 'Run this smoke check within an isolated systemd user service.');
try {
  const program = `const fs=require('fs');const {spawn}=require('child_process');const c=spawn('/bin/sh',['-c','while true; do sleep 1; done'],{detached:true,stdio:'ignore'});fs.writeFileSync('daemon.pid',String(c.pid));c.unref();`;
  const command = `node -e '${program.replaceAll("'", "'\\''")}'`;
  const job = jobs.start({ workspaceId: 'ws_scope_test', root, cwdAbs: root, cwdLabel: '.', command, env: process.env, origin: 'background', timeoutMs: 5000, outputLimitBytes: 16000 });
  await jobs.wait(job.id, 15000);
  assert.equal(job.status, 'succeeded', JSON.stringify(job)); assert.equal(job.quiescent, true); assert.equal(job.quiescence_scope, 'systemd_scope');
  const pid = fs.readFileSync(path.join(root, 'daemon.pid'), 'utf8'); const state = spawnSync('ps', ['-p', pid, '-o', 'stat='], { encoding: 'utf8' }).stdout.trim();
  assert.ok(!state || state.startsWith('Z'), `Detached descendant ${pid} is still live (${state}).`);
  console.log('Scoped job completed; detached descendant was killed and the whole scope is quiescent.');
  const pipeProgram = `const fs=require('fs');const {spawn}=require('child_process');const c=spawn('/bin/sh',['-c','trap "" TERM; while true; do sleep 1; done'],{detached:true,stdio:'inherit'});fs.writeFileSync('pipe-daemon.pid',String(c.pid));c.unref();`;
  const hanging = jobs.start({ workspaceId: 'ws_scope_test', root, cwdAbs: root, cwdLabel: '.', command: `node -e '${pipeProgram.replaceAll("'", "'\\''")}'`, env: process.env, origin: 'background', timeoutMs: 1000, outputLimitBytes: 16000 });
  await jobs.wait(hanging.id, 15000);
  assert.equal(hanging.status, 'timed_out', JSON.stringify(hanging)); assert.equal(hanging.quiescent, true);
  const hangingPid = fs.readFileSync(path.join(root, 'pipe-daemon.pid'), 'utf8'); const hangingState = spawnSync('ps', ['-p', hangingPid, '-o', 'stat='], { encoding: 'utf8' }).stdout.trim();
  assert.ok(!hangingState || hangingState.startsWith('Z'), 'A child that inherited capture pipes survived scope cleanup.');
  console.log('Escalated timeout also closed inherited pipes and proved scope quiescence without a supervisor result.');
} finally { for (const job of jobs.runningJobs()) jobs.stop(job.id); fs.rmSync(root, { recursive: true, force: true }); }
