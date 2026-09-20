import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// Tests must not attach to a developer's live job table or append its reminders
// to JSONL exports, or attach personal global instructions to fixture responses.
// Individual fixtures may create a further isolated job store or instruction directory.
const jobsDir = fs.mkdtempSync(path.join(os.tmpdir(), 'codexpro-test-jobs-'));
const codexDir = path.join(jobsDir, 'codex-home');
fs.mkdirSync(codexDir);
let status = 1;
try {
  const files = fs.readdirSync('test').filter(name => name.endsWith('.test.mjs')).sort().map(name => path.join('test', name));
  const result = spawnSync(process.execPath, ['--test', ...files], {
    stdio: 'inherit', env: { ...process.env, CODEXPRO_JOBS_DIR: jobsDir, CODEXPRO_CODEX_DIR: codexDir }
  });
  if (result.error) console.error(result.error.message);
  status = result.status ?? 1;
} finally {
  fs.rmSync(jobsDir, {recursive: true, force: true});
}
process.exitCode = status;
