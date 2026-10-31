"""Local checks that differential normalization cannot hide contract changes."""
import copy
import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from process_contract_suite import ContractFailure, RefusePrivateRedirect, Side, decode_cursor, encode_cursor


class NormalizationTests(unittest.TestCase):
    def side(self):
        side = Side.__new__(Side)
        side.root = Path("/private/fixture")
        side.workspace = "ws_fixture"
        side.jobs = {"job_1234abcd": "job_<1>"}
        side.previous_jobs = {}
        side.batch_paths = {}
        side.batch_tags = {}
        return side

    def result(self):
        view = {"job_id": "job_1234abcd", "started_at": "2026-10-31T17:00:00.000Z",
                "finished_at": "2026-10-31T17:00:00.100Z", "exit_code": 0,
                "elapsed_ms": 100, "stdout": "α\n", "stderr": "",
                "available_stdout_bytes": 3, "available_stderr_bytes": 0,
                "next_cursor": encode_cursor({"v": 1, "id": "ws_fixture:job_1234abcd:2026-10-31T17:00:00.000Z", "offsets": [3, 0]})}
        return {"content": [{"type": "text", "text": "Duration: 100 ms\nExit: 0\nα\n"}],
                "structuredContent": {"jobs": [view]}}

    def test_only_validated_timings_vary(self):
        side = self.side()
        before = self.result()
        after = copy.deepcopy(before)
        after["content"][0]["text"] = "Duration: 200 ms\nExit: 0\nα\n"
        after["structuredContent"]["jobs"][0]["elapsed_ms"] = 200
        self.assertEqual(side.normalize(before), side.normalize(after))
        after["structuredContent"]["jobs"][0]["exit_code"] = 1
        self.assertNotEqual(side.normalize(before), side.normalize(after))

    def test_known_job_recovery_preserves_prior_identity_order_and_count(self):
        side = self.side()
        side.previous_jobs = {"job_abcdef12": "job_<prior:earlier:1>"}
        result = {"error": "Unknown job_id: job_00000000. Known jobs: job_1234abcd, job_abcdef12.",
                  "known_job_ids": ["job_1234abcd", "job_abcdef12"]}
        normalized = side.normalize(result)
        self.assertEqual(normalized["known_job_ids"], ["job_<1>", "job_<prior:earlier:1>"])
        self.assertIn("job_00000000", normalized["error"])
        wrong_order = copy.deepcopy(result)
        wrong_order["known_job_ids"].reverse()
        self.assertNotEqual(normalized, side.normalize(wrong_order))
        missing = copy.deepcopy(result)
        missing["known_job_ids"].pop()
        self.assertNotEqual(normalized, side.normalize(missing))
        unknown = copy.deepcopy(result)
        unknown["known_job_ids"][1] = "job_deadbeef"
        side.observe(unknown)
        self.assertNotIn("job_deadbeef", side.jobs)
        self.assertNotEqual(normalized, side.normalize(unknown))

    def test_begin_retains_only_previously_observed_job_identities(self):
        side = self.side()
        side.scenario = "earlier"
        with tempfile.TemporaryDirectory() as directory:
            side.fixture = Path(directory)
            side.begin("next")
        self.assertEqual(side.jobs, {})
        self.assertEqual(side.previous_jobs, {"job_1234abcd": "job_<prior:earlier:1>"})
        side.observe({"background_jobs": [{"job_id": "job_1234abcd"}]})
        self.assertEqual(side.jobs, {})
        self.assertEqual(side.normalize({"job_id": "job_1234abcd"}), {"job_id": "job_<prior:earlier:1>"})

    def test_output_bytes_and_text_remain_exact(self):
        side = self.side()
        before = self.result()
        for field, value in [("stdout", "β\n"), ("available_stdout_bytes", 4)]:
            after = copy.deepcopy(before)
            after["structuredContent"]["jobs"][0][field] = value
            self.assertNotEqual(side.normalize(before), side.normalize(after))

    def test_cursor_identity_and_offsets_are_validated(self):
        side = self.side()
        result = self.result()
        view = result["structuredContent"]["jobs"][0]
        cursor = decode_cursor(view["next_cursor"])
        cursor["id"] = "wrong-owner"
        view["next_cursor"] = encode_cursor(cursor)
        with self.assertRaises(ContractFailure):
            side.normalize(result)
        cursor["id"] = "ws_fixture:job_1234abcd:2026-10-31T17:00:00.000Z"
        cursor["offsets"] = [4, 0]
        view["next_cursor"] = encode_cursor(cursor)
        with self.assertRaises(ContractFailure):
            side.normalize(result)

    def test_invalid_time_is_not_normalized_away(self):
        result = self.result()
        result["structuredContent"]["jobs"][0]["elapsed_ms"] = -1
        with self.assertRaises(ContractFailure):
            self.side().normalize(result)

    def test_live_port_is_rejected_before_any_root_or_token_access(self):
        with self.assertRaises(ContractFailure):
            Side("ref", "http://127.0.0.1:7200/mcp", "/not-read-token", "/not-read-root", "fixture", "run")


class ScopeGuardTests(unittest.TestCase):
    def side(self):
        side = Side.__new__(Side)
        side.root = Path("/private/fixture")
        side.workspace = "ws_fixture"
        side.prefix = "sp6-process-contract/0123456789abcdef"
        side.scenario = "scope"
        side.records, side.jobs, side.batch_paths, side.batch_tags = [], {}, {}, {}
        side.previous_jobs = {}
        side.owned_jobs, side.owned_contexts = set(), {}
        return side

    def test_private_endpoint_is_exact_before_any_filesystem_access(self):
        for label, endpoint in [
            ("reference", "http://127.0.0.1:7210/mcp"),
            ("candidate", "http://127.0.0.1:7211/mcp"),
            ("candidate", "https://127.0.0.1:7210/mcp"),
            ("candidate", "http://127.0.0.1:7210/other"),
            ("candidate", "http://user@127.0.0.1:7210/mcp"),
            ("candidate", "http://127.0.0.1:7210/mcp?redirect=elsewhere"),
            ("candidate", "http://127.0.0.1:7210/mcp#fragment"),
        ]:
            with self.subTest(endpoint=endpoint), self.assertRaises(ContractFailure):
                Side(label, endpoint, "/not-read-token", "/not-read-root", "fixture", "0123456789abcdef")

    def test_redirect_never_receives_private_authorization(self):
        with self.assertRaises(ContractFailure):
            RefusePrivateRedirect().redirect_request(None, None, 307, "redirect", {}, "https://example.invalid/mcp")

    def test_batch_definition_cannot_escape_root_or_follow_symlink(self):
        side = self.side()
        with tempfile.TemporaryDirectory() as directory:
            side.root = Path(directory) / "fixture"
            side.root.mkdir()
            outside = Path(directory) / "other.json"
            outside.write_text("not owned")
            (side.root / "alias.json").symlink_to(outside)
            for name in (str(outside), "../other.json", "alias.json"):
                with self.subTest(path=name), self.assertRaises(ContractFailure):
                    side.private_file(name)
            self.assertEqual(outside.read_text(), "not owned")

    def test_digest_job_never_becomes_cleanup_owned(self):
        side = self.side()
        side.observe({"background_jobs": [{"job_id": "job_1234abcd"}]})
        self.assertEqual(side.owned_jobs, set())
        self.assertEqual(side.cleanup(), [])

    def test_foreign_job_metadata_prevents_stop(self):
        side = self.side()
        view = {"job_id": "job_1234abcd", "cwd": side.cwd, "command": "sleep 30",
                "started_at": "2026-10-31T18:00:00.000Z", "status": "running"}
        side.own_job(view, "sleep 30", side.cwd, side.workspace, False)
        calls = []
        class FakeClient:
            def call(self, name, arguments):
                calls.append(name)
                changed = {**view, "cwd": "another-session"}
                return {"body": {"result": {"structuredContent": {"workspace_id": side.workspace, "jobs": [changed]}}}}
        side.client = FakeClient()
        self.assertTrue(side.cleanup())
        self.assertEqual(calls, ["jobs"])
        self.assertEqual(side.owned_jobs, {view["job_id"]})

    def test_binding_reads_one_line_but_checks_complete_file_hash(self):
        for corrupt in (False, True):
            with self.subTest(corrupt=corrupt), tempfile.TemporaryDirectory() as directory:
                root = Path(directory) / "fixture"
                root.mkdir()
                token = Path(directory) / "token"
                token.write_text("local-test-token")
                class FakeClient:
                    closed = False
                    def call(self, name, arguments):
                        if name == "list_projects":
                            data = {"projects": [{"id": "contract", "workspace_id": "ws_fixture"}]}
                        elif name == "read":
                            self.read_arguments = arguments
                            body = (root / arguments["path"]).read_bytes()
                            data = {"text": "1 | " + body.decode().splitlines()[0],
                                    "sha256": "wrong" if corrupt else hashlib.sha256(body).hexdigest()}
                        elif name == "jobs":
                            data = {"jobs": []}
                        else:
                            raise AssertionError(name)
                        return {"body": {"result": {"structuredContent": data}}}
                    def close(self): self.closed = True
                client = FakeClient()
                with patch("process_contract_suite.Client", return_value=client):
                    if corrupt:
                        with self.assertRaises(ContractFailure):
                            Side("candidate", "http://127.0.0.1:7210/mcp", token, root, "contract", "0123456789abcdef")
                        self.assertTrue(client.closed)
                    else:
                        side = Side("candidate", "http://127.0.0.1:7210/mcp", token, root, "contract", "0123456789abcdef")
                        self.assertEqual(side.workspace, "ws_fixture")
                    self.assertEqual(client.read_arguments["end_line"], 1)


if __name__ == "__main__":
    unittest.main()
