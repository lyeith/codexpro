# Job ownership and failure-path admission

The public `status` and internal ownership settlement are separate facts. A job
can return a terminal result while its original scope or host claim remains
unresolved. Its identity, capture files and dependent output pins remain durable.

Running jobs **and terminal jobs with unresolved ownership** count against
`maxActiveCaptures`. Once that bound is reached, admission returns the existing
`job_limit_reached` error before allocating a job, launching a child or granting
execution. Background concurrency fields retain their normal running-job
meaning. This failure-path bound deliberately closes the TypeScript behavior
that could accumulate unlimited unresolved owners while reporting their jobs
as finished. Verified original-scope quiescence and host settlement release the
reservation; a timeout, missing observation or different invocation does not.

Background/promoted completion and its audit outbox entry commit in one SQLite
transaction. `SetCompletionSink` retries until the sink confirms a durable record
or duplicate receipt; an error never acknowledges an event. The dedup identity is
`job_id:started_at`, so reuse of a compact public ID cannot suppress a later job.
Unacknowledged completion entries protect their job metadata from tombstone
pruning. Admission also stops at 1,000 pending completion receipts by default,
providing bounded backpressure if the audit sink remains unavailable.

The repository uses SQLite WAL with synchronous FULL. Prepared records and input
pins commit before helper allocation or runner launch; caller-generated helper
IDs make interrupted allocation cancellable. The runner executes only after a
second committed registration and exclusive durable nonce grant. Immutable
published records keep activity and job-list reads independent of writer waits.

`Close` drains server workers and closes the repository without stopping accepted
detached runners. A new manager reloads prepared, active and terminal-unresolved
records from its private state directory. It never imports or controls the live
TypeScript process store.

Ordinary tests exercise process grants, restart recovery, bounded capture,
redaction, UTF-8 cursors, pins, deduplication and concurrent admission. Explicit
host integration requires `CODEXPRO_GO_TEST_HOST_RESOURCES` set to the installed
absolute `ssd-dev` helper; it creates fresh private scopes/claims and checks full
scope drainage, environment ownership, restart and pre-allocation revocation.
Run builds and race/integration tests under `nice -n 10` with bounded timeouts.
