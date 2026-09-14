# CodexPro FAQ · lyeith fork

Start with the [fork README](README.md) for source installation, project catalogs and client connections. These answers describe this fork's `main`, not the upstream npm package.

## Which ChatGPT account should I use?

Local agents and MCP clients do not need a ChatGPT account. For ChatGPT connections, use an account and workspace that allow custom MCP connections and a model that can call them. Eligibility can change; consult [OpenAI's developer mode guide](https://developers.openai.com/api/docs/guides/developer-mode).

CodexPro provides tools, not models or account access. If the selected chat cannot call MCP tools, a context bundle is available as a manual fallback.

## How is CodexPro different from generic workspace bridges?

This fork combines a persistent project catalog, selected workspaces, snapshot-backed edits, supervised Bash jobs, inspectable output, and optional durable work runs. Local agents, LAN MCP clients and ChatGPT can use the same server.

Repository guidance stays in `AGENTS.md`; AI-Bridge files support context/handoff workflows; the optional coordinator retains versioned plans, todos, claims and recovery records across agent sessions. These serve different purposes. See [work runs and memory](docs/WORK_RUNS.md).

Bash, workspace writes, tool visibility, local Codex history and handoff execution have separate controls. Full Bash uses the server account's privileges and is not confined by an OS sandbox.

## What does Repository Analysis understand?

Repository Analysis builds a local repository map from bounded, inspectable evidence:

- project and package manifests
- source/test/config/documentation paths
- common declarations, imports, includes, and internal module relationships
- Git changes and existing project verification scripts

It supports TypeScript/JavaScript, Python, Go, Rust, Swift, Java, C#, C, and C++ declaration patterns. Unsupported languages still participate in safe inventory and lexical search.

Relationships are labeled `exact`, `strong`, or `inferred`. The repository map does not replace a compiler or language server. CodexPro does not require a language server, daemon, embedding service, or vector database.

Analysis is process-local and cached by a bounded workspace fingerprint. Direct CodexPro writes, edits, and patches invalidate that cache. If limits are reached, results say `partial` and retain normal tree/search/read/review fallback behavior.

Set `CODEXPRO_ANALYSIS=0` to disable this layer while keeping the standard file, search, Git, and review tools available.

Terminal users can inspect the same facts without ChatGPT:

```bash
codexpro inspect --json
codexpro review --json
```

## What is the `codexpro` supertool?

Use the current server's action list; available tools depend on its permissions and enabled features.

`codexpro` is a stable wrapper tool for advanced setups. It accepts:

```json
{ "action": "search", "args": { "query": "needle", "path": "src" } }
```

Call it with `action=list_actions` to see what the current server mode actually allows. It cannot call tools that are hidden by `--tool-mode`, `--no-bash`, or non-workspace write mode.

Use explicit tools such as `read`, `search`, `edit`, `bash`, and `show_changes` for normal work. Use the supertool when ChatGPT connector caching, custom workflows, or stable wrapper-style integrations matter more than separate visible tool descriptors.

## What is the recommended install path?

Install [this fork from source](README.md#install-from-source): clone `https://github.com/lyeith/codexpro.git`, run `npm ci`, `npm run build`, then `npm link`. Or invoke `node /absolute/path/to/codexpro/scripts/codexpro.mjs` without a global install.

Configure a [persistent project catalog](README.md#configure-projects), then run:

```bash
codexpro setup --projects-file "$HOME/.config/codexpro/projects.json"
```

The source checkout is the server software; catalog roots are the projects it serves. You do not need to run setup inside each project. Registry installs of `codexpro` select upstream, not this fork.

## How do I update CodexPro?

There is no `codexpro update` command. From a clean source checkout of this fork's `main`:

```bash
git pull --ff-only origin main
npm ci
npm run build
codexpro --version
git rev-parse --short HEAD
```

Restart the server with the same catalog and runtime configuration. Linked commands use the rebuilt checkout; a packed installation needs a new local tarball. Keep the Git commit as the build identifier because the package version alone does not distinguish forks. See [update instructions](README.md#update-a-source-install).

## How is CodexPro different from ChatGPT's built-in web Agent?

CodexPro is the MCP server running beside your code. It exposes project discovery, files, commands and review tools to an authorized client. When ChatGPT is that client, CodexPro supplies access to the configured machine and projects; a ChatGPT connection is optional.

The server does not attach to an existing browser or Codex conversation. Full Bash can execute arbitrary commands as the server account when enabled.

## How do I import a ChatGPT attachment into my repo?

In workspace write mode, CodexPro advertises `import_file`. ChatGPT must pass an Apps SDK file object:

```json
{
  "download_url": "https://...",
  "file_id": "file_...",
  "mime_type": "image/png",
  "file_name": "screenshot.png"
}
```

CodexPro marks that argument with `_meta["openai/fileParams"]`. It downloads only temporary HTTPS URLs from approved ChatGPT/OpenAI file hosts, enforces `CODEXPRO_MAX_IMPORT_BYTES`, rejects private/loopback redirect targets, and writes into the allowed workspace only. Overwrite defaults to false. Arbitrary user- or model-supplied download URLs are rejected.

Example destination:

```text
docs/evidence/screenshot.png
```

If the client does not provide `download_url` and `file_id`, the tool returns an unsupported-reference error and creates no files.

## What do I enable in ChatGPT?

Follow the [ChatGPT connection steps](README.md#chatgpt-connections). They cover developer mode, the Server URL and the token-in-URL compatibility flow. Choose **No Authentication / None** only for that flow: the complete URL already carries the CodexPro credential. Prefer bearer headers for clients that support them.

Client interfaces and eligibility change; [OpenAI's current connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt) is the reference. Refresh the connection's tool metadata after server upgrades.

## Should CSP stay enabled?

Keep the client's normal security settings. This fork does not render ChatGPT tool cards or MCP widgets, so connecting it does not require disabling CSP. The separate authenticated `/activity` browser page remains available.

## Does CodexPro bypass rate limits?

No.

CodexPro does not bypass, avoid, increase, pool, resell, or modify ChatGPT, Codex, OpenAI, or third-party model limits. Every request still runs through the user's own ChatGPT session and whatever limits that account has.

The useful part is that Codex and ChatGPT are different product surfaces. If one workflow is unavailable and another product surface you already have access to is still available, CodexPro lets you work against the same local repo without changing either product's limits.

## Does CodexPro choose or provide a model?

No. The external agent or ChatGPT session chooses the model and must support MCP tool calls. CodexPro does not provide or proxy models.

For a client that cannot call tools, generate a repository context bundle:

```bash
codexpro pro-bundle --root /path/to/repo --copy
```

This produces a manual handoff; it does not enable tool calls in that client.

## What can ChatGPT see through CodexPro?

ChatGPT can see explicit workspace context exposed by tools:

- `AGENTS.md`
- `.ai-bridge` plans and status files
- git status
- git diff
- selected source files
- file tree and search results

Normal file tools enforce configured workspace paths. Opt-in Codex history tools separately expose local history. Full Bash can access whatever the server account can access; workspace path guards do not constrain arbitrary shell commands.

## What can ChatGPT edit?

In normal coding mode, ChatGPT can write and exact-edit files inside the configured workspace.

Safety defaults block common sensitive paths:

- `.env`
- private keys
- `.git`
- `node_modules`
- generated build/cache folders
- symlink escapes
- paths outside the workspace

Use handoff mode if you want ChatGPT to write a plan only and let Codex execute locally. In handoff mode, generic `write` and `edit` tools are not advertised to ChatGPT.

Use `CODEXPRO_WRITE_MODE=off` to hide workspace mutation tools. Handoff tools are a separate opt-in through `--handoff-mode on`; use `--mode handoff` for the planning profile.

## Can CodexPro bind bash to a specific session id?

CodexPro cannot attach to, read, or execute inside a specific Codex app conversation or terminal session.

The MCP `bash` tool runs from the CodexPro server process you started for the configured workspace. MCP session ids are HTTP transport state between ChatGPT and CodexPro; they are not Codex conversation ids.

What CodexPro can do is require a matching local bash session label before it runs shell commands:

```bash
codexpro start --bash-session main --require-bash-session
```

Then `bash` calls must include `session_id: "main"`. This helps avoid accidental shell execution in the wrong CodexPro terminal, but it is not remote control of an existing Codex app chat.

CodexPro can list local Codex session ids and titles when you explicitly opt in:

```bash
codexpro start --codex-sessions metadata
```

This reads local Codex JSONL history under `~/.codex/sessions` and `~/.codex/archived_sessions` and returns metadata plus `codex resume <session-id>` commands. Use `--codex-sessions read` only if you also want bounded transcript reads. It does not attach to a live Codex app conversation.

If you do not want ChatGPT to trigger shell commands while you work in Codex, start CodexPro with bash disabled:

```bash
codexpro start --no-bash
```

This removes the `bash` MCP tool from the advertised tool list. ChatGPT can still use non-bash CodexPro tools such as workspace open, read, search, and show_changes. Direct `write`/`edit` are advertised only in workspace write mode.

If you only want ChatGPT to plan and leave execution to Codex or another local agent:

```bash
codexpro start --mode handoff --no-bash
```

## Which tunnel should I choose?

Local and LAN MCP clients can use `--tunnel none`; set `--host` to the appropriate interface and keep authentication enabled for LAN bindings. See [local and LAN setup](README.md#local-and-lan-clients).

For a public HTTPS endpoint, the CLI supports Cloudflare quick tunnels, Cloudflare named tunnels, ngrok and Tailscale Funnel. Quick Cloudflare URLs change; stable options require provider setup. Tailscale Funnel is public exposure, not a tailnet-only endpoint. [DOMAIN_SETUP.md](DOMAIN_SETUP.md) documents the provider commands.

For ChatGPT's public Server URL flow and its separate private-tunnel option, see the [connection guide](README.md#chatgpt-connections).

## Why does ChatGPT show “Something went wrong” when I create a connector?

Usually ChatGPT could not reach the public MCP URL. A generated `trycloudflare.com` URL is not proof that `cloudflared` stayed connected.

Run the connection test:

```bash
codexpro connection-test --projects-file "$HOME/.config/codexpro/projects.json"
```

This keeps `read`, `tree`, `search`, and `load_skill`, but disables file writes
and bash. Tool cards are disabled in every mode. In ChatGPT, create the development plugin under
`Settings -> Plugins`, paste the complete Server URL, and choose
`No Authentication`.

The terminal output separates the failure boundary:

- No `POST /mcp received`: the request did not reach CodexPro. Check the ChatGPT
  Plugins page and the tunnel.
- `POST /mcp -> 401`: paste the complete URL, including `codexpro_token`.
- `POST /mcp -> 2xx`: ChatGPT reached CodexPro and the MCP endpoint responded.

The URL token is a personal-use compatibility fallback for connector forms
without custom headers. Shared or multi-user production deployments require
OAuth or `Authorization: Bearer <token>`. CodexPro
requires at least 24 token bytes, removes token parameters from the local
browser address after onboarding, and rate-limits failed authentication
attempts.

Keep CodexPro running while testing. A Cloudflare quick-tunnel URL changes on
every restart. If Cloudflare returns `530` / `Error 1033`, check DNS or
proxy-client DNS handling on the machine running `cloudflared`.

ChatGPT now manages custom MCP connections under Plugins. The browser error
`Failed to execute 'removeChild' on 'Node'` occurs in the ChatGPT page, before
CodexPro can handle an MCP request. Remove or recreate the stale plugin entry
from the Plugins page, then retry with the current URL. CodexPro cannot repair
that browser-side entry.

Official references:

- OpenAI: connect an MCP server to ChatGPT: https://developers.openai.com/plugins/deploy/connect-chatgpt
- OpenAI: MCP server authentication: https://developers.openai.com/apps-sdk/build/auth
- ngrok dev domains: https://ngrok.com/docs/universal-gateway/domains
- Cloudflare Tunnel routing: https://developers.cloudflare.com/tunnel/routing/
- Cloudflare Tunnel DNS records: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/routing-to-tunnel/dns/

## Can I use the same ChatGPT plugin URL every day?

Yes, with a stable hostname and token. Save the provider settings with `codexpro setup --projects-file /absolute/path/to/projects.json`, then launch with that same catalog. Quick tunnel URLs are temporary. See [DOMAIN_SETUP.md](DOMAIN_SETUP.md) for stable tunnel commands.

## What if I run CodexPro in two repos at once?

Use one [persistent project catalog](README.md#configure-projects) and one server. Agents call `list_projects`, then `open_workspace(project_id="...")` or `open_workspace(project_ids=[...])` and reuse the returned handles. Select the default project again with `open_workspace(project_id="...")`; `open_current_workspace` is only exposed for single-project servers.

Repeated `--project` flags remain available for lightweight extra roots, but a catalog supplies stable IDs and persistent creation. Workspace selection belongs to an MCP session; a client conversation is not guaranteed to map one-to-one to that session, so pass explicit workspace handles.

If you need independent servers, use separate ports, credentials, runtime storage and project checkouts. Separate profiles or ports alone do not isolate writes to the same files. For shared durable runs, connect clients to one coordinator.

## How do multiple ChatGPT sessions avoid overwriting each other?

Workspace selection is session-local. For a whole-file `write`, read the shared file first and pass its returned SHA-256 as `expected_sha256`. For `edit`, use the four-character `edit_tag` returned by `read`; CodexPro resolves it to the exact full snapshot retained for the authenticated connector principal. That bounded cache is shared across HTTP server instances in one process so transport rotation does not break an immediate read/edit sequence, while different principals and process restarts remain isolated. CodexPro still rejects stale content, collisions, and line ranges that were not displayed. New files use atomic replacement; existing files are updated in place to retain inode-bound metadata and hard links.

This protects against stale file content. It does not turn CodexPro into a collaborative merge server, so separate worktrees remain the stronger choice for large overlapping changes.

## How do I fix and resume a failed batch?

Use direct tools for one or two ordinary reads and for a mutation followed only by `read` or `show_changes`. Use one consolidated batch for three or more independent parallel reads, a mutation followed by actual Bash verification, or a deliberately resumable workflow. An inline batch containing Bash verification persists by default; other inline batches are one-shot unless `persist=true`.

When a batch is persisted, the result returns its `batch_path` plus the failed operation ID/index. Read that ordinary JSON file, amend it with the normal tagged `edit` tool, then resume inclusively from the corrected operation:

```text
batch(path=".codexpro-batches/7A3C.json", from="tests")
```

`from_index` is the zero-based alternative. If the batch definition was correct but an earlier source edit produced bad code, repair the source separately and resume from the failed test/check; the successful prefix is not replayed. CodexPro retains the 20 most recently created, modified, or run generated definitions per workspace and places their directory in Git's local `info/exclude`. Running a stored file refreshes retention recency without changing its contents or edit tag. Read-only profiles still run inline batches one-shot. For one-file changes prefer tagged `edit`; `apply_patch` accepts native `*** Begin Patch` envelopes as well as Git unified diffs. See [edit and batch formats](docs/HASH_EDIT_AND_BATCH.md).

For service managers and background launches, use `codexpro start --headless`. It avoids prompts, clipboard and browser actions, reports readiness with `CODEXPRO_READY`, and exits nonzero if its HTTP runtime stops unexpectedly.

## Which website and releases belong to this fork?

Use [lyeith/codexpro](https://github.com/lyeith/codexpro) and its README. `rebel0789.github.io/codexpro` and the npm `codexpro` package belong to upstream. The static marketing pages and launch checklist inherited in this checkout are historical references, not this fork's installation or publication procedure.

## Is CodexPro production safe?

CodexPro runs with access to your development machine. Normal file tools enforce workspace paths; full Bash is arbitrary execution as the server account, and safe Bash can run repository scripts. Use trusted projects and clients and authenticate every non-loopback endpoint.

Read [SECURITY.md](SECURITY.md) for the boundaries, and [WORK_RUNS.md](docs/WORK_RUNS.md) before operating durable coordinators. Separate development stores from production stores.

## Where are saved settings stored?

CodexPro stores local state under `~/.codexpro` by default. On Windows that is usually `C:\Users\<you>\.codexpro`.

Workspace profiles are JSON files saved under:

```text
~/.codexpro/profiles/
```

Current runtime connection files are saved under:

```text
~/.codexpro/runtime/
```

Set `CODEXPRO_HOME` to move this directory.

Use:

```bash
codexpro settings
codexpro settings list
codexpro settings delete --yes
```

Saved tokens are redacted when profiles are displayed.
