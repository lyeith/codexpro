#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import legacy_jobs_snapshot as capture


class PrivateJobsCaptureTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sp6-private-jobs-")
        self.base = Path(self.tmp.name)
        self.source = self.base / "original"
        self.source.mkdir()
        self.output = self.base / "snapshot"

    def tearDown(self):
        self.tmp.cleanup()

    def fixture(self):
        record = {"id": "job_1234abcd", "workspace_id": "test", "status": "succeeded", "quiescent": True,
                  "runner_version": 2, "stdout_path": str(self.source / "job_1234abcd.out"),
                  "stderr_path": str(self.source / "job_1234abcd.err")}
        (self.source / "jobs.json").write_text(json.dumps({"version": 1, "jobs": [record]}))
        (self.source / "job_1234abcd.out").write_bytes(b"raw\x00bytes")
        (self.source / "job_1234abcd.err").write_bytes(b"")
        rendered = self.source / "output" / hashlib.sha256(b"test").hexdigest()[:24] / "job_1234abcd"
        rendered.mkdir(parents=True)
        (rendered / "stdout.log").write_text("rendered α\n")
        (rendered / "stderr.log").write_bytes(b"")
        return record

    def test_exact_copy_and_original_mtime_preserved(self):
        self.fixture()
        files = {str(p.relative_to(self.source)): (p.read_bytes(), p.stat().st_mtime_ns)
                 for p in self.source.rglob("*") if p.is_file()}
        report = capture.capture(self.source, self.output)
        self.assertEqual(report["eligible_records"], 1)
        self.assertEqual(report["source_process_operations"], 0)
        for relative, (data, mtime) in files.items():
            self.assertEqual((self.output / relative).read_bytes(), data)
            self.assertEqual((self.source / relative).read_bytes(), data)
            self.assertEqual((self.source / relative).stat().st_mtime_ns, mtime)

    def test_active_ownership_is_metadata_only(self):
        record = self.fixture()
        record["status"] = "running"
        record["quiescent"] = False
        (self.source / "jobs.json").write_text(json.dumps({"version": 1, "jobs": [record]}))
        report = capture.capture(self.source, self.output)
        self.assertEqual(report["archive_only_records"], 1)
        self.assertEqual(report["files"], [])
        self.assertFalse((self.output / "job_1234abcd.out").exists())
        self.assertEqual(json.loads((self.output / "jobs.json").read_text())["jobs"], [record])

    def test_changed_source_never_publishes(self):
        self.fixture()
        original = capture.copy_readonly
        changed = False
        def copy(*args, **kwargs):
            nonlocal changed
            value = original(*args, **kwargs)
            if not changed:
                changed = True
                (self.source / "job_1234abcd.out").write_bytes(b"changed")
            return value
        with mock.patch.object(capture, "copy_readonly", side_effect=copy), self.assertRaises(capture.SourceChanged):
            capture.capture(self.source, self.output)
        self.assertFalse(self.output.exists())
        self.assertFalse(list(self.base.glob(".sp6-jobs-snapshot-*")))

    def test_symlinks_hardlinks_and_foreign_record_paths_refused(self):
        record = self.fixture()
        raw = self.source / "job_1234abcd.out"
        os.link(raw, self.base / "hardlink")
        with self.assertRaises(ValueError):
            capture.capture(self.source, self.output)
        (self.base / "hardlink").unlink()
        raw.unlink()
        raw.symlink_to(self.source / "job_1234abcd.err")
        with self.assertRaises(ValueError):
            capture.capture(self.source, self.output)
        raw.unlink()
        raw.write_text("safe")
        record["stdout_path"] = "/tmp/unrelated.out"
        (self.source / "jobs.json").write_text(json.dumps({"version": 1, "jobs": [record]}))
        with self.assertRaises(ValueError):
            capture.capture(self.source, self.output)

    def test_existing_or_source_nested_destination_refused(self):
        self.fixture()
        self.output.mkdir()
        (self.output / "keep").write_text("untouched")
        for destination in (self.output, self.source / "nested"):
            with self.assertRaises(ValueError):
                capture.capture(self.source, destination)
        self.assertEqual((self.output / "keep").read_text(), "untouched")


if __name__ == "__main__":
    unittest.main()
