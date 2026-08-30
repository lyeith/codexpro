# CodexPro Action Journal

> **Debug/diagnostic boundary:** this stream records engineering tool activity. It is not a day-to-day operations feed. Downstream systems should project it into an explicit debug namespace while preserving source-owned `action_id` and `codexpro://actions/...` references.

CodexPro exposes a durable, metadata-only public stream of direct connector actions. The authenticated local dashboard additionally reads bounded private Bash scripts from the same local journal; those private fields are stripped from every public activity tool and export. The public stream is intended for observability, operational digests, and downstream consumers such as Ops Inbox without requiring a consumer to scrape ChatGPT history or infer actions from Git.

The public record contract is:

```text
codexpro.action.v1
```

Auditing is disabled by default.

## Enable it

```bash
codexpro start --audit metadata
```

Equivalent environment variables:

```text
CODEXPRO_AUDIT_MODE=metadata
CODEXPRO_AUDIT_LOG=~/.codexpro/audit/tool-calls.jsonl
CODEXPRO_AUDIT_MAX_BYTES=8388608
CODEXPRO_AUDIT_RETAIN_ACTIONS=200
```

The CLI equivalents are:

```text
--audit metadata
--audit-log <path>
--audit-max-bytes <bytes>
--audit-retain-actions <count>
```

`CODEXPRO_AUDIT_MODE=off` is the default.

## Read surfaces

The journal is consumed through read-only MCP tools. Consumers do not need filesystem access to the JSONL file.

### Authenticated `/activity` page

The CodexPro HTTP server exposes a small read-only dashboard at:

```text
/activity
```

It groups the latest eight retained actions by configured project. Each action is an expandable card with a human-readable tool-specific summary plus its safe request metadata, result metadata, changed paths, before/after file evidence, and before/after Git evidence when available. For example, a tagged edit can show its path, exact per-hunk `+ / −` line counts, operation count, tag-precondition presence, and file-size transition; a batch can show inline-versus-file source, retained `batch_path`, start/failure operation, selected-versus-total operation counts, file-mutation and verification-command counts, and retention/truncation state. When a current `batch_path` is available, the action card links to an authenticated batch viewer that reads the saved JSON on demand and shows its operation IDs, tools, arguments, and raw definition. Because stored batches remain editable, the viewer explicitly presents the current definition rather than claiming it is an immutable copy of the historical invocation.

Search action cards expose only safe operational shape. Text/config search records mode, workspace/changed/diff scope, context sizes, cursor presence, result/context/editable counts, continuation state, and engine. Structural `ast_grep` records pattern byte count/digest or bounded kind, language/selector/strictness, glob count, provider/version, mode, and the same bounded result/continuation counts. Query or pattern text, cursor text, returned source, captures, configuration values, and edit tags are not copied into the journal.

For Bash actions recorded after exact-command capture was introduced, the page renders the submitted script verbatim after HTML escaping. This applies both to direct `bash` actions and to Bash children that actually ran inside inline, stored, or resumed batches; batch scripts are labelled with their operation IDs. Script text is deliberately not secret-redacted and is bounded to at most 64 KiB in aggregate per action, with a smaller retained prefix when JSON escaping or surrounding metadata would otherwise exceed the 128 KiB action-record ceiling. Older direct Bash records show an explicit unavailable note.

For each configured project checkout, tracked Git changes are rendered as per-file side-by-side before/after hunks. Old and new line-number columns share aligned rows inside one scroll surface, so vertical and horizontal movement remains matched like a split Git viewer.

The dashboard uses a deliberately broader local boundary than the public activity stream:

- it is protected by the server's existing HTTP authentication;
- exact Bash text is stored under private `dashboard_metadata.shell_scripts` and shown only on `/activity`;
- `activity_list`, `activity_get`, and `activity_export` strip `dashboard_metadata` before returning records;
- file replacement bodies, stdout/stderr, prompts, search queries, ast-grep patterns/captures, and raw tool results are not embedded in action cards or public records; the separate saved-batch viewer deliberately shows the current exact batch arguments after authentication;
- safety-blocked paths such as `.env`, keys, and internal audit files are excluded from path lists and diffs;
- untracked file names may be listed, but their contents are not rendered;
- changed paths, script text, and diff output are bounded;
- a saved-batch link reports a clear missing/pruned state when its file has aged out under the 20-file workspace retention policy;
- Git state remains available when auditing is off, while the activity cards require `--audit metadata`.

The page refreshes every 15 seconds while no panel is open. It is an operator view, not another source stream, and reading it does not append audit records.

### `activity_list`

Returns structured `codexpro.action.v1` objects.

```text
activity_list(limit=100)
activity_list(after_sequence=1200, limit=100)
activity_list(after_sequence=1200, limit=100, mutating_only=true)
```

With no `after_sequence`, the tool tails the most recent matching records. With `after_sequence`, it scans forward after the acknowledged source sequence.

The response includes:

- `actions`
- `next_sequence`
- `earliest_sequence`
- `latest_sequence`
- `has_more`
- `malformed_records`
- `gap_detected`

The default limit is 100 and the hard maximum is 500.

### `activity_get`

Returns one retained action by its source-owned ID:

```text
activity_get(action_id="cpa_0123456789abcdef0123456789abcdef")
```

An action that has aged out under retention is no longer available through `activity_get`.

### `activity_status`

Returns the source boundary and health state:

```text
activity_status()
```

Important fields are:

- `retained_from_sequence`
- `latest_sequence`
- `next_sequence`
- `action_count`
- `gap_detected`
- `malformed_records`
- `storage_bytes`
- `retention.retain_actions_per_project`
- `retention.rotation_count`
- `retention.dropped_through_sequence`
- `retention.cursor_floor_sequence`
- `retention.planned_gap_count`
- `retention.compacted_at`

### `activity_export`

Exports a bounded consumer page as JSONL or JSON:

```text
activity_export(after_sequence=1200, limit=100, format="jsonl")
```

The text body is the export payload. Structured metadata includes:

- `export_format`
- `export_bytes`
- `export_sha256`
- `action_count`
- `next_sequence`
- `has_more`
- `truncated_by_bytes`
- source boundary and health fields

The export is additionally bounded by CodexPro's output limit. If a requested page exceeds the export byte budget, CodexPro returns the largest complete prefix and sets `has_more=true`. Individual action records are never split.

The activity tools do not journal their own reads, so polling does not create an observability feedback loop.

## Record shape

A record is one JSON object. Representative shape:

```json
{
  "schema_version": "1.0",
  "sequence": 412,
  "action_id": "cpa_0123456789abcdef0123456789abcdef",
  "occurred_at": "2026-08-28T02:00:00.000Z",
  "finished_at": "2026-08-28T02:00:00.125Z",
  "project_id": "codexpro",
  "workspace_id": "ws_example",
  "tool_name": "edit",
  "operation": "file.edit",
  "operation_class": "write",
  "mutating": true,
  "invocation_surface": "direct",
  "actor_ref": "actor_0123456789abcdef0123456789abcdef",
  "request_ref": "request_0123456789abcdef0123456789abcdef",
  "transport_session_ref": "transport_0123456789abcdef0123456789abcdef",
  "server_session_ref": "srv_0123456789abcdef0123456789abcdef",
  "request_fingerprint": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "status": "succeeded",
  "duration_ms": 125,
  "targets": ["src/example.ts"],
  "changed_paths": ["src/example.ts"],
  "changed_path_count": 1,
  "changed_paths_truncated": false,
  "request_metadata": {
    "path": "src/example.ts",
    "edit_mode": "tagged_lines",
    "edit_tag_supplied": true,
    "edit_operations": 3,
    "edit_content_bytes": 86
  },
  "result_metadata": {
    "changed": true,
    "mode": "tagged_lines",
    "edits_applied": 3,
    "bytes": 418
  },
  "result_ref": "codexpro://actions/cpa_0123456789abcdef0123456789abcdef",
  "summary": "edit succeeded; 1 changed path"
}
```

Fields that do not apply to an action are omitted.

The JSON shown above is the public shape. On disk, a direct Bash or batch action may additionally contain private dashboard-only data such as:

```json
"dashboard_metadata": {
  "shell_scripts": [
    {
      "operation_id": "verify",
      "script": "npm test"
    }
  ]
}
```

`operation_id` is present for batch children and omitted for a direct Bash action. A script may carry `truncated: true` when the 64 KiB script cap or the 128 KiB complete-record cap shortened it. CodexPro removes the complete `dashboard_metadata` object from public list, get, and export responses.

For `open_workspace(project_ids=[...])`, the record is attributed to the first selected project/workspace. Request metadata retains only `project_ids_count`; result metadata retains `workspaces_count` and bounded truncation indicators. The project-id array and absolute roots are not copied into the journal.

For actionable tool failures, `error_code` uses a stable bounded category such as `edit_tag_stale`, `edit_range_unseen`, `patch_format_invalid`, or `patch_context_stale`. Result metadata may additionally retain `retry_unchanged=false` and the recommended `recovery_tool`; recovery prose, file bodies, patch contents, and command text are excluded from the public record. Batch metadata may retain `persist`, persistence defaults/selection, and bounded efficiency guidance without retaining the stored definition or non-Bash child payloads. The sole private payload exception is bounded exact text for direct Bash and executed Bash children, stored in `dashboard_metadata` for the authenticated activity page.

## Identity and ordering

### Action ID

`action_id` is generated by CodexPro and is stable for the retained record. Consumers should use it as their idempotency key.

### Source sequence

`sequence` is monotonically increasing within the journal. Retention never renumbers surviving actions.

A consumer cursor is the last source sequence it has durably ingested. To resume, call:

```text
activity_list(after_sequence=<last acknowledged sequence>)
```

or:

```text
activity_export(after_sequence=<last acknowledged sequence>)
```

Only advance the consumer cursor after the corresponding actions have been committed downstream.

### Request deduplication

When the MCP request has an identity, CodexPro derives a metadata-only request fingerprint from the opaque actor, transport or server session, request reference, and effective tool name. A repeated request within the retained journal returns the original action identity instead of appending a duplicate record.

Request IDs reused by unrelated transports are not treated as duplicates.

## Effective tool attribution

Actions invoked directly are recorded with:

```text
invocation_surface = direct
```

Actions routed through the `codexpro` supertool are attributed to the effective child action, for example `read` or `write`, and carry:

```text
invocation_surface = codexpro
```

The stream therefore describes what CodexPro actually did rather than merely recording the outer dispatcher name.

## Operation classes

`operation_class` is one of:

```text
read
write
execute
git
lifecycle
analysis
handoff
administrative
```

`operation` is a more specific stable label such as:

```text
file.read
file.write
file.edit
file.patch
command.run
git.status
workspace.open
workspace.create
project.create
handoff.write
```

Unknown future tools still receive a safe generic read or mutation classification.

## Outcomes

`status` is one of:

```text
succeeded
failed
timed_out
cancelled
blocked
```

Examples:

- A returned tool error is `failed`.
- A non-zero shell exit is `failed`, with an error code such as `command_exit_3`.
- A command timeout is `timed_out`.
- A request aborted by its cancellation signal is `cancelled`.
- A policy or permission refusal is `blocked`.

`duration_ms` covers the full tool invocation at the central dispatch boundary.

## Mutation evidence

For mutating actions, CodexPro captures bounded evidence before and after the operation while the workspace operation is serialized.

### Path evidence

`path_evidence_before` and `path_evidence_after` may include:

- relative path
- existence
- file kind
- byte size
- modification time

They never include file contents.

### Git evidence

For a Git workspace, `git_before` and `git_after` may include:

- HEAD commit
- branch
- dirty flag
- bounded changed path list and count
- a status fingerprint

They never include a Git diff or blob contents.

`changed_paths` is derived from explicit tool results and differences between the before/after evidence. Lists are bounded; the complete count and a truncation flag remain available.

## Privacy boundary

The public action contract is deliberately not a transcript.

Public activity tools and exports never expose:

- file bodies or replacement text
- prompts, plans, or handoff prose
- search query or ast-grep pattern text, continuation cursor text, returned context/source, configuration values, captures, or search-result edit tags
- shell command text or private `dashboard_metadata`
- bearer tokens, API keys, or attachment bytes
- stdout or stderr bodies
- raw tool results
- raw principal, request, or transport-session IDs
- absolute workspace roots

Where useful, the public contract records only safe metadata such as byte counts, booleans, bounded relative paths, command executable names, and SHA-256 digests.

The local on-disk journal has one deliberate private exception: a direct Bash action, or a batch action with Bash children that actually ran, stores exact script text under `dashboard_metadata.shell_scripts`. The scripts are bounded to at most 64 KiB in aggregate per action and shrink further when needed to keep the complete serialized record within 128 KiB. The whole private object is removed from `activity_list`, `activity_get`, and `activity_export`. Skipped batch children are not captured.

Because a submitted command can itself contain credentials or other sensitive values, treat the local audit file as sensitive even though CodexPro creates it with mode `0600` on POSIX systems. The `/activity` page HTML-escapes script text to prevent markup execution, but intentionally does not secret-redact it.

Opaque references are one-way hashes or random source identifiers. They support correlation without exposing the original identity value.

## Storage and permissions

The active journal is JSONL. By default it is stored at:

```text
~/.codexpro/audit/tool-calls.jsonl
```

CodexPro creates the journal directory with local-user permissions and the files with mode `0600` on POSIX systems.

The configured journal and its internal files are automatically blocked from CodexPro workspace file tools when they fall under an allowed root:

- active JSONL journal
- lock file
- retention index
- compaction temporary files
- replacement backup files

This prevents a broad allowed workspace from reading or rewriting its own observability source through the connector.

## Retention and cursor expiry

Compaction occurs when either limit is exceeded:

- `CODEXPRO_AUDIT_MAX_BYTES` (8 MiB by default)
- `CODEXPRO_AUDIT_RETAIN_ACTIONS` (200 actions per project by default)

The HTTP server enforces both limits at startup, so upgrading from a larger historical default immediately compacts an oversized journal before the dashboard begins serving. Later appends enforce the same limits. CodexPro retains the newest 200 complete records independently for each `project_id`; records without a project ID share a separate unscoped bucket. This prevents a busy project from evicting the useful history of another active project. The global byte ceiling is still authoritative, so unusually large combined histories may retain fewer than 200 records in some buckets. Byte-driven selection takes the newest record from each active bucket before taking older rounds, subject to the available space.

Compaction preserves original sequence numbers. Per-project selection can therefore create intentional internal sequence gaps. CodexPro records a digest of the retained compaction generation so those planned gaps validate cleanly while unexpected truncation or alteration still reports corruption. `retention.planned_gap_count` reports the number of intentional holes. `retention.dropped_through_sequence` remains the discarded prefix boundary, while `retention.cursor_floor_sequence` is the oldest sequence from which forward cursor reads are guaranteed complete.

A planned retention boundary is not reported as corruption. `activity_status` exposes it under `retention`.

A cursor older than the safe forward boundary fails explicitly. For example:

```text
after_sequence 1200 expired because per-project retention compacted history through sequence 1250;
the oldest safe forward cursor is 1250
```

A consumer must not silently jump to the latest sequence. It should record an operational gap, reconcile according to its own policy, and restart from `retention.cursor_floor_sequence` only after that decision. Older per-project records remain available to non-cursor tail reads for the dashboard, but they are historical context rather than a complete replay stream once planned internal gaps exist.

Unexpected truncation, replacement, malformed records, or a sequence discontinuity sets:

```text
gap_detected = true
```

Consumers should stop automatic cursor advancement while a gap is unresolved.

## Recommended consumer loop

A downstream consumer such as Ops Inbox should:

1. Call `activity_status`.
2. Refuse automatic ingestion if `gap_detected=true`.
3. Initialize a new cursor to `retention.cursor_floor_sequence`, not blindly to zero, when the source already has a retention boundary.
4. Call `activity_export` or `activity_list` with the durable cursor and a bounded limit.
5. Upsert each record by `action_id`.
6. Commit the highest fully persisted `next_sequence` in the same downstream transaction or checkpoint operation.
7. Continue while `has_more=true`.
8. Surface cursor expiry as an explicit source gap rather than skipping it.

CodexPro owns source action identity and source ordering. The consumer owns its own cursor, retry policy, classification, digesting, and user-facing presentation.
