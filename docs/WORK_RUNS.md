# Durable work runs

CodexPro can retain a run's workspace, goal, tasks, documents and operation
receipts across agent sessions. The orchestrator selects work and judges results;
the connector supplies state and tools. Ordinary project work does not need a run.

Enable with `--work on`. With workspace writes enabled, the additional tools are
`work_status`, `work_manage` and `work_update`. Read-only servers expose status.
The control directory must remain separate from project, job and ordinary
worktree storage. One server process owns a control directory.

## Workflow

1. Use `work_status` to discover an existing run, or `work_manage(action="create")`
   to record its objective, scope, tasks and acceptance checks and provision a
   retained Git worktree.
2. Open the returned `workspace_id` to load AGENTS instructions. Read the saved
   goal, handoff and relevant tasks before editing.
3. Use ordinary workspace tools. Mutations require
   `execution={operation_key:"a-stable-key-for-this-operation"}`. There is no
   claim step, worker token, lease, heartbeat or planning/execution phase switch.
4. Record progress through `work_update(action="checkpoint")`: the current
   `expected_revision`, a stable `request_key`, `summary` and `next_action`.
   Include task updates, evidence references and documents as appropriate.
5. `finish_iteration` saves a batch outcome and handoff; `finish_run` separately
   requests whole-run verification. A finished conversation alone is not a
   completion receipt.

`mode="manual"` and `mode="ralph"` remain labels for the caller's workflow. Neither
mode tells an agent how long to continue. Put timing and scope discipline in the
orchestrator's instructions. CodexPro does not start or supervise external agents.
Assigning one worker to a workspace is the orchestrator's responsibility.

## Revisions, retries and stops

Run updates require `expected_revision`. If another update wins, read the new
state and reconcile before submitting a new update. Repeating an identical
`request_key` retrieves its original receipt, including after a lost response.

Managed workspace mutations require `execution.operation_key`. Reusing that key
with the same arguments retrieves the saved result; different arguments fail.
An uncertain operation is not replayed. Inspect its receipt, source and jobs,
then use `work_update(action="resolve_operation")` with a reason. Recorded source
edits remain present after failed verification or a failed checkpoint.

Mutations are serialized within a managed workspace. This is not an agent lease:
several callers can read and update a run, and optimistic revisions arbitrate
tracker updates. A pause/cancel increments the run generation; queued effects
from before the stop cannot execute afterward.

Pause and cancellation stop running jobs and wait for quiescence. A paused run
requires explicit `resume` before further source edits. Background jobs retain
their configured deadlines. Agent inactivity does not expire a claim or stop
jobs, because agents no longer hold claims.

After a server restart, interrupted operations and non-quiescent jobs enter
recovery. Inspect retained effects before resuming. An old stored claim is
abandoned through the existing recovery path; its documents, checkpoint, job
receipts and historical iteration are retained. Existing operator pauses and
cancellations remain effective. No live store should be copied without its WAL
or a proper SQLite backup.

## Plans, documents and verification

Use `todo_updates` to upsert tasks by stable ID and `acceptance_updates` for
acceptance checks; replacements remain available for small plans. Changes to the
specification require run management to be enabled, but no planning claim.
Record the reason for scope changes in the checkpoint or a decision document.
No new mandatory acceptance fields or reviewer approval states are imposed.

A checkpoint can atomically save up to twelve `documents`, tasks and handoff.
Existing documents require `document_revision`. Generated specification and
handoff documents are versioned by the server. Notes, decisions, questions and
project memory can reference task IDs and an existing workspace file.

`finish_iteration` accepts `completed`, `yielded`, `blocked` or `failed`, and can
wait for selected `await_job_ids`. Other running jobs are stopped. A blocked or
failed outcome blocks the run; ordinary checkpoints do not release an explicit
operator pause. `finish_run_if_ready` can request verification after the batch.

The existing final verification contract remains: all tasks are done or skipped,
required acceptance checks supply executable commands, operations are reconciled
and jobs are quiescent. Checks must pass against an unchanged, complete source
snapshot. Define meaningful checks; the orchestrator remains responsible for
judging whether their evidence establishes the requested outcome. This change
does not add new acceptance enforcement.

## Reading large runs

`work_status(action="get", section="packet")` returns a briefing that preserves
objective, scope, latest handoff and exact specification/handoff document
references. Large text is explicitly excerpted. `packet.truncated_fields` names
those excerpts; use `read_document` for full text.

The briefing includes bounded task and acceptance previews. `offset` and `limit`
control these previews; each entry in `packet.pages` supplies its own
`next_offset`. If an item does not fit, its cursor does not advance. Read the full
section with `section="todos"` or `section="acceptance"`.

Other sections provide documents, source, activity, jobs, operations and historical
iterations. Documents are read using `action="read_document"`, `document_id`,
`document_revision`, byte `offset` and `max_bytes`. Follow the returned cursor;
never advance past an omitted item. `search_memory` finds retained documents.
Activity warnings disclose gaps in retained history. A gap does not prove that
no work happened.

## Batch checkpoints

A serial batch can save a checkpoint after every selected child succeeds and
verification finishes:

```json
{
  "workspace_id": "<run workspace>",
  "execution": { "operation_key": "fix-verify-save" },
  "operations": [
    { "tool": "write", "args": { "path": "example.txt", "content": "fixed\n" } },
    { "tool": "bash", "args": { "command": "test -s example.txt" } }
  ],
  "checkpoint": {
    "expected_revision": 4,
    "summary": "Fixed and verified the example",
    "next_action": "Review the result"
  }
}
```

Checkpoint data and execution keys are not persisted in batch definitions.
Failure skips the checkpoint while retaining applied edits. Repair a checkpoint
with `work_update`; do not replay successful edits with new keys.

## CLI

`codexpro work status|manage|update --mcp-url URL --args-file FILE|-` calls the
corresponding tool. Authentication uses `CODEXPRO_HTTP_TOKEN`.

The managed `loop-handoff --run-id ...` adapter uses the existing server and runs
the configured executor/reviewer as a bounded server job. `--operation-key`
identifies its durable retry receipts. The old `--claim-key` spelling is accepted
as an alias; it does not acquire a claim. The adapter saves its final handoff with
`work_update` and can replay a lost final response.

## Short run references

Run APIs accept a full ID, a unique case-sensitive `run_` prefix with at least
eight characters after `run_`, or the dashboard prefix…suffix form. Responses
return the full ID. Ambiguous or inaccessible references fail instead of selecting
a run. Short and full spellings share the same idempotent request identity.
