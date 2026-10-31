#!/usr/bin/env python3
"""Private migration-plan and comparator guards; no live files or processes."""
import copy
import json
from pathlib import Path
import sqlite3
import sys
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import archive_work
import legacy_sqlite_migration as migration
import legacy_stores_proof as proof
import test_legacy_sqlite_migration as fixtures


class LegacyProofTests(unittest.TestCase):
    setUp = fixtures.LegacyCaptureTests.setUp
    tearDown = fixtures.LegacyCaptureTests.tearDown
    fixture = fixtures.LegacyCaptureTests.fixture
    hashes = fixtures.LegacyCaptureTests.hashes
    def prepare_fixture(self):
        manifests = []
        hashes = {}
        for kind in ("usage", "inbox"):
            source = self.fixture(kind)
            hashes[kind] = self.hashes(source)
            destination = self.root / (kind + "-capture")
            migration.capture(kind, source, destination, attempts=1)
            manifests.append(destination / "manifest.json")
        prepared = proof.prepare(*manifests, self.root / "read-proof")
        plan = json.loads(Path(prepared["plan"]).read_text())
        expected = json.loads(Path(plan["expected"]).read_text())
        for kind in ("usage", "inbox"):
            self.assertEqual(hashes[kind], self.hashes(self.original / (kind + ".sqlite")))
        return Path(prepared["plan"]), plan, expected

    def test_reads_expectations_from_only_disposable_copy_and_retains_all_receipts(self):
        real_connect = sqlite3.connect
        def guarded_connect(path, *args, **kwargs):
            self.assertNotIn(str(self.original), str(path))
            self.assertNotIn("/usage-capture/usage.sqlite", str(path))
            self.assertNotIn("/inbox-capture/inbox.sqlite", str(path))
            return real_connect(path, *args, **kwargs)
        # Make fixtures before tracking SQLite calls; fixture writers are the
        # only deliberate original connections in this private test.
        captures = []
        for kind in ("usage", "inbox"):
            source = self.fixture(kind)
            destination = self.root / (kind + "-capture")
            migration.capture(kind, source, destination, attempts=1)
            captures.append(destination / "manifest.json")
        with mock.patch.object(proof.sqlite3, "connect", side_effect=guarded_connect):
            result = proof.prepare(*captures, self.root / "read-proof")
        plan = json.loads(Path(result["plan"]).read_text())
        expected = json.loads(Path(plan["expected"]).read_text())
        self.assertEqual(expected["usage"]["snapshots"][0]["value"]["all_time_count"], 3)
        self.assertEqual(len(expected["usage"]["reset_receipts"]), 1)
        self.assertEqual(len(expected["inbox"]["answer_receipts"]), 1)
        self.assertEqual(expected["inbox"]["answer_receipts"][0]["result"]["answer"]["by"], "fixture_owner")

    def test_comparator_keeps_types_nulls_and_all_dto_fields(self):
        plan_path, plan, expected = self.prepare_fixture()
        result_path = plan_path.parent / "candidate.json"
        result = {"schema_version": 1, "clock": plan["clock"], **copy.deepcopy(expected)}
        result["usage"]["snapshots"][0]["value"]["settings"].pop("pending_reset")
        archive_work.write_json(result_path, result)
        summary = proof.verify(plan_path, result_path, plan_path.parent / "report.json")
        self.assertFalse(summary["passed"])
        self.assertFalse(summary["stores"]["usage"]["read_dtos_match"])
        self.assertTrue(summary["stores"]["inbox"]["read_dtos_match"])

    def test_changed_logical_rows_fail_even_if_dto_output_was_fabricated_equal(self):
        plan_path, plan, expected = self.prepare_fixture()
        result_path = plan_path.parent / "candidate.json"
        archive_work.write_json(result_path, {"schema_version": 1, "clock": plan["clock"], **expected})
        with sqlite3.connect(plan["stores"]["usage"]["database"]) as db:
            db.execute("DELETE FROM usage_requests")
        with self.assertRaisesRegex(ValueError, "logical rows"):
            proof.verify(plan_path, result_path, plan_path.parent / "report.json")


if __name__ == "__main__":
    unittest.main()
