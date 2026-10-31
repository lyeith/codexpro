#!/usr/bin/env python3
"""Read-only capture of an explicit catalog and FileLeaseStore into a new bundle.

No reference manager is instantiated: that would initialize/chmod/quarantine,
upgrade v1 documents and reconcile states. This script only reads source files.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import stat
import tempfile
import time

MAX_FILE = 1024 * 1024
MAX_TOTAL = 64 * 1024 * 1024
MAX_ENTRIES = 10000

def within(child: Path, parent: Path) -> bool:
    return child == parent or parent in child.parents

def private(path: Path) -> Path:
    path = path.expanduser().resolve()
    homes = {Path.home(), Path(pwd.getpwuid(os.getuid()).pw_dir)}
    for home in homes:
        for forbidden in (home / ".codexpro", home / ".config" / "codexpro"):
            if within(path, forbidden) or within(path, forbidden.resolve()):
                raise ValueError("destination overlaps live state")
    return path

def identity(info) -> tuple:
    return (info.st_dev, info.st_ino, info.st_mode, info.st_nlink,
            info.st_size, info.st_mtime_ns, info.st_ctime_ns)

def read_stable(path: Path, limit: int = MAX_FILE):
    descriptor = os.open(path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
    try:
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
            raise ValueError("source is not a bounded regular file: " + str(path))
        chunks = []; length = 0
        while True:
            part = os.read(descriptor, min(65536, limit + 1 - length))
            if not part:
                break
            chunks.append(part); length += len(part)
            if length > limit:
                raise ValueError("source grew beyond its limit: " + str(path))
        raw = b"".join(chunks)
        after = os.fstat(descriptor)
        if identity(before) != identity(after) or identity(after) != identity(path.lstat()):
            raise ValueError("source changed during read: " + str(path))
        return raw, after
    finally:
        os.close(descriptor)

def inventory(directory: Path):
    entries = []
    with os.scandir(directory) as stream:
        for item in stream:
            if len(entries) >= MAX_ENTRIES:
                raise ValueError("lease directory exceeds entry bound")
            info = item.stat(follow_symlinks=False)
            entries.append((item.name, stat.S_IFMT(info.st_mode), info.st_ino))
    return sorted(entries)

def write_new(path: Path, raw: bytes):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        offset = 0
        while offset < len(raw):
            offset += os.write(descriptor, raw[offset:])
        os.fsync(descriptor)
    finally:
        os.close(descriptor)

def capture(catalog: Path, managed: Path, destination: Path, archive: Path):
    catalog = catalog.expanduser().absolute()
    managed = managed.expanduser().resolve(strict=True)
    leases = managed / "leases"
    if leases.is_symlink() or not leases.is_dir():
        raise ValueError("lease store must be an existing nonsymlink directory")
    destination = private(destination)
    archive = private(archive)
    for source in (managed, catalog.parent.resolve()):
        if within(destination, source) or within(source, destination):
            raise ValueError("snapshot destination overlaps source")
        if within(archive, source) or within(source, archive):
            raise ValueError("archive destination overlaps source")
    if within(destination, archive) or within(archive, destination):
        raise ValueError("snapshot and archive destinations overlap")
    if destination.exists() or destination.is_symlink():
        raise ValueError("snapshot destination must be new")
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = Path(tempfile.mkdtemp(prefix=".sp6-workspace-capture-", dir=destination.parent))
    sources = []; ignored = []; total = 0
    before_inventory = inventory(leases)
    try:
        (temporary / "worktrees" / "leases").mkdir(parents=True,mode=0o700)
        paths = [(catalog, "catalog.json", 1_000_000)]
        connector = managed / "connector-id"
        if connector.exists() or connector.is_symlink():
            paths.append((connector, "worktrees/connector-id", 4096))
        for name, mode, inode in before_inventory:
            if stat.S_ISREG(mode) and name.endswith(".json"):
                paths.append((leases / name, "worktrees/leases/" + name, MAX_FILE))
            else:
                ignored.append({"path": str(leases / name), "reason": "ignored by FileLeaseStore", "mode": mode})
        for source, relative, limit in paths:
            raw, info = read_stable(source, limit)
            total += len(raw)
            if total > MAX_TOTAL:
                raise ValueError("source snapshot exceeds total byte bound")
            write_new(temporary / relative, raw)
            sources.append({"source_path": str(source), "snapshot_path": relative,
                            "sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw),
                            "mtime_ns": info.st_mtime_ns, "mode": stat.S_IMODE(info.st_mode),
                            "_identity": identity(info)})
        for record in sources:
            raw, info = read_stable(Path(record["source_path"]))
            if identity(info) != record["_identity"] or hashlib.sha256(raw).hexdigest() != record["sha256"]:
                raise ValueError("source changed during capture")
        if inventory(leases) != before_inventory:
            raise ValueError("lease directory changed during capture")
        if not any(item[1] == "worktrees/connector-id" for item in paths) and (connector.exists() or connector.is_symlink()):
            raise ValueError("connector identity appeared during capture")
        for record in sources:
            del record["_identity"]
        manifest = {
            "schema_version": 1, "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "source_catalog": str(catalog), "source_managed_root": str(managed),
            "snapshot_root": str(destination), "source_unchanged_verified": True,
            "reference_manager_instantiated": False, "files": sources, "ignored_source_entries": ignored,
        }
        manifest_raw = (json.dumps(manifest, indent=2) + "\n").encode()
        write_new(temporary / "capture-manifest.json", manifest_raw)
        plan = {
            "schema_version": 1, "snapshot_root": str(destination), "catalog_file": "catalog.json",
            "managed_root": "worktrees", "destination": str(archive),
            "capture_manifest_sha256": hashlib.sha256(manifest_raw).hexdigest(),
            "files": [{"path": record["snapshot_path"], "sha256": record["sha256"], "bytes": record["bytes"]} for record in sources],
        }
        write_new(temporary / "probe-plan.json", (json.dumps(plan, indent=2) + "\n").encode())
        os.rename(temporary, destination)
        directory_fd = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
        return {"snapshot": str(destination), "probe_plan": str(destination / "probe-plan.json"),
                "files": len(sources), "leases": sum(record["snapshot_path"].startswith("worktrees/leases/") for record in sources),
                "ignored_source_entries": len(ignored), "bytes": total,
                "capture_manifest_sha256": plan["capture_manifest_sha256"]}
    except Exception:
        shutil.rmtree(temporary)
        raise

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog-file", required=True, type=Path)
    parser.add_argument("--managed-root", required=True, type=Path)
    parser.add_argument("--snapshot-root", required=True, type=Path)
    parser.add_argument("--archive-dir", required=True, type=Path)
    args = parser.parse_args()
    result = capture(args.catalog_file, args.managed_root, args.snapshot_root, args.archive_dir)
    print(json.dumps(result, indent=2))

if __name__ == "__main__":
    main()
