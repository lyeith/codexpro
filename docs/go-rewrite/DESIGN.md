# SP-6 Go rewrite — design for coordinator review

**Status: proposed; implementation waits for registry row 31.**  
Date: 31 October 2026. Baseline: CodexPro 0.31.0, `44bb76b`.  
Branch/worktree: `feat/go-rewrite`, `/home/spite/Projects/codexpro-go`.

This design covers capture, rebuild and side-by-side proof only. It does not authorize stage 5 or any change to the live service.

## 1. Outcomes and invariants

Build a native Go service that preserves the main build workflows and their existing tool names, schemas, structured results, text/image content, errors and HTTP routes. Remove measured sources of repeated blocking work: synchronous whole-journal retention, broad historical-work reconciliation, per-read full lease persistence and one-second full job-store rewrites.

The service always runs with full Bash permission. It has no approval-mode state machine or approval prompts. Existing file-root guards, edit provenance, input validation, authentication, batch validation and process-ownership checks remain because they define correct operation, not approval machinery.

Non-negotiable operational boundaries:

- Live `codexpro.service`, port 7200, `~/.config/codexpro` and `~/.codexpro` are read-only to this project.
- Go binds **127.0.0.1:7210** with an independently created token and state directory, normally `~/.codexpro-go`.
- Private TypeScript uses 7211 and `~/.codexpro-go-proof/ts`. No test mutates the live TypeScript instance.
- No sudo, live reload, service restart, symlink switch, unit installation or deployment.
- All heavy builds, races and benchmarks use nice 10 and bounded workers/time. The service is an explicitly started private process, never the user service.

The Go entrypoint rejects port 7200 and refuses a writable state/token path inside either live state/config tree. Explicit flags override inherited environment. The harness strips inherited live CODEXPRO variables before launching either private server.

## 2. Compatibility boundary and retirement decision

[CAPTURE.md](CAPTURE.md) and its appendices are the baseline. The exact live `tools/list` and initialization captures are authoritative, including descriptor ordering, annotations, `execution.taskSupport`, security metadata and lack of outputSchema. Every retained tool uses those input contracts; code generation from Go structs must not silently replace them.

### Required retained capabilities

Files, edit/patch, image reads/import, Bash, detached jobs, process stop/timeout, output paging, pins, batches, text/config/AST/repository analysis, projects, workspace/lease lifecycle, Git, audit, activity (including `/activity/live`), usage, inbox and bearer HTTP authentication remain. Conditional workspace creation/list/open/release/remove functionality remains available in the relevant workspace mode even though live mode is currently off.

The result envelope preserves text and structuredContent together, optional image blocks, `codexpro_tool`, `codexpro_title`, explicit null versus absent keys, error codes/recovery payloads, path redaction and byte bounds. Raw server errors, rather than connector-added outer wrappers, are the comparator.

### Proposed drops, requiring the coordinator's ruling

| Candidate | Census/source evidence | Proposed treatment |
|---|---|---|
| Handoff/context dispatch and embedded Codex sessions | Handoff and Codex-session modes are off; retained handoff calls last appear in August | Omit disabled runtime loops/tools; retain legacy files as read-only archives |
| Supertool/inventory alternate registration | Current standard mode exposes 30 individual tools; no demonstrated current supertool consumer | Omit alternate dispatch mode; keep ordinary named tools |
| Autonomous Ralph/decider/monitor orchestration | No recent work mutation in retained October audit; eight blocked and one ready historical run; startup-only updates | Remove autonomous scheduling/decision loops and implicit global coordination from ordinary tools, subject to ownership/consumer confirmation |
| `work_manage` / `work_update` mutation APIs | Historically used, including a manual run created 2 Oct not represented in retained audit | **Keep unless coordinator explicitly approves retirement.** No claim of non-use based only on missing retained events |
| `work_status` | Needed to inspect retained run history during transition | Keep read-only history/status compatibility |

The ready anima run is a specific unresolved retirement concern. The review request asks the coordinator to confirm its owner/consumer and choose among full work-API retention (including the published Ralph mode), manual-only retention with an explicit approved Ralph-mode delta, or retirement of work_manage/work_update. Keeping names/schemas while silently disabling Ralph semantics is not equivalent. Any retirement records exact descriptor/behavior changes and archive-only migration as approved deltas, not passing unchanged contracts. No autonomous orchestration is removed until this ruling settles its consumer and mode semantics.

Nothing in this proposal permits deleting historical work data, dropping usage/inbox, or removing low-frequency build features.

## 3. Packages and integration model

Go module: `github.com/lyeith/codexpro/go`, under `go/`. Initial compiler: installed Go 1.26.8, explicitly recorded in evidence. Dependencies are pinned in go.mod/go.sum. No change to the host's global toolchain is needed.

| Package | Responsibility |
|---|---|
| `cmd/codexpro-go` | HTTP server, runner, explicit import/inspect commands, own-process lifecycle |
| `internal/wire` | Frozen descriptors, JSON DTOs, text/error rendering, redaction; no IO |
| `internal/config` | Own configuration, catalog loading, validated limits, atomic reload snapshots |
| `internal/engine` | Dispatch, schema/default handling, request scope, audit wrapper and tool registry |
| `internal/workspace` | Project IDs, canonical roots, lease ownership/lifecycle and bounded Git observation |
| `internal/files` | Read/write/edit/patch, per-file locks, provenance snapshots, images/import |
| `internal/search` | ripgrep/ast-grep execution, config queries, scope/diff/repository analysis |
| `internal/git` | Status/diff/commit and review checkpoints |
| `internal/process` | Detached runner, host-resource helper, owner identity, signals/quiescence |
| `internal/jobs` | Durable admission, output files/cursors/pins, collection and bounded retention |
| `internal/batch` | Preflight, dependency scheduling, stored definitions, resume and checkpoints |
| `internal/audit` | Durable segmented JSONL, indexed activity reads, retention and export |
| `internal/usage`, `internal/inbox` | Existing SQLite business semantics and HTTP DTOs |
| `internal/work` | Retained work history and any expressly retained manual work APIs |
| `internal/transport` | MCP HTTP sessions, auth, routes, browser-origin rules, graceful drain |
| `internal/store` | SQLite/atomic-file helpers, schema versions, import receipts and corruption errors |

The wire package is a dependency leaf. Batch calls a dispatcher interface and never calls HTTP handlers. Domain code returns typed errors; the engine maps them to existing tool failures. HTTP/JSON-RPC errors remain transport errors.

Shared request scope carries principal, session, request ID, workspace and parent action. Identifiers are explicit rather than hidden in globals. Each component accepts a context, a clock where tests require it, and a small interface for external work.

## 4. Transport and exact inputs/outputs

Use official `github.com/modelcontextprotocol/go-sdk` **v1.8.0**, with low-level `Server.AddTool` and captured raw JSON schemas. This path deliberately leaves argument validation/result construction to CodexPro. The generic inferred-schema helper is unsuitable for preserving the current contracts.

The adapter will verify the SDK's serialized descriptor and initialization behavior against the frozen documents. If SDK serialization reorders descriptors or omits fields, a small standards-conforming serialization adapter supplies the captured representation; this must not become a second independent business dispatcher. Restrict negotiated protocol versions to versions supported by the baseline and tested by the suite, initially including captured `2024-11-05`.

Validation applies baseline defaults, integer/enum/length limits, unknown-field stripping and the Bash compatibility exception. Published schemas and runtime validation are tested separately because baseline runtime limits can be narrower than the published maximum.

Responses are built with explicit maps/DTO fields. Use `omitempty` only where the baseline actually omits a field. File hashes/tags, patch text, line numbering, cursor shapes and errors remain exact. Dynamic values receive field-specific bindings in the contract harness, never blanket removal.

HTTP uses net/http with bounded bodies, per-request deadlines where appropriate, session caps/TTL, graceful shutdown and no unauthenticated debug endpoint. Preserve setup, health, activity/dashboard, admin profile, inbox and usage routes. `/activity/live` remains an HTML fragment. Assets/templates are embedded in the binary and retain browser behavior, data attributes and escaping.

Bearer comparison is constant-time. Preserve header precedence, query-token dashboard compatibility, request-origin checks, auth-failure rate limiting, no-store/security headers and session-principal binding. Tokens never enter audit, logs, URLs emitted by the server or test artifacts.

## 5. Concurrency and scheduling

Use one goroutine per accepted request with bounded admission for expensive subprocess work. Initial private defaults are four global heavy operations, two per workspace and the existing configured job counts; all are measured and adjusted only with evidence. Cheap file/status reads do not wait behind a long Bash process or an unrelated repository's reconciliation.

Locks are narrow and ordered:

1. Resolve immutable configuration/workspace identity.
2. Acquire sorted canonical-file or workspace mutation locks when needed.
3. Perform bounded external work outside SQL write transactions.
4. Commit a short state transition with expected revision/generation.
5. Release locks before rendering/streaming a response.

There is no global lock spanning Git, Bash, remote import, batch children, source hashing or network output. Audit has its own bounded writer; usage, inbox and core state use separate short transaction boundaries. Cancellation stops request-owned subprocesses but does not silently cancel already accepted background jobs.

Cache only observations with explicit keys and invalidation: repository identity/status by checkout generation and bounded TTL; analysis by file hashes and relevant Git state; configuration snapshots by generation. Ownership, job availability, edits and pins are not satisfied by stale cache entries. Deduplicate simultaneous identical Git observations to avoid spawn storms.

Maintenance works from indexed candidates and deadlines, not full directory scans on every tick. It is bounded by time/item budgets and uses lower process priority for heavy helpers. Metrics are available through authenticated diagnostics and test receipts, without putting implementation details in normal user flows.

## 6. State and durability

Use native SQLite through pinned `github.com/mattn/go-sqlite3` **v1.14.52**, CGO enabled, with WAL, synchronous FULL, foreign keys and bounded busy timeout on every connection. Confirm the linked SQLite version in build evidence. One bounded writer per database and a small read pool avoid write contention becoming a new global bottleneck.

- Core private `state.sqlite`: project changes/leases, job metadata, pins, tombstones, batch metadata and import records where durability is required. Edit snapshots remain process-memory state shared by principal across sessions; review checkpoints remain per-MCP-context/session memory, matching restart and cross-session behavior.
- Usage and Inbox: keep existing tables/transactional semantics in independent private databases.
- Work: import a consistent snapshot for history; retained mutation APIs use their existing idempotency/revision semantics.
- Large outputs remain bounded files. SQLite stores references, lengths, lifecycle and ownership, not log blobs.

Accepted persistent transitions wait for commit. No acknowledgement-before-durability shortcut. Atomic file replacement uses write, fsync, rename and parent-directory fsync where the artifact requires crash durability. Store errors are explicit; no silent empty-state reset after malformed JSON or SQL corruption.

### Audit: preserve JSONL honestly, remove rewrite amplification

JSONL remains the authoritative event format, so `activity_status.storage_format="jsonl"` stays truthful. Use append-only bounded segments plus a derived SQLite index for action ID, sequence, project, time, dedup and retention visibility.

A single writer allocates sequences and batches at most a short interval (target 5 ms, bounded queue). Each caller waits until its event's append is fsynced and the index/retention boundary is committed. Startup can replay journal bytes beyond the last durable indexed offset. Replay is idempotent by sequence/action ID and validates record framing/checksums where present. A torn trailing record is diagnosed and recovered according to a documented receipt; interior corruption must not be hidden as planned retention.

Logical latest-N-per-project and byte-budget retention happens with index updates. The public view and cursor floor change atomically. Physical segment reclamation/compaction is incremental and runs outside ordinary request locks. It never rewrites the full journal per action. Retention gaps are distinguished from corruption, sequence IDs never reset, and legacy export remains JSONL in the existing shape.

A crash after journal fsync but before indexed acknowledgement may replay an unacknowledged event; request dedup prevents duplicate visible actions on retry. Tests cover every boundary. Any unavoidable visible retention/storage-metadata difference is documented and reviewed rather than normalized away.

## 7. Workspaces and leases

Separate durable lifecycle/ownership from temporary inspection health. A ready workspace remains owned/ready after a Git timeout or transient IO error; record the failed observation, bounded backoff and diagnostic separately. Verified absence or identity mismatch can make it unavailable, with an explicit reason. Cancellation never converts a lease into an orphan.

Git inspection happens outside the store transaction and applies only if the lease generation/revision still matches. A late result cannot overwrite a newly created/released lease. Coalesce read touches in memory and periodically persist a bounded delta; ownership, branch/base and release changes remain immediately durable.

Direct project reads do not enumerate every historical work run. List/open paths isolate unavailable historical leases, retain their diagnostics and continue serving unrelated ready projects. Preserve stable project/workspace IDs and principal ownership semantics, including the baseline's static-token principal behavior.

Workspace creation/release/removal holds a per-project mutation lock, verifies the intended checkout, and rejects destructive removal of dirty/owned state according to the existing contract. Full Bash permission does not bypass managed lease identity checks.

Required fault test: inject a transient Git timeout for one historical lease while continuously reading/opening an unrelated direct project. The healthy project must remain usable and the timed-out lease must recover without manual JSON edits.

## 8. Processes, jobs, output and pins

Use the same executable in a small detached **runner** mode. An in-process exec.CommandContext alone cannot preserve jobs through server restart.

Admission validates the complete request and all `input_job_ids`, acquires pins and creates a prepared job in one transaction. Persist a fresh launch nonce plus host-resource run/registration IDs before allocation. The runner publishes its boot ID, PID start-time identity, owner scope and receipt, waits for a durable registration grant, then launches the command. A failed or timed-out registration is revoked, including a tombstone that prevents a delayed launch.

The installed `ssd-dev` helper protocol provides normal-weight sibling scopes and whole-owner identity. Preserve its begin/exec/finish/cancel integration where available, using caller-provided IDs to close the pre-persist allocation gap. Do not modify the helper, live service cgroup or Plans priorities.

Tool children run at nice 10. The private process explicitly enables and probes user scopes (`CODEXPRO_JOB_SCOPES=1` for the TypeScript reference), preserving XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS; baseline auto-detection otherwise disables scopes outside a systemd invocation. Verify actual scope placement/CPUWeight during stage 4; the live service already uses a helper-created sibling scope, so priority claims must be measured. The private Go server is not installed as a service.

Command exit and owner quiescence are separate states. A descendant can call setsid, so process-group signaling alone is insufficient. Prefer the unique helper scope, verify emptiness and identity, and preserve unresolved ownership rather than falsely announcing completion. TERM escalates after the existing grace period; signals target only verified owned identities. A fallback without reliable scope ownership is reported as degraded/unknown and must not pass whole-owner acceptance tests.

Persist bounded stdout/stderr streams and terminal receipts. Preserve job status/origin, deadlines, output sizes, truncation, retention notes, UTF-8 cursor boundaries, incremental/head/tail modes, acknowledgement rules and background-job digests. Foreground timeout with background continuation retains the job instead of killing it accidentally.

Retention marks candidates transactionally, respects pins/unresolved owners, preserves foreground/background budget proportions and expiration tombstones, then removes eligible files. A reader sees either available output or the established expired response. Pins release only after dependent ownership is quiescent, including crash/restart recovery.

## 9. Files, search, Git and batches

Canonicalize and enforce workspace/blocked-path guards consistently. Text reads preserve the baseline hash of UTF-8-decoded text (including replacement of malformed non-NUL bytes), original buffer byte count, line semantics, empty files, CRLF behavior and bounded pagination. Image/import hashes remain raw-byte hashes where the baseline uses them. Maintain edit snapshots by principal+canonical file across HTTP sessions. The four-character tag is checked against the full decoded-text snapshot and shown ranges; stale/collision/unseen edits fail before writing.

Mutations lock only affected canonical files and validate every hunk/path first. Preserve existing-file in-place rewrite/fsync semantics (including mode/inode/hardlink behavior) and new-file temporary-write/fsync/rename behavior; do not silently substitute rename for every existing write. Stronger crash guarantees require an explicit compatibility delta and alias/mode tests. Patch handles add/update/delete/move and existing error/context behavior. Remote import uses bounded streaming, redirects/content limits and the captured HTTP/file security checks. Image output preserves MIME, size and MCP image blocks.

Use installed ripgrep and ast-grep as actual engines, with argument arrays, cancellation, bounded output and no shell interpolation of user patterns. Preserve text/config/symbol/reference/impact intent, changed-file/diff scopes, contexts, pagination/fingerprints and analysis warnings. Do not replace AST matching with regex.

Git status/diff can return successful partial output with status_error/diff_error as baseline does. Commit review/checkpoint and staged/path behavior remain explicit. Cache invalidation follows mutations and external Git changes.

Batch validates all operations before effects. Mutations remain ordered; independent reads can run under a bounded worker pool. Verification Bash starts after writes, cannot silently background, and failure does not roll back successful edits. Resume starts from the failed step and does not repeat prior successful mutations. Stored batch IDs, version, retention and Git-local excludes remain compatible.

## 10. Migration and rollback boundaries

There is no live migration in stages 1–4. An explicit importer reads source state and writes only the separate destination, creates a manifest with source paths/hashes/counts/schema versions and is idempotent. Import never changes source mtimes intentionally, checkpoints live databases or follows output paths outside the allowed source/destination policy.

- Catalog and lease JSON: parse and validate into versioned destination tables, preserving IDs and history. Do not adopt a live lease as a writable Go lease.
- Jobs: import completed metadata/logs for read-only history; active live processes remain owned by the TypeScript service. Never signal or claim them.
- Audit: take a stable journal+index snapshot, record its bounds/hash, load as an initial segment and preserve sequences/cursor floors/dedup.
- Usage/Inbox/Work SQLite: use SQLite's backup API from a read-only source connection; copying only the database file would omit WAL changes. Never set immutable=1 on a changing live database.
- Batches: read existing version-1 definitions; test writes only in private fixture copies.
- Disabled orchestrator data: retain a read-only archive with provenance; no implicit execution on import.

The future runbook will require an explicitly approved outage/cutover plan, consistent final snapshot, owner drain, token/config mapping and rollback verification. Stage 4 produces that document but does not execute it. Rollback for this project is simply stopping only its own Go/private TypeScript processes; the live service remains untouched.

## 11. Performance targets

See CAPTURE.md for the sample definitions and limitations. Stage-1 live baseline: p50 **122.217 ms**, p95 **1416.142 ms**, p99 **9673.830 ms**, 23.70 CPU seconds, peak RSS 444,652 KiB and 228,990,976 bytes written during 160 mixed read-only calls. Those values include unrelated live work.

The matched private ext4 retained-history sample before saturation measured p50 **14.775 ms**, p95 **336.037 ms**, p99 **687.270 ms**, **10.38 CPU seconds** and **176,284 KiB peak RSS**. Once the project was at its 350-action cap, the same 160-call mix measured p50 **275.968 ms**, p95 **767.385 ms**, p99 **840.197 ms**, **32.31 CPU seconds**, **308,460 KiB peak RSS** and **787,816,448 bytes written**, with zero failures. This saturated private sample is the primary ordinary-condition comparator. The fully saturated bounded-build sample measured p50 **348.268 ms**, p95 **967.089 ms**, p99 **1046.816 ms**, **40.49 CPU seconds**, **261,084 KiB peak RSS**, **791,773,184 bytes written**, also zero failures. Claims compare equivalent catalog/state/history and resource priority.

| Metric | Acceptance target |
|---|---|
| Exact retained contracts | No unexplained mismatch in required fixture suite; approved removals/deltas listed separately |
| Mixed 160-call private profile, ordinary conditions | p95 <=100 ms; p99 <=250 ms; zero errors |
| Same profile under bounded two-worker build | p95 <=250 ms; p99 <=500 ms; zero errors |
| Relative latency | At least 2x lower mixed p95 than matched saturated TypeScript sample |
| Server CPU per matched 160 calls | <=50% of TypeScript, with a stretch target <=25% |
| Memory | Idle RSS <=100 MiB; peak <=256 MiB in bounded profile/soak; no sustained growth across repetitions |
| Audit retention write amplification | >=90% reduction in bytes written for matched saturated-cap traffic, without weaker durability |
| Isolation | Ordinary file/config/status reads continue while another job/build or repository inspection is slow |
| Lease health | Zero false orphan transitions under transient inspection failures and healthy-project isolation |
| Process correctness | No owned descendant left running after confirmed stop; no false quiescence or reused-PID signal |
| Durability/restart | Accepted actions/jobs/pins/history survive crash/restart tests; no duplicate mutation on idempotent retry |

Targets are gates, not achieved results. A missed gate remains visibly open in RESULTS.md. Do not lower targets after measuring merely to label the stage complete. Record local/network timings separately; connector/proxy timeouts are operational evidence but not a Go handler benchmark.

## 12. Contract and test plan

One Python standard-library harness drives identical recorded MCP calls against endpoints supplied on the command line. Live mode permits only an explicit read-only allowlist; mutation, restart and fault lanes require a private endpoint and private fixture root. Never infer permission from a tool name alone: show_changes must not mark reviewed, and dashboard polling must remain free of lease/job acknowledgement effects.

Raw requests/responses are saved after secret redaction. Bind only specific dynamic IDs/times/roots/elapsed values. File hashes, edit tags, errors, array order, text, cursor behavior and side effects remain exact. Derivative hashes such as query_fingerprint are independently recomputed with each endpoint's bound workspace ID. Commit IDs are checked through parent/tree/content, not erased from assertions.

The matrix in CONTRACT-PLAN.md covers:

- Descriptor/init equality and all retained tool success/error inputs.
- File pagination/UTF-8/CRLF, stale and unseen edits, atomic patch failures and image/import.
- Text/config/AST/scoped search, contexts/fingerprints and Git partial failures/checkpoints.
- Foreground/background Bash, timeout policies, output paging/pins/retention, descendant stop and restart adoption.
- Batch preflight, mixed effects, verification failure, stored resume and idempotency.
- Workspace ownership, concurrent mutations, transient Git timeout, unrelated historical orphan.
- Audit durability, dedup, retention/cursors, export and dashboard read side effects.
- Usage/Inbox CAS/idempotency/migration and every HTTP route/auth/origin/body-limit error.
- Invalid/missing/wrong-principal sessions, drain, body bounds and malformed JSON.

Go unit tests target algorithms and invariants; integration tests exercise real temp Git repos, processes, HTTP and SQLite. Run go test, go vet and bounded `nice -n 10 go test -race` on concurrency code with limited package/worker parallelism. Fault/restart and retained-history soak runs produce reproducible receipts and can span the requested six-hour work period without imposing sustained host-wide load.

## 13. Stage gates and handback

1. Commit capture and design, push origin/feat/go-rewrite.
2. Mail plans/coordinator with the commit and explicit retirement questions; set registry row 31 Notes to **design ready**.
3. Read row 31 about once/minute for ten minutes, then every ten minutes while doing only read-only profiling/prototyping. No implementation until ruling.
4. Implement in concern-sized conventional commits. Keep a continuously runnable private server and contract suite.
5. Run private Go on 7210, replay recorded read-only traffic, execute mutation/fault contracts only in fixtures, compare matched performance and complete a bounded soak.
6. Write RESULTS.md with exact pass/fail counts, environments, approved deltas and unresolved risks; write a cutover/rollback runbook without executing it.
7. Hand back the pushed commit and proof to plans/coordinator. No live cutover.

## References

- Exact source and wire receipts in CAPTURE.md and appendices.
- [Official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk), pinned release [v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0).
- [Native SQLite driver](https://github.com/mattn/go-sqlite3), pinned v1.14.52.
- [SQLite WAL and checkpoint behavior](https://sqlite.org/wal.html).
- [SQLite online backup API](https://sqlite.org/backup.html).
