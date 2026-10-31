# Private admin and catalog differential lane

`admin_catalog_contract_suite.py` compares the exact deployed TypeScript release
`44bb76bfa671e4aad540a8976f0fa6e6d5c2507a` on loopback port 7211 with the private Go
candidate on loopback port 7210. It imports the existing `contract_suite.Client`
and the private-path/evidence helpers in `paired_contract_suite.py`. It does not
modify those shared runners.

This is a default **direct workspace mode**, full Bash/workspace-write permission,
audit-metadata, standard-or-full inventory lane. It never starts, stops, signals,
reconfigures, or redeploys either running process. It never accepts port 7200 or a
live CodexPro token/state directory. The runner itself must have niceness 10 or
greater; Git observations have their own ten-second bound and use `nice -n 10`.

## Preconditions and launch manifest

Run sequentially with all other mutation lanes. Each side requires a fresh launch
manifest containing the general workflow runner's verified fields plus four
explicit admin bindings:

| Field | Meaning |
| --- | --- |
| `port` | Exactly 7211 for TypeScript or 7210 for Go |
| `pid` | The current user-owned process that actually owns that listening socket |
| `process_start_ticks` | Linux `/proc/PID/stat` start ticks, preventing stale PID reuse |
| `state_root` | Private instance namespace, outside live CodexPro state and source repositories |
| `token_file` | Private token inside that namespace, outside the repository fixture |
| `fixture_root` | Existing private fixture, on the same filesystem as private state |
| `project_id` | Fixture project ID in that side's actual catalog |
| `source_commit` | Required on the TypeScript manifest; exact release hash above |
| `config_home` | Actual configuration home: TypeScript `CODEXPRO_HOME` or Go `--state-dir`/`CODEXPRO_GO_HOME` |
| `projects_file` | Actual private catalog path, explicitly passed in CLI/environment |
| `creation_root` | Existing private directory configured as the selected named creation root |
| `creation_root_id` | Its exact ID in `creationRoots`, confirmed through `list_projects` |

The two sides must have distinct fixture, state, configuration-home, catalog, and
creation-root paths. The configured projects and creation-root IDs/labels should
match across the pair. Read-only source-repository projects may also appear in
the catalog; only the bound private fixture and new direct children of the
selected private creation root are modified.

The runner reads `/proc/PID/cmdline` and `/proc/PID/environ` into memory and extracts
only the explicit home/catalog settings. It never logs argv, environment, tokens,
or MCP session IDs. Config-home and catalog settings inherited solely through an
EnvironmentFile or a saved profile are deliberately insufficient for this proof:
provide explicit CLI/environment launch bindings.

Before any project/profile mutation it verifies:

1. Listening PID, user ownership, start time, endpoint, and distinct private paths.
2. A regular singly linked private catalog and its explicit default/creation IDs.
3. Direct workspace IDs derived from canonical catalog roots.
4. A side-specific nonce containing the side label and listening PID, read through
   MCP with `end_line=1`, plus the SHA-256 of the complete newline-terminated file.
5. No active fixture jobs and the required retained tools/configuration.

The catalog, profile, and creation-root guards are checked again as the lane
progresses. An unexpected catalog edit, reused PID, replaced root, or unsettled
request blocks subsequent mutations.

## Run

From the rewrite repository, with the four Python modules beside one another:

    nice -n 10 timeout --kill-after=15s 420s \
      python docs/go-rewrite/admin_catalog_contract_suite.py \
      --reference http://127.0.0.1:7211/mcp \
      --reference-token /PRIVATE/ts/http-token \
      --reference-manifest /PRIVATE/ts/admin-launch.json \
      --candidate http://127.0.0.1:7210/mcp \
      --candidate-token /PRIVATE/go/http-token \
      --candidate-manifest /PRIVATE/go/admin-launch.json \
      --output docs/go-rewrite/evidence/admin-catalog-direct.json

Paths above are placeholders. Both output paths must be new. The default raw
evidence file is the output path plus `.jsonl.gz`; `--raw-output` can select another
new path. Evidence and the report use mode 0600. The compressed raw transcript
retains requests, decoded responses, actual HTTP text, request windows, and all
normalization proofs, with the same 64 MiB uncompressed evidence cap as the shared
workflow lane. HTTP requests explicitly disable proxies and automatic redirects.

## Covered contracts

| Area | Cases and assertions |
| --- | --- |
| Inventory/config | Actual `tools/list`, `server_config` structured fields and exact ordered pretty-JSON text; count consistency; retained `work_status`; explicitly approved removals |
| HTTP authentication | Missing/wrong credentials, bearer success, case-insensitive bearer syntax, `codexpro_token` query compatibility, legacy `token` query compatibility |
| Health | Authenticated response body and semantic HTTP headers, no token/session disclosure |
| Profile reads | Missing profile versus valid `{}`, object/null distinctions, derived private path, actual runtime configuration |
| Profile errors | Wrong method, mismatched/malformed Origin, invalid JSON, unknown keys, bounds, incorrect types/enums, missing stable-tunnel hostname, oversized request |
| Profile saves | Same-host HTTPS Origin, numeric coercion, minimal/standard/full saved tiers, public secret placeholders, private mode 0600, root/version/clock binding, persisted scalar settings |
| Runtime isolation | A profile save changes persisted/effective settings while `server_config` and every current runtime field remain unchanged in another MCP session |
| Catalog errors | Invalid project ID, unknown parent, path traversal, duplicate project; no unexpected catalog or creation-root change |
| Catalog creation | Empty directory and Git initialization; append-only catalog semantics; registration visible immediately and across sessions; exact output DTO/text |
| Git initialization | One empty root commit, actual SHA-1 object verification, bounded creation timestamps, full identity/message/tree comparison with only proven timestamps normalized |
| Open workflow | First/repeated singular opens, independent sessions, multi-open deduplication/order, shared tree budget, conflicting targets, unknown ID, open after a recorded write |
| Self test | Default read-only probe; optional write/edit, Bash and selected-context probes; exact counts/checks; only the documented probe file, no published context bundle |
| Recent activity | Exact source latest-change/tail-ten projection, failed-change eligibility, own actor/request/transport fingerprints, chronology, receipt-bound IDs/times and audit cursor windows |
| Conditional direct tools | Session-scoped `list_workspaces` when present; `open_current_workspace` when present; descriptor mismatch remains an inventory failure |

`recentActivity` is checked against `src/work/activity.ts`, not reconstructed from
the latest Git status. The last changed action is the final retained action among
at most 500 project mutations with changed paths or differing nonempty before/
after Git heads. There is no successful-status filter. The briefing uses its
`finished_at`, optional workspace ID, and first ten changed paths. The ten most
recent mutations independently supply the action briefing.

The source excludes the four activity tools from its own audit journal. Therefore
an immediately preceding `activity_status` snapshot has the same latest sequence
as the in-handler briefing; the immediately following snapshot is exactly one
greater after the open call completes. This is verified before cursor values are
normalized. Unowned concurrent calls or retention movement outside that window
produce a failed proof rather than being silently ignored.

The direct connector principal is independently derived from its configured
default root: `connectorID = SHA256(defaultRoot)[:48]`,
`principal = "connector_" + SHA256(connectorID)[:32]`, and
`actor_ref = "actor_" + SHA256(principal)[:32]`. Session/request fingerprints bind
owned audit records to their actual MCP calls. Historical actors other than that
derived principal remain unchanged.

## Comparison rules and approved differences

The runner compares complete decoded MCP envelopes and text. It preserves omitted,
null, empty-string, empty-list, and empty-object distinctions, array order, unknown
fields, error shapes, and human-readable formatting. `server_config` text must be
the source's exact two-space JSON formatting before normalization; changing key
order in that text still fails.

Only proven dynamic values receive substitutions: private roots and corresponding
workspace identities, the two required listener ports with their original types
validated, echoed JSON-RPC request IDs, finite client timing, self-test timing,
saved-profile clocks, initial Git commit identities after object verification, and
recent-activity values tied to actual retained receipts/cursor windows. A shared
timestamp on two distinct actions does not collapse their causal identities.

The semantic HTTP header list is explicit in the script. HTTP `Date` and framing
length are validated against the observed request/body; transport-specific server,
connection, and ETag decoration are outside this application comparison. No
arbitrary JSON/text fields are dropped to compensate for mismatches.

The eleven descriptor retirements approved at `acf6b9b` are separately recorded.
The source's active autonomous-work `enabled`/`management` flags and profile
`effective.work` default may map to the approved inert Go fields; every such
mapping is recorded as an approved difference. Corresponding self-test inventory
counts and diagnostic strings are first verified against actual tools/list, then
adjusted only for those named removals. Cases containing approved differences are
reported as `approved_delta`, never as unchanged passes.

## State lifecycle and unfinished proof

Existing private profile bytes and their mode stay in memory while the lane seeds
missing/empty profile fixtures and performs HTTP saves. The original file is
restored after settled calls only if the current file still matches the last
confirmed lane write. An unsettled request or concurrent edit preserves the state
for inspection and reports cleanup as blocked. Saved tokens are checked for
equality in memory and never appear in reports or transcripts.

New projects remain registered in the private running processes and catalogs, with
their paths and Git objects listed in the report. The lane does not remove catalog
entries behind an active manager. Use a fresh run ID for another execution.

This lane proves **save persistence and no current-runtime reconfiguration**.
Actual startup application of the saved settings requires the separate
parent-owned private restart proof; the runner marks that case pending. Managed
workspace business calls likewise require a separate private TypeScript
`mcp/safe` reference pair because that release rejects full Bash plus MCP mode;
the Go side continues with full permission. Neither is counted as completed here.

No tunnel process installation/launch, Cloudflare Access authentication, setup
page visual equivalence, catalog-restart recovery, local-repository clone,
or profile rate-limit saturation is claimed by this first lane. Existing Go unit
tests and other lanes are separate evidence. The report always states
`stage4_complete: false`; the coordinator judges the combined stage-4 evidence.

## Offline verification

    python -m unittest test_admin_catalog_contract_suite -v

The focused tests cover source projections, omitted/null distinctions, precise
retirement filtering, ordered config text, private file aliases/hardlinks, profile
restore/CAS behavior, unsettled requests, port typing, runtime invariance, query
credential non-disclosure, bounded HTTP framing, and activity-exempt cursor
arithmetic. Passing these tests validates the harness; it is not a substitute for
running the actual TypeScript/Go pair.
