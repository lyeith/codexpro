import base64
import copy
import gzip
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import paired_contract_suite as suite


WORKSPACE = "ws_0123456789abcdef01234567"
SEARCH_SIGNATURE = "ce4ba49807da78ecbe9c6b0423bce75971f2320a5385c6841aa9c4f42c232933"
AST_SIGNATURE = "1b8a5686fa12770427f154dc5921d16ca1f0bb79a4228c474d60c00a677df1d2"
SEARCH_CURSOR = "eyJ2IjoxLCJzaWduYXR1cmUiOiJjZTRiYTQ5ODA3ZGE3OGVjYmU5YzZiMDQyM2JjZTc1OTcxZjIzMjBhNTM4NWM2ODQxYWE5YzRmNDJjMjMyOTMzIiwiYWZ0ZXIiOiJhLnR4dFx1MDAwMDAwMDAwMDAwMDAwMVx1MDAwMDAwMDAwMDAwMDAwMVx1MDAwMHRleHRcdTAwMDAiLCJjaGVja3N1bSI6ImVjNWFlYmM2ZjViNjg5NTBmYWM1In0"
AST_CURSOR = "eyJ2IjoxLCJzaWduYXR1cmUiOiIxYjhhNTY4NmZhMTI3NzA0MjdmMTU0ZGM1OTIxZDE2Y2ExZjBiYjc5YTQyMjhjNDc0ZDYwYzAwYTY3N2RmMWQyIiwib2Zmc2V0IjoxLCJjaGVja3N1bSI6IjRkNTVhNTk4YzFhZjMwOGFjOTU4In0"


class DummyClient:
    credential = "never-log-the-private-credential"
    session = "never-log-the-private-session"

    def assert_no_credential(self, value):
        encoded = json.dumps(value)
        if self.credential in encoded or self.session in encoded:
            raise RuntimeError("credential in evidence")

    def close(self):
        pass


def fixture_side(base):
    side = suite.Side.__new__(suite.Side)
    side.root = base / "fixture"
    side.state = base / "state"
    side.root.mkdir()
    side.state.mkdir()
    side.root_identity = (side.root.stat().st_dev, side.root.stat().st_ino)
    side.state_identity = (side.state.stat().st_dev, side.state.stat().st_ino)
    side.label = "candidate"
    side.run_id = "fixture-run"
    side.backup = side.lock = None
    side.owned_fixture = side.unsettled = False
    side.guard_owned = []
    side.guard_paths = [side.root.parent / "outside", side.root.parent / "escape.txt"]
    side.guard_payload = b"private guard fixture\n"
    side.case_clients = {}
    side.clients = {}
    side.bootstrap = DummyClient()
    side.workspace = WORKSPACE
    side.dynamic_aliases = {}
    side.bindings = {"workspace": WORKSPACE}
    return side


def envelope(data, text="", *, request_id=3):
    return {"http_status": 200, "content_type": "application/json", "duration_ms": 2.5,
            "body": {"jsonrpc": "2.0", "id": request_id, "result": {
                "_meta": {}, "content": [{"type": "text", "text": text}], "structuredContent": data}}}


class ContractHarnessGuards(unittest.TestCase):
    def test_corpus_is_complete_and_known(self):
        suite.validate_cases(suite.CASES)
        self.assertEqual(len(suite.CASES), 20)
        self.assertEqual(sum(len(case["steps"]) for case in suite.CASES), 94)

    def test_fixture_paths_and_unknown_setup_refused(self):
        for path in ("/tmp/file", "../file", "a/../../file", ".git/config", "C:/file", "a\\file", "a//file", ""):
            with self.subTest(path=path), self.assertRaises(suite.ContractFailure):
                suite.relative_path(path)
        self.assertEqual(str(suite.relative_path(".codex/skills/demo/SKILL.md")), ".codex/skills/demo/SKILL.md")
        case = copy.deepcopy(suite.CASES[0])
        case["setup"] = {"shell": "untrusted"}
        with self.assertRaises(suite.ContractFailure):
            suite.validate_cases([case])

    def test_private_endpoint_rejects_live_before_access(self):
        for endpoint in ("http://127.0.0.1:7200/mcp", "https://127.0.0.1:7210/mcp",
                         "http://remote.test:7210/mcp", "http://127.0.0.1:7210/mcp?token=x",
                         "http://user:pass@127.0.0.1:7210/mcp"):
            with self.subTest(endpoint=endpoint), self.assertRaises(suite.ContractFailure):
                suite.validate_launch({}, endpoint, "/missing/token", "/missing/fixture", 7210)

    def test_private_path_and_evidence_follow_canonical_live_roots(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "home"
            live = home / ".codexpro"
            live.mkdir(parents=True)
            alias = home / "alias"
            alias.symlink_to(live, target_is_directory=True)
            with patch.dict(os.environ, {"HOME": str(home)}):
                with self.assertRaises(suite.ContractFailure):
                    suite.private_path(alias)
                with self.assertRaises(suite.ContractFailure):
                    suite.artifact_path(alias / "results.json")
                self.assertEqual(suite.private_path(home / ".codexpro-go", missing=True), home / ".codexpro-go")
                self.assertEqual(suite.artifact_path(home / "Projects/codexpro-go/results.json"),
                                 home / "Projects/codexpro-go/results.json")

    def test_json_pointer_ref_bindings_and_omissions(self):
        data = {"a/b~c": [{"value": None}]}
        self.assertIsNone(suite.get_pointer(data, "/a~1b~0c/0/value"))
        with self.assertRaises(suite.ContractFailure):
            suite.get_pointer(data, "/a~1b~0c/1")
        suite.replace_pointer(data, "/a~1b~0c/0/value", "new")
        self.assertEqual(data["a/b~c"][0]["value"], "new")
        value = suite.resolve_refs({"tag": {"$ref": "tag"}, "path": "$WORKSPACE/a.txt"},
                                   {"tag": "ABCD"}, Path("/private/fixture"))
        self.assertEqual(value, {"tag": "ABCD", "path": "/private/fixture/a.txt"})
        with self.assertRaises(suite.ContractFailure):
            suite.resolve_refs({"$ref": "missing"}, {}, Path("/private"))

    def test_decoded_comparison_preserves_type_and_presence(self):
        for left, right in (({"x": True}, {"x": 1}), ({"x": None}, {}), ({"x": []}, {"x": None}),
                            ({"x": "a\n"}, {"x": "a"}), ([1, 2], [2, 1])):
            with self.subTest(left=left, right=right):
                self.assertIsNotNone(suite.difference(left, right))
        self.assertIsNone(suite.difference({"a": 1, "b": 2}, {"b": 2, "a": 1}))

    def test_source_node_fingerprint_goldens(self):
        search = suite.signature_payload("search", {"query": "café ✓", "path": "src"}, WORKSPACE)
        self.assertEqual(suite.digest(suite.source_json(search)), SEARCH_SIGNATURE)
        ast = suite.signature_payload("ast_grep", {
            "pattern": "console.log($ARG)", "language": "typescript",
            "context_before": 0, "context_after": 0}, WORKSPACE, "0.40.5")
        self.assertEqual(suite.digest(suite.source_json(ast)), AST_SIGNATURE)
        self.assertEqual(suite.encode_cursor(suite.decode_cursor(SEARCH_CURSOR, "search", SEARCH_SIGNATURE)),
                         SEARCH_CURSOR)
        self.assertEqual(suite.encode_cursor(suite.decode_cursor(AST_CURSOR, "ast_grep", AST_SIGNATURE)),
                         AST_CURSOR)

    def test_cursor_checks_canonical_encoding_checksum_and_causality(self):
        for cursor, signature in ((SEARCH_CURSOR + "=", SEARCH_SIGNATURE),
                                  (SEARCH_CURSOR, "0" * 64)):
            with self.subTest(cursor=cursor), self.assertRaises(suite.ContractFailure):
                suite.decode_cursor(cursor, "search", signature)
        tampered = suite.decode_cursor(SEARCH_CURSOR, "search", SEARCH_SIGNATURE)
        tampered["checksum"] = "0" * 20
        with self.assertRaises(suite.ContractFailure):
            suite.decode_cursor(suite.encode_cursor(tampered), "search", SEARCH_SIGNATURE)
        data = {"matches": [{"path": "a.txt", "line": 1, "source": "text"}]}
        normalized = suite.normalize_cursor(SEARCH_CURSOR, "search", SEARCH_SIGNATURE, "a" * 64, data, {})
        self.assertEqual(suite.decode_cursor(normalized, "search", "a" * 64)["after"],
                         suite.decode_cursor(SEARCH_CURSOR, "search", SEARCH_SIGNATURE)["after"])
        data["matches"][0]["line"] = 2
        with self.assertRaises(suite.ContractFailure):
            suite.normalize_cursor(SEARCH_CURSOR, "search", SEARCH_SIGNATURE, "a" * 64, data, {})
        with self.assertRaises(suite.ContractFailure):
            suite.normalize_cursor(AST_CURSOR, "ast_grep", AST_SIGNATURE, "b" * 64,
                                   {"matches": [{}, {}]}, {})

    def test_inventory_counts_approved_removals_separately(self):
        def response(tools):
            return {"body": {"jsonrpc": "2.0", "id": 3, "result": {"tools": tools}},
                    "http_status": 200, "content_type": "application/json", "duration_ms": 1.0}
        retained = {"name": "read", "inputSchema": {"type": "object"}}
        retired = {"name": "work_update", "inputSchema": {"type": "object"}}
        value = suite.compare_inventory(response([retained, retired]), response([retained]))
        self.assertEqual(value["status"], "approved_delta")
        self.assertEqual(value["removed_descriptors"], ["work_update"])
        changed = copy.deepcopy(retained)
        changed["inputSchema"]["additionalProperties"] = False
        self.assertEqual(suite.compare_inventory(response([retained]), response([changed]))["status"], "fail")
        with self.assertRaises(suite.ContractFailure):
            suite.compare_inventory(response([retained, retired]), response([retained, retired]))
        candidate = response([retained])
        candidate["body"]["result"]["extra"] = None
        self.assertEqual(suite.compare_inventory(response([retained]), candidate)["status"], "fail")

    def test_request_id_echo_is_proved_before_normalization(self):
        side = suite.Side.__new__(suite.Side)
        side.validate_response(envelope({}, request_id=3), 3)
        with self.assertRaises(suite.ContractFailure):
            side.validate_response(envelope({}, request_id=4), 3)
        malformed = envelope({}, request_id=3)
        malformed["body"]["id"] = True
        with self.assertRaises(suite.ContractFailure):
            side.validate_response(malformed, 1)

    def test_raw_evidence_refuses_secrets_and_preserves_empty_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "raw.jsonl.gz"
            evidence = suite.Evidence(path, lambda: [DummyClient()])
            try:
                with self.assertRaises(RuntimeError):
                    evidence.append({"text": DummyClient.credential})
                self.assertEqual(evidence.records, 0)
                evidence.append({"value": None, "empty": [], "object": {}, "text": "a\n"})
            finally:
                evidence.close()
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            record = json.loads(gzip.open(path, "rt").read())
            self.assertEqual(record, {"value": None, "empty": [], "object": {}, "text": "a\n"})

    def test_fixture_backup_restore_keeps_original_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            (side.root / "original.txt").write_bytes(b"original\n")
            side.claim()
            self.assertEqual(list(side.root.iterdir()), [])
            (side.root / "mutation.txt").write_bytes(b"new\n")
            self.assertEqual(side.restore(), [])
            self.assertEqual((side.root / "original.txt").read_bytes(), b"original\n")
            self.assertFalse((side.root / "mutation.txt").exists())
            self.assertIsNone(side.backup)

    def test_partial_backup_failure_never_deletes_unmoved_originals(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            (side.root / "a.txt").write_text("a")
            (side.root / "b.txt").write_text("b")
            actual_replace = os.replace
            count = 0
            def fail_second(source, target):
                nonlocal count
                count += 1
                if count == 2:
                    raise OSError("injected move failure")
                return actual_replace(source, target)
            with patch.object(suite.os, "replace", side_effect=fail_second):
                with self.assertRaises(OSError):
                    side.claim()
            self.assertFalse(side.owned_fixture)
            self.assertEqual(side.restore(), [])
            self.assertEqual((side.root / "a.txt").read_text(), "a")
            self.assertEqual((side.root / "b.txt").read_text(), "b")

    def test_unsettled_tool_preserves_backup_and_prevents_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            (side.root / "original.txt").write_text("original")
            side.claim()
            (side.root / "current.txt").write_text("in flight")
            side.unsettled = True
            errors = side.restore()
            self.assertEqual(len(errors), 1)
            self.assertTrue((side.backup / "originals/original.txt").exists())
            self.assertTrue((side.root / "current.txt").exists())
            side.unsettled = False
            self.assertEqual(side.restore(), [])
            self.assertEqual((side.root / "original.txt").read_text(), "original")

    def test_parent_escape_targets_are_reserved_checked_and_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            side.claim()
            self.assertTrue(all(value["unchanged"] for value in side.guard_snapshot()))
            side.guard_paths[0].write_text("unexpected write")
            self.assertFalse(side.guard_snapshot()[0]["unchanged"])
            self.assertEqual(side.restore(), [])
            self.assertTrue(all(not os.path.lexists(path) for path in side.guard_paths))

    def test_parent_guard_collision_preserves_preexisting_file(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            side.guard_paths[1].write_text("preexisting unrelated data")
            with self.assertRaises(FileExistsError):
                side.claim()
            self.assertEqual(side.restore(), [])
            self.assertEqual(side.guard_paths[1].read_text(), "preexisting unrelated data")

    def test_commit_hash_is_tied_to_object_and_bounded_dates(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            stamp = 1735689600
            raw = ("tree " + "b" * 40 + "\nparent " + "c" * 40
                   + "\nauthor Fixture <fixture@example.invalid> " + str(stamp)
                   + " +0000\ncommitter Fixture <fixture@example.invalid> " + str(stamp)
                   + " +0000\n\nfix: fixture\n")
            encoded = raw.encode()
            commit = hashlib.sha1(b"commit " + str(len(encoded)).encode() + b"\x00" + encoded).hexdigest()
            side.git = lambda *args: commit + "\n" if args[:2] == ("rev-parse", "HEAD") else raw
            alias, semantic = side.commit_value(commit, stamp * 1000, stamp * 1000 + 500)
            self.assertIn("<call-time>", semantic)
            self.assertTrue(alias.startswith("<commit:"))
            with self.assertRaises(suite.ContractFailure):
                side.commit_value(commit, (stamp + 10) * 1000, (stamp + 11) * 1000)
            response = envelope({"commit": commit, "commit_short": commit[:12]},
                                "Commit: " + commit + "\n[main " + commit[:7] + "] fix: fixture")
            result = side.normalize({"dynamic": ["/result/structuredContent/commit",
                                                 "/result/structuredContent/commit_short"], "tool": "commit_changes"},
                                    {}, response, stamp * 1000, stamp * 1000 + 500)
            data = result["response"]["body"]["result"]["structuredContent"]
            self.assertEqual(data["commit_short"], data["commit"] + ":12")
            self.assertNotIn(commit[:7], result["response"]["body"]["result"]["content"][0]["text"])

    def test_cache_keys_require_actual_inventory_and_exact_limits(self):
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            (side.root / "a.txt").write_text("old\n")
            side.workflow = {"id": "GIT-REVIEW-SESSION-18"}
            side.analysis_limits = {"maxInventoryFiles": 20000, "maxAnalyzedFiles": 5000,
                                    "maxScannedBytes": 67108864, "maxSymbols": 100000,
                                    "maxRelationships": 250000}
            fingerprint = suite.digest("a.txt:4:1735689600000")
            observed = {"fingerprint": fingerprint, "entries": [{
                "path": "a.txt", "bytes": 4, "modifiedMs": 1735689600000, "sha256": suite.digest("old\n")}]}
            side.command = lambda *args, **kwargs: json.dumps(observed)
            key = WORKSPACE + ":" + fingerprint + ":" + suite.source_json(side.analysis_limits)
            normalized, evidence = side.cache_key(key)
            self.assertTrue(normalized.startswith("<workspace>:"))
            self.assertEqual(evidence["inventory"][0]["sha256"], suite.digest("old\n"))
            for bad in ("ws_" + "0" * 24 + key[len(WORKSPACE):],
                        key.replace(fingerprint, "f" * 64),
                        key.replace('"maxSymbols":100000', '"maxSymbols":99999')):
                with self.subTest(key=bad), self.assertRaises(suite.ContractFailure):
                    side.cache_key(bad)

    def test_inventory_helper_uses_node_stat_precision_and_collation(self):
        if not suite.shutil.which("node"):
            self.skipTest("exact inventory oracle requires the reference Node runtime")
        with tempfile.TemporaryDirectory() as directory:
            side = fixture_side(Path(directory))
            for name, content in (("z.txt", "z\n"), ("é.txt", "café\n")):
                path = side.root / name
                path.write_bytes(content.encode())
                os.utime(path, ns=(1735689600000000000, 1735689600000000000))
            observed = json.loads(side.command(["node", "-e", suite.INVENTORY_SCRIPT,
                                               '["z.txt","é.txt"]'], limit=65536))
            self.assertEqual([entry["path"] for entry in observed["entries"]], ["é.txt", "z.txt"])
            self.assertEqual(observed["fingerprint"],
                             suite.digest("é.txt:6:1735689600000\nz.txt:2:1735689600000"))


if __name__ == "__main__":
    unittest.main()
