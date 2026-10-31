# Admin/catalog replay corrections

The first direct admin/catalog replay completed 51 cases: 26 passed, 2 matched
explicit approved deltas, 21 failed, none were blocked, and the managed-workspace
and actual-profile-restart proofs remained pending for their separate drivers.
Both original profiles were restored. The report remains preserved as failed;
these changes do not retroactively relabel its outcomes.

## Production corrections

### Preserve payload-discovered HTTP path labels

The actual Go launcher supplied `Redactor.Labels(nil)` to the administrative HTTP
value redactor. The TypeScript source passes the complete config response to
`pathRedactions` before applying config-derived fallback labels
(`src/http.ts:461-496`, `src/pathLabels.ts:16-47,117-129`). A saved profile contains
a nested `root`, so its root must receive `[workspace:current]`, including other
occurrences in the same payload. Go instead emitted `[project:codexpro]`.

The launcher now calls a small `adminHTTPOptions` adapter whose value callback
uses `Labels(value)`. The text-only callback retains its previous behavior. Native
tests exercise the adapter used by main: nested stored-profile roots take
precedence, named discovered roots retain their identifier, responses without a
root retain the project fallback, unknown bare paths remain `[path]`, and the
source payload is not mutated. No engine redaction code is changed by this patch.

### Restore release default blocked globs

The Go default list included six entries absent from the exact private TypeScript
default: `**/secrets/**`, `**/*.sqlite`, `**/*.sqlite3`, `**/*.db`, `**/.venv/**`, and
`**/__pycache__/**`. Both exact private launchers were inspected: neither supplies
`CODEXPRO_BLOCKED_GLOBS`. These were therefore additional Go defaults, not a
required normalization of two explicit fixture configurations.

Only those six extra defaults are removed. The ordered 39-entry release list is
preserved. Explicit private startup configuration can still append the six
entries or other operator-specified globs. The native tests verify the exact
default list and a real `config.Load` using a private EnvironmentFile extension.
Private-store/live-path isolation checks are unchanged.

## Fixture and dynamic-path corrections

The first runner knew the exact absolute private profile, catalog, state, and
creation-root paths but did not recognize the source's already-redacted
`[home]/...` spelling of those paths. It also assumed instructions were in
`config_home/empty-codex`. The TypeScript launcher's explicit instruction directory
is instead a sibling of its config home; Go's happens to be beneath its config
home. That made the two saved-profile payloads describe different relationships
to their actual runtime instruction directories.

The runner now derives config home, catalog, instruction directory, limits file,
and work directory from the running private process's whitelisted argv and
environment settings. It checks the paths against the explicit instance state.
An explicitly configured, nonexistent instruction directory is valid and remains
nonexistent. A symlink escaping the private instance is rejected.

Only exact bound role paths and their source `[home]/...` spellings receive
aliases. The entire `[home]` prefix is not discarded; unknown suffixes and the
product's `[workspace:*]`, `[project:*]`, and `[codex-data]` labels remain visible
to the full comparison. The profile payload uses each side's verified actual
instruction/work directory. No JSON fields, omissions, null values, arrays, text
formatting, error envelopes, or audit sequences are removed from comparison.

The private Go launcher now includes five existing launch facts in its manifest:
config home, catalog path, creation-root path/ID, and binary SHA-256. All are taken
from the same explicit CLI/catalog variables used to launch the process. This
removes the previous need for manual manifest augmentation before the admin lane.

## Prior history remains inconclusive

Three first-run catalog cases differed only in a normalized historical action
ordinal: TypeScript's selected action was at index 141 and Go's at index 137,
with the same semantic projection digest. The histories had already been used by
other successful and failed private test attempts. Their identical prior action
sets and retention windows were not established. A matching suffix is insufficient
to prove the four-row difference harmless.

The audit normalizer is unchanged. The failed evidence remains preserved and the
old history difference remains inconclusive. The next strict admin replay must
use a new explicitly owned pair with fresh journals. Its independently verified
receipt bindings, sequence assertions, and full comparison remain the acceptance
gate. Separate managed and actual-profile-restart drivers close the two pending
cases; this fixture correction does not claim those results.

## Validation status

The corrected runner's 25 offline tests passed. Python syntax checks cover the
runner, its tests, and the launcher. Native Go tests and the fresh paired runtime
replay remain required on ssd. The local staging environment has no Go compiler.
