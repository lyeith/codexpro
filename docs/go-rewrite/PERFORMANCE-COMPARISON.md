# Validated performance comparison

`performance_compare.py` consumes the exact receipts emitted by `profile.py` and `profile_under_build.py`. It performs no HTTP requests, launches no processes and reads no token files. It validates evidence before calculating the unchanged numeric targets from `DESIGN.md`.

Place the script and `test_performance_compare.py` under `docs/go-rewrite/`. Run the 12 fast unit guards with `python3 -m unittest -v test_performance_compare.py` from that directory. The two actual saturated TypeScript baseline receipts and their build-load receipt have also been validated successfully against the routine.

## Invocation

Set the three `SP6_GO_*` paths below from the actual completed candidate manifests. They must identify the optimized binary's 160-call ordinary profile, the same profile under the bounded build, and that build's load receipt. The comparator refuses an existing output file so previous results cannot be overwritten accidentally.

```bash
nice -n 10 python3 docs/go-rewrite/performance_compare.py \
  --ordinary-ts docs/go-rewrite/evidence/private-ts-saturated.json \
  --ordinary-go "$SP6_GO_ORDINARY" \
  --build-ts docs/go-rewrite/evidence/private-ts-saturated-under-build.json \
  --build-go "$SP6_GO_BUILD" \
  --build-ts-load docs/go-rewrite/evidence/private-ts-saturated-under-build-load.json \
  --build-go-load "$SP6_GO_LOAD" \
  --out docs/go-rewrite/evidence/performance-comparison-1.json
```

Exit 0 means all computed numeric gates passed; exit 1 means a valid comparison found a missed gate; exit 2 means the evidence or requested output was refused. The report never uses a generic whole-product `passed:true` flag. Keep the raw inputs and their recorded SHA-256 digests with the aggregate output.

## What is checked

The routine requires the explicit private endpoints (TypeScript 7211, Go 7210), the original eight-request cycle, 160 requested calls, concurrency two and 50 ms inter-call delay. Reference and candidate request arguments must match exactly, including workspace ID; there is no implicit identity normalization. Ordinary and loaded profiles must use the same cycle. Missing measurements, request failures and initialization errors cannot become a fast success.

Every aggregate and per-tool count, error count, nearest-rank percentile and mean is recomputed from the raw call measurements. The source display summaries round to three decimals, but acceptance uses the unrounded measurements. CPU seconds, IO deltas, RSS endpoints and sampled peak are independently checked against the process samples. The baseline's CPU clock is inferred from ticks/seconds and checked across every report; both actual references yield 100 ticks/second. Counter resets, changed cgroups or inconsistent sampled priority prevent the affected gates from passing.

The report computes ordinary p95 ≤100 ms / p99 ≤250 ms; loaded p95 ≤250 ms / p99 ≤500 ms; zero errors and complete 160-call runs; at least 2× lower mixed p95; CPU ≤50% of the matched reference; sampled peak RSS ≤256 MiB; and at least 90% lower written bytes. The CPU ≤25% stretch result is separate from the required gates. CPU and written-byte thresholds use integer counter arithmetic, so rounded percentages cannot turn a near miss into a pass. Linux `/proc` kB values are converted as KiB, with 1 MiB = 1,024 KiB.

## Evidence limits that remain explicit

These profiler files do not establish catalog/Git/fixture identity, filesystem type, actual audit saturation, process start identity, compiler version, scope CPUWeight or equivalent durability. Those require the launch/state/build evidence collected by the parent. The comparator reports those limits instead of inventing missing facts from a label such as `saturated`.

`rss_peak_kb` is the maximum VmRSS observed by the half-second sampler, not a guaranteed between-sample or process-lifetime maximum. Initial RSS is reported only as an observation; it does not pass the idle-memory or sustained-growth/soak gate.

The original build wrapper records its total elapsed time after waiting for both processes. It proves a bounded two-worker build was launched successfully but does not record the compiler's actual finish timestamp, so it cannot alone prove continuous overlap with every profile call. The report leaves `continuous_overlap_verified:null` for those legacy receipts. If a subsequent receipt includes an actual observed `load_finished_epoch`, it is checked; an explicitly early finish prevents the loaded numeric group from passing.

A numeric comparison does not prove retained-contract correctness, lease health, process ownership, restart durability or cutover readiness. Use the separate evidence ledger and runbook for those decisions.
