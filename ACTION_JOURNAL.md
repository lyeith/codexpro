# CodexPro Action Journal

> **Debug/diagnostic boundary:** this stream records engineering tool activity. It is not a day-to-day operations feed. Downstream systems should project it into an explicit debug namespace while preserving source-owned `action_id` and `codexpro://actions/...` references.

CodexPro can expose a durable, metadata-only stream of direct connector actions. The stream is intended for observability, operational digests, and downstream consumers such as Ops Inbox without requiring a consumer to scrape ChatGPT history or infer actions from Git.

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
CODEXPRO_AUDIT_MAX_BYTES=67108864
CODEXPRO_AUDIT_RETAIN_ACTIONS=50000
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

It groups the latest eight retained actions by configured project and shows each action's local timestamp, effective tool/operation, outcome, bounded target metadata, and duration. For each configured project checkout it also shows the current Git branch and commit plus the tracked working-tree diff against `HEAD` when Git is available.

The dashboard preserves the same safety boundary as workspace tools and the journal:

- it is protected by the server's existing HTTP authentication;
- raw shell command text, file bodies, stdout/stderr, prompts, and raw tool results are not displayed;
- safety-blocked paths such as `.env`, keys, and internal audit files are excluded from path lists and diffs;
- untracked file names may be listed, but their contents are not rendered;
- changed paths and diff output are bounded;
- Git state remains available when auditing is off, while the activity table requires `--audit metadata`.

The page refreshes every 15 seconds while no diff panel is open. It is an operator view, not another source stream, and reading it does not append audit records.

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
- `retention.rotation_count`
- `retention.dropped_through_sequence`
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
    "old_text_bytes": 12,
    "new_text_bytes": 18,
    "expected_sha256_supplied": true
  },
  "result_metadata": {
    "changed": true,
    "bytes": 418
  },
  "result_ref": "codexpro://actions/cpa_0123456789abcdef0123456789abcdef",
  "summary": "edit succeeded; 1 changed path"
}
```

Fields that do not apply to an action are omitted.

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

The journal is deliberately not a transcript.

It never stores:

- file bodies or replacement text
- prompts, plans, or handoff prose
- search query text
- shell command text
- bearer tokens, API keys, or attachment bytes
- stdout or stderr bodies
- raw tool results
- raw principal, request, or transport-session IDs
- absolute workspace roots

Where useful, CodexPro records only safe metadata such as byte counts, booleans, bounded relative paths, command executable names, and SHA-256 digests.

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

- `CODEXPRO_AUDIT_MAX_BYTES`
- `CODEXPRO_AUDIT_RETAIN_ACTIONS`

CodexPro retains the newest complete records, preserves their original sequence numbers, and writes a private retention index containing the dropped-through sequence and compaction generation.

A planned retention boundary is not reported as corruption. `activity_status` exposes it under `retention`.

A cursor older than the retained boundary fails explicitly. For example:

```text
after_sequence 1200 expired because retention dropped actions through sequence 1250;
the earliest retained action sequence is 1251
```

A consumer must not silently jump to the latest sequence. It should record an operational gap, reconcile according to its own policy, and restart from `retained_from_sequence - 1` only after that decision.

Unexpected truncation, replacement, malformed records, or a sequence discontinuity sets:

```text
gap_detected = true
```

Consumers should stop automatic cursor advancement while a gap is unresolved.

## Recommended consumer loop

A downstream consumer such as Ops Inbox should:

1. Call `activity_status`.
2. Refuse automatic ingestion if `gap_detected=true`.
3. Initialize a new cursor to `retained_from_sequence - 1`, not blindly to zero, when the source already has a retention boundary.
4. Call `activity_export` or `activity_list` with the durable cursor and a bounded limit.
5. Upsert each record by `action_id`.
6. Commit the highest fully persisted `next_sequence` in the same downstream transaction or checkpoint operation.
7. Continue while `has_more=true`.
8. Surface cursor expiry as an explicit source gap rather than skipping it.

CodexPro owns source action identity and source ordering. The consumer owns its own cursor, retry policy, classification, digesting, and user-facing presentation.
