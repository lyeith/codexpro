#!/usr/bin/env node
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { randomBytes } from 'node:crypto';

const helper = process.argv[2];
const directory = process.argv[3];
assert.equal(process.platform, 'linux');
assert.ok(helper && path.isAbsolute(helper), 'Pass the absolute installed host helper.');
assert.ok(directory && path.isAbsolute(directory), 'Pass a new private evidence directory.');
fs.mkdirSync(directory, { mode: 0o700 });
assert.equal(fs.statSync(directory).uid, process.getuid());
assert.equal(fs.statSync(directory).mode & 0o777, 0o700);

const overrideFile = new URL('../deploy/ssd/codexpro-host-resources.conf', import.meta.url);
const overrides = fs.readFileSync(overrideFile, 'utf8').split(String.fromCharCode(10))
  .map(line => line.trim()).filter(line => line && !line.startsWith('#'));
assert.deepEqual(overrides, ['[Service]', 'ProtectSystem=no', 'PrivateUsers=no']);
const sourceUnit = 'codexpro.service';
const properties = [
  'NoNewPrivileges', 'ProtectHome', 'ProtectControlGroups', 'PrivateTmp',
  'PrivateMounts', 'PrivateNetwork', 'PrivateDevices', 'ProtectKernelTunables',
  'ProtectKernelModules', 'ProtectKernelLogs', 'ProtectClock', 'ProtectHostname',
  'RestrictSUIDSGID', 'RestrictNamespaces', 'CapabilityBoundingSet',
  'SystemCallFilter', 'RestrictAddressFamilies', 'ReadOnlyPaths', 'ReadWritePaths',
  'InaccessiblePaths', 'BindPaths', 'BindReadOnlyPaths', 'RootDirectory', 'RootImage',
];
function show(unit, selected) {
  const result = spawnSync('systemctl', ['--user', 'show', unit,
    ...selected.map(name => `--property=${name}`)], { encoding: 'utf8', timeout: 10_000 });
  assert.equal(result.status, 0, 'Cannot inspect the service configuration.');
  return Object.fromEntries(result.stdout.trim().split('\n').map(line => {
    const equals = line.indexOf('=');
    return [line.slice(0, equals), line.slice(equals + 1)];
  }));
}
const baseline = show(sourceUnit, properties);
assert.equal(baseline.NoNewPrivileges, 'yes');
// These are currently unset on SSD. Refuse rather than invent a clone when a
// future unit adds complex bind mounts, filters or a different root filesystem.
for (const name of ['SystemCallFilter', 'RestrictAddressFamilies'])
  assert.ok(['', '~'].includes(baseline[name]), `Unsupported rehearsal property: ${name}`);
for (const name of ['ReadOnlyPaths', 'ReadWritePaths', 'InaccessiblePaths', 'BindPaths',
  'BindReadOnlyPaths', 'RootDirectory', 'RootImage'])
  assert.equal(baseline[name], '', `Unsupported rehearsal property: ${name}`);

const allocation = { run_id: randomBytes(16).toString('hex'),
  registration_id: randomBytes(16).toString('hex') };
const childFile = path.join(directory, 'probe.mjs');
const proofFile = path.join(directory, 'proof.json');
const configurationFile = path.join(directory, 'expected-configuration.json');
fs.writeFileSync(configurationFile, JSON.stringify({ baseline, allocation, host_namespace: fs.readlinkSync('/proc/self/ns/user'),
  host_uid_map: fs.readFileSync('/proc/self/uid_map', 'utf8').trim() }), { mode: 0o600, flag: 'wx' });
const child = String.raw`
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import { randomBytes } from 'node:crypto';
const [helper, proofFile, unit, configurationFile] = process.argv.slice(2);
const context = JSON.parse(fs.readFileSync(configurationFile, 'utf8'));
const allocation = context.allocation;
const proof = { user_namespace: fs.readlinkSync('/proc/self/ns/user'),
  host_user_namespace: context.host_namespace,
  uid_map: fs.readFileSync('/proc/self/uid_map', 'utf8').trim(),
  root_owner: fs.statSync('/').uid,
  no_new_privileges: fs.readFileSync('/proc/self/status', 'utf8').split(String.fromCharCode(10)).some(line =>
    line.startsWith('NoNewPrivs:') && line.split(':')[1].trim() === '1') };
let claim;
function invoke(args) {
  const result = spawnSync(helper, ['external', ...args],
    { encoding: 'utf8', timeout: 10_000, maxBuffer: 1024 * 1024 });
  proof.operations ??= [];
  proof.operations.push({ operation: args[0], status: result.status,
    error_code: result.error?.code, stdout: result.stdout, stderr: result.stderr });
  assert.equal(result.status, 0, 'Host helper failed; inspect the private proof.');
  return JSON.parse(result.stdout);
}
try {
  assert.equal(proof.user_namespace, proof.host_user_namespace);
  assert.equal(proof.root_owner, 0);
  assert.equal(proof.uid_map, context.host_uid_map);
  assert.equal(proof.no_new_privileges, true);
  const expected = context.baseline;
  const configuration = spawnSync('systemctl', ['--user', 'show', unit,
    ...Object.keys(expected).map(name => '--property=' + name),
    '--property=ProtectSystem', '--property=PrivateUsers'],
    { encoding: 'utf8', timeout: 10_000 });
  assert.equal(configuration.status, 0);
  const actual = Object.fromEntries(configuration.stdout.trim().split('\n').map(line => {
    const equals = line.indexOf('=');
    return [line.slice(0, equals), line.slice(equals + 1)];
  }));
  for (const [name, value] of Object.entries(expected)) assert.equal(actual[name], value);
  assert.equal(actual.ProtectSystem, 'no');
  assert.equal(actual.PrivateUsers, 'no');
  proof.other_hardening_preserved = true;
  const owner = 'codexpro-job_' + randomBytes(4).toString('hex') + '-' +
    randomBytes(16).toString('hex') + '.scope';
  claim = invoke(['begin', '--purpose', 'tool', '--cwd', '/home/spite/Projects',
    '--owner-unit', owner, '--cache-consumer',
    '--run-id', allocation.run_id, '--registration-id', allocation.registration_id]);
  assert.equal(claim.run_id, allocation.run_id);
  assert.equal(claim.registration_id, allocation.registration_id);
  proof.begin_succeeded = true;
} catch (error) {
  proof.failed = true;
  proof.error_name = error.name;
  process.exitCode = 1;
} finally {
  {
    try {
      const cancelled = invoke(['cancel', allocation.run_id,
        '--registration-id', allocation.registration_id]);
      proof.own_created_cancelled = cancelled.cancelled === true && cancelled.quiescent === true;
      assert.equal(proof.own_created_cancelled, true);
    } catch {
      proof.failed = true;
      process.exitCode = 1;
    }
  }
  fs.writeFileSync(proofFile, JSON.stringify(proof, null, 2), { mode: 0o600, flag: 'wx' });
}
`;
fs.writeFileSync(childFile, child, { mode: 0o600, flag: 'wx' });
const unit = `codexpro-resource-namespace-proof-${randomBytes(16).toString('hex')}.service`;
const cloned = properties.filter(name => !['SystemCallFilter', 'RestrictAddressFamilies',
  'ReadOnlyPaths', 'ReadWritePaths', 'InaccessiblePaths', 'BindPaths', 'BindReadOnlyPaths',
  'RootDirectory', 'RootImage'].includes(name));
const args = ['--user', '--unit', unit, '--wait', '--collect', '--quiet', '--pipe',
  '--service-type=exec', ...overrides.slice(1).map(value => '--property=' + value),
  '--property=RuntimeMaxSec=25s',
  ...cloned.map(name => `--property=${name}=${baseline[name]}`),
  '--', process.execPath, childFile, helper, proofFile, unit, configurationFile];
const result = spawnSync('systemd-run', args, { encoding: 'utf8', timeout: 30_000,
  maxBuffer: 1024 * 1024 });
// The controller knows the private IDs before granting the unit. Revoke them
// even if begin returned lost/malformed output or RuntimeMaxSec killed the child.
// Cancel is nonce-bound and idempotent, and absence creates a late-begin tombstone.
const cleanup = spawnSync(helper, ['external', 'cancel', allocation.run_id,
  '--registration-id', allocation.registration_id],
  { encoding: 'utf8', timeout: 10_000, maxBuffer: 1024 * 1024 });
let cancelled;
try { cancelled = JSON.parse(cleanup.stdout); } catch {}
fs.writeFileSync(path.join(directory, 'unit-result.json'), JSON.stringify({ unit,
  baseline, status: result.status, signal: result.signal, error_code: result.error?.code,
  stdout: result.stdout, stderr: result.stderr, controller_cleanup: {
    status: cleanup.status, signal: cleanup.signal, error_code: cleanup.error?.code,
    stdout: cleanup.stdout, stderr: cleanup.stderr } }, null, 2), { mode: 0o600, flag: 'wx' });
// --collect removes the disposable unit; no source unit, socket or release is
// modified. The hypothetical job scope was never started or granted execution.
const settled = show(unit, ['LoadState', 'ActiveState']);
assert.equal(settled.LoadState, 'not-found', 'Disposable unit has not been collected.');
assert.equal(cleanup.status, 0, 'Exact reservation cancellation failed; inspect the private evidence.');
assert.equal(cancelled?.run_id, allocation.run_id);
assert.equal(cancelled?.cancelled, true);
assert.equal(cancelled?.quiescent, true);
assert.equal(result.status, 0, 'Namespace rehearsal failed; inspect the private evidence.');
const proof = JSON.parse(fs.readFileSync(proofFile, 'utf8'));
assert.equal(proof.failed, undefined);
assert.equal(proof.begin_succeeded, true);
assert.equal(proof.own_created_cancelled, true);
assert.equal(proof.other_hardening_preserved, true);
console.log(JSON.stringify({ passed: true, host_user_namespace: true, root_owner: 0,
  no_new_privileges: true, helper_begin_succeeded: true, own_created_cancelled: true,
  other_hardening_preserved: true,
  disposable_unit_collected: true, evidence: directory }));
