# Contextual, Editable, Configuration, and Diff-Aware Search

Status: implemented on 2026-08-31.

CodexPro keeps one `search` tool rather than splitting ordinary lookup, configuration lookup, and Git-diff lookup into unrelated tools. The common response contract provides bounded context blocks, stable continuation cursors, and edit provenance where the returned text represents current workspace bytes.

## Ordinary contextual search

```json
{
  "workspace_id": "<workspace-id>",
  "query": "registerCodexTool",
  "path": "src",
  "glob": "**/*.ts",
  "context_before": 3,
  "context_after": 5,
  "group_by_file": true,
  "max_results": 50
}
```

Each result page contains:

- `matches`: compact match metadata;
- `contexts`: numbered source blocks around those matches;
- `has_more` and an opaque `next_cursor` when another page is available;
- a `query_fingerprint` identifying the complete search shape;
- the engine and scope used;
- bounded warnings.

Overlapping or adjacent context windows in one file are merged when `group_by_file=true`, which is the default. Context before and after may each be set from 0 to 20 lines.

## Cursor contract

Pagination is keyset-based rather than an in-memory result offset. A cursor contains the exact search-shape fingerprint and the last returned source coordinate. It may be reused only with identical values for the query, kind, scope, path, glob, hidden-file setting, Git comparison, context sizes, and grouping mode.

```json
{
  "workspace_id": "<workspace-id>",
  "query": "registerCodexTool",
  "path": "src",
  "context_before": 3,
  "context_after": 5,
  "cursor": "<next_cursor>"
}
```

Changing an option produces `search_cursor_mismatch` and a recovery hint to restart without a cursor. The cursor is stable across transport calls, but it is not a historical repository snapshot: files may still change between pages. The coordinate-based continuation avoids replaying an already returned line merely because its text changed.

## Search-to-edit provenance

A complete current-file context block can establish the same four-hex edit provenance as `read`.

CodexPro performs the following before returning an `edit_tag`:

1. discover the lexical, configuration, or added-diff match;
2. read the current exact file bytes through `PathGuard`;
3. verify that the match still exists at the reported source coordinate;
4. retain the complete file snapshot in the authenticated principal's normal snapshot store;
5. mark only the exact numbered context ranges returned to the caller as displayed.

The result may then be edited without an otherwise redundant `read`:

```json
{
  "workspace_id": "<workspace-id>",
  "path": "src/example.ts",
  "edit_tag": "A1B2",
  "edits": [
    {
      "op": "replace",
      "start_line": 42,
      "content": "replacement"
    }
  ]
}
```

The normal tagged-edit rules still apply. The target line must be inside a returned context block, the complete live file must still equal the retained snapshot, and the tag must not be reused after a mutation.

No edit tag is granted when:

- the file changed while search was being assembled;
- a context block had to be shortened by the context byte limit;
- the result refers to a historical removed diff line;
- the file is blocked, binary, too large, or otherwise unreadable.

Several disjoint context blocks from one unchanged file share one tag while unioning their displayed ranges. A match is marked editable only when its own complete block established provenance; it cannot borrow provenance from another block in the same file.

## Structured configuration queries

Use `kind=config` for path-oriented lookup in JSON, JSONC, YAML, and TOML:

```json
{
  "workspace_id": "<workspace-id>",
  "kind": "config",
  "query": "jobs.*.steps[*].uses",
  "path": ".github/workflows",
  "glob": "**/*.yml"
}
```

Supported query forms include:

```text
scripts.test
servers[0].host
servers[*].host
jobs.*.steps[*].uses
/servers/0/host
```

`*` matches one key or array index at that exact depth. Quoted bracket keys are available for dots or other punctuation in a key:

```text
compilerOptions.paths["@app/*"]
```

`config_format=auto` infers the format from `.json`, `.jsonc`, `.yaml`, `.yml`, or `.toml`; it can be overridden with `json`, `yaml`, or `toml`.

The implementation is deliberately source-oriented:

- JSON/JSONC supports comments and trailing commas while retaining source lines;
- YAML covers ordinary mappings, sequences, inline sequence mappings, and block-scalar anchors;
- TOML covers keys, dotted keys, tables, and arrays of tables.

It is not a schema validator or a fully resolving configuration runtime. Complex YAML tags, anchors/merges, unusual multiline constructs, and semantic interpolation are not evaluated. Unsupported source forms are skipped with bounded warnings rather than silently returning invented values.

Configuration results include the canonical address, source line range, summary text, context, and edit provenance when the current source block is complete.

## Git-aware scopes

`scope` controls where matching occurs:

| Scope | Meaning | Editable provenance |
| --- | --- | --- |
| `workspace` | Normal current workspace files | Yes |
| `changed_files` | Current files whose paths differ from the selected Git base | Yes |
| `diff_added` | Added-side lines only | Yes when the line still matches current bytes |
| `diff_removed` | Removed-side historical lines only | No; explicitly read-only |

Git comparison options are:

```text
diff_target=worktree   base_ref versus index + working tree
diff_target=staged     base_ref versus index
diff_target=head       merge-base(base_ref, HEAD) versus HEAD
```

`base_ref` defaults to `HEAD`. For the worktree target, `include_untracked=true` includes untracked files in `changed_files` and treats their lines as added for `diff_added`.

Examples:

```json
{
  "query": "TODO",
  "scope": "changed_files",
  "diff_target": "worktree"
}
```

```json
{
  "query": "deprecatedCall",
  "scope": "diff_added",
  "base_ref": "origin/main",
  "diff_target": "head"
}
```

```json
{
  "query": "removedSecurityCheck",
  "scope": "diff_removed",
  "diff_target": "worktree"
}
```

Removed-side results intentionally do not authorize edits because those bytes are not the current file snapshot.

## Structured repository analysis

The existing `intent`, `symbol`, and `include_tests` repository-analysis options remain available for the first page of a normal workspace text search. They are not mixed into configuration or Git-diff scopes, and continuation pages return only the deterministic lexical/configuration/diff page.

## Bounds and privacy

Search is bounded by configured match limits, scan-size limits, context-line limits, context byte budgets, configuration-file limits, Git output limits, and overall MCP output compaction. A page may stop before `max_results` so that every returned current-file context remains complete; `next_cursor` resumes after the last actually returned match.

The public action journal records only safe search metadata such as kind, scope, context sizes, cursor presence, query byte count/digest, result counts, engine, and continuation state. It does not store query text, cursor text, context bodies, configuration values, or raw results.
