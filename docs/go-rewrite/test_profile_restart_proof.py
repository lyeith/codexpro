import copy
import gzip
import json
import os
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest import mock

import profile_restart_proof as proof


def oracle_fixture():
    descriptors = {
        "minimal": ["server_config", "read", "work_status", "work_manage", "work_update"],
        "standard": ["server_config", "read", "search", "work_status", "work_manage", "work_update"],
        "full": ["server_config", "read", "search", "list_workspaces", "codexpro", "codexpro_inventory", "work_status"],
    }
    return {"source_commit": proof.SOURCE_COMMIT, "cases": [
        {"options": {"worktree_mode": "off", "tool_mode": tier, "project_count": 1,
                     "analysis_enabled": True, "audit_mode": "metadata", "connection_test": False},
         "source_tool_names": names} for tier, names in descriptors.items()]}


def runtime(expected, oracle):
    names = proof.expected_inventory(oracle, expected["toolMode"])
    config = {**expected, "port": 7210, "bashMode": "full", "writeMode": "workspace",
              "authEnabled": True, "worktreeMode": "off", "registeredTools": names,
              "registeredToolCount": len(names)}
    return config, [{"name": name} for name in names]


class RuntimeAssertions(unittest.TestCase):
    def test_profile_is_not_claimed_consumed_when_new_process_keeps_defaults(self):
        oracle = oracle_fixture()
        config, tools = runtime(proof.DEFAULTS, oracle)
        with self.assertRaisesRegex(proof.ContractFailure, "toolMode"):
            proof.validate_runtime(config, tools, proof.SAVED, oracle)

    def test_runtime_and_registration_both_must_apply_the_saved_tier(self):
        oracle = oracle_fixture()
        config, tools = runtime(proof.SAVED, oracle)
        proof.validate_runtime(config, tools, proof.SAVED, oracle)
        stale_tools = runtime(proof.DEFAULTS, oracle)[1]
        with self.assertRaisesRegex(proof.ContractFailure, "tools/list"):
            proof.validate_runtime(config, stale_tools, proof.SAVED, oracle)

    def test_omitted_null_and_wrong_scalar_type_are_not_normalized(self):
        oracle = oracle_fixture()
        config, tools = runtime(proof.DEFAULTS, oracle)
        del config["bashSessionId"]
        with self.assertRaisesRegex(proof.ContractFailure, "bashSessionId"):
            proof.validate_runtime(config, tools, proof.DEFAULTS, oracle)
        config, tools = runtime(proof.SAVED, oracle)
        config["maxWorktrees"] = "7"
        with self.assertRaisesRegex(proof.ContractFailure, "maxWorktrees"):
            proof.validate_runtime(config, tools, proof.SAVED, oracle)

    def test_retirements_do_not_license_extra_runtime_tools(self):
        oracle = oracle_fixture()
        config, tools = runtime({**proof.SAVED, "toolMode": "full"}, oracle)
        self.assertNotIn("codexpro", config["registeredTools"])
        tools.append({"name": "codexpro"})
        with self.assertRaisesRegex(proof.ContractFailure, "tools/list"):
            proof.validate_runtime(config, tools, {**proof.SAVED, "toolMode": "full"}, oracle)

    def test_oracle_must_match_commit_and_exact_profile(self):
        oracle = oracle_fixture(); oracle["source_commit"] = "different"
        with self.assertRaises(proof.ContractFailure):
            proof.expected_inventory(oracle, "standard")
        oracle = oracle_fixture(); oracle["cases"][0]["options"]["project_count"] = 2
        with self.assertRaises(proof.ContractFailure):
            proof.expected_inventory(oracle, "minimal")


class OwnershipAndLaunch(unittest.TestCase):
    def fake_proof(self):
        return SimpleNamespace(binary=Path("/tmp/candidate"), home=Path("/tmp/private/home"),
            root=Path("/tmp/private/fixture"), parent=Path("/tmp/private"),
            token=Path("/tmp/private/token"), catalog=Path("/tmp/private/projects.json"),
            limits=Path("/tmp/private/limits.env"))

    def test_base_argv_does_not_override_fields_under_proof(self):
        server = proof.RestartServer(self.fake_proof(), "saved-profile")
        for flag in ("--tool-mode", "--bash-transcript", "--max-worktrees", "--worktree-base", "--bash-session"):
            self.assertNotIn(flag, server.argv)
        self.assertIn("127.0.0.1:7210", server.argv)
        self.assertNotIn("7200", server.argv)
        override = proof.RestartServer(self.fake_proof(), "explicit-override",
                                       ("--tool-mode", "full", "--max-worktrees", "11"))
        self.assertEqual(override.argv[-4:], ["--tool-mode", "full", "--max-worktrees", "11"])

    def test_inherited_configuration_cannot_override_profile_fixture(self):
        with mock.patch.dict(os.environ, {"CODEXPRO_TOOL_MODE": "full", "CODEXPRO_PORT": "7200",
                                         "CODEXPRO_GO_HOME": "/live", "CODEX_HOME": "/live"}):
            environment = proof.sanitized_environment()
        self.assertFalse(any(key.startswith("CODEXPRO_") for key in environment))
        self.assertNotIn("CODEX_HOME", environment)

    def test_cleanup_refuses_a_reused_pid(self):
        server = proof.RestartServer(self.fake_proof(), "initial")
        server.child = SimpleNamespace(pid=12345, poll=lambda: None)
        server.witness = 100
        with mock.patch("managed_contract_suite.process_start_ticks", return_value=101), \
             mock.patch("managed_contract_suite.os.killpg") as signal_group:
            with self.assertRaisesRegex(proof.ContractFailure, "PID identity changed"):
                server.stop()
            signal_group.assert_not_called()

    def test_completed_cleanup_never_signals_a_later_reused_pid(self):
        server = proof.RestartServer(self.fake_proof(), "initial")
        server.stopped_report = {"stopped": True, "owned_group_quiescent": True}
        with mock.patch.object(proof.OwnedServer, "stop", side_effect=AssertionError("must not stop again")):
            self.assertEqual(server.stop(), server.stopped_report)

    def test_occupied_port_refuses_before_state_or_process_creation(self):
        with tempfile.TemporaryDirectory(prefix="sp6-profile-restart-test-") as temp:
            base = Path(temp); oracle = base / "oracle.json"
            oracle.write_text(json.dumps(oracle_fixture()))
            state = base / "new-state"
            with mock.patch.object(proof, "assert_free_ports", side_effect=proof.ContractFailure("occupied")), \
                 mock.patch.object(proof.subprocess, "Popen") as spawn:
                with self.assertRaisesRegex(proof.ContractFailure, "occupied"):
                    proof.Proof("/bin/true", state, base / "result.json", oracle)
                self.assertFalse(state.exists())
                spawn.assert_not_called()


class PersistedProfileChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="sp6-profile-restart-test-")
        base = Path(self.temp.name); self.oracle = base / "oracle.json"
        self.oracle.write_text(json.dumps(oracle_fixture()))
        with mock.patch.object(proof, "assert_free_ports"):
            self.proof = proof.Proof("/bin/true", base / "state", base / "report.json", self.oracle)
        self.credential = self.proof.token.read_text().strip()
        def assert_safe(value):
            if self.credential in json.dumps(value):
                raise RuntimeError("credential leaked")
        self.server = SimpleNamespace(client=SimpleNamespace(credential=self.credential,
            session=None, assert_no_credential=assert_safe))
        self.proof.servers.append(self.server)

    def tearDown(self):
        self.proof.evidence.close()
        self.temp.cleanup()

    def saved_http(self, server, method, payload):
        self.assertEqual(method, "POST")
        self.assertEqual(payload["toolMode"], "minimal")
        self.proof.profile.parent.mkdir(mode=0o700)
        value = {**proof.PROFILE_FIELDS, "port": "7210", "root": str(self.proof.root),
                 "version": 1, "token": self.credential}
        proof.write_exclusive(self.proof.profile, json.dumps(value))
        return {"saved": True, "profile_path": str(self.proof.profile)}

    def test_http_persistence_is_verified_without_recording_credential(self):
        with mock.patch.object(self.proof, "http", side_effect=self.saved_http):
            self.proof.save(self.server)
        self.proof.check_saved_unchanged()
        self.proof.evidence.close()
        raw = gzip.open(self.proof.evidence.path, "rt").read()
        self.assertNotIn(self.credential, raw)
        self.assertIn('"token":"<saved>"', raw)

    def test_restart_cannot_silently_rewrite_saved_profile(self):
        with mock.patch.object(self.proof, "http", side_effect=self.saved_http):
            self.proof.save(self.server)
        with self.proof.profile.open("ab") as stream:
            stream.write(b" ")
        with self.assertRaisesRegex(proof.ContractFailure, "changed the saved profile"):
            self.proof.check_saved_unchanged()

    def test_persisted_http_fields_must_match_the_requested_settings(self):
        def wrong_field(server, method, payload):
            result = self.saved_http(server, method, payload)
            value = json.loads(self.proof.profile.read_text()); value["maxWorktrees"] = "64"
            self.proof.profile.write_text(json.dumps(value))
            return result
        with mock.patch.object(self.proof, "http", side_effect=wrong_field):
            with self.assertRaisesRegex(proof.ContractFailure, "maxWorktrees"):
                self.proof.save(self.server)
        self.assertEqual(self.proof.evidence.records, 0)


if __name__ == "__main__":
    unittest.main()
