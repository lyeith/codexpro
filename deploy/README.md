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


## Upgrading the job runner and output store

Build and package the candidate release first. Run the full tests, HTTP/stdio/
worktree/drain smoke checks, and `node scripts/job-restart-smoke.mjs` with isolated
job storage. The restart rehearsal requires Linux user systemd and creates only
disposable service/scope units. Do not run another manager on production storage.

Before switching the immutable release symlink, record its old target, back up
job metadata, and inventory current jobs. Legacy jobs remain readable but cannot
acquire the new runner's independent watchdog while in flight. Prefer a quiet
cutover; allow existing work to complete without changing its deadline. Preserve
the existing catalog, authentication and operator limits.

After switching the symlink, restart only `codexpro.service`; keep its socket and
tunnel running. Reinitialize the client and verify `tools/list`, a small command,
incremental output and native patching in a disposable workspace. Session edit
tags must be acquired again after restart.

For rollback, first inventory runners created by the new release. Keep their
release files available until completion. Repoint the symlink and restart the
service; never restore an old jobs.json over newer jobs. Old code ignores the
additive runner fields and does not expose incremental/rendered output. For a
clean rollback, finish new runners before switching; reconcile retained output
and completion reasons explicitly if an emergency rollback cannot wait.
