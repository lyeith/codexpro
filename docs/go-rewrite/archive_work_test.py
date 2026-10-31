#!/usr/bin/env python3
"""Private fixtures only; no live paths are opened by these tests."""
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

import archive_work


class ArchiveCaptureTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="sp6-archive-test-")
        self.root = Path(self.temporary.name)
        live = self.root / "fixture-original"
        live.mkdir()
        self.source = live / "work.sqlite"
        self.writer = sqlite3.connect(self.source)
        self.writer.execute("PRAGMA journal_mode=WAL")
        self.writer.execute("PRAGMA wal_autocheckpoint=0")
        self.writer.executescript("""
          CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
          INSERT INTO metadata VALUES('schema','3');
          INSERT INTO metadata VALUES('store_id','fixture-original-store');
          CREATE TABLE runs(id TEXT PRIMARY KEY,principal TEXT,project TEXT,state TEXT,revision INTEGER,body TEXT);
          CREATE TABLE iterations(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
          CREATE TABLE operations(id TEXT PRIMARY KEY,run_id TEXT,operation_key TEXT,body TEXT);
          CREATE TABLE sessions(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
          CREATE TABLE documents(id TEXT,run_id TEXT,revision INTEGER,kind TEXT,title TEXT,content TEXT,body TEXT,PRIMARY KEY(id,revision));
          CREATE TABLE events(sequence INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT,kind TEXT,at TEXT,body TEXT);
          CREATE TABLE requests(principal TEXT,request_key TEXT,fingerprint TEXT,result TEXT,PRIMARY KEY(principal,request_key));
          CREATE TABLE work_jobs(id TEXT PRIMARY KEY,run_id TEXT,body TEXT);
        """)
        for index in range(9):
            run_id = f"run_fixture_{index}"
            state = "ready" if index == 8 else "blocked"
            project = "anima" if index == 8 else ("ibkr" if index == 7 else "plans")
            body = json.dumps({
                "id": run_id, "principal_id": "connector_archive",
                "project_id": project, "state": state,
                "mode": "ralph" if index < 7 else "manual",
                "revision": 4, "iteration_id": "historical-claim" if index == 0 else None,
            }, separators=(",", ":"))
            self.writer.execute("INSERT INTO runs VALUES(?,?,?,?,?,?)",
                                (run_id, "connector_archive", project, state, 4, body))
            for revision in (1, 2):
                content = f"run {index} revision {revision} 😀\n"
                doc = {"id": f"doc_{index}", "run_id": run_id, "revision": revision,
                       "kind": "spec", "title": "Specification", "content": content}
                self.writer.execute("INSERT INTO documents VALUES(?,?,?,?,?,?,?)",
                                    (doc["id"], run_id, revision, "spec", "Specification",
                                     content, json.dumps(doc)))
            self.writer.execute("INSERT INTO events(run_id,kind,at,body) VALUES(?,?,?,?)",
                                (run_id, "historical.event", "2026-10-31T00:00:00Z", "{}"))
        self.writer.execute("INSERT INTO sessions VALUES(?,?,?)",
                            ("session", "run_fixture_0", '{"token_hash":"private-fixture-hash"}'))
        self.writer.execute("INSERT INTO operations VALUES(?,?,?,?)",
                            ("operation", "run_fixture_0", "key", '{"state":"unknown"}'))
        self.writer.execute("INSERT INTO work_jobs VALUES(?,?,?)",
                            ("job", "run_fixture_8", '{"status":"running","historical":true}'))
        self.writer.execute("INSERT INTO requests VALUES(?,?,?,?)",
                            ("connector_archive", "request", "fingerprint", '{"receipt":1}'))
        self.writer.commit()

    def tearDown(self):
        self.writer.close()
        self.temporary.cleanup()

    def original_hashes(self):
        return {suffix: archive_work.hash_readonly(Path(str(self.source) + suffix))
                for suffix in ("", "-wal", "-shm")}

    def test_preserves_wal_all_revisions_and_ready_state_without_source_sqlite(self):
        before = self.original_hashes()
        destination = self.root / "archive"
        real_connect = sqlite3.connect
        opened = []

        def tracking_connect(path, *args, **kwargs):
            opened.append(str(path))
            self.assertNotIn(str(self.source.parent), str(path))
            return real_connect(path, *args, **kwargs)

        with mock.patch.object(archive_work.sqlite3, "connect", side_effect=tracking_connect):
            result = archive_work.capture(self.source, destination,
                                          {"connector_private": "connector_archive"}, attempts=1)
        self.assertTrue(opened)
        self.assertEqual(before, self.original_hashes())
        self.assertEqual(result["runs"], 9)
        self.assertEqual(result["documents"], 18)
        self.assertEqual(result["document_ids"], 9)
        manifest = json.loads((destination / "manifest.json").read_text())
        self.assertEqual(manifest["source_sqlite_connections"], 0)
        self.assertTrue(manifest["normalized_archive"]["table_digests_match_raw_snapshot"])
        self.assertEqual(manifest["principal_map"], {"connector_private": "connector_archive"})
        self.assertEqual([run["state"] for run in manifest["inventory"]["runs"]].count("ready"), 1)
        self.assertEqual(manifest["inventory"]["tables"]["sessions"]["rows"], 1)
        with real_connect(archive_work.sqlite_uri(destination / "work.sqlite",
                                                "mode=ro&immutable=1"), uri=True) as connection:
            self.assertEqual(connection.execute("SELECT count(*) FROM documents").fetchone()[0], 18)
            self.assertEqual(connection.execute(
                "SELECT state FROM runs WHERE project='anima'").fetchone()[0], "ready")
            self.assertEqual(connection.execute("PRAGMA integrity_check").fetchone()[0], "ok")
            self.assertEqual(connection.execute("PRAGMA journal_mode").fetchone()[0], "delete")
        self.assertFalse(Path(str(destination / "work.sqlite") + "-wal").exists())

    def test_rejects_a_changing_source_and_removes_only_its_private_attempt(self):
        original = archive_work.copy_readonly
        changed = False

        def changing_copy(source, destination, expected):
            nonlocal changed
            result = original(source, destination, expected)
            if not changed:
                changed = True
                info = self.source.stat()
                os.utime(self.source, ns=(info.st_atime_ns, info.st_mtime_ns + 1_000_000))
            return result

        with mock.patch.object(archive_work, "copy_readonly", side_effect=changing_copy):
            with self.assertRaises(archive_work.SourceChanged):
                archive_work.capture(self.source, self.root / "rejected", attempts=1)
        self.assertFalse((self.root / "rejected").exists())
        self.assertFalse(list(self.root.glob(".sp6-work-archive-*")))
        self.assertTrue(self.source.exists())

    def test_mapping_is_explicit_and_existing_destination_is_preserved(self):
        before = self.original_hashes()
        with self.assertRaises(ValueError):
            archive_work.capture(self.source, self.root / "invalid-map",
                                 {"new": "not-an-archived-principal"}, attempts=1)
        self.assertEqual(before, self.original_hashes())
        destination = self.root / "existing"
        destination.mkdir()
        marker = destination / "keep"
        marker.write_text("unchanged")
        with self.assertRaises(ValueError):
            archive_work.capture(self.source, destination, attempts=1)
        self.assertEqual(marker.read_text(), "unchanged")

    def test_refuses_destination_inside_original_state(self):
        with self.assertRaises(ValueError):
            archive_work.capture(self.source, self.source.parent / "unsafe", attempts=1)
        self.assertFalse((self.source.parent / "unsafe").exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
