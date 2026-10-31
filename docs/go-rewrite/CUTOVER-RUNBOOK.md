# Go cutover and rollback runbook

Status: **planning document only**. SP-6 is authorized for stages 1–4. No command in the cutover or rollback sections has been executed as part of this task. The TypeScript user service, its port, configuration and state remain the production system throughout the side-by-side exercise.

## Release decision

A separate coordinator authorization is required before any production deployment, route change, service change or production-state migration. The decision must identify the exact Go commit and binary SHA-256, the TypeScript fallback release, the accepted `RESULTS.md`, and the person responsible for rollback. A clean build is not sufficient: unresolved retained-contract differences, false workspace orphans, unowned descendants, lost audit receipts or failed durability checks block cutover. Performance targets stay at the values approved in `DESIGN.md`; failures cannot be converted into passes by changing the target after measurement.

Review the final evidence for:

1. The frozen retained MCP inventories and HTTP contracts, including full-permission managed workspaces and conditional tool registration.
2. The same private TypeScript and Go contract cases, with exact failures and explicit approved retirement/behavioral differences separated from passes.
3. Ordinary and concurrent-build latency, CPU, resident memory and audit write bytes under the matched workload and saturated audit condition.
4. Child-process ownership, cancellation, restart recovery, terminal output retention, lease health under transient Git failures, and audit replay after interrupted writes.
5. Complete historical-work archive and export counts, hashes and ownership checks. No archived run may become executable.
6. A final census of current sessions, active jobs, managed workspaces, leases, pending batch results and uncollected completion receipts.
7. Listener-first startup and independent admission readiness: authenticated health and ordinary reads remain available during bounded recovery, while process admission and configured history become ready before production traffic is switched.

The production tunnel/launcher is outside the stage-4 exercise. The Go service persists its compatible profile and connection metadata but does not install or launch Cloudflare, ngrok or Tailscale. The coordinator must choose and separately verify the production launch and routing arrangement before approving cutover. Do not infer a working production route from a successful `/admin/profile` save.

## Known identities and separation

| Item | Production TypeScript | Stage-4 Go candidate |
| --- | --- | --- |
| Base release | `0.31.0`, commit `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a` | Commit and binary hash recorded in the results manifest |
| Checkout | `/home/spite/Projects/codexpro` | `/home/spite/Projects/codexpro-go`, branch `feat/go-rewrite` |
| Running release | `/home/spite/apps/codexpro/current` | Tested private binary `/home/spite/.codexpro-go/bin/codexpro-go`; exact hash and launch manifest in `RESULTS.md` |
| Listener | Port `7200` | `127.0.0.1:7210` |
| Lifecycle | Existing `codexpro.service` | Explicit separate process, never the user service |
| State and credentials | Existing `~/.codexpro/` and `~/.config/codexpro/` | Independent owned-instance state and token under `~/.codexpro-go-proof/`; exact per-instance paths in the launch manifest |
| Test reference | Production is read-only to the suite | Private TypeScript reference on `7211`, under `~/.codexpro-go-proof/` |

The paths above describe the actual stage-4 proof arrangement. The repository checkout's `go/bin/codexpro-go` is not the attested running candidate; use the private binary named by its launch manifest. A future production-designated state or immutable deployment directory is a separate release choice and has not been deployed. Locate each proof instance by its verified manifest rather than assuming an earlier fixture is still running.

Never repoint either side at the other side's writable stores. Resolve symlinks and inspect inode identity before accepting a path. Token contents must not appear in command arguments, command output, reports, Git, URLs or screenshots. Readiness checks use the bearer header and the private token file.

## Prepare an immutable deployment bundle

After the separate release approval, create an immutable Go release directory containing the binary, source revision, `go.mod`, `go.sum`, frozen descriptor assets, the tested configuration manifest and the contract/performance evidence. Build with the pinned Go toolchain and bounded parallelism under `nice -n 10`. Record the compiler version, build flags, binary SHA-256, native AST helper version/hash, and host-resource helper revision/hash.

The current stage-4 target is Go `1.27.3`; `go.mod` pins the `go1.27.3` toolchain. AST operations use a native `ast-grep` executable. Do not make the future Go service depend on a mutable `node_modules` path: copy the verified executable into the immutable deployment bundle, retain its required notices, and verify that the copied bytes match the tested hash. The host-resource helper is an explicit operational dependency for scoped background commands. Verify its availability and ownership behavior without modifying the live TypeScript unit.

Create a new production-designated Go state directory and a separate private token file with mode `0600`. The coordinator decides whether the client-facing bearer credential is retained or rotated; neither choice is implicit. If rotated, update the connector configuration only during the authorized switch and keep a tested rollback credential path. The stage-4 token is not a production deployment credential.

Use the tested startup options to select the listener, state directory, token file, project catalog, operational limits file, native AST helper and host-resource helper explicitly. Full Bash and workspace writes remain enabled. Approved retired orchestrator, handoff and embedded-session controls remain inert. Inventory tier is independent of permission. Do not import a saved profile that silently changes the approved listener or overrides the intended startup configuration.

## Startup and admission readiness

The coordinator's startup amendment deliberately exposes authenticated health before the full MCP dispatcher and background recovery are ready. A successful `/healthz` response proves the listener, authentication and reported configuration; it is not a process-admission or archive-readiness receipt. Preserve the existing health response shape. See [STARTUP-AMENDMENT.md](STARTUP-AMENDMENT.md) and the exact runtime evidence in [RESULTS.md](RESULTS.md).

Before a later authorized route switch, verify each applicable surface independently:

| Surface | Temporary response and required release check |
| --- | --- |
| Full dispatcher | Before publication, authenticated non-health requests return HTTP 503 with `Retry-After: 1`. MCP returns JSON-RPC code `-32000` and `Server is starting; retry shortly`; other JSON routes use `server_starting`. Retry initialization or read-only discovery within a bounded readiness budget, then require successful MCP initialization and the exact retained tool inventory. |
| New process admission | While background job resources are unavailable, new admission returns `server_restarting`, `Job resources are not ready; retry after background startup completes.`, and `retry_unchanged:true`; no child has been admitted. After capability becomes ready, require one owned disposable job/stop/output/quiescence proof. Existing job reads and stop requests remain available during capability checks. Do not retry an accepted or ambiguous mutation on the assumption that it was rejected. |
| Pending recovery | Incomplete ownership settlement remains non-quiescent and consumes its capacity. Require exact owner/grant, process-start and scope witnesses before declaring recovered work settled. Never clear a pending record or signal an arbitrary PID merely to make the readiness check pass. |
| Configured historical archive | Pending or failed initialization returns `work_history_unavailable` promptly. Verify the approved private archive identity and an authorized read-only `work_status` result against the import manifest before declaring history ready. An intentionally absent archive is an explicit configuration choice; an unavailable configured archive must not be silently replaced with an empty history. |

Keep the production route unchanged while a required readiness check is unresolved. The scope capability probe has a five-second attempt budget and five-second retry interval. Ownership-settlement attempts have a twelve-second total budget and the existing ten-second per-helper-operation cap. Archive initialization has a two-second context budget. These bounds govern the implemented work; they do not claim that arbitrary OS filesystem calls can always be forcibly interrupted. Recovery runs in the background and must not hold up an ordinary file read.

The controlled stage-4 startup receipt used the verified 257,908,736-byte private archive, a prepared but ungranted recovery record, a stand-in helper and two bounded CPU workers. Health and the first file read completed before the helper entered; three subsequent reads completed while it was blocked, then exact owned recovery settled after release. This establishes that exercised ordering and responsiveness, not a reproduction of the reported production incident: the helper blocked for about 0.520 seconds, the host load was about 1.33, the workers/helper had nice 19 while the server had nice 10 in the same cgroup, and the driver had hashed the archive before launch. The separate real-helper lifecycle receipt covers its named process scenarios. Preserve those distinctions when reviewing the release evidence.

## State preparation and migration

All preparation works from verified snapshots into the new Go state directory. Existing TypeScript state remains intact for rollback. Produce a manifest with source identity, source and destination hashes, row/object counts, capture times, schema versions, ownership mapping, and every transformation. Refuse unsupported schemas and partial snapshots instead of repairing the source in place.

| State | Required treatment |
| --- | --- |
| Projects and creation roots | Preserve IDs, labels, canonical roots, ordering, default project, base refs and worktree limits. Validate every root before importing the catalog. |
| Managed workspaces and leases | Preserve existing lease documents and connector identity in a separate read-only archive without creating runtime ownership. If the managed source root is absent, verify absence around the catalog copy and report an explicit zero-lease receipt. Before any later writable adoption, separately verify Git/worktree identity and owner mapping through a tested, approved transfer procedure. A failed or timed-out Git probe is not lost ownership. |
| Active Bash/background jobs | Drain or deliberately retain them under their original owner. Never adopt arbitrary PIDs or live TypeScript child processes into Go. PID and process-start identity alone do not confer ownership; Go's scoped-runner grant must be present. |
| Finished jobs and retained output | Preserve metadata, terminal outcome, stdout/stderr bytes, cursor semantics and retention eligibility. Validate hashes and per-stream lengths before declaring an imported record complete. |
| Batches | Preserve the version-1 stored definitions, operation IDs/order/arguments, and their Git-local exclusion and retention behavior. Verify explicit resume by operation ID and index using private copies. Preserve available execution receipts in the original audit/work archives; the definition file itself is not an execution ledger. Do not reconstruct missing child results or automatically rerun pending TypeScript work. |
| Audit | Preserve the original immutable journal, import into private segmented storage, verify action/flight identity and counts, and rebuild the derived index from the journal. Never discard an incomplete record as a successful action. |
| Usage and inbox | Copy and validate the exact SQLite schema/data, including revisions, reset windows, event ordering and ownership. Open only private copies during migration verification. |
| Historical orchestrator work | Keep the full immutable SQLite archive, including every run, iteration, operation, event, session, request receipt and document revision, plus the verified public JSONL export. Public JSONL deliberately omits session/request/metadata tables and designated credentials; it does not replace the complete private archive. It still contains private project/document/history content and must remain private; the word public describes its API projection, not publication permission. Serve `work_status` as read-only history. Do not instantiate a coordinator or execute ready/blocked runs. |
| Profiles and runtime metadata | Preserve supported settings as next-start configuration. Treat old runtime records as historical metadata, not as authority to attach to an old process. |

`archive_work.py` deliberately opens the original work DB/WAL only as read-only files. It verifies a stable capture window, opens SQLite only on a private copy, and writes an immutable normalized archive plus its manifest. If the source changes during capture, retry within its bounded budget or defer capture; never pause, checkpoint, lock or repair production as a convenience. An explicit principal map is required when access identities change. Preserve the original identities in the archive manifest.

For stores without a demonstrated converter in `RESULTS.md`, migration is a release blocker until a converter and differential verification exist. A documented format or successful fresh-state test is not proof that old state was migrated.

Keep the evidence scopes separate: the actual catalog capture contained 21 projects in one 3,782-byte file and no managed lease documents. It preserves that catalog and proves source-root absence, not historical lease migration. The 14 finished-job records used for the executed migration proof came from the stopped private TypeScript `contract-ts-3/state/jobs` fixture, not production history; they demonstrate the exercised converter/output projection only. A final production snapshot still needs the separately authorized capture and validation above. More generally, a catalog-only import with verified absent managed state preserves the actual catalog but proves no historical lease migration; lease-schema/archive fixtures preserve the records they contain but grant no runtime ownership; fresh managed-workspace and restart probes establish new-instance lifecycle behavior. Stored-batch resume proves the selected definition suffix can be explicitly executed, not that historical child results or pending execution state were recovered. Record the actual source counts and evidence files for each applicable gate. A source object that does not exist must be reported as absent, not replaced with synthetic history.

## Authorized switch sequence

This sequence is for the later, separately authorized stage 5. It is intentionally not a shell script and must not be run by SP-6 during stages 1–4.

1. Announce the maintenance window and freeze new write/job submissions at the agreed front door. Keep read access available if the routing arrangement supports it. Capture a final active-operation census and the exact TypeScript deployment identity.
2. Resolve every active job and batch: let it finish, explicitly cancel it through its current owner, or postpone the switch. Confirm scope quiescence, collected completion receipts and retained output before declaring a workspace idle. Do not infer quiescence from an exited shell alone.
3. Capture the final state snapshots, run the approved private import procedures, and compare their manifests to the preflight inventory. Re-run integrity, ownership and completeness checks. Preserve the original stores and immutable snapshots.
4. Start the approved Go release on a separate internal port with its production-designated private state. Complete the separate listener, full-dispatcher, job-admission, recovery and configured-archive checks above; health alone is insufficient. Verify retained tool inventory, an ordinary read-only project probe, activity/usage/audit reads, and a disposable write/job/cancel test in a dedicated fixture project. Check the actual process UID, nice level, scope and dependencies. Retain the launch identity, readiness times, exact completion receipts and scope-quiescence evidence before switching a route.
5. Switch only the agreed public route or connector destination. Require clients to establish new MCP sessions; existing TypeScript session IDs are not transferable. Observe authentication and initialization failures explicitly.
6. Run the agreed smoke suite through the new public route. Record the route revision and time. Confirm no unexpected requests continue to mutate the old service. Keep the old deployment and original state available for rollback according to the agreed maintenance arrangement.
7. Observe the acceptance window defined by the coordinator. Monitor latency, errors, CPU/RSS, audit lag/write volume, retained-job bytes, lease health and owned process scopes. Any rollback trigger below stops further rollout.

Do not run two writable servers over the same state or allow concurrent independent owners to mutate the same managed workspace. Shared project roots in the stage-4 experiment are used only for recorded read-only traffic; that does not authorize dual production writers.

## Rollback triggers and procedure

Immediate rollback triggers are a retained-contract failure that blocks clients, authentication/session failure at the public route, data loss or failed integrity verification, wrong workspace ownership, a false orphan/release, an unowned surviving descendant, missing durable completion/audit receipts, or sustained performance outside the approved gate. Record the exact trigger and evidence before changing a route when doing so does not delay recovery.

1. Freeze new Go mutations and jobs. Keep the Go process and its private state available for evidence unless that itself prevents recovery. Drain or cancel only Go-owned scopes through the Go owner; never sweep unrelated host processes by name.
2. Capture Go's post-switch catalog, leases, jobs/output, batches, audit, usage and inbox deltas into a separate rollback-evidence directory. Preserve their hashes and times. Do not overwrite the original TypeScript stores with Go databases or an untested reverse conversion.
3. Restore the previous public route/connector destination and its known credential arrangement to the recorded TypeScript release. Establish fresh MCP sessions and run the old-service read-only smoke checks.
4. Reconcile writes made during the Go window before resuming writes where state has diverged. Repository file and Git changes may already exist in shared roots; inspect them explicitly. Import usage/inbox/audit/job/batch deltas only through a separately tested reverse procedure, or retain them as read-only evidence and obtain a coordinator ruling. Never silently discard acknowledged writes.
5. Confirm that TypeScript owns all resumed workspaces and that no Go-owned command can still mutate them. Keep both state histories immutable for investigation. Record the rollback revision, route, time, smoke results and outstanding reconciliation items.

Rollback means restoring service availability and accounting for all acknowledged work. Merely pointing the route back is insufficient when writes occurred after the switch. The safest rollback window starts with a short read-only public acceptance phase; the coordinator must decide when to enable production mutations and accept the corresponding reconciliation obligation.

## Stage-4 handback checklist

The final handback identifies the pushed branch/commit, runnable private binary and process status, evidence files, achieved and missed gates, accepted behavioral differences, complete archive/export location and hashes, unresolved migration/launcher limitations, and the exact next approval needed. It explicitly states that SP-6 did not reconfigure, restart, redeploy or adopt any production service, route, token or state and that cutover has not occurred. Attribute the coordinator’s independently reported live restart separately; do not claim that the production process identity remained unchanged throughout the exercise.
