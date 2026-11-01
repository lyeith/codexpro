# Observed compiler-load startup gate

The coordinator's row31 ruling at 22:23 requires a second startup measurement on a loaded SSD. The first receipt remains unchanged. This wrapper runs the exact existing `startup_recovery_proof.py` (SHA-256 `3c3147ba5e7037e628e329d7eb8e6c8b772586ff60b1c0098b7b3c9ed28067a4`) under an observed fresh-cache Go1.27.3 compiler and bounded, owned CPU workers. It is a startup gate with the original health/read corpus; it does not claim to replay the separate 160-call latency corpus.

Before execution, the fixed target is one-minute host load **at least 28.0**, with **40 nice19 workers**, at most **120 seconds** of warm-up and a common **180-second self-deadline**. Each worker has an atomic nonce/birth/group/session/cgroup receipt and must demonstrate CPU progress across the entire startup child. The compiler uses the verified native Go1.27.3 ELF derived from `GOTOOLDIR`, `GOTOOLCHAIN=go1.27.3`, `GOMAXPROCS=2`, a new private cache and a 90-second deadline. Its actual Go/compiler descendants, executable identities, priorities, CPU observations and wrapper overlap are recorded. The wrapper is stopped after measurement; compiler completion is not required or claimed. These sampled observations do not assert that one compiler subprocess occupied every instant.

The unchanged inner gate still requires authenticated health, the first MCP file read including initialization, and three health/file-read pairs during actual pending-owner helper recovery to each finish within **3 seconds**. It binds the same 257,908,736-byte private archive and never opens the live SQLite file. The outer report embeds the original inner receipt and its SHA, adds archive identity/hash checks before and after, and requires zero WAL/SHM/journal sidecars. Provenance hashing warms the archive's filesystem cache; this is not a cold-disk claim. The inner receipt's `source_commit` identifies the TypeScript reference; the outer `candidate_source_commit` identifies the independently supplied exact Go commit.

All new children have durable launch receipts and private sessions. Cleanup targets only witnessed PID/start/group identities, concurrently removes external load, and allows the nested proof 60 seconds for its own server/helper cleanup before ordinary bounded escalation. Unknown launch outcomes and surviving nested owners fail the receipt. Execution has a 210-second budget and cleanup a 90-second budget; the external wrapper has a 300-second TERM limit and a final 90-second KILL grace. The independent health/admission monitor covers owned cleanup. No process is selected by a port, service name or unverified PID. The live service receives only read-only health probes, no lifecycle command or signal.

Run only inside a coordinator-authorized quiet window after the guard verifies admission and healthy live service. Supply the tested executable's exact build-source commit, which differs from later evidence-only commits:

```sh
TMPDIR=/home/spite/Projects/sp6-evidence PYTHONDONTWRITEBYTECODE=1 \
nice -n 10 timeout --signal=TERM --kill-after=90s 300s python3 docs/go-rewrite/loaded_startup_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --expected-binary-sha c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2 \
  --candidate-source-commit 80aee7b9da65a2f31682a86406dd5e4b5a4d4ece \
  --startup-script docs/go-rewrite/startup_recovery_proof.py \
  --state-parent /home/spite/.codexpro-go-proof/startup-loaded-3 \
  --archive /home/spite/.codexpro-go-proof/work-archive-20261031T1830Z/work.sqlite \
  --ruling docs/go-rewrite/evidence/loaded-startup-requirement.json \
  --output docs/go-rewrite/evidence/startup-loaded-3.json \
  --live-token-file /home/spite/.config/codexpro/http-token
```

Recovered status: the second run completed, but its `passed=true` records startup measurements only. The required live-health and landing-admission guards were absent from both the executed wrapper and its saved launch command. The guarded acceptance gate is **OPEN**. `loaded-startup-guards-2.json` contains twelve offline unit tests, not a runtime health monitor. See `evidence/startup-loaded-2-adjudication.json` for the disposition and exact job windows.

## First loaded attempt and native compiler correction

The first loaded attempt reached one-minute load28.507 but failed before candidate startup: the managed `go` shim requested a separate ssd-dev build scope and no actual compiler descendant appeared within the fixed15-second entry bound. Its report is preserved as a failed harness attempt. Wrapper-group cleanup is not by itself evidence that the separately allocated scope is quiescent; the parent records an independent exact-run status/cleanup check before another run. No startup-latency result is claimed from that attempt.

The second wrapper invokes the already observed native toolchain directly within its own witnessed, lower-priority group. Preflight requires Go ELF SHA-256 `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b`, compile ELF SHA-256 `c331d8fc6c4ae9dbe20e8198435db0455f2a5ca55d2d02fc6d7337c906be8283`, stable file identities and a native version response of `go version go1.27.3 linux/amd64`. Actual observed descendants must remain in the owned compiler group/session/cgroup. The load threshold, worker deadlines, original startup script, compiler-entry limit and3-second gates are unchanged. Preflight hashing warms these toolchain images; this is explicitly part of the load fixture. The40 external workers coexist with the original inner fixture's two workers.

## Recovery after the live hang

Job `job_19a613e8` ran from 22:57:46.974 to 22:59:07.240 UTC on 31 October. Its 40 nice-19 workers entered at approximately 22:57:57.193–22:57:58.766 UTC; all 42 outer groups and three inner groups were verified quiescent by job completion, with no cleanup errors. UTC worker times are reconstructed from the same boot's monotonic clock and are bounded by the exact job timestamps. The candidate answered health in 126.246 ms and its first initialized file read in 327.330 ms at load 29.661; those observations are preserved as diagnostic evidence.

No live-health guard ran, so neither a fired nor a non-fired outcome can be claimed. Cleanup completed about 53 seconds before the coordinator's approximate 23:00–23:17 hang window. This timing does not establish or exclude causality. A new guarded qualification must synchronously verify healthy live `/healthz`, poll it at most five seconds apart with a one-second cutoff, stop only the witnessed owned load immediately on failure, and refuse admission while any SSD landing or stack bring-up is active. A safety abort cannot pass. Keep every original receipt unchanged, write new evidence and compiler cache under `~/Projects`, and keep the loaded window under five minutes.


## Guarded qualification

The next qualification uses `loaded_startup_guards.py`. It performs an authenticated, bounded, read-only GET of live `http://127.0.0.1:7200/healthz` before load and throughout worker launch, measurement and cleanup. Poll starts target four-second intervals and may not be more than five seconds apart. A failed response, elapsed time over one second, missed poll deadline, failed admission check or monitor failure aborts the run and requests TERM for the witnessed owned groups. An aborted run cannot pass even when the candidate's timing assertions pass. The independent loaded-window deadline remains five minutes.

Admission refuses every `plans-land-*` directory and every observed landing or stack bring-up. It inspects actual process argv, identity and accessible cwd rather than searching embedded shell command text. Protected systemd/PAM/SSH supervisor cwd entries may be classified only by exact command, uid, cgroup and, where applicable, verified parent identity; arbitrary unreadable same-user processes remain an admission failure. This classification grants no permission to inspect inaccessible data or control those supervisors.

The coordinator requires a row-31 WINDOW OPEN, then SP-6 START before load and END after verified cleanup. The 23:39 window, corrected at 23:42 to expire at 23:55, was released unused at 23:49:40 UTC: SP-6 had not started any new load. The coordinator separately reported stopping a frontend-only landing started around 23:41 before it created an SSD worktree or stack; no SP-6 measurement ran in that interval.

Canonical retained raw evidence through the recovery checkpoint is `/home/spite/Projects/sp6-evidence/private-proof-before-guarded-run.tar`, with the verified mapping and SHA-256 in [private-proof-archive.json](evidence/private-proof-archive.json). Historical runtime paths remain in original receipts. New public receipts and compiler caches are under `~/Projects`; private candidate state is archived there after the run. Original receipts are never rewritten to change their verdict.