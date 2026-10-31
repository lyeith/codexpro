# SP-6 Go rewrite — capture

Captured 31 October 2026 against release **0.31.0**, commit **44bb76bfa671e4aad540a8976f0fa6e6d5c2507a**. This is stage 1 of the requested stages 1–4; it does not authorize a deployment or cutover.

## Scope and evidence

The source of truth is the immutable deployed release and a separate `feat/go-rewrite` worktree at that commit. The ordinary `codexpro` checkout was at a different commit, so source observations below use `codexpro-go`, not that checkout. The running service's dependencies are MCP TypeScript SDK 1.32.2, Zod 4.6.7 and Express 5.3.0; the shared checkout's older dependencies are unsuitable for an exact private reproduction.

The detailed capture is split into reviewable appendices:

- [CAPTURE-CONTRACTS.md](CAPTURE-CONTRACTS.md): every descriptor, registration condition, input/output/error behavior, route, transport and compatibility trap.
- [capture-contracts/tool-input-source.json](capture-contracts/tool-input-source.json): exact source schema expressions and source locations.
- [capture-contracts/tools-list-live.json](capture-contracts/tools-list-live.json) and [tools-list-private.json](capture-contracts/tools-list-private.json): exact advertised JSON schemas.
- [capture-contracts/initialize-live.json](capture-contracts/initialize-live.json): initialization instructions, capabilities, version and negotiated protocol.
- [capture-contracts/schema-verification.json](capture-contracts/schema-verification.json): independent hash and parsed-equality verification.
- [capture-contracts/HTTP-PAYLOADS.md](capture-contracts/HTTP-PAYLOADS.md): HTTP payload contracts.
- [CAPTURE-LIFECYCLE.md](CAPTURE-LIFECYCLE.md): stores, ownership, concurrency, background loops, configuration and environment.
- [CAPTURE-USAGE.md](CAPTURE-USAGE.md): retained-history census and limits of retirement evidence.
- [evidence/runtime-census.json](evidence/runtime-census.json): machine-readable, metadata-only audit/SQLite census.
- [CONTRACT-PLAN.md](CONTRACT-PLAN.md): differential contract plan.

The live and private `tools/list` documents are byte-identical: **30 tools, 113,081 bytes, SHA-256 c2cdc3cbe64ce42e1dd7006cf3f6c7a9424ef07d831485219dbfa4ab376c7fab**. Initialization is also byte-identical: **673 bytes, SHA-256 08cf86466357c0069c7717bbe5b8c53593c2cdac0a5ff15fed6623430e3ba9ca**, negotiated protocol `2024-11-05`. The capture includes no bearer tokens or session IDs.

## Current runtime and isolation

The live user service is `codexpro.service`, port **7200**, serving the release under `~/apps/codexpro/current`. Its service has CPUWeight=1000 and Delegate=no. It was observed to restart externally at 15:19 UTC; SP-6 did not restart, signal or reconfigure it. Recorded live baseline server PID is 829413.

The current host-resource helper already places foreground/background tool jobs in separate `codexpro-job_*.scope` sibling scopes with CPUWeight=100. Thus the claim that all current tool children necessarily share the service's CPUWeight=1000 is not supported by this session's observations. The Go design must preserve separation and verify it under load.

`open_workspace` failed with `state: orphaned` for codexpro, projects and plans; direct read, write and Bash calls still worked. No lease was repaired or changed directly. This is an observed symptom, not proof that the repository is absent.

The private TypeScript process runs the immutable release on **7211**, with its own token, catalog, work state, audit and fixture repository under `~/.codexpro-go-proof/ts`. It never binds 7200 or 7210. Its catalog exposes the existing codexpro checkout for read-only baseline traffic and a private fixture for later mutation tests. It inherits no live CODEXPRO/CODEBASE_BRIDGE/LISTEN variables. The launcher and profile scripts are checked in; secrets and raw copied journals are not.

## MCP surface

### Active 30-tool surface

| Family | Names |
|---|---|
| Discovery and configuration | `list_projects`, `create_project`, `server_config`, `codexpro_self_test`, `load_skill` |
| Workspaces and analysis | `open_workspace`, `inspect_workspace`, `tree`, `search`, `ast_grep` |
| Files | `read`, `view_image`, `write`, `edit`, `apply_patch`, `import_file` |
| Processes | `bash`, `start_jobs`, `jobs`, `stop_jobs` |
| Git and batch | `show_changes`, `commit_changes`, `batch` |
| Activity | `activity_list`, `activity_get`, `activity_status`, `activity_export` |
| Managed work | `work_status`, `work_manage`, `work_update` |

There are **44 source descriptors**, including 14 conditional descriptors: the `codexpro` supertool and inventory, five workspace lifecycle tools, five handoff/context tools and two Codex session tools. Conditional availability is not evidence that workspace/lease capability can be removed.

All active tools advertise `execution.taskSupport="forbidden"` and `_meta.securitySchemes=[{"type":"noauth"}]`; the HTTP endpoint itself authenticates bearer tokens. No outputSchema is advertised. Registration order is nonalphabetical and is part of the frozen reference.

### Cross-cutting contracts

Results include text content and structured content, `codexpro_tool` and `codexpro_title`; some also include image blocks or `_meta: {}`. Absent fields, explicit nulls, empty arrays, object/array ordering where meaningful, redaction, UTF-8 byte limits and text rendering must be preserved. Ordinary business failures return tool errors; JSON-RPC failures have separate codes. Connector-added outer errors must not be mistaken for raw server behavior.

The source schemas generally strip unknown properties; Bash preserves a legacy hidden `background` flag. Work-enabled registration injects optional `execution.operation_key` into non-work tools. Bash advertises a 900,000 ms maximum although a reloaded runtime limit can be lower.

Read and complete search contexts establish edit provenance by principal and canonical file, including the full decoded text and displayed line ranges. Text SHA/tag hashes use UTF-8-decoded content while bytes reports the original buffer length; malformed non-NUL UTF-8 exposes this distinction. Four-character edit tags alone are insufficient: stale content, collisions and edits outside displayed ranges have distinct rejection behavior.

Batches validate their entire plan before effects, serialize mutations, parallelize eligible reads and retain edits after verification failure. They support stored definitions, failed-step resume and verification checkpoints. Job outputs have byte cursors, bounded pages, acknowledgement rules, input pins, expiration tombstones and a distinction between command exit and whole-owner quiescence.

## HTTP route inventory

All routes except the favicon authenticate. Bearer comparison is constant-time. Header authorization takes precedence over the dashboard-compatible `codexpro_token`/`token` query fallback. Defaults include no-store/security headers. Browser mutations enforce same-origin checks. The eleventh failed authentication per address within the one-minute window is the first 429 with Retry-After.

| Surface | Routes and behavior |
|---|---|
| Setup/health | Public `GET /favicon.ico`; authenticated `GET /`, `/setup`, `/healthz` |
| MCP | `POST/GET/DELETE /mcp`, streamable HTTP, JSON or SSE responses, 20 MB request bound, sessions/pruning/draining |
| Activity UI | `GET /activity`, `/activity/live`, `/activity/data`, `/activity/job`, `/activity/diff`, `/activity/batch`, `/activity/inbox` |
| Activity JSON | `GET /activity.json`, `/activity/projects/:projectId.json` |
| Admin profile | `GET/POST /admin/profile`, strict 32 KB patch, rate and origin checks |
| Inbox | List/events/item GETs; item/answer/delivery POSTs |
| Usage | Pro/history GETs; submissions/reset/limits POSTs |

The appendices enumerate exact inbox/usage paths and payloads. `/activity/live` is an HTML fragment, not an SSE feed. Dashboard/JSON polling must not heartbeat leases or acknowledge job output. Missing/malformed, unknown and wrong-principal MCP sessions have different HTTP/JSON-RPC errors. Graceful drain rejects new sessions while allowing in-flight work to complete.

## Stores and loops

| Store | Existing format and significant semantics |
|---|---|
| Project catalog | Versioned JSON; canonical roots, creation roots, stable IDs and atomic catalog changes |
| Workspaces/leases | Full JSON store; repository/branch/base identity, ownership, revision and lifecycle |
| Jobs | jobs.json plus per-job supervisor/spec/status/output files; detached ownership, pins, retention/tombstones |
| Stored batches | Per-workspace `.codexpro-batches/ABCD.json`, version 1, 20-definition bound and mtime use tracking |
| Audit/activity | Append/fsync JSONL plus fsynced index, sequence/dedup/cursor floors, per-project count and total-byte retention |
| Managed work | SQLite WAL: runs, documents, events, iterations, operations, requests, sessions and work_jobs |
| Usage | SQLite WAL/FULL, idempotent submissions, collector/change history and settings |
| Inbox | SQLite WAL/FULL, items, answers, deliveries/events and version/CAS rules |
| Edit/review state | Canonical-file snapshots, displayed ranges, review checkpoints and consumption markers |
| Optional handoff/Codex | File-backed context/handoff/session state, disabled in observed live configuration |

Job maintenance runs roughly every second and scans/stats/sorts retained logs plus rewrites jobs.json. Work coordination sweeps every 15 seconds and is also entered from tool calls. Session pruning is bounded by TTL (at most roughly a minute between checks); job polling runs while jobs are active. Configuration includes full Bash, workspace writes, standard tools, work mode on, worktree/handoff/Codex-session modes off, metadata audit and the host-resource helper. Full environment/limit inventory and reload behavior are in CAPTURE-LIFECYCLE.md.

Usage and Inbox are independent durable features and must survive any orchestration retirement.

## Retained-use census

The metadata snapshot at **15:43:13 UTC** read 2,405 retained audit records and SQLite stores read-only. The audit retention spans dates from August to October unevenly. Its index reports 113,022 rotations and 122,262 planned missing sequences. It is not a complete historical event archive.

| Tool/group | Retained calls | Most recent observed use |
|---|---:|---|
| read | 439 | 31 Oct |
| bash | 390 | 31 Oct |
| jobs | 319 | 31 Oct |
| search | 222 | 31 Oct |
| batch | 140 | 31 Oct |
| edit | 130 | 31 Oct |
| list_projects | 112 | 31 Oct |
| open_workspace | 97, including 20 failures | 31 Oct |
| write / tree / start_jobs / show_changes | 70 / 57 / 56 / 50 | 31 Oct |
| apply_patch / commit_changes | 28 / 8 | 31 Oct / 30 Sep |
| work_update | 49 | 30 Sep |
| work_manage / work_claim | 1 / 3 | 20 Sep |
| handoff_to_agent / export_pro_context / handoff_to_codex | 6 / 4 / 2 | 30 Aug |

The only October retained `work_status` call at capture was SP-6's own probe. This does **not** prove no users existed: the work database includes a manual run created on 2 October that has no corresponding retained work_manage call.

The work database contains **nine runs**: one ready Ralph run (anima), one blocked Ralph run (ibkr), and seven blocked plans runs, of which two are manual. None had state active in the snapshot. Startup recovery updated the plans runs on 31 October; that timestamp is not evidence of fresh user progress. The database is about 258 MB with 1,516 documents, 19,406 events, 17,705 operations, 1,369 requests, 173 sessions and 11,934 work-job records. Session ownership must be checked before retiring any writer; a run-state count alone is insufficient.

Usage has 276 submissions and 1,516 changes; Inbox has 20 items, 20 answers and 60 events. Preserve both.

**Retirement conclusion:** disabled handoff/Codex-session/supertool paths are candidates. Autonomous Ralph/decider/implicit work coordination has no demonstrated current consumer in the retained evidence, but retained work history and the ready anima run require an explicit coordinator ruling. Low observed use is not sufficient grounds to drop Git, analysis, pinning, projects, leases or other requested build features.

## Reproducible baseline

`profile.py` runs eight read-only request shapes through real authenticated MCP sessions: project/config discovery, two file reads, tree, text search, Git status without marking reviewed and activity status. Each sample has **160 calls**, **two workers**, a **50 ms per-worker gap**, a **180-second ceiling**, and 20-second request timeouts. It records raw per-call timing/shape metadata, nearest-rank percentiles, process CPU/RSS/IO and host load. It never records tokens or source payloads.

`profile_under_build.py` adds one bounded `nice -n 10` compiler build with `GOMAXPROCS=2`, `go build -p 2 -a`, a private cache and a 90-second ceiling. Every heavy run uses nice 10. No service profiler or signal was attached to the live server.

| Sample | p50 ms | p95 ms | p99 ms | Errors | Server CPU s | Peak RSS KiB | Written bytes |
|---|---:|---:|---:|---:|---:|---:|---:|
| Live, observed shared traffic | 122.217 | 1,416.142 | 9,673.830 | 0/160 | 23.70 | 444,652 | 228,990,976 |
| Live, bounded build also active | 461.119 | 1,257.411 | 2,548.687 | 0/160 | 47.91 | 474,220 | 640,294,912 |
| Private fresh, tmpfs (exploratory) | 7.840 | 384.536 | 785.915 | 0/160 | 11.51 | 163,960 | 0 |
| Private ext4, copied retained history, below codexpro cap at start | 14.775 | 336.037 | 687.270 | 0/160 | 10.38 | 176,284 | 2,342,912 |
| Private ext4, transition to cap, bounded build | 28.671 | 580.021 | 1,378.610 | 0/160 | 19.13 | 199,048 | 72,855,552 |
| Private ext4, saturated project retention | 275.968 | 767.385 | 840.197 | 0/160 | 32.31 | 308,460 | 787,816,448 |
| Private ext4, saturated retention, bounded build | 348.268 | 967.089 | 1,046.816 | 0/160 | 40.49 | 261,084 | 791,773,184 |

The live process also served unrelated users; its CPU and IO are not solely attributable to these calls. A lower loaded p99 than the earlier live p99 does not mean load improves performance. The tmpfs run is not an acceptance comparator. The first ext4 copied-history run ended with 324 codexpro actions, below the 350/project cap. The subsequent saturated runs reached the relevant steady retention workload and are the primary Go comparators. Saturation increased private write volume from 2.34 MB to about 788 MB for the same call mix; shared host variation remains a limitation on precise latency attribution.

All individual measurements and commands are in `evidence/*baseline*.json`, `*under-build*.json`, `private-ts-fresh.json`, `private-ts-retained.json` and `private-ts-saturated.json`. The raw copied journal remains private; only its stable-copy size/hash receipt is committed.

## Where time and writes go

These mechanisms are directly visible in exact-commit source and supported by the isolated V8 profile in `evidence/private-ts-cpu-summary.json`. Across 973,378 statistical samples, 88.77 seconds were non-idle sampled time: audit/activity ancestry accounted for 61.66%, jobs/process 14.41%, repository analysis 11.13%, workspaces/work 4.35%, and garbage collection 3.63%. The largest self frame was audit parseAction (25.88%), followed by maybeCompact (8.31%); fsync, read, write and spawnSync also appear. The profile includes startup, all private read-only runs and maintenance between them. V8 sampled time is not a complete CPU/IO decomposition and excludes child-process CPU; it must not be reported as a live-service causal profile.

1. **Audit retention amplifies writes.** `audit.ts:1789–1797` fsyncs every appended action. The index is also fsynced. A synchronous cross-process lock/sleep blocks Node (`2032–2070`). Once a project's action count is at its cap, the retention path (`2072–2213`) can rewrite/fsync the entire journal and index on each new action, even when the byte cap is not reached.
2. **Read requests persist lease metadata.** `worktrees/manager.ts:358–381` touches and saves the complete lease JSON after every operation. Concurrent readers do not hold the mutation lock. The store uses temporary write/rename/chmod without fsync.
3. **Transient inspection becomes permanent orphaning.** The manager catch-all (`384–429`) marks a lease orphaned on any reconciliation exception, including Git timeout. Strict lookup then rejects it. `work/runtime.ts:159` resolves every historical run through that strict path, so one unavailable lease can poison otherwise unrelated workspace opens.
4. **Repeated process and Git inspection.** Git workspace observation launches two commands; managed mutations fingerprint source and can run six synchronous Git commands plus hash up to 128 MiB. This work is entered from broad request paths rather than only relevant changes.
5. **Job retention does broad repeated work.** The one-second loop scans, stats and sorts finished logs and rewrites all metadata. Its pinning, unresolved-owner and foreground/background budget semantics are necessary; its full-store rewrite is not.
6. **Blocking work shares Node's request execution.** Synchronous filesystem/lock/child-process operations and audit serialization amplify unrelated-call tail latency. CPUWeight cannot repair an event loop blocked in synchronous work.

## Capture limits and stage gates

This census is explicitly bounded by retention and an active shared host. The main schema, input source, output/error source, route/store/loop/config inventory, live baselines, isolated saturated profiles and V8 sampling are captured. A complete raw runtime-output golden set, fault/restart tests and Go comparison remain stage-3/4 gates. The installed process-helper protocol and source linkage are detailed in PROCESS-CONTRACT.md.

No source, config, token, state or unit belonging to the live service was directly modified. No Go server has been deployed. Implementation must wait for the design ruling in registry row 31.
