# CodexPro MCP and HTTP contract capture

Captured by SP-6 from the mandated source commit `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a` in `codexpro-go` on 31 October 2026. The original shared `codexpro` checkout was older; all contract files used here were reread from the mandated worktree. Source and actual wire-schema evidence, not a claim of passing runtime behavior equivalence.

## Evidence and completeness

- `src/tools/registry.ts:117–317` defines **44 tool descriptors**. The observed live `server_config` returns **30 registered tools** with standard tool mode, direct workspaces, full Bash, workspace writes, audit and work enabled, handoff and Codex-session surfaces disabled.
- `src/server.ts:21–44` fixes the server identity to CodexPro 0.31.0 and registration order. The edit-snapshot store is shared across MCP sessions; review checkpoints are per server/session.
- `capture-contracts/tool-input-source.json` records all 44 input-schema declarations, source hashes and registration locations, including inactive variants. These are source expressions; the runtime snapshots below are authoritative JSON Schema for the active surface.
- **Live and private HTTP schema captures completed and match exactly.** Parent job `job_660e98a6` captured both endpoints at 2026-10-31 15:47:38 UTC. `tools-list-live.json` and `tools-list-private.json` each contain 30 distinct tools in identical registration order, 113,081 bytes, SHA-256 `c2cdc3cbe64ce42e1dd7006cf3f6c7a9424ef07d831485219dbfa4ab376c7fab`. Both parsed bodies and all schema/annotation/metadata fields are identical; independent read hashes match their metadata receipts.
- `initialize-live.json` and `initialize-private.json` also match byte-for-byte: 673 bytes, SHA-256 `08cf86466357c0069c7717bbe5b8c53593c2cdac0a5ff15fed6623430e3ba9ca`. They negotiate protocol `2024-11-05`, identify CodexPro `0.31.0`, advertise tools/listChanged=true, and return identical instructions. No tool advertises outputSchema. Captures exclude credentials and session IDs. These are wire-schema parity checks between the live TS and isolated TS builds, not a Go correctness result.
- No live mutation, restart, lease recovery, profile write or service reconfiguration was performed by this capture subtask. The failed child Bash attempt was superseded by the successful parent capture; no further child Bash was used.
- Inputs described below are the observed standard/direct surface. Do not silently delete conditional variants until the coordinator approves the usage census.

## Shared wire contract

Every ordinary successful tool returns MCP `content` with a text block plus `structuredContent`; the wrapper injects `codexpro_tool` and `codexpro_title`. Most ordinary results also contain `_meta: {}`. Images additionally contain an MCP image block. Errors return `isError: true`, text, and structured `error`; `error_code`, `retry_unchanged`, `recovery` and error-specific details appear when provided by `CodexProError`. Do not turn tool failures into JSON-RPC transport failures. Source: `src/tools/shared.ts:118–124,193–215`, `src/tools/context.ts:76–104,294–408`.

Absolute-path redaction applies to both text and nested structured results when `exposeAbsolutePaths=false`; credential redaction applies to text and structured values. Four-character edit tags refer to raw retained source, while emitted read pages may be redacted. A result may append `background_jobs` (up to eight running/unacknowledged finished jobs) with job_id/status/command/elapsed_ms/exit_code/workspace_id. Bash/start_jobs/jobs/stop_jobs are exempt from this implicit digest; read/status calls can acknowledge finished digest entries. Source: `src/tools/context.ts:246–291`.

With work enabled, every non-`work_*` tool advertises an optional `execution: {operation_key?: string}` envelope (key length 1–160). Workspace routing is required for file/shell tools when a persistent catalog exists, even if it has only one project. Unknown ordinary object keys are stripped by the registered Zod validation; Bash separately preserves legacy hidden `background: boolean`. Some nested schemas are strict. Source: `src/tools/context.ts:51–73,435–490`, `src/tools/shared.ts:246–257`.

## The 30 observed standard tools

In this table, W is required `workspace_id: string`; E is the optional execution envelope above. Required inputs are bold. Schemas and bounds must be taken from the captured declarations or the exact tools/list fixture, not reconstructed from this compact table.

| Tool | Inputs beyond E | Structured result and behavior |
|---|---|---|
| list_projects | none | projects, count, creation_roots, creation_root_count, default_project_id, multi_open_supported, max_multi_open_projects. Direct-mode projects include stable workspace_id. |
| create_project | **project_id, parent_id, source**; label, directory, repository, initial_branch, base_ref, max_worktrees | project_id, parent_id, project, root, source, cloned, git_initialized, initial_commit_created, next_tool. ID lowercase pattern; direct-child directory only; source empty or git. |
| server_config | none | Redacted effective config, limits, mode flags, registeredTools and registeredToolCount; no auth token. Live job timeout and limits are reloadable and differ from stale connector description text. |
| activity_list | after_sequence, limit, mutating_only, tool_name, operation_class, status, project_id, workspace_id | enabled/mode/namespace/schema_version, actions, durable cursor and coverage metadata. Omitted cursor tails; supplied cursor consumes forward. Metadata-only public actions exclude command/query and payload bodies. |
| activity_get | **action_id** | enabled/mode/namespace/schema_version/action. Unknown ID fails. Disabled auditing gives action:null. |
| activity_status | none | retained/latest/next sequence, action_count, malformed_records, gap_detected, storage_bytes, retention metadata. |
| activity_export | **after_sequence**; list filters, format jsonl or json | Text is serialized actions; structured metadata includes export_format, export_bytes, export_sha256, action_count, next/earliest/latest sequences, has_more, truncated_by_bytes, malformed_records, gap_detected. |
| codexpro_self_test | **W**; write_probe, bash_probe, pro_context_probe, include_global_skills, max_skills | status, passed/warned/failed, duration_ms, expected/registered tool lists/counts, mode flags, files_touched, checks, terms_boundary. All mutation probes default off. |
| load_skill | **W, name**; source, path, include_global_skills, max_skills, max_bytes | skill descriptor, bytes, total_bytes, truncated, text. Name resolution over discovered skills; path is a sanitized exact override, not arbitrary file access. |
| open_workspace | project_id or project_ids; alternatively managed workspace_id; include_tree/max_depth/max_entries/include_skills/include_global_skills/bootstrap_context | Singular workspace_id/project_id/root/selection/skill/instruction/git fields plus workspaces:[entry],count:1; multi-open returns workspaces/workspace_ids/project_ids/count/already_open_count/selection and truncation metadata. First distinct project remains selected. bootstrap_context is ignored. |
| inspect_workspace | **W**; path, max_files, include_symbols, include_relationships, max_symbols, max_relationships | schema_version:1, root/path, languages, project_types, entrypoints, important_files, areas, files, symbols, relationships, coverage, warnings, output_limited, returned counts, cache. |
| tree | **W**; path, max_depth, include_hidden, max_entries | workspace_id/root/text/entries/truncated. Defaults path '.', depth4, hiddenfalse, entries800. |
| search | **W, query**; kind, regex, path, glob, hidden/results/context/group/cursor, scope, base_ref, diff_target, include_untracked, config_format, intent, symbol, include_tests | matches, contexts, truncated, has_more, next_cursor:null-or-string, query_fingerprint, used, scope, kind, warnings; optional analysis. Complete current-file contexts carry editable provenance and edit_tag. |
| ast_grep | **W**; exactly one of pattern/kind; language,selector,strictness,path,globs,hidden/results/context/group/cursor,timeout_ms | provider/provider_version/mode, matches,contexts,truncated,has_more,next_cursor,query_fingerprint,warnings. Actual syntax engine contract, not regex emulation. |
| read | **W, path**; start_line,end_line,max_bytes | path,text,start_line,end_line,total_lines,bytes,sha256,truncated,has_more,next_start_line,returned_bytes,edit_tag. Returned text has numbered lines; max_bytes is emitted page budget, not source-file-size limit. |
| view_image | **W, path**; max_bytes | path,mime_type,width/height nullable,bytes,sha256, plus MCP image data. PNG/JPEG/GIF/WebP supported. |
| write | **W,path,content**; create_dirs,overwrite,expected_sha256 | path,existed,bytes,sha256,additions,deletions,diff. Parent creation and overwrite default true; expected hash is a compare-before-write precondition. |
| edit | **W,path,edit_tag,edits** | path,mode:'tagged_lines',edits_applied,changed:true,bytes,base_edit_tag,edit_tag,additions,deletions,diff. 1–100 line edits use replace/delete/insert_before/insert_after against the original snapshot. |
| apply_patch | **W,patch** | paths,stdout,stderr,additions,deletions,changed,diff. Supports raw Git unified diff and native Begin Patch add/update/delete/move; guarded preflight. |
| import_file | **W,file,destination**; overwrite,expected_sha256 | path,bytes,declared_mime_type,detected_mime_type,mime_type_status,sha256,verified,file_id,file_name,overwritten. file object requires download_url/file_id, optional MIME/name. Overwrite defaults false. |
| bash | **W,command**; cwd,input_job_ids,session_id,timeout_ms,on_timeout; hidden background compatibility | command,cwd,exit_code,signal,duration_ms,stdout,stderr,truncated,timed_out,job_id,job_status,job_origin,bash_session_id plus output-retention metadata. Foreground also has a durable job record. Timeout backgrounds by default; kill terminates. |
| start_jobs | **W,commands**; session_id | jobs,job_ids,running_count. Each command object includes command, optional label/cwd/input_job_ids. Identical running background jobs deduplicate by command/cwd/sorted inputs. Capacity is checked before starting the array. |
| jobs | **W**; job_ids,output,cursor,max_bytes,wait_for,wait_ms,tail_bytes,full_output,include_finished | jobs,job_ids,count,wait_for,waited_ms,requested_wait_ms,all_finished,all_succeeded,running_count,returned_output_bytes,max_response_bytes; server_restarting when interrupted. See output/pinning details below. |
| stop_jobs | **W,job_ids** | jobs,stopped_ids,already_finished_ids. SIGTERM then SIGKILL after1.5s; already-finished jobs are returned without restarting work. |
| show_changes | **W**; path,staged,include_diff,since,mark_reviewed | path,status,status_error?,diff_error?,changed_files,staged,include_diff,additions,deletions,changed,diff,review_since,review_marked,review_checkpoint_hit; optional analysis. Default includes full current diff. last_shown suppresses unchanged previously reviewed diffs. |
| commit_changes | **W,message**; paths | commit,commit_short,working_tree_clean,status,status_error,branch,files,file_count,skipped_blocked_paths. Stages selected/all eligible changes; commits but does not push. |
| batch | **W**, exactly one of operations/path; from/from_index,mode,continue_on_error,persist,session_id,checkpoint | Persist/resume metadata, counts, failed_operation_id/index/resumable_from, succeeded, changed_paths, results, output/child truncation, optional checkpoint. See validation details below. |
| work_status | action,run_id/project_id,section,claimed,needs_attention,state,document_id/revision,operation_id,query,offset,limit,max_bytes,after_sequence | Bounded run packet/summary/page or document/receipt/history/search. return_size reports original/returned/truncated; essential identifiers and revisions survive compaction. |
| work_manage | **action,request_key**; run_id,expected_revision,project_id,mode,title,objective,scope,acceptance,todos,base_ref,ready,initial_handoff,reason,evidence_ids,max_attempts,active_ms,reset_no_progress | Run lifecycle result/packet. Create requires project_id/mode/title/objective/scope; other actions require run_id/current revision; pause/cancel/recover/revise_limits require reason. Deprecated active_ms/max_attempts are ignored and reported. |
| work_update | **action,run_id,request_key,expected_revision**; checkpoint/plan/document/operation/finish fields | Atomic plan/handoff/document updates and receipts. Per-action required fields and revision/operation semantics are substantive, not optional storage helpers. |

Source map: projects `src/tools/projects.ts:9–117`; admin/activity/skills `src/tools/admin.ts:113–705`; workspaces/analysis `src/tools/workspaces.ts:159–507`; files `src/tools/files.ts:25–524`; shell/jobs `src/tools/bash.ts:92–312`; Git `src/tools/git.ts:55–213`; batch `src/tools/batch.ts:78–619`; managed work `src/tools/work.ts:14–113`, `src/tools/workSchemas.ts:3–23`.

## Fourteen additional conditional tool names

All exist in the target source but are absent from the observed standard/direct tools/list configuration. Absence here is configuration evidence only, not proof that no consumer exists.

| Tool(s) | Condition and contract source |
|---|---|
| codexpro | Full-tier supertool dispatch, aliases and list_actions; `src/tools/admin.ts:32–111`, `registry.ts:13–33`. |
| codexpro_inventory | Full-tier skill/MCP inventory; W plus include_global_skills/include_mcp_servers/max_skills; `admin.ts:622–656`. |
| create_workspace | MCP-worktree mode; project_id/base_ref/label/idempotency_key and instruction/tree options; creates or reuses isolated workspace. `workspaces.ts:24–89`. |
| list_workspaces | Full-tier direct mode; no ordinary input; returns workspaces/count/selected_workspace_id. `workspaces.ts:92–113`. |
| open_current_workspace | Direct, fixed single-project config; instruction/tree options, returns selected default workspace. `workspaces.ts:116–156`. |
| release_workspace, remove_workspace | MCP-worktree mode, required workspace_id; lifecycle outcomes. `workspaces.ts:386–425`. |
| read_handoff, wait_for_handoff, codex_context, export_pro_context, handoff_to_agent | Handoff-mode gated tools; work-context readers/export and agent handoff. `src/tools/handoff.ts`. |
| codex_sessions, read_codex_session | Opt-in Codex-session metadata/read modes; `src/tools/handoff.ts`. |

## Job output and pinning invariants

`input_job_ids` on Bash and each start_jobs child pins that workspace's retained logs for the dependent command's lifetime. It is not a new standalone MCP tool. Preserve validation, quota accounting and release on every success/failure/cancellation path.

Output metadata includes captured stdout/stderr bytes; available rendered stdout/stderr bytes; output_available/output_growing/output_expires_at; retention_note; and, in full Bash mode, logical output_files under `$CODEXPRO_JOB_OUTPUT_DIR/<job_id>/{stdout,stderr}.log`. File paths are intentionally supplied for bounded subsequent Bash analysis.

Incremental output requires exactly one job, rejects wait_for/tail_bytes/full_output and returns stdout/stderr/returned_bytes/next_cursor/has_more/output_complete/output_mode. The cursor is base64url JSON v1 containing identity `workspace_id:job_id:started_at` and two byte offsets. Validate workspace/job identity, bounds and UTF-8 character boundaries. Output pages default16,384 bytes, max24,576; total response max196,608. Output may expire even after successful process completion. A cursor never authorizes rerunning a workload.

Tail/head output defaults differ from incremental output; legacy full_output is only a bounded head, not a complete-log guarantee. Completion collection defaults wait30s,max300s; incremental wait defaults0. Metadata/incremental reads do not acknowledge completion. A running job's all_finished=false is distinct from all_succeeded; a failed finished job yields all_finished=true and all_succeeded=false. Source: `src/tools/bash.ts:21–84,210–312`, `src/jobOutput.ts:11–19,108–166`.

Rendered logs accumulate complete UTF-8 records, redact credentials before release, suppress binary/replacement/overlong records, and have a bounded aggregate output budget. Do not expose a partial secret while a chunked line is incomplete. `src/jobOutput.ts:21–61`.

## Batch invariants

Up to12 operations. Allowed children: tree/search/ast_grep/read/inspect_workspace/show_changes/write/edit/apply_patch/bash. Child workspace IDs and credential/execution/session fields are forbidden: scope and session come from the outer request. Validate every selected definition and child schema before beginning effects. Operations have unique IDs; omitted IDs become op_1 etc.

Mutating/Bash batches must be serial and cannot continue_on_error. write/edit children must target different canonical files; a patch is the sole file-mutation child. All verification Bash follows all file writes; verification rejects hidden background and forces on_timeout=kill. Parallel children must be read-only safe. These are current behavioral constraints; removing them because full Bash is enabled would break the batch contract.

Persist defaults true with Bash verification and false otherwise. Stored definitions fix mode/continue_on_error; caller cannot override either or combine path with persist. Resume accepts from or from_index on stored batches only. Persisted retention is20 definitions. Failure skips remaining serial children and exposes resumable_from. Successful file effects survive later verification/checkpoint failure. A checkpoint is saved only after every selected child succeeds and finishes; failure must never invite replay of successful edits.

Source: `src/tools/batch.ts:39–105,109–340,467–619`, `src/batchStore.ts`.

## HTTP route inventory

All routes except favicon run after authentication and default no-store/security headers. This includes healthz. Bearer authentication is constant-time for equal-length candidates; Authorization takes precedence over query token, and header parsing accepts a single string/single-element array. Compatibility query names are codexpro_token and token. Static principal deliberately omits AuthInfo; changing its derivation would invalidate existing lease ownership. Cloudflare JWT/either mode exists but live configuration reports static-token only; census/review decides its fate.

| Method | Route | Response / parameters |
|---|---|---|
| GET | /favicon.ico | SVG, public cache86400s; before auth. |
| GET | /, /setup | Onboarding/configuration HTML. |
| GET | /healthz | Redacted health/config JSON including auth and worktree flags. |
| POST | /mcp | JSON-RPC initialize/tools operations;20MB JSON limit; Streamable HTTP response. |
| GET, DELETE | /mcp | Existing session event stream / close through SDK. |
| GET | /activity | Full HTML dashboard; before_sequence and project_id filters. |
| GET | /activity/data | Dashboard JSON snapshot; same filters. Added in target, absent from older shared checkout. |
| GET | /activity/live | Lightweight HTML fragment, no-store;500 plain text on failure. |
| GET | /activity/job | HTML fragment by job_id/workspace_id;404 if unavailable/expired/inaccessible. |
| GET | /activity/diff | HTML project diff; project_id required/known;404 unknown. |
| GET | /activity/batch | Saved batch HTML/full or fragment=1; project_id/path and worktree workspace_id as applicable;400 invalid,404 missing/pruned. |
| GET | /activity.json, /activity/projects/:projectId.json | Compact operational JSON; project_id/run_id and limit1–10,output_bytes0–4096,quiet_after_ms30000–86400000. 409 ambiguous_run;404 unknown_project/run;400 invalid options/ID;500 unavailable. |
| GET | /admin/profile | Saved/effective profile JSON. |
| POST | /admin/profile |32KB strict profile patch, same-origin host check, rate/body limit; saved settings take effect only on restart. Other methods405. |
| GET | /inbox/v1/items | schema_version1 plus items page; optional project_id/status pending-or-answered/consumer;limit1–100 default100,offset0–1,000,000 default0. |
| GET | /inbox/v1/events | schema_version1/events/next_after; optional project_id,after,limit1–100. |
| GET | /inbox/v1/items/:project/:id | Item JSON or404. |
| POST | /inbox/v1/items | Publish payload,32KB; same-origin; store result. |
| POST | /inbox/v1/items/:project/:id/answers | Answer payload with authenticated principal,32KB. |
| POST | /inbox/v1/items/:project/:id/deliveries | Delivery acknowledgement payload,32KB. |
| GET | /activity/inbox | Inbox dashboard HTML. |
| GET | /usage/v1/pro | Snapshot, optional current project_id. |
| GET | /usage/v1/pro/history | schema_version1/changes. |
| POST | /usage/v1/pro/submissions | Ingest,128KB; historical unknown project IDs remain valid. |
| POST | /usage/v1/pro/reset | schema_version1/settings; principal recorded. |
| POST | /usage/v1/pro/limits | schema_version1/settings. |

Source: `src/http.ts:1550–2260`, `src/httpAuth.ts:27–47,96–157`, `src/inbox/routes.ts:8–49`, `src/usage/routes.ts:7–27`.

HTTP auth failure uses401 "Unauthorized"; more than10 failures per address per60s yields429 with Retry-After. Dashboard HTML reauthentication has a specific401 HTML response, using the browser's previously retained credential without exposing it in the captured public API. Mutation endpoints reject browser Origin hosts differing from Host; non-browser requests without Origin are accepted.

MCP sessions are UUIDs. Missing/malformed session gives400 JSON-RPC code-32000 with id:null; unknown well-formed gives404/-32001; another principal gives403/-32003. Initialize without session creates a server/transport; non-initialize without session fails. Per-session TTL pruning avoids active requests; capacity prunes oldest idle transports. Malformed MCP JSON gives400/-32700;oversize413/-32000. Handler exceptions become500/-32603. Graceful drain refuses new sessionless calls with503,Retry-After1,Connectionclose; existing calls may complete and job waits are interrupted. These states need explicit HTTP integration tests, not only successful tools/call tests.

## Contract-suite outline and acceptance limits

1. Store exact tools/list plus initialization/server capabilities from the live service and the immutable private TS build. Compare tool names, required inputs, enums, numeric/string bounds, defaults, annotations and attachment metadata. Preserve list order or document/prove client-independence before accepting a change.
2. Use a shared call-sequence fixture format, with each implementation resolving its own project/workspace IDs. Call order matters for edit tags, reviews, jobs, cursors and stored batches. Bind IDs/timestamps/cursors symbolically instead of hard-coding or indiscriminately dropping fields.
3. Compare stable output keys/types and exact semantic content, including omitted versus null, text rendering, error codes/recovery, path labels, UTF-8 byte counts, and changed-file outcomes. Normalize only explicit dynamic fields (UUIDs, elapsed time, private fixture roots, process times) with per-field rules.
4. Live replay is read-only: list_projects/server_config/read/tree/search/ast_grep/show_changes with mark_reviewed=false, bounded activity and jobs metadata. Never use a live mutation for a golden.
5. Private mutation sequences cover Unicode/CRLF/empty files/read pagination; expected-SHA conflicts; stale/colliding/unseen edit tags; overlapping hunks; patch add/update/delete/move and preflight atomicity; binary/images/import content; Git unusual filenames/status/staging/commit; project/workspace/retry/lease lifecycle.
6. Process sequences cover foreground success/failure, promotion, timeout kill, deduplication/capacity, both streams, incremental cursor boundaries, split UTF-8/redaction records, cancellation of process groups, pinning and retention, restart recovery, and deliberate unknown IDs. No assertion equates completion with success.
7. Batch sequences cover successful parallel reads/serial mutations, distinct-file enforcement, up-front validation, persistence/resume after failed verification, child failure and skip counts, output bounds, and checkpoint gating if managed orchestration remains approved.
8. HTTP sequences cover auth precedence/rejection/rate limit, initialize/session lifecycle/TTL/cancellation/drain, malformed JSON and body limits, all retained routes' successful and invalid parameter forms, inbox idempotency/answer/delivery conflict, and usage deduplication/reset/limits.
9. Generated or handwritten schema-only tests are insufficient: side-by-side fixtures must verify actual filesystem/process/state effects and read-after-restart behavior. All destructive cases stay within isolated fixture roots and token/state directories.
10. Remaining capture gaps: exhaustive private runtime golden responses and library-specific MCP wire behavior beyond initialization/tools-list have not yet been verified by this fragment. The exact live/private schema and initialization snapshots are complete and equal. Parent capture/profile evidence supplies usage census, performance baselines and drop decisions.
