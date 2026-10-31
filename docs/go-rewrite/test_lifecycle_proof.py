"""Local safety checks; never starts a server, helper, job, or host scope."""
import io
import json
from pathlib import Path
import signal
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

from lifecycle_proof import CHILD_SOURCE, GIT_SOURCE, HELPER_SOURCE, Probe, ProofFailure, alive, cgroup_population, main, write_launch_receipt


class GuardTests(unittest.TestCase):
    def launch_fixture(self, directory):
        probe = Probe.__new__(Probe)
        probe.parent = Path(directory)
        probe.state = probe.parent / "state"; probe.state.mkdir()
        probe.launch_dir = probe.parent / "launches"; probe.launch_dir.mkdir()
        probe.binary, probe.helper_wrapper = probe.parent / "candidate", probe.parent / "helper"
        probe.token = probe.state / "http-token"; probe.token.write_text("private-test-credential-never-in-receipt")
        probe.process, probe.client, probe.launch_number = None, None, 0
        probe.log, probe.environment, probe.deadline = io.BytesIO(), {}, time.monotonic() + 10
        probe.report = {"binary_sha256": "a" * 64, "boot_id": "fixture-boot", "server_incarnations": [], "launch_receipts": []}
        return probe

    def test_launch_receipt_precedes_spawn_and_binds_observed_identity_before_http(self):
        with tempfile.TemporaryDirectory() as directory:
            probe = self.launch_fixture(directory)
            path = probe.launch_dir / "server-001.json"
            class OwnedProcess:
                pid = 12345
                def poll(self): return None
            def spawn(*args, **kwargs):
                prepared = json.loads(path.read_text())
                self.assertEqual(prepared["phase"], "prepared")
                self.assertIsNone(prepared["pid"])
                self.assertFalse(prepared["process_identity_verified"])
                return OwnedProcess()
            def initialize(*args, **kwargs):
                identified = json.loads(path.read_text())
                self.assertEqual(identified["phase"], "identified")
                self.assertEqual(identified["pid"], 12345)
                self.assertEqual(identified["process_start_ticks"], 9001)
                self.assertEqual(identified["cgroup"], "/owned-proof.scope")
                return object()
            with patch.object(probe, "assert_free_port"), patch("lifecycle_proof.subprocess.Popen", side_effect=spawn), patch("lifecycle_proof.process_identity", return_value={"pid": 12345, "identity": "9001", "state": "S"}), patch("lifecycle_proof.cgroup_for", return_value="/owned-proof.scope"), patch("lifecycle_proof.Client", side_effect=initialize):
                probe.start_server()
            final = json.loads(path.read_text())
            self.assertEqual(final["phase"], "ready")
            self.assertEqual(final["state_root"], str(probe.state))
            self.assertEqual(final["binary_sha256"], "a" * 64)
            self.assertTrue(final["process_identity_verified"])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.stat().st_nlink, 1)
            self.assertNotIn(probe.token.read_text(), path.read_text())
            self.assertEqual(probe.report["launch_receipts"], [str(path)])

    def test_interrupted_launch_is_unknown_and_never_becomes_signal_authority(self):
        with tempfile.TemporaryDirectory() as directory:
            probe = self.launch_fixture(directory)
            with patch.object(probe, "assert_free_port"), patch("lifecycle_proof.subprocess.Popen", side_effect=OSError("interrupted launch")), patch("lifecycle_proof.os.kill") as signal_pid:
                with self.assertRaises(OSError): probe.start_server()
                probe.stop_server()
                signal_pid.assert_not_called()
            receipt = json.loads((probe.launch_dir / "server-001.json").read_text())
            self.assertEqual(receipt["phase"], "launch_outcome_unknown")
            self.assertIsNone(receipt["pid"])
            self.assertIsNone(receipt["process_start_ticks"])
            self.assertFalse(receipt["process_identity_verified"])

    def test_spawn_without_identity_remains_explicitly_unverified(self):
        with tempfile.TemporaryDirectory() as directory:
            probe = self.launch_fixture(directory)
            class OwnedProcess:
                pid = 12345
                def poll(self): return None
            with patch.object(probe, "assert_free_port"), patch("lifecycle_proof.subprocess.Popen", return_value=OwnedProcess()), patch.object(probe, "wait", side_effect=ProofFailure("identity unavailable")), patch("lifecycle_proof.os.kill") as signal_pid:
                with self.assertRaises(ProofFailure): probe.start_server()
                signal_pid.assert_not_called()
            receipt = json.loads((probe.launch_dir / "server-001.json").read_text())
            self.assertEqual(receipt["phase"], "spawned_unverified")
            self.assertEqual(receipt["pid"], 12345)
            self.assertIsNone(receipt["process_start_ticks"])
            self.assertFalse(receipt["process_identity_verified"])

    def test_receipt_sync_failure_preserves_last_complete_record(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "receipt.json"
            before = {"phase": "prepared", "pid": None}
            write_launch_receipt(path, before, new=True)
            with patch("lifecycle_proof.os.fsync", side_effect=OSError("injected sync failure")):
                with self.assertRaises(OSError): write_launch_receipt(path, {"phase": "identified", "pid": 123})
            self.assertEqual(json.loads(path.read_text()), before)
            self.assertEqual(list(Path(directory).iterdir()), [path])
            with self.assertRaises(FileExistsError): write_launch_receipt(path, {"different": True}, new=True)
            self.assertEqual(json.loads(path.read_text()), before)

    def test_busy_port_refuses_before_creating_state_or_starting_process(self):
        with tempfile.TemporaryDirectory(prefix="sp6-lifecycle-test-") as directory:
            parent = Path(directory) / "fresh"
            with patch.object(Probe, "assert_free_port", side_effect=ProofFailure("occupied")), patch("lifecycle_proof.subprocess.Popen") as spawn:
                with self.assertRaises(ProofFailure):
                    Probe(sys.executable, sys.executable, parent, Path(directory) / "evidence.json")
                self.assertFalse(parent.exists())
                spawn.assert_not_called()

    def test_existing_state_is_never_reused(self):
        with tempfile.TemporaryDirectory(prefix="sp6-lifecycle-test-") as directory:
            marker = Path(directory) / "must-remain"
            marker.write_text("existing private work")
            with patch("lifecycle_proof.subprocess.Popen") as spawn:
                with self.assertRaises(ProofFailure):
                    Probe(sys.executable, sys.executable, directory, Path(directory) / "evidence.json")
                spawn.assert_not_called()
            self.assertEqual(marker.read_text(), "existing private work")

    def test_stop_only_signals_owned_popen_child(self):
        class OwnedProcess:
            signals = []
            def poll(self): return None
            def send_signal(self, value): self.signals.append(value)
            def wait(self, timeout): return 0
        probe = Probe.__new__(Probe)
        probe.process, probe.client = OwnedProcess(), None
        with patch("lifecycle_proof.os.kill") as numeric_signal, patch("lifecycle_proof.subprocess.run") as shell:
            probe.stop_server(hard=True)
            numeric_signal.assert_not_called()
            shell.assert_not_called()
        self.assertEqual(probe.process.signals, [signal.SIGKILL])

    def test_reused_pid_is_not_an_owned_live_process(self):
        with patch("lifecycle_proof.process_identity", return_value={"pid": 1234, "identity": "new", "state": "S"}):
            self.assertFalse(alive({"pid": 1234, "identity": "old"}))

    def test_scope_probe_cannot_read_outside_cgroup_root(self):
        unit = "codexpro-job_01234567-" + "a" * 32 + ".scope"
        with self.assertRaises(ProofFailure):
            cgroup_population({"unit": unit, "cgroup": "/../../../tmp/" + unit})

    def test_generated_fault_fixtures_compile_with_embedded_private_paths(self):
        for name, source in (("child", CHILD_SOURCE), ("git", GIT_SOURCE), ("helper", HELPER_SOURCE)):
            source = source.replace("__SP6_CONTROL__", repr("/tmp/sp6-owned/control")).replace("__SP6_HELPER__", repr("/tmp/sp6-owned/helper")).replace("__SP6_GIT__", repr("/usr/bin/git"))
            compile(source, name, "exec")

    def test_lost_rpc_admission_is_reconciled_only_from_exact_private_intent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            probe = Probe.__new__(Probe)
            probe.control = root / "control"; probe.control.mkdir()
            probe.state = root / "state"; probe.state.mkdir()
            checkout = probe.state / "worktrees" / "owned"; checkout.mkdir(parents=True)
            probe.output = root / "evidence.json"
            probe.token = root / "token"; probe.token.write_text("own-private-test-credential")
            probe.nonce, probe.workspace, probe.owned_jobs = "our-nonce", "ws_owned", set()
            command = "python fixture-child --nonce our-nonce"
            probe.admission_intents = {("ws_owned", command, ".")}
            probe.helper, probe.helper_wrapper = root / "real-helper", root / "fixture-helper"
            probe.report = {"scenarios": [{"status": "FAIL"}], "cleanup_errors": []}
            probe.witnesses, probe.process, probe.client, probe.log = [], None, None, io.BytesIO()
            record = {"id": "job_01234567", "workspace_id": "ws_owned", "command": command, "cwd": ".",
                      "root": str(checkout), "quiescent": False,
                      "resource_claim": {"helper": str(probe.helper_wrapper), "run_id": "a" * 32, "registration_id": "b" * 32}}
            foreign = {**record, "id": "job_76543210", "command": "different our-nonce"}
            order = []
            def snapshot():
                order.append("snapshot")
                return [record, foreign]
            probe.jobs = snapshot
            class Receipt:
                stdout = json.dumps({"run_id": "a" * 32, "quiescent": True, "cancelled": True}).encode()
            def settle(argv, **options):
                order.append("cancel")
                self.assertFalse((probe.control / "release-begin").exists())
                return Receipt()
            with patch.object(probe, "stop_server", side_effect=lambda: order.append("stop")), patch.object(probe, "command", side_effect=settle) as cancel:
                probe.cleanup()
                self.assertEqual(cancel.call_count, 1)
                self.assertEqual(cancel.call_args.args[0], [str(probe.helper), "external", "cancel", "a" * 32,
                                                          "--registration-id", "b" * 32])
            self.assertEqual(order, ["stop", "snapshot", "cancel"])
            self.assertTrue((probe.control / "release-begin").exists())
            self.assertEqual(probe.owned_jobs, {"job_01234567"})
            self.assertEqual(probe.report["uncertain_admissions_recovered_for_cleanup"], ["job_01234567"])
            self.assertFalse(probe.report["passed"])

    def test_admission_intent_is_recorded_before_a_lost_rpc_response(self):
        probe = Probe.__new__(Probe)
        class OwnedProcess:
            def poll(self): return None
        class LostClient:
            def call(self, name, arguments): raise TimeoutError("lost after commit")
        probe.process, probe.client = OwnedProcess(), LostClient()
        probe.deadline, probe.nonce, probe.admission_intents = float("inf"), "our-nonce", set()
        arguments = {"workspace_id": "ws_owned", "commands": [{"command": "echo our-nonce", "cwd": "."}]}
        with self.assertRaises(TimeoutError):
            probe.call("start_jobs", arguments)
        self.assertEqual(probe.admission_intents, {("ws_owned", "echo our-nonce", ".")})

    def test_external_term_reaches_owned_cleanup_and_restores_handler(self):
        handlers, order, previous = [], [], object()
        class InterruptedProbe:
            report = {"scenarios": [], "cleanup_errors": []}
            output = Path("/never-created-proof.json")
            def start_server(self): handlers[0](signal.SIGTERM, None)
            def cleanup(self):
                order.append("cleanup")
                self.report["passed"] = False
        probe = InterruptedProbe()
        def handler(sig, value):
            self.assertEqual(sig, signal.SIGTERM)
            handlers.append(value)
            return previous
        argv = ["lifecycle_proof", "--binary", "/not-opened", "--helper", "/not-opened",
                "--state-parent", "/not-created", "--output", "/not-created"]
        with patch("lifecycle_proof.Probe", return_value=probe), patch("lifecycle_proof.signal.signal", side_effect=handler), patch("lifecycle_proof.os.getpriority", return_value=10), patch.object(sys, "argv", argv), patch.object(sys, "stdout", io.StringIO()):
            self.assertEqual(main(), 1)
        self.assertEqual(order, ["cleanup"])
        self.assertIs(handlers[-1], previous)
        self.assertEqual(probe.report["scenarios"][0]["status"], "FAIL")
        self.assertIn("interrupted by signal", probe.report["scenarios"][0]["error"])

    def test_cleanup_refuses_a_claim_without_this_run_nonce(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            probe = Probe.__new__(Probe)
            probe.control = root / "control"; probe.control.mkdir()
            probe.output = root / "evidence.json"
            probe.token = root / "token"; probe.token.write_text("own-private-test-credential")
            probe.nonce, probe.owned_jobs = "our-nonce", {"job_01234567"}
            probe.admission_intents = set()
            probe.helper, probe.helper_wrapper = root / "real-helper", root / "fixture-helper"
            probe.report = {"scenarios": [{"status": "FAIL"}], "cleanup_errors": []}
            probe.witnesses, probe.process, probe.client, probe.log = [], None, None, io.BytesIO()
            probe.jobs = lambda: [{"id": "job_01234567", "command": "belongs to another caller", "quiescent": False,
                                  "resource_claim": {"helper": str(probe.helper_wrapper), "run_id": "a" * 32, "registration_id": "b" * 32}}]
            with patch.object(probe, "command") as command:
                probe.cleanup()
                command.assert_not_called()
            self.assertFalse(json.loads(probe.output.read_text())["passed"])


if __name__ == "__main__": unittest.main()
