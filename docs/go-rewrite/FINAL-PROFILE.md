# Reproducing the final profile context

[final_profile.py](final_profile.py) is a convenience CLI over the captured template for the inline context forms used for the final private profiles. The captured receipts were not produced by this new CLI, and no profile was rerun to introduce it. The embedded ordinary template, before binding its four placeholders, is byte-bound to SHA-256 `bd65cbf7a8dbbfb8d6eefdef5870487f08c5bea28e1744d7a80c801f5811e378`.

The under-build form makes exactly three changes to that template: select `profile_under_build_observed.py`; remove `--count 160 --concurrency 2 --duration 180 --delay-ms 50` because that wrapper supplies the identical values; and extend the outer subprocess timeout from 200 to 240 seconds. Fingerprints, metadata-only MCP initialize/tools-list readiness, receipt fields and final 160-call/zero-failure/unchanged-input/process-identity assertions remain unchanged. CLI values are bound as AST constants, never interpolated as Python code.

The caller supplies an already-running owned process and its canonical state directory beneath `~/.codexpro-go-proof`. `--side go` fixes port 7210 and `--side typescript` fixes port 7211. The helper validates ownership, refuses arbitrary ports and output/path aliases, and keeps the original process/state/executable checks. It creates no server and performs no cutover. Labels must be bounded filename components, and profile/context/load outputs must be new. The Go form remains pinned to the final `c4402d…` candidate recorded in [RESULTS.md](RESULTS.md).

With `SP6_GO_PROFILE_STATE` and `SP6_TS_PROFILE_STATE` set to the corresponding existing private state directories, run the desired condition sequentially from the repository root:

```bash
nice -n 10 python3 docs/go-rewrite/final_profile.py \
  --state "$SP6_GO_PROFILE_STATE" --side go --label repro-go-ordinary-1

nice -n 10 python3 docs/go-rewrite/final_profile.py \
  --state "$SP6_TS_PROFILE_STATE" --side typescript --label repro-ts-build-1 --under-build
```

Use equivalent independently seeded state, the same source checkout/catalog/limits and a new label for every condition. The context receipt's `expected_audit_actions: 2783` is a declaration of the intended seed, **not a runtime count assertion**. Actual seed/count evidence remains in the separate seed and Go-under-build count receipts; this helper does not add a tool call to verify it before timing.

Source-byte fingerprints read the checkout before profiling and can warm the shared OS page cache. Executable hashing and readiness also occur before timing. Fresh server/application caches therefore do not imply cold filesystem caches. Historical runs had different pre-profile idle ages and preceding toolchain/core checks; preserve their recorded timing/identity evidence rather than retroactively claiming a fixed warm-up protocol. This CLI does not make those earlier conditions identical or remove any cold-call samples.

The helper's offline guards parse and compare both source forms, check safe binding/private paths/new outputs, and execute no profile. Actual performance and acceptance conclusions belong in [RESULTS.md](RESULTS.md), [PERFORMANCE-COMPARISON.md](PERFORMANCE-COMPARISON.md) and the recorded per-run context/load receipts.
