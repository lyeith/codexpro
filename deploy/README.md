# Deploying CodexPro as a systemd user service

`systemd/codexpro.socket` + `systemd/codexpro.service` run the HTTP connector
with socket activation so restarts are clean:

1. systemd owns port 7200. While the service restarts, new connections queue in
   the kernel backlog instead of being refused.
2. On `systemctl --user restart codexpro`, the old process receives SIGTERM,
   stops accepting, finishes in-flight tool calls (up to
   `CODEXPRO_DRAIN_TIMEOUT_MS`, default 30 s), then exits. New `initialize`
   requests arriving on already-open connections get `503 Retry-After: 1`.
3. The new process starts and serves the queued connections.
4. MCP sessions are process-local: a client's next request on an old session
   gets `404 Session not found` and, per the MCP spec, re-initialises. Tagged
   edit snapshots are also per process, so the agent re-reads before editing
   (the `edit_tag_unknown` recovery hint says so).

Install:

```sh
cp deploy/systemd/codexpro.socket deploy/systemd/codexpro.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now codexpro.socket
systemctl --user restart codexpro.service
```

Without systemd the server still drains on SIGTERM/SIGINT; it just cannot keep
the port open in between.
