# Retained admin contracts

The package implements `server_config`, `codexpro_self_test`, `/healthz`, `/admin/profile`, `/setup`, `/`, and the favicon and activity reauthentication responses. The outer transport owns authentication, security headers, MCP sessions, and the activity routes themselves.

The reference is release `44bb76b`: `src/tools/admin.ts`, `src/http.ts`, `src/httpAuth.ts`, and `src/profileStore.ts`. The exact MCP descriptors live in the frozen contracts registry. Unit tests exercise the HTTP DTOs, validation and request limits, profile persistence, diagnostics, and template rendering. End-to-end equivalence still depends on the shared TypeScript/Go differential suite; package tests alone do not establish it.

## Profiles and the running process

Profile identity is the first 24 hexadecimal characters of SHA-256 of the canonical default project root. Profile and runtime records live beneath the Go instance's own state directory. Profile writes use an adjacent temporary file, mode `0600`, file sync, rename, and directory sync. Reads are bounded to 1,000,000 bytes.

`GET /admin/profile` preserves the distinction between a missing/non-object profile and an existing empty object. An existing `{}` gets a `profilePath` and `exists: true`. A missing runtime record is represented by `{}`, never `null`. Saved token fields are represented by `<saved>` in HTTP responses.

`POST /admin/profile` validates and persists the next-start settings. It does not change the running configuration, listener, token, tool registration, workspaces, or processes. Minimal, standard, and full tool inventory tiers remain valid. The approved retired tools are excluded from every tier by the contracts registry. Permissions remain full Bash and workspace writes independently of inventory tier.

The config package reads the private saved profile on a subsequent explicit process start. CLI flags and process/environment-file settings have precedence. Credentials in a profile cannot replace the Go instance's token file.

## Deliberate differences required by this rewrite

- Profiles and runtime records use the private Go state directory instead of the live TypeScript state directory. The path derivation and root identity format remain compatible inside that directory.
- Retired handoff, Codex-session, autonomous-work, and approval operating modes cannot be enabled. Compatibility status fields remain present with inert values; controls that would enable the retired modes are absent from setup.
- Full-permission managed worktrees are supported. The TypeScript profile validator's full-Bash/MCP-worktree prohibition does not apply to the approved Go design.
- Reading a stale runtime record returns `{}` without unlinking it. Dashboard reads do not perform recovery writes.
- Setup retains the bearer credential in browser session storage after removing it from the URL and adds it to the profile POST. This avoids losing authentication after URL cleanup.
- Source `/` and `/setup` both return the setup page. The implementation follows the source response rather than introducing a root redirect.

Tunnel names and connection settings remain profile metadata. This server package does not launch or install Cloudflare, ngrok, or Tailscale, and stage 4 does not exercise the production launcher. Preserve that limitation in the cutover runbook instead of treating metadata persistence as a verified tunnel launch.

## Integration hooks

Construct the service after workspace and registry initialization, passing the singleton files and Git services. Set `RegisteredNames` to the actual dispatcher inventory before serving. This lets diagnostics compare active handlers with the expected registry and lets `server_config` report the actual exposed tools.

`HTTPOptions` supplies the shared same-origin middleware, redactors, and test clock. Register the routes once on the server mux. The outer authenticated router delegates `Favicon`, `WantsDashboardReauth`, and `SendDashboardReauth` as appropriate; the admin package does not bypass bearer verification.
