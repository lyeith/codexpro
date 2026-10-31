# Full native historical export proof

## Scope and current status

The opt-in native test `TestHistoricalFullExportProbe` exercises `workhistory.Service.Export` over the disposable immutable database already prepared for the historical contract suite. It is pinned to the approved 31 October capture:

- Archive SHA-256: `50d09a5cc88c068f897e8734676d784488c9828d65b4da2ae7024f48483f0adf`.
- Nine runs, 465 document IDs and all 1,516 document revisions.
- 231 iterations, 17,705 operations, 11,934 historical work-job rows and 19,406 events.
- The complete SQLite archive also retains 173 sessions, 1,369 request receipts, three metadata rows and the sequence row.

The actual second historical `work_status` comparison passed 405 cases with zero mismatches and 31 expected error responses. Both reference and candidate kept the archive unchanged. The separate full export proof now also passes. Its first execution hit the 120-second context bound after 178,908,900 output bytes / 32,995 complete records, before the independent SQL verifier began. A 20-second CPU profile identified regular-expression replacement as 93.22% of sampled CPU. The subsequent literal-prefilter optimization preserved every original regex/callback and passed byte-for-byte equivalence tests, including Unicode simple-fold aliases.

The same 20-second diagnostic then completed the entire export in 18.490 wall seconds / 18.457 user CPU seconds, producing 210,245,320 bytes and 50,802 records. Actual disk export plus independent full-row verification passed in 40.72 seconds: all nine runs, 465 IDs and 1,516 revisions, one original principal, every row equal and both archive files unchanged. Job `d7608a9b` completed at 20:17:21 UTC. The 120-second context and all verification checks stayed unchanged.

The public JSONL export is a credential-filtered projection. It includes the run, iteration, operation, document, historical job and event records. It excludes session/request/metadata tables, removes the run principal, iteration token/clock/principal fields and operation fingerprint, and applies the shared secret redactor. The original normalized SQLite archive preserves the full tables. Public JSONL must not be described as a lossless replacement for that private archive.

## Install and run

Place `workhistory_export_probe_test.go` in `go/internal/workhistory/`. It intentionally reuses the path, digest and exclusive JSON writer guards from the already installed `historical_contract_probe_test.go`; retain that file.

Use the existing successful historical plan, not the live database, the capture manifest or the published normalized source. The existing plan is under the private `historical-contract-1` fixture and points to its separate read-only `database/work.sqlite` consumer. Set `SP6_WORK_EXPORT_DEST` to a **new direct child** of that plan directory. The test refuses an existing destination.

From the Go module directory:

```bash
SP6_WORK_EXPORT_PLAN=/home/spite/.codexpro-go-proof/historical-contract-1/plan.json \
SP6_WORK_EXPORT_DEST=/home/spite/.codexpro-go-proof/historical-contract-1/full-export-3 \
nice -n 10 timeout 150s go test ./internal/workhistory \
  -run '^TestHistoricalFullExportProbe$' -count=1 -v
```

The successful proof is retained in `full-export-2`; the command uses `full-export-3` as a fresh example. Confirm that destination is absent, and confirm the plan path against the actual successful historical run before invoking the command. If its private fixture directory differs, change both explicit paths together. Do not change the pinned source digest or expected counts to obtain a pass.

The normal package test skips this lane unless `SP6_WORK_EXPORT_PLAN` is set. The opt-in run has a 120-second internal context, a 1 GiB total output limit and a 32 MiB per-record limit. The parent command supplies a 150-second process bound and `nice -n 10`.

## Verification performed

The probe first verifies the plan's source commit, approved archive digest and exact ten-table inventory. It refuses protected live-state paths and their canonical aliases, a linked or writable consumer database, missing source separation, existing output and any SQLite sidecar. The consumer and published capture must both match the captured byte count and digest.

It opens only the private consumer through immutable, query-only SQLite connections. Each archived principal receives a separate `0600` JSONL file inside a new `0700` output directory. No principal is remapped. A missing principal must fail without writing bytes; an unowned principal receives only the archive header.

For every output, the test independently recomputes the file byte count and SHA-256 and compares them with `ExportSummary`. It then streams the entire file against independent SQL rows in the documented order. Every key/value is compared after the explicit credential projection and shared redaction. Unexpected, missing, repeated or inaccessible records fail the proof. Document IDs and revisions are also checked against their physical SQL columns, and duplicate revisions across principal exports are rejected.

All nine runs and every one of the 1,516 revisions must be covered. The proof records source-document and public-document digests and byte totals, including the number of documents transformed by public redaction. It verifies both archive files' byte hashes, sizes, identities and modification times after the export, and checks that no sidecar was created. No coordinator, sweeper, session issuer, process launcher or audit append is constructed.

## Evidence handling

Keep every `principal-*.jsonl` file private. It contains historical work content even after secret redaction. Preserve failed-run directories as evidence and use a fresh destination suffix for any rerun.

Only `full-export-2/summary.json` is intended for repository evidence. It contains counts, byte sizes, SHA-256 digests, state counts, ownership/immutability results and the explicit limits of the public projection; it contains no document text, run IDs, principals, bearer tokens or session credentials. The completed native run passed; use the parent's final manifest to copy only this aggregate into repository evidence.

An unsuccessful native test is not an export compatibility pass. Preserve its bounded logs and private output, diagnose the concrete mismatch, and rerun against a new private destination without altering the source archive.

## Legacy telemetry proof already completed

The actual usage/inbox migration proof passed with five usage snapshots, three inbox project listings and all 20 historical answer receipt replays. The captured usage store had 278 submissions and 1,516 change records; the inbox had 20 items, 60 events and 20 answer receipts. There were zero stored usage reset receipts to replay. Both expected/public-reader DTO hashes matched, all schema/table row digests stayed unchanged, and both published archives stayed unchanged.

Its private aggregate is `/home/spite/.codexpro-go-proof/legacy-readers-20261031T1956Z/comparison.json`. The raw expected/candidate records and any private differences remain outside Git.
