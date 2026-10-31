# Revoke ungranted host registrations before releasing ownership

Admission persists the caller-generated host run and registration IDs before
calling `external begin`. A server crash can leave that begin request in flight.
A quiescent `finish` receipt establishes current absence of owned activity; the
finish contract does not require `cancelled: true`. Only a verified cancellation
receipt establishes registration revocation, which prevents a late begin from
activating after the job manager has released ownership.

The previously staged settlement path called finish first for every terminal
record and only tried cancel on a finish error. The new path calls cancel
directly for prepared or otherwise ungranted records. Any helper error or receipt
without both quiescence and cancellation keeps ownership unresolved, preserving
its admission-capacity reservation and dependent output pins. Retrying a verified
cancel releases ownership through the existing durable transition.

`LaunchState == "granted"` alone is insufficient: the manager commits that field
before publishing the exclusive nonce grant file. Recovery also verifies the
bounded private specification and exact grant nonce. Missing, partial, different,
symlink or non-regular grant metadata selects cancellation. The metadata reads
cannot block on a FIFO. An intact published grant keeps normal finish behavior,
including its existing cancel fallback after a finish failure. No public tool
schema, returned outcome or process/admission identifier changes.

## Staged change

Add `recovery.go` and `recovery_test.go` to `go/internal/jobs`, then apply the
small `monitor-settlement.patch` against the formatted current `monitor.go`.
The patch replaces only the resource-helper finish/fallback block. Do not copy
the older unformatted monitor staging file over current source.

Keep the separately staged `pending_recovery_regression_test.go` unchanged for
the before/after proof. Run it against the current implementation first; its
private fake helper accepts finish(absent) but permits a late begin unless the
exact caller registration was cancelled. Then apply the fix and run the focused
tests under `nice -n 10`, `GOMAXPROCS=2`, and a bounded Go timeout.

The additional tests exercise failed and unverified cancellation receipts,
capacity refusal until exact settlement, refusal of a late begin, admission
after verified revocation, the committed-record/no-grant crash window, bounded
non-regular grant inspection, and unchanged normal completion settlement. No
real host scopes or claims are allocated by these tests. The candidate-only
`lifecycle_proof.py` supplies the separate actual-helper delayed-begin crash
experiment.

The installed-helper source is not present in this local cache. Its
cancel-before-begin revocation behavior was exercised successfully by the earlier
real-host integration test. This change does not assume that its absent-finish
behavior has been reobserved; the fake-helper regression establishes the API
distinction and the actual-helper lifecycle proof must still run. No new test
pass or production runtime result is claimed before root executes those gates.
