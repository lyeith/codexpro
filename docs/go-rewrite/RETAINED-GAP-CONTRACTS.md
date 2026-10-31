# Supplemental retained-contract proof

This version adds the exact native invalid-URL envelope to the unexecuted attachment/direct supplement, retaining the three HTTP failure cases and the reviewed cleanup budget. The earlier staging bundles remain intact. The driver closes the successful-attachment and direct-helper gaps identified after the first 94-step general replay. It owns a fresh TypeScript/Go pair; it does not attach to the existing proof processes and never changes the live service. The separately owned activity-route supplement can run against this pair before cleanup.

## Execution preconditions

Both ports 7210 and 7211 must be free. The driver checks this before creating state, a token, an attachment listener or either server. The exact immutable TypeScript 0.31.0/44bb76b release and the candidate binary must be supplied. State and output paths must be new. State must be beneath `~/.codexpro-go-proof/` or an explicitly named `/tmp/sp6-retained-gaps-*` directory; the ext4 proof root is preferred for the recorded run.

The exact source-supported TypeScript import override is frozen in the runner's `IMPORT_POLICY` record: `CODEXPRO_IMPORT_ALLOW_LOOPBACK=1`. The captured `src/importOps.ts` at the deployed commit has SHA-256 `293fdfd1ba6c9412ed1d0e33b22ed585102eeb02ecf6429f7f733d9ab957d0c1`. Lines 130–151 admit HTTP and a loopback address only with the override, and lines 332–348 show the actual import caller passing the environment-derived flag. The runner refuses launch if this policy record is missing or malformed. This override applies only to its new private processes.

Both test processes run direct mode (`worktreeMode=off`), full tool tier, full Bash permission, workspace writes, audit metadata and one configured fixture project. The reference's retired descriptors are recorded separately as approved inventory changes; they are not counted as unchanged cases. No MCP/safe permission exception is needed for this direct pair.

Each process has its own state, token, project catalog, creation root, instruction directory, jobs, audit and temporary directory. The temporary directory is deliberately on the same filesystem as the disposable workspace so a successful overwrite tests the import contract rather than an incidental cross-device rename failure. `maxImportBytes=1024` is applied to both private processes and verified through `server_config` before any case.

## Cases

Eight direct-helper cases compare complete MCP envelopes: initial session listing, `open_current_workspace`, selected membership after opening, repeated open with a compact tree, stable open time on repeat, independent initial state in a second session, opening in that session, and both sessions' final state. The runner validates source-derived workspace IDs, project identity and timestamp causality. The existing strict admin helper checks recent-activity summaries against adjacent audit watermarks and actual retained receipts.

The private instruction directory is bound to the actual owned process with PID/starttick checks and must equal its declared `state/empty-codex`. Only that directory's absolute path and exact source `[home]/…` spelling receive a common alias. Unknown paths remain visible. Tool-mode fields and summary text remain exact contract values; a `standard` versus `full` difference is a failure.

Sixteen attachment cases cover text, binary and PNG downloads; `view_image` of the imported PNG; collision refusal; overwrite; a same-origin redirect; expected-hash failure; declared/detected MIME mismatch; exactly-at-limit success; advertised overflow; streamed overflow without a length header; HTTP 404; a redirect without Location; the source's six-fetch redirect limit; and native malformed-URL failure before any download. The last case requires the complete `TypeError: Invalid URL` envelope with no domain code or metadata. The fixture serves fixed bytes over an owned ephemeral loopback listener. It performs no external request and logs no authorization value.

Every successful attachment result is checked against the actual downloaded and resulting bytes: full SHA-256, byte count, MIME declaration/detection/status, verification flag, overwrite flag, file ID and nullable file name. Expected failures must leave an existing destination unchanged or a new destination absent. The fixture records exact requested paths so a redirect or unexpected additional fetch is visible. The complete result, including text and omitted/null distinctions, is compared after only validated workspace/root/request/timing bindings.

No Bash command or job is submitted by these twenty-four cases. The optional activity supplement may create its own bounded sentinel job under its own verified ownership checks. It runs sequentially after all twenty-four cases match, while the same pair is still alive, and keeps its own raw/aggregate evidence. The driver refuses to start that supplement unless its complete 150-second budget remains within the driver's deadline.

## Invocation after the native candidate gate

From `/home/spite/Projects/codexpro-go`, run under `nice -n 10` and a 400-second external timeout with a 90-second termination grace. The grace permits supplemental cleanup, all bounded MCP session closes, and both owned process-group stops:

```sh
nice -n 10 timeout --signal=TERM --kill-after=90s 400s \
  python3 docs/go-rewrite/retained_gaps_contract_suite.py \
  --release /home/spite/apps/codexpro/releases/job45-dashboard-20261030-44bb76b \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --state-parent /home/spite/.codexpro-go-proof/retained-gaps-20261031-01 \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/retained-gaps-01.json \
  --activity-supplement
```

Omit only `--activity-supplement` when running the import/direct proof independently. The standalone route runner can also consume the two explicit launch manifests while this pair is alive; concurrent mutation lanes are prohibited.

The driver has a 330-second internal budget. Per-call timeouts and process cleanup waits are bounded. It sends signals only to the newly launched private process groups whose PID, start time, group and user ownership it verified. It neither uses a service manager nor chooses a process by listener/name. Repeated cleanup uses a cached receipt so it cannot signal a reused PID. The loopback attachment server is also shut down and joined.

Require `passed:true`, all twenty-four cases completed, zero failed cases and empty cleanup errors. If the optional supplement is requested, its report must also have `passed:true`. The driver always reports `stage4_complete:false`; this lane cannot establish the other outstanding stage-4 gates.

## Offline tests and evidence

The offline tests require no network or native Go compiler. They check occupied-port refusal before writes, absent policy refusal, exact fixture headers/length boundaries, external/credential-bearing route rejection, cached cleanup ownership, session leakage and time reset detection, raw-byte/hash validation, omitted-versus-null preservation, symlink refusal, unchanged destinations on errors, exact uncredentialed download requests, and rejection of generic/domain names, invented codes or effects in the malformed-URL case. All sixteen offline guard tests passed at staging time.

The raw transcript is `retained-gaps-raw.jsonl.gz` beneath the new private state root. The report records its hash/count/size, launch and source identities, fixture result digests, descriptor deltas, case diffs and cleanup receipts. Failed state is preserved for review. Tokens and MCP session credentials are checked before every evidence append and never emitted in the report.

At staging time, these are prepared cases and offline guard results, not an executed TypeScript/Go parity result.
