# CodexPro UI fixes — 2026-09-09

Implementation prepared in the SSD canonical checkout and approved for commit/deployment. This supersedes the original audit findings. Actual deployed revision and rollback target are recorded in `~/apps/codexpro/deploy.log` and the `current` symlink.

## Implemented coverage

| Original findings | Change |
|---|---|
| W1 | Registered commit/start/collect/stop cards and implemented commit and per-job renderers. |
| W2, W5 | Bash cards distinguish running from completed commands, show job identity/state/origin and timeout/stop context, and disclose bounded output. |
| W3 | A repeated review is labelled “Unchanged review”, not “Clean”. |
| W4 | File metrics use the full list; limited lists/chips and code previews explicitly disclose their display limits. |
| W6 | Missing-result recovery tells users to inspect the existing job/workspace before repeating a mutation. |
| W7 | Added expandable result cards for batch, read/search/tree/AST search, write/edit/patch/import and project creation. Generic cards show per-field details and explicit failure/partial states. Image inspection continues to use native MCP image content rather than replacing it with a JSON card. |
| D1 | Persisted full commit identity, branch, consistent file count, clean/dirty/unavailable state, and skipped-path count. Historical Git evidence provides a fallback identity where it exists. |
| D2–D5 | Persisted bounded per-job IDs/outcomes, wait metrics and stopped/already-finished IDs. Plural API cases are supported. Collection success is explicitly labelled tool-call success, separate from job execution outcomes. |
| D6 | Expanded cards show historical Git/file evidence and optional recorded metadata. |
| D7 | Job-start commands are captured in authenticated dashboard metadata and correlated to returned job IDs. Background completions retain output-byte, signal, timeout and stop metadata. |
| D8 | Authenticated read-only job output endpoint returns current state and a redacted, escaped 16 KiB tail per stream. Structured tool error/recovery messages are bounded and dashboard-only. Known exception codes receive safe explanations; arbitrary exception bodies are not retained. |
| D9 | Persisted bounded historical child outcomes, error/exit codes and truncation markers. Bash children retain job IDs for output retrieval. Saved batch definitions are labelled as definitions, not execution results. Missing/pruned historical details are explicit. |
| D10 | Expanding a job fetches its current state/output. Open running jobs refresh every five seconds; manual refresh also works. Historical receipt and current state are labelled separately. |
| D11 | Mutation capability is labelled “may modify files”, including the timeline legend. |
| D12 | Added project filters, stable sequence-cursor pagination and retained/matching counts. Reload preserves the selected query. |
| Test isolation | `npm test` now runs with a fresh job directory so live background reminders cannot contaminate JSONL assertions. |

## Data boundaries

- Public activity-list/get/export records remain free of dashboard command/error bodies. Per-job and per-child metadata is capped at 32 entries and marks truncation.
- The job endpoint uses existing HTTP authentication, requires the matching workspace ID and a currently catalogued root, sends `Cache-Control: no-store`, and neither acknowledges completion nor changes job state.
- Output and display limits remain bounded and explicit. Full historical stdout/stderr is not copied into the journal; current output comes from retained job files. Expired jobs, unrecorded fields and pruned batch definitions cannot be reconstructed and are labelled unavailable.
- Error diagnostics are limited to 2 KiB and recovery messages to 1 KiB, with redaction and truncation markers. Raw unstructured exception messages are intentionally excluded.
- Earlier command-response fixes (full SHA/post-commit status, non-acknowledging listing, actual wait and output-mode fields) remain part of this change set.

## Verification

- Full automated suite: 112 tests, including actual tool result → audit journal → view model → expanded HTML coverage.
- Widget harness covers running jobs, failed collection, commit clean/dirty/unknown state, checkpoint reviews, large-file counts, escaping and mutation-safe missing-result guidance.
- HTTP coverage verifies authentication, successful output retrieval, mismatched-workspace rejection, missing jobs and invalid history cursors. Direct output-read checks verify that completion is not acknowledged.
- Dashboard browser preview inspected with synthetic records. Historical facts, failed job exits and full commit identity render in expanded cards. The preview is static; endpoint behavior is verified by the HTTP tests.
- Build and `git diff --check` pass. These checks ran before deployment; deployment status is recorded separately in the service deployment log.
