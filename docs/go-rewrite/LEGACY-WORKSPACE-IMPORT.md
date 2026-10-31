# Legacy catalog and managed-lease archive import

This implements the **read-only history** portion of approved DESIGN section 10. It
does not promote a legacy lease into an active Go lease, open a recorded
checkout, signal a process, start a service, or perform cutover.

## What it preserves

`internal/legacyworkspace.Import` reads an explicitly bounded immutable snapshot
and writes only a distinct `legacy-workspaces.sqlite` database in the supplied
private destination. It stores the original catalog, optional persistent
connector identity, and every accepted lease document as raw bytes. It also
indexes workspace/project/repository/owner identity, source version and state
through its manifest and lease table.

Both source lease versions and all five states remain intact. Absent versus
null fields, revision, timestamps, release/failure/retry metadata, and unknown
fields survive through the original bytes. No path or owner is rewritten. The
archive uses schema version 1, WAL and synchronous FULL. A source-manifest hash
identifies each import; repeating the same snapshot verifies and reuses the
existing rows rather than inserting duplicates. A changed snapshot adds a
separate history entry. The normal Go archive reader checks the receipt,
document hashes, byte lengths, indexed identity and counts.

The database has only `legacy_imports`, `legacy_catalogs` and `legacy_leases`.
It is separate from `workspaces.sqlite`. The runtime manager does not open it.
The reader opens the archive in SQLite read-only/query-only mode. Every receipt
states `read_only_history:true`, `active_lease_adoptions:0` and
`catalog_roots_resolved:false`.

## Exact source validation and explicit limits

The captured source store, SHA256
`ac01aa3a57e4e6fdbeb19102b62d4052f8c057c526818bea0db918b32003c515`,
accepts versions 1/2, required string identity/path/time fields, the five state
words, and filenames equal to SHA256(workspaceId) plus `.json`. It does not
validate revision or optional fields. Version 1 does not require a string
projectId. Its `String(state)` test also accepts a single-element nested array
containing a state word. The importer matches that predicate for history
preservation; none of these records receive runtime authority.

`testdata/source-lease-oracle.json` contains **73 cases evaluated by the exact
captured TypeScript predicate**, with 29 accepted. The fixture generator extracts
only that pure predicate and removes its two TypeScript type annotations. It
does not instantiate `FileLeaseStore`, a manager, or a reference server.

The catalog validator follows the captured parser's JSON fields, unknown-key
rules, IDs, default selection, labels measured in UTF-16 units, ref rules,
project/creation-root counts and worktree bounds. It intentionally does not
resolve catalog roots or inspect Git: those recorded paths may belong to the
live system. Canonical-root uniqueness and runtime repository admission remain
separate gates, explicitly recorded as unresolved in the receipt. Literal
duplicate roots are rejected.

Inputs are bounded to 1,000,000 catalog bytes, 1 MiB per lease, 4 KiB for the
identity, 10,000 directory entries and 64 MiB total. Source files must be regular
independent copies; paths below the snapshot root cannot contain symlinks.
Reads verify inode, size, mode, mtime, ctime, hardlink count and content stability,
then repeat verification before import commit. Directory membership and absence
of an optional identity are also checked. Inputs are never chmodded, renamed,
quarantined or deleted. A malformed `.json` aborts before SQL is opened. The
source store would quarantine it; the read-only importer instead reports it.

Private output path checks reject the live configuration/state trees, source/
destination overlap, file symlinks and hardlinked state. A per-archive file lock
serializes importer processes. Existing corruption is reported, never repaired
silently or replaced with empty state.

## Why version-1 promotion is deliberately absent

Reference manager initialization (`manager.ts:43–123`) first inspects every
configured Git scope. A v1 lease is upgraded only when exactly one project has a
matching repositoryId. The manager then changes version, projectId, revision
and updatedAt and **saves back to the source**. It subsequently filters by
project/repository/common-directory identity and runs reconciliation
(`manager.ts:376–470`), which can mutate provisioning/removing/ready state and
mark exceptions orphaned.

That workflow is unsuitable for a read-only importer. This archive retains all
source records, including unmatched history, without pretending that copied
paths have been admitted. Future operational migration must preserve stable
workspace identity, prove the Git linked-worktree/common-directory mapping,
resolve any v1 project ambiguity, establish explicit ownership after a drain,
and run the approved managed business/lifecycle gates. This archive alone is
not evidence of that operational migration.

## Executing the proof

The capture script only reads an explicitly selected catalog and managed store.
It creates a **new** private snapshot directory, recording source paths,
hashes, byte counts, mtimes, modes, ignored entries and two-pass stability.
It never starts the TypeScript manager. The produced probe plan binds the
immutable copied bytes to the source capture manifest.

Run capture with the parent-supplied exact source and private destinations:

```bash
python3 docs/go-rewrite/capture_legacy_workspaces.py \
  --catalog-file /absolute/source/projects.json \
  --managed-root /absolute/source/worktrees \
  --snapshot-root /absolute/private/new-workspace-snapshot \
  --archive-dir /absolute/private/new-workspace-archive
```

Then use the required Go 1.27.3 toolchain from the module directory:

```bash
nice -n 10 go test -count=1 ./internal/legacyworkspace
nice -n 10 go vet ./internal/legacyworkspace ./cmd/import-legacy-workspaces
CODEXPRO_LEGACY_WORKSPACE_PLAN=/absolute/private/new-workspace-snapshot/probe-plan.json \
  nice -n 10 go test -count=1 -run '^TestCapturedLegacyWorkspaceSnapshot$' -v ./internal/legacyworkspace
```

The normal unit command skips the retained-state probe when its explicit plan
is absent. A skipped probe is **not** migration evidence. When supplied, the
probe imports twice, compares complete documents through the Go archive
reader, verifies source bytes/metadata unchanged, and writes a private proof
receipt. It reports `managed_runtime_migration_proven:false`.

The standalone importer uses the same implementation:

```bash
nice -n 10 go run ./cmd/import-legacy-workspaces \
  --snapshot-root /absolute/private/new-workspace-snapshot \
  --catalog catalog.json \
  --managed-root worktrees \
  --destination /absolute/private/new-workspace-archive
```

Its stdout is the manifest/receipt, not raw documents or the connector identity.
Both the command and retained-state probe have a two-minute deadline.

## Verification state when staged

- Source oracle generation: executed locally, 73 cases, Node v24.22.1.
- Capture helper: five local tests passed; source preservation, mode/independent
  copies, existing destination refusal, overlap refusal, ignored symlink and
  catalog/identity symlink handling.
- Go compile/unit/vet and retained-source import: **pending parent execution**.
- Managed paired HTTP lifecycle and actual operational lease migration:
  **not established by this archive lane**.
