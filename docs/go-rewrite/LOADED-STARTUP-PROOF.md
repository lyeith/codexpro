# Observed compiler-load startup gate

The coordinator's row31 ruling at 22:23 requires a second startup measurement on a loaded SSD. The first receipt remains unchanged. This wrapper runs the exact existing `startup_recovery_proof.py` (SHA-256 `3c3147ba5e7037e628e329d7eb8e6c8b772586ff60b1c0098b7b3c9ed28067a4`) under an observed fresh-cache Go1.27.3 compiler and bounded, owned CPU workers. It is a startup gate with the original health/read corpus; it does not claim to replay the separate 160-call latency corpus.

Before execution, the fixed target is one-minute host load **at least 28.0**, with **40 nice19 workers**, at most **120 seconds** of warm-up and a common **180-second self-deadline**. Each worker has an atomic nonce/birth/group/session/cgroup receipt and must demonstrate CPU progress across the entire startup child. The compiler uses `GOTOOLCHAIN=go1.27.3`, `GOMAXPROCS=2`, a new private cache and a 90-second deadline. Its actual Go/compiler descendants, executable identities, priorities, CPU observations and wrapper overlap are recorded. The wrapper is stopped after measurement; compiler completion is not required or claimed. These sampled observations do not assert that one compiler subprocess occupied every instant.

The unchanged inner gate still requires authenticated health, the first MCP file read including initialization, and three health/file-read pairs during actual pending-owner helper recovery to each finish within **3 seconds**. It binds the same 257,908,736-byte private archive and never opens the live SQLite file. The outer report embeds the original inner receipt and its SHA, adds archive identity/hash checks before and after, and requires zero WAL/SHM/journal sidecars. Provenance hashing warms the archive's filesystem cache; this is not a cold-disk claim. The inner receipt's `source_commit` identifies the TypeScript reference; the outer `candidate_source_commit` identifies the independently supplied exact Go commit.

All new children have durable launch receipts and private sessions. Cleanup targets only witnessed PID/start/group identities, concurrently removes external load, and allows the nested proof 60 seconds for its own server/helper cleanup before ordinary bounded escalation. Unknown launch outcomes and surviving nested owners fail the receipt. Execution has a 210-second budget and cleanup a 90-second budget; the external wrapper has a 300-second TERM limit and a final 90-second KILL grace. No process is selected by a port, service name or unverified PID. The live service receives no command or signal.

Run only after the parent verifies port7210 is free. Supply the exact current Git commit; do not substitute the source reference hash:

```sh
nice -n 10 timeout --kill-after=90s 300s python3 docs/go-rewrite/loaded_startup_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --expected-binary-sha c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2 \
  --candidate-source-commit "$SP6_CANDIDATE_COMMIT" \
  --startup-script docs/go-rewrite/startup_recovery_proof.py \
  --state-parent /home/spite/.codexpro-go-proof/startup-loaded-1 \
  --archive /home/spite/.codexpro-go-proof/work-archive-20261031T1830Z/work.sqlite \
  --ruling docs/go-rewrite/evidence/loaded-startup-requirement.json \
  --output docs/go-rewrite/evidence/startup-loaded-1.json
```

Runtime status at staging: **pending**. Offline tests exercise only refusal/ownership/budget guards; they start no load or service. Keep both actual startup receipts and report their measured load, priorities, helper duration and full cleanup outcomes in RESULTS.
