# Repository-driven Ralph monitor

`scripts/codexpro-ralph-monitor.py` watches a CodexPro project and uses SessionPilot
to continue its ChatGPT Ralph loop automatically. The repository's saved state is
authoritative. Conversations are replaceable execution sessions; a managed
`run_id` and an existing conversation URL are both optional.

The watcher polls every minute. It notifies a persistent Codex or Claude orchestration
session when ChatGPT finishes or CodexPro has been idle for ten minutes. The same
explicit provider session ID is resumed for each project's notifications. The
watcher stays resident; the provider process runs only on changed notifications,
so polling does not continuously consume model tokens. The orchestrator owns
continuity, planning, evidence reconciliation, next-work-packet selection and
completion assessment. Each continuation includes its concrete worker instructions.
The laptop orchestrator selects work; SessionPilot opens the ChatGPT Pro worker;
that worker implements and verifies directly through CodexPro. Worker instructions
must not ask ChatGPT to start another worker or delegate to SSD Codex/Claude or
AI-Bridge. Missing editor tools must be reported. Independent-review requirements
remain open until a reviewer is separately authorized. Ordinary build/test jobs
remain available. Partial todo blockers are evidence for the orchestrator, not an
automatic whole-project stop; explicit pauses and human-blocker latches still stop it.

## Setup

Requirements: Python 3.9+, curl, SessionPilot with a paired browser, and an
authenticated Codex or Claude CLI on the laptop running the orchestrator. The
context/decision helper resolves project roots from the CodexPro project catalog.
It reads bounded `AGENTS.md`, `AGENTS.override.md`, `STATE.md`, `HANDOFF.md`, and
`BACKLOG.md` excerpts, with full-file hashes and explicit truncation. It never
modifies the repository. Missing state is an error, not completion.
For retained managed worktrees, use a separate `--catalog` for both `--context`
and `--inspect`, mapping project IDs to their verified retained roots. Do not
change the live CodexPro project catalog or inspect a stale main checkout instead.

Install all three Python files together (the helpers import the monitor), then:

```sh
python3 scripts/codexpro-ralph-monitor.py init
python3 scripts/codexpro-ralph-monitor.py bind --project my-project \
  --url 'https://chatgpt.com/g/g-p-PROJECT/project' --auto-send
```

An optional project conversation URL attaches an existing tab without sending.
A Project URL alone is enough to bootstrap a new conversation from repo state.
Set these fields in `~/.config/codexpro/ralph-monitor.json` (argv arrays, no shell):

```json
{
  "schema_version": 1,
  "activity_config": "~/.config/codexpro/activity-client.json",
  "sessionpilot": "/absolute/path/to/sessionpilot",
  "context_command": ["python3", "/path/to/scripts/codexpro-ralph-decider.py", "--context"],
  "decision_command": ["python3", "/path/to/scripts/codexpro-ralph-decider.py", "--provider", "codex"],
  "targets": [{
    "name": "my-project",
    "project_id": "my-project",
    "chatgpt_project_url": "https://chatgpt.com/g/g-p-PROJECT/project",
    "enabled": true,
    "auto_send": true,
    "effort": "Pro"
  }]
}
```

The watcher, SessionPilot and Codex can all run on the laptop while the repository
remains on SSD. Prefix `context_command` with
`ssh -o BatchMode=yes -o ConnectTimeout=8 user@host python3` and use the remote
helper path. Keep `decision_command` local. No provider credentials are copied.
Use absolute executable paths in a service's config.

For deeper inspection, save a private JSON argv file containing the same remote
helper command with `--inspect` instead of `--context`. Add
`--context-command-file /absolute/path/inspection-command.json` to the local
decision command. The supplied `codexpro-ralph-inspect.py` MCP bridge gives the
orchestrator a `project_inspect` tool pinned to that project: state, Git history,
status, directory listings, paginated file reads and file diffs. The SSH helper
allows only these read operations, validates paths inside the catalog root and
caps output. It cannot run arbitrary commands or modify the remote repository.

Alternatively the decision helper can run over SSH beside the repository;
pass `--executable` if the remote provider CLI is outside noninteractive PATH.

Choose `--provider claude` to use Claude instead; `--model` is optional. Codex runs
with a read-only sandbox and shell tools disabled. Claude has built-in tools and
Chrome integration disabled. Both can use the explicit read-only repository MCP
bridge; other MCP servers are not loaded. Implementation work goes through the
guarded ChatGPT prompt plus the orchestrator's selected next work packet.

```sh
python3 scripts/codexpro-ralph-monitor.py once       # inspect, no sends
python3 scripts/codexpro-ralph-monitor.py watch --send
python3 scripts/codexpro-ralph-monitor.py status --target my-project
python3 scripts/codexpro-ralph-monitor.py hold --target my-project --reason 'Deploying'
python3 scripts/codexpro-ralph-monitor.py resume --target my-project --reason 'Deployment complete'
python3 scripts/codexpro-ralph-monitor.py new-conversation --target my-project --reason 'Fresh context'
```

`new-conversation` forgets the execution binding, preserves the repository and
send history, and requests a fresh conversation on the next eligible notification.
It does not release a maintenance hold or bypass activity checks. `resume` clears
a hold and the completion/human-blocked latch. Config changes apply on the next
poll. A competing check fails immediately on the project's lock; retry the
operator command after that check finishes.

## Decisions and execution

The JSON decision includes `schema_version`, observation `fingerprint`, `action`,
`project_status`, `reason`, `context_request`, and `next_step`. Actions are `wait`, `continue`,
`start_new`, `needs_context`, `intervene`, `complete`, and `stopped`. The model can
only choose from the current packet's allowed actions. Its bounded `next_step`
directs the worker within the existing scope; it cannot supply an executable or
bypass sending guards. `continue`/`start_new` require `project_status=active`.
Whole-project completion and a human blocker latch the monitor until `resume`.
A future policy question is not a whole-project blocker if authorized work remains.

Sends require both `target.auto_send=true` and `--send`. Before each send, the
watcher explicitly requests the configured ChatGPT `effort` (default `Pro`),
including follow-ups. SessionPilot verifies the exact picker label before Send
and fails before submission if selection is unavailable. `model` is an optional
exact model label; effort is never inherited implicitly.

Before each send, the
watcher rereads all sources and checks the fingerprint again. It requires two
fresh idle samples, no calls/jobs/claims, no busy ChatGPT turn, and no operator
hold. Defaults: 30-second settling, five-minute cooldown, six attempts per hour,
and at most three attempts without repository or CodexPro progress. Each mutation
has an idempotency key, and a per-project lock prevents competing local watchers.

An uncertain send is **not** a permanent blocker. Its attempt is recorded before
submission. Subsequent checks inspect CodexPro history/running jobs, repo hashes,
and browser state. Active work means wait. After a full ten-minute idle window,
the judge may start a fresh conversation that rereads/reconciles saved state;
it never blindly replays the previous command. Failed or uncertain browser turns
also require an idle browser and the full idle window before this recovery.
Manual stops, authentication requests and paused managed runs remain stops.

`~/.local/state/codexpro-ralph-monitor/` holds each project's latest packet,
decision, send ledger and rotating decision journal. The decider's separate
`~/.local/state/codexpro-ralph-decider/PROJECT/PROVIDER/session.json` records its
provider session ID and notification count. Files are private (0600) and directories
0700. The existing [activity curl configuration](ACTIVITY_JSON.md) supplies auth;
no bearer token is placed in a URL or command argument.

## Coverage and limits

The watcher inspects open SessionPilot bindings in the same ChatGPT Project.
Unmanaged browser tabs and work outside CodexPro are not observable. Attach a
manually started conversation when it must participate in busy detection. More
than ten open project bindings requires closing/releasing old bindings with
SessionPilot. Neither command output nor the absence of commands proves completion;
the judge uses current saved context and acceptance evidence. If bounded excerpts
are insufficient it reports the missing context rather than inventing an answer.

Only run one watcher installation per project. A filesystem lock coordinates
processes on one machine, not independent watcher installations on different hosts.
The API's optional `run_id` projection is a read-only recorded status: it does not
renew claims or recheck source acceptance.

Provider CLI references: [Codex non-interactive sessions](https://learn.chatgpt.com/docs/non-interactive-mode)
and [Claude programmatic usage](https://code.claude.com/docs/en/headless).
