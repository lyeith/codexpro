# CodexPro response changes and front-end audit — 2026-09-09

**Implementation update:** the fixes are implemented in the SSD checkout and approved for commit/deployment. See [implementation status](UI_IMPLEMENTATION_2026-09-09.md). The findings below preserve the original audit.

Inspected canonical SSD checkout `/home/spite/Projects/codexpro`, baseline `a19e378`, and live release of the same name. This report covers both the ChatGPT tool-card widget and the `/activity` dashboard. Findings were checked against source and synthetic journal-to-HTML/widget-renderer probes; no authenticated browser session was used. No deployment or service restart was performed. Front-end findings below are catalogued, not fixed.

## Implemented response changes

- `commit_changes.commit` now contains the full SHA; `commit_short` provides 12 characters. Both the text receipt and structured result report commit identity and branch.
- `working_tree_clean` is true, false, or null when the post-commit status check failed. `status` contains the post-commit porcelain status; `status_error` explains an unavailable status. A status-read failure does not misreport a successful commit as failed or the tree as clean. Status is a snapshot, not a guarantee against later concurrent writes.
- Listing jobs no longer acknowledges finished jobs or clears their completion reminders. Explicit collection still acknowledges, and results can still be collected again by ID.
- `waited_ms` now measures the actual wait using a monotonic clock. `requested_wait_ms` preserves the requested limit.
- Collection returns `all_succeeded` separately from `all_finished`.
- Job output includes `output_mode` (`head` or `tail`). Tool guidance explicitly says that `full_output=true` returns the bounded beginning of a finished job's output. A truncated head includes instructions to collect again with `full_output=false` to inspect the ending. Running jobs continue to return tails.

Changed implementation: `src/gitOps.ts`, `src/tools/git.ts`, `src/tools/bash.ts`. Regression coverage: `test/tool-recovery.test.mjs`, `test/jobs.test.mjs`.

## ChatGPT tool cards

| ID | Missing or inaccurate behaviour | Evidence / cause | Proposed correction |
|---|---|---|---|
| W1 | `start_jobs`, `jobs`, `stop_jobs`, and `commit_changes` have no custom expanded card. | `src/tools/registry.ts` entries do not set `toolCard: true`; `src/toolCardWidget.ts` render dispatch has no corresponding handlers. | Register cards and add renderers for each result shape. Simply registering them currently produces generic JSON. |
| W2 | A running/promoted Bash job is presented as “Verification needs attention”, with no job ID or collection guidance. | `renderBash` only interprets exit code/signal and assumes a finished command. Synthetic running-job payload reproduced this. | Branch on job status; show running state, job ID, deadline and collection guidance. |
| W3 | A repeated `show_changes(since=last_shown)` response can be labelled “Clean” despite remaining dirty files. | `renderChanges` derives cleanliness from delta fields suppressed on a checkpoint hit and ignores `review_checkpoint_hit`. Reproduced with dirty `status`. | Present “No new changes since last review”; derive clean state only from actual repository status. |
| W4 | Change-card file counts can undercount large changes. | `renderChanges` takes `values(changed_files, 18)` and uses that sliced array's length as the metric. | Count the full list and explicitly label a limited preview. |
| W5 | Bash cards omit output-truncation, timeout and job-stop context. | `renderBash` renders directory, exit, duration and stdout/stderr; it does not render `truncated`, `timed_out`, job status/origin or stop reason. | Show the completeness boundary and terminal reason, and distinguish command execution from verification. |
| W6 | The unavailable-result card advises retrying the action, even when it may have committed or mutated successfully. | `renderUnavailable` says to refresh the connection and “try the action once more.” | Recommend inspecting/recovering the result first; do not blindly repeat a mutation. |
| W7 | Other core tools also lack custom cards: batch, read/search/tree/AST search, write/edit/patch/import, project creation and image inspection. | Only workspace opening, workspace inspection, Bash, show_changes and handoff have `toolCard: true`. | Explicitly choose supported card coverage; text/native-image results still exist, so this is a display-coverage gap rather than proof of missing tool data. |

## Activity dashboard

| ID | Missing or inaccurate behaviour | Evidence / cause | Proposed correction |
|---|---|---|---|
| D1 | Commit cards can expand with no facts: hash, branch and file count are absent. | `src/audit.ts:summarizeResult` discards `commit` and `branch`, and records `files_count`; `actionFacts` expects `commit`, `branch`, `file_count`. Synthetic commit result produced headline `git.commit` and `facts: []`. | Persist an explicit bounded commit receipt and use consistent count names. Include the new clean/unknown status fields. |
| D2 | Job start/stop cards have no job-specific detail. | `actionFacts` has no `start_jobs` or `stop_jobs` case; it still contains obsolete `stop_job`. Generic facts may show only Project. | Add plural API renderers with job IDs and per-job outcomes. |
| D3 | Collection cards omit IDs, per-job status/exit/signal/stop reason, elapsed time, output completeness, and stop results. | Audit request metadata keeps only `job_ids_count`; result metadata keeps only job count/running count/all_finished/restarting. The jobs array and stopped/already-finished IDs are discarded. | Persist bounded per-job metadata with stable IDs and a completeness marker; render it as job rows. |
| D4 | Collection “Waited” is the requested limit, not elapsed time. It can show 30 s for a 2 ms collection. | Dashboard reads request `wait_ms`; new response `waited_ms` is not yet retained or rendered. Reproduced through journal-to-HTML path. | Retain actual/requested waits separately and display actual wait. |
| D5 | A successful collection call can look like successful work even when collected jobs failed. | Activity status describes the tool call. `all_finished` is retained but not shown; per-job outcomes and new `all_succeeded` are discarded. | Separate collection success from job execution success in labels and colour. |
| D6 | Expanded cards hide historical Git and file evidence already present in the model. | `dashboardAction` builds `gitBefore`, `gitAfter`, `pathEvidence`, request and result fields; `renderAction` never calls existing `renderActionGit`, `renderActionEvidence` or `renderFieldSection`. Tests explicitly expect no “Git evidence”. | Render selected historical before/after facts and provide a bounded details section. The project's current Git panel is not a substitute for a commit receipt. |
| D7 | Initial job-start commands/labels are missing; background completion detail is partial. | `dashboardMetadataFor` captures exact scripts only for Bash, bash_job and batch, not `start_jobs`. `bash_job` has facts but lacks the Bash headline case, and output byte/signal/timeout summarization is restricted to `bash`. | Capture bounded dashboard-only job command labels/scripts consistently and render terminal metadata for background jobs. |
| D8 | stdout/stderr and detailed failure explanations are not available in expanded dashboard cards. | The journal deliberately omits raw output; renderer shows only curated facts and error code. | Decide separately on authenticated, bounded job-output retrieval/retention. Rendering alone cannot recover output never journaled. Preserve existing redaction/privacy boundaries. |
| D9 | Saved-batch expansion shows a definition rather than the historical result of each operation. | Batch fragment renders stored operations and raw definition. Journal keeps aggregate counts and failed operation ID, but the card omits several failure/resume/truncation fields and has no per-child historical result view. Pruned definitions may no longer load. | Label definition versus execution; retain/render bounded child outcomes and unavailable/pruned states. |
| D10 | Expanded job cards cannot update while open. | Page auto-refresh explicitly skips whenever any `details[open]` exists; action expansion only fetches saved batches, not fresh job state. | Show the snapshot time and add targeted refresh/live state retrieval for jobs. |
| D11 | “wrote files” overstates what an action did. | Badge derives from `action.mutating`, which denotes mutation capability/classification (Bash/job start/stop), not observed file writes. | Say “may modify files”, or show actual changed-path evidence separately. |
| D12 | Older actions disappear from the visible list despite retention. | Dashboard caps recent actions at 30 and per-project actions at 5, with no action-history pagination. Timeline is separately bounded to 250 actions/14 days. | Expose pagination/filtering and indicate the visible-versus-retained boundary. |

## Reproduction and validation

- Build and 11 targeted response/job tests passed.
- Full suite passed: **109/109**, with isolated job storage (`CODEXPRO_JOBS_DIR` pointing to a temporary directory).
- A synthetic `AuditJournal.record` → `collectActivityDashboard` → `renderActivityDashboardPage` probe confirmed D1–D4: commit facts empty; start/stop have only generic project facts; collection shows requested 30 s rather than actual 2 ms; output, IDs and start command absent.
- Executing the tool-card JavaScript with the repository's fake-host harness confirmed W2 and W3.
- Diagnostic probes are preserved alongside this report for repeatability. Their imports target SSD's canonical compiled modules. They are audit scripts, not regression tests asserting desirable behaviour.
- Initial nonisolated full-suite and audit-only runs failed a JSONL-export test while live job reminders were present; using isolated job storage resolved it. The suite should always isolate job state from the live service. This is separate from malformed journal records: no journal corruption was established.

## Suggested implementation order

1. Add explicit commit/job journal schemas and retain new response fields; align plural tool names.
2. Register and implement ChatGPT commit/job cards; correct running, checkpoint and missing-result semantics.
3. Render dashboard per-job outcomes, historical commit receipts and snapshot/completeness indicators.
4. Add focused live refresh/history navigation, then decide whether and how to expose bounded output and per-batch execution details.
5. Add end-to-end tests across actual tool result → journal → view model → expanded HTML. Existing dashboard tests largely exercise curated file/Bash facts and did not catch schema mismatch in commit/plural jobs.

Historical records cannot be fully repaired when the required fields were never retained. Recover only from evidence still available (Git snapshots, job files or saved batch definitions), and label unavailable fields explicitly.
