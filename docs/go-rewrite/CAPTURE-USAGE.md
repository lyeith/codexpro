# Capture supplement: retained usage and retirement evidence

Captured 31 October 2026. Source baseline: `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`. This supplement interprets the aggregate runtime census in [`evidence/runtime-census.json`](evidence/runtime-census.json), captured by SP-6 at **15:43:13 UTC**. It contains counts and timestamps only; no raw commands, documents, bearer tokens or request payloads are copied here.

## Decision for design review

**The available evidence does not prove that managed orchestration has no remaining consumers. Do not delete it solely because recent retained MCP history has no mutations.** Nine durable runs remain, including one `ready` Anima run and two manual Plans runs created on 30 September and 2 October. The selected live configuration has work mode `on`; it has handoff and Codex-session modes `off`.

Recommended review decision: request an explicit coordinator retirement ruling for orchestration launch/resume/claim/update behavior and its Ralph monitoring/decider paths. If the coordinator confirms those consumers are retired, exclude execution of those paths from the new Go runtime while preserving a read-only historical archive and a documented export/migration procedure. Preserve the ordinary workspace/lease, jobs, output pinning, batch, usage and inbox features independently. They are not interchangeable with managed-run orchestration.

Until that ruling, mark orchestration retirement **held**, not evidence-proven. A compatibility error for a previously callable operation is a contract change and must be named in the approved design; it must not masquerade as a successful no-op. Retain all required main build features regardless of sparse observations.

## What the retained journal actually covers

The live journal is `~/.codexpro/audit/tool-calls.jsonl`; its retention index is the same path plus `.index.json`. The census read **7,450,238 bytes** and **2,405 valid records**, with zero malformed lines. Retained sequence numbers range from 13,173 to 137,839. The oldest per-tool timestamps reach 30 August; the newest reach the capture time on 31 October.

That span is **not continuous traffic coverage**. The retention index identifies `per_project` retention, 113,022 rotations/compactions and 122,262 planned sequence gaps. It has a cursor floor of 137,099 and 2,401 retained entries at its slightly earlier index snapshot. The four-record difference is compatible with a concurrent live writer; this is a read-only observation, not an atomic backup. The journal's overall retained outcomes are 2,178 succeeded, 209 failed and 18 blocked.

Zero records for a tool or zero recent records therefore mean only **not observed in this retained sample**. They cannot establish never-used status, last-use certainty, the absence of HTTP consumers, or a complete operational failure rate. Calls from this SP-6 capture also appear in the recent window. Historical quantiles below describe the retained sample, not a randomized benchmark or a current-load service SLO.

## Main workflow evidence

| Tool/event | Retained records | Records since 30 Oct | p50 / p95 / p99 ms | Interpretation |
|---|---:|---:|---:|---|
| `read` | 439 | 310 | 13 / 176 / 1,249 | Active core workflow. |
| `bash` | 390 | 129 | 2,023 / 21,944 / 119,570 | Active; durations include command execution and waiting. |
| `jobs` | 319 | 60 | 8 / 30,333 / 105,344 | Active; wait options affect latency. |
| `search` | 222 | 156 | 103 / 1,503 / 11,568 | Active core workflow. |
| `batch` | 140 | 25 | 164 / 27,991 / 80,488 | Active; duration depends on children. |
| `edit` | 130 | 20 | 58 / 442 / 4,135 | Active core workflow. |
| `list_projects` | 112 | 112 | 192 / 418 / 504 | Active; this tool's retained history begins 30 Oct. |
| `open_workspace` | 97 | 5 | 31 / 382 / 888 | Active; 20 retained failures, not all classified here. |
| `bash_job` event | 77 | 44 | 53,279 / 1,088,047 / 1,186,507 | Background execution evidence, not a public tool name. |
| `write` | 70 | 13 | 49 / 586 / 3,243 | Active core workflow. |
| `tree` | 57 | 43 | 14 / 121 / 687 | Active core workflow. |
| `start_jobs` | 56 | 46 | 1,522 / 9,249 / 63,563 | Active core workflow. |
| `show_changes` | 50 | 32 | 653 / 23,813 / 38,398 | Active; repository inspection deserves profiling. |
| `server_config` | 48 | 48 | 5 / 13 / 15 | Active diagnostic/config surface. |
| `apply_patch` | 28 | 1 | 73 / 1,314 / 1,573 | Present, sparse recent observations do not justify dropping. |
| `commit_changes` | 8 | 0 | 1,519 / 2,065 / 2,065 | Historical use; keep git feature as required. |
| `create_project` | 3 | 0 | 73 / 257 / 257 | Latest retained call 28 Oct; keep. |
| `codexpro_self_test` | 2 | 1 | 158 / 351 / 351 | Diagnostic, includes current capture. |
| `inspect_workspace` | 1 | 0 | 955 / 955 / 955 | Single retained observation; no retirement conclusion. |

The retained sample does not list every required feature. In particular, missing `ast_grep`, image inspection/import, `stop_jobs`, or a separate pin operation is not evidence of non-use; pinning is also carried through other tool inputs. Their preservation is a user requirement and must be verified by contracts rather than inferred from this table.

## Orchestration and legacy surfaces

| Surface | Retained evidence | Current conclusion |
|---|---|---|
| `work_manage` | 1 retained successful call, 20 Sep; none retained since 1 Oct | Insufficient to retire by census alone. |
| `work_update` | 49 records (47 succeeded, 2 failed), last retained 30 Sep 04:45:10 UTC | No October mutations observed in this sample; durable state proves past use. |
| `work_status` | 88 successful records; only retained October call is SP-6's 31 Oct 15:13 probe | Do not count the capture probe as evidence of an ongoing user workflow. Historical inspection should remain available. |
| `work_claim` | 3 successful retained calls on 20 Sep | Historical claim behavior existed; active claims are unverified. |
| Ralph runs/monitor/decider | 7 persisted runs have mode `ralph`; live work mode is `on` | No complete HTTP/monitor usage census. Explicit retirement ruling needed. |
| Manual durable runs | 2 persisted Plans runs, created 30 Sep and 2 Oct | Refutes a claim that all work state predates October or is empty. |
| `handoff_to_agent` | 6 retained calls on 30 Aug, 4 succeeded / 2 failed | Live handoff mode is `off`; historical use exists. Candidate to omit after confirmation, preserve relevant history. |
| `handoff_to_codex` | 2 successful retained calls on 30 Aug | Live handoff mode is `off`; same limitation. |
| `export_pro_context` | 4 successful retained calls on 30 Aug | Historical use only in this sample; not currently registered in standard mode. Retirement needs approved surface list. |
| Codex-session tools | Selected environment says `CODEXPRO_CODEX_SESSIONS=off` | Disabled today; not proof of no historical consumers. |
| `open_current_workspace` | 3 successful retained calls on 30 Aug | Legacy name; not a current registered standard tool. Do not invent current compatibility requirements. |
| Supertool / full-mode-only entry points | No named records in this retained sample; tool mode is `standard` | Not observed and not standard-registered; no complete historical or HTTP census. |

The live standard registration is a separate source of truth from the historical journal: a past event name does not by itself mean ChatGPT still sees a tool, and a currently visible tool may have no retained observations. The contract inventory must describe both explicitly.

## Durable state is substantial and must not be silently discarded

The work database observed under the configured `~/.codexpro/work` directory is **257,576,960 bytes**. It contains:

| Table | Rows |
|---|---:|
| `runs` | 9 |
| `sessions` | 173 |
| `documents` | 1,516 |
| `events` | 19,406 |
| `iterations` | 231 |
| `operations` | 17,705 |
| `requests` | 1,369 |
| `work_jobs` | 11,934 |

Run states at the snapshot are one Anima `ready`, one IBKR `blocked`, and seven Plans `blocked`. There are **no runs labelled `active` in that snapshot**, which is narrower than saying there are no consumers or no live claims. Session rows have an opaque body; the count 173 is historical row count, not 173 active sessions. Active claim count has not been established.

Several Plans run `updated_at` values cluster at 31 Oct 15:19:28 UTC. Parent inspection identified startup recovery as their cause; do not present those timestamps as new user work or new managed-run launches. The Anima ready run last updated 30 Sep and the IBKR blocked run last updated 20 Sep. The event span runs from 14 Sep to the October recovery, which is likewise not a continuous active-use claim.

The usage database contains 276 `pro_submissions`, 1,516 `usage_changes`, one collector and one settings row, with no `usage_requests` rows in this snapshot. The inbox database contains 20 items, 20 answers and 60 events. These observations support preserving usage and inbox data and behavior independently of the orchestration decision. The main-file byte counts do not establish total SQLite storage when WAL or shared-memory sidecars exist.

## Configuration observations relevant to scope

The process environment whitelist recorded by the census says:

- `CODEXPRO_BASH_MODE=full`.
- `CODEXPRO_TOOL_MODE=standard`.
- `CODEXPRO_WORKTREE_MODE=off`.
- `CODEXPRO_AUDIT_MODE=metadata`.
- `CODEXPRO_WORK_MODE=on`.
- `CODEXPRO_CODEX_SESSIONS=off` and `CODEXPRO_HANDOFF_MODE=off`.
- `CODEXPRO_WORK_DIR=~/.codexpro/work`.
- The host resource helper is `~/.local/bin/ssd-dev`.

The environment also points to the live env and project catalog files. Their secrets and raw contents are intentionally not exported. Base `src/config.ts:9-17` defines the modes; lines 26-35 define work configuration; lines 108-121 define reloadable operational limits. Presence of modes in the legacy type system does not require retaining approval-mode machinery: the user's Go scope explicitly requires full permission, and the design must name removed mode behavior.

## Safe migration and evidence handling

1. During stages 1-4, never alter the live journal, SQLite databases, leases, configuration or service. Read-only queries and aggregate exports are sufficient for this census.
2. Stage private copies under the private Go/TypeScript test state paths; do not point either candidate at live stores. Copying a running SQLite main file alone is not a sound migration procedure: use a consistent read-only snapshot/backup or exported records when migration is eventually authorized.
3. Keep historical runs/documents/events accessible through an archive reader or explicit export even if launch/resume/claim/update execution is retired by ruling. Preserve opaque record identities and timestamps; do not claim exact contract compatibility unless tests demonstrate it.
4. Preserve the required project/workspace/lease and job state separately from orchestration archives. Dropping the latter must not remove workspace attachment, output pinning, job recovery, batch receipts or main tool contracts.
5. Treat the journal's p50/p95/p99 as contextual evidence. Use the controlled stage-1 and stage-4 replays for performance acceptance. Child-agent connector calls that hung in this capture are not attributed to server CPU without independent timing evidence.

## Remaining evidence gaps

- Active claim/session state and complete HTTP usage are unverified.
- Retention gaps prevent a proof that a feature has no recent consumers. Coordinator confirmation is required for the retirement candidates above.
- Per-tool failure causes are not classified by this aggregate census; errors cannot all be attributed to orphaning or load.
- Filesystem type and durable-write cost of the live audit directory are not established by this supplement. The parent profiling evidence handles those measurements.
- No state has been migrated and no cutover has been performed. This supplement is evidence for design review only.
