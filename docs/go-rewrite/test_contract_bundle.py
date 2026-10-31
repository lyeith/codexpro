#!/usr/bin/env python3
"""Critical ownership, interruption and accounting guards; no server traffic."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

import contract_bundle as bundle


class BundleGuards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for _, script, _ in bundle.LANES:
            (self.scripts / script).write_text("# synthetic guard fixture\n")
        self.args = argparse.Namespace(output_dir=str(self.root / "public"), private_control_dir=str(self.root / "private"), approval_file=str(self.root / "approval.json"))
        Path(self.args.approval_file).write_text("{}")
        for label, port in (("reference", 7211), ("candidate", 7210)):
            state = self.root / label
            fixture = state / "fixture"
            fixture.mkdir(parents=True)
            token = state / "token"
            token.write_text(label + "-fixture-credential")
            manifest = dict(port=port, pid=os.getpid(), process_start_ticks=1,
                            state_root=str(state), fixture_root=str(fixture), token_file=str(token),
                            project_id="contract", source_commit="44bb76bfa671e4aad540a8976f0fa6e6d5c2507a")
            path = state / "launch.json"
            path.write_text(json.dumps(manifest))
            setattr(self.args, label + "_manifest", str(path))

    @staticmethod
    def valid_fixture(manifest, endpoint, token, root, port):
        assert manifest["port"] == port
        assert endpoint == f"http://127.0.0.1:{port}/mcp"
        assert str(token) == manifest["token_file"] and str(root) == manifest["fixture_root"]
        return Path(root), Path(manifest["state_root"]), Path(token)

    def prepare(self):
        with patch.object(bundle, "HERE", self.scripts), patch.object(bundle, "validate_launch", side_effect=self.valid_fixture), patch.object(bundle, "load_approval"):
            return bundle.prepare(self.args)

    def test_real_launch_guard_rejects_live_port_before_any_write(self):
        path = Path(self.args.reference_manifest)
        manifest = json.loads(path.read_text())
        manifest["port"] = 7200
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(bundle.require.__globals__["ContractFailure"], "manifest port"):
            bundle.run(self.args)
        self.assertFalse(Path(self.args.output_dir).exists())
        self.assertFalse(Path(self.args.private_control_dir).exists())

    def test_both_identity_guards_must_pass_before_writes(self):
        def stale_candidate(manifest, *args):
            if manifest["port"] == 7210:
                raise RuntimeError("stale candidate identity")
            return self.valid_fixture(manifest, *args)
        with patch.object(bundle, "validate_launch", side_effect=stale_candidate) as guard:
            with self.assertRaisesRegex(RuntimeError, "stale candidate"):
                bundle.run(self.args)
        self.assertEqual(guard.call_count, 2)
        self.assertFalse(Path(self.args.output_dir).exists())
        self.assertFalse(Path(self.args.private_control_dir).exists())

    def test_control_cannot_overlap_fixture(self):
        self.args.private_control_dir = str(self.root / "reference" / "fixture" / "control")
        with self.assertRaisesRegex(AssertionError, "overlap server state"):
            self.prepare()
        self.assertFalse(Path(self.args.private_control_dir).exists())

    def test_commands_are_full_default_lanes_with_only_derived_private_bindings(self):
        sides, approval, _, control = self.prepare()
        for name, script, _ in bundle.LANES:
            command = bundle.command_for(name, script, sides, approval, control / (name + ".json"), control)
            self.assertNotIn("--only", command)
            self.assertNotIn("--scenario", command)
            for label, side in sides.items():
                self.assertEqual(command[command.index("--" + label) + 1], side["endpoint"])
                self.assertEqual(command[command.index("--" + label + "-token") + 1], str(side["token"]))
                self.assertNotIn(side["credential"].decode(), " ".join(command))
            self.assertEqual("--approval-file" in command, name == "telemetry")

    def test_categories_pending_and_raw_details_are_not_flattened(self):
        counts = {"pass": 41, "approved_delta": 8, "fail": 0, "blocked": 0, "pending": 2}
        path = self.root / "admin-report.json"
        path.write_text(json.dumps(dict(ok=True, counts=counts, cleanup={"reference": {}, "candidate": {}}, cases=[{"private": "fixture body"}])))
        got = bundle.receipt("admin", path, [b"unrelated-token"])
        self.assertEqual(got["units"], counts)
        self.assertTrue(got["reported_gate_passed"])
        self.assertTrue(got["cleanup_verified"])
        self.assertNotIn("fixture body", json.dumps(got))
        self.assertEqual(got["report_sha256"], hashlib.sha256(path.read_bytes()).hexdigest())

    def test_missing_cleanup_and_credential_payload_are_rejected(self):
        path = self.root / "telemetry-report.json"
        data = dict(passed=True, comparison_counts={"passed_unchanged": 102, "passed_with_approved_delta": 2, "failed": 0})
        path.write_text(json.dumps(data))
        with self.assertRaisesRegex(AssertionError, "cleanup evidence"):
            bundle.receipt("telemetry", path, [])
        data.update(cleanup_errors={"reference": [], "candidate": []}, private="fixture-credential")
        path.write_text(json.dumps(data))
        with self.assertRaisesRegex(AssertionError, "credential found"):
            bundle.receipt("telemetry", path, [b"fixture-credential"])

    def test_timeout_interrupts_only_owned_harness_and_allows_finally_cleanup(self):
        control = self.root / "logs"
        control.mkdir()
        marker = control / "cleanup-witness"
        code = "import time\nfrom pathlib import Path\ntry:\n time.sleep(20)\nfinally:\n Path(" + repr(str(marker)) + ").write_text('cleaned')\n"
        got = bundle.run_bounded([sys.executable, "-c", code], control, "owned", 1.8, grace=0.4)
        self.assertTrue(got["timed_out"])
        self.assertFalse(got["cleanup_forced"])
        self.assertTrue(marker.exists())
        self.assertLess(got["elapsed_seconds"], 1.8)
        for name in ("owned.stdout.log", "owned.stderr.log"):
            self.assertEqual(stat.S_IMODE((control / name).stat().st_mode), 0o600)

    def test_timed_out_lane_prevents_later_mutation_lanes(self):
        sides, approval, output, control = self.prepare()
        observed = dict(exit_code=130, timed_out=True, interrupted=False, cleanup_forced=False, elapsed_seconds=1, budget_seconds=120)
        with patch.object(bundle, "prepare", return_value=(sides, approval, output, control)), patch.object(bundle, "HERE", self.scripts), patch.object(bundle, "validate_launch", side_effect=self.valid_fixture), patch.object(bundle, "run_bounded", return_value=observed) as launch, patch.object(bundle, "receipt", return_value={"reported_gate_passed": False, "cleanup_verified": True}):
            old_mask = os.umask(0o077)
            try:
                self.assertEqual(bundle.run(self.args), 1)
            finally:
                os.umask(old_mask)
        self.assertEqual(launch.call_count, 1)
        report = json.loads((output / "summary.json").read_text())
        self.assertEqual(report["not_run"], ["process", "telemetry", "admin"])
        self.assertFalse(report["all_cli_gates_passed"])


if __name__ == "__main__":
    unittest.main()
