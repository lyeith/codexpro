# Agent inbox v1

A durable question/answer exchange, independent of Ralph, ChatGPT, transport or UI.
The first renderer is `/activity/inbox`. Other producers and inboxes can use the
same JSON records and implement the small adapter protocol below. Nothing in the
wire format is a command to execute.

Machine-readable schemas: [question](inbox-question.schema.json),
[answer](inbox-answer.schema.json). These are exported from the runtime validators.

## Question format

POST `/inbox/v1/items`, using the normal CodexPro authentication:

```json
{
  "schema_version": 1,
  "id": "COM-04-delivery",
  "project_id": "twilight-dev",
  "source": "ralph-orchestrator",
  "title": "Purchase delivery when inventory is full",
  "question": "Should a paid purchase go onto the showroom floor?",
  "context": "Floor goods have ordinary cleanup/reboot lifetime.",
  "options": ["Allow floor delivery", "Refuse without charging"],
  "recommendation": "Preserve floor delivery.",
  "blocking_scope": "ticket",
  "blocked_work": ["COM-04"],
  "source_url": "https://chatgpt.com/c/example"
}
```

`id` is stable within `project_id`; source is descriptive, not authorization.
IDs use 1–160 letters/digits/`_.:-`, starting with a letter/digit. Title ≤200,
question ≤2,000, context ≤4,000, recommendation ≤2,000 characters; ≤6 option
strings of ≤500 and ≤12 affected-work strings of ≤200. `source_url` is optional
HTTP(S). Context/options/recommendation/blocked_work default to empty values.
Unknown fields are rejected, including producer-supplied answers/status.
`blocking_scope` is `none`, `ticket`, or `project`; a ticket question must not
stop independent authorized work. Routine engineering choices are not questions.

Creation is idempotent. Reposting the same question returns its current record,
including an existing answer. Reusing an ID for different content returns 409;
read the existing question first. A materially different decision needs a new ID.
Producer reposts never overwrite answers or reopen a question.

Records add `status` (`pending`/`answered`), monotonic `revision`, creation/update
UTC timestamps, optional `answer` and `deliveries`. Storage is a separate SQLite
WAL database beside the configured audit journal (`inbox/inbox.sqlite`), mode 0600.
Answers and event history survive restarts and audit-journal retention. Include
this directory in backups. The inbox uses the connector's existing single-user
trust boundary: token holders can answer. Do not distribute that token to
untrusted producers; use an adapter with separate credentials for multi-user use.

## Reading and answering

- GET `/inbox/v1/items?project_id=PROJECT&status=pending&limit=30&offset=0`
  returns `{schema_version, items, total, next_offset}`. Filters are optional;
  limit is 1–100. Pending questions sort before answered ones, newest first.
- GET `/inbox/v1/items/PROJECT/ID` returns one current record.
- GET `/inbox/v1/events?project_id=PROJECT&after=SEQUENCE&limit=100` returns an
  ordered, append-only event page and `next_after`. Persist that cursor for another
  dashboard, terminal UI, notification delivery or exports.
- POST `/inbox/v1/items/PROJECT/ID/answers` accepts:

```json
{"schema_version":1,"request_id":"operator-answer-unique-id","expected_revision":1,"answer":"Floor delivery is fine."}
```

The authenticated principal is recorded server-side. A stale revision returns
409 rather than overwriting a concurrent answer. Retrying the same request ID and
payload returns its original receipt; different content with that ID conflicts.
An answer can be updated against its latest revision; previous answers remain in
the event history. Browser cross-origin writes are rejected. GETs never acknowledge
or execute anything; all responses are authenticated and uncached.

After persisting the exact answer for its project, a consumer can POST
`/inbox/v1/items/PROJECT/ID/deliveries` with
`{"schema_version":1,"consumer":"ralph-PROJECT","revision":2}`. This is an
idempotent receipt for delivery, not a claim that the requested work is implemented.
A subsequent answer revision requires a new delivery. No receipt can acknowledge
a stale revision or unanswered question.

## Portable stdin/stdout adapter

`scripts/codexpro-inbox.py --config ~/.config/codexpro/activity-client.json`
reads one JSON request on stdin and emits one JSON result. It uses the existing
0600 curl credential config; no token is embedded in prompts or command arguments.

```json
{"operation":"publish","project_id":"twilight-dev","question":{"schema_version":1,"id":"example","project_id":"twilight-dev","source":"worker","title":"Choose scope","question":"Which next outcome?","blocking_scope":"ticket"}}
```

Other operations are `list` (same project_id), `answer` (add `id` and `answer`
payload), and `deliver` (add `id` and `delivery` payload). Another inbox adapter
can implement this protocol without changing the orchestrator or dashboard.
The current monitor adapter bounds each project to 100 total records and refuses
an incomplete page explicitly. It does not silently ignore an older question.

## Ralph integration

Configure `inbox_command` as an argv array for the adapter and
`inbox_answers_command` as an argv array for the delivery helper, e.g. SSH to
`codexpro-ralph-decider.py --apply-inbox-answers --catalog CATALOG`. The catalog
must identify the same canonical/retained worktree used for repository inspection.
The write helper can update only `INBOX_ANSWERS.json` at that registered root; it
cannot execute an answer, change arbitrary paths or escape through symlinks. It
rejects cross-project, stale and conflicting answers, preserves earlier entries,
and uses a lock plus atomic replacement. It is not exposed by the read-only MCP.

The persistent orchestrator emits a bounded `questions` array on ordinary checks,
including questions that block only future tickets. Failed posts remain in a
local outbox and are retried. Existing whole-project context requests are surfaced
when upgrading an already latched monitor. Question records should contain the
actual decision and recommendation, not a raw tool error dump.

Answers are delivered into the repository before the receipt is acknowledged.
They become context for the existing persistent orchestrator and the next Pro
worker. A new answer clears only the monitor's human-dependency latch for fresh
assessment; manual holds, canceled turns, current jobs/claims, busy tabs, identity,
freshness, duplicate-send and cooldown gates still apply. It never starts a second
executor. Delivery errors retain the question/answer and prevent false receipts.
The worker reads `INBOX_ANSWERS.json` and updates saved plans/acceptance to match
answered decisions; receiving an answer is not proof the implementation is done.

Use DECISIONS.md for concise human-readable project decisions as needed. Current
explicit answers override older proposed-only gates; keep ordinary engineering
choices with the orchestrator and reserve questions for real user choices.
