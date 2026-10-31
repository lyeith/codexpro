# Process ownership and host resource contract

Captured 2026-10-31 for SP-6 stages 1–2. This is an implementation contract and test plan, not an authorization to start implementation or change the live service.

## Evidence and scope

CodexPro references below are the requested release baseline `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a` in `/home/spite/Projects/codexpro-go`. Main sources: `src/jobs.ts`, `src/jobRunner.ts`, `src/jobScope.ts`, `src/jobResources.ts`, `docs/HOST_JOB_RESOURCES.md`, `test/job-resources.test.mjs`, and `scripts/job-resources-smoke.mjs`.

The root agent verified at 16:02 UTC that `/home/spite/.local/bin/ssd-dev` resolves to `/home/spite/Projects/ssd-dev-tools/bin/ssd-dev`, whose launcher imports that checkout's `src/ssd_dev/cli.py`. Its installed `src/ssd_dev/external.py` SHA-256 matches the source read for this report:

```text
d427b0c12cb9eef22de01509032f4b9f3ff28d1d83192a5491178198fc032ccb
```

Additional helper sources:

| Source in `/home/spite/Projects/ssd-dev-tools` | SHA-256 |
| --- | --- |
| `src/ssd_dev/cli.py` | `1d6d935bb58741de422471214c83d186bbc04fb230935a339e918198ec6acfc7` |
| `docs/external-api.md` | `671a3dc2f4f3b074bfc876f83fbf4e853b52779aef4c18bd5306b881eb24ef0a` |
| `bin/ssd-dev` | `3776cdecdb2220a1a2357fbeeb171b6ea952644e61e0577b77d1c303430ca9ad` |

The October 5 `docs/codexpro-adapter.md` is historical. The current external API explicitly supersedes its retired pinned-cwd/scratch and integrity-check details. This report follows current code and `docs/external-api.md`.

This investigation only read source. It did not invoke allocation, activation, cancellation, maintenance, service changes, or signals against live jobs. Observed runtime cgroup facts were supplied by the root agent and are identified separately below.

## 1. Ownership boundaries

CodexPro owns the complete process tree through one unique systemd scope per job. The existing detached runner owns output capture, its command process group, deadlines, and stop intent. `ssd-dev external` owns scratch and cache claims attached to that existing scope. It never creates a second process supervisor, joins the controller to a cgroup, signals a process, or changes build concurrency. A child's exit, a launcher PID disappearing, a closed inherited FD, or empty immediate-child list does not release the durable resource claim. See helper `docs/external-api.md:3–5,87–98` and `external.py:225–281,346–384`.

Preserve three independent identities:

| Identity | Purpose | Baseline representation |
| --- | --- | --- |
| Public job ID | MCP lookups and output references | `job_` plus eight lowercase hexadecimal characters |
| Process owner incarnation | Prevent adopting/signalling a reused scope | `codexpro-job_<id>-<fresh32hex>.scope` plus boot UUID, actual InvocationID, and complete ControlGroup |
| Host allocation identity | Own exactly one scratch/cache claim | Fresh 32-hex `run_id` and independent 32-hex `registration_id`, plus the absolute helper path |

The persisted claim is `{version:1,helper,run_id,registration_id}`. Version 1 belongs to CodexPro's claim schema; it is not a helper API negotiation field. Persist the chosen helper path with each claim and use that path when reconciling old jobs, rather than silently switching claims to the server's current helper setting. Source: `src/jobResources.ts:6,20–32`.

Public job outcome and ownership settlement are independent. A failed, timed-out, stopped, or succeeded job may retain `quiescent:false` while its complete owner cannot be verified. Such a job must remain recoverable and exempt from ordinary metadata/log expiry. Source: `src/jobs.ts:674–708` and helper `docs/external-api.md:128–132`.

## 2. Exact helper command contract

All examples below are argv sequences, not shell strings. Use an absolute helper executable and pass arguments directly. CodexPro's baseline wrapper uses a 10-second timeout and a 1 MiB response cap for begin/finish/cancel. Nonzero exit or execution failure maps to `Host job resource operation failed.`; invalid JSON maps to `Host job resource response was invalid.`. It deliberately does not expose helper stderr through MCP because paths may be private. Source: `src/jobResources.ts:7–11`.

The helper supports a global `--config <path>` before `external` for an isolated test policy. Production claims use the host's installed policy. Do not add a second production controller over the live job store.

### 2.1 Begin: durable allocation before command permission

Baseline invocation:

```text
<helper> external begin
  --purpose tool
  --cwd <canonical-absolute-existing-directory>
  --owner-unit <fresh-unit.scope>
  --cache-consumer
```

The current helper additionally accepts caller-generated identities, which the Go design should use:

```text
  --run-id <fresh32hex>
  --registration-id <independent-fresh32hex>
```

`--purpose` permits `build`, `test`, `tool`, or `service` and defaults to `tool`. Generic build/test/tool purposes claim the cache; service omits that claim unless `--cache-consumer` is present. Optional `--pin` retains diagnostic scratch. The unit must be absent according to the user manager (`LoadState=not-found`) before allocation. Cwd must already exist, be absolute, and equal its canonical resolution. Both IDs must be fresh and never reused, including after receipt expiry. If IDs are omitted, the helper generates independent UUID hex values. Source: helper `cli.py:177–193,310–330`; `external.py:186–213`; `docs/external-api.md:51–72`.

Success exits zero and prints one JSON object:

```json
{
  "run_id": "<32 lowercase hex>",
  "registration_id": "<32 lowercase hex>",
  "scratch": "<absolute allocated directory>",
  "environment": {
    "TMPDIR": "<scratch>/tmp",
    "GOTMPDIR": "<scratch>/go-tmp",
    "GOCACHE": "<host shared Go cache>"
  },
  "owner": {
    "kind": "unit",
    "unit": "<fresh-unit.scope>"
  }
}
```

The environment map may contain additional host tool-cache variables. Treat it as extensible; the baseline adapter validates only the two IDs and relies on activation to apply the environment. Returned owner is still a reservation: the actual InvocationID and physical cgroup identity are captured during activation, not guessed from the parent's environment. Source: helper `external.py:179–213,240–258`.

The helper also supports existing delegated raw cgroups through mutually exclusive `--owner-cgroup <absolute-path>` with required `--owner-device <int> --owner-inode <int> --owner-boot <uuid>`. Optional `--owner-parent-unit` and `--owner-parent-invocation` must appear together and bind an enclosing actual manager invocation. The cgroup must be a canonical, user-owned directory under `/sys/fs/cgroup` with a readable v2 `cgroup.events`. The scope path is the present CodexPro contract; Go need not implement a second raw-cgroup backend to preserve it. Source: helper `external.py:38–52,107–121,197–200` and `cli.py:313–327`.

### 2.2 Exec: activation inside the original owner

```text
<helper> external exec <run-id>
  --registration-id <registration-id>
  --
  <runner-executable> <runner-arguments...>
```

The explicit `--` boundary is required for command arguments. This process must already run inside the allocated scope. The helper verifies the registration nonce, `created` receipt state, current boot, unchanged physical cache policy, actual manager InvocationID and ControlGroup, and its membership inside that owner. It checks the scratch path and directory, then atomically changes the receipt to `running` before exec. Activation and cancellation share an exclusive metadata gate, preventing a late activation after revocation. Source: helper `cli.py:194–204,242–258,331–335` and `external.py:225–258`.

For a scope, membership may be inside its descendant group; the current CodexPro runner subsequently requires its own cgroup to equal the scope's ControlGroup exactly. Inherited `INVOCATION_ID` is not proof: the helper removes it and installs the actual bound invocation. It changes cwd to the registered cwd; applies host TMPDIR, GOTMPDIR, GOCACHE and tool-cache variables; sets `SSD_DEV_RUN_ID`, `SSD_DEV_RECEIPT`, and `SSD_DEV_SCOPE`; removes `NPM_CONFIG_CACHE`; preserves other arguments, environment and standard streams; then uses direct exec. Resource injection must occur after CodexPro's restricted-environment construction. Do not recursively wrap the payload in `ssd-dev run`. Source: helper `external.py:87–104,263–276` and `docs/external-api.md:80–98`.

There is no success JSON response after exec: the helper becomes the runner. Policy failures exit 125; executable-not-found exits 127; permission-denied exits 126. Argument parser errors can exit before the policy handler. A repeated activation, changed owner, wrong nonce, revoked receipt, changed cache policy, or unavailable scratch refuses startup.

### 2.3 Finish: only after the entire owner is quiet

```text
<helper> external finish <run-id>
  --registration-id <registration-id>
  --exit-code <integer-0-through-255>
```

Success exits zero and prints:

```json
{
  "run_id": "<run-id>",
  "status": "succeeded",
  "quiescent": true,
  "cancelled": false
}
```

A nonzero command exit produces `status:"failed"`. Repeating finish for a previously verified terminal receipt returns its existing terminal status unchanged; an already cancelled receipt returns `cancelled:true`. `succeeded`, `failed`, `cancelled`, and `abandoned` are recognized terminal statuses in the helper. Unknown command outcomes should be reported as failure code 125, never converted to success. The baseline adapter uses zero for succeeded jobs and otherwise recorded nonzero exit or 1; changing only that internal fallback to 125 must not alter public job status/error contracts. Source: helper `external.py:20,284–338`; `docs/external-api.md:100–132`; CodexPro `src/jobs.ts:691–708`.

The controller drains the complete scope first. The helper independently verifies quiescence using the persisted original owner:

- A different host boot proves the old process tree cannot survive.
- A systemd unit absent from its manager proves absence. Otherwise the saved InvocationID must match, the unit must be inactive/failed, and its group must be absent from the manager or physically quiet.
- For a raw cgroup, a pinned directory descriptor must still match original device/inode/boot and show `populated 0`. A missing or replaced pathname alone is not proof. If its original raw path is absent, a saved exact enclosing-unit binding can provide the permitted parent proof.

Source: helper `external.py:55–76,124–176`. The host proof can recover a claim even if CodexPro lost its local started receipt; preserving this fallback is required.

Wrong nonce, populated or unresolved owner, invalid exit code, or an unreadable claim fails and retains ownership. Only a successful, unpinned, verified-quiet receipt permits immediate scratch removal. Failure/cancellation diagnostics remain for the configured interval; the current documentation guarantees at least 24 hours. The terminal receipt is durably published before disposable cleanup, so a busy scratch directory does not resurrect a settled cache claim. Host maintenance can retry cleanup later. Source: helper `external.py:284–338`.

### 2.4 Cancel: revoke only an unactivated allocation

```text
<helper> external cancel <run-id>
  --registration-id <registration-id>
```

Success for a revoked reservation prints:

```json
{
  "run_id": "<run-id>",
  "status": "cancelled",
  "cancelled": true,
  "quiescent": true
}
```

Cancellation applies to `allocating`/`created` claims. It never signals an active owner and refuses an activated claim; drain and finish that claim instead. Repeating cancellation on any already verified terminal receipt returns that existing outcome rather than rewriting it. Accordingly the baseline `cancelResources` treats success as `cancelled===true`, while `finishResources` accepts any `quiescent===true`. Source: helper `external.py:284–338`; CodexPro `src/jobResources.ts:28–32`.

If allocation does not yet exist, cancellation creates a nonce-bound revocation tombstone under the same allocation/activation metadata gate. Delayed begin or exec then refuses. A different nonce cannot overwrite that tombstone. This supports safe recovery when the caller durably recorded IDs but did not receive begin's response. Tombstones expire under the ordinary receipt retention policy, so IDs must still never be reused. Unactivated reservations can be revoked by host maintenance after the documented minimum retention period; absence of a future scope alone does not permit early cleanup. Source: helper `external.py:23–35,293–315`; `docs/external-api.md:134–155`.

### 2.5 Inspect and errors

`ssd-dev inspect <run-id>` returns receipt/unit-state evidence, not permission to adopt another owner's processes. For an existing external claim its fields are `receipt`, `unit_state`, `alive`, `pending`, `quiescent` and `allocation_claim`. Pending reservations report `alive:false,pending:true,quiescent:false,allocation_claim:"reserved"`. Verified terminal claims report quiet and either `settled` or `revoked`. Active, unresolved and prior-boot observations are distinct. Source: helper `external.py:147–176` and `cli.py:391–401`.

A missing inspect receipt has a legacy fallback against an `ssd-dev-<id>.service` name. That is not a recovery proof for an unknown CodexPro scope. Use persisted allocation IDs and explicit finish/cancel semantics instead of treating a missing inspect receipt as success.

The helper's caught policy/filesystem/manager/JSON exceptions print `ssd-dev: <message>` to stderr and return 125 (`cli.py:16,431–433`). MCP must keep the baseline sanitized errors. A helper timeout or malformed response is an unresolved operation; it does not prove no allocation happened. Retain the claim and retry settlement with its original IDs.

## 3. Baseline launch, grants, capture and settlement

The baseline uses a detached runner so an HTTP disconnect or server restart does not automatically kill an accepted job. Source: `src/jobs.ts:300–410` and `src/jobRunner.ts`.

1. Validate capacity, input-output availability and retained-output reservations. Preserve background deduplication by command/cwd/sorted input IDs.
2. Generate public job ID, unique scope incarnation and independent grant nonce. Baseline calls begin before it persists the job; Go should close that crash gap by persisting caller-generated allocation IDs and intended scope first.
3. Create bounded capture files and a runner spec containing command, cwd, paths, deadline, timeout, output limit, scope and grant nonce.
4. Persist the prepared job and any managed-operation linkage before a command can execute.
5. Spawn `systemd-run --user --scope --quiet --collect --unit <scope-stem> -- <helper> external exec <claim> --registration-id <nonce> -- <runner> <spec>`. It is the existing scope, not a helper-created second owner.
6. Record launcher PID/process identity and granted registration durably, then exclusively create the nonce grant file. Go may use an equivalent durable execution gate while preserving crash behavior.
7. The runner independently captures actual scope identity, exclusively writes its started receipt, and waits at most 10 seconds for the exact grant before launching Bash. Without a grant it reports lost/timeout/stopped without executing the payload.
8. Capture stdout/stderr, enforce combined raw/output-render budgets, and preserve output paths, cursor behavior, redaction and MCP job result shapes.
9. Write a versioned result atomically before the numeric exit marker. Persist public terminal outcome separately from complete-owner settlement.
10. Drain/reconcile the original exact scope, then finish the host claim; cancellation is a fallback for an allocation that never activated. Retain and retry unresolved ownership.

The runner uses Bash `-lc` in a detached POSIX process group. It watches control intent at 100 ms intervals; timeout is enforced by both elapsed monotonic time and the wall-clock deadline. Stop first sends TERM to the command group, then KILL after 1.5 seconds. After the main command exits it drains remaining process-group members. The manager has a separate five-second escalation for runner jobs and independently settles the full cgroup, which contains descendants that used `setsid` and closed output FDs. The group's local quiet result is never sufficient by itself for a scoped job. Source: `src/jobRunner.ts` and `src/jobs.ts:525–708`.

Baseline results include `version:2,exit_code,signal,stop_reason,quiescent,cgroup,scope_identity,finished_at_ms`. Public outcome mapping remains `timeout→timed_out`, `stopped→stopped`, another stop reason→failed, and otherwise exit zero→succeeded. Preserve partial output and the difference between exit/signal/stop reason. Historical jobs without a host resource claim must not acquire an invented claim during migration.

## 4. Exact scope identity and signal authority

CodexPro's saved identity is:

```json
{
  "version": 1,
  "unit": "codexpro-job_<hex>-<fresh32hex>.scope",
  "boot_id": "<host boot UUID>",
  "invocation_id": "<actual32hex>",
  "cgroup": "<complete absolute ControlGroup>"
}
```

`src/jobScope.ts:27–43` validates the unique scope-name format, expected unit, boot/invocation format, absolute traversal-free cgroup and matching basename. Capture requires actual `LoadState=loaded`, `ActiveState=active` and the runner's `/proc/self/cgroup` to equal the manager's ControlGroup.

Inspection distinguishes active, quiescent, unknown and mismatch. Manager failures or malformed evidence are unknown. A current unit with a different InvocationID or ControlGroup is mismatch, never permission to signal it. The baseline recursively examines `cgroup.procs`, including child cgroups. It signals only a verified active exact owner through:

```text
systemctl --user kill --kill-whom=all --signal=<signal> <exact-unit>
```

Never fall back to signalling an arbitrary stored PID for a scoped job whose scope identity is missing or mismatched. Baseline PID start-time identity remains useful for launcher observation, not ownership substitution. Source: `src/jobScope.ts:46–87`; `src/jobs.ts:134–142,525–708`.

The current settle helper may repeatedly spawn synchronous manager calls while waiting up to one second. Go should keep the same identity checks and signal authority while moving bounded inspection to asynchronous workers with timer backoff. No database transaction or global request lock should span helper/systemctl execution, process drainage or capture IO.

## 5. Resource and CPU separation

Read-only runtime evidence supplied by the root agent at 15:36 UTC showed both a foreground command and a private TypeScript background job in sibling `codexpro-job_*.scope` groups with CPUWeight 100 and unrestricted `cpu.max`. The live server was in `codexpro.service` with CPUWeight 1000. Therefore the measured current service already separates these jobs from the server's cgroup; it is incorrect to attribute all observed contention to children sharing the server group.

Go runs as a separate private process during this job. It must not modify the live service, its weights, delegation, config or state. Preserve separate job ownership and apply the required nice 10 to heavy/test/build children before payload execution. A permanent higher-weight server leaf/delegated sibling layout belongs to a future reviewed deployment decision, not a stage-4 cutover.

Baseline scope detection is disabled outside a systemd invocation unless `CODEXPRO_JOB_SCOPES=1`. An absolute configured host helper with unavailable/disabled scopes fails startup; it does not silently fall back to unowned jobs. The private Go process must explicitly enable/probe user scopes and pass the necessary `XDG_RUNTIME_DIR`/`DBUS_SESSION_BUS_ADDRESS` after restricted-environment construction. Source: `src/jobs.ts:145–177,348–352` and `src/jobResources.ts:13–18`.

The host helper must execute in the host user namespace. Existing CodexPro integration documentation records that private-user mappings induced by some sandbox settings can make root ownership unmapped and cause helper policy refusal. Preserve host namespace semantics; do not solve this by modifying the live unit. Source: `docs/HOST_JOB_RESOURCES.md`.

## 6. Suggested Go package boundary

This is an interface design, not implementation code. The process package owns live subprocesses, signals, timers and identity proofs. The job repository owns durable prepared/granted/result/settlement transitions. The output package owns bounded files, redaction and cursor views. Transport owns public DTOs and errors. Keep these dependencies one-way.

| Interface operation | Durable input/output contract | Blocking work outside DB transaction |
| --- | --- | --- |
| `PrepareJob` | Allocate public ID, owner incarnation, grant nonce and caller-generated resource IDs; reserve input pins and capacity in one committed admission | None |
| `BeginResources` | Stored claim and canonical cwd → validated allocation response; save observed result before launch | Helper begin, bounded |
| `StartOwner` | Stored prepared job/spec → launcher identity; runner can wait for durable grant | systemd-run/helper exec |
| `GrantJob` | CAS prepared→granted, persist owner linkage, then release exact execution gate | Grant publication after durable commit |
| `ObserveOwner` | Saved exact identity → active/quiet/unknown/mismatch with timestamp | Manager/cgroup inspection |
| `RequestStop` | Persist stop reason idempotently; enqueue signal/drain for original owner | Control publication and exact-owner signalling |
| `RecordResult` | Runner result → public terminal status; ownership remains pending until separately verified | Read already bounded result/output metadata |
| `SettleResources` | Original claim + outcome → verified quiet or unresolved retry | Drain, finish/cancel, bounded helper calls |
| `RecoverJobs` | Prepared, active and terminal-unresolved records → scheduled observation/settlement | No synchronous sweep on every tool call |

Use per-job serialized state transitions with revision/CAS checks so a stale observation cannot overwrite a newer stop/result. Run helper/inspection work in a bounded queue with separate control capacity from user jobs; signal and deadline control must remain available during load. Keep a terminal job's recovery identity even if its display/output has otherwise aged out. The authoritative durable claim must never be released because a timer callback or API request was cancelled.

A detached reexec mode in the Go binary is a suitable runner boundary. It needs its own finite grant deadline, process containment, capture and result publication; it must not rely on the API server's in-memory goroutines surviving. Retain the result/spec/identity format sufficiently to import historical jobs, or explicitly version and migrate it in an offline snapshot. Never let TypeScript and Go both control the production jobs directory.

## 7. Required fault and parity tests

Run these only against the private Go/TypeScript instances and disposable job/helper state, with bounded nice 10 load. Existing baseline tests and smoke script supply reusable expectations.

| Case | Required result |
| --- | --- |
| Begin succeeds but response is lost | Durable caller IDs remain; cancellation or subsequent proof settles the exact claim |
| Cancel completes before delayed begin | Nonce-bound tombstone refuses later allocation/exec; payload never runs |
| Activation races cancellation | Shared helper gate yields exactly one permitted transition; active cancellation cannot kill the process |
| Manager dies before grant | Runner's finite wait expires; no Bash command executes; claim remains recoverable |
| Manager dies after grant | Detached runner enforces original deadline and produces output/result; restart recovers without duplicate execution |
| Runner cannot publish started identity | Payload never starts; host proof can settle the activated helper claim |
| Main command exits while a `setsid` descendant survives | Full exact scope is drained before scratch/cache release |
| Terminal result exists but owner inspection times out | Public terminal status may be reported; metadata/claim are retained and retried |
| Scope name exists with foreign InvocationID | No signal, adoption or cleanup based on that owner; remain unresolved |
| Prior-boot owner | Old tree is quiescent; finish/retention can progress using correct persisted claim |
| Helper absent, relative, non-Linux, or scopes unavailable | Refuse configured host-resource launch with sanitized error before payload execution |
| Wrong registration nonce | No outcome mutation, cancellation or adoption |
| Successful quiet unpinned owner | Host scratch removed or marked for bounded maintenance; claim stays durably settled |
| Failed/cancelled owner | Diagnostic scratch retained according to host policy; public result preserved |
| Legacy job has no resource claim | Reconcile its existing ownership only; do not manufacture a claim |
| Input-output pins and retention race | Admission reservation remains valid until dependent jobs release it |
| Two concurrent commands | Distinct scratch and Go temporary paths, shared host Go cache, independent durable claims |
| Heavy payload uses all bounded worker CPU | API/control latency measured separately; child scope and nice values verified |

Evidence anchors: `test/job-resources.test.mjs` covers invalid helper/identity, previous-boot recovery, unresolved retention, helper/scope refusal and host-proof recovery. `scripts/job-resources-smoke.mjs` exercises overlapping environments, foreign invocation refusal, escaped descendants, timeout, missing started proof and manager restart. Extend those fault boundaries without creating a second controller over live state.

## Conclusion for implementation review

Preserve one exact process owner, one durable host claim, and a separate execution grant. Close the pre-begin crash gap with caller-persisted identities. Treat terminal outcome and verified quiescence as separate durable facts. Keep helper settlement asynchronous and bounded, but never acknowledge resource release from uncertainty. These are correctness requirements independent of whether the Go store uses SQLite, journals or another internal layout.
