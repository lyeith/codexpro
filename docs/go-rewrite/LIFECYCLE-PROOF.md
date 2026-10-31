# Candidate lifecycle proof

`lifecycle_proof.py` supplies the process-level evidence that unit tests alone cannot establish. It launches a fresh private Go instance on port 7210, with its own random bearer token, fresh catalog, Git fixture, state directory, and fault-injection scripts. The parent must finish the other candidate lanes and stop their owned candidate process before running this driver. An occupied port is a hard refusal; the driver never discovers and stops a listener.

Run from the rewrite worktree after uploading this script beside `contract_suite.py` and the updated `process_contract_suite.py`:

```sh
nice -n 10 timeout -k 90s 400s python3 docs/go-rewrite/lifecycle_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --helper /home/spite/.local/bin/ssd-dev \
  --state-parent /home/spite/.codexpro-go-proof/lifecycle-ATTEMPT \
  --output docs/go-rewrite/evidence/go-lifecycle-ATTEMPT.json
```

Replace the binary path with the actual built candidate and use a fresh attempt name for both paths. Existing state and evidence are not overwritten. The driver enforces reduced scheduling priority itself, uses a 240-second scenario deadline, bounds individual control commands, and reserves the outer timeout for cleanup. Expected runtime is roughly two minutes, including two actual 30-second lease-maintenance intervals; host delays can increase it.

## Eight proof scenarios

1. **Managed workspace creation and idempotency.** Reusing the explicit retry key returns the same lease; the checkout belongs to the private state directory.
2. **HTTP server crash while a job runs.** The driver sends SIGKILL only through the Popen object it created. The detached command must remain alive, the restarted server must recover the same job, scope, resource registration, and output cursor, and the command must execute only once.
3. **Normal completion with an escaped descendant.** A child creates a new session, ignores TERM, and redirects every standard stream. Normal parent completion must drain its registered scope. The probe checks both persisted settlement and the real PID/start-time identity.
4. **Explicit stop with an escaped descendant.** The same ownership check is required after `stop_jobs`. While the job runs, removing its otherwise-clean workspace must fail with the source-compatible active-operations error.
5. **Request timeout with an escaped descendant.** A three-second foreground timeout must kill and settle the scope, including the independently observed child.
6. **Server crash during delayed host registration.** A private helper shim pauses `external begin` after durable caller-chosen ownership IDs exist. Recovery must settle that prepared admission. Releasing the original late begin must not allocate a resource after the server declared quiescence, and no ungranted command may execute.
7. **Real periodic lease inspection failure and recovery.** A private PATH shim fails Git inspection without moving or changing the checkout. The actual background maintenance loop must record unavailable health while retaining ready ownership, then record healthy state at the next interval with the same lease identity. Every observed durable revision is included in evidence.
8. **Final clean removal.** Every private job must be terminal and quiescent, every observed command/descendant must be gone, and release plus clean removal must leave no managed lease.

## Scope and cleanup

Only the newly created Popen server is restarted. No live service, configuration, production state, existing listener, broad process-name match, or shared job allocation is stopped. Fault markers affect only the private server's Git/helper scripts. SQLite checks use read-only connections to the freshly created candidate stores.

Primary cleanup first stops its own Popen server and then reads the private durable job table. An admission whose RPC response was lost is recovered only when its exact submitted command, nonce, workspace, checkout root, and helper registration match this proof. The driver cancels these exact registrations before releasing any delayed begin shim. SIGTERM from the external timeout enters this cleanup path; the additional 90-second grace bounds emergency settlement. A failed probe never turns into a pass because cleanup succeeded. If the candidate leaves a witnessed process alive, the report fails; any emergency PID cleanup uses a pidfd after rechecking the recorded start identity. Fixture directories and evidence are preserved for inspection.

The evidence reports zero survivors and zero unresolved owners only for the concrete bounded scenarios that passed. It does not claim a universal absence of leaks. Missing scope support, a missing maintenance loop, startup failure, late activation, an uncertain owner, or a surviving process is a visible failure.

Local guard checks, with no server or host-resource execution:

```sh
python3 -m unittest discover -s docs/go-rewrite -p test_lifecycle_proof.py -v
```
