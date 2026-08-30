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

Each authenticated connector principal owns an independent bounded snapshot store. The store is process-wide rather than tied to one transient MCP server instance:

- maximum 256 file paths;
- maximum four retained versions per path;
- maximum 64 MiB of retained full text;
- shared across HTTP server instances for the same principal, so transport rotation does not break `read` followed by `edit`;
- canonical real paths, so a file and an allowed symlink alias share identity;
- exact full-text comparison when resolving a tag;
- displayed-line provenance attached to each exact snapshot.

Consequences:

- If a newly read file body collides with an older retained body on the same four-hex tag, the newly read exact snapshot replaces the older colliding entry for that path; the latest read wins.
- A tag collision cannot authorize an edit against different live bytes, including an older colliding body that later reappears.
- A tag remains usable across transient HTTP transport/server-session changes for the same principal, but cannot cross to another authenticated principal.
- Separate partial reads of the same unchanged file union their displayed ranges under the same tag.
- Tags are intentionally ephemeral. A CodexPro process restart, cache eviction, or stale file state requires another `read`.

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
- Every line number comes from a `read` or from a complete current-file `search` context that returned that tag.
- Each targeted line or range must have been displayed under that exact snapshot. Elided, truncated, historical removed-diff, or otherwise unread ranges are rejected.
- All operations use the same original coordinate space. An earlier insertion or deletion does not shift a later target.
- The connector acquires the canonical file lock, validates the live full text against the retained snapshot, validates every operation, computes the complete output, checks size and secret policy, and writes once.
- Overlapping replace/delete ranges are rejected.
- Two inserts into the same original gap are rejected; combine their content into one operation.
- An insert strictly inside a replaced/deleted range is rejected. Inserts at range boundaries are deterministic and allowed.
- Uniform CRLF, LF, or bare-CR line endings are preserved. Mixed-line-ending files are rejected; use `apply_patch` for those files.
- A UTF-8 BOM and the original terminal-newline shape are preserved.
- One terminal newline in an operation's `content` is treated as formatting rather than an extra blank line.
- Reported additions and deletions are calculated from each anchored hunk independently. Untouched lines between distant hunks are not counted merely because the rendered fallback diff spans from the first change to the last.
- A byte-identical overall edit is rejected.
- A successful edit returns a fresh four-hex tag, but its changed lines have not yet been displayed under that new snapshot. Read or search the relevant current ranges before another line-anchored edit.

### No automatic stale recovery yet

Oh My Pi can sometimes recover a stale anchor by comparing retained snapshots and uniquely relocating unchanged content. CodexPro currently fails closed instead:

- a changed live file produces a hard stale-tag error;
- a tag that aged out, belongs to another authenticated principal, or predates a process restart produces a re-read error;
- a colliding tag with different full content is rejected;
- no line is silently remapped to a similar-looking location.

Automatic recovery can be reconsidered only with clear uniqueness rules, observability, and tests showing that it reduces meaningful friction without making edits less predictable.

Oh My Pi's separate replace-style editor has a configurable fuzzy-match threshold whose documented default is `0.95`. That is a similarity threshold for locating near-matching `old_text`; it is not a 95% hash check. CodexPro's tagged edit path has no equivalent fuzzy threshold: the compact tag resolves to retained full text, the live file must match that exact snapshot, and stale anchors fail closed. Any future relocation feature should use uniqueness and provenance rules rather than silently treating a globally similar file or line block as the same anchor.

## `batch`, not `macroops`

The public tool remains `batch`. It is still a bounded execution request rather than a persistent macro language, but its definition is now an ordinary editable JSON file.

Do not use it merely to wrap one or two ordinary reads, or a one-file mutation followed only by `read`/`show_changes`. Prefer direct calls in those cases. Use one consolidated batch for three or more independent parallel reads, a mutation followed by actual Bash verification, or a workflow deliberately retained for resume.

### Verification and explicitly retained calls become editable batch files

A normal verification workflow is unchanged:

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
      "id": "review",
      "tool": "show_changes",
      "args": {}
    }
  ]
}
```

After complete schema and safety preflight, a batch containing Bash verification is materialized by default. Other inline batches remain one-shot unless `persist=true` is supplied. A retained definition is written as:

```text
.codexpro-batches/7A3C.json
```

The file contains only executable configuration:

```json
{
  "version": 1,
  "mode": "serial",
  "continue_on_error": false,
  "operations": [
    { "id": "change", "tool": "edit", "args": {} },
    { "id": "tests", "tool": "bash", "args": { "command": "npm test" } },
    { "id": "review", "tool": "show_changes", "args": {} }
  ]
}
```

Missing operation IDs are normalized to `op_1`, `op_2`, and so on before the file is written. Run results are not written back into the definition, so merely executing a batch does not churn its edit tag.

The response returns `batch_path`, its four-hex filename tag, the original operation indexes, the first failed operation, and a ready-to-use resume reference.

The authenticated `/activity` page turns a retained `batch_path` into an **Open saved batch** link. The viewer reads the current JSON file through the normal path guard and shows operation IDs, tools, arguments, and raw JSON. It is not a historical snapshot: amending the batch changes what the viewer shows. If automatic retention has already pruned the file, the viewer reports that state instead of exposing an arbitrary replacement.

### Amend through normal file tools

A stored batch has no special edit protocol. Read it and use the existing tagged `edit` tool:

```text
read(path=".codexpro-batches/7A3C.json")
edit(path=".codexpro-batches/7A3C.json", edit_tag="41EF", edits=[...])
```

Then run the amended file from the failed operation:

```json
{
  "workspace_id": "<workspace-id>",
  "path": ".codexpro-batches/7A3C.json",
  "from": "tests"
}
```

`from` is inclusive and uses the stable operation ID. A zero-based `from_index` is available when needed. `path` cannot be combined with inline `operations`; `mode` and `continue_on_error` are read from the JSON file and must be changed by editing that file.

### Common recovery workflows

**The batch command is wrong:** read the batch file, amend the failed operation, then run the file from that operation ID.

**An upstream source edit was wrong:** repair the current source with the normal `read` and `edit` workflow, then run the stored batch from the failed test/check operation. The successful prefix is not replayed, so the standalone repair remains intact.

**The edit operation itself failed:** read the current source to obtain a fresh source `edit_tag`, update the edit operation inside the batch JSON, then run from that edit operation.

No historical revision or workspace checkpoint machinery is involved. Every stored invocation executes the current JSON definition against the current workspace state.

### Storage and retention

- Verification batches are auto-stored under `.codexpro-batches/` inside their workspace.
- Other inline batches stay one-shot unless the caller explicitly sets `persist=true`.
- CodexPro retains the 20 most recently created, amended, or run four-hex batch files per workspace.
- Running an auto-stored file refreshes its filesystem recency without rewriting the definition or changing its edit tag.
- Files are created with local-user permissions.
- In a Git workspace, CodexPro adds the scoped directory to `.git/info/exclude`; it does not modify the repository's tracked `.gitignore`.
- Non-Git workspaces still retain ordinary batch files, without Git exclusion.
- In read-only or connection-test mode, inline batches remain one-shot and no retained definition is created. Existing JSON files may still be executed read-only.
- Any workspace-relative `.json` file with the versioned batch shape can be run; only generated four-hex files participate in automatic retention.

### Execution policy

Parallel mode remains restricted to explicitly parallel-safe read/analysis tools:

- `tree`
- `search`
- `read`
- `inspect_workspace`
- `git_status`
- `git_diff`

`show_changes` remains serial because it advances the session's review checkpoint.

Serial policy remains:

- Maximum 12 operations.
- Child IDs must be unique.
- The outer `workspace_id` applies to every child; nested workspace IDs are rejected.
- Recursion and arbitrary tool dispatch are not allowed.
- A serial batch may contain several `write` and/or `edit` children only when every child resolves to a distinct canonical file.
- Duplicate canonical targets, including normalized aliases of the same path, are rejected before the first child runs. Multiple changes within one file belong in one tagged `edit` call.
- `apply_patch` is exclusive: when present, it must be the only file-mutation child because one patch may already change several files.
- `apply_patch` remains reserved for a deliberate raw Git multi-file diff or a file that tagged edit cannot handle; `*** Begin Patch` wrapper syntax is rejected.
- One valid `apply_patch` validates all paths, locks its targets, and runs `git apply --check` first.
- Zero or more verification-only Bash children may follow all file mutations.
- A Bash child before the final mutation is rejected, so later mutations cannot invalidate an earlier verification result.
- Bash non-zero exits, timeouts, and terminating signals fail the child.
- `continue_on_error` is allowed only when every child is read-only.
- The complete definition is validated before any child runs, even when execution starts from a suffix.
- Serial execution stops after failure and marks later selected operations skipped.
- Aggregate output remains bounded, while original indexes and mutation evidence survive result compaction.
- Public action tools and exports store only bounded batch path/count/resume metadata, never the JSON definition, file bodies, non-Bash child arguments, or shell text. The local on-disk journal privately retains bounded scripts for Bash children that actually ran so the authenticated `/activity` page can display them; `activity_list`, `activity_get`, and `activity_export` strip that private field.

### Not a transaction

`batch` is not atomic and provides no rollback boundary. If a mutation succeeds and a later test fails, that mutation remains applied. Resume simply starts at the requested current operation against the current workspace state.

This is deliberate: arbitrary project commands and filesystem side effects cannot honestly be promised a general rollback mechanism.

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
- normalization and materialization of inline definitions with stable operation IDs;
- editing retained JSON through ordinary `read` and tagged `edit`;
- resume by operation ID and zero-based index without replaying the successful prefix;
- standalone source repair followed by suffix resume;
- 20-file retention, active-file protection, per-file prune locking, local Git exclusion, and file permissions;
- read-only one-shot behavior and execution of existing definitions;
- invalid/ambiguous stored definitions failing before child side effects;
- stored batch metadata auditing without retaining definition payloads.

Run:

```bash
npm test
npm run smoke
npm run stress
```
