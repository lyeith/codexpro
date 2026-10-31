# Performance context and actual compiler identity

The original160-call profiler and its timed request cycle remain unchanged. The observed-build wrapper adds identity evidence for the compiler load it already starts; it does not introduce another workload or change the two-worker/fresh-cache command.

## Input and configuration receipt

The parent records the current shared project HEAD, status and input digests before/after the final profiles. The TypeScript release revision (`44bb76b`) and the checkout being read are separate identities. The initial capture identified the shared checkout as `500cad72d44`; without a stage1 input receipt, do not infer that its files matched the release commit or retrospectively assign it a new digest.

Both cached private launchers construct the same logical two-project catalog: the original codexpro root and a private contract fixture, with matching labels, base refs, limits and creation-root IDs. Their absolute private fixture/creation paths differ. Go validates and reuses an existing catalog; TypeScript rewrites its catalog on launch. Bind the actual post-launch catalogs and instance path roles. The profiler records response byte counts and keys, not equality of complete responses; its comparator validates the exact request cycle and resource statistics, not catalog/seed identity.

Pin these same baseline defaults explicitly in all four new private limits files:

```text
CODEXPRO_AUDIT_MAX_BYTES=8388608
CODEXPRO_AUDIT_RETAIN_ACTIONS=350
CODEXPRO_MAX_OUTPUT_BYTES=120000
CODEXPRO_JOB_TIMEOUT_MS=5400000
```

The final paired files have SHA`7aca22fa4cd71526129ee7ceb5aabf74945443f0277ed2bcb65bceddf984a498`, as supplied by the parent. The first three values are the captured baseline defaults; the last preserves the existing private-launcher timeout override. Record actual environment/arguments and any saved profile precedence rather than assuming a file alone controls effective runtime values. Disable TypeScript CPU profiling in the numeric comparison.

The shared2783-action audit seed's import proof already compares all full stored values and public DTOs using8MiB/350/120000 options. For optional post-launch checks, Go's private running WAL index must be opened read-only with `query_only`, not `immutable=1`. Retained rows are in `entries`, ordered by sequence; use the indexed segment/offset/length slices and validate them against `segments.committed_bytes`. Old physically retained JSONL records are not all logically retained. The legacy sidecar's `retained_entry_digest` covers only its compacted prefix; a whole-retained-set digest needs a distinct name even when it uses the same ordered `sequence + NUL + action_id + newline` recipe.

The TypeScript launcher enables its legacy work management; Go has the coordinator-approved retirements. Those are intentional architectural/descriptor deltas. They must not be presented as hidden equal-output normalization. Hashing fixture inputs before timing also warms filesystem pages: a fresh server means fresh process-level caches, not a claim that the shared host page cache was flushed.

## Actual load identity supplement

`profile_under_build_observed.py` keeps the existing command:

```text
nice -n 10 timeout 90s go build -p 2 -a -o PRIVATE-COMPILER cmd/compile
```

Before starting the load, a bounded preflight records the selected `go version`, tool directory, invoked Go path, current directory and explicit toolchain settings. Set `GOTOOLCHAIN=go1.27.3` for **both** contemporary build profiles. The fresh `GOCACHE` remains unique to that one run; the preflight does not compile packages.

The wrapper records the new load leader's PID/start tick/group/session, then observes its bounded descendant tree every0.5seconds. Go can fork from any runtime thread, so the census checks each bounded task's children rather than assuming the main thread owns every compiler process. Each observation includes executable path/device/inode/size, PID/start tick, parent, process group, session, CPU ticks, nice and cgroup. Cgroup CPU weight/quota and memory/PID limits are read without changing them.

After the timed profile and load complete, observed executable paths are hashed only if they still match their witnessed inode/device/size. These hashes identify the actual `go` and `compile` images, not merely whichever binary the string `go` would select later. Missing identities or census limits remain explicit errors. There is no new signaling path in this supplement; the existing wrapper still owns and cleans up its Popen load process.

The prior50ms wrapper-liveness observations continue to bound complete profile overlap. Descendant samples establish actual compiler execution and its identities, but do not claim one particular compiler subprocess occupied the whole profile interval. Read `load_identity.actual_go_compiler_observed`, the compiler CPU counters, identity errors and executable hashes together with `toolchain_preflight` and the existing overlap result. An incomplete identity receipt remains incomplete evidence even if the unchanged profile itself succeeds.

Ten offline tests passed: five existing overlap guards and five new identity guards. They cover actual descendant attribution, reused/unrelated child rejection, leader incarnation change, fast load exit and executable replacement. No actual compiler or server was launched by these tests. Runtime evidence remains pending until the parent runs the two contemporary loaded profiles.
