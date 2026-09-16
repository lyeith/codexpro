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
   the current `expected_revision`, claim credential and `request_key`. Include
   `documents[]` to save multiple notes/decisions with that same update.
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

## Consolidating updates and verification

One checkpoint, plan revision or iteration finish can include up to 12
`documents`, alongside `todos` and the handoff. Each document takes `title`,
`content`, optional `kind`, `todo_ids` and `reference_path`. To update an existing
document, supply its `document_id` and current `document_revision`. Omitted `kind`
preserves an existing document's kind. New todo references can use the todos
supplied in this update. The whole update checks one run `expected_revision` and
commits once: a stale document revision, invalid reference or storage limit rolls
back every document, todo and handoff change. The result includes the saved
document IDs and revisions. `put_document` remains available for a single note.

To combine source work and its progress update, use a managed serial batch:

```json
{
  "workspace_id": "<workspace from work_claim>",
  "execution": { "attempt_token": "<current claim>", "operation_key": "packet-3-edit-verify" },
  "operations": [
    { "id": "change", "tool": "write", "args": { "path": "new-file.txt", "content": "ready\n" } },
    { "id": "verify", "tool": "bash", "args": { "command": "test -s new-file.txt" } }
  ],
  "checkpoint": {
    "expected_revision": 4,
    "summary": "Added and verified the file.",
    "next_action": "Review the next packet.",
    "documents": [{ "kind": "decision", "title": "File contract", "content": "The file must be nonempty." }]
  }
}
```

Use the actual current revision. Prefer tagged `edit` for an existing file.
Checkpoint validation runs before child effects and again when committing.
The final checkpoint runs only after **every selected child succeeds**, including
completed, quiescent Bash verification. Failure, timeout or unfinished verification
returns `checkpoint.status="skipped"`. With `continue_on_error` on a read-only
batch, any failed read still skips the checkpoint. A resumed stored suffix checks
only its selected operations; include all verification needed for the checkpoint.

Source operations are not transactional: successful edits remain if a later child
or checkpoint fails. A concurrent run update can cause the final checkpoint to
return `status="failed"`. Inspect `work_status`, then use a corrected `work_update`
with the current revision and a new request key. Do not rerun successful edits to
repair a metadata conflict. Retrying the exact batch with the same operation key
returns its durable receipt, including the checkpoint outcome; large child returns
may be omitted from this replay. A process crash with an uncertain operation still
requires the normal recovery/reconciliation workflow.

The batch inherits run identity, claim and checkpoint request key from its outer
execution context. Nested credentials are rejected. If Bash session authorization
is configured, pass `session_id` on the outer batch. Saved batch files contain only
validated child operations, never these credentials or the checkpoint payload.
Supply the checkpoint and credentials anew when resuming a saved file. Claiming,
recovery, iteration finish and whole-run acceptance remain explicit work calls.

Activity records resolve work actions through the authenticated run's actual
project/workspace. Server-wide discovery has its own Server lane. Metadata includes
action/run/document identifiers and counts, not credentials or memory text.
Older work events without reliable attribution appear as Unattributed in the
dashboard; their stored history is not guessed or rewritten.

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
a current claim's idle allowance. Claims have no absolute duration limit.
Repeated heartbeats without recorded progress still appear as a suspected stall.
An admitted operation is active work and refreshes the idle allowance on return;
background jobs alone do not renew an absent worker's ownership. Their individual
deadlines remain finite, and an expired claim is fenced before another can write.

## Clock and stop policy

There is no cumulative run time limit. A run can span many worker sessions;
server-measured elapsed time remains available as reporting and Ralph guidance.
It never prevents a claim, checkpoint or completion, and does not shorten jobs.
Final acceptance checks each use the configured job deadline, independently of
time spent on previous packets.

The default idle allowance is 10 minutes. There is no claim-duration or iteration
count ceiling. Three iterations without detected source/todo/plan progress produce
`health.state="no_progress_advisory"`; this does not prevent claims or checkpoints.
Explicit failed/blocked outcomes, pause/cancel, unresolved effects and live competing
writers still require attention. `CODEXPRO_WORK_MANAGEMENT=0` disables
run-management mutations for a worker deployment. A shared unrestricted connector
credential cannot distinguish a human manager from an agent; this is not a
separate human-approval identity.

Existing stored time/count/no-progress caps are retired automatically at coordinator
startup, with an event recording the former values. Measured time, documents, workspaces,
attempt counts and explicit blocked/paused states are preserved. A run that an
agent explicitly marked blocked still needs `resume` after reviewing its blocker.
Time/count policy fields are `null`, meaning unlimited, and
`limits.no_progress_policy` is `advisory`. Legacy `revise_limits(active_ms=...,
max_attempts=...)` requests explicitly report these inputs in `ignored_fields`;
they cannot create or silently clamp a work budget. `reset_no_progress` remains
an optional acknowledgement of the advisory counter.

The control store advances to schema 3 when retiring these caps. Older binaries
refuse that store because their job admission requires the removed numeric field.
Back up the database before upgrade; a binary-only rollback is unsupported.
Restore a quiescent pre-upgrade backup only when no later work would be lost, or
perform an explicit compatible downgrade that preserves the newer work records.

**Only `ralph` mode** receives continuation guidance. CodexPro measures elapsed
claim time using its monotonic clock. Consecutive packet claims can carry the
server-issued `session_token` to aggregate one worker session's measured time.
A fresh worker omits that token and gets a separate clock. At less than 30 minutes,
with useful work available, the response recommends another
packet. At 30 minutes or above it does not. Manual runs have no continuation hint.

Time claimed by the model or caller is never used. Wall-clock jumps cannot make
the target appear reached. Restart preserves previously measured time, discloses
a continuity gap, and does not guess downtime. Status polling doesn't renew
ownership or add extra time beyond elapsed claim time. Completion, blockers,
and stop requests override continuation. Never wait or invent work to
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

Current settings are `CODEXPRO_WORK_IDLE_MS`, `CODEXPRO_WORK_MAX_DOCUMENT_BYTES`,
`CODEXPRO_WORK_SOURCE_MAX_BYTES`, `CODEXPRO_WORK_PACKET_BYTES` and
`CODEXPRO_WORK_SWEEP_MS`. Former `CODEXPRO_WORK_ATTEMPT_MS`,
`CODEXPRO_WORK_MAX_ATTEMPTS`, `CODEXPRO_WORK_MAX_ACTIVE_MS`,
`CODEXPRO_WORK_MAX_RUNS`, `CODEXPRO_WORK_MAX_DOCUMENTS` and
`CODEXPRO_WORK_MAX_RUN_DOCUMENT_BYTES` are ignored.

Retained runs, request/operation receipts, document counts and aggregate revision
bytes have no lifetime admission quota. Managed run checkouts do not consume the
ordinary workspace-count allowance. History remains on disk; operators must
monitor actual disk capacity and back up the control store. Nothing is silently
deleted to make space. User-authored documents retain a per-document byte bound;
split large notes across documents. Generated specification/handoff/evidence
documents do not acquire a hidden aggregate-plan bound.

`todos` and `acceptance` replace a list within one bounded request. For larger
plans, use `todo_updates` (up to 200 per call) and, in a planning claim,
`acceptance_updates` (up to 50 per call). Updates merge by stable id, preserve all
other items, and commit atomically with the checkpoint. Duplicate update ids or
invalid references reject the whole update. Read large plans through paged status
sections. These per-call bounds do not limit the total plan.

See [the cap audit](WORK_CAPS_AUDIT.md) for the remaining process, transport,
storage and certification bounds, including the source-fingerprint limitation.
