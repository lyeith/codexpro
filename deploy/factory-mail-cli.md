# Factory FP mail through CodexPro's existing CLI route

Status: operator provisioning and live acceptance are pending. This document prepares one canonical `factory/fp4` route. It does not claim that the current FP4 session has used that route.

## Selected route and authority

Use the installed Agentmail CLI from an FP4-labelled CodexPro `bash` call, with a dedicated named Agentmail credential bound to the canonical mailbox `factory/fp4`. CodexPro's `agent="FP4"` is attribution metadata. Agentmail authenticates the credential, validates its registration pin, and decides mailbox ownership on each request.

This route uses existing CLI, client, policy-writer and receipt mechanisms. It needs no new CodexPro tool, runtime release, named/static authentication change, or restart. CodexPro's optional pending-summary and original-mail attachments are separate integrations: they require a named CodexPro request and cannot be enabled by supplying an agent label to a static-token request.

A distinct canonical registration gives FP4 the requested `factory/fp4` sender and recipient. A recipient alias on `plans/sp-fleet` would retain that registration's canonical sender, so it is not the selected setup. Keep the active `plans/sp-fleet` registration, credential, pull mode, configured logical labels, aliases and consumers intact throughout this change.

## Concierge provisioning: first FP only

The concierge performs these steps against the currently selected Agentmail controller. Workers do not edit the live policy or create credentials.

1. Resolve the active `factory/coordinator4` registration and its enabled system principal from the current validated policy. Use that existing principal for FP4. If the coordinator is absent, ambiguous, disabled, or not system-owned, stop for the coordinator/operator decision; do not invent a replacement principal.
2. Confirm that agent ID `factory-fp4`, canonical address `factory/fp4` (including any existing alias), credential ID `factory-fp4-connector`, and the intended output token path are unused. If any exists, inspect and reconcile its current owner rather than overwriting, reissuing, or repurposing it. Also confirm the mailbox has no existing native binding, active managed session, or retained native submission.
3. Select the installed `plans-access` and `agentmail` executables, selected controller socket and policy path, and a new private token-file path. The token's parent directory must already satisfy the policy writer's private-directory requirements. The credential stays in operator-managed storage; record only its path and registration ID in the worker hand-back.
4. Register one ordinary system leaf and issue one Agentmail-only credential. A pull-only ChatGPT worker needs no native ID, host/repo binding, lifecycle grant, Fleet coordinator grant, mailbox alias, or change to the CodexPro connector credential. `ProjectID` is not required for a fresh send to an active system coordinator; the current mail recipient rule permits system-to-system correspondence independently of project membership.

The following variables are operator-selected values, not values derived from the request's agent label:

| Variable | Required value |
| --- | --- |
| `FP4_ACCESS_BIN` | Absolute path to the selected installed `plans-access` executable |
| `FP4_MAIL_BIN` | Absolute path to the selected installed `agentmail` executable |
| `FP4_POLICY_FILE` | Current controller's validated policy file |
| `FP4_SYSTEM_PRINCIPAL` | Existing enabled system principal of the current factory coordinator |
| `FP4_MAIL_SOCKET` | Current controller's approved Unix socket |
| `FP4_MAIL_TOKEN_FILE` | New private, operator-managed token-file path for FP4 |

After the preflight above, the concierge runs the existing strict writer commands:

```sh
"$FP4_ACCESS_BIN" validate --file "$FP4_POLICY_FILE"

"$FP4_ACCESS_BIN" register-agent --file "$FP4_POLICY_FILE" \
  --id factory-fp4 --principal "$FP4_SYSTEM_PRINCIPAL" \
  --kind leaf --address factory/fp4

"$FP4_ACCESS_BIN" issue --file "$FP4_POLICY_FILE" \
  --id factory-fp4-connector --principal "$FP4_SYSTEM_PRINCIPAL" \
  --agent factory-fp4 --device factory-fp4-cli \
  --audience agentmail --out "$FP4_MAIL_TOKEN_FILE"

"$FP4_ACCESS_BIN" validate --file "$FP4_POLICY_FILE"
```

These are separate validated operations. If issuance fails after registration, retain the partial result for inspection; do not repeat registration or replace an existing token. The writer refuses duplicate records and token-file collisions. It generates the bearer internally and does not print it.

The concierge then configures the new unbound mailbox using its exact new credential:

```sh
"$FP4_MAIL_BIN" delivery-mode \
  --url '' --socket "$FP4_MAIL_SOCKET" \
  --token-file "$FP4_MAIL_TOKEN_FILE" \
  --expected-credential factory-fp4-connector \
  --identity factory/fp4 --fixed-identity \
  factory/fp4 pull
```

Require the returned address to be `factory/fp4`, its delivery mode to be `pull`, and its native binding to be empty. A refusal or binding conflict requires inspection; it is not a reason to unbind or retire another session. No logical-label registration is needed for this dedicated mailbox.

## FP4 worker binding

The concierge hands FP4 the three approved executable/socket/token-path values, the expected credential ID `factory-fp4-connector`, and the fixed canonical mailbox `factory/fp4`. Make that approved token available to the intended execution context using the existing private credential mechanism. Do not copy bearer contents into a prompt, repository, command argument or report.

Use those explicit values in each CodexPro `bash` invocation, with `agent="FP4"` on the CodexPro call. Shell state is not assumed to persist across calls. The examples below assume the operator-supplied variables are set in that invocation. `--url ''` prevents an inherited HTTP endpoint from overriding the chosen local socket. The expected credential and fixed identity remain explicit even if environment defaults exist.

Confirm the scoped metadata first:

```sh
"$FP4_MAIL_BIN" mailboxes \
  --url '' --socket "$FP4_MAIL_SOCKET" \
  --token-file "$FP4_MAIL_TOKEN_FILE" \
  --expected-credential factory-fp4-connector \
  --identity factory/fp4 --fixed-identity
```

The named system leaf's metadata should show its own `factory/fp4` pull mailbox. `agentmail identity` is not a system-worker readiness check: its person-only route can legitimately return HTTP 403 for a valid system credential.

## One-FP live acceptance

The concierge or current factory coordinator sends one explicitly approved test message to `factory/fp4` through its existing supported route. Record the returned canonical message ID and the exact harmless body expected by FP4. Record the existing shared `plans/sp-fleet` metadata before and after this setup without pulling its unrelated pending mail.

From the actual FP4 CodexPro context, fetch only the new mailbox:

```sh
"$FP4_MAIL_BIN" inbox \
  --url '' --socket "$FP4_MAIL_SOCKET" \
  --token-file "$FP4_MAIL_TOKEN_FILE" \
  --expected-credential factory-fp4-connector \
  --identity factory/fp4 --fixed-identity \
  --to factory/fp4 --limit 10
```

Require the original canonical ID, expected sender, `to="factory/fp4"`, and complete unchanged body. CLI recipient-owned pulls use the existing read-receipt route. A returned receipt warning means the acquired body must be retained and matched by canonical ID if it appears again; it does not authorize manufacturing a replacement message. Confirm terminal `read_at` and `ack_at` through the returned receipt or the sender's supported receipt view before claiming confirmed receipt.

Prepare the authorized reply in a body file and select one stable caller dedupe key, at most 24 UTF-8 bytes under fleet STANDING. Set `FP4_REPLY_FILE`, `FP4_REPLY_KEY`, and `FP4_ORIGINAL_ID` to those exact values in the same invocation, then reply:

```sh
"$FP4_MAIL_BIN" reply \
  --url '' --socket "$FP4_MAIL_SOCKET" \
  --token-file "$FP4_MAIL_TOKEN_FILE" \
  --expected-credential factory-fp4-connector \
  --identity factory/fp4 --fixed-identity \
  --id "$FP4_REPLY_KEY" --mode next --body-file "$FP4_REPLY_FILE" \
  "$FP4_ORIGINAL_ID"
```

Require canonical `from="factory/fp4"`, the original sender as destination, and `reply_to` equal to the original canonical ID. The server derives the destination from that original; do not supply `--to` or `--reply-to`. The coordinator confirms receipt of the reply through its normal route. If the reply response is lost, reuse the same dedupe key, original ID and body; do not create a second reply key.

### Cross-lane refusal

Use an operator-approved disposable original between another already provisioned lane and its coordinator, with FP4 absent from its sender and recipient. The operator supplies only that fixture's canonical ID as `FP4_OTHER_MESSAGE_ID` and verifies its receipt state before and after this check. An unknown ID alone is insufficient proof of isolation.

From the same FP4 CodexPro context, this exact foreign read must return a concealed/forbidden result and no original body:

```sh
"$FP4_MAIL_BIN" read \
  --url '' --socket "$FP4_MAIL_SOCKET" \
  --token-file "$FP4_MAIL_TOKEN_FILE" \
  --expected-credential factory-fp4-connector \
  --identity factory/fp4 --fixed-identity \
  "$FP4_OTHER_MESSAGE_ID"
```

For a new ordinary system leaf the expected response is HTTP 404. Confirm that no receipt for the other lane changed. A foreign `--from` override also fails the CLI's fixed-identity check, but that local check alone does not prove backend isolation. Private API tests should separately cover foreign inbox, receipt and reply denials; do not use production bodies as negative-test fixtures.

## Completion and extension

Hand back the nonsecret registration tuple, selected executable identity, socket and credential-file path, and actual CodexPro call/job references for metadata, pull, reply and cross-lane refusal. Retain the original and reply canonical IDs and the coordinator's confirmation. Report fixture tests separately from live acceptance. Until the concierge provisions this tuple and those actual-context checks pass, the route remains prepared, not active.

After FP4 succeeds, provision each remaining FP with its own canonical `factory/fpN` registration and credential using the same supported mechanism. A different caller label never selects another credential automatically. Existing SP callers continue using `plans/sp-fleet` and their existing logical selections until their own migration is authorized and verified.

## Source contracts reviewed

CodexPro baseline: `dc886e0c54011f3d0e2f75a6ecdd940d0051859b`. Its relevant boundaries are `go/internal/transport/identity.go`, `logical_mail.go`, `logical_mail_delivery.go`, `pending_mail.go`, and `go/internal/config/logical_mail.go`. This document changes none of those runtime files.

Agentmail provides the reused commands in `cmd/plans-access/main.go` and `cmd/agentmail/main.go`, the strict writer in `internal/plansaccess/writer_unix.go`, and the shared CLI/MCP receipt boundary in `internal/client/pull.go`. The canonical sender and mailbox-scope checks remain server authority. Live acceptance must use the operator's selected installed versions; a source inspection does not prove activation.
