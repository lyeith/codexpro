# Lifecycle and historical job proof review

These changes and commands are staged for the main agent to publish and run. No server or host-resource operation was performed during this review. The lifecycle and historical-job projection lanes remain pending actual execution.

## Frozen corrections

The lifecycle driver records each exact job admission intent before its RPC. Cleanup stops the Popen server it created, reads the private durable job table, and recovers an uncertain response only when the job ID, command, run nonce, workspace, checkout root and helper claim all match this proof. Exact claims are cancelled before the delayed-begin fixture is released. The delayed helper is also tracked by its witnessed PID and kernel start identity, after its registration IDs match the durable admission. External SIGTERM enters cleanup instead of bypassing the `finally` block. The driver never resolves a listener PID or signals a process by name.

Ten local tests pass without starting a server, helper, job or host scope. They cover occupied-port refusal before state creation, existing-state refusal, exact Popen ownership, PID reuse, cgroup path guards, fixture syntax, lost-response ownership recovery, stop/snapshot/cancel/release ordering, registration of intent before RPC, SIGTERM cleanup, and refusal of foreign claims. Python compilation of both proof drivers and the snapshot helper also passes. These are harness checks, not the requested runtime proof.

The rootless-error redaction patch is separate from the frozen batch-child fix. TypeScript `src/pathLabels.ts` uses the configured `worktreeRoot`; Go's `Redactor.Labels` still hardcoded `Home/worktrees`. The patch uses the configured root with the existing default when unset/empty. Two native regression tests check custom fallback and payload-root precedence, then default fallback. Native execution belongs to the main agent. Batch raw-child behavior needs no further change: an actual successful child supplies its raw root, and a batch containing only errors uses the same config/project fallback as TypeScript.

## Lifecycle command

Finish the other candidate lanes and stop only the candidate process owned by the main agent before this command. Port 7210 must be free; the proof refuses to displace an existing listener. The supplied binary must be the newly built candidate. Use a fresh attempt name; do not pre-create its state parent.

```sh
nice -n 10 timeout -k 90s 400s python3 docs/go-rewrite/lifecycle_proof.py \
  --binary /home/spite/Projects/codexpro-go/go/bin/codexpro-go \
  --helper /home/spite/.local/bin/ssd-dev \
  --state-parent /home/spite/.codexpro-go-proof/lifecycle-ATTEMPT \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/go-lifecycle-ATTEMPT.json
```

The scenario deadline remains 240 seconds. Individual HTTP/control calls are bounded. The longer external bound leaves room for fixture setup and exact-owned cleanup; `timeout` sends TERM at 400 seconds and KILL only after an additional 90-second grace. The proof is expected to take roughly two minutes because it waits for two actual 30-second maintenance cycles. It includes accepted-job restart/cursor continuity, setsid descendant settlement on success/stop/timeout, prepared-before-begin recovery, transient lease health recovery, and clean final lease removal. Its eight scenario results and cleanup errors must all be inspected; merely exiting or producing a file is insufficient.

## Historical jobs command against the verified private source

The existing jobs snapshot/projection driver is unchanged. Its selected source is the owned TypeScript instance's `/home/spite/.codexpro-go-proof/contract-ts-3/state/jobs`, not production state. A separate state parent must already exist; it must not be an ancestor or descendant of the source. Create one fresh parent, then run from the rewrite worktree:

```sh
mkdir -m 700 /home/spite/.codexpro-go-proof/migration-jobs-ATTEMPT
nice -n 10 timeout -k 15s 190s python3 docs/go-rewrite/legacy_jobs_proof.py \
  --source-jobs /home/spite/.codexpro-go-proof/contract-ts-3/state/jobs \
  --release /home/spite/apps/codexpro/releases/job45-dashboard-20261030-44bb76b \
  --go-dir /home/spite/Projects/codexpro-go/go \
  --state-parent /home/spite/.codexpro-go-proof/migration-jobs-ATTEMPT \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/legacy-jobs-proof-ATTEMPT.json
```

The driver snapshots bounded regular files and verifies that the private source did not change. It invokes only the immutable release's output store, with a 45-second oracle bound, then the one opt-in native migration/projection test under `GOMAXPROCS=2`, a 110-second Go test deadline and a 125-second subprocess limit. The outer timeout and the caller's owned host job scope bound toolchain descendants. The native probe does not admit commands or adopt source process authority. Active/unresolved source records remain inert archived metadata; settled terminal records preserve supported output metadata, bytes and cursors and must survive a Go-store reopen. The selected source must contain at least one finished record and nonempty output, so an empty migration cannot pass.

Keep `legacy_jobs_snapshot.py`, `legacy_jobs_oracle.mjs`, `legacy_jobs_proof.py` and `archive_work.py` available in the documented helper locations. The evidence output directory must already exist. Existing evidence is never overwritten. A concurrent source change is a visible failure rather than permission to alter or freeze the source server.
