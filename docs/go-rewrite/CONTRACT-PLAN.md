# Differential contract test plan

Status: design-stage specification. No Go implementation or private mutation traffic is authorized by this document itself; implementation and mutation lanes begin after the coordinator's stage-2 ruling. This plan preserves the user's existing authorization for stages 1–4 and does not add an approval-mode mechanism.

Reference: immutable TypeScript release/source `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`, CodexPro 0.31.0. Exact live/private schemas and initialize responses were captured on 31 October 2026 at 15:47:38 UTC and independently verified equal. See CAPTURE-CONTRACTS.md, capture-contracts/schema-verification.json, and capture-contracts/HTTP-PAYLOADS.md.

## 1. Acceptance contract

One fixture specification must drive the same public MCP calls and HTTP requests against the private TypeScript reference and the Go candidate. Each step records the actual response and checks independent semantic invariants, followed by a differential comparison. A matching error from two broken implementations is not a successful scenario.

The primary oracle is the raw HTTP MCP server response, not the surrounding ChatGPT connector's transformed error envelope. Compare text and structured outputs, omitted-versus-null fields, ordered results, argument validation, observable state changes, durability and process lifecycle. Successful schema comparison alone establishes neither behavior nor performance parity.

The source has 44 tool descriptors; the observed direct/standard deployment advertises 30. A checked-in surface manifest must identify retained tools/routes, conditional surfaces, and every coordinator-approved removal or intentional bug fix with its ruling/evidence. Never silently skip an unimplemented tool, change an expected result because Go fails it, or normalize away a disagreement. In particular, dropping work_* orchestration does not authorize dropping projects/workspaces/leases, job pinning, inbox, usage, or activity.

A retained feature needs passing happy-path, boundary and failure cases. A behavior intentionally changed to fix an observed TypeScript defect needs a separately named regression case and an approved exception to strict differential equality. The erroneous orphaning of healthy workspaces after transient probes is a likely such case; record the reference failure and the candidate's required behavior separately.

## 2. Test lanes, fixtures and isolation

| Lane | Purpose | Traffic and state |
|---|---|---|
| Schema | Exact advertised surface and initialize behavior | Live7200 read-only initialization/tools-list and private7211/Go7210. Captures already establish TS-to-TS equality. |
| Live observation/replay | Realistic read-only request shapes and load distribution | Only bounded read-only MCP calls and known nonmutating HTTP routes. Never create/write/edit/patch/import/commit/start/stop jobs, alter config or recover live leases. Replayed data comparisons require an immutable corresponding fixture snapshot. |
| Private sequential | Deterministic functional parity | Separate TS and Go fixture roots, catalogs, tokens and state. Same seed bytes and request sequence. Mutations confined to a named test-owned catalog project. |
| Private concurrent | Atomicity, quotas, lock scope, process cancellation | Bounded two-to-six clients/commands; seeded gates control ordering. Validate allowed histories and effects rather than assuming a particular goroutine wins. |
| Private recovery | Restart, crash boundaries, persisted cursors and state | Controller restarts only its own private processes. Checkpoints record exact PID/start identity, state root and expected recovery outcome. Never signal the live service. |
| Dependency/fault | Missing tool, transient Git/filesystem error, malformed copied state | Owned wrappers/temp fixtures or subprocess tests. Never alter host binaries, live catalogs or shared settings. |
| Load/profile | Correctness under representative bounded load and performance | Same disk class, fixed workload, concurrency, dataset and retention scale. Heavy runs under nice -n10; kept separate from correctness goldens. |

The private TS process now uses `/home/spite/.codexpro-go-proof/ts` and port7211; Go uses its own state/token on7210. Endpoint addresses and token-file paths are runtime inputs, not fixture constants. Resolve project/workspace IDs with list_projects. State for the two implementations must never point at one shared writable fixture root.

Before any mutation lane, the runner verifies loopback endpoint, expected implementation identity, private state provenance and the test-owned project root returned by the catalog. A live endpoint is rejected for a mutation scenario even if it accepts the credentials. Credentials stay in memory and never enter command arguments, fixture JSON, diagnostic output or saved HTTP headers.

Seed fixtures from byte manifests using an out-of-band controller, not the tool being tested. This avoids using a buggy write implementation to generate the input for another write test. Each case either starts fresh or explicitly declares its dependency on previous case state. Use deterministic file contents, directory ordering, Git branch/user configuration, symlink targets, binary fixtures and old persisted record versions. Preserve evidence before cleanup.

Keep mutation/setup utilities local to the private controller. Do not put an unrestricted shell escape into a data fixture interpreter. Shell commands under test are predeclared case content; substitutions support only declared bindings.

## 3. Fixture/spec format

A versioned JSON format should be readable without executing code. It has a case ID, lane, prerequisites, seed manifest, ordered steps, captures, invariants, comparison rules and cleanup ownership.

Example specification:

```json
{
  "schema_version": 1,
  "id": "FILE-08-unseen-edit-range",
  "lane": "private-sequential",
  "requires": ["read", "edit"],
  "fixture": "text-lines-v1",
  "project_id": "contract",
  "steps": [
    {
      "id": "projects",
      "mcp": {"name": "list_projects", "arguments": {}},
      "bind": {
        "workspace": {
          "from": "/result/structuredContent/projects",
          "select": {"id": "contract"},
          "field": "workspace_id"
        }
      }
    },
    {
      "id": "page",
      "mcp": {
        "name": "read",
        "arguments": {
          "workspace_id": {"ref": "workspace"},
          "path": "document.txt",
          "start_line": 1,
          "end_line": 2
        }
      },
      "expect": {"outcome": "tool-success"},
      "bind": {"tag": {"from": "/result/structuredContent/edit_tag"}}
    },
    {
      "id": "unseen",
      "mcp": {
        "name": "edit",
        "arguments": {
          "workspace_id": {"ref": "workspace"},
          "path": "document.txt",
          "edit_tag": {"ref": "tag"},
          "edits": [{"op": "replace", "start_line": 5, "content": "changed"}]
        }
      },
      "expect": {
        "outcome": "tool-error",
        "fields": {
          "/result/isError": true,
          "/result/structuredContent/error_code": "edit_range_unseen",
          "/result/structuredContent/retry_unchanged": false
        },
        "effects": [{"path": "document.txt", "bytes_equal_seed": true}]
      }
    }
  ],
  "comparison": {"profile": "strict-file-result-v1"},
  "cleanup": {"fixture_owned_only": true}
}
```

This is a proposed schema for the runner; it has not yet been executed. It deliberately distinguishes transport errors from MCP tool errors. JSON Pointer preserves array order and supports explicit presence/absence tests. An absent field is represented by a dedicated internal MISSING sentinel, never converted into null.

Useful step types:

- `mcp`: tools/call, with explicit client/session identity and typed arguments.
- `rpc`: initialize, ping, malformed JSON-RPC or notifications used to test transport behavior.
- `http`: method/path/query/body, expected status/headers/content type and optional JSON/body assertions.
- `observe`: read-only fixture filesystem/Git/process/state inspection by the controller.
- `barrier`: explicit release/acquire for bounded concurrency.
- `restart`: private controller action with a saved precondition and postcondition; cannot name arbitrary PIDs or units.
- `expect`: success/error category, field equality/type/range/presence, relation to bindings, and side effects.
- `bind`: a value captured separately for TS and Go and referred to symbolically in subsequent steps.
- `normalize`: a named, reviewed rule with explicit JSON Pointers/text locations and an independent validator.

Persist raw response bodies before normalization, with sanitized transport headers. Keep raw tool-level text, structuredContent, image blocks and error objects. Store original response SHA and byte size so later comparator changes cannot silently rewrite the evidence.

## 4. Normalization policy

Normalization is an explicit comparison transform, never a filter hiding unexpected fields.

| Value | Permitted handling | Required checks |
|---|---|---|
| Own fixture roots and workspace labels | Map only the two declared absolute roots and exact emitted workspace labels to one symbolic fixture root. | No unrelated path changes. Relative paths, filenames, escaping and redaction behavior remain exact. |
| Workspace/project lifecycle IDs | Bind separately from corresponding creation/catalog steps. | Shape, uniqueness, stability, correct project association and subsequent lookup. Configured project_id stays exact. |
| Job/action/run/operation IDs | Bind by causal request/position, not regex replacement of all ID-like strings. | No duplicate identities, correct ownership, cross-reference integrity, repeat/retry behavior. |
| Session IDs | Keep in transport memory; record presence and validated format instead of persisted secret-like headers. | Session reuse, mismatch, malformed/unknown IDs, close and principal ownership. |
| Wall-clock timestamps | Compare parseable values and causal bounds against runner request windows, preserving null/absence. | UTC/ISO formatting contract, monotonic revisions/events, started<=finished, correct retention/deadline relations. Inject a clock only in component tests that explicitly support it. |
| duration_ms, elapsed_ms, age/deadline values | Compare numeric bounds and relations; report distributions separately. | Nonnegative values, finished elapsed stability, deadlines decreasing while running. Do not replace timeout outcomes. |
| Git commit IDs affected by author/committer time | Bind returned commit IDs only after independently inspecting the object. Prefer fixed fixture metadata where possible. | Full hash shape, commit_short relation, expected tree/content/parents/branch, message and clean status. |
| Search/AST query_fingerprint | Treat as a binding with a declared workspace-identity dependency; verify deterministic repeat/changed-query behavior and the reference payload algorithm where feasible. | Same query same fingerprint within one implementation; different relevant options invalidate cursors. Do not drop it globally. Source includes workspace_id in the fingerprint. |
| Search/job cursors | Bind the issued opaque token and feed it unchanged to that implementation. | Correct continuation, no skips/duplicates, invalid/tampered/cross-job/cross-workspace/wrong-query rejection, completion semantics. If migration requires byte-compatible legacy cursor parsing, add direct legacy-cursor cases. |
| Activity sequence numbers | Prefer fresh equal seed state; otherwise compare a declared initial offset and increments. | Global order, cursor advancement, no accidental gaps/duplicates, filtering does not falsify coverage. Never sort chronological actions arbitrarily. |
| Cache ages/provider executable paths | Normalize only explicitly machine-specific fields with evidence. | Provider name/version, hit/miss transitions, bounded coverage and warnings remain checked. |
| HTTP Date and computed Content-Length | Validate own body length and timestamp; do not require equal wall time. | Status, content type, cache/security headers, content semantics and body-size limits stay exact. |

Never normalize away file SHA-256, edit tags for identical fixture bytes, patch content, diff stats, actual stdout/stderr, errors/recovery instructions, status/exit/signal, boolean outcomes, array order, counts, truncation markers, changed paths, schema constraints, omitted/null/empty distinctions, or unknown extra fields.

Text and structured content must both be compared. For text containing a dynamic timestamp/ID/path, substitute the exact bound literal at declared positions and assert that the surrounding complete text matches. Avoid broad regexes that could erase meaningful differences or mask accidental secrets.

For concurrent races, compare against a small declared set of legal histories and validate final state. Example: two writes with the same expected hash must not both silently overwrite one another; identify the successful history and require the losing request's documented conflict outcome. A race-specific allowed history does not loosen ordinary sequential goldens.

## 5. Required case matrix

Case families below are acceptance coverage, not a suggestion to launch every case concurrently. Small variants can share one fixture sequence when their failure effects remain independently observable.

### 5.1 Schema, result envelopes and routing

| ID | Scenario | Assertions |
|---|---|---|
| META-01 | Initialize both implementations and capture tools/list. | Exact retained names, order, titles/descriptions, schemas, annotations, execution.taskSupport, metadata and capabilities; approved removals listed separately. |
| META-02 | Each retained tool: smallest valid request plus unknown top-level key. | Match actual Zod stripping/passthrough behavior; strict nested objects still reject unknown keys. Bash's hidden background input remains separately tested. |
| META-03 | Missing required field, wrong scalar type, enum, min/max, excessive string/array. | Distinguish JSON-RPC validation from tool error; compare exact raw error shape/recovery, not connector-added INVALID_ARGUMENT. |
| META-04 | Unknown tool/project/workspace and conflicting open arguments. | Known-ID recovery metadata; no fallback to default project for an explicit invalid ID; no source side effect. |
| META-05 | Optional execution envelope under the approved surface. | Preserve accepted historical input or document approved removal. Unsupported managed lifecycle must not silently run a source mutation. |
| META-06 | Two projects, duplicate project_ids, reversed order and repeated open. | First distinct project selected; stable IDs; already_open/count/tree defaults and multi-open budgets match. |
| META-07 | Shared result wrapper, image exception and failure result. | codexpro_tool/title present as specified; content/structured/_meta/isError distinctions and redaction preserved. No invented outputSchema. |

### 5.2 Files, trees, instructions and skills

| ID | Scenario | Assertions |
|---|---|---|
| FILE-01 | Read empty, one-line, final-newline, LF, CRLF, CR, BOM and Unicode files. | Exact text/line numbers/line count/byte count/SHA/tag; preserve each actual reference convention. |
| FILE-02 | Reconstruct a several-page Unicode file with max_bytes1000. | No skipped/duplicate lines or split characters; stable SHA/tag; returned_bytes<=budget; final continuation null. |
| FILE-03 | Read starts after1 and reaches EOF; explicit end before EOF; out-of-range start/end. | truncated and has_more are distinct; preserve error/continuation and omitted/null fields. |
| FILE-04 | First line alone exceeds budget; source bigger than page limit; source scan ceiling. | read_line_too_large recovery, source-size versus returned-size distinction; no partial line. |
| FILE-05 | Binary/NUL, malformed UTF-8, missing file, directory and symlink target. | Actual decoder/guard/error behavior; no accidental filesystem traversal. |
| FILE-06 | Write new file, parent creation off/on, overwrite false, empty replacement and same content. | Exact bytes/hash/diff/existed, preserved mode as applicable; no parent created after rejected preflight. |
| FILE-07 | Correct/incorrect expected_sha256; file changes between check and replace. | Conflict leaves competitor's bytes intact; no silent lost update; cross-path writes can proceed independently. |
| FILE-08 | Read partial ranges, edit unseen line, then read it and retry. | edit_range_unseen with actionable recovery; cumulative displayed-range provenance; rejected call changes nothing. |
| FILE-09 | Several disjoint replace/delete/insert operations using original coordinates. | Correct final bytes/diff/stats; no sequential coordinate drift. |
| FILE-10 | Overlapping ranges, two insertions in one gap, insertion inside versus on range boundary. | All preflight failures before mutation; legal boundary insertion succeeds. |
| FILE-11 | Stale/unknown/malformed/lowercase tags; deliberately colliding four-hex hashes. | Full-text check prevents stale/collision edits; casing behavior follows schema and resolver; retry metadata matches. |
| FILE-12 | Read on sessionA, edit on sessionB with the same principal; different principal where auth permits a component test; restart the owned private server and retry a pre-restart tag before any new read. | Same-principal provenance is shared across MCP sessions but remains process-memory state; no cross-principal tag authorization. A fresh process rejects the old snapshot/tag until a new read establishes provenance. Compare actual reference error/recovery and ensure rejection does not write. |
| FILE-13 | Homogeneous CRLF/BOM edits, mixed newlines, trailing content newline. | Preserve line endings/BOM; reject mixed styles as reference does; exact final newline and stats. |
| FILE-14 | Tree depth/entry limits, hidden paths, blocked files, ordering, empty and unreadable directories. | entries/truncated and rendered tree match; no hidden-path leaks. |
| FILE-15 | Traversal, escaping/internal symlinks, path aliases and unusual Unicode/spaces filenames. | Guard consistency across read/write/edit/patch/search/image/import; canonical write-lock identity. Use only owned fake outside fixtures. |
| INST-01 | Global plus nested AGENTS variants/override precedence; absent files. | Exact precedence, order, scope metadata, agents_complete semantics and realpath deduplication. |
| INST-02 | Huge/invalid/unreadable instruction file; UTF-8 budget boundary. | Explicit incomplete metadata, no silent truncation or broken character; opening does not create context files. |
| SKILL-01 | Same-name workspace/user/plugin skills, explicit source/path, missing name and bounded content. | Resolution precedence and safe path overrides; byte/truncation metadata; no arbitrary-path access. |
| MAP-01 | inspect_workspace on representative TS/Python/Go fixture and narrowed area. | Languages/types/entrypoints/files/symbols/relationships/coverage/warnings/caps; no success-shaped empty substitute. |
| MAP-02 | Analysis after edit/add/delete and repeated query. | Invalidation and cache transitions preserve current results; explicit limits remain honest. |

### 5.3 Patch and Git

| ID | Scenario | Assertions |
|---|---|---|
| PATCH-01 | Raw unified diff with multiple files and nonadjacent hunks. | Correct bytes/paths/statistics and standard returned diff. |
| PATCH-02 | Native Begin Patch add/update/delete/move, including no-final-newline. | Equivalent effects and outputs; no unsupported native operation silently ignored. |
| PATCH-03 | Stale context, malformed patch and an invalid later file. | Whole-patch guarded preflight; no partial early-file effect before validation. |
| PATCH-04 | Path traversal, symlink destination, binary patch and blocked path. | Expected specific failure; owned source unchanged. |
| GIT-01 | Clean/staged/unstaged/untracked/rename/delete fixture. | Status parsing, changed paths and diff/stats; spaces, quotes, tabs and Unicode filenames survive. |
| GIT-02 | show_changes include_diff=false, staged/path scope and since=last_shown; repeat within one session, open a second session, and restart the owned private server. | Review checkpoints are scoped to one MCP tool context/session and keyed by workspace/path/staged. An unchanged diff can be suppressed only after that session marks it reviewed; a new session or process shows it again. No stale suppression after changed untracked content. Keep these ephemeral review maps distinct from durable managed-work checkpoints. |
| GIT-03 | show_changes in a nonrepository or with Git unavailable. | Preserve successful result with status_error/diff_error if reference does so; no false clean status. |
| GIT-04 | commit selected paths versus default all eligible changes. | Exact staged file set; blocked paths excluded; clean/dirty postcommit receipt, parent/tree/message validated. |
| GIT-05 | Nothing to commit, hook failure and invalid message/path. | Failure semantics; original state and index changes recorded accurately; no false commit receipt. |
| GIT-06 | Concurrent commit/status/read on separate and same workspaces. | Serial mutation where needed, bounded read interference, no global Git lock across unrelated projects. |
| PROJ-01 | create_project empty/init/clone, duplicate id/root and invalid parent/directory/ref. | Catalog persistence and immediate discovery; preflight cleanup on failure; retry does not clobber existing project. Clones use owned local fixtures. |

### 5.4 Text/config/structural search

| ID | Scenario | Assertions |
|---|---|---|
| SEARCH-01 | Literal versus regex, hidden/glob filtering, Unicode/newline context and grouped ranges. | Exact matches/context line numbers/order, used/scope/kind, warnings and edit tags. |
| SEARCH-02 | Multiple pages with unchanged options. | Concatenated results have no skips/duplicates; last next_cursor is null; stable fingerprint. |
| SEARCH-03 | Reuse cursor with different query/path/glob/context/scope/base_ref/workspace; corrupt cursor. | Actual search_cursor_invalid versus search_cursor_mismatch distinctions; recovery guidance and no broad retry. |
| SEARCH-04 | changed_files/diff_added/diff_removed against worktree/staged/head, untracked included/excluded. | Match the declared Git comparison, deletions' source contexts and editability; removed lines never authorize current-file edits. |
| SEARCH-05 | JSON/YAML/TOML config paths, pointer/wildcard/list selectors, malformed format/query. | Typed extraction, source positions, supported-format errors and no regex substitute. |
| SEARCH-06 | Structured symbol/references/impact intent and related tests. | Expected analysis shape/coverage/cache/warnings; first-page-only structured behavior preserved. |
| SEARCH-07 | Missing ripgrep/native provider, invalid regex, per-process timeout and bounded huge result. | Explicit fallback/provider/error behavior; no false empty success or orphaned search process. |
| AST-01 | Pattern and kind searches in TS/TSX/Go/Python; inferred/explicit language. | Actual ast-grep syntax results, captures/context/source offsets, provider/version. |
| AST-02 | selector/strictness/globs; both/neither pattern/kind; incompatible options. | Exact argument constraints and output modes. |
| AST-03 | Pagination, tampered/wrong-query cursor, timeout and provider unavailable. | AST-specific cursor codes, stable continuation and explicit failure; no string-search emulation. |
| AST-04 | Read/displayed structural context then tagged edit. | Only complete current-file context creates valid seen-range provenance. |

### 5.5 Batch

| ID | Scenario | Assertions |
|---|---|---|
| BATCH-01 | Parallel three-read workflow and serial read/edit/read workflow. | Child order/IDs/counts, exact success/error shape, workspace inheritance and aggregate bounds. |
| BATCH-02 | Distinct-file write/edit children followed by Bash verification. | Correct final files and successful verification; one saved definition by default. |
| BATCH-03 | Same-file aliases, patch plus another mutation, parallel mutation, continue_on_error mutation. | Rejected before any child effect; canonical path collision detected. |
| BATCH-04 | Invalid schema in later child, duplicate operation ID, unknown/unavailable child. | Up-front validation prevents all effects, not only the invalid child. |
| BATCH-05 | Child workspace_id/execution/session credentials; verification before final mutation. | Exact rejection; outer routing/session governs all children. |
| BATCH-06 | Failed verification after successful write and a later child. | Write remains, later child skipped, succeeded/failed/skipped counts and resumable_from correct. |
| BATCH-07 | Read-only continue_on_error versus default stop. | Failed/skipped outcomes distinguished and preserved in ordered results. |
| BATCH-08 | Inline persist=false/default/true; stored path/from/from_index; contradictory overrides. | Definition persistence/version/retention and resume boundaries; no replay of prior successful edits. |
| BATCH-09 | Verification timeout, hidden background and nonzero exit. | Timeout forced kill; no unfinished process counted as verified; Bash child failure propagates. |
| BATCH-10 | Large text/structured children and20-definition retention. | Bounded complete envelope, truncation flags/pruned paths/resume errors; no secret or oversized script leak. |
| BATCH-11 | Managed checkpoint if retained; approved dropped checkpoint behavior otherwise. | Atomic checkpoint only after all selected effects/verification succeed; failure preserves source changes with explicit recovery. |

### 5.6 Bash, processes, jobs, output and pinning

| ID | Scenario | Assertions |
|---|---|---|
| JOB-01 | Foreground stdout/stderr, exit0/nonzero, cwd and inherited allowlisted environment. | Durable job receipt, exact command/streams/status/exit/signal, compact text, environment contract. |
| JOB-02 | Foreground timeout promoted to job versus on_timeout=kill. | job_origin/job_status/timed_out, continuation or terminated process group, no duplicate shell execution. |
| JOB-03 | start_jobs with1/several commands, short completion and long running command. | Request ordering, labels, job_ids and running_count; immediate return does not omit finished output. |
| JOB-04 | Identical running commands, reordered input_job_ids, different cwd/input set. | Correct dedup key; distinct jobs remain distinct; capacity preflight does not half-start an array. |
| JOB-05 | Workspace/global job limits and foreground/background distinctions. | Correct capacity/error details; unrelated workspace availability within global budget. |
| JOB-06 | jobs list versus explicit IDs, include_finished, wait_for all/any and timeout. | Requested/waited values and all_finished/all_succeeded separate; no busy-wait CPU growth. |
| JOB-07 | Tail/head/legacy full_output/none modes and invalid option combinations. | Correct fields, returned byte counts and response cap; omitted stream fields remain omitted. |
| JOB-08 | Incremental stdout/stderr with multiple pages and new data after an empty page. | Monotone independent offsets, no skips/duplication, has_more versus output_complete, output wait semantics. |
| JOB-09 | Wrong job/workspace/start-time cursor, beyond-end offset, split UTF-8 and corrupted token. | job_cursor_invalid and unchanged retained output; no implicit workload rerun. |
| JOB-10 | Child emits UTF-8 characters and fake credential-shaped lines across chunks, CRLF, NUL/overlong records. | Decoder boundaries, no partial credential release, redacted rendered bytes, bounded omission markers. |
| JOB-11 | stop running/finished/unknown job; parent with child/grandchild and ignored TERM. | Only owned processes signaled; TERM→KILL escalation; final status/stop_reason, group quiescence and no survivors. |
| JOB-12 | Pin completed producer logs with a dependent job; retention pressure/expiry while pinned. | Logs stay available during dependency, lease releases on every terminal path, invalid cross-workspace inputs rejected. |
| JOB-13 | Pin storage quota and requested multiple input logs over budget. | Explicit job_storage_limit/capacity behavior; no partial consumer start or leaked pin after rejection. |
| JOB-14 | Restart private server with running, finished-unacknowledged and expired jobs. | No duplicate job/process; owned runner reconnect/recovery, final outputs and cursor availability correct; expired stays explicit. |
| JOB-15 | Concurrent output reads/retention/stop plus repeated completion collection. | No data races, torn metadata, invalid offsets or missing stop; completion acknowledgment behavior follows mode. |
| JOB-16 | Unrelated read tool receives background_jobs digest. | At most8, correct running/finished ordering and one-time finished acknowledgment; exempt tools remain exempt. |
| JOB-17 | Server drain while jobs wait for output/completion. | Wait interruption and server_restarting receipt without canceling unrelated work; reconnect with same job IDs/cursors. |

### 5.7 Workspaces, leases, recovery and any retained managed work

| ID | Scenario | Assertions |
|---|---|---|
| LEASE-01 | Create/reuse/open/release/remove isolated worktree if retained lifecycle is enabled. | Stable workspace/base commit/branch, correct Git registration, idempotency-key reuse and owner principal. |
| LEASE-02 | Catalog direct workspace versus managed/worktree workspace. | Direct project IDs never incorrectly inherit a managed lease state or disappear after unrelated reconciliation. |
| LEASE-03 | Two sessions same principal, different principal, expired/unknown workspace. | Ownership and readiness are explicit; compatible reconnect succeeds; foreign owner cannot mutate. |
| LEASE-04 | Transient Git timeout/CPU contention during reconcile, followed by recovery. | Healthy source remains usable; transient uncertainty is not durable orphan evidence. Record approved intentional fix versus TS defect. |
| LEASE-05 | Missing worktree path, stale Git registration, changed base/ref or actually deleted workspace. | Distinguish real loss from transient probe failure; no silent recreation/clobber or automatic source fallback. |
| LEASE-06 | Restart at create/provision/commit-to-store boundaries. | Retry resolves one durable workspace, no duplicate branch/path, recoverable partial state and explicit evidence. |
| LEASE-07 | Concurrent create/release/remove/use and idempotency-key races. | One legal serialized result per key; in-flight process/file ownership remains valid; no cross-project lock convoy. |
| LEASE-08 | Migrate read-only copies of legacy projects/leases/jobs/batches/audit state. | IDs and relationships preserved; migrations write only candidate copies; unsupported/corrupt versions fail explicitly. |
| LEASE-09 | Catalog reload/add and deleted/unknown project. | Existing leases and retained jobs retain identifiable owners; catalog change cannot silently retarget an ID. |
| WORK-01 | Conditional: work_status/manage/update, documents/revisions/operation keys/checkpoint/recovery/finish. | If retained, use existing source fixtures for every action and private state. If dropped, approved surface manifest and legacy-state archival/read policy are explicit; never report a fake successful lifecycle action. |

### 5.8 HTTP transport, authentication and limits

| ID | Scenario | Assertions |
|---|---|---|
| HTTP-01 | Valid/invalid/missing Bearer; casing/whitespace; valid query token; header/query precedence. | Exact status/body and auth policy, no token leakage; malformed header cannot silently select the wrong credential source. |
| HTTP-02 | Repeated invalid auth beyond10 in60s and recovery window. |429/Retry-After, bounded map state; run only private so no live client is rate-limited. |
| HTTP-03 | Initialize/initialized/tools-list/ping, negotiated versions, supported Accept forms. | SDK-compatible JSON/SSE framing and capabilities; notifications have correct no-result status. |
| HTTP-04 | Missing/malformed/unknown session, valid session reused, DELETE then reuse. |400/-32000 versus404/-32001; UUID binding and session cleanup; no leak of another session. |
| HTTP-05 | Principal mismatch and conditional Cloudflare/either mode if retained. |403/-32003 ownership; equivalent static principal across sessions; JWT claim/error behavior separately specified. |
| HTTP-06 | Malformed JSON, oversize MCP body, unknown method and JSON-RPC batch if supported. | Correct transport code/id/null/body; no process crash or tool side effect on malformed request. |
| HTTP-07 | Session TTL/capacity with active POST and open GET event stream. | Active requests not pruned; idle bounds enforced; no unbounded session accumulation. |
| HTTP-08 | Request cancellation/disconnect and process completion afterward. | Cancellation obeys each tool's ownership semantics; no duplicate effects, leaked locks or orphan jobs. |
| HTTP-09 | Graceful private drain during active call and new initialize. | Existing bounded work settles; new sessionless request503 with Retry-After1; no live signal or config change. |
| HTTP-10 | Every retained route: method/status/content type/security/cache headers and invalid parameters. | Explicit route matrix from CAPTURE-CONTRACTS; favicon auth exception; health remains authenticated. No missing route replaced by200 empty HTML. |
| HTTP-11 | Admin/inbox/usage mutation with absent Origin, same host, cross-host and malformed Origin. | Same-origin behavior and strict body limits; no cross-origin write; browser-only rule does not reject documented non-browser use. |
| HTTP-12 | Profile save GET/POST/unknown fields/rate limit if retained. | Candidate/private profile only, no immediate undocumented runtime reconfiguration; other methods405. |

### 5.9 Activity and audit

| ID | Scenario | Assertions |
|---|---|---|
| AUDIT-01 | Successful/failed read/write/execute/Git/lifecycle call. | Exactly attributable records with statuses/class/identity/timing; no payload/secret/raw-output in public actions. |
| AUDIT-02 | activity_list tail/forward cursor, filters and activity_get known/unknown. | Ordered records/cursors, correct selection, disabled-audit behavior; audit tools do not recursively create journal traffic. |
| AUDIT-03 | Export JSONL/JSON from identical seeded journals and from freshly generated actions; byte cap, first record too large, continuation. | For identical seeded journals, compare exact exported bytes/SHA/action_count and has_more/gap/retention metadata. For fresh actions with dynamic IDs/times, independently verify each raw export's UTF-8 byte count and SHA-256, then compare parsed actions using declared causal bindings; unequal dynamic payloads must not be required to have equal raw hashes. Preserve JSON/JSONL formatting, bounded response and complete records. |
| AUDIT-04 | Rotation/per-project retention/malformed retained record/copied gap state. | Durable sequence continuity, explicit coverage/gaps, retained cursor floors and no false complete history. |
| AUDIT-05 | /activity/live, /activity/data, /activity.json and per-project view. | Actual inflight and persistent-job facts; bounded text; route/filter distinctions and unavailable errors. |
| AUDIT-06 | Full command present in authenticated dashboard, absent from public activity tools. | Intentional data boundary preserved through batch child metadata and exports. |
| AUDIT-07 | Read activity repeatedly while a lease/job is idle. | No lease heartbeat or job completion acknowledgment from monitoring; no expensive full Git sweep on every lightweight poll. |
| AUDIT-08 | Concurrent appends/read/rotation and private restart. | No torn record/lost acknowledged sequence or race; queue/backpressure/failure policy observable and durable. |
| AUDIT-09 | Saved batch/job/diff viewers after expiry/missing project/invalid path. | Correct404/400, safe path resolution and truthful retention explanation. |

### 5.10 Usage, inbox and images/import

| ID | Scenario | Assertions |
|---|---|---|
| USAGE-01 | Empty snapshot; ingest selected/requested/null/unknown-historical project. | Exact schema/counters/project attribution/coverage; unknown historical projects retained, invalid current filter404. |
| USAGE-02 | Duplicate query+turn, upgrade evidence/project, conflicting timestamp/project and invalid future time. | Idempotent insert count, selected never downgraded, conflicts rollback whole batch. |
| USAGE-03 | Reset request replay/conflict/current revision/pending reset. | No deletion of history; exact original idempotent receipt and revision conflict. |
| USAGE-04 | Fresh/stale/future/older limits observation; weekly missing/invalid/boundary. | Clock-window validation and settings fields/revision/history; no invented future weekly date. |
| USAGE-05 | Pending reset completed with matching fresh observation versus old/wrong request. | Correct manual boundary and pending state, counters and history; duplicate requests stable. |
| USAGE-06 | Restart and concurrent ingest/reset/limits. | SQLite durable transactions, no lost updates, retained receipts and bounded history30. |
| INBOX-01 | Publish defaults/strict fields, replay same ID and changed content. | Normalized question equality, pending revision1, conflicting content409. |
| INBOX-02 | Answer valid/replay/new payload same request/old revision/missing question. | Revision increment and authenticated principal; old accepted request returns original receipt. |
| INBOX-03 | Delivery absent answer/old revision/replay/new consumer/consumer update/cap50. | Correct conflict/idempotency/replacement; no unbounded consumer list. |
| INBOX-04 | List pending/answered/project/consumer with paging. | Exact ordering, counts, next_offset and consumer delivery filter at current revision. |
| INBOX-05 | Events after sequence with project filter and page boundary. | Stable global ascending sequence, next_after and no skipped matching records. |
| INBOX-06 | Invalid identity/trimmed fields/body size/unknown project. | Strict schema and route errors, no partial stored question. |
| INBOX-07 | Concurrent same publish/answer/delivery plus restart. | Single idempotent outcome, transactional receipts/events and preserved durable state. |
| IMAGE-01 | PNG/JPEG/GIF/WebP fixtures, Unicode path and empty/corrupt/unsupported file. | Native MCP image block, MIME/dimensions/bytes/SHA and specific error behavior; compare image bytes exactly where unchanged. |
| IMAGE-02 | Image max_bytes boundary and blocked/escaping path. | Bounds enforced before returning oversized/base64 data; no path leak. |
| IMPORT-01 | Platform attachment object with expected SHA and MIME/name variants. | Exact bytes/hash/verified/MIME status/file identity and overwrite=false default. Use an approved fixture downloader seam or isolated allowed attachment fixture. |
| IMPORT-02 | Arbitrary URL, invalid scheme/host, redirect, oversized download, declared MIME conflict. | Existing import validation/network boundary, bounded streaming and cleanup; no general URL downloader introduced. |
| IMPORT-03 | Hash mismatch, destination exists, partial download/network failure. | Original destination preserved; no partial file or false verified receipt. |
| IMPORT-04 | Concurrent import/write to same canonical path. | Consistent lock/precondition outcome; no torn bytes or accidental overwrite. |

## 6. Toolkit interfaces that matter

Implement the harness around narrow interfaces so it can drive both servers without embedding either implementation.

1. **EndpointClient**: initialize and retain session; raw JSON-RPC and tools/call; SSE/JSON parsing; bounded timeout/cancellation; sanitized transport receipt. Do not let an SDK convenience method collapse structured error fields or coerce missing/null.
2. **BindingTable**: per-implementation capture/lookups with causal symbolic IDs. Validates identity shapes and cross-step relationships; cannot substitute arbitrary paths or shell fragments.
3. **FixtureController**: seed/read/checksum/stat/list/Git-object inspection of owned roots; process identity and liveness; private process start/stop/restart with recorded start identity. This is out-of-band evidence, not the source of the expected tool response.
4. **Comparator**: recursively exact by default, preserves JSON types/array order/MISSING, applies reviewed path-specific rules, compares text/content blocks, and emits a minimal path-level mismatch plus raw receipt pointers.
5. **InvariantEngine**: independently asserts filesystem bytes, branch/tree/parent state, one surviving process/job, transactional revision counts, cursor progress, secret absence and byte bounds.
6. **Scheduler**: ordered steps plus explicit barriers; bounded concurrency; collects every result. Failure cancels/cleans only owned work and records unfinished cases as incomplete.
7. **LifecycleDriver**: processes fresh/legacy state and restart fault points; accepts controller-created identities, never a free-form unit/PID from test data.
8. **EvidenceWriter**: versioned case manifest/source SHA, seed hashes, implementation commit/binary/dependency versions, effective config without secrets, raw response hashes, normalized diffs and effects, start/end times, pass/fail/incomplete/approved-exception.
9. **SurfaceManifest**: exact expected tool/route set plus approved conditional/dropped behavior and exception IDs. A missing scenario/capability fails coverage; no automatic skip from absent tool discovery.
10. **LoadDriver**: replay the same recorded read-only workload against comparable snapshots; reports correctness and latency separately by tool/status, percentiles, CPU/RSS, concurrent load, dataset sizes and timeout counts.

For Go's public layer, low-level MCP registration with the captured input schema is preferable to reflection-generated replacements. Preserve the actual taskSupport/annotations/_meta fields, no outputSchema, registration order, result envelope and raw validation behavior. Keep business/tool handlers independently callable for unit tests while the differential acceptance suite always exercises the HTTP boundary.

For stores/processes, expose clock, bounded subprocess runner, filesystem adapter and durable transaction interfaces where needed for deterministic failure tests. These are implementation seams, not new public API fields or approval policies.

## 7. Evidence, budgets and stop conditions

Each case result is one of PASS, FAIL, INCOMPLETE, or APPROVED-DIFFERENCE. A baseline failure is reported as BASELINE-FAIL and requires triage; it cannot become candidate PASS through equality alone. Unsupported fixtures are coverage gaps. Exhausted time/resources are incomplete work, never a skipped success.

Run schema and small sequential cases first; then the bounded concurrency/recovery cases resolving concrete races and durability risks. Use existing TS tests as scenario evidence (read-pagination, edit-batch, native-patch, search-upgrades, ast-grep, jobs/job-output/job-resources, worktree-manager, HTTP-auth, activity, inbox and pro-usage), not as proof that ported behavior already matches.

Run heavy benchmark/load/race work under nice -n10 and cap duration/concurrency. Avoid running the functional mutation suite during a benchmark sample; audit retention and fixture changes would contaminate the comparison. Capture current host pressure and process identities without changing host settings. The private baseline and Go sample must use the same storage class; the initial tmpfs TS profile is exploratory, not a fair comparison to ext4.

When a differential failure occurs, preserve the failing seed and raw responses, classify it as transport/schema/behavior/effect/normalization/reference-defect, and rerun only the failing bounded sequence after a concrete fix. Do not repeat the entire heavy suite to obtain a lucky pass. Race tests must cover the actual concurrent structures and subprocess/store interactions, not merely mirror pure helper implementations.

The stage-4 report must list coverage counts by retained feature, every remaining gap/approved difference, exact tested refs, schema comparison, semantic and durability results, and matched-workload performance. A cutover runbook can cite this evidence but stage5 remains outside this job.
