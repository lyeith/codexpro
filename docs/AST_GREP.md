# Structural Search with `ast_grep`

Status: implemented on 2026-08-31.

CodexPro exposes the same bounded ast-grep provider through:

```text
MCP tool:  ast_grep
CLI:       codexpro ast-grep
Alias:     codexpro ast
Direct bin: codexpro-ast-grep
```

The provider uses the packaged `@ast-grep/cli` native executable. It performs local Tree-sitter structural matching inside the selected workspace and does not make network requests.

## What it is for

Use `ast_grep` when the question is about source syntax rather than raw text:

```text
calls whose first argument has a particular shape
functions, classes, imports, catch blocks, or object literals
API calls regardless of whitespace and formatting
code occurrences excluding comments and ordinary strings
metavariable captures from a structural pattern
```

Use ordinary `search` for text, regular expressions, configuration paths, changed files, and added/removed Git lines.

`ast_grep` is not a language server. It does not resolve inferred types, overloaded methods, aliases across module graphs, runtime dispatch, semantic references, or implementations. Those remain a separate future read-only LSP capability.

## MCP pattern mode

```json
{
  "workspace_id": "<workspace-id>",
  "pattern": "console.log($ARG)",
  "language": "ts",
  "path": "src",
  "globs": ["**/*.ts", "!**/*.generated.ts"],
  "context_before": 2,
  "context_after": 3,
  "max_results": 100
}
```

Exactly one of `pattern` or `kind` is required.

A pattern may use ast-grep metavariables such as:

```text
$ARG       one syntax node
$$ARGS     named multiple-node capture where supported
$$$ARGS    variadic capture
```

The result contains bounded capture metadata. The readable response omits punctuation-only variadic captures to reduce noise, while structured output retains the bounded native capture list.

Optional pattern controls are:

```text
selector
strictness = cst | smart | ast | relaxed | signature | template
```

`selector` and `strictness` apply only to pattern mode.

## MCP kind mode

Use a Tree-sitter syntax-node kind when the shape itself is sufficient:

```json
{
  "workspace_id": "<workspace-id>",
  "kind": "function_declaration",
  "language": "ts",
  "path": "src",
  "context_before": 1,
  "context_after": 2
}
```

Kind mode is useful for inventory and broad syntax discovery. Pattern mode is normally more selective.

## Language selection

`language` accepts ast-grep language identifiers such as:

```text
js
jsx
ts
tsx
py
go
rust
java
c
cpp
csharp
swift
```

When omitted, ast-grep infers the language from file extensions. Supplying a language is preferable when querying one known language or an unusual extension.

## Response contract

The MCP result includes:

```text
provider = ast-grep-cli
provider_version
mode = pattern | kind
matches
contexts
truncated
has_more
next_cursor
query_fingerprint
warnings
```

Each match includes:

```text
path
language
start/end line and column
start/end byte offset
bounded matched text
bounded metavariable captures
editable
edit_tag when eligible
```

Each context block includes numbered source, its matching result indexes and lines, and edit provenance when eligible.

## Search-to-edit provenance

A complete current-file ast-grep context can be used directly by the normal tagged `edit` tool.

Before returning an `edit_tag`, CodexPro:

1. runs ast-grep against the guarded workspace path;
2. rereads the current exact file bytes through `PathGuard`;
3. verifies that the ast-grep byte range still has the same byte length and SHA-256 digest;
4. records the complete current file in the authenticated principal's normal snapshot store;
5. marks only the exact numbered context range returned to the caller as displayed.

The result can then be edited without a redundant `read`:

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

All normal tagged-edit guarantees still apply. The complete file must remain byte-identical to the retained snapshot, and the target range must have appeared in a complete returned context.

No edit provenance is granted when:

```text
the file changed during structural search assembly
the match or context exceeded display bounds
the file is blocked, binary, too large, or unreadable
the caller is the standalone CLI rather than an authenticated MCP session
```

## Context and pagination

Options shared with contextual text search are:

```text
context_before     0-20, default 2
context_after      0-20, default 2
group_by_file      default true
max_results        bounded by the configured search limit
cursor             opaque continuation cursor
timeout_ms         1000-60000, default 15000
```

Overlapping or adjacent contexts in one file are merged by default.

Pagination uses an opaque offset cursor tied to:

```text
workspace id
provider and provider version
pattern or kind
language
selector and strictness
path and globs
hidden-file setting
context sizes and grouping
```

Changing any of those inputs produces `ast_grep_cursor_mismatch`.

The cursor is not a repository snapshot. CodexPro reruns the structural query and skips the number of eligible matches already returned. If files change between pages, later result positions can change. Restart without a cursor when the repository has materially changed.

## Globs and hidden files

`globs` accepts up to 64 include or exclusion filters:

```json
{
  "globs": ["**/*.ts", "!**/*.test.ts"]
}
```

An exclusion starts with `!`.

`include_hidden=true` asks ast-grep to inspect hidden files, but it never overrides CodexPro's blocked-path policy. Safety exclusions are appended after caller globs, and every returned file is independently resolved through `PathGuard` before it is accepted.

## Parallel batch use

`ast_grep` is read-only and parallel-safe:

```json
{
  "workspace_id": "<workspace-id>",
  "mode": "parallel",
  "operations": [
    {
      "id": "handlers",
      "tool": "ast_grep",
      "args": {
        "pattern": "router.$METHOD($PATH, $HANDLER)",
        "language": "ts",
        "path": "src"
      }
    },
    {
      "id": "empty_catches",
      "tool": "ast_grep",
      "args": {
        "pattern": "try { $$$BODY } catch ($ERR) {}",
        "language": "ts",
        "path": "src"
      }
    }
  ]
}
```

This is preferable to several serial structural queries when the questions are independent.

## CLI

Human-readable output:

```bash
codexpro ast-grep \
  --root ~/Projects/example \
  --pattern 'console.log($ARG)' \
  --lang ts \
  --path src
```

Structured output:

```bash
codexpro ast-grep \
  --kind function_declaration \
  --lang ts \
  --path src \
  --json
```

The shorter alias is available:

```bash
codexpro ast --pattern 'oldApi($ARG)' --lang ts
```

A direct executable is also published:

```bash
codexpro-ast-grep --pattern 'oldApi($ARG)' --lang ts
```

The CLI uses the same provider, parser, cursor, bounds, path guard, and result types as MCP. It intentionally does not create edit tags because it is not attached to an authenticated MCP snapshot store.

## Executable resolution

CodexPro resolves ast-grep in this order:

```text
CODEXPRO_AST_GREP_PATH
AST_GREP_PATH
packaged @ast-grep/cli executable
ast-grep on PATH
```

Ordinary installations use the packaged executable. An explicit override is useful for development or controlled pinning.

If no executable is available, the MCP tool returns `ast_grep_unavailable` with installation/configuration recovery guidance.

## Bounds and failure behavior

The provider enforces:

```text
workspace and blocked-path guards
maximum query, language, selector, glob, and cursor sizes
maximum result count and context lines
per-match JSON-event limits
native stdout and stderr limits
file scan and context byte limits
native process timeout
MCP cancellation
bounded capture count and capture text
bounded public audit metadata
```

CodexPro terminates the native process as soon as one additional match proves that another page exists. That intentional termination is distinguished from a real ast-grep failure.

Invalid query combinations fail before the native process starts. Native parse or pattern failures return `ast_grep_failed`; timeout and cancellation have separate error codes.

## Audit privacy

Public activity records retain only bounded operational metadata such as:

```text
pattern byte count and SHA-256 digest
kind, language, selector, and strictness
path and glob count
context sizes
cursor presence
provider/version and mode
match, context, warning, and editable-match counts
continuation state and query fingerprint
```

They do not retain:

```text
pattern text
cursor text
matched source
captures
context bodies
edit tags
raw native output
```

## Deliberate exclusions

This implementation does not expose:

```text
ast-grep rewrite/apply operations
project YAML rule execution
interactive editor/LSP mode
semantic definitions, references, implementations, or types
a persistent ast-grep daemon
```

A future structural rewrite should use an explicit preview/apply contract that revalidates every affected file and replacement set before the first write. It should not turn this read-only search tool into an immediate broad mutation surface.
