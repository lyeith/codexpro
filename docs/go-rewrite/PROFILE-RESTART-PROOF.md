# Saved HTTP profile: actual-process restart proof

## Existing coverage and the remaining distinction

`go/internal/config/restart_integration_test.go` saves through the HTTP handler
using `httptest`, verifies that the current configuration remains unchanged, and
calls `config.Load` again to check saved-profile and EnvironmentFile precedence.
That is useful native integration coverage, but it does not start a second OS
process. The paired admin/catalog lane intentionally reports
`PROFILE-ACTUAL-NEXT-START` as pending for that reason.

`profile_restart_proof.py` closes that specific gap by starting and stopping three
new private Go processes. It does not attach to an existing candidate and does not
control the TypeScript service or any service manager.

## Four bounded cases

| Case | Process | Assertion |
| --- | --- | --- |
| 01 | Fresh process, no saved profile | The actual process reports standard tools, compact Bash transcript, 64 maximum worktrees, `HEAD` base, and a null Bash session. |
| 02 | Same process | HTTP POST saves minimal tools, full transcript, a limit of 7, `main` base, and a named Bash session. Mode-0600 persisted bytes contain those exact fields. The existing process's entire server-config object and tools/list descriptors remain unchanged. |
| 03 | New PID/start-time identity, same state and launch arguments | The saved fields are active, and the registered minimal inventory matches the captured source oracle with approved retirements removed. |
| 04 | Third owned process | Explicit `--tool-mode full --max-worktrees 11` overrides only those two saved fields. Other saved fields remain active, and the persisted profile hash is unchanged. |

The exact source registry oracle is
`go/internal/contracts/testdata/registry-capability-oracle.json`, generated from
`src/tools/registry.ts` at the captured commit. Its single-project, direct-mode,
analysis-enabled, metadata-audit cases contain 29 retained standard descriptors,
23 minimal descriptors, and 30 full descriptors. This proof checks tools/list and
server_config against the same selected source case. Approved retirements are
recorded separately; they are not counted as unchanged runtime behavior.

The source of startup precedence is `go/internal/config/startup.go`: explicit CLI
values precede process environment, the private EnvironmentFile, saved profile,
and defaults. The profile is derived from the canonical default project root by
`go/internal/config/profile.go`. The new process executes the normal executable
entry point, so the proof also checks that startup plumbing actually calls this
loader.

## Isolation and cleanup

The helper refuses an occupied port 7210 before creating any state or launching a
child. Each phase checks again before launch. It accepts only a new state path
under `~/.codexpro-go-proof/` or `/tmp/sp6-profile-restart-*`; canonical live-state
and repository paths are rejected by the shared private-path guard. Its own
catalog, token, profile, limits, logs, and stores are kept under that new directory.

Child environment is restricted to the shared fixture allowlist, so an inherited
`CODEXPRO_*` or `CODEX_HOME` setting cannot silently override the fields under
test. The executable is hashed before each launch. The base launch explicitly
binds private state/catalog/token/instructions and the fixed port, and does not
supply CLI overrides for the five fields being tested.

Before HTTP or MCP work, the shared launch guard checks process ownership, Linux
start ticks, and the PID's listening socket. The executable path must also match.
Every phase resolves the catalog's direct workspace ID from the canonical root,
then reads a distinct phase/PID/start-time nonce through MCP and verifies both
the exact first line and full-file SHA-256. This detects accidental cross-binding
between processes or fixtures.

The helper reuses `OwnedServer.stop` from the frozen managed runner. It signals
only the process group created by its own `Popen(start_new_session=True)`, with
PID/start-time and session/group checks before signaling and bounded cleanup of
witnessed descendants. Completed cleanup is cached so a later reused PID is never
signaled. The process budget is 120 seconds, with bounded per-request, startup,
and cleanup waits. Use the external 180-second timeout below with a 90-second
termination grace so bounded session closes and owned process-group cleanup can
finish after TERM. A hard KILL can prevent final evidence publication.

The evidence stream checks every record against all private bearer tokens and MCP
session identifiers. The persisted profile credential is compared only in memory
and recorded as `<saved>`. The private profile remains on disk for inspection;
cleanup stops owned processes and preserves their state.

## Invocation

Run only after the parent has made port 7210 free by stopping its own preceding
candidate. The helper itself never stops an existing listener.

```bash
nice -n 10 timeout --signal=TERM --kill-after=90s 180s \
  python3 docs/go-rewrite/profile_restart_proof.py \
  --binary /home/spite/.codexpro-go/bin/codexpro-go \
  --state-parent /home/spite/.codexpro-go-proof/profile-restart-20261031-01 \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/profile-restart-01.json
```

The helper imports the existing adjacent `contract_suite`,
`paired_contract_suite`, `admin_catalog_contract_suite`,
`managed_contract_suite`, and `process_contract_suite` modules. No existing runner
is edited. Offline checks run as:

```bash
python3 -m unittest discover -s docs/go-rewrite -p test_profile_restart_proof.py -v
```

Thirteen offline tests passed in local staging. They exercise real private Git
and profile files, wrong or omitted runtime values, source inventory selection,
credential-safe evidence, persisted-profile tampering, inherited configuration
isolation, occupied-port refusal before state creation, and reused-PID cleanup
refusal. Socket and process launch operations are mocked in those tests; they do
not constitute actual runtime proof.

The helper sets `profile_restart_proven=true` only after all four runtime cases
and all owned-process stops succeed. `passed` additionally requires no cleanup
errors. It always leaves `stage4_complete=false`: a Go-only startup proof does not
establish general differential parity, port migration, tunnel launch behavior,
historical-state adoption, or completion of the full rewrite.
