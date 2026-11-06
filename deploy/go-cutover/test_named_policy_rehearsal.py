#!/usr/bin/env python3
"""Focused controls for the fixture-only recovery driver."""

import copy
import hashlib
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location(
    "named_policy_rehearsal", Path(__file__).with_name("named-policy-rehearsal.py"))
REHEARSAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REHEARSAL)


class RecoveryDriverTests(unittest.TestCase):
    def test_child_environment_does_not_inherit_service_or_credential_settings(self):
        hostile = {
            "AGENTMAIL_URL": "http://outside.invalid",
            "AGENTMAIL_TOKEN_FILE": "/private/not-a-fixture",
            "CODEXPRO_ENV_FILE": "/private/not-a-fixture",
            "HTTP_PROXY": "http://outside.invalid",
            "LISTEN_PID": "123", "LISTEN_FDS": "1",
            "CODEX_HOME": "/private/not-a-fixture",
        }
        with mock.patch.dict(os.environ, hostile):
            env = REHEARSAL.fixture_environment(Path("/fixture"))
        self.assertTrue(set(hostile).isdisjoint(env))
        self.assertEqual(env["HOME"], "/fixture/home")
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], "/dev/null")
        self.assertEqual(env["CODEXPRO_JOB_SCOPES"], "0")

    def test_retirement_keeps_existing_schema_tombstones_and_fleet_value(self):
        policy, tokens = REHEARSAL.make_policy()
        original = copy.deepcopy(policy)
        fleet = tokens["fleet-connector"]
        REHEARSAL.retain_new_denials(policy, tokens)
        self.assertEqual(tokens["fleet-connector"], fleet)
        self.assertEqual(policy["project_streams"], original["project_streams"])
        before = {row["id"]: row for row in original["credentials"]}
        after = {row["id"]: row for row in policy["credentials"]}
        self.assertTrue(set(before) < set(after))
        self.assertTrue(after["old-fleet"]["revoked"])
        self.assertTrue(after["revoked"]["revoked"])
        self.assertTrue(after["rotating"]["revoked"])
        self.assertEqual(after["former-legacy"]["format"], "imported_fleet_v1")
        self.assertTrue(after["former-legacy"]["revoked"])
        self.assertEqual(after["former-legacy"]["sha256"],
                         hashlib.sha256(tokens["initial-legacy"].encode()).hexdigest())
        self.assertTrue(next(row for row in policy["principals"] if row["id"] == "later")["disabled"])
        self.assertEqual(after["fleet-connector"], before["fleet-connector"])
        self.assertEqual(after["collector"]["audience"], "status-collector")

    def test_binary_selection_refuses_installed_symlink_and_shared_files(self):
        with tempfile.TemporaryDirectory() as name, tempfile.TemporaryDirectory() as outside:
            parent = Path(name)
            binary = parent / "fixture-bin"
            binary.write_bytes(b"not executed")
            binary.chmod(0o700)
            self.assertEqual(REHEARSAL.fixture_binary(binary, parent), binary)
            external = Path(outside) / "installed"
            external.write_bytes(b"not executed")
            external.chmod(0o700)
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.fixture_binary(external, parent)
            link = parent / "alias"
            link.symlink_to(binary)
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.fixture_binary(link, parent)
            hardlink = parent / "shared"
            os.link(binary, hardlink)
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.fixture_binary(binary, parent)

    def test_present_binary_metadata_requires_clean_exact_module_and_source(self):
        sha = "a" * 40
        module = "example.invalid/fixture/cmd/server"
        digest = hashlib.sha256(b"fixture").hexdigest()
        with tempfile.TemporaryDirectory() as name:
            binary = Path(name) / "fixture-bin"
            binary.write_bytes(b"fixture")
            good = (str(binary) + ": go1.fixture\n\tpath\t" + module +
                    "\n\tbuild\tvcs.revision=" + sha + "\n\tbuild\tvcs.modified=false\n")
            for value in (good.replace(sha, "b" * 40),
                          good.replace("modified=false", "modified=true"),
                          good.replace(module, "example.invalid/another"),
                          good.replace("\tbuild\tvcs.revision=" + sha + "\n", "")):
                with self.subTest(metadata=value):
                    with mock.patch.object(REHEARSAL, "capture", return_value=value):
                        with self.assertRaises(REHEARSAL.Refusal):
                            REHEARSAL.main_binary_pin("go", binary, sha, module, digest)
            with mock.patch.object(REHEARSAL, "capture", return_value=good):
                result = REHEARSAL.main_binary_pin("go", binary, sha, module, digest)
            self.assertEqual(result, {"sha256": digest, "vcs_metadata": "present",
                                      "vcs_revision": sha, "vcs_modified": "false"})
        with self.assertRaises(REHEARSAL.Refusal):
            REHEARSAL.source_pin("/unused", "short", {})

    def test_absent_vcs_metadata_requires_exact_digest_and_discloses_absence(self):
        sha = "a" * 40
        module = "example.invalid/fixture/cmd/server"
        digest = hashlib.sha256(b"fixture").hexdigest()
        with tempfile.TemporaryDirectory() as name:
            binary = Path(name) / "fixture-bin"
            binary.write_bytes(b"fixture")
            absent = str(binary) + ": go1.fixture\n\tpath\t" + module + "\n"
            with mock.patch.object(REHEARSAL, "capture", return_value=absent):
                result = REHEARSAL.main_binary_pin("go", binary, sha, module, digest)
                self.assertEqual(result, {"sha256": digest, "vcs_metadata": "absent",
                                          "vcs_revision": None, "vcs_modified": None})
                for wrong in ("b" * 64, "short"):
                    with self.assertRaises(REHEARSAL.Refusal):
                        REHEARSAL.main_binary_pin("go", binary, sha, module, wrong)
                with self.assertRaises(REHEARSAL.Refusal):
                    REHEARSAL.main_binary_pin("go", binary, sha, "example.invalid/another", digest)
                binary.write_bytes(b"changed fixture artifact")
                with self.assertRaises(REHEARSAL.Refusal):
                    REHEARSAL.main_binary_pin("go", binary, sha, module, digest)

    def test_redirect_is_a_failure_not_an_authentication_success(self):
        with mock.patch.object(REHEARSAL, "request", return_value=(302, {"Location": "/elsewhere"}, b"")):
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.expect(1234, "/healthz", "disposable", 200, "auth")

    def test_collector_schema_cannot_silently_turn_into_global_mail_health(self):
        body = b'{"version":1,"observed_at":"fixture","health":{"ok":true},"mail":{"scope":"plans_control"}}'
        with mock.patch.object(REHEARSAL, "expect", return_value=(
                {"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"}, body)):
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.collector_check("mail", 1234, {"collector": "disposable"})

    def test_public_mode_is_only_in_recovered_bridge_off_command(self):
        root = Path("/fixture")
        before = REHEARSAL.service_command("mail", Path("/fixture/mail"), root, 1234, False)
        after = REHEARSAL.service_command("mail", Path("/fixture/mail"), root, 1234, True)
        self.assertIn("--no-adapters", before)
        self.assertIn("--no-adapters", after)
        self.assertIn("--allow-legacy=true", before)
        self.assertNotIn("--public-prefix", before)
        self.assertIn("--allow-legacy=false", after)
        self.assertEqual(after[-2:], ["--public-prefix", "/agentmail"])
        self.assertIn("127.0.0.1:1234", after)

    def test_private_root_rejects_world_readable_directory(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            root.chmod(0o755)
            with self.assertRaises(REHEARSAL.Refusal):
                REHEARSAL.private_directory(root)


if __name__ == "__main__":
    unittest.main()
