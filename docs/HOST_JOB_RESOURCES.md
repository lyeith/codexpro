# Host resources for managed jobs

On SSD, configure the service with
`CODEXPRO_HOST_RESOURCES_HELPER=/home/spite/.local/bin/ssd-dev` and keep
`CODEXPRO_JOB_SCOPES=1`. The helper path must be absolute. Enabling the adapter
requires working Linux user systemd scopes; a missing helper or unavailable
scope fails before the command executes. Other hosts may leave the adapter
unset. Existing job concurrency, deadlines and output budgets are unchanged.

## Ownership

The existing `codexpro-job_*.scope` is the sole process owner. Every new scope
has its own 128-bit incarnation in addition to the public compact job ID.
Before a job can launch, the host allocates a fresh resource run and registration
nonce, and CodexPro persists both in its private job record. The host helper then
activates that registration from inside the actual job scope, verifies its
systemd invocation and current cgroup, applies unique disk `TMPDIR`/`GOTMPDIR`
and shared `GOCACHE`, and executes the Node supervisor. These settings are
applied after the ordinary restricted environment sanitizer. The command,
environment restrictions, working directory, streams and redaction policy
otherwise retain their existing behavior.

The host receipt is a durable cache-consumer claim for the whole scope. It
continues to protect the cache if Node or a descendant closes an inherited file
descriptor. This adapter does not wrap the CodexPro service in a second scope.

The supervisor records the original boot ID, systemd InvocationID and full
ControlGroup before waiting for the command grant. Signals and whole-tree
quiescence checks use that exact registration. A matching scope basename or
an absent launcher PID is insufficient. Process-group completion on hosts
without systemd does not authorize host scratch cleanup.

## Completion and recovery

Command completion and whole-scope quiescence are separate facts. A terminal
job whose owner is unverified remains in the recovery poller after restart.
Its spec, result, grant, logs, scope identity and resource claim are retained
despite normal log age/count/storage expiry. The manager stops residual scope
descendants only after verifying the original boot and invocation. It then asks
the host to finish the resource registration; the host independently proves
whole-owner quiescence before deleting successful scratch or retaining failed
scratch for its configured period.

If the controller fails before activation, cancel atomically revokes the
unactivated resource registration, so a late exec cannot start. An allocation
created before its job record was persisted is held by the host's durable
receipt and reconciled by the host's collector after the pending allocation
retention period. Recovery never adopts historical jobs, scratch or cache paths.
Existing finished history remains readable without retroactively creating
resource registrations.

## SSD controller namespace

SSD's quota preflight validates global root ownership and the root broker's
kernel credentials. Keep the CodexPro controller in the host user namespace.
An unprivileged systemd user service with `ProtectSystem=full` can implicitly
create a namespace mapping only the service user, even when the configured
`PrivateUsers` property says `no`. Root UID 0 then appears as an unmapped UID,
and the host correctly refuses allocation before starting a job.

Install the SSD-specific [drop-in](../deploy/ssd/codexpro-host-resources.conf)
under `~/.config/systemd/user/codexpro.service.d/ssd-dev-host-resources.conf`.
It sets `ProtectSystem=no` and `PrivateUsers=no`; the existing unit continues
to enforce `NoNewPrivileges=true`. The service UID, authentication, catalog,
PATH configuration and job scope ownership remain configured by their existing
owners. Other hosts keep the general service template.

Do not accept an unmapped UID as proof of root ownership. The helper's strict
filesystem, project quota, root peer and cgroup checks apply unchanged.
For the observed SSD service, the old mount namespace was already the host's
mount namespace; `ProtectSystem=full` had not established read-only OS mounts.

Before activation, run the disposable namespace rehearsal:

```sh
node scripts/job-resources-namespace-smoke.mjs /home/spite/.local/bin/ssd-dev /absolute/new/private/evidence-directory
```

The rehearsal reads the installed CodexPro unit's other hardening settings and
applies the exact drop-in to a disposable user unit. It verifies the host user
namespace, full UID map, root ownership, `NoNewPrivileges`, and the unchanged
effective settings. It calls the installed helper to allocate and cancel only
its own unactivated registration, then verifies that the disposable unit was
collected. The controller records both private allocation IDs before launching
the unit and repeats nonce-bound cancellation after it exits, including a lost
begin response or child timeout. Cancellation atomically blocks a delayed begin. No hypothetical job scope or payload is started. Helper responses
and diagnostics remain in the new private evidence directory.

Activate this unit change during the guarded idle restart described below,
without changing the immutable release. Afterwards, verify a real MCP job's
host resource registration, XFS boundary and whole-scope completion.

The implicit namespace behavior is implemented in
[systemd's service execution code](https://github.com/systemd/systemd/blob/main/src/core/exec-invoke.c).

## Verification and deployment

`test/job-resources.test.mjs` exercises fail-closed configuration, immutable
resource IDs, terminal recovery and retention protection. Run
`scripts/job-resources-smoke.mjs /absolute/path/to/ssd-dev` on Linux to rehearse
isolated scratch/cache injection, overlapping consumers, escaped descendants
and manager disappearance using a disposable private job store. The existing
`scripts/job-restart-smoke.mjs` and `scripts/work-scope-smoke.mjs` exercise
scope ownership without an adapter.

Follow [the deployment procedure](../deploy/README.md): build and verify a
candidate release, back up job metadata, inventory active jobs, and switch the
immutable release symlink during a quiet cutover. Restart only the connector
service, preserving its socket, tunnel, catalog, authentication and limits.
Reinitialize the MCP client and acquire fresh edit tags. Do not start a second
manager on production job storage.

For rollback, first finish or stop every job created by the candidate and prove
its host resource registration quiescent. An older release cannot reconcile
the new resource claims. Preserve candidate release files until its runners
finish; never restore an older job table over newer records.
