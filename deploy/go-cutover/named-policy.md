# Named-policy bootstrap recovery

This procedure belongs to the existing Go deployment owner. It describes the
initial named-policy recovery baseline for CodexPro and Agentmail and a bounded
disposable rehearsal. It does not install a release, operate a service, change
the connector, issue credentials, or replace the cutover coordinator.

## What is established

For the initial cutover, root selects one accepted, composed Go and Agentmail
source pair. That pair must understand the complete policy and routing floor
below. Controlled failure followed by a restart of that **same pinned pair**
over the latest state is a recovery proof. It is not an OLD/NEW binary downgrade
proof.

No genuinely distinct accepted predecessor currently satisfies the complete
floor. A deterministic defect in the baseline, an unsupported state, or an
unproved refusal is a stop-and-repair condition. Returning to the earlier live
Go build or TypeScript is not an available named-policy recovery procedure.
A future distinct release needs its own compatibility decision and an
OLD -> NEW -> OLD rehearsal; a second filename, test-only commit, or rebuilt
copy of the same runtime is not such a predecessor.

The existing `cutover.sh` is a TypeScript state-import procedure.
`rollback.sh` restores TypeScript and does not establish the guarantees here.
Do not invoke either for a named-policy Go-to-Go recovery. This document leaves
those historical scripts intact.

## Compatibility floor

The exact full source revisions and binary digests belong in the current
coordinator handback/runbook record. Do not select a source by a branch tip,
version string, `current` symlink, installed binary, or an environment variable
that an older executable might ignore.

Both policy readers must retain and enforce:

- Strict v1 fields, generated named credentials, `imported_fleet_v1`, and
  recognized-denied-before-legacy behavior.
- Revoked credentials and disabled principals/agents as retained registrations.
  Their records must not be deleted to make an older reader accept the file.
- The accepted `project_streams` reader. Routing remains separate from grants.
- Optional `mail_aliases` on an agent registration, with the same strict schema
  in both readers. Aliases preserve incoming mailbox ownership, never a second
  sender, native identity or lifecycle grant. A policy containing this field
  requires an alias-capable recovery baseline; an earlier strict reader is
  incompatible and must not be selected as a fallback.
- The confined `status-collector` audience and fresh owner-concierge binding.
  `both` does not grant collector access.
- CodexPro's exact `/status/collector` projection and Agentmail's exact
  `/v1/status/collector` projection, admitted only from an actual loopback socket
  peer; forwarding headers do not supply that authority.
- Agentmail's roles-enforced, controller-only public prefix. Its enabled mode
  requires its legacy bridge off. Public `/agentmail/mcp` and
  `/agentmail/healthz` use the existing authentication owner and require header
  credentials. Nonloopback peers cannot use unprefixed routes. The prefix does
  not expose private REST or the collector.

The fleet connector bearer and URL remain unchanged. A named device label is
not personal approval. Authentication failures, unknown credentials, a malformed
enabled policy, and stale/disabled bindings must not obtain a shared-token lane.

## State that survives recovery

Keep the latest policy and state in place. Do not restore an older database or
policy snapshot, move a production database into a fixture, run two controllers
against one state directory, or transfer resource ownership during recovery.

| Owner | Required retained meaning |
| --- | --- |
| CodexPro jobs | Original identity, credential, optional agent label, resource owner key, terminal outcome, output/capture provenance and completion semantics |
| CodexPro workspace leases | Lease v2, original owner key, repository, checkout, branch and idempotent allocation |
| Agentmail messages/events | Original named Origin or original unattributed legacy record, credential-bound retry identity, pending/read/ack state |
| Agentmail native queue | Original host, submission/client receipt and pending message; queue acceptance must not become a read or acknowledgement |
| Agentmail managed sessions | Original intent, revision, worker/native identity and readiness binding |

Imported fleet authentication creates no right to adopt historical legacy
jobs or leases. HTTP/MCP session IDs are process-local and may need ordinary
reinitialization. Lease touch timestamps and revisions may advance through
their owner. Do not demand equality of volatile session bytes as a substitute
for the retained meanings above.

## Disposable rehearsal

`named-policy-rehearsal.py` is a sequential control driver around maintained
Go builds and existing service startup interfaces. It accepts explicit source
trees, full SHAs, expected binary SHA-256 digests, test-local executable paths
and an owned mode-0700 fixture parent. It has no installed defaults, predecessor
auto-selection, release builder, service manager, provider integration or real
credential input.

The driver verifies clean source HEADs, exact main-binary digests and embedded
module paths. Present VCS metadata must name the exact source revision and a
clean worktree; contradictory or incomplete metadata fails. A maintained build
that omits VCS metadata may use the coordinator-approved binding between the
verified clean full source SHA, actual build command/CWD and expected binary
digest. The result explicitly reports absent metadata. It never infers source
identity from a version label or accepts an arbitrary binary's computed digest
as proof. Record the expected digest immediately after the checked build.
The driver rechecks it before each launch and at completion.

The test-control binaries must be built by the documented owner commands from
those runtime sources plus the reviewed fixture-control test files. Keep the
exact build command and test-control source commit in the handback.

Build into an owned private directory outside the source trees using the
maintained toolchain. Set `umask 077` before creating the fixture parent,
its `bin` directory and these executables. The parent must be owned mode 0700
with trusted ancestors that are not writable by other users. Existing generated
fixture executables must be single-link, owned and executable without group or
other write permissions; adjust only those exact disposable files if necessary,
never installed binaries or broad directory trees. In the accepted Go tree's
`go` directory:

```sh
umask 077
go build -trimpath -buildvcs=true -o "$FIXTURE_PARENT/bin/codexpro" ./cmd/codexpro
go test -c -o "$FIXTURE_PARENT/bin/workspace.test" ./internal/workspace
go test -c -o "$FIXTURE_PARENT/bin/jobs.test" ./internal/jobs
```

In the accepted Agentmail tree:

```sh
umask 077
go build -trimpath -buildvcs=true -o "$FIXTURE_PARENT/bin/agentmail" ./cmd/agentmail
go test -c -o "$FIXTURE_PARENT/bin/store.test" ./internal/store
```

If root has frozen runtime composition before integrating these new test
controls, use Go's ordinary `-overlay` build input for the three new test files
only. Map each accepted tree's
`internal/{jobs,workspace,store}/named_policy_rehearsal_test.go` target to the
reviewed SP3 test source. For Go, the targets are under its `go` directory.
Pass the same explicit private overlay file to the corresponding
`go test -overlay "$FIXTURE_PARENT/owner-overlay.json" -c ...` command.
Do not mutate root's source trees or overlay any production file. Record the
runtime SHA and test-control SHA separately.

No `build-release.sh`, installed runtime helper, private EnvironmentFile,
production state, native app-server, adapter or provider is used. The driver
passes the server and state-fixture children a new private HOME/state/TMPDIR
and an explicit environment. They use only loopback listeners, fake bearer
values, a disposable Git repository, `--no-adapters`, and an explicitly empty
CodexPro host-resource helper. Builds and binary-metadata queries use the
ordinary maintained Go resource owner and its caches; do not replace its
compiler environment with a stripped runtime fixture environment. No service
credentials or service environment files are copied.

Invocation:

```sh
python3 named-policy-rehearsal.py \
  --fixture-parent "$FIXTURE_PARENT" \
  --go-tool "$GO_TOOL" \
  --go-tree "$ACCEPTED_GO_TREE" --go-sha "$ACCEPTED_GO_SHA" \
  --mail-tree "$ACCEPTED_MAIL_TREE" --mail-sha "$ACCEPTED_MAIL_SHA" \
  --go-binary "$FIXTURE_PARENT/bin/codexpro" \
  --go-binary-sha256 "$GO_BINARY_SHA256" \
  --mail-binary "$FIXTURE_PARENT/bin/agentmail" \
  --mail-binary-sha256 "$MAIL_BINARY_SHA256" \
  --workspace-binary "$FIXTURE_PARENT/bin/workspace.test" \
  --jobs-binary "$FIXTURE_PARENT/bin/jobs.test" \
  --store-binary "$FIXTURE_PARENT/bin/store.test"
```

The control sequence is fixed:

1. Seed disposable jobs, legacy/named leases, pending mail, a native queue
   receipt and an unfinished managed-session intent through their maintained
   owners. Each control requires an owned private root and the explicit
   disposable-fixture marker. Normal package test runs skip these controls.
2. Start the accepted pair with both fixture bridges on and the public prefix
   off. Verify actual HTTP outcomes, imported fleet authentication, correct and
   wrong Agentmail credential pins, collector scope/confinement, fresh owner
   and concierge reductions, revoked/disabled credentials and invalid policy.
3. Add a revoked imported registration for the previously unrecognized shared
   fixture value. With the bridges still on, that same value must now be denied.
   Add another credential revocation and principal disable. Preserve all old
   registrations, streams and collector fields.
4. Deliberately stop only those known fixture processes, retaining the same
   fixture files. Run the state-owner controls in separate processes while
   both controllers are stopped.
5. Restart the same executable pair with the bridges off and Agentmail's
   public prefix on. The imported fleet value is unchanged. A different
   **unregistered negative-control shared fixture value** is configured so the
   off-bridge check cannot pass merely because the old value is a tombstone.
   This fixture-only control does not rotate the imported fleet bearer.
6. Recheck the new and retained refusals, collector schema, public prefix
   routing, private route preservation and latest policy bytes. Stop the
   fixture pair normally and check the same retained state once more.

Functional refusals are exact expected outcomes; the runner never retries an
auth check until it succeeds. Startup waiting is bounded, calls and bodies are
bounded, and no worker/retry/runtime budget is raised. A failed state control or
unexpected status fails the proof. Keep the original failed result, diagnose
the concrete cause, and correct only the released source/fixture scope.

The Python unit controls and the relevant maintained owner tests are separate
from the paired run. A package reopen pass by itself is not the paired process
recovery proof. An isolated race pass is not a universal race claim.

Logs and disposable fixtures stay on the originating host under the approved
SP3 evidence directory with a finite seven-day expiry. Never commit logs,
fixture credentials, copied state, inventories, archives or qualification
records to the source branches.

## Root's ordered failure recovery

All operational actions here are root-only, after prerequisite source review
and integration and the cutover runbook's admission checkpoint.

1. **Keep admissions stopped.** Record the exact source/binary pair, intended
   configuration selection, job and lease facts, and the failing checkpoint.
   An unresponsive connector is not permission for workers to auto-retry or
   refresh chats. Use the agreed DRAIN/reconnect coordination.
2. **Classify the failure before changing anything.** A transient process failure
   with a compatible baseline and supported state can use that same baseline.
   A malformed policy needs a specific authorized repair of the latest policy.
   A deterministic binary defect or unsupported state requires source repair
   and review. Do not label those cases a safe restart merely because a
   process can be launched.
3. **Preserve current authority and durable state.** Keep every existing
   revocation, disabled registration, imported fleet binding, stream and
   collector restriction. Retain queue/job/lease/session provenance. Never
   recover by deleting tombstones, copying an earlier policy/DB over the
   current state, changing the fleet bearer/URL, or enabling shared admission.
4. **Select only the validated compatible baseline.** Use the exact accepted
   source pair and binary/configuration selection recorded by root. Confirm
   no other controller owns the same state. Re-enter the runbook at its
   approved recovery point; do not call the historical TS rollback scripts.
5. **Re-establish the actual contract.** Check named admission/refusal, expected
   IDs, bridge settings, collector scope and the retained-state meanings
   above. A process health response alone is insufficient. If any guarantee
   remains unknown, keep admissions held and report the smallest repair.
6. **Recover public routing separately.** If the failure is the new Agentmail
   public path, remove its prefix ingress first, then revert only the
   prefix/bind selection through the approved operational owner. Preserve the
   named policy, state, original CodexPro route and local/SSH access. The
   Cloudflare prefix rule is not a LAN firewall.
7. **Reconnect one client at a time.** Root supplies the explicit next row note
   after zero-job census and the approved restarts. Reinitialize ordinary
   process-local sessions without refreshing/cancelling other SP chats.
   Record each runbook checkpoint before continuing.

A successful bootstrap rehearsal supports recovery of that exact same
compatible pair. It neither activates production nor establishes arbitrary
future downgrade compatibility.
