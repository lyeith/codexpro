# Paired files, search, analysis, and Git workflow lane

`paired_contract_suite.py` imports the existing `contract_suite.Client` and the unchanged twenty-workflow corpus in `contract_cases.py`. It executes the same 94 declared MCP steps against the private release reference on port 7211 and the Go candidate on port 7210. The base client and the process/telemetry lanes are unchanged.

The runner has eighteen guard tests plus one real, bounded Node inventory-oracle test. These test the harness. They do not establish server parity; a paired server report is required.

## Required private launches

Run sequentially with other mutation and performance lanes. Both servers must already have the same logical project registered against different explicitly private fixture roots. The fixture and state roots must be on the same filesystem so original fixture entries can be moved into a private backup without copying.

Each launch manifest needs:

```json
{
  "port": 7211,
  "pid": 12345,
  "process_start_ticks": 123456789,
  "state_root": "/home/spite/.codexpro-go-proof/ts",
  "token_file": "/home/spite/.codexpro-go-proof/ts/http-token",
  "fixture_root": "/home/spite/.codexpro-go-proof/ts/fixture",
  "project_id": "contract",
  "source_commit": "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"
}
```

The candidate uses port 7210 and its own PID, token, state, and fixture paths. Its `source_commit` may describe the rewrite's captured source; the reference must identify the exact release above. Extra manifest fields used by the telemetry runner are allowed.

The script proves that the declared unprivileged PID owns the listening socket, checks process start ticks against PID reuse, refuses live paths and port 7200, and resolves the supplied project through `list_projects`. A nonce containing the side label and listening PID is written under the explicit fixture root and read through MCP with `end_line: 1`; the full-file SHA is also checked. Active jobs block the lane.

## Invocation

Upload the runner, its tests, and this document beside `contract_suite.py` and `contract_cases.py`.

```bash
nice -n 10 timeout --kill-after=15s 420s \
  python3 docs/go-rewrite/paired_contract_suite.py \
  --reference http://127.0.0.1:7211/mcp \
  --reference-token /home/spite/.codexpro-go-proof/ts/http-token \
  --reference-fixture /home/spite/.codexpro-go-proof/ts/fixture \
  --reference-manifest /path/to/reference-launch.json \
  --candidate http://127.0.0.1:7210/mcp \
  --candidate-token /path/to/go/http-token \
  --candidate-fixture /path/to/go/fixture \
  --candidate-manifest /path/to/candidate-launch.json \
  --project contract \
  --output docs/go-rewrite/evidence/paired-workflows-UNIQUE.json
```

Use a new output basename for each run. Repeated `--only EXACT-WORKFLOW-ID` arguments select a bounded diagnostic subset after the first full report. Running without `nice -n 10` is refused. Fixture Git and Node helper processes are independently bounded and run at reduced priority.

The evidence consists of a JSON report and a mode-0600 gzip JSONL stream containing complete raw request/response envelopes, normalized comparisons, seeded/final filesystem states, and content hashes. Per-step unified diffs are bounded with an explicit truncation flag. Credentials and MCP session IDs are checked before evidence is written.

## Comparison rules

Object property ordering outside human-readable text is irrelevant; arrays, omitted fields, nulls, empty values, booleans, and text formatting remain distinct. The full MCP response wrapper is compared after proving exact request-ID echo. The client-observed wall duration is a harness measurement and is normalized separately from tool fields.

Only manifest-bound own roots and the workspace identity are substituted. Edit tags, file SHA-256 values, byte counts, line coordinates, output shapes, errors, content blocks, and image bytes remain exact.

Search and AST fingerprints are independently recomputed from release `searchOps.ts163–181` and `astGrepOps.ts370–388`. Continuation tokens must use the exact source base64url JSON serialization, matching query signature, checksum, and position. Search tokens must identify the last returned hit; AST offsets must advance by the number of returned matches. Later calls use each side's own captured token, preserving causality.

Declared analysis cache keys must contain the actual workspace ID, exact analysis-limit JSON, and the source SHA-256 of the current filesystem inventory. A small isolated Node helper uses the same `fs.statSync().mtimeMs` precision and `localeCompare` ordering as the source. Only after this validation is the identity/time-dependent cache key normalized to the ordered paths, byte sizes, and actual file content hashes. Cache-hit booleans and all surrounding analysis results remain exact.

Declared commit IDs must equal local HEAD and the SHA-1 of the complete Git commit object. Author/committer times must fall within the observed call window. The complete object is then compared with only those two timestamps substituted; tree, parents, author identity, offset, and message remain exact. Full, twelve-character, and seven-character identities in human text are tied to that same validated object.

Final fixture state is also compared: paths, file types, permissions, byte counts, content hashes, and symlink targets; Git HEAD semantics, tree, index, and porcelain status. Initial fixture timestamps and initial Git commits are deterministic. Post-mutation file timestamps are not overwritten by the harness.

The eleven approved retired descriptors are recognized explicitly. Only descriptors present in the reference and absent in the candidate are reported as approved deltas, associated with review commit `acf6b9b`; they are never counted as unchanged passes. Retained descriptors and all other `tools/list` envelope fields remain exact.

## Fixture restoration and interrupted calls

The corpus tests real project-root behavior, including skill discovery and Git review scope. Each original fixture entry is first moved into:

```text
<private-state>/paired-fixture-backups/<run-id>-<side>/originals/
```

`RESTORE.json` records the bound root identity and original names. A per-fixture advisory lock prevents concurrent instances of this runner. Every cleanup verifies the original root identity, and directory removal uses Python's descriptor-based symlink-safe implementation.

The two intentional parent-escape targets, `../outside` and `../escape.txt`, must initially be absent. The runner refuses pre-existing files, directories, or symlinks at those names. It reserves them with exclusive public sentinel files inside the shared private fixture/state namespace, verifies that every workflow leaves their bytes unchanged, and removes only those owned sentinels after calls have settled. The reference and candidate need distinct parent targets. A broken server guard therefore cannot overwrite an unrelated pre-existing sibling file.

Normal completion restores the original entries. An interrupted or timed-out tool call leaves its outcome unsettled; subsequent mutations and cleanup on that side are refused, and the original backup is preserved. The report identifies the recovery path. First prove the private process has finished the call or stop that private process, then clear only its bound disposable fixture and move the entries from `originals/` back. Never remove a backup until its original entries are present at their original paths.

This lane does not claim the additional contract coverage listed at the end of `contract_cases.py`: hardlinked file writes, malformed UTF-8 and surrogate boundaries, injected file races, successful import/redirect/MIME/hash cases, native-tool failure/cap cases, or the complete Git diff-target matrix. Those require their own paired records even where Go unit tests already exist.
