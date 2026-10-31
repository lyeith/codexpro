# Legacy usage and inbox migration proof

The Go stores use the native legacy SQLite schemas, but schema similarity alone does not demonstrate migration. This lane verifies actual captured databases through the Go readers and checks that all logical records survive startup, reading and replay of previously committed receipts.

It has three separate artifacts: an immutable published capture, a disposable Go consumer copy, and private reader evidence. No SQLite connection receives a live source path. The original DB/WAL files are read only with the same stable-copy helpers and double-hash window approved for work history by the coordinator on 31 October 2026 at 18:24. SQLite backup runs from a read-only private copy. Live SHM is neither opened nor copied.

## Place the files

Put legacy_sqlite_migration.py, legacy_stores_proof.py and their test modules beside archive_work.py in docs/go-rewrite/. Put legacy_migration_probe_test.go in go/internal/usage/. Its package is usage_test and it imports the real usage and inbox packages. It skips ordinary test runs unless SP6_LEGACY_PLAN is explicitly supplied.

## Capture the verified legacy paths

Use the exact source paths from runtime/source configuration, not the Go destination convention. The original service derives these stores beneath its audit directory; for the private TypeScript instance the observed files were state/audit/usage/usage.sqlite and state/audit/inbox/inbox.sqlite. Go destinations are state/usage/usage.sqlite and state/inbox/inbox.sqlite. Verify the actual live source paths before assigning SP6_USAGE_SOURCE and SP6_INBOX_SOURCE.

Choose separate, new private directories for SP6_USAGE_CAPTURE, SP6_INBOX_CAPTURE and SP6_LEGACY_PROOF. Existing paths, paths below live state/config, symlink destinations and a destination below its source directory are refused.

```bash
nice -n 10 timeout 90s python3 docs/go-rewrite/legacy_sqlite_migration.py \
  --kind usage --source "$SP6_USAGE_SOURCE" --destination "$SP6_USAGE_CAPTURE" --attempts 3

nice -n 10 timeout 90s python3 docs/go-rewrite/legacy_sqlite_migration.py \
  --kind inbox --source "$SP6_INBOX_SOURCE" --destination "$SP6_INBOX_CAPTURE" --attempts 3
```

Each capture is bounded to 128 MiB of DB plus WAL, with at most five attempts. The manifest records original identities and SHA-256 hashes, zero original SQLite connections, stable source verification, integrity and foreign-key checks, and every table's row count and type-sensitive digest. Raw DB/WAL bytes and a normalized standalone SQLite file remain private. The helper publishes only a completed, verified capture.

## Prepare and run the real Go readers

```bash
nice -n 10 timeout 60s python3 docs/go-rewrite/legacy_stores_proof.py prepare \
  --usage-manifest "$SP6_USAGE_CAPTURE/manifest.json" \
  --inbox-manifest "$SP6_INBOX_CAPTURE/manifest.json" \
  --destination "$SP6_LEGACY_PROOF"
```

Preparation verifies both published captures and copies each to a new consumer directory. It opens only the disposable copies to build expectations from their stored rows. A fixed millisecond UTC clock makes period boundaries, stale collectors and reset-date calculations reproducible.

From go/, with the required Go 1.27.3 toolchain:

```bash
SP6_LEGACY_PLAN="$SP6_LEGACY_PROOF/plan.json" \
SP6_LEGACY_RESULT="$SP6_LEGACY_PROOF/candidate.json" \
nice -n 10 timeout 90s go test ./internal/usage \
  -run '^TestLegacyMigrationProbe$' -count=1 -v
```

The probe verifies all paths, initial file hashes and separate file identities before opening either store. The real Go constructors may enable WAL and initialize their normal connections in the disposable consumer directories. They cannot reach the published captures or live state. The probe calls:

- usage Snapshot for all submissions and each archived project, plus History;
- inbox List and Events through every page, both globally and for each archived project;
- RequestReset and Answer with every already committed legacy request payload, requiring the original stored receipt and preserving its original author and clock.

The final replay is a private migration compatibility check. It should perform no logical mutation because the receipts already exist. The next step verifies that property by comparing all table digests, including idempotency receipts, event history and SQLite sequence state.

From the repository root:

```bash
nice -n 10 timeout 60s python3 docs/go-rewrite/legacy_stores_proof.py verify \
  --plan "$SP6_LEGACY_PROOF/plan.json" \
  --candidate "$SP6_LEGACY_PROOF/candidate.json" \
  --output "$SP6_LEGACY_PROOF/comparison.json"
```

## Evidence and limits

Verification requires the published archive and manifest hashes to stay identical, every consumer schema/table digest to match the capture, and integrity_check to remain clean. It compares all returned reader fields and receipt values without dropping fields or normalizing clocks, counts, types, nulls, text or original authors. The fixed clock is the only controlled time input.

The aggregate comparison contains table counts and digests and can be retained with stage-4 evidence. Expected values, candidate output and any difference files contain private archived text and must not be committed. A failed read, changed row, incompatible receipt or DTO difference is an unresolved migration failure; the report does not turn it into a pass based on shared schema names.

This is a storage-to-Go reader proof. The separate paired HTTP/MCP lane compares the release implementation's request validation, HTTP contracts and state transitions. An empty legacy receipt table yields zero replay cases, which should be reported as such rather than claimed as historical replay coverage.

## Preparation tests

```bash
python3 -m unittest discover -s docs/go-rewrite -p 'test_legacy_*.py'
```

The six local guards exercise open-WAL private fixtures, unchanged original DB/WAL/SHM, all-table and receipt preservation, changing-source rejection, forbidden destinations before SQLite, disposable-only expectation reads, strict DTO comparison and rejection when logical rows changed despite matching candidate output. The opt-in native Go probe and actual captured legacy databases remain a separate required execution gate.
