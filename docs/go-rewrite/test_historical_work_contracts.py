#!/usr/bin/env python3
"""Private archive/normalization guards, independent of the installed release."""
import copy
import json
from pathlib import Path
import sqlite3
import unittest
from unittest import mock

import archive_work
import archive_work_test as archive_tests
import historical_work_contracts as lane


class HistoricalArchiveContractTests(unittest.TestCase):
    def setUp(self):
        self.fixture = archive_tests.ArchiveCaptureTests(
            "test_preserves_wal_all_revisions_and_ready_state_without_source_sqlite")
        self.fixture.setUp()
        self.archive = self.fixture.root / "published-archive"
        archive_work.capture(self.fixture.source, self.archive, attempts=1)

    def tearDown(self):
        self.fixture.tearDown()

    def test_prepare_preserves_every_run_and_revision_and_never_opens_original_sqlite(self):
        original = self.fixture.original_hashes()
        archive_hash = archive_work.hash_readonly(self.archive / "work.sqlite")
        real_connect = sqlite3.connect
        opened = []
        def tracking(path, *args, **kwargs):
            opened.append(str(path))
            self.assertNotIn(str(self.fixture.source), str(path))
            self.assertNotIn(str(self.archive / "work.sqlite"), str(path))
            self.assertIn("mode=ro&immutable=1", str(path))
            return real_connect(path, *args, **kwargs)
        destination = self.fixture.root / "contract-private"
        with mock.patch.object(lane.sqlite3, "connect", side_effect=tracking):
            result = lane.prepare(self.archive / "manifest.json", destination)
        self.assertTrue(opened)
        self.assertEqual(result["work_runs"], 9)
        self.assertEqual(result["document_revisions_preserved"], 18)
        self.assertEqual(result["document_revisions_sampled"], 18)
        plan = json.loads((destination / "plan.json").read_text())
        self.assertEqual(plan["coverage"]["run_states"], {"blocked": 8, "ready": 1})
        self.assertEqual(plan["preserved_table_counts"]["documents"], 18)
        self.assertEqual(plan["preserved_table_counts"]["sessions"], 1)
        cases = lane.read_jsonl(destination / "cases.jsonl")
        self.assertLessEqual(len(cases), 1024)
        self.assertTrue(any(case["expected_error"] and case["case_id"] == "outsider-exact-id" for case in cases))
        read_revisions = {(case["arguments"].get("document_id"), case["arguments"].get("document_revision"))
                          for case in cases if case["arguments"].get("action") == "read_document"
                          and "document_revision" in case["arguments"]}
        self.assertEqual(len(read_revisions), 18)
        self.assertEqual(self.fixture.original_hashes(), original)
        self.assertEqual(archive_work.hash_readonly(self.archive / "work.sqlite"), archive_hash)
        self.assertEqual(archive_work.hash_readonly(Path(plan["archive"]["path"])), archive_hash)
        self.assertEqual(Path(plan["archive"]["path"]).stat().st_mode & 0o777, 0o400)
        self.assertFalse(Path(plan["archive"]["path"] + "-wal").exists())

    def test_prepare_refuses_existing_destination_and_published_archive_subdirectory(self):
        destination = self.fixture.root / "existing"
        destination.mkdir()
        sentinel = destination / "keep"
        sentinel.write_text("retained")
        for target in (destination, self.archive / "nested"):
            with self.subTest(target=target), self.assertRaises(ValueError):
                lane.prepare(self.archive / "manifest.json", target)
        self.assertEqual(sentinel.read_text(), "retained")

    def test_comparison_decodes_only_json_text_and_keeps_all_metadata_and_types(self):
        left = {"case_id": "case", "parsed_arguments": {"limit": 3}, "result": {
            "_meta": {}, "content": [{"type": "text", "text": '{"a":1,"b":2}'}],
            "structuredContent": {"a": 1, "b": 2, "return_size": {"total_bytes": 17, "returned_bytes": 17, "truncated": False}}}}
        right = copy.deepcopy(left)
        right["result"]["content"][0]["text"] = '{ "b": 2, "a": 1 }'
        self.assertEqual(lane.canonical(lane.comparable(left)), lane.canonical(lane.comparable(right)))
        right["result"]["structuredContent"]["return_size"]["returned_bytes"] = 18
        self.assertNotEqual(lane.canonical(lane.comparable(left)), lane.canonical(lane.comparable(right)))
        right = copy.deepcopy(left)
        right["result"]["structuredContent"]["a"] = True
        self.assertNotEqual(lane.canonical(lane.comparable(left)), lane.canonical(lane.comparable(right)))
        right = copy.deepcopy(left)
        right["result"]["structuredContent"]["new_null"] = None
        self.assertNotEqual(lane.canonical(lane.comparable(left)), lane.canonical(lane.comparable(right)))

    def test_live_paths_are_rejected_without_opening_sqlite(self):
        with mock.patch.object(lane.sqlite3, "connect") as connect:
            with self.assertRaises(ValueError):
                lane.private_path(Path.home() / ".codexpro" / "work" / "work.sqlite", exists=False)
            connect.assert_not_called()


if __name__ == "__main__":
    unittest.main()
