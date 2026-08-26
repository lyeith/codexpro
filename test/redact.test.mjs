import assert from 'node:assert/strict';
import test from 'node:test';
import { hasSecretValue, introducesSecretValue } from '../dist/redact.js';

function fakeSecret(suffix = '1234567890') {
  return ['OPENAI', '_API_KEY', '=', 'not-a-real-credential-', suffix].join('');
}

test('detects newly introduced secrets while allowing unrelated edits to guarded fixture files', () => {
  const existing = fakeSecret();
  const changed = fakeSecret('0987654321');

  assert.equal(hasSecretValue(existing), true);
  assert.equal(introducesSecretValue('', existing), true);
  assert.equal(introducesSecretValue(`${existing}\nold text\n`, `${existing}\nnew text\n`), false);
  assert.equal(introducesSecretValue(`${existing}\n`, `${existing}\n${existing}\n`), true);
  assert.equal(introducesSecretValue(`${existing}\n`, `${changed}\n`), true);
});

test('placeholder values remain safe', () => {
  const placeholder = ['OPENAI', '_API_KEY', '=', '[REDACTED_', 'SECRET]'].join('');
  assert.equal(hasSecretValue(placeholder), false);
  assert.equal(introducesSecretValue('', placeholder), false);
});
