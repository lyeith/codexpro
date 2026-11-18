# D176: Agentmail resolves pending-notice labels

This is the current source transition for row 894. The earlier D169/D171
release documents and JSON examples describe the retained historical releases;
do not use their binding-list setup for this candidate.

## Contract

CodexPro retains only its trusted local Agentmail socket setting. Every eligible
named system call supplies its exact, case-sensitive attribution label to:

    GET /v1/mailboxes/pending-summary?attribution=LABEL

It forwards the current request's bearer and credential-registration pin.
There is no CodexPro label list, target mailbox parameter, logical-recipient
parameter or registration cache. Existing credential, restricted-identity,
cancellation, two-second lookup, 4-KiB response and no-retry boundaries remain.

Agentmail resolves the current canonical mailbox's logical registration or an
existing exact metadata-only delegation on each request. A valid unknown label
returns HTTP 200 with exactly count=0 and latest_sender="". A positive response
contains the same two fields; the notice renders the call's label, count and
canonical latest sender. No original body, message identity, receipt, ACK,
send or lifecycle capability is included. A label is attribution within the
existing trust model, not verified conversation identity.

New logical registration within existing authority is the only per-session
step. It needs no CodexPro edit, release or restart. A newly registered foreign
mailbox does not acquire an implicit cross-mailbox grant. Those grants remain
Agentmail authority under ROOT's review.

## Staged compatibility

The new Agentmail endpoint retains its old explicit address/logical-recipient
mode for the existing CodexPro release and ordinary owner consumers.

| Agentmail | CodexPro | Result |
| --- | --- | --- |
| Existing | Existing | Existing explicit notice contract |
| New | Existing | Existing contract remains supported |
| New | New | Dynamic registration and two-field summaries |
| Existing | New | Lookup refusal omits the optional notice; original tool results remain intact |

The last combination is not completed activation. The existing original-mail
delivery path is separate and retains its original receipt semantics.

## Tested source

The CodexPro author descends from D171 source
819dd7d9d649a47fba7a0d5c61c099118b56bad2; its starting HEAD
32002c70936b365edced5c0f73e639b2511d3226 adds only the previous release handoff.
Canonical Go 740c936319e7ba6f446b00b62e78370be60f6397 is also an ancestor.
The older TypeScript deployment's REVISION file is not this Go release selector.

Whole configuration and transport packages passed in job_c131c517:
config 0.656s, transport 7.219s; outer operation 76.061s, exit 0,
2026-11-17 22:53:10.483–22:54:26.544 UTC. The durable log is
coordinator-evidence/row894-release-candidate-20261117/codexpro-d176-packages-01.log.
These controls cover an arbitrary new label on the same MCP server/session,
unchanged configuration, unrelated-label and named-identity isolation, strict
metadata validation, original results, errors and original-delivery receipts.

The paired Agentmail author's 16 top-level tests and 30 subtests passed in
job_9873bc60, including actual logical registration against one running HTTP
server, real owner pull clearing, unchanged message/delivery/session state,
hot grant removal and refusal of unrelated capabilities. Log:
coordinator-evidence/row894-release-candidate-20261117/agentmail-d176-focused-01.log.

Published commit/tree identities and their readbacks belong in the coordinator
handoff; this file does not substitute for live installation evidence.

## ROOT activation continuation

1. Review the exact published CP/AM diffs, source identities, tests and current
   AUTH/custody state. Retain all accepted or unknown jobs and existing
   conversations. The source change does not grant a blanket service restart.
2. Stage and activate the new Agentmail implementation first through the
   protected release procedure. Confirm existing explicit consumers still
   work while the previous CodexPro release is selected.
3. Select the matching CodexPro implementation through ROOT's supervised
   activation boundary. Preserve the existing named policy, current socket,
   original-delivery configuration and resource limits. The retired
   --pending-mail-bindings CLI flag must not be supplied to the new binary.
   A retained CODEXPRO_PENDING_MAIL_BINDINGS environment value is ignored.
4. Once the pair is live, remove CODEXPRO_PENDING_MAIL_BINDINGS and the obsolete
   binding environment/drop-in. Retain CODEXPRO_PENDING_MAIL_SOCKET in the
   approved canonical instance configuration; removing the old file must not
   accidentally remove the socket setting or unrelated environment.
5. Prove the no-restart contract with an authorized disposable logical label
   registered only in Agentmail after the new CodexPro server is already
   running. Record the unchanged server identity and source/configuration,
   the next named call's count/latest-sender notice, an unrelated label's lack
   of notice, and clearing after the actual recipient pulls its mail.
   Preserve the ordinary result and original receipt evidence.
6. Clean up only the owned temporary label/message fixture after the owner
   settles its mail. Preserve all unrelated registrations and current lifecycle
   decisions. AP2's restart under ROOT g4ty2p4r/17672 does not itself authorize
   pending-summary access. Resolve any separate summary delegation from ROOT's
   current explicit approval; never infer it from a lifecycle change.

ROOT owns the actual live registration, authority decisions and release
selection. No source-level test is reported as completed live proof.

## Recovery

The old Agentmail explicit API remains supported by the new Agentmail release.
ROOT can therefore recover the prior CodexPro selector without rolling back
Agentmail, using the retained prior release's exact routing environment.
Historical binding material is kept in Git and coordinator evidence for that
recovery; it is not a runtime configuration input for the new implementation.
No automated recovery command stops another owner's job or conversation.
