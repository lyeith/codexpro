# Compact activity JSON for agents

The existing HTTP authentication protects both read-only endpoints:

- `GET /activity.json`: small per-project summaries, without scripts or output bodies.
- `GET /activity/projects/{project_id}.json`: one project's in-flight calls/jobs, managed runs and recent commands. `GET /activity.json?project_id={project_id}` is an alias.

Responses are JSON with `schema_version: 1`, server `generated_at`, explicit limits and coverage. They use `Cache-Control: no-store`. Unknown projects return 404; invalid options return 400. This is an operator view with the same project visibility as the authenticated dashboard.

## Defaults and bounds

| Parameter | Default | Allowed |
| --- | --- | --- |
| `limit` | 8 recent commands | 1–10 |
| `output_bytes` | 1024 combined stdout/stderr bytes per command | 0–4096; 0 omits output |
| `quiet_after_ms` | 300000 (5 minutes) | 30000–86400000 |

Each in-flight category returns at most 10 details, with full counts and truncation flags. Commands are redacted and capped at 1024 UTF-8 bytes. Output tails share a 32 KiB response budget and split each command's allowance evenly between stdout and stderr. Multi-job receipts include up to three output tails and report omissions. Expired/unavailable output is explicit. Non-shell tools return retained operational metadata, not file contents or unretained raw tool responses.

The recent list folds duplicate Bash invocation/completion receipts. Persisted job receipts fill gaps left by journal retention or disabled auditing. It inspects up to three times `limit` recent journal receipts for the project, merges retained completed jobs, and returns newest completions first. The all-project view never reads output bodies or collects Git diffs.

## Reading a project

- `has_inflight_work` and `inflight_counts`: accepted tool calls, running shell jobs and active managed-run claims. A Bash call and its process can appear in both counts.
- `last_command_started_at` / `last_command_started_age_ms`: latest observed command/tool start, including commands that are still running. The corresponding `finished` fields track the latest observed completion. `null` means no retained observation.
- `last_activity_at` / `last_activity_age_ms`: latest of observed starts, completions, running-job output and active-claim contact.
- `inflight.calls`: queued/running state, elapsed time, workspace, command preview and action id. The id is reused in the eventual durable completion receipt.
- `inflight.jobs`: elapsed time, hard deadline, last raw output write, time without output, command and bounded redacted output. An empty output file means no output yet; missing files mean progress is unknown.
- `work_runs`: nonterminal run states, whether claimed, last contact/progress and recovery reason. This also exposes a ready run waiting for its next agent claim, when no shell job exists.
- `recent_commands`: last 8 tool/job receipts by default, timestamps, outcome, operational result metadata, job references and available output tails.

`review_recommended` is advisory. Signals identify a long-running call, a quiet job, a job past its deadline, a stale claim, a ready/unclaimed run waiting beyond the threshold, or a blocked/recovering run. **Silence is not proof of a stall.** Inspect the command, output, deadline and saved handoff before deciding whether to intervene in ChatGPT. These GETs never cancel jobs, acknowledge completion, renew claims, sweep runs or change limits.

Coverage is limited to recorded CodexPro work. In-flight tool calls are process-local; jobs and managed runs use their existing persistent stores. The public activity journal still contains completion events, not durable start events. Other programs and ChatGPT activity that does not call CodexPro cannot be observed.

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

curl --config ~/.config/codexpro/http-auth.conf \
  'https://your-connector.example/activity/projects/my-project.json?limit=8&output_bytes=1024'
```
