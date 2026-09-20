# Pro request counter

The Activity dashboard shows confirmed SessionPilot ChatGPT **Pro submissions** for the current counting period, with a project breakdown, JSON output, and manual reset. An accepted request counts immediately, even if still running or subsequently failed/canceled. Preparation failures before Send do not count. Retries count separately when a new turn is actually submitted. This is a request count, not an OpenAI quota or remaining-allowance estimate.

A laptop collector reads SessionPilot's state, archived queries, and incremental `turn-submitted` events. It never opens tabs or sends model requests. The collector transfers only query/turn IDs, project IDs, timestamps, and evidence classification. Prompts, outputs, OpenAI tokens, account identifiers, and credit information stay off the wire. Selected Pro mode takes precedence; historical accepted turns with only requested mode are explicitly labeled in the coverage summary. Unconfirmed or unknown-mode records are excluded and reported.

## Weekly and manual resets

The collector calls the logged-in laptop Codex CLI's documented [`account/rateLimits/read`](https://learn.chatgpt.com/docs/app-server#6-rate-limits-chatgpt) API through `codex app-server --stdio`. It selects the `codex` bucket's **10,080-minute** window, whether primary or secondary, validates its Unix `resetsAt` datetime, and sends only weekly-window metadata. It polls every five minutes and checks again when a deadline passes or a manual reset is pending. It does not consume account reset credits.

Automatic counting follows API-observed period boundaries. When the known reset date passes, the counter moves to that boundary and marks the next date unverified until the API returns it. It does not invent a recurring calendar schedule. The dashboard formats dates in the browser's local timezone.

**Reset counter** queues an idempotent request. The laptop fetches the account reset date afresh, validates it, and completes the reset. The cutoff is the time of the click, so requests submitted while the date check is pending remain counted. Failure to obtain a valid date leaves the reset pending and the existing count intact; the collector retries and the dashboard explains the delay. Reset applies globally, including when viewing one project. It neither changes the account allowance nor deletes history. Requests imported later are counted using their submission time, so old backfill cannot undo a reset.

The active cutoff is the later of the API period start and the most recent manual reset. Both settings and receipts survive service restarts. Reset request IDs and revision checks protect against double clicks, network retries, and stale dashboard tabs.

## API and local operation

All endpoints use existing CodexPro authentication and reject cross-origin browser writes. Responses are schema version 1 and use the existing no-store response policy.

| Endpoint | Purpose |
| --- | --- |
| `GET /usage/v1/pro` | Count, reset dates, project breakdown, coverage, collector freshness, pending reset |
| `GET /usage/v1/pro?project_id=twilight-dev` | Bounded project summary |
| `GET /usage/v1/pro/history` | Latest 30 reset requests, completions, and API boundary changes |
| `POST /usage/v1/pro/reset` | Queue `{schema_version:1, request_id:<UUID>, expected_revision:<settings.revision>}` |
| `POST /usage/v1/pro/submissions` | Collector receipt ingestion; max 100 entries, deduplicated by query + turn |
| `POST /usage/v1/pro/limits` | Fresh API observation; stale timestamps and invalid weekly windows are rejected |

The server ledger is `usage/usage.sqlite` beside the configured audit log. The laptop outbox/cursors are `~/.local/state/codexpro-pro-usage/collector.sqlite`, with a process lock preventing duplicate collectors. Both databases use private permissions and durable SQLite transactions. Rotation, partial log lines, and crash-after-upload replay are supported. Keep one collector identity per SessionPilot installation. Use the same account in Codex and the ChatGPT worker browser.

Run with the existing private activity client and project mappings:

```sh
python3 scripts/codexpro-pro-usage.py --watch
python3 scripts/codexpro-pro-usage.py --account-reset
```

Defaults: `~/.config/codexpro/activity-client.json` for the server origin and private curl config; `~/.config/codexpro/ralph-monitor.json` for ChatGPT Project URL mappings; `~/.sessionpilot` for source records. Override with `--config`, `--monitor-config`, `--sessionpilot-home`, `--state-dir`, `--collector-id`, or `--codex`. Disabled/held loop targets still contribute historical usage. Unmapped projects contribute to the global count under Other / unmapped.

For a laptop LaunchAgent, install the script beside `codexpro-ralph-monitor.py`, use an absolute Python and Codex executable path, and run `--watch` with KeepAlive/RunAtLoad. This collector is independent of the Ralph watcher, so held loops do not stop usage collection. A source older than two minutes is marked delayed. API verification older than ten minutes is marked stale.

If a server ledger is restored or recreated, stop the collector service, run once with `--replay`, then restart the service. This re-uploads all locally retained receipts without double counting. Recover only from retained evidence; browser submissions not observed by SessionPilot cannot be reconstructed by this collector.

On David's laptop, the installed command is `~/.local/bin/codexpro-pro-usage` and the LaunchAgent is `com.davidwong.codexpro-pro-usage`. A read-only curl example using the existing local key:

```sh
curl --disable --config ~/.config/codexpro/ssd-curl.conf \
  --silent --show-error --fail https://codexpro.foundry65.com/usage/v1/pro
```
