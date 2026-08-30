# Tagged Multi-Hunk Edit and Batch Operations

Status: implemented on 2026-08-30.

This document records the design behind CodexPro's four-hex-tagged `edit` tool and bounded `batch` tool. It also records Oh My Pi ideas that may be useful later; those review-only candidates are not implemented here.

## Research basis

The design was informed by Oh My Pi's hashline edit implementation at commit `51f03804476c3fd3c15748ae07e4849d1efc883b`:

- `docs/tools/edit.md`
- `packages/hashline/README.md`
- `packages/hashline/src/format.ts`
- `packages/hashline/src/snapshots.ts`
- `packages/hashline/src/prompt.md`

The useful invariants are:

1. A read establishes an immutable file snapshot.
2. One edit call may contain several non-adjacent hunks.
3. Every line reference in that call addresses the original snapshot, not the output of an earlier hunk.
4. The whole edit is parsed and preflighted before any write.
5. Overlapping or otherwise ambiguous hunks are rejected.
6. A stale snapshot must fail rather than silently applying to different content.
7. An edit may only target source lines the agent was actually shown.

Oh My Pi renders a short four-hex tag backed by retained full snapshots. The tag is a convenient handle, not a collision-resistant identity by itself. CodexPro uses the same important separation: the model sends four characters, while the connector validates against retained full file content.

## Four-hex edit tags

`read` returns a four-uppercase-hex `edit_tag`, for example `A1B2`. CodexPro derives it from the full file content, but never treats the 16-bit tag alone as proof that a file is unchanged.

Each MCP session owns an independent bounded snapshot store:

- maximum 256 file paths;
- maximum four retained versions per path;
- maximum 64 MiB of retained full text;
- canonical real paths, so a file and an allowed symlink alias share identity;
- exact full-text comparison when resolving a tag;
- displayed-line provenance attached to each exact snapshot.

Consequences:

- If a newly read file body collides with an older retained body on the same four-hex tag, the newly read exact snapshot replaces the older colliding entry for that path; the latest read wins.
- A tag collision cannot authorize an edit against different live bytes, including an older colliding body that later reappears.
- A tag read in one MCP session cannot be used in another session, even when the visible four characters are identical.
- Separate partial reads of the same unchanged file union their displayed ranges under the same tag.
- Tags are intentionally ephemeral. A connector restart, MCP-session restart, cache eviction, or stale file state requires another `read`.

The full SHA-256 remains in `read` results for uses such as the `write` precondition. Agents normally need only the compact `edit_tag` for `edit`.

## `edit`: one public mode

The old `old_text` / `new_text` exact-replacement contract is retired. The public tool now has one contract:

```json
{
  "path": "src/example.ts",
  "edit_tag": "A1B2",
  "edits": [
    {
      "op": "replace",
      "start_line": 12,
      "end_line": 14,
      "content": "replacement line one\nreplacement line two"
    },
    {
      "op": "insert_after",
      "line": 29,
      "content": "new line after original line 29"
    },
    {
      "op": "delete",
      "start_line": 47,
      "end_line": 49
    }
  ]
}
```

### Operations

| Operation | Fields | Meaning |
| --- | --- | --- |
| `replace` | `start_line`, optional `end_line`, `content` | Replace an inclusive original line range. `end_line` defaults to `start_line`. |
| `delete` | `start_line`, optional `end_line` | Delete an inclusive original line range. |
| `insert_before` | `line`, `content` | Insert before an original line. |
| `insert_after` | `line`, `content` | Insert after an original line. |

### Edit invariants

- `edit_tag` and at least one operation are required.
- Every line number is from a `read` that returned that tag.
- Each targeted line or range must have been displayed under that exact snapshot. Elided or unread ranges are rejected.
- All operations use the same original coordinate space. An earlier insertion or deletion does not shift a later target.
- The connector acquires the canonical file lock, validates the live full text against the retained snapshot, validates every operation, computes the complete output, checks size and secret policy, and writes once.
- Overlapping replace/delete ranges are rejected.
- Two inserts into the same original gap are rejected; combine their content into one operation.
- An insert strictly inside a replaced/deleted range is rejected. Inserts at range boundaries are deterministic and allowed.
- Uniform CRLF, LF, or bare-CR line endings are preserved. Mixed-line-ending files are rejected; use `apply_patch` for those files.
- A UTF-8 BOM and the original terminal-newline shape are preserved.
- One terminal newline in an operation's `content` is treated as formatting rather than an extra blank line.
- A byte-identical overall edit is rejected.
- A successful edit returns a fresh four-hex tag, but its changed lines have not yet been displayed under that new snapshot. Re-read before another line-anchored edit.

### No automatic stale recovery yet

Oh My Pi can sometimes recover a stale anchor by comparing retained snapshots and uniquely relocating unchanged content. CodexPro currently fails closed instead:

- a changed live file produces a hard stale-tag error;
- a tag that aged out or belongs to another MCP session produces a re-read error;
- a colliding tag with different full content is rejected;
- no line is silently remapped to a similar-looking location.

Automatic recovery can be reconsidered only with clear uniqueness rules, observability, and tests showing that it reduces meaningful friction without making edits less predictable.

## `batch`, not `macroops`

The public tool is named `batch`.

`macroops` suggests a named, persistent, or reusable macro language. This tool is intentionally none of those things: it is one bounded execution request over existing typed CodexPro tools. Reserving `macro` or `workflow` leaves room for a future reviewed design with persistence, parameters, branching, or approval boundaries.

### Parallel read batch

```json
{
  "workspace_id": "<workspace-id>",
  "mode": "parallel",
  "operations": [
    { "id": "source", "tool": "read", "args": { "path": "src/server.ts", "start_line": 1, "end_line": 180 } },
    { "id": "tests", "tool": "search", "args": { "path": "test", "query": "createCodexProServer" } },
    { "id": "tree", "tool": "tree", "args": { "path": "src", "max_depth": 2 } }
  ]
}
```

Parallel mode is restricted to explicitly parallel-safe read/analysis tools:

- `tree`
- `search`
- `read`
- `inspect_workspace`
- `git_status`
- `git_diff`

`show_changes` remains serial because it advances the session's review checkpoint.

### Serial edit, verify, and inspect

The edit tag must already be known when the batch request is authored; `batch` is not a dataflow language and does not interpolate one child result into later child arguments.

```json
{
  "workspace_id": "<workspace-id>",
  "mode": "serial",
  "operations": [
    {
      "id": "change",
      "tool": "edit",
      "args": {
        "path": "src/example.ts",
        "edit_tag": "A1B2",
        "edits": [
          { "op": "replace", "start_line": 10, "content": "replacement" }
        ]
      }
    },
    {
      "id": "tests",
      "tool": "bash",
      "args": { "command": "npm test" }
    },
    {
      "id": "after",
      "tool": "read",
      "args": { "path": "src/example.ts" }
    },
    {
      "id": "review",
      "tool": "show_changes",
      "args": {}
    }
  ]
}
```

Serial batch policy:

- Maximum 12 operations.
- Child IDs must be unique when supplied.
- The outer `workspace_id` applies to every child; nested workspace IDs are rejected.
- Recursion and arbitrary tool dispatch are not allowed. The child allowlist is explicit.
- At most one file-mutation child is allowed: `write`, `edit`, or `apply_patch`.
- Multiple changes within one file belong in one tagged `edit` call.
- One `apply_patch` may deliberately change several files because it already validates paths, obtains all file locks, and runs `git apply --check` before applying.
- Zero or more Bash children may follow the file mutation.
- Batch-embedded Bash is independently restricted to the safe check/build allowlist even when standalone Bash runs in `full` mode. Typical commands include tests, type checks, linters, supported builds, and read-only Git inspection.
- A Bash child before the file mutation is rejected.
- A Bash child fails on a non-zero exit, timeout, or terminating signal.
- `continue_on_error` is allowed only when every child is read-only.
- All batch-level policy, child availability, IDs, and child schemas are validated before the first child runs.
- Serial execution stops after a failed child and marks later operations skipped.
- Aggregate text and structured results are bounded by `maxOutputBytes`, with explicit truncation counters.
- Changed-path evidence is extracted before result compaction so output limits cannot hide a successful mutation from audit metadata.
- Batch is recorded as one aggregate metadata-only audit action; child payload text is not copied into the journal.

### Not a transaction

`batch` is not atomic and provides no rollback boundary. If the file mutation succeeds and a later test or read fails, the mutation remains applied. The response reports each child as succeeded, failed, or skipped and retains the changed-path evidence.

This is deliberate. A general rollback promise would be false for arbitrary project commands and filesystem side effects.

## Review-only Oh My Pi candidates

Nothing in this section was implemented as part of the tagged-edit/batch work.

| Candidate | Recommendation | Why / MCP adaptation |
| --- | --- | --- |
| Structural search (`ast_grep`) | **High priority** | Strong read-only MCP fit. It can distinguish syntax from text, return captures and parse issues, and improve refactor discovery over regex. CodexPro should use local workspace paths only, bounded results, explicit language inference, and no external/internal-URL routing. |
| Read-only LSP actions | **High priority, separate design** | Diagnostics, definitions, references, hover, and symbols add semantic project knowledge beyond static inventory. Start read-only; explicitly design server discovery, process lifetime, timeouts, workspace trust, and output bounds. |
| Structural rewrite (`ast_edit`) | **Promising after structural search** | Do not expose immediate broad writes. Use an MCP-native preview object containing exact affected-file identities and a bounded diff, followed by a separate apply call that recomputes and verifies preview parity before any write. |
| Preview/apply primitive | **Steal the invariant, not the `xd://` protocol** | Useful for risky multi-file refactors, LSP rename, and code actions. MCP should expose typed `preview_*` / `apply_*` calls rather than harness-specific virtual write paths. |
| Safe stale-anchor recovery | **Defer** | The snapshot/cache foundation now exists, but automatic relocation still needs strict uniqueness, conflict and provenance rules plus strong observability. |
| Grep pagination/context refinements | **Selective improvements only** | CodexPro already has `search`; a second grep tool would duplicate surface area. Bounded per-file grouping, context lines, and explicit partial-coverage metadata remain useful ideas. |
| Bash dedicated-tool routing | **Small policy improvement** | A transparent interceptor could steer shell forms toward read/search/edit/write tools, but it should remain separate from command authorization and must not grow into a shell parser. |
| DAP debugger | **Potentially valuable, later and opt-in** | Runtime debugging can solve problems static tools cannot, but a CodexPro design should start with an adapter allowlist and reduced action set, disable memory writes and arbitrary custom requests, bound one session per workspace, and expose lifecycle/audit clearly. |
| Checkpoint/rewind | **Do not port** | Oh My Pi's checkpoint is conversation-message state rather than a Git or filesystem checkpoint. Session compaction belongs to the MCP host/harness. |
| `compact`, `ask`, task/subagent orchestration | **Do not port** | These manage model conversation, UI, or agent scheduling. CodexPro should remain a repository capability server rather than another agent harness. |
| Browser/computer/image tools | **Do not port** | These are outside the repository boundary and belong to host-native or dedicated MCP capabilities. |
| Persistent eval/runtime | **Low priority / likely no** | Retained runtimes and tool/subagent bridges materially expand process lifetime, isolation, cancellation, network, and cleanup surfaces. Reconsider only for a concrete workflow with a deliberately narrow runtime. |

### Suggested review order

1. Structural search only.
2. Read-only LSP design and process-security review.
3. A generic preview/apply primitive.
4. Structural rewrite and LSP rename/code actions on top of it.
5. Measure whether Bash dedicated-tool routing prevents meaningful misuse.
6. Evaluate a reduced, opt-in DAP debugger after read-only semantic tooling matures.
7. Revisit safe stale-anchor recovery only if re-reading is measurably costly.

## Verification

Dedicated MCP-level coverage includes:

- non-adjacent operations using frozen original coordinates;
- four-hex collision rejection through exact full-text validation;
- per-MCP-session tag/provenance isolation;
- displayed-range enforcement and range union across partial reads;
- legacy exact-text schema rejection;
- stale-tag rejection with no file change;
- overlap rejection before writing;
- CRLF and final-newline preservation;
- parallel read batches;
- serial edit, verification Bash, read, and review workflows;
- rejection of multiple file mutations before any child executes;
- validation of every child schema before an earlier write can run;
- rejection of non-allowlisted batch Bash before mutation;
- explicit partial completion when later verification fails;
- non-zero Bash exit handling and serial skip behavior;
- read-only continuation after failure;
- aggregate output bounding without losing mutation evidence;
- metadata-only audit behavior for tagged edits and batches.

Run:

```bash
npm test
npm run smoke
npm run stress
```
