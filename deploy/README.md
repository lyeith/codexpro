# Deploying CodexPro with systemd

Build a release from the `go/` module and install `codexpro-go` under a versioned directory in `~/apps/codexpro-go/releases/`. Point `~/apps/codexpro-go/current` at the selected release. The [service unit](systemd/codexpro.service) starts that binary directly. The [socket unit](systemd/codexpro.socket) holds port 7200 through service restarts.

```sh
cd go
go build -ldflags "-X github.com/lyeith/codexpro/go/internal/admin.buildRevision=$(git rev-parse HEAD)" \
  -o /absolute/path/to/release/codexpro-go ./cmd/codexpro
```

The revision flag makes `/healthz` and `server_config` report the source commit; Go does not stamp it in a linked worktree. Name the release directory after the owner, date and short commit (for example `r-concierge-20261128-<sha>`) and never overwrite one.

## First installation

Set up the private `~/.config/codexpro/env` file using [the example](../config.example.env). The service template reads the project catalog from that environment and uses `~/.codexpro-go/state` and `~/.config/codexpro/http-token`. Set the socket's `ListenStream` to the intended interface and port. The example binds all IPv4 interfaces on port 7200.

Install the adapted units under `~/.config/systemd/user`, then activate the socket and service.

```sh
systemctl --user daemon-reload
systemctl --user enable --now codexpro.socket
systemctl --user restart codexpro.service
```

When using SSD's [host resource adapter](../docs/HOST_JOB_RESOURCES.md), set its absolute path and apply the settings in [the SSD example](ssd/codexpro-host-resources.conf) to the service. Keep the effective CPU and I/O weights and the service PATH used by installed tools.

## Upgrade from a unit with release overrides

1. Build and test the release. Record the previous binary and the effective service configuration. Transfer live startup options from release overrides into the service environment, including the named policy/legacy bridge, pending-mail socket, Plans Access command, ast-grep executable and work-history path when configured. Remove the superseded `--work off` flag from the new unit. Preserve the same state and catalog paths.
2. Select a quiet moment with no running jobs and no terminal jobs with unsettled runners or resource claims. Preserve the prior binary and its options for rollback. Check active SessionPilot work before switching. Existing queued jobs can remain queued.
3. Point `current` at the new release, replace the service unit, remove the old release/ExecStart overrides, and run `systemctl --user daemon-reload` followed by `systemctl --user restart codexpro.service`. Keep the socket and tunnel running.
4. Verify authenticated `/healthz` (`revision` and `httpSessionMode`), then run `go run ./cmd/mcp-smoke` from `go/` against the endpoint and an SP-style read with its `agent` label. Check named credentials and the shared bearer while the bridge is enabled. Verify retained jobs and pending-mail notices with the applicable credentials.

SIGTERM drains in-flight calls within `CODEXPRO_DRAIN_TIMEOUT_MS` and accepted background jobs run in their own scopes when configured. While draining, new MCP requests get `503 Retry-After: 1` and the socket hands the retry to the new process. MCP is stateless by default, so clients carry on across the restart without re-initializing; a stale `Mcp-Session-Id` is ignored. Re-read a file before using an edit tag from the previous process.

`CODEXPRO_HTTP_SESSION_MODE` selects `stateless` (default) or `stateful`. Stateful mode restores in-memory sessions (`CODEXPRO_MAX_HTTP_SESSIONS`, `CODEXPRO_HTTP_SESSION_TTL_MS`); after a restart its clients get `404` once and re-initialize. To fall back without changing the release, set `CODEXPRO_HTTP_SESSION_MODE=stateful` in the EnvironmentFile and restart the service. The mode is read at startup only.

For rollback, select another quiet moment, repoint `current` to the previous release and restore its effective startup configuration, then reload the unit and restart the service. Repeat the health and MCP checks. Keep release files available to their runners through completion and preserve current state. See [runner rollback](../go/internal/jobs/README.md).

## Retire the shared bearer

Configure the private `principals.json` file and enable `CODEXPRO_LEGACY_BRIDGE=true` during cutover. Use a named credential in each ChatGPT connector, SessionPilot workflow and operator client. After testing all clients, remove the bridge setting, restart at a quiet moment and verify the named clients. The configured policy then rejects the shared bearer. [Named authentication](../go/internal/config/README.md#named-credentials)

## Reload operational limits

Edit the private EnvironmentFile and run `systemctl --user reload codexpro.service`. SIGHUP reloads the operational limits from `CODEXPRO_ENV_FILE` without closing MCP sessions. Inspect the journal or `server_config.reloadableLimits`. [Operational limit contract](../docs/JOBS.md#changing-operational-limits-without-a-restart)
