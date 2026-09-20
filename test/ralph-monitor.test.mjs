import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';

test('Ralph monitor guards, repository context and persistent decision sessions', () => {
  const result = spawnSync('python3', ['-B', 'test/ralph_monitor_test.py'], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
});
