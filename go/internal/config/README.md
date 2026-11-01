# Go startup and operational limits

The Go process owns its state directory and profile files. In production it runs as `codexpro.service` and shares the operator configuration in `~/.config/codexpro` with the TypeScript release it replaced: the EnvironmentFile, the project catalog and the bearer token file. It never opens a path inside the TypeScript state tree `~/.codexpro`, which stays as it was left so that a rollback finds it intact. When systemd socket-activates the process (`LISTEN_PID`/`LISTEN_FDS`), it serves on the inherited fd 3 instead of binding the configured host and port.

## Startup

Configuration precedence is explicit CLI options, process environment, the private EnvironmentFile, the private saved profile, then source defaults. The private EnvironmentFile fills the role normally served by the TypeScript launcher's process environment; only the twelve operational limits can change on a later reload.

A saved profile is selected by the canonical default-project root in the initial catalog, using the TypeScript layout `profiles/<first 24 hex characters of SHA-256(root)>.json` beneath the Go state directory. A mismatched saved root is ignored. Symlinks cannot make this profile reader leave the private directory. Credentials in a saved profile are ignored: the Go instance's separate token file remains authoritative.

A profile save does not alter an existing Config. Retained settings apply on the next Load, and explicit CLI options win. Connection/tunnel metadata can remain in the profile, but this independent Go process does not launch a tunnel or restart any service.

Bash permission is always full and write permission is always workspace. Handoff, Codex transcript sessions, autonomous work and other approved retired modes cannot be enabled through profiles, environment or flags. Inventory tier (minimal, standard or full) is distinct from permission; the retained registry determines its exposed workspace tools.

The account home and an environment-selected home are both checked for the TypeScript state tree. Checks follow existing symlink ancestors. Existing token files must be regular, private, bounded to 16 KiB and unshared by hard links. These checks run before configuration loading creates any directory.

## Reload

Source: `src/config.ts:657–768`. The parser preserves the source EnvironmentFile quoting, escape, continuation, duplicate-key and comment rules. It never evaluates shell syntax or expands variables. NUL, BOM, invalid UTF-8 and files larger than 1 MiB are rejected.

`ReloadOperationalLimits` returns `{ok, changed, ignored}` and an error. `Reload` preserves the existing Go error-only API. Missing keys retain their effective values. Unknown and startup-only keys are ignored, with names only in the report. Every supplied operational number is validated before the complete update is installed under one mutex. Invalid updates leave all values unchanged. Bash default timeout and maximum timeout are independently bounded, matching the source; there is no additional cross-field rejection.

| Environment key suffix (CODEXPRO_) | Source fallback | Minimum | Maximum |
| --- | ---: | ---: | ---: |
| MAX_OUTPUT_BYTES | 120000 | 4000 | 2000000 |
| MAX_BASH_TIMEOUT_MS | 600000 | 1000 | 900000 |
| BASH_TIMEOUT_MS | 120000 | 1000 | 900000 |
| JOB_TIMEOUT_MS | 2700000 | 10000 | 21600000 |
| MAX_JOBS | 12 | 1 | 64 |
| MAX_JOBS_PER_WORKSPACE | 6 | 1 | 32 |
| MAX_JOB_OUTPUT_BYTES | 8388608 | 65536 | 268435456 |
| JOB_RETENTION_MS | 86400000 | 1000 | 604800000 |
| MAX_RETAINED_JOB_BYTES | 536870912 | 65536 | 2147483648 |
| MAX_JOB_HISTORY_PER_WORKSPACE | 50 | 1 | 200 |
| AUDIT_MAX_BYTES | 8388608 | 4096 | 1073741824 |
| AUDIT_RETAIN_ACTIONS | 350 | 1 | 1000000 |

The live capture's 90-minute job timeout is an environment setting, not a source default. Matched private comparisons must set 5400000 explicitly; the Go default is the source's 45 minutes.

## Verification

Tests cover parser goldens, exact operational key membership, all numeric boundaries, JavaScript numeric syntax, atomic rejection, concurrent snapshots, source fallback/clamping, private profile identity and symlink boundaries, explicit CLI overrides, retired-mode rejection, live-path and listener aliases, and token-file bounds. An integration test saves a profile through the admin HTTP handler and verifies that only a subsequent Config.Load observes the change. The same test verifies that operational reload ignores startup keys and the next load applies them.

All execution and race-test results are reported by the root agent after running the staged package with the pinned Go toolchain.

