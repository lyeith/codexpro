# Job ownership and failure-path admission

The public `status` and internal ownership settlement are separate facts. A job
can return a terminal result while its original scope or host claim remains
unresolved. Its identity, capture files and dependent output pins remain durable.

Running jobs **and terminal jobs with unresolved ownership** count against
`maxActiveCaptures`. Set the independent `CODEXPRO_MAX_ACTIVE_CAPTURES` limit
(1–64, default 24) in the private limits environment file and reload to change
new admission. Once that bound is reached, admission returns the existing
`job_limit_reached` error before allocating a job or launching a child.
Background concurrency fields retain their normal running-job meaning.
Verified original-scope quiescence and host settlement release the
reservation. When cancellation finds an already settled original host claim,
its verified terminal receipt also releases the reservation. Missing ownership
evidence and receipts for a different invocation keep it reserved.

The fallback queue limits waiting work per workspace and across the server.
Completed retry receipts stay available for deduplication under their existing
expiry and retained byte budget. They do not occupy pending queue slots.

Background/promoted completion and its audit outbox entry commit in one SQLite
transaction. `SetCompletionSink` retries until the sink confirms a durable record
or duplicate receipt; an error never acknowledges an event. The dedup identity is
`job_id:started_at`, so reuse of a compact public ID cannot suppress a later job.
Unacknowledged completion entries protect their job metadata from tombstone
pruning. Admission also stops at 1,000 pending completion receipts by default,
providing bounded backpressure if the audit sink remains unavailable.

The repository uses SQLite WAL with synchronous FULL. Prepared records and input
pins commit before helper allocation or runner launch; caller-generated helper
IDs make interrupted allocation cancellable. The launcher commits its intent,
then starts the runner with a private specification file. The runner records
its identity before executing the command. Recovery can revoke a launch whose
runner has not claimed that identity file. Immutable published records keep
activity and job-list reads independent of writer waits.

`Close` drains server workers and closes the repository without stopping accepted
detached runners. A new manager reloads prepared, active and terminal records
with unresolved ownership from its private state directory.

Roll back to the previous grant-based binary during a quiet window. Drain
requests and wait for running jobs and unsettled scopes or resource claims to
finish before swapping binaries. Jobs that are still queued can resume under
either binary.

Ordinary tests exercise direct runner launch, restart recovery, bounded capture,
redaction, UTF-8 cursors, pins, deduplication and concurrent admission. Explicit
host integration requires `CODEXPRO_GO_TEST_HOST_RESOURCES` set to the installed
absolute `ssd-dev` helper; it creates fresh private scopes/claims and checks full
scope drainage, environment ownership, restart and pre-allocation revocation.
Run builds and race/integration tests under `nice -n 10` with bounded timeouts.
