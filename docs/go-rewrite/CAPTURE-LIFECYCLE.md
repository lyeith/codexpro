# Lifecycle, stores, and performance capture

Source of truth: `/home/spite/Projects/codexpro-go` at `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`, the requested 0.31.0 baseline. Read-only source inspection on 31 October 2026. Earlier exploratory reads of the shared checkout at `500cad72d44` are explicitly excluded unless noted as a comparison. No live service/config/state mutation was performed by this capture task.

## Confirmed failure mechanisms

1. **Transient Git failures become persistent orphaned leases.** `src/worktrees/manager.ts:384–429` reconciles every lease sequentially. A ready checkout is inspected with Git. The catch block catches every exception, sets `state="orphaned"`, records the exception as failure, increments revision, and persists it. The same loop has branches for provisioning/ready/removing but none for recovering orphaned leases. `leaseFor`, lines 206–216, rejects every state other than ready. A Git timeout or temporary IO error is therefore sufficient to strand an otherwise existing checkout. This is a source-proven possible failure path, not proof that every observed orphan has this cause.
2. **One historical orphan can break unrelated project opens.** Exact baseline `src/work/runtime.ts:159` implements `listWorkspaces` by resolving every retained run with a workspace through strict `runtime.workspace`. An orphaned retained run throws during enumeration. `open_workspace` observed in this session returned orphaned for codexpro, projects and plans while direct read/tree calls still worked. The newer shared source contains a ready-only `listManagedWorkspaces` helper at lines 150–157, absent from baseline. This comparison supports a concrete regression test: historical missing/failed/orphaned managed leases must not prevent opening an unrelated direct project.
3. **Read operations write leases.** `manager.ts:358–381` updates timestamps/revision and calls `store.save` after every successful execute, including non-mutating calls. Reads run without the per-workspace mutation lock. `leaseStore.ts:73–87` initializes/chmods the lease directory, writes a fresh full JSON file, renames it, and chmods it. It does not fsync. Concurrent reads can race snapshots/revision writes even though JavaScript executes synchronously between awaits; durable writes complete asynchronously.
4. **Each lease inspection spawns Git twice.** `src/worktrees/gitDriver.ts:85–103` runs `git rev-parse --show-toplevel`, then `git rev-parse --git-common-dir`, canonicalizes multiple paths, and derives repository identity. Initialization additionally resolves the base ref per project. Default timeout is 180,000 ms; overflow/timeouts send SIGTERM without an escalation timer (lines 30–82). Reconcile is sequential.

## Durable stores and compatibility requirements

| Store | Baseline representation | Required semantics |
| --- | --- | --- |
| Project catalog | Configured JSON file, project IDs and canonical roots | Preserve returned IDs, deterministic direct-workspace IDs, allowed roots, creation roots, base refs and per-project limits. Keep live catalog read-only throughout stages 1–4. |
| Managed leases | `<worktreeRoot>/leases/<sha256(workspaceId)>.json`; schema versions 1/2 | Preserve repository/common-dir/scope binding, owner, idempotency key, branch/base pin, states, revision and timestamps. Invalid files are renamed to quarantine by baseline startup; migration must operate on a private copy. |
| Jobs | `<jobsDir>/jobs.json`, version 1; per-job spec, started, grant, control, result, exit, stdout/stderr and rendered output | Preserve IDs, output cursors, retention/pinning, status versus whole-owner quiescence, crash-recovery proof and process incarnation. Never adopt/stop production jobs during a private import. |
| Stored batches | Workspace `.codexpro-batches/ABCD.json`, definition version 1 | Four-hex random names, 20-file retention, serial/parallel mode, child operation IDs, resume by ID/index. Baseline adds the directory to Git local excludes and refreshes last-use mtime. Imported definitions remain editable text. |
| Audit/activity | JSONL action journal plus `.index.json`, `.lock`, transient replacement files | Stable action IDs and monotonic sequence; per-project retention, deliberate gaps versus corruption, expired/ahead cursor behavior, metadata-only public records, richer authenticated dashboard records. Preserve durable acknowledgement boundary. |
| Usage | `usage.sqlite`, WAL + synchronous FULL, 5-second busy timeout | Required feature independent of orchestrator. Tables `pro_submissions`, `usage_settings`, `usage_collectors`, `usage_changes`, `usage_requests`; transactional dedup/upgrade, revision checks, reset receipt replay and verified weekly anchor. |
| Inbox | `inbox.sqlite`, WAL + synchronous FULL, 5-second busy timeout | Required feature independent of Ralph. Tables `inbox_items`, `inbox_events`, `inbox_answers`; atomic publication/answer/event/idempotency receipt, revision CAS, per-consumer deliveries. |
| Orchestrator | `work.sqlite`, WAL + synchronous FULL | Runs, iterations, operations, sessions, versioned documents, events, requests, work_jobs and metadata owner. Dropping it requires usage census and approved scope; retained history alone must remain inspectable/exportable. |

`src/usage/store.ts:35–78` establishes the usage schema and ingestion transaction. Identity is `(query_id,turn_id)`; conflicting timestamps/projects are 409; missing project may be filled and selected mode evidence cannot be downgraded. Lines 80–127 implement idempotent reset and rate-limit verification. Reset changes a counting boundary without deleting history. `src/inbox/store.ts:33–105` implements publication idempotency, answer request replay and expected revision, consumer revision delivery and monotonically sequenced events. A Go SQLite implementation is the lowest-risk way to preserve these transaction boundaries and read/import their existing state; JSONL is possible only with explicitly reconstructed transaction and recovery semantics.

## Audit write amplification and blocking

Baseline `src/audit.ts:1789–1797` opens/appends/fsyncs/closes the action journal synchronously for every event. After each append, it calls `maybeCompact`; otherwise it writes/fsyncs/replaces the index (`1814–1825`, `2215–2250`, `2317–2364`). The action is acknowledged after this path. Do not improve throughput by acknowledging before durability without an approved contract change.

`maybeCompact` (2072–2213) scans all retained entries, computes per-project bucket counts and triggers if **any** bucket exceeds the retain count or total bytes exceed the limit. After selecting the newest permitted entries, it rereads/parses and serializes every retained record into a new file, fsyncs it, replaces the journal and index, resets its in-memory index and rereads it. Once a busy project is at exactly its retain count, each next event can trigger this complete rewrite even when total bytes remain under the limit. This is a source-proven write-amplification mechanism. Parent baseline process IO supports prioritizing it, but source inference must not be mislabeled a CPU-profile measurement.

Both reads and writes take `withJournalLock` (2032–2070). The lock uses exclusive file creation and synchronous `sleepSync` retries while waiting; it blocks the Node event loop. Status/list/get all refresh the index under this lock. Public records remove `dashboard_metadata` (937–941); the authenticated dashboard may retain shell scripts, and public export must not leak them.

Go design implication: preserve sequence/dedup/retention/cursor contracts with indexed transactional rows or append segments and small index updates. Keep compaction off the request critical path with an atomic generation swap. Durable event acceptance can use bounded group commit; callers wait for their committed generation. Retention must continue distinguishing planned gaps from actual corruption.

## Jobs, pinning, child isolation and load

`src/jobs.ts:206–243` synchronously scans/sorts all finished jobs, stats up to four logs per job, computes pinned bytes, splits unpinned budget 25% foreground/75% background, expires old output and rewrites the entire jobs JSON. Pin references are the `input_job_ids` of running jobs. Expired output retains bounded tombstones (500) to distinguish an expired job from a fabricated ID. Jobs with unresolved ownership are never discarded. `prune` is called from reads and persists at most once per second unless forced (270 onward). These semantics need dedicated contract tests; merely removing an old log can destroy evidence required to stop/recover a process safely.

`src/jobRunner.ts` is a detached supervisor. It publishes its process/cgroup identity before waiting for a registration grant; grant timeout is 10 seconds, polled every 25 ms. The command uses a process group and stdout/stderr capture; output caps stop the group. Stop sends TERM then KILL after 1.5 seconds. Completion checks group quiescence with synchronous `ps` and writes a result separately from the exit code. Control/timeout checks run every 100 ms. `src/jobResources.ts` integrates an optional external host resource helper using synchronous 10-second calls; begin/finish/cancel belong to the existing scope owner and must not create a second owner.

Coordinator row-31 requirement: separate the MCP server's CPU share from compiler/test children; server high weight, children normal/low weight, or at least nice children where delegation is unavailable. Runtime evidence supplied by parent at 15:36 UTC shows the current live server PID 829413 in `codexpro.service` with CPUWeight 1000, both foreground and background tool children in sibling `codexpro-job_*.scope` with CPUWeight 100, and no CPU quota. Therefore **do not claim current children all inherit the server cgroup**. The live service changed externally around 15:19; this task did not alter it. Go must preserve or improve this separation and prove actual `/proc`/cgroup placement during a bounded heavy build. Start all Go heavy proof at nice 10, with no sudo and no changes to the production service.

## Background and synchronous work

- HTTP transport pruning: `src/http.ts:1782`, interval `min(httpSessionTtlMs,60000)`.
- Job manager polling: `src/jobs.ts:581`, `POLL_MS`; starts only with active/unresolved jobs and stops when none remain.
- Detached job runner control/deadline polling: `src/jobRunner.ts:84`, 100 ms.
- Work coordinator sweep: `src/work/runtime.ts:121`, configured 15 seconds by default; `invoke` also calls sweep synchronously on every tool request (166).
- Managed mutations observe entire source before and after: runtime 191–192, 207, 228; `src/work/source.ts:9–67` uses six synchronous Git calls and synchronously hashes every tracked/nonignored source file up to a configured byte cap. Exact-base work source cap is 128 MiB (`config.ts:650`). This cost disappears only if the relevant unused orchestration is approved for removal; otherwise move observations off unrelated request paths.
- Dashboard browser polling is distinct: live/jobs 5 seconds, full dashboard/inbox 15 seconds, usage 5 seconds. Do not count client polling timers as server background loops.

## Relevant configuration

Exact baseline `src/config.ts:583–653` reads host/port/auth, Bash transcript/session, codex session directory, write/handoff/tool modes, path exposure, inherited env, read/write/output sizes, Bash/job limits, job state and external helper, import/search/session limits, blocked globs/context, audit and analysis limits, worktree settings, and work runtime settings. State root derives from `CODEXPRO_HOME`; explicit overrides include `CODEXPRO_JOBS_DIR`, `CODEXPRO_WORKTREE_ROOT`, `CODEXPRO_WORK_DIR`, `CODEXPRO_AUDIT_LOG`. HTTP token is never to be copied into reports.

Operational numeric limits use `OPERATIONAL_LIMITS` and can reload from a configured EnvironmentFile through SIGHUP (`config.ts:657–768`). The parser does not execute shell expansion; numeric changes are validated as a complete set before assignment. Startup-only keys are ignored and values are not logged. Stages 1–4 must not send SIGHUP to the live service. The Go runtime uses only its own token/state/config and port 7210.

## Required lifecycle regression cases

1. Git timeout, temporary IO failure, missing directory, and repository identity mismatch are distinct results; only verified identity loss can invalidate a lease. Retry transient inspection with backoff, never permanently orphan it on timeout.
2. One unavailable historical workspace cannot break unrelated direct open/list/read calls.
3. Concurrent read touches do not regress revisions or race durable snapshots; avoid full-disk touch per read.
4. Restart during job prepare/grant/launch/output/exit distinguishes unknown effects from successful completion; wrong/reused PID or cgroup identity is never adopted or signaled.
5. Stop/timeout/output cap terminate descendants, and API reports completion versus quiescence faithfully.
6. Pinned output survives unrelated retention; expiration preserves tombstone/cursor behavior and unresolved owner proofs.
7. Audit crash at append, index update, compaction swap and retention boundary preserves monotonic sequence and detects real gaps; public export never includes dashboard script payloads.
8. Usage/inbox duplicate request replay, conflicting payload/revision, and transaction rollback match baseline.
9. Heavy child CPU load does not consume the server priority allocation; record actual cgroup placement, nice level, latency and CPU/IO concurrently.
