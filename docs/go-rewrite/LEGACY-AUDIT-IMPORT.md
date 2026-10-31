# Legacy audit import and saturated private fixture

The production entry point is `(*audit.Journal).ImportLegacy(ctx, sourceJSONL, sidecarPath)`. It imports a consistent private copy of the release-0.31 JSONL journal into a fresh, empty Go journal. It preserves action IDs, sequence numbers, stored public metadata and private dashboard metadata, then creates the derived SQLite index and a durable receipt keyed by the source SHA-256.

The importer accepts a regular JSONL file up to 64 MiB, a retention sidecar up to 1 MiB, and individual lines within the original 128 KiB event bound. It validates the sidecar's retained-entry digest and planned-gap evidence. Unexplained gaps remain explicit. It fsyncs the new segments before committing the derived index. Repeating the same import returns the committed receipt instead of appending duplicate actions. A different import into a nonempty destination is refused.

Configured retention still applies during import. Preserve the original private snapshot and manifest even when preparing a smaller retention view. The verification lane below requires every captured action to fit the standard 8 MiB / 350-per-bucket limits so it can assert no pruning or lost payloads.

## Run the optional proof

Place legacy_audit_import_probe_test.go in go/internal/audit/. It skips ordinary test runs unless SP6_AUDIT_SOURCE is supplied. Use the existing private stage-1 JSONL and its matching sidecar, with their exact verified paths. Do not use live ~/.codexpro paths or a snapshot currently being mutated by a private TypeScript process.

Create a new private parent directory for SP6_AUDIT_DEST, but leave the destination itself absent. A suitable destination for a later private server is a new private state root's audit/ directory. The probe creates only that directory and its files; it never starts a process, chooses a token or writes server configuration.

From go/, using Go 1.27.3:

```bash
SP6_AUDIT_SOURCE="$SP6_PRIVATE_JSONL" \
SP6_AUDIT_INDEX="$SP6_PRIVATE_INDEX" \
SP6_AUDIT_DEST="$SP6_PRIVATE_SATURATED_AUDIT" \
nice -n 10 timeout 120s go test ./internal/audit \
  -run '^TestLegacyAuditImportProbe$' -count=1 -v
```

The probe checks private paths and file hashes, refuses an existing destination, and requires a nonempty, ordered, duplicate-free source within the fixed retention bounds. It then:

1. Opens the real Go journal with maxBytes=8,388,608, retainActions=350 and maxOutputBytes=120,000 and imports the exact private legacy copy.
2. Closes the journal and compares every complete physical action record against its source, including private dashboard metadata. JSON encoding whitespace and object key order are ignored; field values, nulls and arrays remain exact.
3. Reopens the journal and requires the entire public status DTO to stay identical.
4. Reads every imported action through the real activity_get handler and compares the full public DTO with the original record after removing only dashboard_metadata, matching the original publicAction implementation.
5. Replays the import after restart and verifies the durable receipt, unchanged status, and unchanged source/sidecar SHA-256 hashes.

It writes import-proof.json inside the new private audit directory. This aggregate contains counts, hashes, limits, import/read timings and public status, with no raw action payloads. The resulting directory remains available for bounded performance runs under the private Go process. Point only a separately launched private instance at its parent state root; never repoint or redeploy the live service.

## Scope of preservation

Import preserves the information actually present in the action journal. For historical batches, this includes recorded request/result metadata and any stored dashboard details. It does not reconstruct stdout, per-operation results or files that the TypeScript journal never retained. Public activity_get/export continues to omit private dashboard metadata; the physical-record check separately proves that metadata survived the private import.

The proof is an actual execution gate. Its existence or ordinary skipped-test compilation does not establish migration success. Keep the source snapshot, full private store, aggregate proof and the subsequent performance result as distinct evidence.
