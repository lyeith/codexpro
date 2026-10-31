# Coordinator ruling and accepted compatibility deltas

Design review commit: `acf6b9b6d2538196ddae10055e9d62648cec831d`.  
Ruling observed in workpackets registry row 31 at 16:12 UTC on 31 October 2026.

## Approval

Coordinator 16:11 UTC: **DESIGN APPROVED (acf6b9b6d2); proceed to stage 3 then 4. No cutover.**

The coordinator explicitly approved retirement of handoff/context dispatch, embedded Codex sessions, the supertool/inventory alternative, autonomous Ralph/decider/monitor orchestration, and the `work_manage`/`work_update` mutation APIs. Plans/SP coordination uses the workpackets registry. Keep `work_status` as read-only history and preserve a read-only archive/export of all historical work data, including all nine retained runs. The ready Anima run is archived and must not be executed.

The ruling requires **Go 1.27.3**, expressed as `go 1.27.0` plus `toolchain go1.27.3` in go.mod, following the Plans WP-16 toolchain update. This supersedes the proposed design's initial 1.26.8 compiler choice.

All other architecture and section-11 gates were accepted, including frozen raw descriptors, official low-level Go SDK registration, native SQLite WAL/FULL, segmented JSONL audit with a derived index, lease health separate from ownership, detached runner/grant/nonce, nice-10 tool children in helper sibling scopes, and private-only imports. Missed gates remain visibly open. Handback requires RESULTS.md and the cutover/rollback runbook.

## Explicit descriptor removals

| Descriptor | Baseline availability | Ruling |
|---|---|---|
| work_manage | Active | Retired |
| work_update | Active | Retired |
| codexpro | Conditional supertool | Retired |
| codexpro_inventory | Conditional supertool | Retired |
| read_handoff | Conditional handoff | Retired |
| wait_for_handoff | Conditional handoff | Retired |
| codex_context | Conditional context | Retired |
| export_pro_context | Conditional context | Retired |
| handoff_to_agent | Conditional handoff | Retired |
| codex_sessions | Conditional session access | Retired |
| read_codex_session | Conditional session access | Retired |

The ordinary active surface therefore changes from 30 to **28 retained descriptors**. Conditional workspace lifecycle descriptors remain. Removal is an approved delta, never a successful unchanged-schema comparison.

## Behavioral deltas

- Autonomous orchestration, run mutation/claim/resume and Ralph execution cease. Historical records are retained and inspectable/exportable.
- Transient Git/IO inspection errors no longer permanently orphan healthy owned workspaces. Historical unavailable leases cannot block unrelated project opens.
- Audit retention no longer rewrites the whole journal on every retained action. JSONL remains authoritative; physical segmented storage is reported honestly, while logical retention and cursor semantics remain explicit.
- Read-touch persistence is coalesced. Ownership and structural lease changes remain durable.
- Go runs only on its own port/token/state during this task. This is a parallel proof, not a deployment.

## Fidelity clarifications from source review

Text read SHA/tag hashes use decoded UTF-8 text; byte counts use the source buffer. Existing-file writes preserve in-place inode/hardlink semantics, while new files use temporary write and rename. Edit provenance stays process-memory and principal-scoped across sessions; Git review checkpoints stay per session. An underlying tool mutation is not retried or represented as rolled back when audit persistence fails: preserve the baseline tool outcome and record the failed audit receipt diagnostically. Successfully acknowledged audit records wait for durable commit.

These details refine implementation fidelity and do not authorize additional descriptor removal or weakening of the approved acceptance gates.
