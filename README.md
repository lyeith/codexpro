<p align="center">
  <img src="docs/favicon.svg" width="72" height="72" alt="CodexPro logo">
</p>

<h1 align="center">CodexPro · lyeith fork</h1>

<p align="center">
  Serve your projects to local agents and MCP clients, with optional ChatGPT access.
</p>

<p align="center">
  <a href="https://github.com/lyeith/codexpro/actions"><img alt="Fork CI" src="https://img.shields.io/github/actions/workflow/status/lyeith/codexpro/ci.yml?branch=main"></a>
  · <a href="LICENSE">MIT license</a>
  · <a href="README_ZH.md">中文</a>
</p>

CodexPro runs on the machine that holds your code. One server can expose a catalog of named projects, let agents inspect and edit selected workspaces, supervise commands, and retain plans and handoffs across agent sessions.

This README describes **[lyeith/codexpro](https://github.com/lyeith/codexpro)**. The `codexpro` package on npm and the `rebel0789.github.io/codexpro` website belong to the upstream project; they do not identify a release of this fork. Install this repository from source to get the behavior documented here. The CLI and package still use the name `codexpro`.

[Install from source](#install-from-source) · [Configure projects](#configure-projects) · [Connect clients](#connect-clients) · [Tools and permissions](#tools-and-permissions) · [Durable runs](#durable-runs) · [Update](#update-a-source-install)

## Install from source

Use Git and Node.js with npm. Node 22 and 24 have been validated; the package also supports Node 20, which may require building the native SQLite dependency. If a prebuilt addon is unavailable, install Python and your platform's C/C++ build tools. Build and run with the same Node installation.

```bash
git clone --branch main https://github.com/lyeith/codexpro.git
cd codexpro
npm ci
npm run build
npm link
codexpro --version
git rev-parse --short HEAD
```

The `cd codexpro` above is into the **server's source checkout**, not one of the projects it will serve. `npm link` makes this checkout's commands available on your PATH; keep the checkout in place and rebuild after source changes. It can replace an existing global `codexpro` command. If your global npm prefix is not writable, use a user-owned prefix or run the CLI directly:

```bash
node /absolute/path/to/codexpro/scripts/codexpro.mjs --help
```

Substitute that command for `codexpro` in the examples below. You do not need a global installation. `npm ci` still downloads dependencies from your configured npm registry; installing CodexPro from source is not an offline dependency install.

For a separate installed snapshot, run `npm pack` from the built checkout, then install the **exact `.tgz` path it prints** with `npm install -g /absolute/path/to/codexpro-VERSION.tgz`. This uses your local package, not the registry's `codexpro` release. Keep the Git commit alongside the package: `--version` alone does not distinguish the fork from upstream.

## Configure projects

A persistent project catalog is the main setup path. Configure it once on the server machine and pass its path at startup; you do not have to start CodexPro from each repository.

Create or edit `~/.config/codexpro/projects.json` (create the parent directory if needed):

```json
{
  "version": 1,
  "defaultProject": "web",
  "projects": [
    { "id": "web", "label": "Website", "root": "~/Projects/web" },
    { "id": "api", "label": "API", "root": "~/Projects/api" }
  ],
  "creationRoots": [
    { "id": "projects", "label": "New projects", "root": "~/Projects" }
  ]
}
```

Replace these roots with existing directories **on the CodexPro machine**. `~` refers to the account running the server; relative roots resolve against the catalog's directory. `defaultProject` selects the default project, not the only accessible one. Do not combine `--projects-file` with `--root`.

`creationRoots` is optional. It permits creation of new direct-child projects without exposing the parent directory as an ordinary workspace. [projects.example.json](projects.example.json) also shows per-project `baseRef` and `maxWorktrees` settings for Git worktrees.

For guided setup, including a tunnel choice:

```bash
codexpro setup --projects-file "$HOME/.config/codexpro/projects.json"
```

For scripts and services, use `start` with explicit options as shown next. Saved profiles are associated with the catalog's default project; passing `--projects-file` on each launch makes project selection independent of your shell's working directory.

### Selecting and creating projects

In direct workspace mode, an agent discovers projects and opens the ones it needs:

```text
list_projects()
open_workspace(project_id="web")
open_workspace(project_ids=["web", "api"])
```

Reuse the returned `workspace_id` for later file, search, Bash and Git calls. `list_projects` also returns handles for read-only inspection; open the workspace before editing to load `AGENTS.md` and project guidance. Multi-open validates all requested IDs first and supports up to 12 projects.

With a persistent catalog and `--write workspace`, `create_project` can initialize or clone a new project and register it immediately:

```text
create_project(project_id="new-api", parent_id="projects", source="git")
create_project(project_id="scratch", parent_id="projects", source="empty")
```

Supply `repository` with `source="git"` to clone. New directories are direct children of the chosen creation root or project. An external edit to the catalog requires a server restart before further project creation; CodexPro does not overwrite that edit.

Single-project setup remains available with `codexpro setup --root /path/to/project` or `codexpro start --root /path/to/project`. Repeated `--project` options provide lightweight additional roots, but a catalog gives stable project IDs and persistent project creation.

For isolated Git workspaces, start with `--worktree-mode mcp` and safe or disabled Bash. Agents use `create_workspace`, `open_workspace`, `release_workspace` and `remove_workspace` for that lifecycle; full Bash is incompatible with this mode. Durable runs described below manage their own retained worktrees and claims.

## Connect clients

Choose the transport for the client that will connect. All HTTP examples below serve the same catalog and MCP tool surface.

### Local and LAN clients

For clients on the server machine:

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel none --host 127.0.0.1 --port 8787 --auth-mode static-token
```

For other machines on your LAN, replace the host with the server's actual LAN address:

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel none --host 192.168.1.50 --port 8787 --auth-mode static-token
```

Configure a **Streamable HTTP** client with `http://192.168.1.50:8787/mcp` and `Authorization: Bearer <token>`. The CLI generates a token if none is supplied; use `--token-file /path/to/private-token` for a stable credential. Non-loopback bindings require authentication. `--host 0.0.0.0` listens on all IPv4 interfaces, but clients must use a real server address, not `0.0.0.0`.

No cloudflared process is needed for `--tunnel none`. Plain HTTP does not encrypt the token or traffic; use it only on a trusted network, or put a TLS reverse proxy in front. **Current limitation:** `codexpro work` and managed `loop-handoff --mcp-url` reject authenticated plain HTTP outside localhost. Other MCP clients can use the LAN endpoint; those CLI adapters need HTTPS or a localhost connection.

For example, a local Codex client can use the following configuration, with the token available in its environment. See [OpenAI's MCP configuration guide](https://learn.chatgpt.com/docs/extend/mcp) for client setup.

```toml
[mcp_servers.codexpro]
url = "http://192.168.1.50:8787/mcp"
bearer_token_env_var = "CODEXPRO_HTTP_TOKEN"
```

Clients on the same machine can also launch a stdio server directly:

```bash
codexpro-mcp --projects-file "$HOME/.config/codexpro/projects.json" --write workspace
```

Configure that as the client's process command, not as an HTTP URL. Stdio starts a server per client. Use one shared HTTP server when several clients need the same durable runs; independent servers must not share job or work-control storage.

### ChatGPT connections

For the public Server URL flow, start the catalog server with an HTTPS tunnel:

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel cloudflare
```

Enable developer mode in ChatGPT's **Settings → Security and login**, then add the MCP connection from **Plugins → +**. Use the printed Server URL, including `/mcp`. For the CLI's token-in-URL compatibility flow, choose **No Authentication / None** in the form: CodexPro still validates the URL token. Keep the complete URL private.

Account and workspace policy control availability. Follow [OpenAI's connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt) for current setup; it also describes Secure MCP Tunnel for private servers. CodexPro's built-in tunnel flags do not configure that separate OpenAI tunnel service.

Quick Cloudflare URLs change. For a stable public endpoint, use `--tunnel cloudflare-named` with `--hostname` and `--tunnel-name`, `ngrok`, or Tailscale Funnel. See [DOMAIN_SETUP.md](DOMAIN_SETUP.md). Prefer bearer headers whenever the client supports them. Refresh the client connection's tools after server upgrades.

## Tools and permissions

| Capability | Main tools / behavior | Details |
| --- | --- | --- |
| Project discovery | Named projects, workspace handles and permitted project creation | [Project catalog](#configure-projects) |
| Read and search | `tree`, `read`, `search`, structural `ast_grep`; bounded context and cursors | [Search](docs/SEARCH.md), [AST search](docs/AST_GREP.md) |
| Edit and review | Snapshot-backed edit tags, multi-hunk `edit`, `write`, native/unified `apply_patch`, `show_changes`, `commit_changes` | [Edits and batches](docs/HASH_EDIT_AND_BATCH.md) |
| Related operations | `batch` with parallel reads, serial mutations and retained definitions for resume | [Edits and batches](docs/HASH_EDIT_AND_BATCH.md) |
| Commands and output | Supervised Bash jobs, deadlines, bounded capture, incremental log reads and shell inspection | [Jobs](docs/JOBS.md) |
| Work across sessions | Optional manual/Ralph runs, claims, todos, versioned handoffs and recovery | [Work runs](docs/WORK_RUNS.md) |
| Recent changes | Project activity, mutation evidence and an authenticated `/activity` dashboard | [Action journal](ACTION_JOURNAL.md) |
| Optional integrations | ChatGPT attachment import, AI-Bridge handoffs/context bundles, opt-in local Codex history | [FAQ](FAQ.md) |

The default agent setup uses `--write workspace`, `--bash safe` and `--tool-mode standard`. `safe` Bash runs allowlisted verification commands, including repository scripts, so the repository must be trusted. `--bash full` grants arbitrary shell execution with the server account's privileges; workspace path filters are **not an OS sandbox for full Bash**. Use `--bash off` to disable commands and `--write off` to hide workspace mutation tools.

`--tool-mode minimal|standard|full` selects the visible tool set; it does not grant permissions. AI-Bridge tools need `--handoff-mode on` (or handoff mode). MCP replies contain text and structured data, with **no ChatGPT tool cards**. The separate browser activity dashboard remains available.

`read`, `search` and `ast_grep` can establish edit provenance, so shell `rg` is not a substitute for every search-to-edit workflow. Retained inline batches live under `.codexpro-batches/`; resume from a failed operation rather than replaying a successful prefix.

### Background jobs and large output

`bash` normally waits up to 120 seconds before returning a background job ID. `start_jobs` starts commands immediately; `jobs` inspects or collects them; `stop_jobs` requests termination. A foreground wait expiring does not reset the command's absolute deadline.

Job replies report capture/output sizes and truncation. Use `output="none"` for status, `head` or `tail` for bounded excerpts, and `incremental` with the returned cursor to page through a log. Legacy `full_output=true` is a **bounded head**, not a complete-log download.

With full Bash, returned `output_files` and `input_job_ids` let a follow-up command pin and inspect retained rendered logs with `grep`, `rg`, `sed` or scripts on the CodexPro machine. Logs are outside project source. Default limits are 25 minutes per job, 8 MiB captured output, and 24-hour finished-log retention subject to count/storage ceilings. See [docs/JOBS.md](docs/JOBS.md) for exact limits, pagination and retention rules.

## Durable runs

Add `--work on` to an HTTP server launch to enable the coordinator. With workspace writes enabled, it exposes `work_status`, `work_manage`, `work_claim` and `work_update`; read-only servers expose status only.

A **project** is a catalog entry. A **workspace** is a selected checkout. A **run** owns a retained Git worktree, plan and history. An **iteration** is one agent's bounded claim on a packet of work. Ordinary agents can use projects without joining a run.

The lifecycle is: discover or create a run → plan → claim a packet → checkpoint todos and handoff → finish the iteration → request separate whole-run acceptance checks. Fresh agents can discover existing runs without the predecessor's token, inspect claims/jobs and recent changes, and resume work. Server-owned expiry and job reconciliation handle agent death; uncertain effects stay visible for recovery.

One `work_update` can save multiple documents, todos and handoff atomically. A managed serial `batch` can also end with a checkpoint after its edits and verification succeed. Failed verification skips the checkpoint and keeps prior edits; claim, recovery and completion stay explicit. Activity groups run actions under their actual project/workspace, with a separate Server lane for server-wide calls.

Runs have no cumulative time, claim-duration or iteration-count limit. Repeated no-progress detection is advisory. Retained run history, receipts and documents do not consume an allowance for future work. Claims recover after inactivity; individual commands and MCP returns remain bounded. Large plans can grow through `todo_updates` and `acceptance_updates` pages. Only `mode="ralph"` gets the under-30-minute continuation hint, measured on CodexPro's monotonic clock across linked claims. Completion, blockers and stop requests take precedence. Manual mode gets no continuation hint. The coordinator does not itself launch fresh external agent sessions.

See [docs/WORK_RUNS.md](docs/WORK_RUNS.md) for documents and memory, acceptance evidence, restart recovery, limits, and the `codexpro work` / managed `loop-handoff` CLI adapters.

## State and server operation

`CODEXPRO_HOME` defaults to `~/.codexpro`. It contains saved profiles and default locations for jobs, audit data, legacy worktrees and work-run storage. Specific directory settings can override those defaults.

Use a separate home and port for a development server:

```bash
CODEXPRO_HOME="$HOME/.codexpro-dev" codexpro start \
  --projects-file "$HOME/.config/codexpro/dev-projects.json" \
  --tunnel none --host 127.0.0.1 --port 8788 --work on --headless
```

Create that development catalog first, pointing at test checkouts. Separate ports alone do not isolate files or state: use distinct project checkouts and storage directories too, and check explicit directory overrides. Do not point two independent coordinators at the same work or job store.

Enable `--audit metadata` for retained action history and recent-change briefings. The authenticated `/activity` page shows retained actions and current diffs; `/healthz` reports service health. These HTTP routes require the configured authentication. Audit metadata has retention limits; absent history is not proof that source never changed.

Use `codexpro settings --help`, `codexpro doctor` and `server_config` to inspect configuration. For a persistent Linux service, see [deploy/README.md](deploy/README.md). Quiesce active work before replacing a running binary; retain its work database and worktrees together for recovery.

## Update a source install

From a clean source checkout on this fork's `main`:

```bash
git pull --ff-only origin main
npm ci
npm run build
codexpro --version
git rev-parse --short HEAD
```

Restart the server after rebuilding. A linked install continues to point at the checkout; a packed snapshot needs a new `npm pack` and installation of that new local tarball. Saved runtime state is separate from the source checkout. Do not use `npm install -g codexpro@latest` to update this fork: that selects the upstream registry package.

## Development and documentation

Run relevant checks from the CodexPro source root:

```bash
npm test
npm run smoke
npm run stress
```

For direct source execution, `npm run dev:http -- --projects-file /absolute/path/to/projects.json` starts the TypeScript HTTP entry point. It reads environment/entry-point configuration and does not launch a tunnel or run the setup wizard. This command is not an automatic-reload mode.

[CONTRIBUTING.md](CONTRIBUTING.md) covers contribution and validation. This fork is distributed from source; the inherited npm publication scripts are not an installation step or an instruction to publish the upstream package.

Further reference: [FAQ](FAQ.md) · [Security boundaries](SECURITY.md) · [Configuration example](config.example.env) · [Jobs](docs/JOBS.md) · [Work runs](docs/WORK_RUNS.md) · [Action journal](ACTION_JOURNAL.md).

Based on [rebel0789/codexpro](https://github.com/rebel0789/codexpro), under the [MIT license](LICENSE). Upstream releases, documentation and version numbers have their own history; this fork's Git commit identifies the code you are running.
