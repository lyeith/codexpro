# Executing and settling the private lifecycle proof

This checklist applies only to the new private lifecycle fixture. It never grants authority over the production service, an existing listener, or another agent's job.

## Before launch

1. Finish the other candidate lanes and stop only the candidate process already owned by the main agent. The managed differential driver and this lifecycle driver both use port 7210; execute them sequentially. The lifecycle script refuses an occupied port rather than locating or terminating its listener.
2. Use the newly verified candidate binary and record its SHA. Confirm the installed host-resource helper's exact executable path, using the already established source linkage. Use a fresh proof root and a fresh evidence path. Do not pre-create the proof root.
3. Publish the updated driver, its guard tests and its dependencies (`contract_suite.py`, `process_contract_suite.py`) together. The pre-update driver hash is `49ed05c5006a80be09887fd40bc2b0912d2921fd2adc5531e0c5521d09857b7a`; the manifest binds the replacement hash.
4. Start through an explicitly owned, bounded command/job. Retain that outer launch ID independently of the driver's output. Never use a broad process-name or listener search as shutdown authority.

```sh
nice -n 10 timeout -k 90s 400s python3 docs/go-rewrite/lifecycle_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --helper /home/spite/.local/bin/ssd-dev \
  --state-parent /home/spite/.codexpro-go-proof/lifecycle-ATTEMPT \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/go-lifecycle-ATTEMPT.json
```

Substitute the actual built binary if it has a different path. An existing state/evidence path is an error, not a reason to remove it.

## Durable launch identity

Every incarnation has its own private `launches/server-NNN.json` receipt. The file and containing directory are synced, and updates publish complete JSON atomically. The receipt records the binary/path hash, proof/state roots, port, boot ID and, after observation, PID, kernel start ticks and cgroup. It contains no bearer token, HTTP session ID, grant nonce or host registration credential.

| Receipt phase | Meaning and permitted interpretation |
|---|---|
| `prepared` | The launch intent is durable. The child may or may not have been spawned if the driver subsequently died. There is no verified process identity. |
| `launch_outcome_unknown` | Spawning raised or was interrupted. It is not evidence that no child exists, and it is not authority to infer a PID. |
| `spawned_unverified` | Popen returned a child PID, but the required kernel identity has not been durably established. A separate recovery process must not signal the numeric PID solely from this receipt. |
| `identified` | The driver observed its own Popen child, kernel start ticks and cgroup. Readiness has not been proven. |
| `ready` | The same owned process completed MCP initialization. This is a historical readiness observation; recheck identity before any later control action. |

The running driver retains the Popen object and stops only that child. If the driver is gone, a verified receipt requires the same boot ID and matching `/proc/PID/stat` start ticks before the PID can be considered the original child. A changed or missing identity is not a matching target. An unverified receipt remains an uncertain launch; preserve it and use only independently established ownership, such as the exact dedicated outer harness job recorded at submission. Do not adopt an unrelated process because it occupies 7210 or has a similar name.

## Actual time bounds

| Operation | Bound in the driver or candidate |
|---|---|
| Scenario deadline | 240 seconds, checked between tool calls and by polling waits |
| MCP request | 12-second urllib socket I/O timeout; this is not a total wall-clock deadline for a continuously active stream |
| Initialization retry loop | 15-second scheduling window, plus any already-started initialization request; initialization makes two RPCs |
| Client session close | 3 seconds |
| Owned server stop | TERM and wait up to 5 seconds, then only that Popen child's KILL and a 3-second wait |
| Driver helper cancellation | 12 seconds per exact registration; at most five fixture job admissions are expected |
| Candidate helper control call | 10 seconds, with bounded output |
| Candidate scope inspection/control | 2 seconds per command; startup probe 5 seconds |
| Escaped command fixture | 35-second independent lifetime |
| Delayed-begin fixture | 60-second pause bound, then a 12-second helper invocation |
| Actual maintenance observation | Up to 40 seconds for each of two real 30-second maintenance intervals |
| Outer harness | TERM at 400 seconds; KILL after a further 90 seconds |

The 240-second value is not a universal wall-clock bound on every nested operation. The outer timeout is the final bound. TERM enters Python cleanup; a hard KILL can prevent final evidence publication. Per-incarnation receipts remain available in that case. The expected successful duration is roughly two minutes, but host load can increase it.

## Job and helper ownership

For every admitted fixture job, retain the job ID, workspace ID, exact nonce-bearing command and cwd, private root, resource claim and scope identity from the private job store/evidence. The resource claim contains the caller-chosen run ID and registration ID. The scope identity contains unit, boot ID, invocation ID and cgroup; a unit name alone is insufficient proof of the same incarnation.

The candidate's helper protocol uses these exact identities:

```text
ssd-dev external begin --purpose tool --cwd PRIVATE_CWD --owner-unit EXACT_UNIT \
  --cache-consumer --run-id RUN_ID --registration-id REGISTRATION_ID
ssd-dev external exec RUN_ID --registration-id REGISTRATION_ID -- RUNNER_COMMAND
ssd-dev external finish RUN_ID --registration-id REGISTRATION_ID --exit-code EXIT
ssd-dev external cancel RUN_ID --registration-id REGISTRATION_ID
```

Do not invoke `begin` or `exec` manually while investigating a failed proof. Prepared or ungranted recovery requires cancellation/revocation; a quiescent `finish` response for an absent allocation does not prevent a delayed begin. The driver intentionally tests that distinction. A verified cancellation response must identify the same run and report both `quiescent:true` and `cancelled:true`.

## Cleanup and acceptance

1. The driver's cleanup stops its own HTTP server first, freezing new admissions before reading the private durable jobs table.
2. Lost-response admissions are eligible for cleanup only when their exact submitted command, nonce, cwd, workspace and private root match. Their helper path and complete registration IDs must also be valid. Cleanup never enumerates or cancels unrelated host allocations.
3. Cancel exact owned registrations before releasing the delayed-begin fixture. Keep unresolved ownership visible if cancellation fails; do not report success because the command's exit code is already known.
4. Check the corresponding scope is unpopulated and that every observed PID/start-time witness is dead. The driver may use a pidfd for a still-matching witnessed process after a failure; such emergency cleanup leaves the proof failed. PID reuse, missing identity or an unknown scope is never permission for a broad signal.
5. Require all eight scenarios to pass, zero cleanup errors, zero unresolved fixture owners, no surviving witnessed processes and clean final lease removal. Inspect the private logs and receipts when any condition fails. Preserve fixture state and the failed evidence rather than erasing it for a retry.

The result establishes only the bounded scenarios it actually exercised. The managed business lane's empty server PGID does not prove all separately grouped descendants are absent. Both proof drivers run under nice 10, so report actual child niceness and cgroup separation without claiming that these fixtures measured a server scheduling-priority advantage.
