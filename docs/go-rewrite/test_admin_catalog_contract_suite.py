import copy
import email.message
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch

import admin_catalog_contract_suite as lane
from paired_contract_suite import ContractFailure, difference


def response(data, text=""):
    return {"http_status": 200, "content_type": "application/json",
            "duration_ms": 0.25, "body": {"jsonrpc": "2.0", "id": 8,
            "result": {"content": [{"type": "text", "text": text}],
                       "structuredContent": data, "_meta": {}}}}


def receipt(number=1, **updates):
    action = {
        "schema_version": "1.0", "namespace": "debug", "sequence": number,
        "action_id": "cpa_" + str(number).zfill(32), "project_id": "test",
        "workspace_id": "ws_" + "a" * 24, "tool_name": "write",
        "mutating": True, "status": "succeeded",
        "occurred_at": "2026-10-31T18:00:00.001Z",
        "finished_at": "2026-10-31T18:00:00.002Z",
        "duration_ms": 1, "actor_ref": "actor_" + "b" * 32,
        "changed_paths": ["a.txt"], "changed_path_count": 1,
    }
    action.update(updates)
    return action


class DummyClient:
    credential = "private-test-credential-never-evidence"
    session = "private-test-session"

    def assert_no_credential(self, value):
        encoded = json.dumps(value)
        if self.credential in encoded or self.session in encoded:
            raise ContractFailure("credential appeared in evidence")

    def close(self):
        pass


class Reply:
    def __init__(self, body, status=200):
        self.body, self.status = body, status
        self.headers = email.message.Message()
        self.headers["Content-Type"] = "application/json; charset=utf-8"
        self.headers["Content-Length"] = str(len(body))
        self.headers["Cache-Control"] = "no-store"

    def read(self, limit):
        return self.body[:limit]

    def __enter__(self):
        return self

    def __exit__(self, *args):
        return False


class AdminContractTests(unittest.TestCase):
    def minimal_side(self):
        side = lane.Side.__new__(lane.Side)
        side.label = "reference"
        side.port = 7211
        side.aliases = {"/private/reference": "<private-state>"}
        side.approved = []
        side.current = "test"
        side.normalizations = []
        side.tools = ["read", "work_manage", "work_status"]
        return side

    def test_explicit_launch_precedence_and_no_inferred_catalog(self):
        env = {"CODEXPRO_PROJECTS_FILE": "/private/env.json"}
        self.assertEqual(lane.explicit_setting(
            ["program", "--projects-file", "/private/first.json",
             "--projects-file=/private/last.json"], env,
            "projects-file", ["CODEXPRO_PROJECTS_FILE"]), "/private/last.json")
        self.assertEqual(lane.explicit_setting([], env, "projects-file",
            ["CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_PROJECTS_FILE"]), "/private/env.json")
        self.assertIsNone(lane.explicit_setting([], {}, "projects-file", ["CODEXPRO_PROJECTS_FILE"]))
        with self.assertRaises(ContractFailure):
            lane.cli_option(["--projects-file"], "projects-file")

    def test_private_roles_derive_from_process_paths_not_config_home_guesses(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            home, codex = state / "config", state / "instructions"
            home.mkdir(); codex.mkdir()
            catalog, limits = state / "projects.json", state / "limits.env"
            catalog.write_text("{}"); limits.write_text("")
            manifest = {"state_root": str(state), "config_home": str(home), "projects_file": str(catalog)}
            env = {"CODEXPRO_HOME": str(home), "CODEXPRO_PROJECTS_FILE": str(catalog),
                   "CODEXPRO_CODEX_DIR": str(codex), "CODEXPRO_ENV_FILE": str(limits)}
            bound = lane.private_launch_paths(manifest, [], env, state)
            self.assertEqual(bound["codex_dir"], str(codex))
            self.assertNotEqual(bound["codex_dir"], str(home / "empty-codex"))
            self.assertEqual(bound["work_dir"], str(home / "work"))
            self.assertEqual(bound["limits_file"], str(limits))
            missing = state / "configured-but-empty-instructions"
            absent = lane.private_launch_paths(manifest, [], {**env, "CODEXPRO_CODEX_DIR": str(missing)}, state)
            self.assertEqual(absent["codex_dir"], str(missing))
            self.assertFalse(missing.exists())
            with self.assertRaisesRegex(ContractFailure, "explicit private instruction"):
                lane.private_launch_paths(manifest, [], {k: v for k, v in env.items()
                    if k != "CODEXPRO_CODEX_DIR"}, state)
            with tempfile.TemporaryDirectory() as external:
                bad = {**env, "CODEXPRO_CODEX_DIR": external}
                with self.assertRaisesRegex(ContractFailure, "private instance state"):
                    lane.private_launch_paths(manifest, [], bad, state)
                link = state / "escaped-instructions"; link.symlink_to(external)
                with self.assertRaisesRegex(ContractFailure, "private instance state"):
                    lane.private_launch_paths(manifest, [], {**env, "CODEXPRO_CODEX_DIR": str(link)}, state)

    def test_private_aliases_include_only_bound_home_spellings_and_keep_contract_labels(self):
        with tempfile.TemporaryDirectory() as directory:
            account = Path(directory).resolve()
            reference, candidate = account / "reference", account / "candidate"
            reference.mkdir(); candidate.mkdir()
            left_path = reference / "state" / "work"
            right_path = candidate / "work"
            left = lane.private_role_aliases({str(left_path): "<private-work-dir>"}, account)
            right = lane.private_role_aliases({str(right_path): "<private-work-dir>"}, account)
            self.assertEqual(lane.replace_strings("[home]/reference/state/work", left), "<private-work-dir>")
            self.assertEqual(lane.replace_strings("[home]/candidate/work", right), "<private-work-dir>")
            # The normalizer does not strip an arbitrary home/state prefix or
            # collapse product-supplied labels into one placeholder.
            for text in ("[home]/reference/state/unknown", "[workspace:current]",
                         "[project:codexpro]", "[codex-data]"):
                self.assertEqual(lane.replace_strings(text, left), text)
            wrong = lane.replace_strings("[home]/candidate/work-unrelated", right)
            self.assertNotEqual(wrong, "<private-work-dir>")

    def test_private_role_aliases_reject_noncanonical_input(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve(); real = state / "real"; real.mkdir()
            link = state / "link"; link.symlink_to(real)
            with self.assertRaisesRegex(ContractFailure, "canonical private path"):
                lane.private_role_aliases({str(link): "<private-role>"}, state)

    def test_private_file_rejects_symlinks_and_hardlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "projects.json"
            path.write_text("{}")
            lane.regular_private_file(path)
            link = root / "link.json"
            link.symlink_to(path)
            with self.assertRaises(ContractFailure):
                lane.regular_private_file(link)
            link.unlink()
            os.link(path, link)
            with self.assertRaises(ContractFailure):
                lane.regular_private_file(path)

    def test_catalog_requires_absolute_unique_roots(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "projects.json"
            value = {"version": 1, "defaultProject": "x", "creationRoots": [],
                     "projects": [{"id": "x", "root": directory}]}
            path.write_text(json.dumps(value))
            parsed, checksum = lane.catalog_object(path)
            self.assertEqual(parsed, value)
            self.assertEqual(checksum, hashlib.sha256(path.read_bytes()).hexdigest())
            value["projects"][0]["root"] = "relative"
            path.write_text(json.dumps(value))
            with self.assertRaises(ContractFailure):
                lane.catalog_object(path)
            value["projects"] = [{"id": "x", "root": directory}] * 2
            path.write_text(json.dumps(value))
            with self.assertRaises(ContractFailure):
                lane.catalog_object(path)

    def test_timestamp_rejects_precision_and_zone_changes(self):
        self.assertGreater(lane.timestamp("2026-10-31T18:00:00.001Z"), 0)
        for invalid in (None, "2026-10-31T18:00:00Z", "2026-10-31T18:00:00.001+00:00"):
            with self.assertRaises(ContractFailure):
                lane.timestamp(invalid)

    def test_last_change_keeps_failed_actions_and_exact_eligibility(self):
        rows = [
            receipt(1),
            receipt(2, changed_paths=[], changed_path_count=0,
                    git_before={}, git_after={"head": "b"}),
            receipt(3, status="failed", changed_paths=["failed.txt"], changed_path_count=1),
        ]
        self.assertEqual(lane.last_change(rows)["action_id"], rows[2]["action_id"])
        rows.append(receipt(4, changed_paths=[], changed_path_count=0,
                            git_before={"head": "a"}, git_after={"head": "b"}))
        self.assertEqual(lane.last_change(rows)["action_id"], rows[3]["action_id"])
        self.assertIsNone(lane.last_change([rows[1]]))

    def test_recent_projection_preserves_omitted_and_explicit_null(self):
        missing = receipt()
        missing.pop("workspace_id")
        explicit_null = receipt(2, workspace_id=None, work=None)
        values = lane.recent_actions([missing, explicit_null], "other")
        self.assertNotIn("workspace_id", values[0])
        self.assertNotIn("work", values[0])
        self.assertIsNone(values[1]["workspace_id"])
        self.assertIsNone(values[1]["work"])
        self.assertEqual(values[0]["attribution"], "other or unattributed project work")
        self.assertNotIn("workspace_id", lane.last_change([missing]))

    def test_recent_projection_is_tail_ten_and_paths_ten(self):
        rows = [receipt(number, changed_paths=[str(n) for n in range(15)], changed_path_count=15)
                for number in range(1, 13)]
        projection = lane.recent_actions(rows, rows[0]["workspace_id"])
        self.assertEqual([item["sequence"] for item in projection], list(range(3, 13)))
        self.assertEqual(projection[0]["changed_paths"], [str(n) for n in range(10)])
        self.assertEqual(projection[0]["changed_path_count"], 15)
        self.assertEqual(projection[0]["attribution"], "this workspace; see actor/operation evidence")

    def test_retirement_normalization_is_narrow_and_explicit(self):
        side = self.minimal_side()
        original = {"port": 7211, "registeredTools": side.tools,
                    "registeredToolCount": 3,
                    "work": {"enabled": True, "management": True, "packet_bytes": 16384},
                    "other": None}
        normalized = side.config_value(original)
        self.assertEqual(normalized["registeredTools"], ["read", "work_status"])
        self.assertEqual(normalized["registeredToolCount"], 2)
        self.assertEqual(normalized["work"], {"enabled": False, "management": False, "packet_bytes": 16384})
        self.assertIsNone(normalized["other"])
        self.assertEqual(len(side.approved), 3)
        self.assertTrue(original["work"]["enabled"])
        side.label = "candidate"
        with self.assertRaises(ContractFailure):
            side.config_value(original)

    def test_config_port_and_registry_are_independently_validated(self):
        side = self.minimal_side()
        value = {"port": 7211.0, "registeredTools": side.tools, "registeredToolCount": 3}
        with self.assertRaises(ContractFailure):
            side.config_value(value)
        value["port"] = 7211
        value["registeredToolCount"] = 2
        with self.assertRaises(ContractFailure):
            side.config_value(value)
        value["registeredToolCount"] = 3
        value["registeredTools"] = ["read", "read", "work_status"]
        with self.assertRaises(ContractFailure):
            side.config_value(value)

    def test_server_config_text_order_and_format_are_preserved(self):
        side = self.minimal_side()
        data = {"port": 7211, "registeredTools": side.tools, "registeredToolCount": 3}
        text = "# CodexPro Server Config\n\n" + json.dumps(data, indent=2)
        normalized = side.normalize_config(response(data, text))
        reversed_data = dict(reversed(list(data.items())))
        other = side.normalize_config(response(reversed_data,
            "# CodexPro Server Config\n\n" + json.dumps(reversed_data, indent=2)))
        self.assertIsNotNone(difference(normalized, other))
        with self.assertRaises(ContractFailure):
            side.normalize_config(response(data, text + "\n"))
        with self.assertRaises(ContractFailure):
            side.normalize_config(response({**data, "registeredToolCount": 4}, text))

    def test_no_blanket_null_empty_or_unknown_field_masking(self):
        side = self.minimal_side()
        left = response({"optional": None, "unknown": {"x": ""}})
        right = response({"unknown": {"x": ""}})
        self.assertIsNotNone(difference(side.normalize_mcp(left), side.normalize_mcp(right)))
        right = response({"optional": None, "unknown": {"x": []}})
        self.assertIsNotNone(difference(side.normalize_mcp(left), side.normalize_mcp(right)))

    def profile_side(self, home):
        side = self.minimal_side()
        side.home = home
        side.profile_path = home / "profiles" / "fixture.json"
        side.run_id = "12345678"
        side.profile_armed = False
        side.profile_backup = None
        side.profile_last_hash = None
        side.profile_saved_clock = None
        side.profile_saved_label = None
        side.profile_original_mode = None
        side.unsettled = False
        side.created = {}
        side.alive = lambda: None
        side.clients = {"bootstrap": DummyClient()}
        side.lock = io.BytesIO()
        return side

    def test_profile_restores_original_bytes_mode_and_missing_state(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.profile_side(Path(directory))
            side.profile_path.parent.mkdir()
            original = b'{"token":"private retained bytes","other":null}\n'
            side.profile_path.write_bytes(original)
            side.profile_path.chmod(0o640)
            side.arm_profile()
            self.assertFalse(side.profile_path.exists())
            side.seed_profile({})
            self.assertEqual(side.profile_path.read_bytes(), b"{}\n")
            self.assertTrue(side.restore()["profile_restored"])
            self.assertEqual(side.profile_path.read_bytes(), original)
            self.assertEqual(side.profile_path.stat().st_mode & 0o777, 0o640)
            side = self.profile_side(Path(directory) / "new")
            side.home.mkdir()
            side.arm_profile()
            side.seed_profile({})
            self.assertTrue(side.restore()["profile_restored"])
            self.assertFalse(side.profile_path.exists())

    def test_profile_restore_refuses_unsettled_and_external_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.profile_side(Path(directory))
            side.arm_profile()
            side.seed_profile({})
            side.unsettled = True
            self.assertIn("blocked", side.restore())
            self.assertTrue(side.profile_path.exists())
            side.unsettled = False
            side.profile_path.write_text('{"external":true}')
            with self.assertRaises(ContractFailure):
                side.restore()
            self.assertEqual(side.profile_path.read_text(), '{"external":true}')

    def profile_response(self, side, exists):
        runtime = {
            "port": side.port, "bashMode": "full", "bashTranscript": "compact",
            "codexSessions": "off", "writeMode": "workspace", "handoffMode": "off",
            "toolMode": "standard", "worktreeMode": "off", "worktreeBaseRef": "HEAD",
            "maxWorktrees": 64, "toolCards": False,
        }
        side.initial_config = copy.deepcopy(runtime)
        return {"http_status": 200, "headers": {}, "body": {
            "ok": True, "exists": exists, "profile": {}, "runtime": runtime,
            "runtime_connection": {}, "effective": {"port": str(side.port), "work": "off"}}}

    def test_profile_missing_empty_and_port_types_are_not_conflated(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.profile_side(Path(directory))
            missing = self.profile_response(side, False)
            side.normalize_profile(missing)
            side.profile_path.parent.mkdir()
            side.profile_path.write_text("{}")
            with self.assertRaises(ContractFailure):
                side.normalize_profile(missing)
            empty = self.profile_response(side, True)
            side.normalize_profile(empty)
            empty["body"]["effective"]["port"] = 7211
            with self.assertRaises(ContractFailure):
                side.normalize_profile(empty)
            empty = self.profile_response(side, True)
            empty["body"]["runtime"]["port"] = 7211.0
            with self.assertRaises(ContractFailure):
                side.normalize_profile(empty)

    def test_profile_runtime_cannot_silently_follow_saved_tier(self):
        with tempfile.TemporaryDirectory() as directory:
            side = self.profile_side(Path(directory))
            value = self.profile_response(side, False)
            value["body"]["runtime"]["toolMode"] = "minimal"
            with self.assertRaises(ContractFailure):
                side.normalize_profile(value)

    def http_side(self):
        side = self.minimal_side()
        side.alive = lambda: None
        side.origin = "http://127.0.0.1:7211"
        side.client = DummyClient()
        side.unsettled = False
        side.records = []
        side.record = side.records.append
        return side

    def test_query_auth_is_sent_but_never_recorded(self):
        side = self.http_side()
        requests = []

        class Opener:
            def open(self, request, timeout):
                requests.append(request)
                return Reply(b'{"ok":true}')

        with patch.object(lane.urllib.request, "build_opener", return_value=Opener()):
            result, _ = side.http("GET", "/healthz", auth="query")
        self.assertEqual(result["body"], {"ok": True})
        self.assertIn("codexpro_token=", requests[0].full_url)
        self.assertNotIn(side.client.credential, json.dumps(side.records))
        self.assertNotIn("Authorization", side.records[0]["request"])
        self.assertEqual(side.records[0]["request"]["auth"], "query")
        self.assertFalse(side.unsettled)

    def test_http_uncertain_outcome_blocks_cleanup(self):
        side = self.http_side()

        class Opener:
            def open(self, request, timeout):
                raise TimeoutError("bounded test timeout")

        with patch.object(lane.urllib.request, "build_opener", return_value=Opener()):
            with self.assertRaises(TimeoutError):
                side.http("POST", "/admin/profile", {"toolMode": "full"})
        self.assertTrue(side.unsettled)
        self.assertEqual(side.records, [])

    def test_http_rejects_response_credentials_and_bad_lengths(self):
        side = self.http_side()

        class Opener:
            def open(self, request, timeout):
                return Reply(json.dumps({"leak": side.client.credential}).encode())

        with patch.object(lane.urllib.request, "build_opener", return_value=Opener()):
            with self.assertRaises(ContractFailure):
                side.http("GET", "/admin/profile")
        self.assertEqual(side.records, [])
        reply = Reply(b"{}")
        reply.headers.replace_header("Content-Length", "3")
        with patch.object(lane.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value = reply
            with self.assertRaises(ContractFailure):
                side.http("GET", "/healthz")

    def test_http_redirects_are_not_followed(self):
        self.assertIsNone(lane.NoRedirect().redirect_request(
            None, None, 302, "redirect", {}, "https://outside.example.invalid"))

    def recent_side(self, after_sequence=11):
        side = self.minimal_side()
        action = receipt(7)
        side.actions = {action["action_id"]: (action, "WRITE")}
        side.aliases.update({action["action_id"]: "<action:WRITE>",
                             action["actor_ref"]: "<validated-actor>"})
        brief = {
            "enabled": True, "last_recorded_project_change": lane.last_change([action]),
            "latest_recorded_activity": action["finished_at"],
            "actions": lane.recent_actions([action], action["workspace_id"]),
            "latest_sequence": 10, "next_sequence": 10, "earliest_sequence": 1,
            "has_more": False, "gap_detected": False, "coverage": lane.COVERAGE,
        }
        data = {"project_id": "test", "workspace_id": action["workspace_id"],
                "recent_activity": brief}
        text = "# Workspace\n\nLast recorded project change: " + action["finished_at"] + ". " + lane.COVERAGE
        values = iter([
            response({"latest_sequence": 10, "retained_from_sequence": 1}),
            response(data, text),
            response({"latest_sequence": after_sequence, "retained_from_sequence": 1}),
        ])
        side.mcp = lambda *args: next(values)
        side.audit_rows = lambda project: ([action], {})
        return side

    def test_recent_watermarks_respect_audit_exempt_activity_reads(self):
        side = self.recent_side()
        result = side.open_and_normalize({"project_id": "test"})
        data = result["body"]["result"]["structuredContent"]["recent_activity"]
        self.assertEqual(data["latest_sequence"], "<validated-call-watermark>")
        self.assertEqual(data["last_recorded_project_change"]["at"], "<finished:WRITE>")
        self.assertEqual(data["actions"][0]["sequence"], "<sequence:WRITE>")
        self.assertIn("Last recorded project change: <finished:WRITE>",
                      result["body"]["result"]["content"][0]["text"])

    def test_recent_watermarks_detect_unowned_concurrent_call(self):
        with self.assertRaises(ContractFailure):
            self.recent_side(after_sequence=12).open_and_normalize({"project_id": "test"})

    def test_same_clock_different_receipts_keep_distinct_time_bindings(self):
        side = self.minimal_side()
        first, second = receipt(1), receipt(2)
        side.actions = {first["action_id"]: (first, "FIRST"),
                        second["action_id"]: (second, "SECOND")}
        a = side.normalize_change(lane.last_change([first]))
        b = side.normalize_change(lane.last_change([second]))
        self.assertEqual(a["at"], "<finished:FIRST>")
        self.assertEqual(b["at"], "<finished:SECOND>")
        self.assertNotEqual(a["at"], b["at"])


if __name__ == "__main__":
    unittest.main()
