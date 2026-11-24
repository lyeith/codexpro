# Compact activity JSON for agents

The existing HTTP authentication protects both read-only endpoints:

- `GET /activity.json`: small per-project summaries, without scripts or output bodies.
- `GET /activity/projects/{project_id}.json`: one project's in-flight calls/jobs, archived work-run records and recent commands. `GET /activity.json?project_id={project_id}` is an alias.

Responses are JSON with `schema_version: 1`, server `generated_at`, explicit limits and coverage. They use `Cache-Control: no-store`. Unknown projects return 404; invalid options return 400. This is an operator view with the same project visibility as the authenticated dashboard.

## Defaults and bounds

| Parameter | Default | Allowed |
| --- | --- | --- |
| `limit` | 8 recent commands | 1–10 |
| `output_bytes` | 1024 combined stdout/stderr bytes per command | 0–4096; 0 omits output |
| `quiet_after_ms` | 300000 (5 minutes) | 30000–86400000 |
| `run_id` | omitted | Full or unique short archived run ID in the selected project, including terminal runs |
| `agent` | omitted | Exact caller label, 1–4 ASCII letters, digits or hyphens; case-sensitive |

Each in-flight category returns at most 10 details, with full counts and truncation flags. Commands are redacted and capped at 1024 UTF-8 bytes. Output tails share a 32 KiB response budget and split each command's allowance evenly between stdout and stderr. Multi-job receipts include up to three output tails and report omissions. Expired/unavailable output is explicit. Non-shell tools return retained operational metadata, not file contents or unretained raw tool responses.

The recent list folds duplicate Bash invocation/completion receipts. Persisted job receipts fill gaps left by journal retention or disabled auditing. It inspects up to three times `limit` recent journal receipts for the project, merges retained completed jobs, and returns newest completions first. The all-project view never reads output bodies or collects Git diffs.

Every tool accepts an optional `agent` label, for example `"SP3"`. It is caller-supplied tracking metadata, never an identity for authorization or task grants. An explicitly configured local Agentmail integration may also use it to select mail addressed to that logical SP within the authenticated fleet account; this does not verify a ChatGPT conversation. Calls, their retained audit receipts, and jobs started by the call retain the label. Project JSON exposes the optional `agent` field on `inflight.calls`, `inflight.jobs`, and `recent_commands`; older or unlabeled records omit it. Omitting `agent` on a later call does not reuse a previous call's label.

Job output also carries optional `output.agent` (including entries under `job_outputs`) for the job's original caller. A receipt that inspects another caller's job retains its own `recent_commands[].agent`; its referenced output keeps the original job label and remains available through the filter.

The `agent` query filters calls, jobs and recent receipts before their counts and detail limits are calculated. The recent-receipt scan limit counts matching receipts; finding them can inspect additional retained metadata. Archived runs retain their project scope because they have no caller label. `/activity/live` also accepts this exact label filter and displays labels beside calls and jobs. Omit the query to include all labels and unlabeled work.

The `/activity` page and `/activity/data` show process-local per-agent tool metrics. Use the JSON endpoints or live fragment for detailed calls, jobs and retained receipts. Work-run fields describe the last state saved in the configured immutable archive; a historical claim does not establish that a worker is running now.

## Reading a project

- `has_inflight_work` and `inflight_counts`: accepted tool calls, running shell jobs and claims marked active in the work archive. A Bash call and its process can appear in both counts. Inspect current jobs separately from historical claims.
- `last_command_started_at` / `last_command_started_age_ms`: latest observed command/tool start, including commands that are still running. The corresponding `finished` fields track the latest observed completion. `null` means no retained observation.
- `last_activity_at` / `last_activity_age_ms`: latest of observed starts, completions, running-job output and active-claim contact.
- `inflight.calls`: queued/running state, elapsed time, workspace, command preview and action id. The id is reused in the eventual durable completion receipt.
- `inflight.jobs`: elapsed time, hard deadline, last raw output write, time without output, command and bounded redacted output. An empty output file means no output yet; missing files mean progress is unknown.
- `work_runs`: saved nonterminal run states, whether claimed, last contact/progress and recovery reason. This can include an archived ready run without a current shell job.
- `recent_commands`: last 8 tool/job receipts by default, timestamps, outcome, operational result metadata, job references and available output tails.

`review_recommended` is advisory. Signals identify a long-running call, a quiet job, a job past its deadline, a stale claim, a ready/unclaimed run waiting beyond the threshold, or a blocked/recovering run. **Silence is not proof of a stall.** Inspect the command, output, deadline and saved handoff before deciding whether to intervene in ChatGPT. These GETs never cancel jobs, acknowledge completion, renew claims, sweep runs or change limits.

Coverage is limited to recorded CodexPro work. In-flight tool calls are process-local; jobs use the persistent job store and archived runs use the configured immutable work history. The public activity journal still contains completion events, not durable start events. Other programs and ChatGPT activity that does not call CodexPro cannot be observed.

With `run_id`, a top-level `run` adds bounded objective/scope, todo counts and the
first five unfinished todos, checkpoint/next action/blockers, unresolved operation
count and recorded completion evidence. Unknown or cross-project run IDs return
404. It never returns claim tokens, principal IDs, or capabilities. File-backed
Ralph loops do not need a managed run; see the [repository-driven monitor](RALPH_MONITOR.md).

Short IDs use a case-sensitive `run_` prefix with at least eight characters after
the underscore, or the historical `prefix…suffix` form (`...` also works).
Resolution only considers the requested project. Ambiguous references return
HTTP 409 with `error: "ambiguous_run"`; responses identify the selected run with
its full ID. See [short run references](WORK_RUNS.md#short-run-references).

## Curl helper

`scripts/codexpro-activity.py` uses curl and a private authentication config, keeping the token out of URLs and process arguments. Install it as `~/.local/bin/codexpro-activity` and configure `~/.config/codexpro/activity-client.json`:

```json
{
  "base_url": "https://your-connector.example",
  "curl_config": "~/.config/codexpro/http-auth.conf"
}
```

The curl config contains a bearer `header` and must have mode 0600. The containing directory should have mode 0700. Keep credentials outside Git and never paste their contents into agent output. `CODEXPRO_ACTIVITY_CONFIG` can select a different settings file.

```sh
codexpro-activity
codexpro-activity my-project
codexpro-activity my-project --limit 5 --output-bytes 512
codexpro-activity my-project --quiet-after-ms 600000
codexpro-activity my-project --run-id run_example
codexpro-activity my-project --agent SP3

curl --config ~/.config/codexpro/http-auth.conf \
  'https://your-connector.example/activity/projects/my-project.json?agent=SP3&limit=8&output_bytes=1024'
```
