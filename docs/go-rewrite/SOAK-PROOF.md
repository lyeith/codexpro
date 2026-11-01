# Bounded ten-minute read-only soak

This gate observes an existing, separately owned private Go candidate on port7210. It never starts, stops, signals or reconfigures that server. It controls only the Python profiler processes it creates. The parent retains candidate lifecycle ownership.

The driver verifies the private launch manifest against the current PID/start tick, executable path/inode/hash, listening socket, actual command-line state/token/catalog paths, process user and `GOMAXPROCS=2`. It accepts the manifest's separate `state_root` and `config_home`; both must be private, and home/token/catalog must remain inside the declared outer state. It derives the direct workspace ID from the selected project in that private catalog, then checks an explicit `--workspace` if supplied. The original repository may be the profile project because all eight tool requests are the unchanged read-only corpus.

## Work and limits

- Run the unchanged `profile.py` at approximately0,300 and600seconds, each with160 calls, concurrency2, delay50ms and the original180-second internal profile deadline. The soak imposes a stricter130-second child lifetime; an overrun is a failed gate and retained partial evidence, not a successful partial corpus.
- Retain every per-call measurement and the profiler's0.5-second process samples in three separate JSON reports. The parent soak also retains the unfiltered five-second RSS/CPU/I/O/thread/nice/load/cgroup series, including points before and after every profile. Atomic checkpoints preserve that series in the private observer directory.
- Generate no compiler or synthetic CPU load. Concurrent host work remains visible in the recorded load average and candidate resource samples. Profile-child scheduling priority is recorded; inherited nice can make `nice -n10` numerically lower priority than10.
- Require all480 measured calls, no tool or initialization errors, observed idle RSS at most100MiB, and sampled peak RSS at most256MiB. “Idle” means every sample outside an active profiler child, including immediate post-profile samples; no quiet-time exclusion or memory tolerance is added.
- Publish raw post-profile RSS values/deltas, strict-increase observation and the idle series slope. Those descriptive values do not establish an automatically accepted growth tolerance or unbounded stability. Review the complete bounded series and its cache/load context.
- Internal wall budget is735seconds, with the normal third profile beginning at600seconds. An external740-second TERM/10-second KILL wrapper provides a750-second absolute process budget. Owned profiler cleanup uses exact process-start/group/session witnesses. An unknown launch outcome does not authorize a guessed signal.

No MCP or health warm-up precedes the first profile. A reused candidate must be labelled `warm-prior-traffic` with its known prior-profile call count. A newly started candidate may use `fresh-no-prior-profile` only with zero prior profile calls; this does not assert a cold filesystem page cache. Soak latency measurements supplement and never replace the separately declared cold-target performance profile.

## Execution

Use the actual existing candidate's launch manifest and a new observer/evidence name. The following is the intended warm-candidate invocation after its earlier160-call profile has completed; replace the manifest and expected binary hash with the freshly verified values supplied by the parent:

```sh
nice -n 10 timeout --signal=TERM --kill-after=10s 740s \
  python3 docs/go-rewrite/soak_readonly.py \
  --manifest /home/spite/.codexpro-go-proof/OWNED-CANDIDATE/launch.json \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --expected-binary-sha VERIFIED_BINARY_SHA256 \
  --profiler /home/spite/Projects/codexpro-go/docs/go-rewrite/profile.py \
  --profile-sha 3967a1f0245676fce001be1faac0d6e9d5890529e46965983421c899180dd868 \
  --project codexpro \
  --workspace ws_4d43e03b8d338283c2776d95 \
  --cache-condition warm-prior-traffic --prior-profile-calls 160 \
  --control-dir /home/spite/.codexpro-go-proof/soak-observer-1 \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/soak-private-go-1.json
```

The driver writes the main report and `soak-private-go-1-profile-0.json` through `-2.json` beside it. Launch receipts, logs, the pinned binding and the sample checkpoint remain in the new private observer directory. No credential or raw process environment enters evidence. The existing candidate remains running after this command, whether the gate passes or fails.

The profiler source SHA is pinned by the upload manifest. It is imported unchanged; its client is wrapped only to check the exact candidate PID/start/executable before each RPC and to refuse redirects/proxies. No case, response, timing or error is removed. A profile that returns fewer than160 distinct indices is incomplete even if its process exits successfully.

Nine offline guards cover PID reuse/death, refusal of port7200, outer-state versus config-home binding, escaping config home, partial/duplicate call indices, retained errors, positive memory-growth visibility and unknown-launch signal refusal. They do not launch a server or profiler. The distinct actual runtime gate has now completed, as recorded below.

## Executed result

The warm private candidate with binary SHA-256 `c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2` completed **606.448662 seconds** of observation and all **480 measured calls**. [soak-private-go-1.json](evidence/soak-private-go-1.json) records `passed_declared_error_and_memory_gates:true`, no cleanup errors, and no server-control actions by the observer. Idle peak was **49,528 KiB** and overall sampled peak **54,804 KiB**, below the original 100/256 MiB limits. Post-profile RSS was **48,976 → 48,520 → 48,916 KiB**, not strictly increasing; the recorded idle slope was negative. These are bounded observations, not a new growth tolerance or an unbounded stability guarantee.

The [independent validator](evidence/soak-private-go-1-validation.json) recomputed all three original 160-call reports without discarding samples. Their p95 values were **76.156 / 74.218 / 75.493 ms**, and p99 **100.047 / 81.375 / 82.215 ms**; all three were complete and valid. This reused warm-candidate soak supplements the separate final ordinary/under-build profiles. Candidate lifecycle cleanup belongs to the owning parent and its separate receipt; this observer only stopped its own profiler groups. The loaded-startup safeguards and final census are separate gates in [RESULTS.md](RESULTS.md), and this soak does not close them.
