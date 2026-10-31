#!/usr/bin/env python3
"""Capture legacy usage/inbox SQLite and verify private Go reader imports.

Original DB/WAL files are read only through archive_work's stable-copy helpers.
SQLite receives only private copies. All writes are beneath new private paths.
"""
from __future__ import annotations

import argparse
from contextlib import closing
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import sqlite3
import tempfile
import time

from archive_work import (SourceChanged, copy_readonly, hash_readonly, identity,
                          quote_identifier, source_state, sqlite_uri,
                          sync_directory, table_digest, utc_now, write_json)

TABLES = {
    "usage": {"pro_submissions", "usage_settings", "usage_collectors", "usage_changes", "usage_requests"},
    "inbox": {"inbox_items", "inbox_events", "inbox_answers"},
}
MAX_BYTES = 128 * 1024 * 1024


def require(condition, message):
    if not condition:
        raise ValueError(message)


def private_path(path: Path, *, exists=True) -> Path:
    path = path.absolute().resolve(strict=exists)
    homes = {Path.home().resolve(), Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()}
    for home in homes:
        for suffix in (".codexpro", ".config/codexpro"):
            blocked = (home / suffix).resolve(strict=False)
            require(path != blocked and not path.is_relative_to(blocked), "protected live destination refused")
    require(path != Path("/") and path not in homes, "root/home is not a private fixture")
    return path


def inventory(connection, kind):
    objects = connection.execute("SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name").fetchall()
    tables = [row[1] for row in objects if row[0] == "table"]
    require(TABLES[kind].issubset(tables), "legacy " + kind + " schema is incomplete")
    values = {name: table_digest(connection, name) for name in tables}
    return {"schema_sha256": hashlib.sha256(json.dumps(objects, ensure_ascii=False, separators=(",", ":")).encode()).hexdigest(),
            "tables": values}


def normalize(raw, target, kind):
    with closing(sqlite3.connect(sqlite_uri(raw, "mode=ro"), uri=True, timeout=5)) as source:
        source.execute("PRAGMA query_only=ON")
        require(source.execute("PRAGMA integrity_check").fetchall() == [("ok",)], "private raw copy failed integrity_check")
        before = inventory(source, kind)
        with closing(sqlite3.connect(target, timeout=5)) as output:
            output.execute("PRAGMA synchronous=FULL")
            source.backup(output, pages=128, sleep=0.01)
            output.commit()
            require(output.execute("PRAGMA journal_mode=DELETE").fetchone()[0].lower() == "delete", "private normalization failed")
            output.commit()
            require(output.execute("PRAGMA integrity_check").fetchall() == [("ok",)], "normalized copy failed integrity_check")
            require(output.execute("PRAGMA foreign_key_check").fetchall() == [], "legacy database has foreign-key violations")
            after = inventory(output, kind)
            require(before == after, "schema or table rows changed during backup")
    return after


def capture(kind, source, destination, attempts=3, max_bytes=MAX_BYTES):
    require(kind in TABLES, "unsupported legacy store")
    require(1 <= attempts <= 5 and 0 < max_bytes <= MAX_BYTES, "capture bounds are invalid")
    source = source.absolute()
    require(not source.is_symlink(), "pass the actual source database, not a symlink")
    source = source.resolve(strict=True)
    require(not destination.exists() and not destination.is_symlink(), "destination already exists or is a symlink")
    destination = private_path(destination, exists=False)
    require(not destination.is_relative_to(source.parent), "destination is inside original state")
    require(not destination.exists(), "destination already exists")
    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(private_path(destination, exists=False) == destination, "destination path changed")
    started = time.monotonic()
    last_error = None
    for attempt in range(1, attempts + 1):
        stage = Path(tempfile.mkdtemp(prefix=".sp6-" + kind + "-capture-", dir=destination.parent))
        try:
            os.chmod(stage, 0o700)
            before = source_state(source)
            require(before[""] is not None, "source database disappeared")
            if before["-journal"] and before["-journal"]["bytes"]:
                raise SourceChanged("active rollback journal")
            require(sum(v["bytes"] for k, v in before.items() if v and k != "-journal") <= max_bytes,
                    "source DB/WAL exceed the bounded capture size")
            raw = stage / "raw"
            raw.mkdir(mode=0o700)
            copied = {}
            filename = kind + ".sqlite"
            for suffix in ("", "-wal"):
                if before[suffix] is None:
                    continue
                original = Path(str(source) + suffix)
                target = raw / (filename + suffix)
                copied[suffix] = {**before[suffix], "sha256": copy_readonly(original, target, before[suffix]),
                                  "archive_path": "raw/" + target.name}
            if source_state(source) != before:
                raise SourceChanged("source changed during DB/WAL copy")
            for suffix, record in copied.items():
                if hash_readonly(Path(str(source) + suffix), before[suffix]) != record["sha256"]:
                    raise SourceChanged("source changed during second hash pass")
            if source_state(source) != before:
                raise SourceChanged("source changed during verification")
            captured_at = utc_now()
            database = stage / filename
            tables = normalize(raw / filename, database, kind)
            os.chmod(database, 0o600)
            for suffix, record in copied.items():
                require(hash_readonly(stage / record["archive_path"]) == record["sha256"], "raw private copy changed")
            require(not Path(str(database) + "-wal").exists(), "normalized database still has a WAL")
            fd = os.open(database, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
            manifest = {"schema_version": 1, "kind": kind, "captured_at": captured_at, "completed_at": utc_now(),
                        "source_path": str(source), "source_sqlite_connections": 0,
                        "source_access": "O_RDONLY|O_NOFOLLOW|O_NOATIME where supported; stable second hash pass",
                        "source_shm": "not opened or copied", "source_files": copied,
                        "source_stable_through_copy_and_second_hash_pass": True,
                        "normalized_archive": {"path": filename, "sha256": hash_readonly(database),
                                               "bytes": database.stat().st_size, "journal_mode": "delete",
                                               "integrity_check": "ok", "foreign_key_check": "ok",
                                               "table_digests_match_raw_snapshot": True},
                        "inventory": tables, "attempt": attempt, "elapsed_seconds": round(time.monotonic()-started, 3)}
            write_json(stage / "manifest.json", manifest)
            sync_directory(raw)
            sync_directory(stage)
            require(not destination.exists(), "destination appeared during capture")
            os.rename(stage, destination)
            sync_directory(destination.parent)
            return {"kind": kind, "destination": str(destination), "manifest_sha256": hash_readonly(destination / "manifest.json"),
                    "archive_sha256": manifest["normalized_archive"]["sha256"],
                    "tables": {name: table["rows"] for name, table in tables["tables"].items()},
                    "attempt": attempt}
        except SourceChanged as error:
            last_error = error
            shutil.rmtree(stage)
            if attempt < attempts:
                time.sleep(0.2)
        except BaseException:
            shutil.rmtree(stage)
            raise
    raise SourceChanged(f"no stable snapshot in {attempts} attempts: {last_error}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kind", choices=tuple(TABLES), required=True)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--max-bytes", type=int, default=MAX_BYTES)
    options = parser.parse_args()
    result = capture(options.kind, options.source, options.destination, options.attempts, options.max_bytes)
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
