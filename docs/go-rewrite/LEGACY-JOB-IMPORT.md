# Private TypeScript job-history import

This implements the approved design's explicit, idempotent migration boundary.
It does not migrate any live service in stages 1–4. `ImportLegacySnapshot` reads
an already-copied private TypeScript v1 job directory and creates a new private Go
state directory. There is no process adoption, signal, helper call, control-file
write, service action or network request in the import path.

## Preserved data and ownership boundary

The exact source `jobs.json` is retained as `legacy-jobs/jobs.json`, including
unknown historical fields such as work receipts. Finished records with settled
ownership enter the Go jobs table with their IDs, workspace/project attribution,
commands, status, timestamps, acknowledgement, captured-byte counts and retained
raw/rendered output. Every control path, PID, process identity, helper claim and
scope identity is removed from the runtime record. `launch_state=historical` and
`quiescence_scope=inert_history` mean that Go has no process owner; they do not
claim that an original PID was killed. Import creates no completion-outbox or
active pin rows and does not replay audit completions.

Running records, prepared launches, explicit nonquiescent records and terminal
records with unresolved scope/claim ownership remain only in the source archive.
The manifest gives each record its disposition and reason. Import does not stop,
cancel, acknowledge or revive these records. Their original owner is unchanged.

Raw and rendered files retain their exact bytes. The workspace/job/start-time
cursor identity and byte offsets therefore remain valid. Expired records retain
their captured counts and availability flag; import does not fabricate missing
output or reset retention ages. Normal Go retention applies when its manager is
subsequently used. A finite old-runner record is supported only when the private
snapshot already contains its rendered files; the importer does not execute the
TypeScript lazy-rendering path or silently substitute raw logs.

## Inputs, bounds and durability

`SourceDir/jobs.json` is the only metadata input. `OriginalJobsDir` is provenance
used to validate the original `<job-id>.out/.err` paths; the importer never opens
that directory. Output reads use fixed ID-derived relative paths beneath the
private snapshot, with component-by-component `openat/O_NOFOLLOW`. Files must be
regular private copies with one hard link. FIFOs, devices, symlinks, escaping
paths, duplicate IDs, bad timestamps, invalid rendered UTF-8, changing files and
incorrect expected digests produce explicit errors.

Defaults bound metadata to 64 MiB, records to 10,000, each output file to 64 MiB
and combined raw/rendered output to 512 MiB. Context cancellation is checked
during every bounded read. Each source file is hashed during copying and again
before commit. Source data, permissions and mtimes are never written; `O_NOATIME`
is used where permitted. The snapshot capture helper also verifies source
identities and hashes before publishing a copy.

A fresh destination is reserved with a directory lock and a durable incomplete
marker. The jobs constructor takes a shared lock during construction and refuses
an incomplete marker, so it cannot mistake a partially imported record for an
accepted launch. Private output files and their directories are synced before
the marker is removed. The SQLite import uses `synchronous=FULL`, a transaction,
a closed single-file database and a durable manifest. Failures leave a clearly
quarantined private destination; nothing deletes or repairs the source.

An existing destination is accepted only as a matching completed receipt replay.
Replay verifies the archived metadata and source-file hashes and returns the
original import identity without rewriting SQLite, output or the manifest. It
does not claim that normal subsequent retention has kept all imported logs.

## Source evidence

The baseline is commit `44bb76bfa671e4aad540a8976f0fa6e6d5c2507a`.
`src/jobs.ts` defines the v1 table, ID-derived raw paths and finite retention;
`src/jobOutput.ts` defines the workspace-hashed rendered directory, separate
captured/available sizes, expired receipts and v1 byte cursors. `src/tools/bash.ts`
defines the public job view. The oracle imports the immutable release's actual
`dist/jobOutput.js` and records its SHA-256; it never instantiates `JobManager`.

## Install the staged files

- `legacy_import.go`, `legacy_import_test.go`, and
  `legacy_import_probe_test.go` belong in `go/internal/jobs/`.
- Apply the narrow constructor patch `store-import.patch` to
  `go/internal/jobs/store.go`.
- `import-jobs-main.go` belongs at `go/cmd/import-jobs/main.go`.
- The three Python/Node proof helpers and capture test belong in
  `docs/go-rewrite/`, alongside the existing `archive_work.py`.

## Run the actual private-state proof

Choose the jobs directory of the already-owned private TypeScript instance.
Use a state parent separate from that source and a fresh evidence filename.
After replacing the one source argument with that verified private directory:

```sh
nice -n 10 timeout 190s python3 docs/go-rewrite/legacy_jobs_proof.py \
  --source-jobs /absolute/private-ts/state/jobs \
  --release /home/spite/apps/codexpro/releases/job45-dashboard-20261030-44bb76b \
  --go-dir /home/spite/Projects/codexpro-go/go \
  --state-parent /home/spite/.codexpro-go-proof/migration \
  --output /home/spite/Projects/codexpro-go/docs/go-rewrite/evidence/legacy-jobs-proof.json
```

The wrapper only copies private source files, invokes a read-only reference
output store, and runs the opt-in Go probe under a finite timeout with two Go
workers. It does not restart either HTTP server. It requires at least one
finished job and nonempty retained output; an empty fixture cannot pass as
migration proof. The result records every imported and archive-only record,
source and destination schema versions, metadata/output hashes, and zero source
process operations. The native probe checks every file's complete bytes and the
reference metadata, head/tail excerpts and existing cursors, reopens the Go
manager and repeats them, checks zero completion/pin adoption, and verifies
idempotent receipt replay. Public incremental calls are also compared with the
source page outputs. No success is claimed until that native probe executes.

For explicit offline use outside the proof wrapper:

```sh
nice -n 10 timeout 130s go run ./cmd/import-jobs \
  --state /absolute/never-used-private-go-home \
  --source-jobs /absolute/private-snapshot \
  --original-jobs-dir /absolute/original-directory-recorded-in-snapshot \
  --metadata-sha256 SHA256_FROM_CAPTURE
```

The entire Go Home must be fresh for this import. Import job history first when
combining it with independently verified audit/usage/inbox imports; other
store importers may then populate their separate destinations. This utility
does not remap workspaces or make a historical checkout writable. The eventual
cutover operator must separately establish catalog/lease ownership and follow
the approved runbook. Stage 5 remains out of scope.
