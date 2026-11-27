# CodexPro

CodexPro serves a catalog of projects over HTTP MCP. Agents can inspect files and Git state, search repositories, edit with snapshot checks, and run or monitor shell jobs. The authenticated Activity dashboard shows per-agent tool metrics.

## Install from source

Build on Linux with Go 1.27.3 and a C compiler for SQLite. Install Git and ripgrep on the server. For structural search, install [ast-grep](https://ast-grep.github.io/) on PATH or set `CODEXPRO_AST_GREP_PATH` to its executable.

```sh
git clone https://github.com/lyeith/codexpro.git
cd codexpro/go
go build -o codexpro-go ./cmd/codexpro
```

Use [the systemd deployment instructions](deploy/README.md) for socket activation and upgrades.

## Configure projects

Copy [projects.example.json](projects.example.json) to a private configuration directory and set each root to an existing directory on the server. Relative roots resolve from the catalog directory. Project IDs stay stable when labels change. Configure `creationRoots` if agents may register new projects with `create_project`.

```sh
./codexpro-go \
  --projects-file "$HOME/.config/codexpro/projects.json" \
  --state-dir "$HOME/.codexpro-go/state" \
  --listen 127.0.0.1:7210 \
  --host-resources-helper=
```

On SSD, configure the [host job resource helper](docs/HOST_JOB_RESOURCES.md). The state directory holds the audit journal, retained jobs, usage ledger and profile settings.

An agent can call `list_projects` and use the returned `workspace_id` for reads. Before editing, call `open_workspace(project_id="web")` to load the project's [AGENTS instructions](docs/AGENTS_INSTRUCTIONS.md). `open_workspace(project_ids=["web", "api"])` opens related projects in one call. Fleet manages isolated agent workspaces outside CodexPro.

## Connect clients

Configure a Streamable HTTP MCP client with `http://127.0.0.1:7210/mcp` and an Authorization bearer header. The endpoint is stateless by default: each POST stands alone, so idle clients, restarts and parallel calls never meet an expired `Mcp-Session-Id` (a stale one is ignored). It serves MCP 2025-11-25 and earlier through `initialize`, and 2026-07-28 through `server/discover` without `initialize`. `GET` and `DELETE` return 405. `CODEXPRO_HTTP_SESSION_MODE=stateful` restores the older in-memory sessions as a rollback. For remote clients, use a trusted HTTPS endpoint and the same `/mcp` path. ChatGPT connector support depends on account and workspace policy. [OpenAI's connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt)

Set `--principals-file` to the private Plans Access `principals.json` file for named bearer credentials. During migration, `--legacy-bridge` allows the shared bearer stored in `--token-file` alongside named credentials. Disable the bridge once every client uses named credentials. For a standalone server with no policy file, CodexPro reads or provisions a private `http-token` in the state directory, or at the supplied `--token-file` path. [Authentication settings](go/internal/config/README.md#named-credentials)

## Tools and operations

- Browse or search with `tree`, `read`, `search`, `ast_grep`, and `inspect_workspace`. [Search](docs/SEARCH.md) · [Structural search](docs/AST_GREP.md)
- Edit with `write`, `edit`, `apply_patch`, and `batch`. Review and commit with `show_changes` and `commit_changes`. [Snapshot-based edits and batches](docs/HASH_EDIT_AND_BATCH.md)
- Run shell commands with `bash`, `start_jobs`, and `queue_jobs`. Check current jobs with `jobs` and stop them with `stop_jobs`. [Jobs](docs/JOBS.md)
- Read retained work-run records with `work_status` when the operator has configured the immutable work archive. [Work status](docs/WORK_RUNS.md)
- View per-agent tool metrics at `/activity`; use `/activity.json` for retained actions and current calls/jobs. `/healthz` reports service health. [Activity JSON](docs/ACTIVITY_JSON.md) · [SessionPilot usage](docs/PRO_USAGE.md) · [Completion signal](docs/status-collector.md)

Operator MCP credentials can write to configured workspaces and run shell commands with the service account's privileges. Use named project grants for read-only access. Keep project roots, policy files and service state scoped to the account that runs CodexPro. [Security](SECURITY.md)

## Development

CodexPro is Go only. In `go/`, run `go test ./...`, `go test -race ./...`, and `go vet ./...`; a repository test fails if TypeScript, JavaScript or Node package files (`.ts`, `.tsx`, `.js`, `.mjs`, `package.json`) reappear. Use a disposable project catalog and state directory for local integration testing. [Contributing](CONTRIBUTING.md)
