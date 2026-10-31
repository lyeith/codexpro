# Retained contract coverage

This map connects every retained MCP tool to executed compatibility cases. [RESULTS.md](RESULTS.md) is the authoritative ledger for report locations, tested commits and binary identities, native gates, startup, performance and final handback status. The map records exercised contracts; it does not claim exhaustive equivalence for every branch or authorize a production cutover.

## Inventory and result accounting

The standard multi-project inventory contains **28 tools**. Five conditional workspace tools bring the retained union to **33**: `create_workspace`, `release_workspace` and `remove_workspace` in managed mode; `list_workspaces` in direct/full mode; and `open_current_workspace` in direct/single-project mode. Their presence is configuration-dependent. The [configured registry oracle](CONFIGURED-REGISTRY.md) checks the capability/configuration matrix independently of the business-call suites.

The [paired runner](paired_contract_suite.py) compares the complete MCP envelope and structured result, preserving errors, omitted/null distinctions, arrays, truncation fields and text. Descriptor array order is preserved. Only validated dynamic values and explicitly approved retirement differences are normalized. Expected errors count as passing contracts when both implementations return the same result.

The eleven approved retired descriptor names are `work_manage`, `work_update`, `codexpro`, `codexpro_inventory`, `read_handoff`, `wait_for_handoff`, `codex_context`, `export_pro_context`, `handoff_to_agent`, `codex_sessions` and `read_codex_session`. `work_status` remains read-only history. Conditional absence is not retirement. The design ruling is recorded in [APPROVAL.md](APPROVAL.md).

## Executed lanes

Counts use each lane's own unit and must not be summed into a single pass total.

| Key used below | Runner and cases | Completed outcome |
| --- | --- | --- |
| General | [paired_contract_suite.py](paired_contract_suite.py), [contract_cases.py](contract_cases.py), [PAIRED-WORKFLOWS.md](PAIRED-WORKFLOWS.md) | 20 workflows / 94 steps passed. This corpus has no `batch` steps. The initial 82/94 attempt remains preserved. |
| Admin | [admin_catalog_contract_suite.py](admin_catalog_contract_suite.py), [admin-catalog-direct-3.json](evidence/admin-catalog-direct-3.json) | 41 unchanged, eight approved retirement instances, zero failures/blocked. Its two originally pending labels have separate managed/profile receipts; the original report counts are unchanged. |
| Telemetry | [telemetry_contract_suite.py](telemetry_contract_suite.py), [telemetry_contract_cases.py](telemetry_contract_cases.py), [TELEMETRY-CONTRACTS.md](TELEMETRY-CONTRACTS.md) | 102 unchanged, two specifically approved HTTP-413 differences, zero failures. |
| Process | [process_contract_suite.py](process_contract_suite.py), [PROCESS-PARITY-FIXES.md](PROCESS-PARITY-FIXES.md) | Third replay: all ten scenarios passed in 87.534 seconds; earlier failed attempts remain preserved. |
| Managed | [managed_contract_suite.py](managed_contract_suite.py), [MANAGED-CONTRACTS.md](MANAGED-CONTRACTS.md) | Eight unchanged plus four matched with the declared TypeScript-safe/Go-full permission fixture, zero failures. Those four are not unchanged full-permission comparisons. |
| Gaps | [retained_gaps_contract_suite.py](retained_gaps_contract_suite.py), [retained-gaps-2.json](evidence/retained-gaps-2.json) | Eight direct-workspace and 16 import cases unchanged, zero failures. The first run's three direct-summary/path-role failures remain preserved. |
| Activity supplement | [activity_routes_contract_suite.py](activity_routes_contract_suite.py), embedded report in [retained-gaps-2.json](evidence/retained-gaps-2.json) | 24 unchanged, zero failures, within the HTML projection scope described below. |
| History | [historical_work_contracts.py](historical_work_contracts.py), [HISTORICAL-CONTRACTS.md](HISTORICAL-CONTRACTS.md), [HISTORICAL-EXPORT-PROOF.md](HISTORICAL-EXPORT-PROOF.md) | 405 matched historical `work_status` cases, including 31 expected errors; both archive identities unchanged. Separate full Export verification covers all 50,802 projected records and all 1,516 document revisions. |

The eight admin retirement instances are `ADMIN-CONFIG`, `PROFILE-MISSING`, `PROFILE-EMPTY-OBJECT`, `PROFILE-RUNTIME-UNCHANGED` in three inventory tiers, and two self-tests. They reuse the existing work-orchestrator retirement ruling. Duplicate adjustment entries reflect both text and structured configuration and initial/current values; they are not additional approvals. These instances remain separate from Telemetry's two [approved HTTP-413 differences](evidence/http413-approval.json). The four managed permission-qualified cases retain their explicit fixture classification. Batch behavior uses only the specific ruling and scope recorded in [RESULTS.md](RESULTS.md).

## Retained MCP tool map

| Retained tool | Executed suite and representative cases | Claim supported by those cases |
| --- | --- | --- |
| `list_projects` | Admin: `CATALOG-LIST-INITIAL`, `CATALOG-LIST-AFTER-CREATE`, `CATALOG-LIST-NEW-SESSION`, persisted-catalog checks | Catalog DTO/text, creation visibility and cross-session reads |
| `create_project` | Admin: empty, Git and object-form Git creation; invalid ID, unknown parent, traversal, duplicate | Successful private creation and exercised validation/failure paths |
| `server_config` | Admin: `ADMIN-CONFIG`, three `PROFILE-RUNTIME-UNCHANGED` tiers | Complete text/structured settings with only approved retired-work normalization |
| `activity_list` | Telemetry: `three-durable-action-contracts` | Ordered controlled actions, cursor/sequence scope, actor/session/request metadata and exact DTO/text |
| `activity_get` | Telemetry: `get-read-0`, `get-read-1`, `get-read-2` | Complete retained action and text projections; audit inspection remains non-appending |
| `activity_status` | Telemetry: private-store, after-inspection and non-appending checks | Logical status/retention and validated physical-size accounting |
| `activity_export` | Telemetry: `audit-export-json`, `audit-export-jsonl`; separate 2,783-record native import proof | Exact controlled export bytes/hash, newline/cursor bounds; historical public DTO continuity separately |
| `codexpro_self_test` | Admin: `SELF-TEST-DEFAULT`, `SELF-TEST-PROBES` | Exercised probes and exact result envelopes, with approved retired-descriptor counts |
| `load_skill` | General: `FILE-SKILLS-10` primary, exact-shadow, other, missing | Successful discovery/load and actual baseline shadow/error behavior; no invented absolute-path success |
| `open_workspace` | Admin: first/repeated/second-session, multi dedup/tree budget/conflict/unknown/after-write receipt; Managed 07/09 | Standard direct summary/session behavior and declared managed-fixture behavior |
| `inspect_workspace` | General: `ANALYSIS-SCOPE-15` scoped/cached | Scoped repository analysis and observable cache behavior |
| `tree` | General: `FILE-TREE-07`, `FILE-GUARDS-06` | Ordinary/hidden/bounded/file-root forms and guarded paths |
| `search` | General: `SEARCH-CURSOR-EDIT-12`, `SEARCH-REGEX-13`, `SEARCH-CONFIG-14`, grouped analysis | Cursor causality/edit provenance, regex success/error/hidden paths, JSON/YAML/TOML configuration queries and exercised grouped analysis |
| `ast_grep` | General: `AST-PATTERN-16`, `AST-KIND-17` | Pattern/kind queries, match-derived edit proof and selector/strictness/mode errors |
| `read` | General: `FILE-UTF8-CRLF-01`, `FILE-PAGINATION-02`, principal-tag/guard workflows | UTF-8/CRLF text, paging and bounds, empty/past-EOF, read proofs, guarded/error envelopes |
| `view_image` | General: `FILE-IMAGE-11`; Gaps `IMPORT-04` | PNG content and imported-image reuse plus unsupported-image error; not every image format |
| `write` | General: `FILE-WRITE-CAS-05`, `FILE-GUARDS-06` | SHA CAS, exclusive creation, nested paths, stale write and traversal behavior |
| `edit` | General: principal-tag, coordinate, search and AST workflows | Principal-shared/cross-session proofs, stale/unseen tags, multi-hunk/no-op/overlap and match provenance |
| `apply_patch` | General: native/unified workflows 08/09 | Native and unified patches, add/move, stale preflight, guarded paths and verified file outcomes |
| `import_file` | General: five negative cases; Gaps `IMPORT-01`–`IMPORT-16` | Text/binary/PNG success; hash/MIME checks; collision/overwrite; redirect; exact/advertised/streamed byte bounds; HTTP404/no-Location/redirect limit; native invalid-URL `TypeError` |
| `bash` | Process: outcomes, incremental output, timeout, output pinning | Exit/signal/null outcomes, stdout/stderr, foreground/background deadline behavior and retained-input integration |
| `start_jobs` | Process: dedupe/stop and digest acknowledgement | Parallel launch result contracts, running dedupe and controlled terminal output/ack state |
| `jobs` | Process: outcomes, incremental output, dedupe/stop, digest and timeout | Metadata/head/tail/legacy-head, UTF-8 cursor pages, stale/wrong cursor failures, terminal outcomes and acknowledgement behavior |
| `stop_jobs` | Process: `dedupe_and_stop`; separate candidate lifecycle proofs | Explicit job selection, duplicate/repeated stop behavior; owned-descendant quiescence is separately witnessed by lifecycle tests |
| `show_changes` | General: `GIT-REVIEW-SESSION-18`, commit review/clean verification; activity supplement same-session witness | Session-scoped review state, full/omitted diffs and complete same-session nonmutation after dashboard GETs |
| `commit_changes` | General: `GIT-COMMIT-EXPLICIT-19` | Explicit-path commit and nothing-to-commit behavior; not every merge/rename/default-staging branch |
| `batch` | Process scenarios 7–10: preflight, parallel/skip, resume, bounded verification; persisted-v1 migration | Schema/preflight before effects, ordering/failure policy, stored recipe/resume-by-id/index and bounded verification; preserve the exact recorded batch ruling |
| `work_status` | History 405-case actual differential plus complete Export proof | Read-only list/get/summary/packet/section/operation/history/document/search-memory/error/principal behavior over preserved history; no retired execution or ownership adoption |
| `create_workspace` — MCP | Managed 03 create, 04 idempotency/cross-session, 05 conflicting base, 06 quota | Managed creation/idempotency/limits for the explicit safe-reference/full-candidate fixture |
| `release_workspace` — MCP | Managed 09 release/reopen | Release lifecycle and subsequent access in the declared fixture |
| `remove_workspace` — MCP | Managed 10 dirty refusal and 12 clean remove/branch preservation | Dirty protection and clean removal outcomes in the declared fixture |
| `list_workspaces` — direct/full | Gaps `DIRECT-01`, 03, 05, 06, 08 | Initial/after-open session lists, stable open time and independent session state |
| `open_current_workspace` — direct/single | Gaps `DIRECT-02`, 04, 07 | Current-workspace open, tree/repeat and second-session behavior; corrected configured tool-mode summary remains compared, not normalized |

## HTTP, pinning and state continuity

Bearer authentication, negative authentication cases, health, profile GET/POST and same-origin handling have paired admin/telemetry cases. Native transport tests separately cover MCP validation/session behavior and security headers; native admin tests cover setup/root/assets and reauthentication rendering. These are different evidence types. The actual four-case profile proof spans three owned process incarnations and establishes next-start consumption separately from same-process profile writes; see [PROFILE-RESTART-PROOF.md](PROFILE-RESTART-PROOF.md).

Usage and inbox are HTTP/store features, not missing MCP descriptors. Fresh telemetry exercises their revision, pagination, mutation, reset/answer receipt and error contracts. Historical readers additionally matched five usage snapshots, three inbox listings and all 20 stored answer receipts. No historical reset receipts existed, so reset continuity relies on the fresh telemetry cases rather than an invented historical replay. [LEGACY-TELEMETRY-MIGRATION.md](LEGACY-TELEMETRY-MIGRATION.md) describes the separate immutable-reader proof.

Pinning is the process contract's `input_job_ids` behavior and retained-output environment. Its paired pin case and native retention tests establish the branches they exercise; they do not prove every pressure, TTL and crash combination. Finished normal jobs, archived orchestrator jobs, stored batch definitions and live execution ownership are distinct state families. The 14-record finished-job migration and persisted version-one recipe resume proofs do not authorize adoption of an existing TypeScript process or execution; see [JOBS-BATCH-MIGRATION-GAPS.md](JOBS-BATCH-MIGRATION-GAPS.md).

### Activity rendering scope

The original activity-shell checks validate status, headers, functional markers and script/style presence. The executed 24-case supplement adds `/activity/batch` and `/activity/diff`: full and fragment pages; query-token authentication; direct-mode absent/supplied workspace labels; unknown-version display JSON; route validation errors; controlled Git diffs; and read-only state witnesses.

The supplement compares complete visible non-code text, headings and links; decoded JSON code values/types/fields; and exact non-JSON code text. JSON key order and indentation are presentation in this projection. Raw HTML is retained privately. There is no claim of full HTML/CSS/JavaScript byte identity, JavaScript execution, pixel equality or browser interaction.

Each side's complete same-session Git review response is compared before and after the GETs, after a cache warm call. Independent fixture cache digests remain recorded separately rather than masked or equated. Recipe contents and timestamps, journal records, Git file/index/excludes and the controlled job's acknowledgement state remain unchanged. Subsequent ordinary reads expose and acknowledge that completion exactly once.

## Limits of the evidence

All 33 retained names have exercised business calls in compatible inventories. The tests do not exhaust every image format, file encoding/hardlink combination, regex/AST language, Git edge case or concurrent failure interleaving. Native invariant, fault, race and lifecycle tests provide additional evidence and remain distinct from paired runtime equivalence. [RESULTS.md](RESULTS.md) records current source/binary alignment and the separately required startup and measured performance gates; a compiled or prepared case is not a runtime pass.

The actual catalog capture contains one 3,782-byte file and zero lease documents because the configured managed root was absent. This establishes catalog/absence preservation, not migration of real historical leases or operational ownership. No archive or compatible reader grants authority to resume an archived run, claim an old PID or adopt an active lease/job/batch. The later production transition remains outside this task and is described in [CUTOVER-RUNBOOK.md](CUTOVER-RUNBOOK.md).

Published evidence distinguishes synthetic private-fixture transcripts from actual retained user data. Fixture-only compressed transcripts may be published after decompressed content and credential review. Actual historical/catalog/usage/inbox/audit payloads, full Export streams and payload-bearing failed historical diffs remain private; reviewed aggregate receipts publish counts, hashes and outcomes. Credential filtering of a public DTO does not remove its private document content.
