import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { makeRestrictedBashEnv } from '../dist/bashOps.js';

test('restricted shells pass only the configured Agentmail token-file reference', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-agentmail-env-'));
  const tokenFile = path.join(root, 'credential');
  try {
    await fs.writeFile(tokenFile, 'fixture-only-credential', { mode: 0o600 });
    const source = { ...process.env, AGENTMAIL_TOKEN_FILE: tokenFile, AGENTMAIL_TOKEN: 'must-not-pass', CODEXPRO_HTTP_TOKEN: 'must-not-pass' };
    const env = makeRestrictedBashEnv({ inheritEnv: false }, source);
    assert.equal(env.AGENTMAIL_TOKEN_FILE, tokenFile);
    assert.equal(env.AGENTMAIL_TOKEN, undefined);
    assert.equal(env.CODEXPRO_HTTP_TOKEN, undefined);
    const child = spawnSync(process.execPath, ['-e', 'const fs=require("node:fs"); process.exit(fs.readFileSync(process.env.AGENTMAIL_TOKEN_FILE,"utf8") === "fixture-only-credential" ? 0 : 1)'], { env, encoding: 'utf8' });
    assert.equal(child.status, 0, child.stderr);
    delete source.AGENTMAIL_TOKEN_FILE;
    assert.equal(makeRestrictedBashEnv({ inheritEnv: false }, source).AGENTMAIL_TOKEN_FILE, undefined);
    assert.equal(makeRestrictedBashEnv({ inheritEnv: true }, { AGENTMAIL_TOKEN_FILE: tokenFile }).AGENTMAIL_TOKEN_FILE, tokenFile);
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});
