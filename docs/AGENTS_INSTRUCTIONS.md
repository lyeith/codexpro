# Attached workspace instructions

`open_workspace` and `open_current_workspace` attach instruction **contents** to their text response. Reopening a workspace rereads
the files, so instruction changes take effect without restarting the server.

Instructions are attached in this order:

1. The global file from the **server's** Codex directory.
2. The file at the selected workspace root.

In each directory, the first existing name wins: `AGENTS.override.md`,
`AGENTS.md`, `agents.md`, then `.agents.md`. An override replaces the regular
file in that scope. Case-insensitive fallback names are supported. More specific
workspace instructions take precedence over global instructions.

The global directory is resolved from `--codex-dir`, `CODEXPRO_CODEX_DIR`,
`CODEX_HOME`, then `~/.codex`. A remote connector reads the server's copy. Provision or synchronize that file on the server.

Opening several projects attaches shared global text once, followed by each
project's separately labelled instructions. It does not combine one project's
rules with another's. Opening a project does not walk above its workspace root
or recursively load every subdirectory's instructions. Agents must inspect applicable deeper files before editing.

The structured response includes:

- `agents_loaded`: at least one file's content was loaded.
- `agents_path`: the first loaded workspace instruction path, when present
  (retained for compatibility; global-only opens can have no `agents_path`).
- `agents_files`: the loaded paths in application order.
- `agents_sources`: each selected file's scope, path, `loaded`, `truncated`,
  and optional `error`.
- `agents_complete`: every selected file was loaded without truncation. Absence
  is explicitly reported in text; a scope with no file has no source entry.

Existing secret redaction and path-labeling rules apply to attached text and
metadata. Project symlinks cannot read files outside their workspace. Only the
operator-configured global directory is read outside that boundary.

Instruction content has a total default budget of `min(60000, 60% of
CODEXPRO_MAX_OUTPUT_BYTES)`, further limited by `CODEXPRO_MAX_READ_BYTES`. The
budget is divided across selected files so a large global file cannot crowd out
local rules. Multi-open reserves up to half the budget for the shared global scope and
divides the remainder among requested workspaces. Truncated previews preserve UTF-8 boundaries and include
an explicit warning; unreadable selected files are reported rather than silently
falling back to lower-priority files. Inspect incomplete scopes before editing.
