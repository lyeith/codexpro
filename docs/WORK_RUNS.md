# Durable work runs

Runs are optional. An ordinary agent can keep using project workspaces without
creating a run. A run owns a retained Git worktree, specification, todo list,
versioned documents, iteration history and operation/job receipts. It does not
launch an external agent by itself.

Enable the coordinator on the existing server with `--work on`, or
`CODEXPRO_WORK_MODE=on`. `--work-dir` / `CODEXPRO_WORK_DIR` selects private storage
(default `~/.codexpro/work`). Keep it outside projects, job storage and legacy
worktree storage. All HTTP sessions share one coordinator; a second process
cannot open the same store. Keep this directory and its worktrees together in
backups. SQLite uses WAL transactions and private file permissions.
The package includes a native SQLite dependency; if a matching prebuilt binary is
unavailable, npm installation needs the platform's Node native-addon build tools.

## Agent workflow

1. `work_status(action="list")` discovers accessible runs without a predecessor's
   ID or credentials. Filter by `project_id`, `state`, `claimed` or
   `needs_attention`. `get` returns the current plan, claim, jobs, handoff and
   recent changes. Inspection does not renew a claim.
2. Create with `work_manage(action="create", request_key, project_id, mode,
   title, objective, scope, acceptance, todos)`. Explicit `mode` is `manual` or
   `ralph`. Omit `ready` to create a draft, or use `ready=true` with acceptance
   criteria. Creation pins the base commit in a dedicated retained worktree.
3. `work_claim(run_id, expected_revision, request_key, phase, worker_label,
   objective, todo_ids, check_plan)` claims one iteration. Use `phase="plan"`
   to draft/revise a plan; planning claims cannot mutate source. Execution
   claims select unfinished todos from a ready run. Open the returned
   `workspace_id` to load AGENTS.md and inspect applicable instructions.
4. Workspace mutations carry
   `execution={attempt_token, operation_key}`. Reuse an operation key only for
   the exact same call after a lost return. Different effects need different
   keys across the run, including after a session handoff. Direct calls, `codexpro`
   and batch children share admission and fencing.
5. `work_update(action="checkpoint"|"revise_plan", ...)` atomically saves todo
   revisions and a concise handoff: `summary`, `next_action`, `blockers`,
   `decisions`, `failed_approaches`, `evidence_ids`. Durable updates include
   the current `expected_revision`, claim credential and `request_key`.
6. `finish_iteration` uses the same checkpoint fields plus an explicit outcome
   (`completed`, `yielded`, `blocked`, `failed`). It saves the final checkpoint
   and closes admission together. Jobs normally stop before release; explicitly
   selected `await_job_ids` may finish under their existing deadlines, with the
   run shown as waiting. No successor writes until they quiesce.
7. `work_manage(action="finish_run", ...)` separately requests final acceptance.
   Required criteria must have server-executable commands. The coordinator runs
   those checks under bounded jobs, requires success/quiescence, and compares a
   complete source fingerprint before and after. Done todos alone never mark a
   run complete. `finish_run_if_ready=true` on iteration finish requests this
   automatically when the todo list is finished.

Commands for required acceptance criteria are part of the versioned
specification, changed only through a planning claim. They should check source,
not modify it. Fingerprints cover HEAD, index, tracked and nonignored untracked
source; generated context and batch artifacts are excluded. Ignored dependencies
and the host environment are outside this source proof. Oversized or unsupported
source observations block completion instead of accepting a partial fingerprint.
No automatic merge or push is performed; the run's branch/worktree remains for
review and integration.

## Agent death and recovery

The agent never has to revoke itself. CodexPro owns expiry and revocation.
A missing worker contact expires its claim. Server restart invalidates old
claims, marks interrupted operations uncertain, and reconciles surviving jobs.
Each claim has a new generation and secret; late calls from old attempts fail.
Coordinator ownership also lives in a SQLite transaction: a replacement process
can reclaim a dead owner's store without a stale recovery lock file. A live or
unverifiable owner prevents a competing coordinator from starting.

Command startup first persists the job and links it to its operation. A detached
supervisor waits for a grant written only after that registration. A crash before
the grant cannot execute the command. A crash after the grant leaves a recorded,
deadline-bounded job. The supervisor waits for process-group cleanup before
publishing its quiescence result. Job receipts stay in the work database even
when large log files expire from ordinary job retention.

The run remains in recovery while admitted calls or jobs are live. If a
supervisor disappears without proving quiescence, the run is visibly quarantined;
inspect server processes before repair. This is cooperative supervision on the
CodexPro host, not a security sandbox against arbitrary full-Bash programs,
escaped daemons, another same-user process, or direct filesystem edits. Systemd
scopes improve containment on supported Linux service deployments. Do not start
an unmanaged writer in a managed worktree.

After an interrupted operation with quiescent jobs, a planning claim can inspect
source and receipts, then use `resolve_operation(operation_id, resolution,
reason)` to record the observed outcome. This is an explicit reconciliation
record, not a replay or a claim of exactly-once external effects. Resume the run
after resolving uncertainty. A lost successful reply replays its stored receipt;
a too-large return provides a receipt and job/output references.

`pause`, `cancel` and `recover` revoke active claims on the server and reconcile
work. They do not delete partial source. Failed provisioning can be retried with
`recover`; it retains the run's creation identity.

Status distinguishes last contact from last progress, a suspected stall from
expired ownership, and job launch from successful completion. Heartbeats renew
only a current claim's idle allowance, never the hard attempt limit. Repeated
heartbeats without recorded progress still appear as a suspected stall.

## Clock and stop policy

The default idle allowance is 10 minutes, attempt cap 25 minutes, run cap 2 hours,
and attempt count 20. Three iterations without source/todo progress block further
execution until management records a reason and resets the no-progress limit.
Configured ceilings bound `revise_limits`. `CODEXPRO_WORK_MANAGEMENT=0` disables
run-management mutations for a worker deployment. A shared unrestricted connector
credential cannot distinguish a human manager from an agent; this is not a
separate human-approval identity.

**Only `ralph` mode** receives continuation guidance. CodexPro measures elapsed
claim time using its monotonic clock. Consecutive packet claims can carry the
server-issued `session_token` to aggregate one worker session's measured time.
A fresh worker omits that token and gets a separate clock. At less than 30 minutes,
with useful work available and budgets remaining, the response recommends another
packet. At 30 minutes or above it does not. Manual runs have no continuation hint.

Time claimed by the model or caller is never used. Wall-clock jumps cannot make
the target appear reached. Restart preserves previously measured time, discloses
a continuity gap, and does not guess downtime. Status polling doesn't renew
ownership or add extra time beyond elapsed claim time. Completion, blockers,
stop requests and budgets override continuation. Never wait or invent work to
fill 30 minutes. Ignoring a hint does not leave a closed iteration hanging.

## Memory, documents and large returns

The database generates versioned specification, current handoff, historical
iteration and final evidence documents. `put_document` adds notes, decisions,
questions and reusable `project_memory`, with an originating iteration and
optional todo references. `reference_path` registers an existing workspace
file with a source observation. Generated control documents cannot be overwritten
as ordinary notes.

`work_status(section="documents")` lists the manifest. `read_document` selects
an exact revision and uses byte offsets to return lossless UTF-8 pages.
`search_memory` performs bounded literal searches within a run; project searches
include explicitly published project memory belonging to the authenticated
principal. Historical memory is evidence to inspect, not an instruction source
that overrides project policy or the current specification.

Keep the current handoff short. Preserve original decisions and failed approaches
in referenced documents. Refresh summaries from the structured plan and relevant
original evidence, not just the previous summary. Outstanding blockers, acceptance
criteria and uncertain effects must remain explicit.

Responses report their original/returned size and truncation. Run IDs, state,
revision and clock guidance survive packet compaction. Page `todos`, `acceptance`,
`documents`, `iterations`, `operations`, `jobs` or `activity` rather than assuming a
shortened packet is complete. Source observations include their observation time;
recent activity distinguishes this workspace from other project work and discloses
capture/retention gaps. Ordinary workspace opens also show recent activity.

Large command logs use the existing `jobs` incremental cursors and retained
`output_files`. In full Bash mode, pass `input_job_ids` to pin those logs while
using `sed`, `grep` or installed `rg` against `CODEXPRO_JOB_OUTPUT_DIR`. The run
memory API does not export secrets or private claim credentials in history.

## CLI and launchers

The CLI talks to the existing MCP server; it does not open another writer store:

```sh
codexpro work status --mcp-url https://your-server/mcp --args-file status.json
codexpro work manage --mcp-url https://your-server/mcp --args-file create.json
```

`--args-file -` reads JSON from stdin. Authentication uses
`CODEXPRO_HTTP_TOKEN`; protect claim-response files because they contain private
credentials. The JSON arguments are the corresponding `work_*` tool arguments.

`loop-handoff --run-id RUN --mcp-url URL ...` adapts the existing local
executor/test/reviewer engine. It selects a packet (or `--todo-ids a,b`), claims
it, starts the engine as a supervised job **on the CodexPro machine**, heartbeats
while collecting it, and records a final checkpoint. Agent/reviewer executables
must already exist on that machine. `--claim-key` supplies a stable claim receipt
key and `--session-token` links consecutive packets. It does not invent a
fresh-session launcher or assume a particular external agent. Use `--dry-run`
to inspect selection/command without claiming. A launcher crash leaves server
recovery in charge.

The adapter keeps private request/response receipts under
`$CODEXPRO_HOME/work-clients` (default `~/.codexpro/work-clients`) so retries with
the same claim key preserve their original arguments. A new packet needs a new
key. Its final JSON includes the work-session credential for a subsequent packet;
an expired claim still requires server recovery and a new claim, not local replay
of its effects.

The legacy CLI remains available for unmanaged workspaces. The managed adapter's
executor/reviewer result closes only its packet; whole-run completion still uses
the coordinator's acceptance checks. Future native MCP Tasks and parallel writers
are outside this sequential coordinator.

## Operations and rollout

Keep the feature opt-in during rollout. Start with a disposable Git project and
exercise an interrupted agent before enabling unattended work in a real project.
To roll back, pause/drain managed runs first and retain their database and
worktrees. An older binary does not enforce these claims.

Relevant ceilings are `CODEXPRO_WORK_IDLE_MS`, `CODEXPRO_WORK_ATTEMPT_MS`,
`CODEXPRO_WORK_MAX_ATTEMPTS`, `CODEXPRO_WORK_MAX_ACTIVE_MS`,
`CODEXPRO_WORK_MAX_RUNS`, `CODEXPRO_WORK_MAX_DOCUMENTS`,
`CODEXPRO_WORK_MAX_DOCUMENT_BYTES`, `CODEXPRO_WORK_MAX_RUN_DOCUMENT_BYTES`,
`CODEXPRO_WORK_SOURCE_MAX_BYTES`, `CODEXPRO_WORK_PACKET_BYTES` and
`CODEXPRO_WORK_SWEEP_MS`. Control receipts and retained work are never silently
evicted to make a new claim fit. Capacity errors require deliberate maintenance
or a configured ceiling change. Final lifecycle evidence has reserved space so
ordinary note usage cannot consume its entire allocation.
