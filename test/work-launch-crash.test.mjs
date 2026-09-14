import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
import { loadConfig } from '../dist/config.js';
import { JobManager } from '../dist/jobs.js';

// Abrupt process exit at real persistence/launch boundaries, without production test hooks.
for (const boundary of ['prepared', 'spawned', 'granted']) test(`command grant survives parent crash at ${boundary}`, async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'codexpro-launch-crash-'));
  const dir = path.join(root, 'jobs'); const marker = path.join(root, 'effect.txt');
  const base = pathToFileURL(path.resolve('dist/')).href + '/';
  const script = `import fs from 'node:fs';
    import { JobManager } from ${JSON.stringify(new URL('jobs.js', base).href)};
    import { loadConfig } from ${JSON.stringify(new URL('config.js', base).href)};
    import { runWithToolContext } from ${JSON.stringify(new URL('toolContext.js', base).href)};
    const config = loadConfig(['--root', ${JSON.stringify(root)}, '--bash', 'full']); config.jobsDir=${JSON.stringify(dir)};
    const jobs = new JobManager(config);
    const write = fs.writeFileSync;
    fs.writeFileSync = function(file, data, ...rest) { const out = write.call(fs, file, data, ...rest);
      if (${JSON.stringify(boundary)} === 'spawned' && String(file).includes('jobs.json.tmp') && JSON.parse(data).jobs.some(j => j.pid > 0)) process.exit(76);
      if (${JSON.stringify(boundary)} === 'granted' && String(file).endsWith('.grant')) process.exit(77);
      return out;
    };
    runWithToolContext({principalId:'test', requestId:'test', signal:new AbortController().signal, workJobPrepared: () => { if (${JSON.stringify(boundary)} === 'prepared') process.exit(75); }}, () => jobs.start({workspaceId:'ws_test', root:${JSON.stringify(root)}, cwdAbs:${JSON.stringify(root)}, cwdLabel:'.', command:${JSON.stringify(`printf x >> '${marker}'; sleep 5`)}, env:process.env, origin:'background', timeoutMs:2000, outputLimitBytes:4096}));`;
  const child = spawn(process.execPath, ['--input-type=module', '-e', script], { stdio: ['ignore', 'ignore', 'pipe'] });
  let error = ''; child.stderr.on('data', b => { error += b; });
  try {
    const [code] = await once(child, 'exit'); assert.ok([75, 76, 77].includes(code), error);
    const config = loadConfig(['--root', root, '--bash', 'full']); config.jobsDir = dir;
    const jobs = new JobManager(config); const job = jobs.list()[0]; assert.ok(job);
    for (let n = 0; n < 80 && job.status === 'running'; n++) await delay(100);
    assert.notEqual(job.status, 'running');
    const effect = fs.existsSync(marker) ? fs.readFileSync(marker, 'utf8') : '';
    if (boundary === 'prepared' || boundary === 'spawned') assert.equal(effect, ''); else assert.equal(effect, 'x');
    assert.equal(job.quiescent, true);
  } finally { if (child.exitCode === null) child.kill('SIGKILL'); await delay(100); fs.rmSync(root, { recursive: true, force: true }); }
});
