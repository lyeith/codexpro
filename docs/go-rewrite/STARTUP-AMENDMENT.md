# Startup amendment: serve before background reconciliation

## Authority and scope

This amendment implements the coordinator's registry row31 ruling recorded at20:56/21:10 UTC on31October2026 and observed by SP-6 at21:22. The ruling followed a live TypeScript incident and a coordinator-operated restart. SP-6 did not restart or reconfigure the live service. The new requirement is that the Go candidate listen and answer health before any host reconciliation, and that startup and unrelated requests never wait for `ssd-dev`, host ownership recovery, or a full historical-work scan.

The existing approval at`acf6b9b` remains in force: stages3/4, full Bash and workspace-write permission, no approval machinery, the eleven approved retired descriptors remain absent, and no cutover. This document records deliberate startup behavior changes; they are not claimed as byte-identical TypeScript behavior during initialization.

## Startup sequence

The entry point validates configuration and loads the bounded project catalog and lease metadata. These steps do not invoke the host helper or reconcile leases. It then binds the configured candidate listener, installs the normal authentication/security wrapper, and enters the actual HTTP accept loop. The readiness fence is the listener's first `Accept` call, not a log line.

The early `/healthz` route and the steady route share `admin.Service.HealthHandler`. Its authentication, fields, path redaction, GET/HEAD behavior and body remain the steady contract; there is no extra `ready` field. Audit, jobs, analysis and archived-work services are constructed after the accept-loop fence. A stable, deferred archive object is installed before publication, so routes never observe a replaced archive pointer.

`engine.NewPausedWithWorkspaces` and `jobs.NewPaused` load durable state without a scope probe or background workers. Once the complete dispatcher is published atomically, the entry point starts lease maintenance, job monitoring/recovery, the asynchronous systemd-scope capability probe, and deferred work-history opening. None is awaited by health or ordinary file reads. The existing `New` constructors retain their synchronous semantics for isolated tools/tests; the HTTP entry point uses the paused constructors.

Cancellation closes the bootstrap listener and prevents a late publication. After full publication, HTTP draining (in-flight tool calls get CODEXPRO_DRAIN_TIMEOUT_MS, default 30 s, as in the TypeScript service) runs before domain stores close, including a signal arriving in the small interval immediately after publication. Domain cleanup remains idempotent with the normal graceful-shutdown path.

Source: `go/cmd/codexpro/main.go`, `listener_ready.go`, `go/internal/transport/startup.go`, `go/internal/admin/health.go`, `go/internal/engine/engine.go`, `go/internal/jobs/startup.go`, and `go/internal/workhistory/deferred.go`.

## Explicit transient contract changes

| Condition | Observable behavior | Bound and recovery |
|---|---|---|
| Listener is serving, complete dispatcher not yet published | Authenticated paths other than health return HTTP503 with `Retry-After: 1`; MCP gets JSON-RPC error`-32000`, `Server is starting; retry shortly`; ordinary JSON routes use `server_starting` | No tool invocation occurs. A later request uses the complete dispatcher after publication. Authentication remains enforced. |
| Jobs have not started their background services or the scope capability probe has not succeeded | New process admission returns the existing business code`server_restarting`, the message`Job resources are not ready; retry after background startup completes.`, and`retry_unchanged: true` | No child is admitted. The capability probe has a5-second attempt budget and5-second retry interval. Reads and stop requests do not wait for that probe. |
| A configured historical archive is still opening or failed its bounded initialization | Explicit historical-work access returns`work_history_unavailable` promptly | Opening runs in the background with a2-second context budget. When no archive path is configured, history is immediately ready and empty. A failed configured archive is not silently replaced by an empty history. |
| Historical ownership cannot yet be settled | The record remains non-quiescent and continues to consume unresolved-ownership capacity | Each settlement attempt shares a12-second budget; no host operation exceeds its existing10-second control-call limit. Retries occur in the bounded worker pool. |

`ssd-dev` and other bounded control commands run at nice10 or lower priority through the same process-group cleanup implementation. Actual nice values are measured by the proof because inherited priority may make them numerically higher. No cgroup-weight claim follows merely from separate process groups. Exact allocation/registration IDs, cancel-before-late-begin revocation, and the durable completion outbox are retained.

Explicit `bash`/`start_jobs` resource allocation still follows its command contract once admission is ready. The new guarantee concerns startup, implicit reconciliation and unrelated requests; it does not invent a successful background allocation receipt before the helper has allocated resources.

## Historical-work scope

The verified private snapshot used by the gate is:

- Path:`/home/spite/.codexpro-go-proof/work-archive-20261031T1830Z/work.sqlite`
- Size:`257908736` bytes
- SHA-256:`50d09a5cc88c068f897e8734676d784488c9828d65b4da2ae7024f48483f0adf`

The driver hashes this existing private file before timing and never opens the live SQLite database. The candidate uses the immutable archive reader. Source review found that archive opening validates schema and loads nine historical run bodies; it does not enumerate all operation bodies. The private archive's `operation_states` expression index serves the run-scoped unresolved-operation count, as confirmed by the captured query plan. That count is used only by explicit work-history/dashboard access, not ordinary health or file-read startup.

This snapshot's lineage is independent of the finished-job importer proof: the fourteen finished jobs came from a stopped private TypeScript contract instance. They are synthetic contract records, not production job history. No live process, lease or resource registration is adopted by either proof.

## Predeclared runtime gate

`startup_recovery_proof.py` starts one owned candidate on7210, two nice10 CPU workers capped at45seconds, and an `ssd-dev` stand-in capped at30seconds per invocation. It seeds one real-format, prepared but ungranted ownership record into a fresh private jobs database. Recovery must invoke `external cancel` for those exact caller IDs. The helper publishes an atomic fsynced entry witness and remains blocked until the driver releases it. A helper-unused cold start cannot pass.

The pass limits are **3000ms from immediately before candidate Popen to the first successful authenticated health response**, and **3000ms from that same timestamp through MCP initialization and the first successful read of the nonce-bound file**. Three additional health and file-read calls must each finish within3000ms while the helper remains alive and blocked, ownership remains non-quiescent, and both CPU workers remain active. A miss is retained as failed/open evidence; the limit is not adjusted to the observed result.

The proof verifies that the candidate opened the exact private archive inode, records its total process `rchar` and startup delta, and requires total read-syscall bytes below one quarter of the archive. This is supporting runtime evidence alongside the source audit; `rchar` alone cannot exclude memory-mapped or page-cache access. Provenance hashing by the Python driver is outside the candidate's counters and outside the timing window.

CPU workers, candidate and helper have PID/start-tick/cgroup witnesses. If workers share the candidate's cgroup, the report calls this **same-scope conservative CPU load**; it does not claim sibling cgroup weight isolation. The separate lifecycle lane supplies the real host-scope ownership evidence. This stand-in lane creates no real `ssd-dev` allocation, although the normal bounded `systemd-run ... true` capability probe may create its own transient scope.

Every launch has an atomic fsynced receipt containing the binary hash, private state path, boot ID, PID/start tick, cgroup and cleanup result. Unknown launch outcomes do not authorize a guessed PID, listener or name-based signal. Cleanup releases only the private marker, closes the own MCP session, and stops only witnessed own groups. Exact helper PID cleanup requires the nonce, start tick and a pidfd; needing that fallback makes the gate fail. The wrapper budget is150seconds with90seconds TERM-to-KILL grace. CPU/helper self-caps provide an additional bound.

## Execution and evidence

Run only after confirming7210 is free and using a new state/evidence name:

```sh
nice -n 10 timeout --signal=TERM --kill-after=90s 150s \
  python3 docs/go-rewrite/startup_recovery_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --expected-binary-sha c67483e57eea21e3d5b2984effdd02048cd314236e9b9bb6add7a735c492896a \
  --state-parent /home/spite/.codexpro-go-proof/startup-recovery-1 \
  --archive /home/spite/.codexpro-go-proof/work-archive-20261031T1830Z/work.sqlite \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/startup-recovery-1.json
```

The binary hash is a guard for this prepared run, not a permanent release identity. Future runs must use their freshly verified build hash. The driver imports the existing contract, paired, managed, lifecycle and process proof helpers from the same documentation directory; `lifecycle_proof.py` must be the committed version with durable launch receipts.

Ten offline guard tests passed without starting a listener, worker or helper. The seven-package startup race gate passed in24.35seconds, followed by the affected command/transport rerun after the shutdown-order amendment.

The owned runtime gate executed at21:53 UTC on31October2026 with candidate SHA`c67483e57eea21e3d5b2984effdd02048cd314236e9b9bb6add7a735c492896a` and passed all predeclared thresholds. Evidence:`evidence/startup-recovery-1.json`, SHA-256`765d20b0a5caf33775015649dec80a4752b59de1ca185d961d7ea871345fa1cc`.

| Measurement | Observed | Predeclared limit |
|---|---:|---:|
| First authenticated health, from before Popen | 84.795ms | 3000ms |
| First file read including MCP initialization, from before Popen | 163.934ms | 3000ms |
| Three health calls while recovery remained blocked | 1.285–1.497ms | 3000ms each |
| Three file reads while recovery remained blocked | 8.944,9.054,9.820ms | 3000ms each |
| Candidate total read-syscall bytes through the blocked reads | 335,898B | Below64,477,184B, with the I/O qualification above |
| Cleanup errors | 0 | 0 |

The helper entry and release refer to the same exact PID/start tick and caller registration. The prepared claim remained non-quiescent during all three measured reads and became quiescent after the release. The ungranted command did not execute. The server exited with code0; both owned CPU groups stopped on SIGTERM; all three groups were observed quiescent.

This actual stand-in remained blocked for0.519603seconds, so the runtime case exercises concurrent recovery and reads but does not reach the helper timeout/retry branch. Both CPU workers were active and advanced their individual CPU counters from0.12/0.14seconds to0.68/0.70seconds. Workers and helper ran at nice19 while the server ran at nice10, all in the same cgroup. Host one-minute load average was1.33; this is the declared controlled two-worker experiment, not a reproduction of the incident's load30. Archive provenance hashing occurs before launch and warms the private snapshot's cache; this is not a cold-cache archive test. The separate real-helper lifecycle lane supplies host-scope recovery evidence.
