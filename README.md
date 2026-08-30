<p align="center">
  <img src="docs/favicon.svg" width="72" height="72" alt="CodexPro logo">
</p>

<h1 align="center">CodexPro</h1>

<p align="center">
  Give ChatGPT local coding tools for repos you explicitly allow.
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/codexpro"><img alt="npm" src="https://img.shields.io/npm/v/codexpro?style=flat-square"></a>
  <a href="https://github.com/rebel0789/codexpro/actions"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/rebel0789/codexpro/ci.yml?branch=main&style=flat-square"></a>
  <a href="https://github.com/rebel0789/codexpro/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/rebel0789/codexpro?style=flat-square"></a>
  <a href="https://rebel0789.github.io/codexpro/"><img alt="Website" src="https://img.shields.io/badge/site-GitHub%20Pages-67e8f9?style=flat-square"></a>
</p>

## What it is

CodexPro is a local MCP server. It connects **your ChatGPT session** to **your machine** and **repos you allow**.

ChatGPT can read, search, edit, review, verify, import attachments, and write handoff plans. It stays inside those roots.

It is not a hosted SaaS product, model proxy, quota bypass, account pool, or remote shell service.

## Install

Needs:

- Node.js 20+
- A ChatGPT account that can create custom MCP plugins
- An HTTPS URL to your machine for ChatGPT web (tunnel or Tailscale Funnel)

```bash
npm install -g codexpro
cd /path/to/your/repo
codexpro setup
```

## Connect in ChatGPT

1. `Settings -> Security and login` → turn **Developer mode** on (keep CSP enforcement on).
2. `Settings -> Plugins` → Plugins tab → **+** beside Search plugins.
3. Create a plugin named `CodexPro`.
4. Connection: **Server URL** → paste the URL CodexPro copied.
5. Authentication: **No Authentication / None** (change this if the form defaults to OAuth).

CodexPro auth is the token already in that URL. Do not share the URL.

| Open Plugins and click `+` | Complete the New Plugin form |
| --- | --- |
| ![Open Plugins and click the plus button](docs/images/chatgpt-plugins-add.png) | ![Complete the New Plugin form](docs/images/chatgpt-plugin-details.png) |

Daily use from the same repo:

```bash
codexpro start
```

If plugin creation fails, run `codexpro connection-test` and check whether ChatGPT requests reach the local server.

## What ChatGPT can do

With workspace write mode (the normal agent setup):

- read, search, and inspect the repo
- edit with `write`, four-hex-tagged multi-hunk `edit`, or guarded `apply_patch`
- bundle bounded related operations with `batch` (parallel reads or serial edit → checks → review)
- import ChatGPT attachments with `import_file`
- run allowlisted checks with `bash`
- review diffs with `show_changes`
- write plans under `.ai-bridge`
- export a context bundle for chats that cannot call tools

`read` returns a four-character `edit_tag` backed by the exact full file snapshot retained for that MCP session. All hunks in one `edit` call address the original displayed line numbers, so earlier insertions do not shift later targets; the old `old_text` / `new_text` mode is retired. Serial `batch` permits one file mutation followed by allowlisted test/typecheck/lint/build or Git-inspection commands, reads, and `show_changes`. See [Tagged Multi-Hunk Edit and Batch Operations](docs/HASH_EDIT_AND_BATCH.md).

## Multiple projects

One CodexPro process can allow more than one repo. Extra saved roots are the lightweight option:

```bash
codexpro settings set --project ~/code/web --project ~/code/api
codexpro settings show
codexpro start
```

Ask ChatGPT to `open_workspace` on an allowed root. `open_current_workspace` returns to the launch repo.

Use a named, persistent catalog when ChatGPT should select projects by id or create new ones:

```bash
cp projects.example.json ~/.config/codexpro/projects.json
codexpro start --projects-file ~/.config/codexpro/projects.json
```

Open one catalog project with `open_workspace(project_id="web")`, or resolve several handles at once:

```text
open_workspace(project_ids=["web", "api", "shared"])
```

Duplicate ids are collapsed, every id is validated before any requested workspace is opened, and the first entry becomes the selected primary. Multi-open skips file trees by default and divides an explicit `max_files` tree budget across the returned workspaces. Reuse the returned `workspace_ids`; repeating a singular open is idempotent and omits its tree unless `include_tree=true` is explicitly requested.

Add one or more `creationRoots` to the catalog for directories that may contain new projects but must not themselves be opened or provisioned as projects:

```json
"creationRoots": [
  { "id": "projects", "label": "Projects directory", "root": "~/Projects" }
]
```

With workspace write mode, the connector exposes `create_project`. Every new directory is a direct child of the selected `parent_id`, which may name a creation root or an existing project. Prefer a creation root so repositories remain siblings. The new project is added atomically to the catalog and is immediately available to `open_workspace` or `create_workspace`:

```text
create_project(project_id="scratch", parent_id="projects", source="empty")
create_project(project_id="new-api", parent_id="projects", source="git")
create_project(project_id="fork", parent_id="projects", source="git", repository="https://example.com/team/repo.git")
```

`source="git"` without `repository` initializes Git and creates an empty initial commit on `main` by default. A supplied repository is cloned without submodules. Local clone sources must remain inside an allowed root. Raw empty projects are available in direct-workspace mode; isolated MCP worktree mode requires Git-backed projects.

Project creation is hidden in read-only/handoff/connection-test modes and when no persistent projects file is configured. Creation-root and project IDs/paths must be unique. If the catalog changes outside CodexPro while the server is running, creation fails closed until restart instead of overwriting that edit.

For two ChatGPT accounts or hard isolation, run two CodexPro processes on different ports and Server URLs.

## Commands

```bash
codexpro setup
codexpro start
codexpro start --root /path/to/repo
codexpro doctor
codexpro connection-test
codexpro settings
codexpro inspect
codexpro review
```

Useful modes:

```bash
# Normal direct coding workflow: tree/search/read/write/edit/bash, without AI-Bridge tools.
codexpro start --write workspace --handoff-mode off

codexpro start --no-bash
codexpro start --tool-mode minimal
codexpro start --tool-mode full
codexpro start --mode handoff
codexpro start --mode pro
codexpro start --headless
```

`standard` remains the useful direct repository surface: workspace selection, inspection, tree/search/read, write/edit/patch/import, bash, and change review. AI-Bridge handoff/context tools are hidden by default and can be enabled independently with `--handoff-mode on`; deliberately selecting `--mode handoff` or `--write handoff` enables them automatically.

Opt-in tool cards:

```bash
CODEXPRO_TOOL_CARDS=1 codexpro start
```

### Direct-action observability

CodexPro can append a local, metadata-only `codexpro.action.v1` record for every direct MCP action, including actions routed through the `codexpro` supertool. This is an engineering **debug/diagnostic** stream, not a day-to-day operations feed:

```bash
codexpro start --audit metadata
# Optional controls:
# codexpro start --audit metadata \
#   --audit-log ~/.codexpro/audit/tool-calls.jsonl \
#   --audit-max-bytes 67108864 \
#   --audit-retain-actions 50000
```

Each record has a source-owned action ID and monotonic sequence, opaque actor/request/session references, effective tool and operation class, project/workspace, safe targets, outcome and duration. Mutations also include bounded before/after path and Git-state evidence. CodexPro deliberately does **not** retain file bodies, prompts/plans, search text, shell command text, bearer tokens, attachment bytes, stdout, stderr, or raw tool results. Sensitive free text is represented only by byte counts and SHA-256 digests where correlation is useful.

Use the read-only activity tools rather than scanning chat history or Git logs:

```text
activity_list(limit=100)
activity_list(after_sequence=0, limit=100, mutating_only=true)
activity_get(action_id="cpa_...")
activity_status()
activity_export(after_sequence=0, limit=100, format="jsonl")
```

Auditing is off by default, and the `activity_*` debug tools are not registered until `--audit metadata` is explicitly enabled. The journal is created with local-user permissions under `~/.codexpro/audit/` unless `CODEXPRO_AUDIT_LOG` overrides it. Retention compacts the active journal without renumbering actions; an expired cursor fails explicitly with the retained boundary. The configured journal, lock, retention index, and temporary rotation files are blocked from workspace file tools even when the journal is placed under an allowed root.

The authenticated HTTP server also exposes `/activity`. It shows the latest retained CodexPro actions for each configured project, local timestamps, and the configured checkout's current tracked diff against `HEAD`. Each action is an expandable card: tagged edits and patches show changed paths, line additions/deletions, operation counts, and file-size evidence; batches show file-mutation, verification-command, success/failure, and truncation counts; reads show ranges and result sizes; searches show scope and result counts; Bash shows a narrow safe label such as `npm test`, `go vet`, or `git status`, plus timeout, exit, output-size, and fingerprint metadata. Raw shell arguments remain deliberately unretained. Untracked file names are listed without their contents, safety-blocked paths are hidden, and all output is bounded. Git state remains visible when auditing is off, but recent actions require `--audit metadata`.

See [ACTION_JOURNAL.md](ACTION_JOURNAL.md) for the schema, privacy boundary, cursor contract, retention behavior, dashboard semantics, and consumer guidance.

## Public HTTPS options

ChatGPT web needs HTTPS:

```bash
codexpro start --tunnel cloudflare          # quick demo URL (changes)
codexpro ngrok --hostname your.ngrok-free.dev
codexpro stable --hostname codexpro.example.com --tunnel-name codexpro
codexpro tailscale --hostname your-device.your-tailnet.ts.net
codexpro start --tunnel none                # local only
```

Keep a stable token for stable hostnames:

```bash
mkdir -p ~/.codexpro
openssl rand -hex 32 > ~/.codexpro/http-token
chmod 600 ~/.codexpro/http-token
```

Prefer `Authorization: Bearer <token>` when the client supports headers. The `?codexpro_token=` query form is a personal compatibility fallback.

## Safety defaults

- Public tunnels require a CodexPro HTTP token (min 24 bytes)
- Writes stay hidden unless write mode is `workspace`
- Safe bash is the default
- Blocked paths cover `.env`, keys, `.git`, build caches, and similar
- Attachment import only accepts ChatGPT Apps SDK file objects from approved HTTPS hosts

Read [SECURITY.md](SECURITY.md) before exposing a tunnel.

## Update

```bash
npm install -g codexpro@latest
codexpro --version
```

Restart `codexpro start` after updating. Saved profiles under `~/.codexpro` stay in place.

## Development

```bash
npm install
npm run build
npm run smoke
npm run stress
npm run release:check
```

Publish only from the CodexPro root:

```bash
cd /path/to/codexpro
npm run release:publish
```

## Docs

- [Website](https://rebel0789.github.io/codexpro/)
- [FAQ](FAQ.md)
- [Security](SECURITY.md)
- [Stable URL guide](DOMAIN_SETUP.md)
- [Changelog](CHANGELOG.md)
- [Contributors](CONTRIBUTORS.md)
