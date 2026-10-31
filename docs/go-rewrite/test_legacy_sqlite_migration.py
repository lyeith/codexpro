#!/usr/bin/env python3
"""Capture guards exercised only with disposable fixture databases."""
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest
from unittest import mock

# In the repository both helpers are colocated; local staging keeps the earlier
# work helper one directory above this new telemetry helper.
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import archive_work
import legacy_sqlite_migration as migration


class LegacyCaptureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="sp6-legacy-store-test-")
        self.root = Path(self.temporary.name)
        self.original = self.root / "original"
        self.original.mkdir()
        self.connections = []

    def tearDown(self):
        for connection in self.connections:
            connection.close()
        self.temporary.cleanup()

    def fixture(self, kind):
        source = self.original / (kind + ".sqlite")
        db = sqlite3.connect(source)
        self.connections.append(db)
        db.execute("PRAGMA journal_mode=WAL")
        db.execute("PRAGMA wal_autocheckpoint=0")
        if kind == "usage":
            db.executescript("""
              CREATE TABLE pro_submissions(query_id TEXT NOT NULL,turn_id TEXT NOT NULL,project TEXT,submitted_at TEXT NOT NULL,submitted_ms INTEGER NOT NULL,mode_evidence TEXT NOT NULL,PRIMARY KEY(query_id,turn_id));
              CREATE INDEX pro_submissions_time ON pro_submissions(submitted_ms,project);
              CREATE TABLE usage_settings(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);
              CREATE TABLE usage_collectors(id TEXT PRIMARY KEY,body TEXT NOT NULL);
              CREATE TABLE usage_changes(sequence INTEGER PRIMARY KEY AUTOINCREMENT,body TEXT NOT NULL);
              CREATE TABLE usage_requests(id TEXT PRIMARY KEY,payload TEXT NOT NULL,result TEXT NOT NULL);
            """)
            settings = {"revision": 7, "weekly_anchor_at": None, "manual_reset_at": None,
                        "pending_reset": None, "limits_checked_at": None,
                        "limits_verified_at": None, "limits_error": None}
            db.execute("INSERT INTO usage_settings VALUES(1,?)", (json.dumps(settings),))
            for index in range(3):
                db.execute("INSERT INTO pro_submissions VALUES(?,?,?,?,?,?)",
                           ("query" + str(index), "turn", "plans" if index else None,
                            "2026-10-31T12:00:00.000Z", 1793448000000, "selected"))
                db.execute("INSERT INTO usage_changes(body) VALUES(?)", (json.dumps({"fixture": index}),))
            reset = {"schema_version": 1, "request_id": "reset-fixture", "expected_revision": 6}
            db.execute("INSERT INTO usage_requests VALUES(?,?,?)", (reset["request_id"], json.dumps(reset, separators=(",", ":")), json.dumps(settings)))
        else:
            db.executescript("""
              CREATE TABLE inbox_items(project TEXT NOT NULL,id TEXT NOT NULL,status TEXT NOT NULL,updated TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(project,id));
              CREATE TABLE inbox_events(sequence INTEGER PRIMARY KEY AUTOINCREMENT,project TEXT NOT NULL,id TEXT NOT NULL,body TEXT NOT NULL);
              CREATE TABLE inbox_answers(project TEXT NOT NULL,id TEXT NOT NULL,request TEXT NOT NULL,payload TEXT NOT NULL,result TEXT NOT NULL,PRIMARY KEY(project,id,request));
            """)
            item = {"schema_version": 1, "id": "question", "project_id": "plans", "source": "fixture",
                    "title": "Migration fixture", "question": "Keep existing receipt?", "context": "Archived proof",
                    "options": ["yes", "no"], "recommendation": "yes", "blocking_scope": "task", "blocked_work": [],
                    "status": "answered", "revision": 2, "created_at": "2026-10-31T11:00:00.000Z",
                    "updated_at": "2026-10-31T12:00:00.000Z", "deliveries": [],
                    "answer": {"text": "yes", "at": "2026-10-31T12:00:00.000Z", "by": "fixture_owner", "request_id": "answer-fixture"}}
            answer = {"schema_version": 1, "request_id": "answer-fixture", "expected_revision": 1, "answer": "yes"}
            db.execute("INSERT INTO inbox_items VALUES(?,?,?,?,?)", ("plans", "question", "answered", item["updated_at"], json.dumps(item)))
            db.execute("INSERT INTO inbox_events(project,id,body) VALUES('plans','question',?)", (json.dumps({"kind": "question.answered", "at": item["updated_at"]}),))
            db.execute("INSERT INTO inbox_answers VALUES(?,?,?,?,?)", ("plans", "question", answer["request_id"], json.dumps(answer, separators=(",", ":")), json.dumps(item)))
        db.commit()
        return source

    def hashes(self, source):
        return {suffix: archive_work.hash_readonly(Path(str(source) + suffix)) for suffix in ("", "-wal", "-shm")}

    def test_both_stores_preserve_schema_rows_receipts_and_original_shm(self):
        for kind in ("usage", "inbox"):
            with self.subTest(kind=kind):
                source = self.fixture(kind)
                before = self.hashes(source)
                real_connect = sqlite3.connect
                opened = []
                def connect(path, *args, **kwargs):
                    self.assertNotIn(str(self.original), str(path))
                    opened.append(str(path))
                    return real_connect(path, *args, **kwargs)
                with mock.patch.object(migration.sqlite3, "connect", side_effect=connect):
                    result = migration.capture(kind, source, self.root / (kind + "-archive"), attempts=1)
                self.assertTrue(opened)
                self.assertEqual(before, self.hashes(source))
                self.assertEqual(result["tables"]["usage_requests" if kind == "usage" else "inbox_answers"], 1)
                manifest = json.loads((Path(result["destination"]) / "manifest.json").read_text())
                self.assertEqual(manifest["source_sqlite_connections"], 0)
                self.assertTrue(manifest["normalized_archive"]["table_digests_match_raw_snapshot"])
                self.assertEqual(manifest["normalized_archive"]["journal_mode"], "delete")
                self.assertFalse(Path(result["destination"], kind + ".sqlite-wal").exists())

    def test_changed_source_never_publishes_an_archive(self):
        source = self.fixture("usage")
        original = migration.copy_readonly
        changed = False
        def copy(*args, **kwargs):
            nonlocal changed
            result = original(*args, **kwargs)
            if not changed:
                changed = True
                db = self.connections[0]
                db.execute("INSERT INTO usage_changes(body) VALUES('{}')")
                db.commit()
            return result
        with mock.patch.object(migration, "copy_readonly", side_effect=copy):
            with self.assertRaises(archive_work.SourceChanged):
                migration.capture("usage", source, self.root / "changed", attempts=1)
        self.assertFalse((self.root / "changed").exists())
        self.assertFalse(list(self.root.glob(".sp6-usage-capture-*")))

    def test_existing_original_and_live_destinations_fail_before_sqlite(self):
        source = self.fixture("usage")
        existing = self.root / "existing"
        existing.mkdir()
        (existing / "keep").write_text("unchanged")
        for destination in (existing, self.original / "bad", Path.home() / ".codexpro" / "sp6-forbidden"):
            with self.subTest(path=str(destination)), mock.patch.object(migration.sqlite3, "connect") as connect:
                with self.assertRaises(ValueError):
                    migration.capture("usage", source, destination, attempts=1)
                connect.assert_not_called()
        self.assertEqual((existing / "keep").read_text(), "unchanged")


if __name__ == "__main__":
    unittest.main()
