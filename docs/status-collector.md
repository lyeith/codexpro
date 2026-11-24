# Status collector and completion counters

The loopback-only `GET /status/collector` endpoint accepts the existing dedicated
status-collector credential. Ordinary MCP credentials do not gain this endpoint.
It returns scalar activity only: no command arguments, output, paths, request or
job identifiers, or principal details.

## Existing monitor

A request without a query keeps the version 1 response and exact nine SP1–SP9
rows used by the Plans status monitor. It retains the same JSON keys.
`inflight_jobs` includes accepted queued jobs in this response, so pending work
cannot look idle. Reads now use owner-maintained counters instead of audit and
job-history scans. Call timestamps begin with the current server runtime; job
timestamps also include recovered durable jobs. A label with no observed work
has a null activity timestamp.

## Completion protocol

An explicit label selects version 2:

```text
GET /status/collector?agent=FP5
GET /status/collector?agent=SP12&wait=20s&since_completed=42&epoch=SERVER_EPOCH
GET /status/collector?agent=RDP2&wait=20s&since_revision=105&epoch=SERVER_EPOCH
```

Labels use the normal case-sensitive 1–4 ASCII letter, digit or dash contract.
The response contains `version: 2`, `epoch`, `observed_at`, `health`, and
`activity`. Known activity has scope `all_projects` and one selected agent row:

| Field | Meaning |
| --- | --- |
| label | Requested caller label; attribution, not authenticated identity. |
| inflight_calls | Top-level tool calls currently inside the dispatcher. |
| inflight_jobs | Accepted jobs still running. |
| queued_jobs | Accepted jobs still waiting for capacity. |
| started_calls | Calls started during this runtime. |
| completed_calls | Calls that returned or unwound during this runtime, including failures. |
| failed_calls | Completed calls classified as unsuccessful by the existing tool metrics owner. |
| revision | Monotonic sum of call and job activity revisions during this runtime. |
| last_activity_at | Latest observed call start/finish or job activity, or null. |
| last_tool_started_at | Latest call start, or null. |
| last_tool_finished_at | Most recent call completion, or null. |
| last_tool_name | Most recently completed tool; before any completion, latest started tool. |
| last_tool_error | Boolean failure classification for the last completed call, or null. |

A completed `start_jobs` or asynchronous `bash` request does not mean its jobs
have finished. Failed calls are activity, but successful completion evidence is
the delta of `completed_calls - failed_calls`. Internal batch children and
background completion audit receipts do not add top-level call completions.

Selected-label reads are O(1) in retained call/job history. Counters update at
their existing owners, after accepted job commits, and never read audit files.
Dashboard time-window expiry and job-output retention do not reset counters.
At most 65,536 call labels and 65,536 job labels are retained during a runtime;
after capacity is reached, a previously untracked label is unknown instead of
being reported idle. Existing tracked labels remain usable.

## Waiting

`wait` is a nonnegative duration of at most 30 seconds. A positive wait requires
exactly one of `since_completed` or `since_revision`, plus an explicit agent.
The response is immediate if that counter already differs, the supplied epoch
differs, or the selected source is unavailable. Otherwise it waits on owner
notifications until the selected counter changes, the timeout expires, the
request is cancelled, or the handler closes. Unrelated agent changes do not
satisfy the wait.

Use `since_completed` for the next tool return. Use `since_revision` when
waiting for any relevant change, including a queued job starting or the final
job finishing after its submission call returned. `HEAD` never waits and
returns no body. Unknown or duplicate parameters, malformed counters and waits
over the bound are rejected; credentials are never accepted in query parameters.

## SessionPilot integration

The caller must explicitly bind one active query owner to a label. Labels are
not conversation IDs; concurrent conversations sharing a label cannot be
distinguished by these counters.

Before submitting a turn, capture and persist a known, idle baseline containing
the epoch, completed and failed counts, and selected label. Pre-existing active
work must finish before taking that baseline. Completion evidence requires the
same epoch, a sufficient successful-call delta, and all three outstanding
counts equal to zero. Keep the existing browser acceptance, final-answer,
stable-idle, approval, cancellation, error and image gates.

Counter evidence supplements missing DOM tool cards; it does not manufacture
cards or prove that the model stopped generating. Use source completion time
for ordering. Do not assign a late HTTP response's arrival time as the last tool
activity time, since final prose may already have appeared.

A server restart gives a new epoch and zero call counters. Preserve that as
unknown evidence for an already submitted turn; never subtract across epochs
or resubmit an uncertain operation. A fresh pre-submission baseline is valid
for a new turn. Authentication, network, source, or parse failures also remain
unknown and cannot cause completion or replay.

The producer is deployed by the CodexPro operator at the normal quiet boundary.
The SessionPilot owner integrates the consumer against its current canonical
source and activates it at its own safe boundary. No principals.json change is
required for the existing collector credential.
