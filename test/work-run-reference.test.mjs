import test from 'node:test';
import assert from 'node:assert/strict';
import { AmbiguousRunReferenceError, resolveRunReference } from '../dist/work/runReference.js';

test('run references accept literal case-sensitive prefixes and dashboard abbreviations, with exact IDs taking precedence', () => {
  const run = { id: 'run_Ab_cD-12abcdefghXYZ9' };
  for (const ref of [run.id, 'run_Ab_cD-12', 'run_Ab_cD-12…XYZ9', 'run_Ab_cD-12...XYZ9']) {
    assert.equal(resolveRunReference([run], ref), run);
  }
  for (const ref of ['run_Ab_cD-1', 'run_ab_cD-12', 'run_Ab%cD-12', 'run_Ab_cD-12*', 'run_Ab_cD-12…ZZZZ', 'run_Ab_cD-12…XYZ', 'Ab_cD-12', ' run_Ab_cD-12']) {
    assert.equal(resolveRunReference([run], ref), undefined, ref);
  }
  const collision = { id: 'run_Ab_cD-12differentXYZ9' };
  assert.throws(() => resolveRunReference([run, collision], 'run_Ab_cD-12'), AmbiguousRunReferenceError);
  assert.throws(() => resolveRunReference([run, collision], 'run_Ab_cD-12…XYZ9'), AmbiguousRunReferenceError);
  assert.equal(resolveRunReference([collision, run], run.id), run);
  const legacy = { id: 'run_Ab_cD-12' };
  assert.equal(resolveRunReference([run, legacy], legacy.id), legacy);
  assert.equal(resolveRunReference([{ id: 'run_old' }], 'run_old').id, 'run_old');
});
