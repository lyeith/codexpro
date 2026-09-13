# Bounded jobs and large output

`bash` waits for a command, promotes it if its call wait expires, and returns a
job ID. `start_jobs` starts background commands. `jobs` lists/collects them;
`stop_jobs` stops them. No additional MCP tools are required.

Three different limits apply:

* The default absolute job lifetime is 25 minutes. `CODEXPRO_JOB_TIMEOUT_MS`
  is operator-configurable from 10 seconds to six hours. Polling, reconnecting
  and promotion never reset the original deadline. Termination has a bounded
  escalation grace period.
* `CODEXPRO_MAX_JOB_OUTPUT_BYTES` defaults to 8 MiB combined stdout/stderr for
  foreground and background commands. Exceeding it stops the command with
  `stop_reason=output_limit`. Rendered views have a separate ceiling of twice
  this capture allowance. New commands are limited to twice the configured
  background server capacity, including foreground work, to bound active capture.
* MCP replies are plain text and structured data, with no tool-card widgets.
  Excerpts are much smaller: job pages default to 16 KiB and allow up to
  24 KiB combined text. Multi-job replies share an aggregate budget. The
  encoded jobs response is capped at 192 KiB, including metadata and escaping.
  `maxOutputBytes` bounds excerpts; it no longer kills a foreground build.

The detached Node runner owns the deadline, output capture and termination
escalation independently of the MCP process. On Linux under systemd it runs
in a separate scope. Persisted completion records retain stop reasons across
server restarts. Existing version-1 job tables remain readable; jobs started
by older releases keep their legacy supervision until they finish.

## Inspecting output

```javascript
jobs({workspace_id, job_ids: [job_id], output: "none", wait_ms: 0})
jobs({workspace_id, job_ids: [job_id], output: "incremental", max_bytes: 16384})
jobs({workspace_id, job_ids: [job_id], output: "incremental", cursor: next_cursor})
```

Status receipts report captured bytes, available rendered bytes, returned
bytes, expiry and whether output is growing before the excerpt. `none` returns
status and sizes without log text. `incremental` accepts one job and returns
separate stdout/stderr, `next_cursor`, `has_more` and `output_complete`.
An empty page from a running job means caught up, not finished. An incremental
wait defaults to zero; a positive `wait_ms` waits for output, completion or
server drain. Completion `wait_for=all/any` remains available in ordinary
collection, whose default wait is 30 seconds.

Keep cursors unchanged and scoped to the same workspace/job. Independent
clients can read their own cursors. Incremental and metadata-only reads do
not acknowledge completion. Legacy `full_output=true` still requests a bounded
head; use `output=tail` for the ending. Neither means the whole retained log.

In full Bash mode, `output_files` supplies shell references on the execution
host. They point to rendered views outside source repositories. The variable
`CODEXPRO_JOB_OUTPUT_DIR` is injected into that workspace's commands even with
environment inheritance disabled. It is not a laptop path or the raw job spool.

```javascript
bash({workspace_id,
  command: 'grep -n -C 3 -m 20 "FAIL" "$CODEXPRO_JOB_OUTPUT_DIR/job_ab12cd34/stdout.log"',
  input_job_ids: ["job_ab12cd34"]})
```

Use actual returned IDs. `input_job_ids` validates and pins referenced logs
for the analysis command's bounded lifetime; it is also accepted per command
in `start_jobs`. No shell parsing guesses dependencies. Expired references
fail before launch. Available shell tools such as rg, grep, sed, awk or scripts
can inspect large files without sending them through MCP. Filtered output is
itself bounded. Source `read/search` size limits and edit provenance are unchanged.
Safe Bash configurations use MCP pages rather than unrestricted shell filters.

All views use complete-record UTF-8 decoding and the existing best-effort
redactor before exposure. Unterminated records are held until completion;
records over 1 MiB or containing non-text data are explicitly omitted. Rendered
logs are diagnostic text, not guaranteed byte-identical or valid structured
data after redaction. Completed views are stable; running views grow. Raw
capture files and private job metadata are not returned as inspection paths.

## Retention and operations

Finished logs target 24 hours (`CODEXPRO_JOB_RETENTION_MS`, 1 second–7 days),
with 50 background/promoted records per workspace
(`CODEXPRO_MAX_JOB_HISTORY_PER_WORKSPACE`, 1–200). Foreground records use a
separate allowance, at most 20 per workspace. The default finished-log disk
budget is 512 MiB (`CODEXPRO_MAX_RETAINED_JOB_BYTES`, 64 KiB–2 GiB), including
capture and rendered views. Pinned data is reserved first; the remaining
budget is split 75% background and 25% foreground. Input-lease admission is
bounded by the retained budget. Active capture has its own bounded capacity.

Count/storage pressure can expire logs earlier than the age target. Active
logs and leased inputs are preserved. Bounded tombstones distinguish
`job_output_expired` from `job_not_found`. Routine source operations do not
count as proof that all output has been read. `server_config` reports the
effective operator limits. Polling creates no indefinite retention pin.

Rehearse upgrades on separate job storage. `scripts/job-restart-smoke.mjs`
uses a disposable systemd service and scopes to test server disappearance.
Do not start a second manager against the live job directory. Rollback must
account for in-flight runners; never restore an old job table over newer jobs.
