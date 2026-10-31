# Private telemetry contract lane

This paired lane complements the main MCP and process suites. It runs the same
usage, inbox, audit, and activity workflows against the release TypeScript
instance on **7211** and the Go instance on **7210**. It imports the shared
`contract_suite.Client`; it does not import or launch either server.

The runner rejects port 7200 and protected live state/project paths. Before its
first HTTP mutation it verifies the supplied server PID, owner, process start
ticks, ownership of the listening socket, exact fixture binding through an MCP
read of a nonce, and ownership of the declared usage/inbox SQLite file
descriptors. It never opens SQLite itself. Its direct filesystem reads are
limited to the explicit approval artifact, proc metadata and declared private audit segment sizes; direct writes
create only nonce-named fixture files and evidence.

## Files

Copy these files beside the existing `contract_suite.py` on ssd:

- `telemetry_contract_suite.py`: private guards, bounded raw evidence, strict
  clock/identity normalization, runner.
- `telemetry_contract_cases.py`: paired requests and behavioral assertions.
- `test_telemetry_contract_suite.py`: guard tests against accidental masking or
  live access.
- `telemetry_approved_deltas.py` and `test_telemetry_approved_deltas.py`: the
  explicitly approved, narrowly scoped HTTP 413 comparison and its guards.
- `evidence/http413-approval.json`: the exact observed coordinator ruling.

Use a fresh usage store and inbox store on both private instances. Run this lane
before importing a work archive, and run it sequentially with other mutation
lanes on the fixture project. Earlier retained audit actions and completed jobs
are allowed; the audit store must be gap-free with at most 500 retained actions,
and there must be no active private jobs or work claims. Both projects must have
the same ID/label and separate initialized Git roots.

The fresh-store check occurs before the mutation phases. A failed fresh-state
precondition stops the lane. A failed later phase is recorded as failed; it is
not converted to a successful skip. Rerunning requires fresh private telemetry
stores. Do not remove or reset the live stores to satisfy that prerequisite.

## Launch manifest

Each launch writes its own explicit manifest. Required fields:

```json
{
  "port": 7210,
  "pid": 12345,
  "process_start_ticks": 12345678,
  "state_root": "/home/spite/.codexpro-go-proof/contract-go",
  "token_file": "/home/spite/.codexpro-go-proof/contract-go/token",
  "fixture_root": "/home/spite/.codexpro-go-proof/contract-go/fixture",
  "project_id": "contract",
  "usage_database": "/home/spite/.codexpro-go-proof/contract-go/state/usage/usage.sqlite",
  "inbox_database": "/home/spite/.codexpro-go-proof/contract-go/state/inbox/inbox.sqlite",
  "audit": {
    "format": "segments",
    "path": "/home/spite/.codexpro-go-proof/contract-go/state/audit"
  }
}
```

The numbers above are illustrative. `pid` is the actual listening Node or Go
process, not a launcher or shell PID. `process_start_ticks` is integer field 22
of that process's `/proc/PID/stat`. The runner independently verifies it.

The TypeScript manifest uses `port: 7211` and
`audit: {"format": "jsonl", "path": "<private audit/tool-calls.jsonl>"}`.
All usage/inbox/audit paths must resolve beneath the declared private
`state_root`. The token file must match the explicit CLI argument; its contents
never appear in argv or evidence. Optional `principal_id` further verifies the
static HTTP identity observed in a reset/answer receipt.

Usage and inbox may initialize lazily. The runner first issues their read-only
GET routes, then checks the listening PID's open descriptors against
`usage_database` and `inbox_database`. If either file is absent or owned by a
different process, no POST is sent.

## Run

From the remote worktree, using the real private manifest and token paths:

```bash
nice -n 10 timeout 240s python3 docs/go-rewrite/telemetry_contract_suite.py \
  --reference http://127.0.0.1:7211/mcp \
  --reference-token /home/spite/.codexpro-go-proof/contract-ts/token \
  --reference-manifest /home/spite/.codexpro-go-proof/contract-ts/launch.json \
  --candidate http://127.0.0.1:7210/mcp \
  --candidate-token /home/spite/.codexpro-go-proof/contract-go/token \
  --candidate-manifest /home/spite/.codexpro-go-proof/contract-go/launch.json \
  --approval-file docs/go-rewrite/evidence/http413-approval.json \
  --output /home/spite/.codexpro-go-proof/evidence/telemetry-contract-01.json
```

The runner uses 25-second request deadlines. Its two explicit cache waits are
10.2 seconds each. The normal duration should be roughly 30–60 seconds plus
shared-host request latency; the external timeout bounds the entire lane.

Run guard tests without either server:

```bash
nice -n 10 timeout 30s python3 -m unittest discover \
  -s docs/go-rewrite -p test_telemetry_contract_suite.py -v
```

The eleven current local runner guard tests passed on 31 October 2026 in 0.007 seconds. They verify
endpoint/path isolation, the mutation gate, Unicode/credential-safe evidence,
JSON type distinctions, narrow timestamp normalization, principal consistency,
audit hash binding, and activity arithmetic/order. These are harness checks;
paired server results are a separate gate and must be reported separately.

Eight additional approval-policy guards passed in 0.022 seconds. They verify
the exact approval artifact and ruling-text hashes, fail-closed activation,
strict case scope, every captured stack frame, exact candidate HTML, unchanged
raw evidence, required headers and separate outcome reporting.

## Approved HTTP 413 delta

Registry row 31's coordinator ruling at 19:25 UTC on 31 October approves
omitting the Node/Express development stack from the usage and inbox oversized
body responses. The 413 status, HTML envelope, error class/message, content type,
CSP and nosniff remain exact. MCP and profile responses are outside the ruling.
The runner requires `--approval-file`; it verifies the complete artifact SHA-256
`298189dfea7cb066ffb05df5af35c71e9a45421d6d29f97658ff5aebd6d3a2fe`
and quoted ruling-text SHA-256
`b42a964ca18a01bb315098871683154661dd3c0b116370d9b6931333430bd4df`
before creating either private client/fixture. There is no implicit approval.

Only `usage/usage-body-budget` and `inbox/inbox-body-budget` can use this policy.
The reference must match the captured ten-frame HTML stack exactly, including
function/module/line/column order and a single shared release path. The Go body
must match the exact approved stack-free HTML. Original responses remain in the
raw gzip evidence. The normalization ledger records body hashes and the ruling.

Each case records `comparison_status` and `approved_deltas`. Report counts
separate `passed_unchanged`, `passed_with_approved_delta`, and `failed`; an
approved error case is never counted as an unchanged pass. A changed header,
message, frame, HTML shell or candidate body still fails. The original attempt-2
failures remain evidence of the defect and review decision.

## Coverage and comparison rules

### Usage

Full HTTP status, selected protocol/security headers, and every response field
are compared. Cases cover submission insertion/replay, project and mode
enrichment, atomic rollback on conflicting batches, scoped counts, stale
revisions, reset request idempotency, pending-reset conflicts, a pre-click limits
observation, failed verification, successful verification, exact preservation of
the original click boundary, original receipt replay after completion, history
order, and submissions after reset.

Strict DTO cases include explicit nulls, quoted numeric fields, unknown fields,
missing nested values, and the 128 KiB request budget. Values above JavaScript's
safe-integer boundary deliberately have no asserted status: TypeScript and Go
must agree, and any stricter Go behavior is surfaced as a real difference.

### Inbox

Full DTOs are compared through question creation/defaults/trimming, exact
publish replay, conflicting publication, pending filters, revision checks,
answers, original answer receipts, deliveries, delivery idempotency, per-consumer
filtering, answer replacement, replay after later delivery, and event cursors.
Question revision and answer timestamps must remain unchanged by delivery.
Receipt replay must not rewind the actual current item.

The lane exercises Unicode answer text, empty blocked-work strings,
null members, unknown keys, invalid URLs, query number syntax, unknown/missing
resources, and the 32 KiB budget. It checks the inbox UI's API/revision/safe-text
hooks and security headers while retaining the complete HTML in raw evidence.

### Audit

Three controlled read calls must each produce one durable action, in order,
with contiguous sequences after the measured baseline. The suite checks all
action fields and binds actor/request/transport hashes to the actual private
principal and MCP request/session. It independently checks action clock bounds,
duration arithmetic, project/workspace attribution, IDs, and result references.

List/get must agree exactly. JSON and JSONL export texts are decoded and every
exported action is compared to its retained receipt. Export UTF-8 byte lengths,
SHA-256, newlines, count, and forward cursor values are verified against the
actual export before canonicalization. The original text is preserved in raw
evidence.

Status counts, retained bounds, safe cursor arithmetic, and gaps are checked
against a bounded full retained list. Storage bytes are checked against the
declared private JSONL file or segment files. Only these independently verified
state-dependent counters are replaced by comparison markers. The three owned
reads must increase the status count/sequence by exactly three; inspecting audit
or HTTP activity must add no records and cause no deduplication or compaction.

### Activity

The full project JSON DTO is compared for controlled audited reads. Every
reported age is checked against the response's exact generated timestamp and
the original receipt time. The suite checks completion ordering, latest-time
selection, stable coverage text/flags, project URLs, absent invented work,
limits, and absent shell output for file reads. It compares route errors and
both project selectors.

A small owned job proves that metadata-only job reads and the HTTP
JSON/live/job monitors preserve the pending completion notification. The first
ordinary file read must still receive it; the next must not. Full responses are
retained, but this job scenario compares its explicitly scoped behavioral
projection because the process lane owns the complete jobs DTO contract.

Dashboard partial responses must retain all seven uniquely keyed HTML sections.
A directly created, nonce-named untracked file must be absent during the warm
10-second Git cache and present after expiry. Complete HTML is retained;
comparison covers the section protocol, security headers, and observed cache
behavior. Existing history HTML is not asserted byte-identical across two
different retained histories.

## Dynamic values and evidence

No blanket key-drop filter is used. All usage/inbox body fields remain in the
comparison. Server-created clocks are required to be exact millisecond UTC
strings within the lane's observed wall-clock window. Caller-supplied submission,
limits-check, weekly reset, and verification timestamps remain exact. The
reset's original click timestamp is separately bound across receipts and the
current counter boundary.

Private roots/workspace IDs and authenticated static principal hashes are
replaced only after explicit binding. Audit request/session/actor hashes and
time/cursor values are checked before replacement. Activity ages and times are
checked arithmetically against the original receipts. Unknown extra fields,
null-versus-omitted fields, array order, booleans, integers, and numeric
representations are preserved. The report includes the normalization ledger.

Two artifacts are created with mode 0600:

1. The requested JSON report, containing per-case result, validation errors,
   canonical responses, and a bounded unified diff for discrepancies.
2. A sibling `.raw.jsonl.gz` stream containing every recorded request and
   complete response, plus method/route and timing. It is capped at 64 MiB before
   compression; individual HTTP responses are capped at 4 MiB. Each append
   rejects either private bearer token or MCP session ID before writing.

HTTP query authentication records only the uncredentialed route and the auth
mode; the authenticated URL is never stored. The raw file uses exclusive
creation so a rerun cannot overwrite earlier evidence. Choose a new output
basename for each run.

The lane cleans up only jobs it launched and its nonce-named Git cache files.
Its tiny fixture files and private database history remain as reproducible
evidence. It never stops either server, clears existing jobs, resets live
telemetry, starts a coordinator, or executes an archived work run.

