# Work coordinator implementation

Implementation branch: `feature/work-coordinator-20260914`, based on `c1b7842`.
Canonical implementation checkout: SSD `/home/spite/Projects/codexpro-work-coordinator`.
The laptop checkout is a staging clone; production remains at `78c6e67`.

User-approved scope: discoverable optional runs, drafts and structured todos,
bounded claimed iterations, guarded mutations and durable operation/job receipts,
crash recovery, distinct iteration/run completion, document manifests and memory,
recent activity briefings, in-flight/stall visibility, and continuation guidance.
Only explicit **Ralph mode** receives the 30-minute continuation check. Timing
comes exclusively from the CodexPro machine; clients cannot override it.

Implementation checkpoints:

- [x] Transactional store, bounded schemas, server clock and mode policy.
- [x] Hybrid managed workspaces, ownership and common execution admission.
- [x] Durable job launch and run-aware process reconciliation.
- [x] Four MCP tools, todos, documents, handoffs, verification and recovery.
- [x] Discovery/recent changes, status/health, guidance and CLI integration.
- [x] Race/crash/clock tests, regression checks and packaged Linux verification.

Design details are in the session study on the laptop. This file records actual
implementation progress and any material departures; it is not a completion receipt.

## Implementation checkpoint (2026-09-14)

Implemented in the staging source and synced to the dedicated SSD worktree:
SQLite store/indices and transactional process ownership; run/iteration/clock schemas;
per-project managed worktree adapter; common direct/batch/supertool admission;
run-scoped operation keys; durable command preparation/grant protocol;
retained job receipts; automatic expiry/restart recovery; versioned todo,
specification, handoff, document and evidence lifecycle; distinct iteration/run
completion; unchanged-source command verification; recent project activity;
claim/in-flight/stall status; manual/Ralph guidance; CLI/profile integration;
and a supervised adapter for the legacy executor/reviewer engine.

The CLI now stores private retry receipts and final handoff references. It does
not choose a fresh-session launcher. Notes/history have explicit capacity
ceilings; there is no automatic deletion of source or unresolved history.
Required acceptance criteria currently need executable server checks. These
limits and cooperative full-Bash containment are documented in WORK_RUNS.md.

Final regression suite: 140/140 passed on macOS Node 24.17.0 and SSD Linux Node
22.22.1. The packed package's 15 lifecycle/HTTP/crash tests passed on Node 22.22.1
and Node 20.20.2, including clean process shutdown. Node 20 used an isolated native
build toolchain with matching headers, as described below. The existing smoke
suite passed, including the packaged text-only
MCP surface and >1 MB output capture/pagination. A real isolated SSD systemd user
service proved cleanup of both a detached daemon and an escaped child holding
capture pipes after supervisor escalation. Dependency audit: zero vulnerabilities.
No production service/config has been changed.

Implementation and validation are complete on the dedicated feature branch.
Integration and production rollout remain separate. Laptop tests need a canonical TMPDIR
(e.g. /Users/davidwong/Library/Caches/codexpro-work-validation/tmp); legacy fixtures
assume canonical paths and fail under macOS /var -> /private/var temp aliases.
SSD validation uses a TMPDIR under `/home/spite/codexpro-validation-20260913`
because the host's `/tmp` has a per-user quota. When testing a second Node major,
use matching headers and an unmodified node-gyp: Ubuntu's system node-gyp uses
its installed Node headers and links `libnode.so.127`, which cannot be mixed with
the isolated Node 20 executable. This is an installation-environment constraint;
the packaged application code is identical across the runtime checks.

## Blast radius and boundaries

The coordinator is opt-in. Four tools and the optional workspace execution
envelope appear only when enabled. Central admission wraps direct, supertool and
batch dispatch so a new mutation tool cannot accidentally bypass run fencing.
Managed workspaces use a separate retained-worktree adapter; ordinary workspace
and legacy launcher workflows remain available.

Job preparation/grant persistence, deadlines and quiescence reconciliation apply
to the shared job runner, including ordinary jobs. This is the main shared runtime
change and is covered by the existing job/output tests and smoke checks. Recent
project activity also appears on ordinary workspace opens. The native SQLite
dependency changes package installation; configuration, CLI profiles and HTTP
profile persistence carry the opt-in setting. ChatGPT tool cards remain disabled.

Run state, documents, revisions, claim generations and operation receipts live in
the new work modules; the existing job manager remains responsible for process and
log lifetimes. No external session launcher, automatic merge/push, multi-writer
execution, source eviction or production migration is introduced.
