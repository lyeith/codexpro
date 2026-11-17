# Go startup and operational limits

The Go process owns its state directory and profile files. In production it runs as `codexpro.service` and shares the operator configuration in `~/.config/codexpro` with the TypeScript release it replaced: the EnvironmentFile, the project catalog and the bearer token file. It never opens a path inside the TypeScript state tree `~/.codexpro`, which stays as it was left so that a rollback finds it intact. When systemd socket-activates the process (`LISTEN_PID`/`LISTEN_FDS`), it serves on the inherited fd 3 instead of binding the configured host and port.

## Startup

Configuration precedence is explicit CLI options, process environment, the private EnvironmentFile, the private saved profile, then source defaults. The private EnvironmentFile fills the role normally served by the TypeScript launcher's process environment; only the twelve operational limits can change on a later reload.

A saved profile is selected by the canonical default-project root in the initial catalog, using the TypeScript layout `profiles/<first 24 hex characters of SHA-256(root)>.json` beneath the Go state directory. A mismatched saved root is ignored. Symlinks cannot make this profile reader leave the private directory. Credentials in a saved profile are ignored: the Go instance's separate token file remains authoritative.

A profile save does not alter an existing Config. Retained settings apply on the next Load, and explicit CLI options win. Connection/tunnel metadata can remain in the profile, but this independent Go process does not launch a tunnel or restart any service.

Bash permission is always full and write permission is always workspace. Handoff, Codex transcript sessions, autonomous work and other approved retired modes cannot be enabled through profiles, environment or flags. Inventory tier (minimal, standard or full) is distinct from permission; the retained registry determines its exposed workspace tools.

The account home and an environment-selected home are both checked for the TypeScript state tree. Checks follow existing symlink ancestors. Existing token files must be regular, private, bounded to 16 KiB and unshared by hard links. These checks run before configuration loading creates any directory.

## Named credentials

Set `--principals-file` or `CODEXPRO_PRINCIPALS_FILE` to the private Plans Access policy to opt into named authentication. The services validate the current file for each request. Missing or invalid enabled policy refuses access. The default with a configured file accepts named credentials only. An operator can explicitly enable `--legacy-bridge` or `CODEXPRO_LEGACY_BRIDGE=true` for existing trusted shared-token sessions during migration. Without a principals file the original shared-token lane continues.

Each MCP session binds to its credential, principal and registered agent. A role change on that registration takes effect on its next request. Another device initializes its own session. Existing shared-token leases keep their original connector key. Audit and job records distinguish verified identity from the optional caller-supplied agent label. Historical jobs with no identity record have no named attribution.

Phase A named credentials are for trusted operators. MCP job tools enforce the creating credential's ownership. The HTTP activity views, including `/activity/live`, `/activity/job` and `/activity.json`, expose jobs across callers and projects. The shared-UID shell, full tool inventory and administrative dashboard also remain operator surfaces in this phase. New process activation requires an independent migration decision because current MCP session IDs live in process memory.

With the granted-project read source, a person's concierge and crew use authenticated MCP with seven available tools: `list_projects`, `tree`, `read`, `view_image`, plain `search`, `activity_list` and `activity_get`. Supply an explicit workspace ID returned from the filtered catalogue for file operations, and a granted project ID for activity lists. Stored activity receipts expose the action ID, project, time, tool and status. Grants are reloaded on every request and narrowed to the registered agent's project where set. Personal registrations cannot use operator HTTP views, batch, writes or shell. System registrations and the expressly enabled trusted legacy lane retain their existing operator access. Publish individually approved repositories before onboarding people; keep the catch-all catalogue entry confined to the trusted lane.

Personal file reads cover ordinary, visible files inside individually registered Git workspaces. Links, nested repositories and private service files are excluded. Search matches literal text after secret redaction. Personal image reads return a bounded PNG rendering of the first frame of a valid PNG, JPEG or GIF, up to four million pixels. Embedded metadata and trailing file contents are omitted.

The policy's principal ID is the named actor in audit, usage and inbox records. The durable resource owner key is a separate storage identifier, `principal:<id>` for a named actor or the original connector key for legacy resources. Shared requests are attributed as legacy. A configured policy-load failure refuses the request and writes one generic server diagnostic per minute at most.

People administration uses `--plans-access-command` or `CODEXPRO_PLANS_ACCESS_COMMAND` for the absolute path of the paired local `plans-access` executable, together with `principalsFile`. This is a startup-only setting, excluded from saved profiles and operational reload. The default is empty, so People data/actions report unavailable until an operator activates the paired owner. Requests cannot select either path. People is a separate owner-only HTTP admission: a named person owner, active same-person concierge and exact `codexpro` credential audience are required on every data/action request. It does not broaden personal MCP tool or project access.

## Logical mail in tool results

This local integration is disabled by default. To enable it, an operator supplies
all four startup-only settings below together with the existing named policy.
Saved profiles and operational reload cannot select or change these settings.

| CLI option | Environment variable | Meaning |
| --- | --- | --- |
| `--logical-mail-socket` | `CODEXPRO_LOGICAL_MAIL_SOCKET` | Absolute path of the existing local Agentmail Unix socket |
| `--logical-mail-principal` | `CODEXPRO_LOGICAL_MAIL_PRINCIPAL` | Exact named system principal allowed to select logical mail |
| `--logical-mail-agent` | `CODEXPRO_LOGICAL_MAIL_AGENT` | Exact registered agent within that principal |
| `--logical-mail-mailbox` | `CODEXPRO_LOGICAL_MAIL_MAILBOX` | Exact canonical pull mailbox configured by its existing owner |

The actual request must authenticate to that principal and agent. CodexPro
forwards its current bearer and the authenticated credential's registration ID
as `X-Agentmail-Expected-Credential` to the configured local socket, with no proxy,
redirect, broader credential or caller-selected endpoint. Agentmail independently
checks current ownership and the mailbox's explicitly configured logical labels.
No policy or registration is created by a tool call.

Each `tools/call` may supply the ordinary `agent` field, for example `SP6`, to
select mail explicitly addressed to that logical label. This selection is within
the shared authenticated account; it is not separate SP authorization or proof
of which ChatGPT conversation received the response. Labels never grant task
rights and are never inherited from a previous call or transport. Missing,
unknown, malformed or conflicting labels cannot offer addressed mail. Concurrent
calls and reconnects are selected independently.

After the ordinary tool result is ready, CodexPro makes one pure offer request
for up to three pending originals. Backend validation still limits the complete
offer to ten originals, 64 KiB of body bytes and 512 KiB on the wire. The appended
text item contains at most three complete original envelopes and is at most
16 KiB, including its explanation, JSON encoding and compact remainder notice.
Existing tool content, structured fields, metadata and error state stay intact.
A valid label may also accompany a tool validation error; the invalid tool is
not dispatched. The offer has a two-second bound and observes POST cancellation.

Bodies are never truncated. If the first original cannot fit, a bounded notice
points to the existing exact-ID/exact-label Agentmail read, or a scoped list
when the backend's oversize refusal provides no ID. A notice receipts no message.
Other invalid or unavailable offers leave the ordinary result intact.

The delivery boundary is a successful complete combined-response write. For
the pinned SDK's SSE transport, CodexPro observes the matching typed JSON-RPC ID
and exact appended mail item in a whole result event, requires a full successful
write and successful subsequent flush, and only then sends receipts after the
SDK handler returns normally. Early headers, notifications, short/failed writes,
failed flushes and cancellation before or during that boundary leave originals
pending. This observation does not alter the accepted ordinary tool's lifetime.

Receipts use Agentmail's existing idempotent per-original read endpoint with the
same authenticated credential and exact logical selection. One further
two-second budget covers the whole included batch. An uncertain receipt stops
the remaining walk, leaves ordinary output unchanged and logs a generic
diagnostic; later offers recover originals still pending. Omitted IDs are never
receipted. Concurrent responses and unconfirmed receipts can repeat the same
original IDs; there is no persistent cursor, new retry queue or model ACK loop.

A successful server write does not prove that a model read, acted on or completed
the requested work, and does not promise exactly-once consumption. Generic
shared inbox pulls must still exclude logically addressed originals; matching
logical selection is required to read, receipt or reply to them.

Activate the paired Agentmail source and configure its intended pull mailbox
before enabling these CodexPro settings. Keep existing MCP conversations and
accepted work during the operator-owned deployment; a source test is not live
acceptance. Disable the optional settings to stop offering mail. The originals
remain in Agentmail and must not be deleted or handed to a generic older inbox
consumer merely to roll back the connector.

## Pending-mail notices

This independent optional integration appends one text line such as
`SP9: 2 unread, latest from people/david/concierge` to an attributed tool result
when its mail owner reports pending messages. It changes no existing tool
content, structured fields, error state, original delivery or receipt.

Set startup-only `--pending-mail-socket` / `CODEXPRO_PENDING_MAIL_SOCKET` to the
existing local Agentmail Unix socket and `--pending-mail-bindings` /
`CODEXPRO_PENDING_MAIL_BINDINGS` to a JSON array. Named authentication is required.
Saved profiles and operational reload cannot activate or change these settings.

Each binding must explicitly contain all five string fields:

```json
[
  {"label":"SP9","principal":"fixture-fleet","agent":"fixture-worker","mailbox":"fixture/fleet","logical_recipient":"SP9"},
  {"label":"AP1","principal":"fixture-mobile","agent":"fixture-app","mailbox":"fixture/mobile","logical_recipient":""}
]
```

These are examples, not live routing defaults. Install only bindings verified
against the current named access policy and the mail owner's configuration.
An empty logical recipient explicitly selects ordinary mail in that canonical
mailbox; a nonempty selection must already be configured by Agentmail. An SP,
AP or RDP label selects its configured binding only when the current named
system principal and registered agent match. A label cannot borrow another
registration's mailbox or credential. Missing, unknown, duplicate or ambiguous
labels produce no notice. The caller label remains attribution, not proof of a
particular conversation's identity.

The parsed routing lookup is cached for at most two seconds and rechecked
against the exact current configuration and authenticated identity. Counts and
senders are never cached across calls. Every eligible tool result makes a fresh
GET to `/v1/mailboxes/pending-summary`, forwarding only that request's bearer and
credential-registration pin over the local socket. The request is bounded to
two seconds and 4 KiB and follows the current HTTP request's cancellation.
There are no redirects, proxies, fallback credentials or delivery retries.

Agentmail returns only the exact address, logical selection, count and latest
canonical sender. CodexPro validates the selection and emits only label, count
and sender. Its summary path never receives originals or sends a read/ACK.
“Unread” follows the mail owner's existing pending predicate, including backlog
notice exclusion. A new message is visible on the next tool call, and a completed
owner pull clears the next notice without waiting for a cache expiry. Existing
original auto-delivery, when separately enabled, retains its own receipt rules.

Activate the paired metadata endpoint before enabling these settings. A missing,
refused, malformed or unavailable summary leaves the ordinary result intact;
it is not presented as a zero count. Deployment and restart remain operator-owned
actions that preserve existing conversations, accepted jobs and original mail.

## Reload

Source: `src/config.ts:657–768`. The parser preserves the source EnvironmentFile quoting, escape, continuation, duplicate-key and comment rules. It never evaluates shell syntax or expands variables. NUL, BOM, invalid UTF-8 and files larger than 1 MiB are rejected.

`ReloadOperationalLimits` returns `{ok, changed, ignored}` and an error. `Reload` preserves the existing Go error-only API. Missing keys retain their effective values. Unknown and startup-only keys are ignored, with names only in the report. Every supplied operational number is validated before the complete update is installed under one mutex. Invalid updates leave all values unchanged. Bash default timeout and maximum timeout are independently bounded, matching the source; there is no additional cross-field rejection.

| Environment key suffix (CODEXPRO_) | Source fallback | Minimum | Maximum |
| --- | ---: | ---: | ---: |
| MAX_OUTPUT_BYTES | 120000 | 4000 | 2000000 |
| MAX_BASH_TIMEOUT_MS | 600000 | 1000 | 900000 |
| BASH_TIMEOUT_MS | 120000 | 1000 | 900000 |
| JOB_TIMEOUT_MS | 2700000 | 10000 | 21600000 |
| MAX_JOBS | 12 | 1 | 64 |
| MAX_JOBS_PER_WORKSPACE | 6 | 1 | 32 |
| MAX_JOB_OUTPUT_BYTES | 8388608 | 65536 | 268435456 |
| JOB_RETENTION_MS | 86400000 | 1000 | 604800000 |
| MAX_RETAINED_JOB_BYTES | 536870912 | 65536 | 2147483648 |
| MAX_JOB_HISTORY_PER_WORKSPACE | 50 | 1 | 200 |
| AUDIT_MAX_BYTES | 8388608 | 4096 | 1073741824 |
| AUDIT_RETAIN_ACTIONS | 350 | 1 | 1000000 |

The live capture's 90-minute job timeout is an environment setting, not a source default. Matched private comparisons must set 5400000 explicitly; the Go default is the source's 45 minutes.

## Verification

Tests cover parser goldens, exact operational key membership, all numeric boundaries, JavaScript numeric syntax, atomic rejection, concurrent snapshots, source fallback/clamping, private profile identity and symlink boundaries, explicit CLI overrides, retired-mode rejection, live-path and listener aliases, and token-file bounds. An integration test saves a profile through the admin HTTP handler and verifies that only a subsequent Config.Load observes the change. The same test verifies that operational reload ignores startup keys and the next load applies them.

All execution and race-test results are reported by the root agent after running the staged package with the pinned Go toolchain.

