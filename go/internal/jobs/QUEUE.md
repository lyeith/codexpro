# Opt-in background capacity waiting

`start_jobs` keeps its existing reject-on-full default. A caller that wants the
connector to retain a waiting command uses `queue_jobs`, with the same workspace and command shape:

```json
{
  "workspace_id": "the-existing-workspace",
  "commands": [{"command": "the-ready-script"}],
  "queue_key": "stable-outcome-and-invocation-key",
  "queue_timeout_ms": 3600000
}
```

The response returns durable job IDs immediately. `queued` means accepted but not
executing. Collect and cancel those IDs through `jobs` and `stop_jobs`; do not
submit the command again with a new key. `jobs.all_finished` stays false while
any requested job is queued or running. `running_count` counts actual admitted
work; `queued_count`, when present, counts waiting work.

The key is scoped to the existing `sameCaller` credential/principal/agent identity
and workspace. Exact key/payload retries return the same IDs, including terminal
IDs. A changed payload refuses. In queued mode, distinct keys deliberately
represent distinct submissions; the key, rather than command-text coincidence,
defines the retry identity. The original default-mode running-command dedupe is
unchanged. Receipts advertise `queue_key_expires_at`; keys are retained at least
through their queue deadline plus the existing job-retention interval. Do not
reuse an old key after that interval. This is bounded retry protection, not an
exactly-once guarantee for arbitrary shell commands.

A submission is persisted atomically with its input-log pins. Pending jobs have
no process, execution grant, systemd scope or host-resource allocation. The queue
is bounded to four times the configured execution limits globally and per
workspace (48/24 at the normal 12/6 settings), and 1,000 retained queued-submission
job receipts. A full bound refuses the whole new submission. Encoded submission metadata
(including retained terminal rows and pending completion receipts) shares the
existing `maxRetainedJobBytes` budget with protected input logs. The whole new
batch must fit before any job is accepted. Unfinished/unsettled keyed records
also reserve their future completion copy and lifecycle growth bounded by the
existing process metadata limits. Settled terminal rows count their actual stored
bytes; unused reservation is released without dropping their retry receipt.
Exact key retries remain readable
even when the count or byte budget is full. Normal output pruning uses only the
remaining budget and never evicts protected or pending inputs to fit metadata. Waiting defaults to
one hour and can be set between one second and 24 hours. Waiting does not spend
the existing execution timeout.

The jobs service dispatches the oldest eligible command under its existing
admission mutex. A workspace at its execution limit does not block an eligible
other workspace. A later immediate background submission cannot take a slot
from older eligible queued work. Active-capture, pending-audit, retained-input,
execution and host-resource safeguards remain in force. Queue mode grants no
stack, test, business-command or fixture permission beyond the submitted command.

Before dispatch, the resolver and canonical cwd must still identify the captured
workspace/path. Changed or missing paths fail without executing. The existing
launch owner is then used with a durable running/prepared transition before host
allocation. It retains its process grant and settlement protocol. A restart may
continue work that is still durably `queued`; launched, prepared or uncertain
executions never return to the queue. Cancelling or expiring queued work creates
no control file or signal and releases its input pins.

The additive `queue_jobs` descriptor inherits the same workspace routing and
permissions as `start_jobs`, including managed profiles. `start_jobs` has no queue
mode or alias. Older services refuse the unknown tool before launching anything;
never fall back to `start_jobs` after an uncertain `queue_jobs` response. The
installed Agentmail and named-caller identity implementation is retained. Old
record rewrites can discard terminal key metadata: retain original IDs and prefer
a forward repair once queued submissions have been accepted.

No live service change is part of this source candidate. ROOT owns the connector
cutover and actual live acceptance. Once queued records exist, an older binary
does not understand them: drain/cancel them through this version before rolling
back, or keep the capable version installed while they settle. Do not replay them
through an older service or a separate shell/workspace queue.
