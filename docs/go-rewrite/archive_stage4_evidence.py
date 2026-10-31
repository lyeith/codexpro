#!/usr/bin/env python3
"""Archive only SP-6 private proof artifacts under Projects, without following links."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import stat
import tarfile
import time


def sha_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def signature(info):
    return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True)
    parser.add_argument("--destination", required=True)
    parser.add_argument("--manifest", required=True)
    args = parser.parse_args()
    source = Path(args.source).expanduser()
    destination = Path(args.destination).expanduser()
    manifest = Path(args.manifest).expanduser()
    projects = Path.home() / "Projects"
    assert source == Path.home() / ".codexpro-go-proof"
    assert source.is_dir() and not source.is_symlink()
    for path in (destination, manifest):
        assert path.is_absolute() and path.is_relative_to(projects)
        assert path.parent.resolve() == path.parent
        assert not path.exists() and not path.is_symlink()
    started = datetime.datetime.now(datetime.timezone.utc).isoformat()
    started_mono = time.monotonic()
    inventory = []
    total = 0
    for root, directories, files in os.walk(source, followlinks=False):
        directories.sort()
        files.sort()
        for name in directories + files:
            path = Path(root) / name
            info = path.lstat()
            record = {"path": str(path.relative_to(source)), "mode": stat.S_IFMT(info.st_mode),
                      "signature": signature(info)}
            if stat.S_ISREG(info.st_mode):
                total += info.st_size
            inventory.append(record)
            assert len(inventory) <= 50000 and total <= 3 * 1024 ** 3
    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with destination.open("xb") as output:
        os.chmod(destination, 0o600)
        with tarfile.open(fileobj=output, mode="w", dereference=False) as archive:
            archive.add(source, arcname="private-proof", recursive=True)
        output.flush()
        os.fsync(output.fileno())
    verified = []
    with tarfile.open(destination, "r") as archive:
        members = {member.name: member for member in archive.getmembers()}
        for record in inventory:
            path = source / record["path"]
            assert signature(path.lstat()) == tuple(record["signature"]), "source changed during archive"
            member = members.get("private-proof/" + record["path"])
            if record["mode"] == stat.S_IFREG:
                assert member is not None and (member.isfile() or member.islnk())
                archive_hash = hashlib.sha256()
                with archive.extractfile(member) as handle:
                    for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                        archive_hash.update(chunk)
                digest = sha_file(path)
                assert digest == archive_hash.hexdigest(), "archive file differs from source"
                assert signature(path.lstat()) == tuple(record["signature"])
                verified.append({"path": record["path"], "bytes": path.stat().st_size, "sha256": digest})
            elif record["mode"] == stat.S_IFLNK:
                assert member is not None and member.issym() and member.linkname == os.readlink(path)
            elif record["mode"] == stat.S_IFDIR:
                assert member is not None and member.isdir()
            elif record["mode"] == stat.S_IFSOCK:
                assert member is None, "socket unexpectedly archived"
            else:
                assert member is not None, "special metadata omitted"
    report = {"schema_version": 1, "kind": "private-stage4-evidence-archive",
              "started_at": started, "finished_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "elapsed_seconds": time.monotonic() - started_mono,
              "source": str(source), "archive": str(destination), "archive_bytes": destination.stat().st_size,
              "archive_sha256": sha_file(destination), "source_regular_bytes": total,
              "verified_regular_files": len(verified), "source_entries": len(inventory),
              "symlinks_followed": False, "regular_file_contents_verified": True,
              "source_identity_changes": 0, "live_state_accessed": False,
              "private_credentials_may_exist": True, "publication": "private archive; do not commit raw contents",
              "files": verified}
    with manifest.open("x") as handle:
        json.dump(report, handle, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    print(json.dumps({key: report[key] for key in
                      ("archive", "archive_bytes", "archive_sha256", "verified_regular_files", "elapsed_seconds")}))


if __name__ == "__main__":
    main()
