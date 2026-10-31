# Work history archive capture and verification

This is a private import preparation step for stages 3–4. It does not cut over the service or execute historical work. The Go reader preserves stored run states, documents, operation receipts and events while omitting the retired coordinator, claim sweeper and agent launcher.

## Why DB and WAL are captured together

Committed SQLite transactions may still reside in the WAL. The SHM file is a derived coordination/index structure and contains no database content needed for recovery. The capture therefore preserves the DB and WAL bytes, and lets SQLite rebuild SHM only inside its private staging directory. See [SQLite's WAL file format](https://www.sqlite.org/walformat.html).

The helper never passes the original database path to SQLite. It reads original files through read-only file descriptors, with no-follow and no-atime flags where supported. It does not open or copy the original SHM, request a lock, checkpoint, alter journal mode, stop a process, or edit live config/state.

## Capture procedure

Run the helper as the same unprivileged owner as the source state, under bounded scheduling:

~~~bash
nice -n 10 timeout 180s python3 docs/go-rewrite/archive_work.py \
  --source "$SP6_WORK_SOURCE" \
  --destination "$SP6_PRIVATE_ARCHIVE" \
  --attempts 3 \
  --max-bytes 1073741824
~~~

Set SP6_WORK_SOURCE to the actual work.sqlite path verified in the runtime census. Set SP6_PRIVATE_ARCHIVE to a new directory under the isolated Go state, outside every live state/config directory. The helper refuses an existing destination or one inside the source directory. The one-gibibyte bound covers the original DB and WAL together; the private archive needs roughly twice their size.

For each attempt it:

1. Records the DB/WAL identities, sizes and nanosecond modification/change times. An active rollback journal is rejected.
2. Copies DB/WAL to a fresh private directory and fsyncs each copy.
3. Rechecks all original identities, rereads both original files for a second SHA-256 pass, and rechecks the complete identity set again. A source change causes a retry; at most five attempts are permitted.
4. Opens only the private copy, runs SQLite integrity checks and inventories every table.
5. Uses the [SQLite backup API](https://www.sqlite.org/backup.html) to create a separate private normalized database. It changes only this new database to DELETE journal mode, then checks integrity, foreign keys, schema digest, every table's exact row digest and all historical run states against the raw snapshot.
6. Writes a durable manifest and explicit principal map, fsyncs the private directories, and atomically publishes the finished archive directory.

This is a verified stable-file capture. If live DB/WAL activity prevents a stable interval, the helper exits without publishing an archive. It does not obtain a live SQLite lock or assume an inconsistent copy is acceptable.

Original path/inode changes, non-regular source files, a nonempty rollback journal, unsupported schema, invalid run identity, bad principal mapping, integrity failure or differing row digests all fail closed. Failed attempts remove only their uniquely named private staging directories.

## Archive contents

| File | Purpose |
| --- | --- |
| raw/work.sqlite | Exact verified source DB bytes |
| raw/work.sqlite-wal, if present | Exact verified source WAL bytes |
| work.sqlite | Verified standalone normalized database for immutable Go reads |
| manifest.json | Source identities/checksums, schema/table digests, row counts, per-run states/counts, normalization and integrity receipts |
| principal-map.json | Explicit current-principal to archived-principal mapping; an empty map preserves exact identity |

A derived private raw/work.sqlite-shm may exist after private validation. It is never a copy of the live SHM and is not authoritative archival content.

The manifest inventories every document revision, not only the latest manifest view. Its per-run counts distinguish document rows from distinct document IDs. The full original tables—including sessions, requests, operation receipts and work_jobs—remain in the private SQLite archive. Preserve its restrictive permissions and do not commit raw state or payloads to Git.

The ready Anima run and all other captured states remain unchanged. A stored iteration_id or running job is historical evidence; the archive reader neither issues a claim nor adopts a process.

## Principal ownership

By default workhistory.Open preserves the legacy exact principal filter. If the private instance intentionally uses a different connector identity, provide a reviewed mapping file:

~~~json
{
  "connector_PRIVATE_ID": "connector_ARCHIVED_ID"
}
~~~

Pass its path with --principal-map, then pass the same object through workhistory.Options.PrincipalMap when opening the normalized archive. Mapping values must be identities present in the archived runs; the helper never invents or automatically assigns one. Unmapped callers retain exact identity matching. The manifest records the mapping used for verification.

## Go reader and portable export

Use the normalized work.sqlite path for --work-history. The Go reader opens it with mode=ro, immutable=1 and query_only; it rejects a nonempty adjacent WAL so a partial raw copy cannot silently omit accepted historical transactions. SQLite documents that [immutable files are opened read-only and skip locking/change detection](https://www.sqlite.org/uri.html); only finalized private snapshots qualify.

The retained work_status contract supports list, get, read_document, search_memory, history and operation, with its original paged sections, owner filtering and UTF-8 document cursors. Source observations default to complete:false with an archive diagnostic unless the caller supplies an explicit read-only observation hook.

For a portable, credential-filtered history, Service.Export(ctx, scope, writer) streams JSONL for the authenticated principal. It includes all document revisions and durable events/receipts, strips private iteration token/clock fields and operation request fingerprints, and returns record counts, bytes and SHA-256. It is an internal archive API, not a new MCP action.

## Verification gates

Run the private fixture tests before capturing live history:

~~~bash
nice -n 10 timeout 45s python3 docs/go-rewrite/archive_work_test.py
~~~

The fixture keeps a WAL writer connection open, contains nine runs including one ready Anima run, and has eighteen document revisions. Tests prove that:

- original DB, WAL and SHM hashes stay identical;
- SQLite never receives an original source path;
- the final immutable database retains every table, revision, ready state and stored claim;
- principal mapping is explicit and existing destinations are preserved;
- a changed source is rejected and only the private failed attempt is removed.

After capture, retain the helper's aggregate stdout and manifest checksum with stage-4 evidence. Verify the expected nine runs and the recorded census/table counts against this new snapshot; later legitimate live activity may explain a larger count. Load it through the private Go process only, run work_status and activity read-only contracts, and verify the normalized archive hash remains unchanged afterward.

## Cleanup and rollback

No live service rollback is required: capture never changes it. Stop only the private Go process before changing which archive it reads. Preserve the successful archive until the stage-4 report and future cutover review are accepted. Remove a private test archive only by its exact recorded destination; never delete or checkpoint the original DB/WAL/SHM.
