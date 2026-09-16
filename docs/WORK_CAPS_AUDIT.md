# Work cap audit — 2026-09-16

The SSD Anima run reached 20 iterations and paused at revision 97. Two requests
for `max_attempts=40` succeeded but silently used `min(request, server ceiling)`
and left the effective maximum at 20. This was independent of the cumulative
time cap removed in `69c29ab`. Plans also had two no-progress iterations, one
below another automatic blocker.

## Work lifetime gates removed

| Previous gate | Behavior after this change |
| --- | --- |
| Two-hour cumulative run time | Already retired; elapsed time is reporting only. |
| 20 attempts, server maximum 200 | No iteration-count limit, including existing runs at/over the old maximum. |
| 25-minute hard claim duration | No duration ceiling while the worker maintains its idle lease. |
| Three no-progress iterations | Advisory health signal; execution and checkpointing remain available. |
| 100,000 retained request receipts | No count-based admission rejection; exact idempotency records remain retained. |
| 10,000 operation receipts per run | No count-based mutation rejection; uncertain effects still require reconciliation. |
| 100 retained runs | No lifetime run-count gate. |
| Ordinary retained-workspace quota applied to run history | Managed run provisioning is independent of ordinary workspace-count quotas. |
| 200 documents / 16 MiB of accumulated revisions | No aggregate history quota and no reservation derived from attempt count. |
| Full-plan replacement limited to 200 todos / 50 criteria | Bounded upsert pages grow a plan beyond those per-call counts. |
| Generated specification limited like a user-authored note | Generated control documents can represent the full plan; reads remain paged. |

The `active_ms` and `max_attempts` compatibility inputs explicitly report that
they are ignored. Public status includes the effective unlimited policy even
when other packet content is truncated. Migration preserves counters, plans,
documents, receipts, and explicit paused/blocked states. Review the retained
reason before resuming a previously paused run. No automatic completion is added.

## Bounds retained for individual operations and resources

| Bound | Reason / handling |
| --- | --- |
| Idle claim allowance: default 10 min | Recover a disappeared agent. Authenticated calls/heartbeat renew it; unrelated inspection does not. |
| Exactly one current writer per run | Prevent concurrent agents from overwriting each other. Old generations cannot resume writing. |
| Individual job deadline: default 25 min; configurable up to 6 h | Bound a launched process, independently of work already completed. Each new command gets its own allowance. |
| Running jobs: default 6/workspace, 12/server | Simultaneous capacity; collect or stop jobs to free capacity. Foreground admission also has a server ceiling. |
| Captured command output: default 8 MiB | Current supervisor stops a command that exceeds capture capacity. Redirect verbose output to an appropriate artifact or configure the per-job bound. |
| Finished job-log retention: default 24 h, count/byte budgets | Old raw output may expire; durable operation/checkpoint history remains. Pin logs during analysis and retain important conclusions/evidence. |
| Bounded MCP requests, returned pages, batches and individual notes | Split inputs and use pagination. Ordinary read/search/write and HTTP session limits remain. |
| Actual disk capacity | Retained history and checkouts consume disk. This change does not promise infinite storage or silently prune evidence. |
| Source scope, blocked paths, principals, revision checks and evidence validation | Integrity and access boundaries remain enforced. |

Regular non-run workspaces retain their configured workspace-count quota.
Manual and Ralph work runs share the unlimited lifetime policy; only Ralph gets
the server-clock 30-minute continuation guidance.

## Remaining certification limitation

`observeSource` still fingerprints at most 50,000 paths / 128 MiB by default,
with bounded Git subprocess output/time. Exceeding those observation bounds
does not prevent ordinary work, but **does prevent whole-run certification**
because the coordinator cannot prove unchanged source across acceptance checks.
An existing paused Plans audit run records precisely this 128 MiB problem.

This needs a separate source-observation refactor: asynchronous/incremental
manifest construction outside SQLite write transactions, chunked hashing and
explicit invalidation, with a complete final comparison. Merely removing the
bound from today's synchronous scan could monopolize the MCP process; treating
a partial fingerprint as complete would misrepresent the evidence. Neither is
done in this change. Failed tests, changed source and incomplete observation
remain explicit, distinct from exhausted work budgets.

## Verification

Regression coverage exercises resumed legacy caps, iteration 1001, several hours
of authenticated claim activity, a long admitted operation followed by a handoff,
dead-agent expiry and stale-writer rejection, retained histories beyond both
receipt ceilings and document quotas, and plans larger than one request.
Existing process supervision, replay, HTTP, atomic checkpoint and text-only
response tests remain applicable. No live Anima source is changed by this fix.
