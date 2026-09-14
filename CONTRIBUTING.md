# Contributing to the lyeith fork

Changes here target [lyeith/codexpro](https://github.com/lyeith/codexpro). Open pull requests against this fork's `main`. Upstream attribution and release history remain in the repository; an upstream npm version is not a release identifier for this fork.

## Local setup

Follow [source installation](README.md#install-from-source), using the same Node installation to build and run the native dependencies. A global link is optional for development:

```bash
npm ci
npm run build
node scripts/codexpro.mjs --help
```

Create a test project catalog using [projects.example.json](projects.example.json), with existing disposable checkouts. Start an isolated server from the CodexPro source root:

```bash
CODEXPRO_HOME="$HOME/.codexpro-dev" node scripts/codexpro.mjs start \
  --projects-file /absolute/path/to/dev-projects.json \
  --tunnel none --host 127.0.0.1 --port 8788 --auth-mode static-token --headless
```

Keep development project roots, ports and state separate from running services. Explicit job, audit, work and worktree directory settings can override `CODEXPRO_HOME`; inspect inherited environment settings. Never run two independent coordinators against the same work store. Add `--work on` when testing the coordinator.

For source execution, `npm run dev:http -- --projects-file /absolute/path/to/dev-projects.json` runs the TypeScript HTTP entry point. It does not run the setup wizard, launch a tunnel or watch for changes.

## Validation

Run checks appropriate to the changed behavior:

```bash
npm run build
npm test
npm run smoke
npm run stress
```

`npm test` and `npm run stress` also build. The smoke suite includes HTTP, stdio, project catalogs, workspaces and job output. Coordinator behavior has focused tests under `test/`; see [implementation notes](docs/WORK_IMPLEMENTATION.md) for restart and packaging validation. Linux systemd lifecycle rehearsals need an isolated host/user service environment.

For documentation changes, verify commands against the CLI and check links. Install examples should use this fork's source or a tarball built from it. Test global-link or package-install instructions with a disposable npm prefix so an existing installation is not replaced.

## Pull requests

- Explain the concrete problem, resulting behavior and relevant validation.
- Keep private paths, source data, tunnel URLs and credentials out of commits.
- Update the English and Chinese READMEs and relevant reference docs when behavior changes.
- Review auth, file access, shell execution, storage and tunnel boundaries when those areas change.
- Keep MCP replies as text and structured data; ChatGPT tool cards are disabled.

Use `npm pack` from the source root to inspect an installable local snapshot. This fork currently documents source distribution. The inherited npm publication scripts and [upstream launch checklist](PUBLIC_LAUNCH_CHECKLIST.md) are not a fork release procedure; do not infer registry ownership or publication authorization from the package name.

## Documentation style

Use concrete commands, flags and failure cases. Keep the project catalog as the main onboarding path, with single-project mode as an option. Distinguish the server source checkout, exposed project roots and runtime state. Use portable example paths and hostnames, and link to current primary documentation for client-specific instructions.
