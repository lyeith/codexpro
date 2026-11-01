# Observed compiler-load startup gate

**Final disposition: OPEN / pre-cutover blocker. Guarded attempts 3 and 4 ABORTED; neither launched compiler load or the candidate.** The coordinator's 1 November 2026 00:08 UTC ruling directed stage-4 handback with this gate open if the final attempt aborted and authorized no further load windows tonight. No further tests or load are part of this handback. No cutover occurred, and SP-6 did not stop, restart, reconfigure or redeploy live CodexPro.

The original 31 October 22:23 UTC row-31 ruling required a second startup measurement on a loaded SSD. Attempt 2 produced candidate measurements, but both its executed wrapper and retained launch command lacked the subsequently required live-health guard. Its unchanged `passed=true` receipt is diagnostic evidence only. The two later guarded attempts used the same user-required abort limits. A guard abort cannot be converted into a startup pass, and safe cleanup does not supply the missing candidate measurements.

## Fixed qualification and reproduction inputs

The wrapper uses the exact frozen `startup_recovery_proof.py`, SHA-256 `3c3147ba5e7037e628e329d7eb8e6c8b772586ff60b1c0098b7b3c9ed28067a4`. This is a startup gate using the original health/read corpus; it does not replay the separate 160-call performance corpus. The final guard/driver implementation is committed at `1e7f0d4`. Its provenance is distinct from the Go executable's build source.

| Input or constraint | Retained value |
| --- | --- |
| Candidate executable | `/home/spite/.codexpro-go/bin/codexpro-go`, SHA-256 `c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2` |
| Candidate build source | `80aee7b9da65a2f31682a86406dd5e4b5a4d4ece`; later evidence/driver commits do not change its embedded identity |
| Driver | `docs/go-rewrite/loaded_startup_proof.py`, SHA-256 `e85b71f8c82199b95401109916a8b155349f27cceab4ed200578ce0a6732172e` |
| Guard module | `docs/go-rewrite/loaded_startup_guards.py`, SHA-256 `56a8084daa82f9bdb10df650715529e22a5142cd0cebb3650aa67284db835e4d` |
| Historical input | Private 257,908,736-byte archive at `/home/spite/.codexpro-go-proof/work-archive-20261031T1830Z/work.sqlite`, SHA-256 `50d09a5cc88c068f897e8734676d784488c9828d65b4da2ae7024f48483f0adf`; never open live SQLite |
| Load fixture | One-minute load target at least **28.0**, **40 nice-19 workers**, at most **120 s** warm-up, common **180 s** worker self-deadline |
| Compiler fixture | Verified native Go 1.27.3 ELF, `GOTOOLCHAIN=go1.27.3`, `GOMAXPROCS=2`, new private cache under `~/Projects/sp6-evidence`, **90 s** command deadline and **15 s** compiler-entry bound |
| Candidate limits | Authenticated health, first MCP file read including initialization, and each of three health/file-read pairs during pending-owner helper recovery must complete within **3 s** |
| Live-service guard | Authenticated read-only `GET http://127.0.0.1:7200/healthz`; synchronous preflight, four-second target poll starts with no gap above **5 s**; one failed response or elapsed time above **1 s** aborts |
| Admission | Refuse SSD landing/stack bring-up, unverified `plans-land-*` directories, unreadable unclassified process ownership, failed admission checks or monitor failure |
| Budgets | Execution **210 s**, cleanup **90 s**, outer TERM at **300 s** with **90 s** final KILL grace; independent loaded-window cap **5 min** |

The retained command interface is `loaded_startup_proof.py` with `--binary`, `--expected-binary-sha`, `--candidate-source-commit`, `--startup-script`, `--state-parent`, `--archive`, `--ruling`, `--output` and `--live-token-file`. The verified runtime receipts bind the values above, the original ruling digest and the unique run-3/run-4 state/output locations. The driver ran under `nice -n 10`, with `TMPDIR=/home/spite/Projects/sp6-evidence` and `PYTHONDONTWRITEBYTECODE=1`. The live credential is read from `/home/spite/.config/codexpro/http-token`; its contents are never published. Both commands already completed and must not be submitted again. Reproduction in a later task requires a newly authorized window, fresh non-overwriting run paths and the unchanged safeguards; this interface is not authorization for another run tonight.

The inner gate binds actual prepared/ungranted pending-owner recovery and requires the ungranted command never to execute. It is followed by exact owned cleanup. The outer report embeds the inner receipt and hash only if that child actually ran, checks archive identity/hash before and after, and requires no new WAL/SHM/journal sidecars. Hashing the archive warms its filesystem cache, so this is not a cold-disk claim. The inner receipt's `source_commit` identifies the TypeScript reference; the outer `candidate_source_commit` identifies the supplied Go build source.

Every child has an atomic nonce/birth/group/session/cgroup receipt and a private session. Worker CPU progress and actual compiler descendants are required across a completed candidate measurement; sampled observations do not claim that one compiler subprocess occupied every instant. The compiler wrapper is stopped after measurement, so compiler completion is not required or claimed. The 40 external workers coexist with the original inner fixture's two workers only when that inner fixture actually starts.

Cleanup selects only witnessed PID/start/group identities. The guard's callback snapshots known handles under a short non-I/O lock, then signals owned groups without holding that lock. Durable prelaunch preparation is outside enumeration; a child started during an abort race is bound in memory and stopped before it can be forgotten. Postlaunch receipt writes cannot delay publication of its owned handle. The inner proof receives up to 60 seconds for its own server/helper cleanup before bounded escalation. Unknown launch outcomes, receipt failures and surviving nested owners fail the receipt; existing launch receipts are never overwritten. No process is selected by a port, service name or unverified PID. Live CodexPro receives only read-only health probes, never a lifecycle command or signal.

## First loaded attempt and native compiler correction

Attempt 1 reached one-minute load **28.507** but failed before candidate startup: the managed `go` shim requested a separate ssd-dev build scope and no actual compiler descendant appeared within the fixed 15-second entry bound. Its report remains a failed harness attempt. Wrapper-group cleanup alone did not establish quiescence of the separately allocated scope; the separate exact-run status/cleanup check was retained before another run. No startup-latency result is claimed from attempt 1.

Attempt 2 invoked the already observed native toolchain directly within its own witnessed lower-priority group. The native Go ELF SHA-256 is `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b`; the compile ELF SHA-256 is `c331d8fc6c4ae9dbe20e8198435db0455f2a5ca55d2d02fc6d7337c906be8283`. Preflight requires stable identities and `go version go1.27.3 linux/amd64`. Observed descendants must remain in the owned compiler group/session/cgroup. Toolchain hashing warms those images and is part of the declared fixture. The load threshold, worker deadlines, frozen startup script and three-second candidate limits were unchanged. Later guarded attempts validated the same native inputs but aborted before compiler load or candidate launch.

## Attempt 2: recovered diagnostic result and hang timeline

Job `job_19a613e8` ran **2026-10-31 22:57:46.974–22:59:07.240 UTC**. Its 40 nice-19 worker starts were **22:57:57.193–22:57:58.766 UTC**. All **42 outer groups and three inner groups** were quiescent by **22:59:07.174 UTC**, with zero cleanup errors. Worker UTC times are reconstructed from same-boot monotonic witnesses within the exact job timestamps. At load **29.661**, the candidate answered health in **126.246 ms**, its first initialized file read in **327.330 ms**, and three blocked-helper reads in **14.525–18.928 ms**.

The guard absence is established from both the executed wrapper and the retained command; it is no longer an unresolved investigation. **No live-health guard ran**, so neither a fired nor a non-fired guard outcome can be claimed. [Attempt-2 adjudication](evidence/startup-loaded-2-adjudication.json), [outer receipt](evidence/startup-loaded-2.json) and [inner receipt](evidence/startup-loaded-2-startup.json) preserve the actual measurements and diagnostic disposition. The old `loaded-startup-guards-2.json` records twelve offline unit tests, not a runtime health monitor.

Cleanup completed about **53 seconds before** the coordinator's approximate **23:00–23:17 UTC** live-hang window. This timing does not establish or exclude causality. Preserve the approximate nature of the operator-reported incident boundaries and the distinct exact worker/cleanup witnesses.

## Guard implementation and window controls

The monitor covers preflight, worker launch, measurement and owned cleanup. Poll starts target four-second intervals and may not be more than five seconds apart. A failed response, elapsed time over one second, missed poll deadline, failed admission check or monitor failure aborts the run and requests TERM for witnessed owned load. An aborted run cannot pass even if candidate timing assertions happened to pass. The final [offline validation](evidence/guarded-startup-driver-validation.json) records **23 monitor tests and 22 integration-driver tests passed**, with zero load workers and no live-service probes. The frozen startup script remained byte-for-byte unchanged. Offline passes do not qualify runtime startup.

Admission inspects actual process argv, identity and accessible cwd rather than searching embedded shell text. Protected systemd/PAM/SSH supervisor cwd entries are classified only by exact command, uid, cgroup and applicable parent identity; arbitrary unreadable same-user processes fail admission. This classification permits no access to protected data or control of those supervisors. No broad ignore list for retained landing directories is used.

The coordinator requires row-31 WINDOW OPEN, then SP-6 START before load and END after verified cleanup. The 23:39 window, corrected at 23:42 to expire at 23:55, was released unused at 23:49:40 UTC: SP-6 had started no new load. The coordinator separately reported stopping a frontend-only landing started around 23:41 before it created an SSD worktree or stack; no SP-6 measurement ran in that interval.

## Executed guarded attempts

| Attempt | Exact outer job interval, UTC on 1 November | Result | Receipt SHA-256 |
| --- | --- | --- | --- |
| 3, `job_d27e76f6` | **00:05:58.858–00:06:07.042** | **ABORTED** during preflight; no workers/compiler/candidate | `05e2afb53159f9f0b4bd8356d9bed217c28303502c171114da523638911102b7` |
| 4, `job_ed2018c9` | **00:10:48.863–00:11:10.344** | **ABORTED** after 40 workers; no compiler load or candidate startup | `1863b961816c88639c4904518f2faa51723bbdf49951ac953f76f493b17c26b8` |

[Attempt 3](evidence/startup-loaded-3.json) passed admission but the initial live-health sample timed out at **1005.141 ms**. Its preflight guard fired at **00:06:06.763 UTC**; the periodic monitor did not start, and **no children were launched**. Cleanup errors were zero, the private compiler cache was removed, and the archive stayed unchanged without sidecars. Its empty cleanup list reflects no launched owners, not unobserved cleanup.

At **00:08 UTC** the coordinator allowed **one final window**, requested five preflight health reads over about 20 seconds and said to hand back with the gate open if that final attempt aborted. Although the ruling suggested a relaxed load guard, the user's explicit **same-guards** instruction governed: **one failed or over-one-second health response still aborts**. The separate five-read preflight, `job_487d9c0a`, passed in **20.031 seconds**, with **1.393–2.076 ms** latencies. Attempt 4 also passed its own admission/health preflight before worker launch.

[Attempt 4](evidence/startup-loaded-4.json) started 40 nice-19 workers at **00:10:59.562 UTC**. The first loaded live-health poll timed out at **1004.563 ms**; the guard fired **00:11:04.535 UTC**. TERM was issued to all 40 owned groups by **00:11:04.543**, within **8.032 ms**. All groups were quiescent by **00:11:10.069**, producing a **10.506-second loaded window** with **zero cleanup errors**. **No compiler load or candidate startup began**, so there are no qualifying candidate timings. The immutable archive remained unchanged and its sidecars absent; the private cache was removed.

The [fresh END/postcheck](evidence/startup-loaded-4-end.json) found **no surviving exact owned PID/group identities** and refused IPv4 loopback connections on **7210 and 7211**. Connection refusal records those private endpoints as unavailable at that observation; it is not a complete host-wide PID/listener inventory. END was posted to row 31 at **00:12:19 UTC**, releasing the window. This establishes the observed guard-triggered cleanup; it does not close the loaded-startup requirement. Per the 00:08 ruling, **there are no further load windows tonight**. Handback keeps the gate **OPEN / pre-cutover blocker**.

## Canonical retained evidence

All original receipts remain unchanged. Legacy runtime paths are mapped to verified private archives under `/home/spite/Projects/sp6-evidence`; raw contents and private credentials are not committed. New public receipts and compiler caches were written under `~/Projects`, and the private run states were archived there after cleanup.

| Verified archive | Contents and mapping | SHA-256 |
| --- | --- | --- |
| `private-proof-before-guarded-run.tar` | **3,012 regular files**, 1,263,472,640 bytes; [checkpoint mapping](evidence/private-proof-archive.json) maps original `~/.codexpro-go-proof/` paths to `private-proof/` members | `c4f9999885718951957d41bfa4a9bf1c0986d9a6a190675d93bd04e59a53c6ff` |
| `private-proof-startup-loaded-3.tar` | **One regular file**, 10,240 bytes; [attempt-3 supplement](evidence/startup-loaded-3-private-archive.json) | `e1f95b3c4924058d3a0dd8e599da99361e768ae1afc9e8522997a8921657b498` |
| `private-proof-startup-loaded-4.tar` | **121 regular files**, 256,000 bytes; [attempt-4 supplement](evidence/startup-loaded-4-private-archive.json) | `71a139888b50fe974eeb5482c5ec5a951ead6a21ef4901f8dfc46a268fecf54a` |

All three archive receipts verify regular-file contents with zero source-identity changes and no live-state access. Their manifests retain exact member mappings and hashes. Canonical placement is complete. The [final census](evidence/final-readonly-census.json) ran once at 1 November 00:18 UTC; its raw flags are retained alongside the [saved-evidence role adjudication](evidence/final-readonly-census-adjudication.json), which separates four observed host supervisors from 154 absent private-proof identities without a new live observation. The exact final publication/delivery identity accompanies [RESULTS.md](RESULTS.md), the coordinator message and registry row 31. The loaded-startup gate remains OPEN; no further load attempt or cutover was performed.