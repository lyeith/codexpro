# Design - CodexPro

A locked product-system note for CodexPro docs and the local admin surface.
Every redesign should keep the same trust story: ChatGPT can act on one
configured local repository through a token-protected MCP bridge, using either
the direct workspace or explicit isolated worktree handles, while shell,
writes, Codex history, and handoff execution stay explicit user choices.

## Genre

modern-minimal developer tool

## Positioning

Use ChatGPT like your local coding agent.

CodexPro should explain itself in this order:

1. Install the CLI.
2. Run setup inside one repo.
3. Paste the copied Server URL into ChatGPT Developer Mode.
4. Let ChatGPT inspect, edit, verify, or hand off work inside that workspace.
5. Keep the safety boundary visible: it is a local bridge, not a quota bypass,
   model proxy, hosted SaaS, or OS sandbox.

## Worktree Isolation

- MCP worktree mode has no shared default checkout.
- `create_workspace` returns a durable application-level `workspace_id`; it is
  not derived from an MCP transport session.
- Every repository tool fails closed without the exact handle.
- Caller-provided labels never control checkout paths or branch names.
- Transport expiry and connector restart preserve worktrees and dirty changes.
- Clean removal is explicit and preserves the Git branch.

## Multi-Project Catalog

- One connector may load a versioned catalog of stable project ids and canonical roots.
- Project selection is explicit during workspace creation; it is never mutable transport-session state.
- After creation, the opaque `workspace_id` is the sole routing input and remains bound to principal, project, repository scope, branch, and base commit.
- Worktree quotas are global and optionally per project. Git lifecycle locks use the common Git directory so separate monorepo scopes cannot race Git metadata updates.
- Catalog order is not identity. Removed, renamed, or repository-rebound projects fail closed on retained leases.
- `--root` remains a synthetic one-project catalog for backward compatibility.

## Macrostructure Family

- Marketing/docs pages: left-led product workbench with a visible three-step
  path, trust boundary, and reference sections below.
- Local admin pages: compact control surface with connection profile first,
  live runtime guardrails second, CLI-only controls below, and raw paths
  visually secondary.
- Content pages: long-document reference with short setup recipes first and
  detailed options after.

## Theme

- Paper: near-white blue
- Ink: deep neutral blue-black
- Accent: Codex blue
- Accent use: links, primary actions, selected tabs, status highlights
- Avoid: orange/amber, purple gradients, fake terminal/browser frames, invented
  metrics, and decorative glow.

## Typography

- Display: Geist-like system sans, heavy weight, normal style.
- Body: system sans, normal style.
- Mono: system monospace for commands, paths, tool names, and IDs only.
- Headings stay compact. Hero copy should be short enough to read in one scan.

## Spacing

Use a 4-point rhythm. Dense admin controls can use row dividers and grouped
fieldsets. Marketing sections can breathe, but the first viewport must still
show installation and trust details.

## Motion

No cinematic motion. Use hover, active press, copy confirmation, and reduced
motion support. Animate transform and opacity only.

## Copy Rules

- Say what CodexPro does, then say what it does not do.
- Do not claim permanent ChatGPT memory. Say repo-backed context files.
- Do not imply CodexPro unlocks models, bypasses limits, or automates approval
  gates.
- Do not expose raw local paths as marketing proof. Local admin can show them
  because it is token-protected and opened by the local user.

## Shared Components

- Primary action: blue filled button.
- Secondary action: white button with blue border.
- Trust boundary: compact list or table near setup.
- Commands: real text blocks with copy buttons, no fake terminal chrome.
- Admin panels: white surfaces, 1px blue-gray rules, no nested-card clutter.
