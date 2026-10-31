#!/usr/bin/env python3
"""Capture an immutable CodexPro work archive without opening live SQLite.

Run under nice -n 10 and an external timeout. All original DB/WAL access uses
read-only file descriptors; SQLite opens only staged private copies. A changing
source is retried or rejected, never paused, locked, checkpointed or repaired.
"""
from __future__ import annotations

import argparse
import datetime as dt
import errno
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import struct
import sys
import tempfile
import time
from urllib.parse import quote

REQUIRED_TABLES = {
    "metadata", "runs", "iterations", "operations", "sessions",
    "documents", "events", "requests", "work_jobs",
}
CHUNK = 1024 * 1024


class SourceChanged(RuntimeError):
    pass


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def identity(path: Path) -> dict | None:
    try:
        info = path.stat(follow_symlinks=False)
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(info.st_mode):
        raise ValueError(f"Expected a regular, non-symlink source file: {path.name}")
    return {
        "device": info.st_dev, "inode": info.st_ino, "bytes": info.st_size,
        "mtime_ns": info.st_mtime_ns, "ctime_ns": info.st_ctime_ns,
    }


def source_state(source: Path) -> dict:
    return {suffix: identity(Path(str(source) + suffix)) for suffix in ("", "-wal", "-journal")}


def read_fd(path: Path) -> int:
    # O_NOATIME works for the owner's files without sudo. It prevents this
    # read-only capture from changing access timestamps where Linux supports it.
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    if hasattr(os, "O_NOATIME"):
        flags |= os.O_NOATIME
    return os.open(path, flags)


def fd_identity(fd: int) -> dict:
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("Opened source is not a regular file")
    return {
        "device": info.st_dev, "inode": info.st_ino, "bytes": info.st_size,
        "mtime_ns": info.st_mtime_ns, "ctime_ns": info.st_ctime_ns,
    }


def hash_readonly(path: Path, expected: dict | None = None) -> str:
    digest = hashlib.sha256()
    fd = read_fd(path)
    try:
        before = fd_identity(fd)
        if expected is not None and before != expected:
            raise SourceChanged(f"Source identity changed before verification: {path.name}")
        while True:
            block = os.read(fd, CHUNK)
            if not block:
                break
            digest.update(block)
        if fd_identity(fd) != before:
            raise SourceChanged(f"Source changed during verification: {path.name}")
    finally:
        os.close(fd)
    return digest.hexdigest()


def copy_readonly(source: Path, destination: Path, expected: dict) -> str:
    digest = hashlib.sha256()
    read = read_fd(source)
    write = None
    try:
        if fd_identity(read) != expected:
            raise SourceChanged(f"Source identity changed before copy: {source.name}")
        write = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        while True:
            block = os.read(read, CHUNK)
            if not block:
                break
            digest.update(block)
            view = memoryview(block)
            while view:
                written = os.write(write, view)
                if written <= 0:
                    raise OSError("Short archive copy write")
                view = view[written:]
        os.fsync(write)
        if fd_identity(read) != expected:
            raise SourceChanged(f"Source changed during copy: {source.name}")
    finally:
        os.close(read)
        if write is not None:
            os.close(write)
    return digest.hexdigest()


def sync_directory(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def within(path: Path, parent: Path) -> bool:
    return path == parent or parent in path.parents


def safe_destination(source: Path, destination: Path) -> None:
    resolved = destination.resolve(strict=False)
    home = Path.home().resolve()
    forbidden = [source.parent, home / ".codexpro", home / ".config" / "codexpro"]
    if any(within(resolved, root.resolve(strict=False)) for root in forbidden):
        raise ValueError("Destination must be outside the original state/config directories")
    if destination.exists() or destination.is_symlink():
        raise ValueError("Archive destination already exists; refusing to overwrite")


def quote_identifier(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def table_digest(connection: sqlite3.Connection, name: str) -> dict:
    digest = hashlib.sha256()
    columns = [row[1] for row in connection.execute(f"PRAGMA table_info({quote_identifier(name)})")]
    digest.update(json.dumps(columns, ensure_ascii=False, separators=(",", ":")).encode())
    count = 0
    for row in connection.execute(f"SELECT * FROM {quote_identifier(name)} ORDER BY rowid"):
        digest.update(b"R")
        for value in row:
            if value is None:
                kind, data = b"N", b""
            elif isinstance(value, bytes):
                kind, data = b"B", value
            elif isinstance(value, str):
                kind, data = b"T", value.encode("utf-8")
            elif isinstance(value, int):
                kind, data = b"I", str(value).encode("ascii")
            elif isinstance(value, float):
                kind, data = b"F", struct.pack(">d", value)
            else:
                raise ValueError(f"Unsupported SQLite storage class in {name}")
            digest.update(kind)
            digest.update(len(data).to_bytes(8, "big"))
            digest.update(data)
        count += 1
    return {"rows": count, "sha256": digest.hexdigest(), "columns": columns}


def database_inventory(connection: sqlite3.Connection) -> dict:
    objects = connection.execute(
        "SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name"
    ).fetchall()
    tables = [row[1] for row in objects if row[0] == "table"]
    missing = sorted(REQUIRED_TABLES - set(tables))
    if missing:
        raise ValueError("Missing work-history tables: " + ", ".join(missing))
    schema = connection.execute("SELECT value FROM metadata WHERE key='schema'").fetchone()
    if schema and schema[0] not in ("1", "2", "3"):
        raise ValueError("Unsupported work-history schema")
    table_values = {name: table_digest(connection, name) for name in tables}
    runs = []
    for run_id, principal, project, state_value, revision, body in connection.execute(
        "SELECT id,principal,project,state,revision,body FROM runs ORDER BY id"
    ):
        decoded = json.loads(body)
        if decoded.get("id") != run_id or decoded.get("principal_id") != principal:
            raise ValueError("Run row identity disagrees with its stored JSON")
        run = {
            "id": run_id, "principal_id": principal, "project_id": project,
            "state": state_value, "mode": decoded.get("mode"),
            "revision": revision, "body_sha256": hashlib.sha256(body.encode()).hexdigest(),
            "stored_iteration_id": decoded.get("iteration_id"),
            "counts": {},
        }
        for table in ("iterations", "operations", "sessions", "documents", "events", "work_jobs"):
            run["counts"][table] = connection.execute(
                f"SELECT count(*) FROM {quote_identifier(table)} WHERE run_id=?", (run_id,)
            ).fetchone()[0]
        run["counts"]["document_ids"] = connection.execute(
            "SELECT count(DISTINCT id) FROM documents WHERE run_id=?", (run_id,)
        ).fetchone()[0]
        runs.append(run)
    return {
        "schema": schema[0] if schema else None,
        "schema_sha256": hashlib.sha256(
            json.dumps(objects, ensure_ascii=False, separators=(",", ":")).encode()
        ).hexdigest(),
        "tables": table_values,
        "runs": runs,
        "principals": sorted({run["principal_id"] for run in runs}),
    }


def sqlite_uri(path: Path, query: str) -> str:
    return "file:" + quote(str(path), safe="/") + "?" + query


def normalize_private_copy(raw_db: Path, archive_db: Path, principal_map: dict) -> dict:
    # Only private staged files are ever passed to sqlite3.connect. Rebuilding a
    # private SHM/WAL index is safe and cannot reach any original source path.
    source = sqlite3.connect(sqlite_uri(raw_db, "mode=ro"), uri=True, timeout=5)
    target = None
    try:
        source.execute("PRAGMA query_only=ON")
        check = source.execute("PRAGMA integrity_check").fetchall()
        if check != [("ok",)]:
            raise ValueError("Private copied work database failed integrity_check")
        before = database_inventory(source)
        if any(not isinstance(key, str) or not key or not isinstance(value, str)
               or value not in before["principals"] for key, value in principal_map.items()):
            raise ValueError("Principal map must explicitly reference existing archived principals")
        target = sqlite3.connect(archive_db, timeout=5)
        target.execute("PRAGMA synchronous=FULL")
        source.backup(target, pages=128, sleep=0.01)
        target.commit()
        # This is deliberately a private-only journal conversion. The resulting
        # standalone file passes workhistory.Open's immutable, no-WAL gate.
        mode = target.execute("PRAGMA journal_mode=DELETE").fetchone()[0]
        if mode.lower() != "delete":
            raise ValueError("Could not normalize private backup journal mode")
        target.commit()
        check = target.execute("PRAGMA integrity_check").fetchall()
        if check != [("ok",)]:
            raise ValueError("Normalized work archive failed integrity_check")
        after = database_inventory(target)
        if before != after:
            raise ValueError("Table rows, schema or historical run state changed during private normalization")
        foreign_keys = target.execute("PRAGMA foreign_key_check").fetchall()
        if foreign_keys:
            raise ValueError("Historical work archive contains foreign-key violations")
        return after
    finally:
        if target is not None:
            target.close()
        source.close()


def write_json(path: Path, value: dict) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        data = (json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode()
        view = memoryview(data)
        while view:
            written = os.write(fd, view)
            if written <= 0:
                raise OSError("Short manifest write")
            view = view[written:]
        os.fsync(fd)
    finally:
        os.close(fd)


def capture(source: Path, destination: Path, principal_map: dict | None = None,
            attempts: int = 3, max_bytes: int = 1024 * 1024 * 1024) -> dict:
    source = source.absolute()
    if source.is_symlink():
        raise ValueError("Pass the actual work database path, not a symlink")
    source = source.resolve(strict=True)
    destination = destination.absolute()
    principal_map = dict(principal_map or {})
    if attempts < 1 or attempts > 5:
        raise ValueError("Attempts must be between 1 and 5")
    safe_destination(source, destination)
    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    safe_destination(source, destination)
    start = time.monotonic()
    last_error = None
    for attempt in range(1, attempts + 1):
        stage = Path(tempfile.mkdtemp(prefix=".sp6-work-archive-", dir=destination.parent))
        try:
            os.chmod(stage, 0o700)
            before = source_state(source)
            if before[""] is None:
                raise FileNotFoundError("Work database disappeared")
            if before["-journal"] and before["-journal"]["bytes"]:
                raise SourceChanged("Active rollback journal; no unlocked snapshot is accepted")
            total = sum(value["bytes"] for suffix, value in before.items() if value and suffix != "-journal")
            if total > max_bytes:
                raise ValueError("DB/WAL exceed the bounded capture byte budget")
            raw = stage / "raw"
            raw.mkdir(mode=0o700)
            source_files = {}
            for suffix in ("", "-wal"):
                expected = before[suffix]
                if expected is None:
                    continue
                original = Path(str(source) + suffix)
                target = raw / ("work.sqlite" + suffix)
                digest = copy_readonly(original, target, expected)
                source_files[original.name] = {
                    **expected, "sha256": digest, "archive_path": "raw/" + target.name
                }
            if source_state(source) != before:
                raise SourceChanged("DB/WAL changed while being copied")
            # A second source read proves byte equality, not only stable sizes
            # and timestamps. The global identity window spans both passes.
            for suffix in ("", "-wal"):
                expected = before[suffix]
                if expected is not None:
                    original = Path(str(source) + suffix)
                    if hash_readonly(original, expected) != source_files[original.name]["sha256"]:
                        raise SourceChanged("DB/WAL hashes changed during verification")
            if source_state(source) != before:
                raise SourceChanged("DB/WAL changed during verification")
            captured_at = utc_now()
            normalized = stage / "work.sqlite"
            inventory = normalize_private_copy(raw / "work.sqlite", normalized, principal_map)
            os.chmod(normalized, 0o600)
            wal = Path(str(normalized) + "-wal")
            if wal.exists() and wal.stat().st_size:
                raise ValueError("Normalized archive still has a nonempty WAL")
            # Preserve the raw DB/WAL bytes as exact evidence. Opening the raw
            # copy read-only may create a private SHM, which is derived only.
            for original_name, info in source_files.items():
                private_path = stage / info["archive_path"]
                if hash_readonly(private_path) != info["sha256"]:
                    raise ValueError("Private source copy changed unexpectedly")
            archive_hash = hash_readonly(normalized)
            fd = os.open(normalized, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
            manifest = {
                "schema_version": 1, "captured_at": captured_at, "completed_at": utc_now(),
                "source_path": str(source), "source_access": "O_RDONLY|O_NOFOLLOW|O_NOATIME where supported",
                "source_sqlite_connections": 0, "source_files": source_files,
                "source_stable_through_copy_and_second_hash_pass": True,
                "source_shm": "Not opened or copied; private SQLite may rebuild its own derived SHM.",
                "source_rollback_journal": before["-journal"],
                "attempt": attempt, "elapsed_seconds": round(time.monotonic() - start, 3),
                "normalized_archive": {
                    "path": "work.sqlite", "bytes": normalized.stat().st_size,
                    "sha256": archive_hash, "journal_mode": "delete",
                    "integrity_check": "ok", "foreign_key_check": "ok",
                    "table_digests_match_raw_snapshot": True,
                },
                "principal_map": principal_map,
                "principal_policy": "Explicit map only; all unmapped callers retain exact principal filtering.",
                "historical_execution": "None. Stored ready/claimed/running states are evidence, not current claims or processes.",
                "inventory": inventory,
            }
            write_json(stage / "manifest.json", manifest)
            write_json(stage / "principal-map.json", principal_map)
            sync_directory(raw)
            sync_directory(stage)
            os.rename(stage, destination)
            sync_directory(destination.parent)
            return {
                "destination": str(destination),
                "manifest_sha256": hash_readonly(destination / "manifest.json"),
                "archive_sha256": archive_hash,
                "runs": len(inventory["runs"]),
                "documents": inventory["tables"]["documents"]["rows"],
                "document_ids": sum(run["counts"]["document_ids"] for run in inventory["runs"]),
                "tables": {name: value["rows"] for name, value in inventory["tables"].items()},
                "attempt": attempt, "elapsed_seconds": manifest["elapsed_seconds"],
            }
        except SourceChanged as error:
            last_error = error
            shutil.rmtree(stage)
            if attempt < attempts:
                time.sleep(0.2)
        except BaseException:
            shutil.rmtree(stage)
            raise
    raise SourceChanged(f"No stable DB/WAL snapshot in {attempts} attempts: {last_error}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--destination", required=True, type=Path)
    parser.add_argument("--principal-map", type=Path)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--max-bytes", type=int, default=1024 * 1024 * 1024)
    options = parser.parse_args()
    mapping = {}
    if options.principal_map:
        with options.principal_map.open(encoding="utf-8") as handle:
            mapping = json.load(handle)
        if not isinstance(mapping, dict):
            raise ValueError("Principal map must be a JSON object")
    result = capture(options.source, options.destination, mapping, options.attempts, options.max_bytes)
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, sqlite3.Error, SourceChanged) as error:
        print(json.dumps({"error": type(error).__name__, "message": str(error)}), file=sys.stderr)
        sys.exit(1)
