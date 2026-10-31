# HTTP payload and file-contract edge cases

Source commit: `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`. Static capture only. These findings supplement CAPTURE-CONTRACTS.md and require runtime fixtures before equivalence is accepted.

## Inbox payloads

All three write schemas are strict (unknown keys fail). Identity strings use `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$`.

- Question: schema_version=1; id/project_id; source trimmed1–160; title trimmed1–200; question trimmed1–2000; context max4000 default empty; options up to6 trimmed nonempty strings max500 default[]; recommendation max2000 default empty; blocking_scope enum none/ticket/project; blocked_work up to12 strings max200 default[]; optional source_url must be valid HTTP(S).
- Answer: schema_version=1, request_id identity, expected_revision positive integer, answer trimmed1–8000.
- Delivery: schema_version=1, consumer identity, revision positive integer.
- Item: question fields plus status pending/answered, revision, created_at, updated_at, optional answer{text,at,by,request_id}, deliveries[{consumer,revision,at}].
- Event: sequence plus schema_version,kind,at,project_id,id,revision,detail.

Question publish is idempotent for equal parsed content and rejects conflicting content with409. Initial revision1. Answer deduplicates by project/id/request_id before checking current revision: replaying the exact previously accepted request returns its original result; changing its payload yields409. A successful new answer increments revision and records the authenticated principal. Delivery requires an answer and matching current revision; equal consumer/revision is idempotent. Updating a consumer replaces its old receipt, and new consumers are capped at50.

Consumer-filtered list returns pending questions or answers not delivered to that consumer at the current revision. Ordering is status DESC,updated DESC,project,id. next_offset is null at end. Event sequence is global/autoincrement, ordered ascending after the supplied sequence. The stores use SQLite WAL and synchronous FULL, transaction.immediate for mutations. Source: `src/inbox/store.ts:6–31,34–106`.

## Usage payloads and outputs

All schemas strict. Usage identity pattern is the inbox pattern with max200 characters (rather than160). Dates use ISO datetime validation allowing offsets.

- Submission: query_id,turn_id,project_id nullable,submitted_at,mode_evidence selected/requested.
- Ingest: schema_version1,collector_id,submissions max100,coverage{unconfirmed,failed_before_send,unknown_mode nonnegative integers; oldest_record_at nullable datetime}.
- Reset: schema_version1,request_id,expected_revision positive integer.
- Limits: schema_version1,checked_at,optional reset_request_id,weekly nullable {resets_at,window_minutes:10080,used_percent0–100}.
- Settings: revision,weekly_anchor_at/manual_reset_at nullable; pending_reset nullable {request_id,requested_at,by}; limits_checked_at/limits_verified_at/limits_error nullable.
- Ingest reply: schema_version1,accepted,inserted.
- Snapshot: schema_version1,generated_at,settings,period_started_at,next_weekly_reset_at,reset_date_stale,count,requested_mode_count,first_submission_at,last_submission_at,all_time_count,projects[{project_id,count}],collectors,coverage_note.
- Collector: collector_id,last_sync_at,coverage,stale.
- History: most recent30 changes, descending sequence.

Submissions deduplicate on (query_id,turn_id). Different timestamps or conflicting nonnull projects yield409. A missing historical project can be filled later; selected evidence is never downgraded to requested. New submission timestamps must be nonnegative and at most5min in future. Store normalizes timestamps to UTC.

Reset is request-id idempotent, revision guarded, and cannot begin while another pending reset exists. It does not delete submissions. A limits observation is accepted only between now−120s and now+30s; weekly reset must be after checked_at and at most one week+5min beyond it. Older observations than the stored observation yield409. A pending reset completes only with matching reset_request_id and a freshly observed date at/after requested_at. Weekly bounds are observed, never guessed after expiration. Collector staleness is2min; reset-date staleness includes absent/expired boundary or verification older than10min.

Do not simplify usage into a raw request counter: these reconciliation and idempotency rules are existing public behavior. Source: `src/usage/store.ts:6–32,35–151`.

## Instruction metadata

Opening workspaces exposes agents_loaded, optional agents_path, agents_files, agents_sources[{scope:global/workspace,path,loaded,truncated,error?}],agents_complete. With no instruction files, agents_complete=true (every over empty sources), agents_loaded=false. File precedence per directory is AGENTS.override.md,AGENTS.md,agents.md,.agents.md with case-insensitive fallback. Only one selected file per scope; realpath duplicates are removed.

Global instructions come from configured codexDir; workspace scopes proceed from root down to target directory. Source bodies are bounded, UTF-8 decoded without splitting a final character, secret-redacted; any unreadable/truncated scope marks incomplete. A not-yet-created target inherits from its parent. Source: `src/instructions.ts:22–46,49–134`.

## File/edit fixtures required

1. edit_tag is the upper-case first4 hexadecimal characters of SHA-256 of decoded text. Snapshot identity is authenticated principal plus canonical real path, not merely workspace_id.
2. Store bounds are256 paths,4 versions/path,64MiB total raw text; recency updates on record/resolve. Seen ranges accumulate for identical text. A new version with the same short tag replaces the old collision; resolution still requires full live-text equality.
3. A tag first read in one HTTP session can be used in another session for the same principal. The tag never authorizes editing unseen lines. Insertions require their anchor line to have been displayed too.
4. read counts all newline-separated lines including a terminal blank line. Its byte budget measures line-numbered, redacted text actually returned. Overlong first line fails read_line_too_large with search recovery; it does not emit a partial line.
5. read truncated=true if start_line>1 even when the selected page reaches EOF; has_more depends only on end_line<total_lines. next_start_line becomes null at EOF.
6. Tagged edits preserve UTF-8 BOM and homogeneous existing line endings. Mixed newline styles fail with an instruction to use patch. Replacement content normalizes line endings and removes one trailing empty content line.
7. Ranges refer to the original snapshot and must be disjoint. Two insertions into the same original gap fail. An insertion strictly inside a replaced/deleted range fails; insertions on boundaries are allowed.
8. File writes lock canonical paths and recheck the original decoded text before modifying it. Existing files use r+, truncate/write, and fsync in place (fsOps.ts298–320); new files use a sibling temporary file, fsync, and rename without parent-directory fsync (323–334). The existing-file path preserves inode/hardlink identity but is not crash-atomic replacement. If Go strengthens crash behavior, document and test inode/alias/mode effects as an explicit compatibility decision. expected_sha256 is binding when supplied: a detected conflict must leave the existing content unchanged.
9. An empty file, no-final-newline file, CRLF, lone CR, BOM, non-ASCII/supplementary Unicode, malformed UTF-8 without NUL, and a line containing secret-like source text need separate parity fixtures. Bytes and SHA are not interchangeable with rune counts or UTF-16 lengths.
10. Safe file tooling and full Bash remain distinct: file path guards block secrets/build artifacts and refuse escaping symlinks; the explicit full-Bash policy can reach those paths. Do not silently weaken file APIs to emulate Bash.

Source: `src/fsOps.ts:60–243,245–355,512–570,633–777`, `src/guard.ts`. Some source reads themselves redact expressions containing secret-like variable names; exact runtime bytes must be compared using isolated fixture outputs rather than reconstructed redacted source.

## Error codes and connector normalization

Raw MCP tool error output is authoritative. `CodexProError` has no default code (`src/guard.ts:36–49`); `errorResult` conditionally emits error_code/retry/recovery (`src/tools/shared.ts:193–215`). The surrounding ChatGPT connector has surfaced INVALID_ARGUMENT for orphaned/ENOENT errors where the raw source does not assign that code. Therefore do not build Go's server error contract from the outer connector's automatic categorization.

Explicit source codes include args_invalid; project_unknown/project_exists/project_root_not_allowed; workspace_unknown/workspace_root_invalid/workspace_root_not_allowed; path_blocked/path_outside_workspace/path_symlink_refused/path_not_file/path_not_directory; file_too_large/file_binary/file_exists/file_changed/secret_content_blocked; edit_tag_invalid/edit_tag_unknown/edit_tag_stale/edit_range_unseen; read_line_too_large; patch_format_invalid/patch_context_stale/patch_apply_failed; bash_disabled/bash_blocked/bash_session_required/bash_session_mismatch; job_not_found/job_limit_reached/job_start_failed/job_cursor_invalid/job_output_expired/job_output_invalid/job_storage_limit; batch validation codes; nothing_to_commit/git_unavailable/git_command_failed; search_cursor_invalid. This list combines the documented contract and observed source and is not a substitute for negative runtime fixtures.
