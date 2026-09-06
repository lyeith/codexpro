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

test('digit-free identifiers are not treated as secrets, real ones are described without the value', async () => {
  const { describeSecretMatches, secretContentBlockedError } = await import('../dist/redact.js');
  const actionName = 'const ACTION_TOKEN = "io.personalops.calendar.widget.TOGGLE_DONE"';
  assert.equal(hasSecretValue(actionName), false);
  assert.equal(introducesSecretValue('', actionName), false);
  const real = `WIDGET_API_KEY = "${['not-a-real', '-credential-', '9876543210'].join('')}"`;
  assert.equal(hasSecretValue(real), true);
  const described = describeSecretMatches(real);
  assert.deepEqual(described, ['WIDGET_API_KEY = "…"']);
  const error = secretContentBlockedError('write', real);
  assert.equal(error.code, 'secret_content_blocked');
  assert.match(error.message, /workspace stays writable/);
  assert.doesNotMatch(error.message, /9876543210/);
  assert.deepEqual(error.details, { secret_matches: ['WIDGET_API_KEY = "…"'] });
});
