import copy
import json
import os
from pathlib import Path
import tempfile
import time
import types
import unittest
from unittest.mock import Mock, patch

import retained_gaps_contract_suite as lane
import managed_contract_suite as managed_lane


POLICY = {"source_commit": lane.SOURCE_COMMIT, "source_sha256": "a" * 64,
          "environment": {"CODEXPRO_IMPORT_ALLOW_LOOPBACK": "1"}}


class RetainedGapGuards(unittest.TestCase):
    def test_case_units_are_explicit_and_distinct(self):
        self.assertEqual(len(lane.DIRECT_CASES), 8)
        self.assertEqual(len(lane.IMPORT_CASES), 16)
        cases = lane.DIRECT_CASES + lane.IMPORT_CASES
        self.assertEqual(len(cases), len(set(cases)))
        for index in range(len(cases)):
            self.assertTrue(callable(lane.case_function(index)))

    def test_missing_or_unproven_import_policy_refuses_launch(self):
        for policy in (None, {}, {**POLICY, "source_commit": "wrong"},
                       {**POLICY, "source_sha256": "not-sha"},
                       {**POLICY, "environment": {"UNPROVEN_OVERRIDE": "1"}}):
            with self.subTest(policy=policy):
                with self.assertRaises(lane.ContractFailure):
                    lane.verify_policy(policy)
        env = lane.verify_policy(POLICY)
        env["CODEXPRO_IMPORT_ALLOW_LOOPBACK"] = "changed"
        self.assertEqual(POLICY["environment"]["CODEXPRO_IMPORT_ALLOW_LOOPBACK"], "1")

    def test_occupied_port_refusal_precedes_every_state_write(self):
        with tempfile.TemporaryDirectory(prefix="sp6-retained-gaps-") as directory:
            base = Path(directory)
            release = base / "release-44bb76b"
            (release / "dist").mkdir(parents=True)
            (release / "package.json").write_text('{"version":"0.31.0"}')
            (release / "dist/http.js").write_text("// immutable fixture")
            binary = base / "candidate"
            binary.write_text("#!/bin/sh\nexit 0\n")
            binary.chmod(0o700)
            state, output = base / "new-state", base / "report.json"
            with patch.object(lane, "IMPORT_POLICY", POLICY), \
                 patch.object(lane, "assert_free_ports", side_effect=lane.ContractFailure("occupied")), \
                 patch.object(lane, "AttachmentFixture") as listener, \
                 patch.object(lane, "DirectServer") as server:
                with self.assertRaisesRegex(lane.ContractFailure, "occupied"):
                    lane.Pair(release, binary, state, output)
            listener.assert_not_called(); server.assert_not_called()
            self.assertFalse(state.exists()); self.assertFalse(output.exists())

    def test_fixture_bytes_headers_and_redirect_boundaries(self):
        code, headers, payload = lane.request_spec("/text")
        self.assertEqual(code, 200)
        self.assertEqual(payload, lane.TEXT)
        self.assertEqual(int(headers["Content-Length"]), len(payload))
        self.assertEqual(lane.request_spec("/redirect"),
                         (302, {"Location": "/png", "Content-Length": "0"}, b""))
        self.assertEqual(len(lane.request_spec("/exact")[2]), lane.IMPORT_LIMIT)
        self.assertEqual(len(lane.request_spec("/oversized")[2]), lane.IMPORT_LIMIT + 1)
        self.assertEqual(lane.request_spec("/oversized")[1]["Content-Length"], str(lane.IMPORT_LIMIT + 1))
        self.assertNotIn("Content-Length", lane.request_spec("/streamed")[1])
        self.assertEqual(lane.request_spec("/not-found")[0], 404)
        self.assertEqual(lane.request_spec("/http-not-found")[0], 404)
        self.assertEqual(lane.request_spec("/missing-location"),
                         (302, {"Content-Length": "0"}, b""))
        self.assertEqual(lane.request_spec("/redirect-loop"),
                         (302, {"Location": "/redirect-loop", "Content-Length": "0"}, b""))

    def test_fixture_never_accepts_external_or_credential_query_routes(self):
        for path in ("http://localhost/text", "//other/text", "/text?token=secret", "/png#fragment"):
            with self.subTest(path=path), self.assertRaises(lane.ContractFailure):
                lane.request_spec(path)

    def test_cleanup_receipt_is_cached_before_any_second_signal(self):
        server = lane.DirectServer.__new__(lane.DirectServer)
        server.stopped_report = None
        receipt = {"stopped": True, "owned_group_quiescent": True}
        with patch.object(lane.OwnedServer, "stop", return_value=receipt) as stop:
            self.assertIs(server.stop(), receipt)
            self.assertIs(server.stop(), receipt)
        stop.assert_called_once()


class DirectSessionValidation(unittest.TestCase):
    def test_owned_instruction_alias_preserves_unknown_paths_and_tool_mode(self):
        with tempfile.TemporaryDirectory(prefix="sp6-retained-gaps-alias-") as directory:
            account = Path(directory)
            sides = []
            for label in ("reference", "candidate"):
                state = account / "proof" / label
                codex = state / "empty-codex"
                codex.mkdir(parents=True)
                side = lane.Side.__new__(lane.Side)
                side.manifest = {"pid": 222, "process_start_ticks": 17, "state_root": str(state)}
                side.aliases, side.normalizations = {}, []
                roles = {"codex_dir": str(codex), "account_home": str(account)}
                with patch.object(managed_lane, "process_start_ticks", return_value=17), \
                     patch.object(managed_lane, "process_path_binding", return_value=roles):
                    side.bind_instruction_directory()
                home_path = "[home]/proof/" + label + "/empty-codex"
                self.assertEqual(side.aliases, {str(codex): "<private-codex-dir>",
                                               home_path: "<private-codex-dir>"})
                self.assertNotIn("[home]", side.aliases)
                unknown = {"same_run": "[home]/proof/" + label + "/other-codex",
                           "other_run": "[home]/another-run/empty-codex",
                           "source_label": "[codex-data]", "nullable": None}
                self.assertEqual(side.normalize(unknown), unknown)
                self.assertEqual(side.normalizations[0]["codex_dir"], str(codex))
                text = "Global instructions: none found in " + home_path + ".\nTool mode: "
                sides.append((side, text))
            reference = sides[0][0].normalize({"text": sides[0][1] + "full", "tool_mode": "full"})
            candidate = sides[1][0].normalize({"text": sides[1][1] + "standard", "tool_mode": "full"})
            self.assertNotEqual(reference, candidate)
            self.assertTrue(candidate["text"].endswith("Tool mode: standard"))
            candidate = sides[1][0].normalize({"text": sides[1][1] + "full", "tool_mode": "standard"})
            self.assertNotEqual(reference, candidate)
            self.assertEqual(candidate["tool_mode"], "standard")

    def test_owned_instruction_alias_rejects_wrong_path_or_changed_process(self):
        with tempfile.TemporaryDirectory(prefix="sp6-retained-gaps-alias-") as directory:
            account = Path(directory); state = account / "proof" / "candidate"
            codex = state / "empty-codex"; codex.mkdir(parents=True)
            other = state / "other-codex"; other.mkdir()
            side = lane.Side.__new__(lane.Side)
            side.manifest = {"pid": 222, "process_start_ticks": 17, "state_root": str(state)}
            side.aliases, side.normalizations = {}, []
            roles = {"codex_dir": str(other), "account_home": str(account)}
            with patch.object(managed_lane, "process_start_ticks", return_value=17), \
                 patch.object(managed_lane, "process_path_binding", return_value=roles):
                with self.assertRaisesRegex(lane.ContractFailure, "differs from the owned"):
                    side.bind_instruction_directory()
            roles["codex_dir"] = str(codex)
            with patch.object(managed_lane, "process_start_ticks", side_effect=[17, 18]), \
                 patch.object(managed_lane, "process_path_binding", return_value=roles):
                with self.assertRaisesRegex(lane.ContractFailure, "changed during"):
                    side.bind_instruction_directory()
            self.assertEqual(side.aliases, {})
            self.assertEqual(side.normalizations, [])

    def side(self):
        side = lane.Side.__new__(lane.Side)
        side.manifest = {"project_id": "contract"}
        side.workspaces = {"contract": "ws_" + "1" * 24}
        side.aliases = {side.workspace: "<workspace:contract>"}
        now = int(time.time() * 1000)
        side.session_windows = {"alpha": now - 200, "beta": now - 100}
        side.initial_shape, side.opened = None, {}
        return side

    def response(self, side, *, count=1, when=None, extra=None):
        when = when or lane.iso_now()
        data = {"workspaces": [], "count": count,
                "selected_workspace_id": side.workspace if count else None}
        text = ""
        if count:
            data["workspaces"].append({"id": side.workspace, "root": "[workspace:" + side.workspace + "]",
                "projectId": "contract", "kind": "direct", "openedAt": when})
            text = "- " + side.workspace + " — [workspace:" + side.workspace + "] project=contract (selected) (opened " + when + ")"
        if extra:
            data.update(extra)
        return {"http_status": 200, "content_type": "application/json", "duration_ms": 3.0,
                "body": {"jsonrpc": "2.0", "id": 4, "result": {
                    "_meta": {}, "content": [{"type": "text", "text": text}], "structuredContent": data}}}

    def test_open_timestamp_normalization_preserves_every_other_field(self):
        side = self.side()
        raw = self.response(side, extra={"optional_probe": None})
        side.mcp = Mock(return_value=raw)
        normalized = side.workspace_list("alpha", expected_count=1)
        self.assertIn("optional_probe", normalized["body"]["result"]["structuredContent"])
        self.assertIsNone(normalized["body"]["result"]["structuredContent"]["optional_probe"])
        self.assertIn("_meta", normalized["body"]["result"])
        self.assertNotIn("isError", normalized["body"]["result"])
        self.assertEqual(normalized["body"]["result"]["structuredContent"]["workspaces"][0]["openedAt"],
                         "<validated-openedAt:contract>")
        self.assertTrue(normalized["body"]["result"]["content"][0]["text"].endswith(
            "(opened <validated-openedAt:contract>)"))
        self.assertNotEqual(raw, normalized)
        self.assertEqual(raw["body"]["id"], 4)

    def test_repeated_open_cannot_reset_the_original_time(self):
        side = self.side(); raw = self.response(side)
        side.mcp = Mock(return_value=raw)
        side.workspace_list("alpha", expected_count=1)
        changed = copy.deepcopy(raw)
        original = changed["body"]["result"]["structuredContent"]["workspaces"][0]["openedAt"]
        replacement = "2026-10-31T00:00:00.000Z"
        side.opened["alpha"] = replacement
        self.assertNotEqual(original, replacement)
        with self.assertRaisesRegex(lane.ContractFailure, "existing session"):
            side.workspace_list("alpha", expected_count=1)

    def test_new_session_does_not_inherit_an_opened_workspace(self):
        side = self.side()
        side.mcp = Mock(return_value=self.response(side, count=0))
        side.workspace_list("alpha", first=True)
        side.mcp = Mock(return_value=self.response(side))
        with self.assertRaisesRegex(lane.ContractFailure, "leaked"):
            side.workspace_list("beta", first=True)

    def test_list_rejects_unbound_workspace_and_wrong_text_clock(self):
        for change in ("workspace", "clock"):
            side = self.side(); raw = self.response(side)
            if change == "workspace":
                raw["body"]["result"]["structuredContent"]["workspaces"][0]["id"] = "ws_other"
            else:
                raw["body"]["result"]["content"][0]["text"] = "unbound time"
            side.mcp = Mock(return_value=raw)
            with self.subTest(change=change), self.assertRaises(lane.ContractFailure):
                side.workspace_list("alpha", expected_count=1)


class ImportResultValidation(unittest.TestCase):
    def side(self, directory, route="/text"):
        side = lane.Side.__new__(lane.Side)
        side.root = Path(directory)
        side.import_root = side.root / lane.IMPORT_PREFIX
        side.import_root.mkdir()
        side.manifest = {"project_id": "contract"}
        side.workspaces = {"contract": "ws_" + "1" * 24}
        side.aliases = {side.workspace: "<workspace:contract>"}
        code, headers, body = lane.request_spec(route)
        attachment = Mock(origin="http://127.0.0.1:39999")
        attachment.count.return_value = 0
        attachment.since.return_value = [{"method": "GET", "path": route, "status": code, "bytes": len(body)}]
        side.pair = types.SimpleNamespace(attachments=attachment)
        return side

    def success(self, side, args, payload=lane.TEXT):
        path = side.root / args["destination"]
        previous = path.exists()
        path.write_bytes(payload)
        data = {"workspace_id": side.workspace, "root": "[workspace:" + side.workspace + "]",
            "path": args["destination"], "bytes": len(payload), "sha256": lane.sha(payload),
            "file_id": args["file"]["file_id"], "declared_mime_type": "text/plain",
            "detected_mime_type": None, "mime_type_status": "unknown",
            "verified": "expected_sha256" in args, "overwritten": bool(args.get("overwrite") and previous),
            "file_name": None, "additional_exact_field": {"value": None}}
        return {"http_status": 200, "content_type": "application/json", "duration_ms": 2.0,
            "body": {"jsonrpc": "2.0", "id": 8, "result": {"content": [
                {"type": "text", "text": "full exact result text"}], "structuredContent": data}}}

    def test_success_binds_raw_file_hash_and_keeps_extra_optional_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.side(directory)
            side.mcp = lambda tool, args, session: self.success(side, args)
            result = side.import_one("/text", "text.txt", lane.TEXT)
            self.assertEqual(result["destination"], {"bytes": len(lane.TEXT), "sha256": lane.sha(lane.TEXT)})
            data = result["response"]["body"]["result"]["structuredContent"]
            self.assertEqual(data["additional_exact_field"], {"value": None})
            self.assertIsNone(data["detected_mime_type"])
            self.assertEqual(result["response"]["body"]["result"]["content"][0]["text"], "full exact result text")

    def test_missing_null_field_and_wrong_hash_are_never_normalized(self):
        for defect in ("omit-null", "wrong-hash", "wrong-bytes"):
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as directory:
                side = self.side(directory)
                def call(tool, args, session):
                    value = self.success(side, args)
                    data = value["body"]["result"]["structuredContent"]
                    if defect == "omit-null": del data["detected_mime_type"]
                    elif defect == "wrong-hash": data["sha256"] = "0" * 64
                    else: (side.root / args["destination"]).write_bytes(b"wrong file")
                    return value
                side.mcp = call
                with self.assertRaises(lane.ContractFailure):
                    side.import_one("/text", "text.txt", lane.TEXT)

    def test_error_cannot_change_existing_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.side(directory)
            path = side.import_root / "text.txt"; path.write_bytes(b"original")
            def call(tool, args, session):
                path.write_bytes(b"changed despite error")
                return {"body": {"result": {"isError": True}}}
            side.mcp = call
            with self.assertRaisesRegex(lane.ContractFailure, "changed the destination"):
                side.import_one("/text", "text.txt", lane.TEXT, error=True)

    def test_symlink_destination_cannot_be_used_as_a_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.side(directory)
            outside = side.root / "outside"; outside.write_bytes(b"outside")
            (side.import_root / "linked.txt").symlink_to(outside)
            with self.assertRaises(lane.ContractFailure):
                side.file_bytes(lane.IMPORT_PREFIX + "/linked.txt")

    def test_unexpected_download_or_credential_forwarding_is_an_error(self):
        for events in ([{"credential_forwarded": True}],
                       [{"path": "/text"}, {"path": "/unexpected"}]):
            with self.subTest(events=events), tempfile.TemporaryDirectory() as directory:
                side = self.side(directory)
                side.pair.attachments.since.return_value = events
                side.mcp = lambda tool, args, session: self.success(side, args)
                with self.assertRaisesRegex(lane.ContractFailure, "uncredentialed attachment"):
                    side.import_one("/text", "text.txt", lane.TEXT)

    def test_invalid_url_requires_exact_native_envelope_and_zero_effects(self):
        for defect in (None, "domain-name", "generic-name", "code", "download", "destination"):
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as directory:
                side = self.side(directory)
                side.pair.attachments.since.return_value = []
                result = {"isError": True,
                    "content": [{"type": "text", "text": "TypeError: Invalid URL"}],
                    "structuredContent": {"codexpro_tool": "import_file",
                        "codexpro_title": "Import Attachment File", "error": "TypeError: Invalid URL"}}
                if defect in ("domain-name", "generic-name"):
                    message = ("CodexProError" if defect == "domain-name" else "Error") + ": Invalid URL"
                    result["content"][0]["text"] = message
                    result["structuredContent"]["error"] = message
                if defect == "code":
                    result["structuredContent"]["error_code"] = "INVENTED"
                if defect == "download":
                    side.pair.attachments.since.return_value = [{"path": "/text"}]
                response = {"http_status": 200, "content_type": "application/json", "duration_ms": 0.1,
                    "body": {"jsonrpc": "2.0", "id": 7, "result": result}}
                def call(tool, args, session):
                    self.assertEqual(args["file"]["download_url"], "not a URL")
                    if defect == "destination":
                        (side.import_root / "invalid-url.bin").write_bytes(b"unexpected")
                    return response
                side.mcp = call
                if defect:
                    with self.assertRaises(lane.ContractFailure):
                        side.invalid_url_import()
                else:
                    value = side.invalid_url_import()
                    self.assertIsNone(value["destination"])
                    self.assertEqual(value["fixture_requests"], [])
                    self.assertEqual(value["response"]["body"]["result"], result)


if __name__ == "__main__":
    unittest.main()
