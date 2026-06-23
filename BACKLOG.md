# Backlog

## Open

### Session-scoped `.ai-bridge` handoffs for parallel agents

CodexPro's handoff model currently uses singleton files such as
`.ai-bridge/current-plan.md`, `.ai-bridge/agent-status.md`,
`.ai-bridge/implementation-diff.patch`, and `.ai-bridge/execution-log.jsonl`.
That is simple for one workstream, but parallel agents can create contention as
independent plans, status updates, diffs, and execution logs overwrite or
interleave through the same paths.

**Scope trigger:** pick this up when CodexPro is used as a regular
parallel-agent bridge. Add first-class workstream/session namespacing so
`handoff_to_agent`, `read_handoff`, `codex_context`, `export_pro_context`,
`execute-handoff`, and `watch-handoff` can target
`.ai-bridge/sessions/<session_id>/...` instead of singleton bridge files.

**Acceptance criteria:** `handoff_to_agent` accepts a sanitized `session_id` or
`context_dir` and returns the exact matching local execution command; worker
status, implementation diff, execution log, decisions, and open questions are
session-scoped; `watch-handoff` maintains duplicate-run state per session; a
coordinator can read an aggregate index without workers writing to shared status
files; legacy singleton `.ai-bridge` paths remain supported for single-agent
workflows.
