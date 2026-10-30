# Bounded jobs and large output

`bash` waits for a command, promotes it if its call wait expires, and returns a
job ID. `start_jobs` starts background commands. `jobs` lists/collects them;
`stop_jobs` stops them. No additional MCP tools are required.

Three different limits apply:

* The default absolute job lifetime is 45 minutes. `CODEXPRO_JOB_TIMEOUT_MS`
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
server restarts. Existing version-1 job tables remain readable. New Linux scopes
have an independent incarnation and retain the original boot ID, InvocationID
and full cgroup identity before execution is granted. Terminal jobs whose scope
is not yet proven quiescent remain recoverable after restart and are excluded
from normal log/metadata expiry until that owner is drained.

SSD also uses the [host resource adapter](HOST_JOB_RESOURCES.md) for unique disk
scratch, the shared Go cache and durable whole-job cache-consumer claims. It
augments the existing scope; it does not create another process owner or change
concurrency. Historical records are not adopted into the new resource lifecycle.

## Shell environment and detached commands

Shells use a restricted environment by default. If the operator configures
`AGENTMAIL_TOKEN_FILE`, that file reference is passed through so the Agentmail
CLI can authenticate; the token itself and unrelated service credentials are
not copied into the restricted environment.

On Linux with systemd job scopes, `setsid`, `nohup` and shell backgrounding
(`&`) do not let a process outlive its managed job. Descendants stay in the
job's cgroup even if they start a new session or process group. CodexPro drains
the entire scope when the job completes, is stopped or reaches its deadline.
Redirecting output to a file does not change that lifetime. Use `start_jobs`
and collect results with `jobs`; do not detach long checks to bypass the job
maximum. Without systemd scopes, process-group cleanup cannot guarantee that
a daemonized descendant is stopped; detaching is not a supported persistence
contract in either mode.

## Changing operational limits without a restart

After installing a release with reload support, edit the service's existing
`~/.config/codexpro/env` and run:

```sh
systemctl --user reload codexpro.service
journalctl --user -u codexpro.service -n 20 --no-pager
```

The unit sends SIGHUP to the launcher, which forwards it to the HTTP process.
HTTP and stdio servers use the same reloader in the config module. Outside
systemd, send SIGHUP directly to the server or launcher PID. The file defaults
to `~/.config/codexpro/env`; set `CODEXPRO_ENV_FILE` at startup for another
absolute path. The file is read only on SIGHUP, so incomplete edits do not
trigger reloads. Use an atomic file replacement when an editor supports it.

The reloadable settings are:

| Environment key | Effect |
| --- | --- |
| `CODEXPRO_JOB_TIMEOUT_MS` | Absolute maximum for newly launched jobs. |
| `CODEXPRO_MAX_JOBS` | Server background capacity; total active capture admission is twice this value. |
| `CODEXPRO_MAX_JOBS_PER_WORKSPACE` | Workspace background capacity and commands per `start_jobs` call. |
| `CODEXPRO_MAX_JOB_OUTPUT_BYTES` | Capture allowance for newly launched jobs. |
| `CODEXPRO_JOB_RETENTION_MS` | Finished-output retention age. |
| `CODEXPRO_MAX_RETAINED_JOB_BYTES` | Finished-output storage budget. |
| `CODEXPRO_MAX_JOB_HISTORY_PER_WORKSPACE` | Background/promoted history count. |
| `CODEXPRO_MAX_OUTPUT_BYTES` | Response excerpt budget. |
| `CODEXPRO_BASH_TIMEOUT_MS` | Default foreground call wait for new commands. |
| `CODEXPRO_MAX_BASH_TIMEOUT_MS` | Maximum foreground call wait. |
| `CODEXPRO_AUDIT_RETAIN_ACTIONS` | Journal/timeline count per stream; default 350. |
| `CODEXPRO_AUDIT_MAX_BYTES` | Global journal byte budget. |

Startup and reload share one table of numeric defaults and bounds. Every
specified reloadable value must be a nonempty integer within those bounds.
An invalid value or malformed/unreadable file rejects the entire numeric
update and keeps the previous limits. Omitted keys keep their current values.
Assignments support systemd EnvironmentFile quoting and backslash continuation;
no shell commands or variable expansion are evaluated. Other keys (including
ports, tokens, paths, permissions and tunnel settings) are ignored and logged
by name only; changing them still requires a restart.

Running jobs keep their original deadline and capture allowance, including
foreground jobs promoted after a reload. Lower capacity blocks new admission
without stopping existing jobs. Promotion only changes collection for an
already admitted job, so active background counts can temporarily exceed a
quota until those jobs finish. A successful reload immediately reapplies
finished-log and journal retention while preserving active jobs, leased logs
and unresolved resource owners. Lower retention can permanently discard old
history. Dashboard counts update on its next refresh; removed history cannot
be recovered by raising the limit later.

`server_config` reports effective values in `reloadableLimits`, plus the
current `maxCommandsPerCall`, `maxActiveCaptures` and Bash wait limits.
Existing MCP sessions use current limits: advertised input schemas retain the
supported hard bounds, with current limits checked when a command is called.
The reload log lists numerical changes, ignored key names or a rejection;
credentials and ignored values are never logged. The systemd reload command
acknowledges signal delivery, so check the log or `server_config` to confirm
that validation succeeded.

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
