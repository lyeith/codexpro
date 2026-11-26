# Retained admin contracts

The package implements `server_config`, `codexpro_self_test`, `/healthz`, `/admin/profile`, `/setup`, `/`, and the favicon and activity reauthentication responses. The outer transport owns authentication, security headers, MCP sessions, and the activity routes themselves.

The Go contracts package defines the MCP tool schemas. Tests cover HTTP DTOs, validation, request limits, profile persistence, diagnostics, and template rendering.

## Profiles and the running process

Profile identity is the first 24 hexadecimal characters of SHA-256 of the canonical default project root. Profile and runtime records live beneath the Go instance's own state directory. Profile writes use an adjacent temporary file, mode `0600`, file sync, rename, and directory sync. Reads are bounded to 1,000,000 bytes.

`GET /admin/profile` preserves the distinction between a missing/non-object profile and an existing empty object. An existing `{}` gets a `profilePath` and `exists: true`. A missing runtime record is represented by `{}`, never `null`. Saved token fields are represented by `<saved>` in HTTP responses.

`POST /admin/profile` validates and persists the next-start settings. It does not change the running configuration, listener, token, tool registration, workspaces, or processes. Minimal, standard, and full tool inventory tiers remain valid. The approved retired tools are excluded from every tier by the contracts registry. Permissions remain full Bash and workspace writes independently of inventory tier.

The config package reads the private saved profile on a subsequent explicit process start. CLI flags and process/environment-file settings have precedence. Credentials in a profile cannot replace the Go instance's token file.

## Setup and runtime

- Setup edits profiles for the direct workspace catalog. Fleet handles isolated worker workspaces.
- Reading a stale runtime record returns an empty object.
- Setup retains the bearer in browser session storage after removing it from the URL, then sends it in the profile request.
- Both `/` and `/setup` serve the setup page.

Tunnel names and connection settings are profile metadata. Configure and operate any proxy or tunnel independently.

## Integration hooks

Construct the service after workspace and registry initialization, passing the singleton files and Git services. Set `RegisteredNames` to the actual dispatcher inventory before serving. This lets diagnostics compare active handlers with the expected registry and lets `server_config` report the actual exposed tools.

`HTTPOptions` supplies the shared same-origin middleware, redactors, and test clock. Register the routes once on the server mux. The outer authenticated router delegates `Favicon`, `WantsDashboardReauth`, and `SendDashboardReauth` as appropriate; the admin package does not bypass bearer verification.

## People administration

`/people` serves a fact-free sign-in shell, including for unauthenticated GET and HEAD requests. The outer transport admits `/people/data` GET and `/people/actions` POST only for a current named person owner, that person's active concierge and a credential with exactly the `codexpro` audience. People accepts an explicit Authorization header and rejects credential query parameters, including encoded and duplicate forms. Other routes keep their existing authentication contracts.

The six actions are add person, change personal role, disable person, register personal concierge/crew, retire personal agent and revoke an eligible existing generated personal credential. Roster projections contain only the declared person, agent and credential metadata; no bearer, digest, raw policy or private path is returned. Registration can select only a parent's existing project. Credential issuance, rotation, grants and system/fleet administration remain with their existing owners.

The service invokes the startup-selected `plans-access` executable directly with the fixed `people --file` protocol. A request contains the transport-derived actor and one finite action, never an executable, path, arbitrary argument or role assertion. The command rechecks current authority and expected target metadata under the policy owner's existing writer lock. Its safe projections do not replace or discard hidden policy fields. Stdin is bounded to 64 KiB, stdout to 4 MiB, and the local command has a ten-second timeout. Child stderr is discarded, and malformed output becomes a fixed error without subprocess details.

A pre-replacement refusal, a confirmed replacement and an uncertain outcome are separate receipts. A process or response loss after start makes an apply uncertain; the browser never automatically retries that POST. Reading the current roster cannot establish which request caused it. Self and last-owner changes remain permitted, with explicit consequences and honest loss of access after a confirmed change. The page retains drafts on refusal and disables writes after a failed read until current records are loaded.

Activation requires the paired Agentmail protocol binary, a trusted startup executable path and the existing private policy path. Source qualification uses disposable policy fixtures; it does not install binaries, change live principals or activate services.

## Execution maintenance control

The exact canonical `/admin/execution-hold` route uses the transport's current owner decision: a named person owner, active concierge and exact `codexpro` audience, or the existing legacy operator credential only when no named policy is configured. System/fleet, restricted/read-only, owner-crew and bridge credentials do not gain this control. Escaped and normalized aliases are refused before mux routing. The existing same-origin middleware applies; unrelated admin routes retain their contracts.

`GET` is a pure local status read. `POST` accepts an application/json object no larger than 4096 bytes, with exactly one of these shapes:

| Action | Request |
|---|---|
| Begin fresh-execution admission hold | `{"action":"begin","request_key":"operator-chosen-idempotency-key"}` |
| Request dispatch hold advancement | `{"action":"dispatch","hold_id":"original-32-hex-hold-id"}` |
| Inspect existing owned execution | `{"action":"observe","hold_id":"original-32-hex-hold-id"}` |
| Release local ingress after its original continuations and queue settle | `{"action":"resume","hold_id":"original-32-hex-hold-id"}` |

Duplicate/unknown keys, trailing JSON, wrong types and mismatched action fields are rejected. Request keys use the jobs owner's nonblank, valid-UTF-8, no-NUL, at-most-160-character contract. Begin is idempotent for the existing key; another key or foreign hold ID cannot replace or resume it. Observation has a ten-second request-context bound and checks the original hold both before and after the live read. A concurrent replacement is a conflict, never an observation of the requested hold.

All projections use the shared public redaction path. Backend errors become fixed safe messages, retaining the returned status so the operator can inspect uncertainty. Responses explicitly describe this CodexPro instance's owned execution and set `whole_host_barrier` and `root_barrier` false. Dispatch closure, command completion and owner quiescence are separate facts. Neither a successful HTTP request nor an empty status projection establishes a whole-host or ROOT maintenance barrier.

`resume` uses the local jobs owner's published settlement and original hold token. It does not perform or certify a fresh SSD owner observation. Only the separate bounded observation reports its `complete` result, and even that result covers tracked CodexPro execution rather than ROOT's independent producer barrier. Saved-locator failures, mismatched identities and unknown ownership remain visible; the operator must use the appropriate observation and coordinated barrier before changing an owner unit.

For jobs accepted with an independent diagnostic launcher, the hold also reports `launcher_state` and `launcher_quiescent`. A terminal payload and completed diagnostic receipt do not settle the hold while that original launcher is alive or its identity is uncertain; payload/scope quiescence remains a separate observation. Original version-zero jobs retain their accepted behavior. All hold actions only read existing launcher evidence: they never install a marker, recreate evidence, or synthesize an identity. An attempted launch with no valid marker or saved process identity remains an explicit blocker, including after restart. A recorded `cmd.Start` failure can therefore leave maintenance blocked when neither identity nor marker was saved; resolving that availability limitation requires a separate durable proof design.

The concrete People command optionally shares this jobs admission owner. Its mutation protocol (`apply`) acquires admission after existing validation and before starting the unchanged executable/argv; a held apply is refused without starting the command. Read-mode roster inspection keeps its existing behavior. A successfully started apply records the original PID/start-tick/boot as an **unverified** legacy owner, then retains admission until the original Wait and protocol-response handling finish. The existing direct-child contract has no verified descendant containment; a completed child therefore leaves an unknown-owner blocker. There is no automatic removal or forced completion. Evidence-recording failure is logged with a fixed internal diagnostic and does not skip Wait or replace the command's original confirmed/refused/uncertain outcome.

This source adds no MCP tool, principal, role, live hold or service activation. Qualification uses private HTTP, jobs-store and owner-protocol fixtures.

## Development DOM checks

The People and dashboard reauthentication behavior tests use the private development-only package in `testdata/dom`. It pins happy-dom 21.0.4 and its nine-package dependency closure from the historical root lockfile at `451c5d0cdad7be4091f9f81c4d71c517360e6f9a`; Node 20 or newer is required. The tests import that local module directly. These dependencies are not part of the Go service or its deployed runtime.

From the repository root, provision the locked dependency with lifecycle scripts disabled:

```sh
npm ci --include=dev --prefix go/internal/admin/testdata/dom --ignore-scripts --no-audit --no-fund
```

Then, from the `go` directory, select the two existing behavior tests:

```sh
go test -json ./internal/admin -run '^(TestDashboardReauthScriptRecoversWithExistingNavigation|TestPeopleScriptReadsActionsAndUncertainOutcomes)$' -count=1
```

Use the maintained fleet-remote route and a bounded owned worker scope for installation and qualification. Both named tests must run and pass; an unavailable Node or happy-dom skip is not DOM qualification. The ignored `testdata/dom/node_modules` directory is confined to the worker checkout, and `npm ci` must leave the tracked package and lock files unchanged.

These tests evaluate the actual generated page scripts with synthetic DOM, credentials and fetch responses. People includes an isolated `execution_held` / `refused` HTTP 503 case: it preserves the draft, consequence confirmation, roster and fresh controls, shows the known refusal, and makes no retry or access GET. The separate uncertain-response scenario retains its unknown-outcome contract. Dashboard recovery continues to cover its existing navigation and credential behavior. This coverage does not exercise a live browser, server, policy or maintenance hold.
