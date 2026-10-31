#!/usr/bin/env python3
"""Copy a bounded private TS jobs snapshot, without instantiating JobManager.

Only a separate private TS instance is an accepted source. All source access is
read-only; unknown/active ownership is archived as metadata and never controlled.
Run under nice -n 10 and a finite external timeout on the shared host.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from archive_work import SourceChanged, copy_readonly, hash_readonly, identity, read_fd, fd_identity, sync_directory, utc_now

JOB_ID = re.compile(r"job_[0-9a-f]{8}\Z")
MAX_METADATA = 64 * 1024 * 1024
MAX_FILE = 64 * 1024 * 1024
MAX_TOTAL = 512 * 1024 * 1024
COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"


def private_path(value: Path, exists: bool) -> Path:
    raw = Path(os.path.abspath(value))
    result = raw.resolve(strict=exists)
    if result != raw:
        raise ValueError("Snapshot paths may not traverse symlinks")
    homes = {Path.home(), Path(pwd.getpwuid(os.getuid()).pw_dir)}
    for home in homes:
        for suffix in (".codexpro", ".config/codexpro"):
            forbidden = (home / suffix).resolve(strict=False)
            if result == forbidden or forbidden in result.parents:
                raise ValueError("Live state/config is not an eligible private snapshot path")
    return result


def checked_file(root: Path, relative: str, maximum: int) -> tuple[Path, dict]:
    path = root / relative
    if Path(relative).is_absolute() or ".." in Path(relative).parts or path.resolve(strict=True) != path:
        raise ValueError("Snapshot file path escapes or traverses symlinks")
    info = path.stat(follow_symlinks=False)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > maximum:
        raise ValueError("Source must be a bounded regular private copy without hard links")
    return path, identity(path)


def eligible(record: dict) -> bool:
    return (record["status"] != "running" and record.get("launch_state") != "prepared"
            and ("quiescent" not in record or record["quiescent"] is True)
            and (not (record.get("scope_unit") or record.get("resource_claim")) or record.get("quiescent") is True))


def metadata(source: Path) -> tuple[bytes, dict, list[dict]]:
    path, before = checked_file(source, "jobs.json", MAX_METADATA)
    fd = read_fd(path)
    try:
        if fd_identity(fd) != before:
            raise SourceChanged("jobs.json changed before reading")
        chunks = []
        remaining = before["bytes"]
        while remaining:
            block = os.read(fd, min(1024 * 1024, remaining))
            if not block:
                raise SourceChanged("jobs.json shortened")
            chunks.append(block)
            remaining -= len(block)
        if os.read(fd, 1) or fd_identity(fd) != before or identity(path) != before:
            raise SourceChanged("jobs.json changed while reading")
    finally:
        os.close(fd)
    raw = b"".join(chunks)
    table = json.loads(raw)
    if table.get("version") != 1 or not isinstance(table.get("jobs"), list) or len(table["jobs"]) > 10000:
        raise ValueError("Expected bounded TypeScript v1 jobs table")
    seen = set()
    for record in table["jobs"]:
        identifier = record.get("id", "")
        if not JOB_ID.fullmatch(identifier) or identifier in seen or not record.get("workspace_id"):
            raise ValueError("Invalid or duplicate source job identity")
        seen.add(identifier)
        if record.get("status") not in ("running", "succeeded", "failed", "stopped", "timed_out"):
            raise ValueError("Unknown source job status")
        for name, suffix in (("stdout_path", ".out"), ("stderr_path", ".err")):
            if record.get(name) != str(source / (identifier + suffix)):
                raise ValueError("Source metadata output path disagrees with selected private jobs directory")
    return raw, before, table["jobs"]


def capture(source_value: Path, destination_value: Path) -> dict:
    source = private_path(source_value, True)
    destination = private_path(destination_value, False)
    if source == destination or source in destination.parents or destination in source.parents:
        raise ValueError("Snapshot destination must be separate from source")
    if destination.exists() or destination.is_symlink():
        raise ValueError("Snapshot destination already exists")
    if not destination.parent.is_dir():
        raise ValueError("Snapshot destination parent must already exist")
    raw, before, records = metadata(source)
    digest = hashlib.sha256(raw).hexdigest()
    stage = Path(tempfile.mkdtemp(prefix=".sp6-jobs-snapshot-", dir=destination.parent))
    published = False
    try:
        outputs = []
        total = 0
        for record in records:
            if not eligible(record) or record.get("output_expired"):
                continue
            identifier = record["id"]
            ws = hashlib.sha256(record["workspace_id"].encode()).hexdigest()[:24]
            for relative in (identifier + ".out", identifier + ".err",
                             f"output/{ws}/{identifier}/stdout.log", f"output/{ws}/{identifier}/stderr.log"):
                path, info = checked_file(source, relative, MAX_FILE)
                total += info["bytes"]
                if total > MAX_TOTAL:
                    raise ValueError("Private snapshot output exceeds total bound")
                target = stage / relative
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                sha = copy_readonly(path, target, info)
                if identity(path) != info:
                    raise SourceChanged(f"Source name changed during copy: {relative}")
                outputs.append({"path": relative, "bytes": info["bytes"], "sha256": sha, "source_identity": info})
        for item in outputs:
            path, current = checked_file(source, item["path"], MAX_FILE)
            if current != item["source_identity"] or hash_readonly(path, current) != item["sha256"]:
                raise SourceChanged(f"Source changed before snapshot publication: {item['path']}")
        if identity(source / "jobs.json") != before or hash_readonly(source / "jobs.json", before) != digest:
            raise SourceChanged("jobs.json changed before snapshot publication")
        fd = os.open(stage / "jobs.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "wb") as handle:
            handle.write(raw)
            handle.flush()
            os.fsync(handle.fileno())
        report = {"schema_version": 1, "source_commit": COMMIT, "captured_at": utc_now(),
                  "private_snapshot": True, "source_dir": str(source), "snapshot_dir": str(destination),
                  "metadata_sha256": digest, "source_records": len(records),
                  "eligible_records": sum(eligible(r) for r in records),
                  "archive_only_records": sum(not eligible(r) for r in records),
                  "files": outputs, "copied_output_bytes": total, "source_process_operations": 0}
        fd = os.open(stage / "capture.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as handle:
            json.dump(report, handle, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        for directory, _, _ in os.walk(stage, topdown=False):
            sync_directory(Path(directory))
        if destination.exists() or destination.is_symlink():
            raise ValueError("Snapshot destination appeared during capture")
        os.rename(stage, destination)
        sync_directory(destination.parent)
        published = True
        return report
    finally:
        if not published:
            shutil.rmtree(stage)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-jobs", type=Path, required=True)
    parser.add_argument("--snapshot", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(capture(args.source_jobs, args.snapshot), indent=2))


if __name__ == "__main__":
    main()
