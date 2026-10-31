#!/usr/bin/env python3
"""Offline ownership/provenance guards; no listener, load or helper is started."""
import copy
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest import mock

import startup_recovery_proof as p


class StartupGuards(unittest.TestCase):
    def test_embedded_scripts_compile(self):
        compile(p.WORKER, "cpu-worker", "exec")
        compile(p.HELPER.replace("__CONTROL__", repr("/private/proof/control")), "ssd-dev-standin", "exec")

    def test_occupied_port_refuses_before_any_proof_write_or_archive_read(self):
        with tempfile.TemporaryDirectory() as temp:
            home = Path(temp)
            parent = home / ".codexpro-go-proof" / "never-created"
            with mock.patch.object(Path, "home", return_value=home), \
                 mock.patch.object(p, "assert_free_ports", side_effect=p.Failure("occupied")), \
                 mock.patch.object(p, "archive_identity") as archive, \
                 mock.patch.object(p.subprocess, "Popen") as popen:
                with self.assertRaisesRegex(p.Failure, "occupied"):
                    p.Proof(sys.executable, parent, home / "evidence.json", home / "archive")
                archive.assert_not_called(); popen.assert_not_called()
                self.assertFalse(parent.exists())
                self.assertFalse((home / "evidence.json").exists())

    def test_wrong_binary_refuses_before_archive_read_and_state_creation(self):
        with tempfile.TemporaryDirectory() as temp:
            home = Path(temp)
            parent = home / ".codexpro-go-proof" / "never-created"
            with mock.patch.object(Path, "home", return_value=home), \
                 mock.patch.object(p, "assert_free_ports"), \
                 mock.patch.object(p, "archive_identity") as archive:
                with self.assertRaisesRegex(p.Failure, "expected build hash"):
                    p.Proof(sys.executable, parent, home / "evidence.json", home / "archive", "0" * 64)
                archive.assert_not_called()
                self.assertFalse(parent.exists())

    def test_archive_identity_checks_source_hash_size_and_wal(self):
        with tempfile.TemporaryDirectory() as temp:
            home = Path(temp)
            source = home / ".codexpro-go-proof" / "archive" / "work.sqlite"
            source.parent.mkdir(parents=True)
            raw = b"fixed private archive fixture"
            source.write_bytes(raw)
            digest = hashlib.sha256(raw).hexdigest()
            with mock.patch.object(Path, "home", return_value=home):
                path, info = p.archive_identity(source, digest, len(raw))
                self.assertEqual(path, source)
                self.assertEqual(info["bytes"], len(raw))
                with self.assertRaisesRegex(p.Failure, "source hash"):
                    p.archive_identity(source, "0" * 64, len(raw))
                with self.assertRaisesRegex(p.Failure, "wrong type/size"):
                    p.archive_identity(source, digest, len(raw) + 1)
                Path(str(source) + "-wal").write_bytes(b"pending")
                with self.assertRaisesRegex(p.Failure, "uncheckpointed WAL"):
                    p.archive_identity(source, digest, len(raw))

    def test_archive_outside_private_proof_root_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            home = Path(temp)
            source = home / "untrusted.sqlite"
            source.write_bytes(b"data")
            with mock.patch.object(Path, "home", return_value=home), \
                 mock.patch.object(p.os, "open") as opened:
                with self.assertRaisesRegex(p.Failure, "private proof snapshot"):
                    p.archive_identity(source, "0" * 64, 4)
                opened.assert_not_called()

    def test_helper_identity_rejects_wrong_nonce_group_and_missing_cgroup(self):
        value = {"nonce": "own", "helper": "/private/ssd-dev", "pid": 127,
                 "identity": "1234", "group": 127, "cgroup": "/owned", "entered_ns": 1}
        self.assertEqual(p.valid_helper_witness(value, "own", Path("/private/ssd-dev")), value)
        for key, changed in (("nonce", "other"), ("group", 128), ("cgroup", ""), ("identity", "")):
            bad = dict(value, **{key: changed})
            with self.subTest(field=key), self.assertRaises(p.Failure):
                p.valid_helper_witness(bad, "own", Path("/private/ssd-dev"))

    def test_cpu_identity_rejects_unrelated_role_process_and_priority(self):
        value = {"nonce": "own", "role": "cpu-0", "pid": 127, "identity": "1234",
                 "group": 127, "session": 127, "cgroup": "/owned", "nice": 10}
        self.assertEqual(p.valid_cpu_witness(value, "own", "cpu-0", 127, 1234), value)
        for key, changed in (("role", "cpu-1"), ("identity", "1235"), ("nice", 9), ("pid", 128)):
            bad = dict(value, **{key: changed})
            with self.subTest(field=key), self.assertRaises(p.Failure):
                p.valid_cpu_witness(bad, "own", "cpu-0", 127, 1234)

    def test_successful_health_never_masks_missing_or_slow_file_read(self):
        row = {"health": {"duration_ms": 10}, "file_read": {"duration_ms": 15}}
        report = {"first_health_ms": 100, "first_file_read_ms": 200,
                  "observations": [copy.deepcopy(row) for _ in range(3)]}
        self.assertTrue(p.thresholds_pass(report))
        report["first_file_read_ms"] = 3000.01
        self.assertFalse(p.thresholds_pass(report))
        report["first_file_read_ms"] = 200
        report["observations"][1]["file_read"]["duration_ms"] = 3000.01
        self.assertFalse(p.thresholds_pass(report))
        report["observations"] = []
        self.assertFalse(p.thresholds_pass(report))

    def test_unknown_launch_does_not_gain_signal_authority(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            proof = types.SimpleNamespace(launches=root, parent=root, state=root / "state",
                binary=Path(sys.executable).resolve(), binary_sha="a" * 64,
                environment={}, report={"boot_id": "own-boot"})
            owned = p.Owned(proof, "server", [str(proof.binary)])
            with mock.patch.object(p.subprocess, "Popen", side_effect=OSError("ambiguous launch")), \
                 mock.patch.object(p.os, "killpg") as signalled:
                with self.assertRaises(OSError): owned.start()
                before = json.loads(owned.receipt_path.read_text())
                self.assertEqual(before["phase"], "launch_outcome_unknown")
                self.assertIsNone(before["pid"])
                self.assertFalse(before["process_identity_verified"])
                with self.assertRaisesRegex(p.Failure, "launch outcome remains unknown"):
                    owned.stop()
                self.assertEqual(json.loads(owned.receipt_path.read_text())["phase"], "launch_outcome_unknown")
                signalled.assert_not_called()

    def test_unverified_created_process_refuses_signals(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            proof = types.SimpleNamespace(launches=root, parent=root, state=root / "state",
                binary=Path(sys.executable).resolve(), binary_sha="a" * 64,
                environment={}, report={"boot_id": "own-boot"})
            owned = p.Owned(proof, "server", [str(proof.binary)])
            owned.child = types.SimpleNamespace(pid=123, poll=lambda: None)
            with mock.patch("managed_contract_suite.process_start_ticks", return_value=999), \
                 mock.patch.object(p.os, "killpg") as signalled:
                with self.assertRaises(AssertionError): owned.stop()
                signalled.assert_not_called()


if __name__ == "__main__": unittest.main()
