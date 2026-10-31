#!/usr/bin/env python3
"""Capture private TS history, generate an exact-release oracle, run native Go proof.

Does not start/stop either server and does not open any live source/control path.
The selected TS directory must already belong to a private test instance.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from legacy_jobs_snapshot import capture, private_path, sync_directory, utc_now


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-jobs", type=Path, required=True)
    parser.add_argument("--release", type=Path, required=True)
    parser.add_argument("--go-dir", type=Path, required=True)
    parser.add_argument("--state-parent", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--retention-ms", type=int, default=86400000)
    args = parser.parse_args()
    source = private_path(args.source_jobs, True)
    parent = private_path(args.state_parent, True)
    go_dir = private_path(args.go_dir, True)
    output = private_path(args.output, False)
    release = args.release.resolve(strict=True)
    if not (go_dir / "go.mod").is_file() or not (release / "dist/jobOutput.js").is_file():
        parser.error("Go module and immutable TypeScript release are required")
    if source == parent or source in parent.parents or parent in source.parents:
        parser.error("Proof state parent must be separate from source")
    if output.exists() or output.is_symlink() or output == source or source in output.parents:
        parser.error("Proof result must be fresh and outside source")
    if not 1 <= args.retention_ms <= 31536000000:
        parser.error("Invalid retention")
    root = Path(tempfile.mkdtemp(prefix="sp6-legacy-jobs-", dir=parent))
    report = {"schema_version": 1, "started_at": utc_now(), "private_root": str(root),
              "source_jobs": str(source), "passed": False}
    failure = None
    try:
        snapshot = root / "snapshot"
        captured = capture(source, snapshot)
        report["capture"] = captured
        oracle = root / "source-oracle.json"
        command = ["node", str(Path(__file__).with_name("legacy_jobs_oracle.mjs")),
                   "--release", str(release), "--snapshot", str(snapshot),
                   "--original-jobs-dir", str(source), "--metadata-sha256", captured["metadata_sha256"],
                   "--retention-ms", str(args.retention_ms), "--output", str(oracle)]
        reference = subprocess.run(command, text=True, capture_output=True, timeout=45, check=False)
        report["reference_exit"] = reference.returncode
        report["reference_stdout"] = reference.stdout[-8192:]
        report["reference_stderr"] = reference.stderr[-8192:]
        if reference.returncode:
            raise RuntimeError("Exact-release output oracle failed")
        result = root / "projection.json"
        env = dict(os.environ)
        env.update({"GOMAXPROCS": "2", "SP6_LEGACY_JOBS_ORACLE": str(oracle),
                    "SP6_LEGACY_JOBS_ORACLE_SHA256": hashlib.sha256(oracle.read_bytes()).hexdigest(),
                    "SP6_LEGACY_JOBS_STATE": str(root / "go-state"),
                    "SP6_LEGACY_JOBS_RESULT": str(result), "CODEXPRO_JOB_SCOPES": "0"})
        candidate = subprocess.run(["go", "test", "./internal/jobs", "-run", "^TestLegacyJobMigrationSnapshotProof$",
                                    "-count=1", "-timeout=110s", "-v"], cwd=go_dir, env=env,
                                   text=True, capture_output=True, timeout=125, check=False)
        report["candidate_exit"] = candidate.returncode
        report["candidate_stdout"] = candidate.stdout[-16384:]
        report["candidate_stderr"] = candidate.stderr[-8192:]
        if result.is_file():
            report["projection"] = json.loads(result.read_text())
        if candidate.returncode or report.get("projection", {}).get("passed") is not True:
            raise RuntimeError("Native job migration/projection proof failed")
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error)
        failure = error
    finally:
        report["finished_at"] = utc_now()
        fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as handle:
            json.dump(report, handle, indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        sync_directory(output.parent)
        print(json.dumps({"passed": report["passed"], "evidence": str(output), "private_root": str(root)}))
    if failure:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
