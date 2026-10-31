# Historical work_status compatibility lane

This offline lane compares the release-0.31.0 work_status read contract with the Go archive reader on one disposable, normalized SQLite archive. It does not create a coordinator, WorkStore instance, session issuer, claim sweeper, agent launcher, process, or audit writer. Stored run states remain unchanged, including a ready Anima run.

The coordinator approved the archive preparation method at 18:24 on 31 October 2026: stable O_RDONLY/O_NOATIME DB/WAL capture with a second hash pass, followed by SQLite backup from a read-only private copy, with integrity, foreign-key, schema, table and revision checks. Use a successful archive_work.py manifest; do not point this lane at live work.sqlite.

## Files and placement

Place historical_work_reference.mjs, historical_work_reference_test.mjs, historical_work_contracts.py and test_historical_work_contracts.py beside archive_work.py under docs/go-rewrite/. Place historical_contract_probe_test.go in go/internal/workhistory/. The probe skips ordinary test runs unless SP6_WORK_PLAN is explicitly set.

The Node reference loads the original compiled release modules and Zod schema. It binds only the original read methods to a frozen SELECT-only store facade. The original status method calls sweep before reading; the adapter verifies its exact method body and uses its read projection with that single leading sweep omitted. A changed or unrecognized release body fails the lane. No lifecycle method is available on either facade.

Use the immutable release directory whose base commit was captured as 44bb76bfa671e4aad540a8976f0fa6e6d5c2507a. The reference checks package version 0.31.0 and records SHA-256 hashes of all imported read/format modules. It does not claim those hashes independently establish Git provenance; the captured release deployment supplies that binding.

## Run

Set SP6_ARCHIVE_MANIFEST to the successfully captured manifest, SP6_WORK_PROOF to a new private proof directory, and SP6_REFERENCE_RELEASE to the exact captured release directory. Do not reuse an existing proof directory or output file.

```bash
nice -n 10 timeout 60s python3 docs/go-rewrite/historical_work_contracts.py prepare \
  --archive-manifest "$SP6_ARCHIVE_MANIFEST" \
  --destination "$SP6_WORK_PROOF"
```

Preparation verifies the published archive's checksums and counts, then copies it to a separate disposable database with mode 0400. It creates a fixed-clock plan and requests covering every archived principal and run. It never opens the published capture or live state with SQLite.

```bash
nice -n 10 timeout 90s node docs/go-rewrite/historical_work_reference.mjs \
  --release "$SP6_REFERENCE_RELEASE" \
  --plan "$SP6_WORK_PROOF/plan.json" \
  --output "$SP6_WORK_PROOF/reference.jsonl"
```

From the go/ module directory, using the required Go 1.27.3 toolchain:

```bash
SP6_WORK_PLAN="$SP6_WORK_PROOF/plan.json" \
SP6_WORK_REFERENCE="$SP6_WORK_PROOF/reference.jsonl" \
SP6_WORK_CANDIDATE="$SP6_WORK_PROOF/candidate.jsonl" \
nice -n 10 timeout 90s go test ./internal/workhistory \
  -run '^TestHistoricalContractProbe$' -count=1 -v
```

Then, from the repository root:

```bash
nice -n 10 timeout 45s python3 docs/go-rewrite/historical_work_contracts.py compare \
  --plan "$SP6_WORK_PROOF/plan.json" \
  --reference "$SP6_WORK_PROOF/reference.jsonl" \
  --candidate "$SP6_WORK_PROOF/candidate.jsonl" \
  --output "$SP6_WORK_PROOF/comparison.json"
```

Outputs are exclusive mode-0600 files under the proof directory. They can contain historical text and identities and must stay private. Console output contains only aggregate counts and artifact locations. Keep a redacted aggregate result in repository evidence; do not commit raw request/result streams.

## Coverage and comparison

The plan covers principal ownership and negative ownership, list filters and pagination, exact/unique/ambiguous run references where available, every run's summaries and packet sections, operation/history cursors, source/activity archive semantics, and selected oldest/latest document revisions with UTF-8 byte cursors. The manifest separately reports all preserved rows and the smaller set of document DTO revisions sampled. Preserving all revisions is verified by the archive table digests; the lane does not claim to render every stored revision.

Both sides use the same fixed clock, packet limit, context directory and exact archived principal. This lane supplies no principal remapping. The production reader supports an explicit reviewed map; no identity is rewritten here. Current-source inspection is explicitly incomplete and audit observation is disabled on both sides, as required for an inert archive. These semantics are distinct from the separately tested running-service MCP/HTTP lane.

All output fields, errors, array order, null presence, numeric values and byte-count metadata are compared. Only JSON-valued text blocks are decoded before comparison, so insignificant JSON whitespace and object key order do not produce false differences. No result field is discarded. Both readers attest that the disposable database hash stayed identical and that no WAL, SHM or journal appeared.

## Guard tests

```bash
node docs/go-rewrite/historical_work_reference_test.mjs
python3 -m unittest discover -s docs/go-rewrite -p test_historical_work_contracts.py
```

The fixture tests include nine runs, eight blocked states, one ready Anima state, eighteen document revisions, and an open WAL writer. They verify that original and published capture files stay unchanged, the probe uses only a separate normalized copy, private/live path guards run before SQLite, and comparison detects metadata/type/null changes. These guard tests are preparation evidence; actual release-versus-Go compatibility is established only when the paired lane completes.
