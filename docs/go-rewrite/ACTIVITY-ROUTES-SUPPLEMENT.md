# Saved-batch and tracked-diff route supplement

This is a separate 24-case lane. It does not change the completed 104-case telemetry suite and does not start, stop, restart or adopt a server. The parent owned-pair driver launches direct/off, single-project, full-tier TypeScript and Go instances on 7211/7210, runs its own cases, then calls:

```python
run_existing_pair(reference_manifest_path, candidate_manifest_path, new_private_output_directory)
```

The standalone equivalent is:

```text
activity_routes_contract_suite.py --reference-manifest PATH --candidate-manifest PATH --out-dir NEW_PRIVATE_DIRECTORY
```

The caller must reserve the complete 150-second supplement allowance before invoking it. The enclosing owned-pair driver uses its own outer deadline and 90-second kill grace so failed-test cleanup can finish. This module never sends signals or lifecycle commands to servers. It closes only its own MCP clients and stops only the job IDs returned by its own sentinel admission. A successful stop envelope is insufficient: its returned IDs must exactly match those owned IDs and every record must be terminal. Session close is attempted even when stopping fails.

## Fixture binding and effects

Before mutations, the existing frozen telemetry guards verify loopback endpoint, port, current unprivileged UID, PID/start ticks, listener ownership, separate private state/root/token paths and pinned source commit. A nonce file is read through MCP and its full-file hash is checked. Manifests and binding hashes are included in the report. Live paths and port 7200 are refused.

Each side receives one unique fixture directory. Three read operations produce a real persisted version-one batch through the product. The TypeScript recipe's exact bytes replace only the candidate's independently created, nonce-bound automatic recipe and are also copied to a new custom recipe. This avoids inventing a stored format. The test creates one tracked fixture file with a deterministic private Git commit; the Git environment removes routing and injected-config variables, disables external hooks and signing, and uses bounded `nice -n 10` commands. No unrelated index content may already be staged.

The same MCP session establishes a review point, then observes a controlled tracked-file change. A `mark_reviewed:false` call first warms the changed inventory's analysis cache; the next complete response becomes the baseline. This preserves the cache hit field instead of misclassifying its normal first-miss/second-hit transition as a drawer side effect. One bounded `printf` job becomes a terminal, unacknowledged completion. Between this terminal observation and the digest assertions, the lane issues only dashboard HTTP GETs and reads bounded private journal files. It does not use an ordinary MCP audit call in that interval, because source `attachJobStatus` would itself acknowledge the completion. A durable completion receipt is witnessed before the journal baseline is taken.

## Cases and exact comparison scope

| Group | Count | Assertions |
| --- | ---: | --- |
| Owned completion precondition | 1 | Exact owned compact ID settles successfully with exit zero; metadata-only collection does not acknowledge it |
| Route errors | 10 | Exact status, selected HTTP headers and body for missing arguments, unknown projects, length bounds, missing file, traversal, invalid JSON, and missing/unknown diff project |
| Saved-batch success | 7 | Automatic full page/fragment/query-token access, custom full page, arbitrary version-two JSON displayed without execution, absent direct workspace label and supplied direct display label |
| Tracked diff | 2 | Full controlled diff content and escaped path/content, including repeated read |
| Read-only witnesses | 1 | Journal files/records, recipe bytes/timestamps, controlled file, Git index and local exclude file stay unchanged across the HTTP GET window |
| Completion acknowledgement | 2 | First subsequent ordinary read sees the exact terminal notification; the next ordinary read does not repeat it |
| Review checkpoint | 1 | The complete same-session `show_changes(since="last_shown", mark_reviewed=false)` response remains exactly equal before and after the drawer reads |

For success HTML, the comparator preserves all visible non-code text, headings and link targets. Every saved operation argument and the complete raw definition is parsed as JSON and compared with exact JSON types and all fields; JSON key order and indentation are not treated as behavior. Non-JSON code evidence retains exact text. Caller-controlled angle brackets, quotes and ampersands must remain text, with no injected element or event handler. Full original HTML is retained in credential-scanned private gzip evidence. The lane does not claim pixel equality or full HTML/CSS/script byte equality.

The two review-response hashes are retained separately. The complete before/after equality is strict within each session; the harness does not equate independently created analysis cache fingerprints across fixtures, nor does it remove analysis fields. Those keys depend on real file inventories and timestamps.

## Source basis and concrete repairs

The source HTTP routes are at `src/http.ts` 44bb76b. The exact renderer input source is `src/activityDashboard/page.ts`, SHA-256 `c934eec9f62408e32c3bc62b8a7030e2a8d08478153fc5065e19b55cde418ebe`. The batch loader is `src/batchStore.ts`, SHA-256 `2309ec58e4cd1888f549368a2d5dacec5b5dbedcf478bd71c367c4b79e7823b9`.

Source review found three gaps before this actual lane: the full Go batch page omitted source attribution/sections/current-definition copy; the main hook applied managed workspace resolution to direct-mode display labels; and the read-only saved-definition viewer incorrectly applied execution schema validation. Narrow source-backed fixes and native regressions are staged separately. Path/text/JSON guards and strict execution validation remain in place. No comparator exception excuses those gaps.

`comparison.json` reports `passed`, unchanged and failed case counts, cleanup errors, original source/binding identities and raw-evidence hash. Only a complete 24-case result with no failure, fatal error or cleanup error passes. Eighteen offline harness guards currently pass; native Go tests and actual paired execution are separate gates and were not run by writing this document.
