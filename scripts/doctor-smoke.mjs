import { spawnSync } from 'node:child_process';
import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';

function git(cwd, args) {
  const result = spawnSync('git', args, { cwd, encoding: 'utf8', shell: false });
  if (result.status !== 0) throw new Error(result.stderr || result.stdout || `git ${args.join(' ')} failed`);
}

async function getFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = typeof address === 'object' && address ? address.port : undefined;
      server.close(() => (port ? resolve(port) : reject(new Error('no free port'))));
    });
    server.on('error', reject);
  });
}

const root = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-doctor-smoke-'));
const home = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-doctor-home-'));
const port = await getFreePort();
const result = spawnSync(process.execPath, [
  'scripts/codexpro.mjs',
  'doctor',
  '--root',
  root,
  '--port',
  String(port),
  '--tunnel',
  'none'
], {
  cwd: path.resolve('.'),
  env: { ...process.env, CODEXPRO_HOME: home },
  encoding: 'utf8'
});

if (result.status !== 0) {
  throw new Error(`doctor failed\nstdout:\n${result.stdout}\nstderr:\n${result.stderr}`);
}

const output = `${result.stdout}\n${result.stderr}`;
for (const expected of ['CodexPro doctor', 'Node', 'Build artifacts', 'Local port', 'Ready']) {
  if (!output.includes(expected)) {
    throw new Error(`doctor output missing ${expected}\n${output}`);
  }
}

const secondRoot = await fs.mkdtemp(path.join(os.tmpdir(), 'codexpro-doctor-smoke-b-'));
for (const projectRoot of [root, secondRoot]) {
  git(projectRoot, ['init', '-q']);
  git(projectRoot, ['config', 'user.email', 'doctor@example.com']);
  git(projectRoot, ['config', 'user.name', 'Doctor Smoke']);
  await fs.writeFile(path.join(projectRoot, 'tracked.txt'), 'tracked\n', 'utf8');
  git(projectRoot, ['add', '.']);
  git(projectRoot, ['commit', '-q', '-m', 'initial']);
}
const projectsFile = path.join(home, 'projects.json');
await fs.writeFile(projectsFile, `${JSON.stringify({
  version: 1,
  defaultProject: 'alpha',
  projects: [
    { id: 'alpha', root },
    { id: 'beta', root: secondRoot }
  ]
}, null, 2)}\n`, 'utf8');
const catalogPort = await getFreePort();
const catalogResult = spawnSync(process.execPath, [
  'scripts/codexpro.mjs',
  'doctor',
  '--projects-file',
  projectsFile,
  '--worktree-mode',
  'mcp',
  '--port',
  String(catalogPort),
  '--tunnel',
  'none'
], {
  cwd: path.resolve('.'),
  env: { ...process.env, CODEXPRO_HOME: home },
  encoding: 'utf8'
});
if (catalogResult.status !== 0) {
  throw new Error(`catalog doctor failed\nstdout:\n${catalogResult.stdout}\nstderr:\n${catalogResult.stderr}`);
}
const catalogOutput = `${catalogResult.stdout}\n${catalogResult.stderr}`;
for (const expected of ['Projects', 'Git alpha', 'Git beta', 'Ready']) {
  if (!catalogOutput.includes(expected)) throw new Error(`catalog doctor output missing ${expected}\n${catalogOutput}`);
}

console.log('✓ doctor smoke test passed');
