#!/usr/bin/env python3
"""Guard tests: prove the differential lane cannot normalize away wrong evidence."""
from __future__ import annotations

import copy
import datetime as dt
import gzip
import hashlib
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

from contract_suite import Client
from telemetry_contract_suite import (
    ContractFailure, RawEvidence, Side, canonical_json, digest, iso,
    path_allowed, timestamp_ms, validate_launch,
)


def side_fixture():
    side = Side.__new__(Side)
    side.label = "candidate"
    side.root = Path("/tmp/sp6-telemetry-test-fixture")
    side.state = Path("/tmp/sp6-telemetry-test-state")
    side.workspace = "ws_test_telemetry"
    side.project = "contract"
    side.project_label = "Private contract fixture"
    side.origin = "http://127.0.0.1:7210"
    side.manifest = {}
    side.clock_start_ms = round(time.time() * 1000) - 1000
    side.principal = "connector_" + "a" * 32
    side.reset_at = None
    side.jobs = {}
    side.actions = {}
    side.action_labels = {}
    side.normalizations = []
    side.evidence = None
    side.client = Client.__new__(Client)
    side.client.credential = "private-test-credential-8d9625ab7e8a447088d6"
    side.client.session = "private-test-session-476e4973408940479205"
    return side


def action_fixture(side, number=1):
    ended = dt.datetime.now(dt.timezone.utc) - dt.timedelta(milliseconds=100 * (4 - number))
    started = ended - dt.timedelta(milliseconds=3)
    actor = "actor_" + digest(side.principal)[:32]
    request = "request_" + digest(str(number + 10))[:32]
    transport = "transport_" + digest(side.client.session)[:32]
    action = {
        "schema_version": "1.0", "namespace": "debug", "sequence": number,
        "action_id": "cpa_" + ("%032x" % number), "tool_name": "read", "status": "succeeded",
        "mutating": False, "project_id": side.project, "workspace_id": side.workspace,
        "result_ref": "codexpro://actions/cpa_" + ("%032x" % number),
        "occurred_at": iso(started), "finished_at": iso(ended),
        "duration_ms": timestamp_ms(iso(ended)) - timestamp_ms(iso(started)),
        "server_session_ref": "srv_" + "b" * 32,
        "actor_ref": actor, "request_ref": request, "transport_session_ref": transport,
        "request_fingerprint": digest("\0".join((actor, transport, request, "read"))),
        "request_metadata": {"path": "owned/read-" + str(number) + ".txt", "extra": False},
        "result_metadata": {"bytes": 21}, "operation": "read", "operation_class": "read",
    }
    return action, number + 10, (timestamp_ms(iso(started)) - 2, timestamp_ms(iso(ended)) + 2)


def activity_fixture(side):
    generated = iso()
    generated_ms = timestamp_ms(generated)
    recent = []
    for action in reversed(list(side.actions.values())):
        recent.append({
            "action_id": action["action_id"], "tool": "read", "status": "succeeded",
            "workspace_id": side.workspace, "started_at": action["occurred_at"],
            "finished_at": action["finished_at"], "duration_ms": action["duration_ms"],
            "started_age_ms": max(0, generated_ms - timestamp_ms(action["occurred_at"])),
            "finished_age_ms": max(0, generated_ms - timestamp_ms(action["finished_at"])),
            "command": "read", "command_truncated": False, "result": {"bytes": 21},
            "error": None, "job_ids": [], "job_ids_total": 0,
            "output": {"available": False, "reason": "not_retained_or_not_a_shell_command"},
        })
    last = recent[0]
    project = {
        "project_id": side.project, "label": side.project_label,
        "url": "/activity/projects/contract.json", "has_inflight_work": False,
        "inflight_counts": {"tool_calls": 0, "jobs": 0, "claims": 0},
        "inflight": {"calls": [], "jobs": [], "truncated": False},
        "work_runs": [], "work_runs_total": 0, "recent_commands": recent,
        "last_command_started_at": last["started_at"],
        "last_command_started_age_ms": last["started_age_ms"],
        "last_command_finished_at": last["finished_at"],
        "last_command_finished_age_ms": last["finished_age_ms"],
        "last_activity_at": last["finished_at"], "last_activity_age_ms": last["finished_age_ms"],
        "review_recommended": False, "signals": [], "signals_total": 0,
    }
    return {
        "http_status": 200, "headers": {"content-type": "application/json; charset=utf-8"},
        "started_ms": generated_ms - 2, "finished_ms": generated_ms + 2,
        "body": {
            "schema_version": 1, "generated_at": generated,
            "limits": {"recent_commands": 3, "output_bytes_per_command": 0,
                       "inflight_per_kind": 10, "quiet_after_ms": 300000},
            "coverage": {"tool_calls": "current_server_process", "jobs": "persistent_job_store",
                         "audit_enabled": True, "recent_receipts_scanned_per_project": 9,
                         "history_incomplete": False,
                         "note": "Recorded CodexPro activity only. Quiet signals invite review, not automatic cancellation. Output is a bounded retained tail; requests do not acknowledge jobs or heartbeat claims."},
            "project": project,
        },
    }


class TelemetryGuardTests(unittest.TestCase):
    def test_bootstrap_nonce_binds_each_side_and_the_full_newline_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            roots = {"reference": root / "reference", "candidate": root / "candidate"}
            for path in roots.values():
                path.mkdir()
            manifests = {}
            for label, pid in (("reference", 101), ("candidate", 102)):
                manifest = root / (label + ".json")
                manifest.write_text(json.dumps({"project_id": "contract", "pid": pid}))
                manifests[label] = manifest
            mapped_roots = dict(roots)
            wrong_hash = set()
            calls = []
            class FakeClient:
                def __init__(self, endpoint, token_file, timeout):
                    self.label = "reference" if ":7211/" in endpoint else "candidate"
                def call(self, name, args):
                    calls.append((self.label, name, args))
                    if name == "list_projects":
                        data = {"projects": [{"id": "contract", "label": "Private fixture", "workspace_id": "ws_" + self.label}]}
                    else:
                        self_check = mapped_roots[self.label] / args["path"]
                        body = self_check.read_bytes()
                        lines = body.decode().split("\n")  # original fsOps retains the final empty line
                        end = args.get("end_line", len(lines))
                        text = "\n".join(f"{number + 1} | {line}" for number, line in enumerate(lines[:end]))
                        digest_value = hashlib.sha256(body.rstrip(b"\n") if self.label in wrong_hash else body).hexdigest()
                        data = {"text": text, "sha256": digest_value}
                    return {"body": {"result": {"structuredContent": data}}}
            def launch(manifest, endpoint, token, port):
                label = "reference" if port == 7211 else "candidate"
                return roots[label], root / (label + "-state"), root / (label + "-token")
            with mock.patch("telemetry_contract_suite.Client", FakeClient), mock.patch("telemetry_contract_suite.validate_launch", side_effect=launch):
                Side("reference", "http://127.0.0.1:7211/mcp", "unused", manifests["reference"], "shared-fixture")
                mapped_roots["candidate"] = roots["reference"]
                with self.assertRaisesRegex(ContractFailure, "explicit private root"):
                    Side("candidate", "http://127.0.0.1:7210/mcp", "unused", manifests["candidate"], "shared-fixture")
                mapped_roots["candidate"] = roots["candidate"]
                wrong_hash.add("candidate")
                with self.assertRaisesRegex(ContractFailure, "complete private fixture file"):
                    Side("candidate", "http://127.0.0.1:7210/mcp", "unused", manifests["candidate"], "hash-fixture")
                wrong_hash.clear()
                Side("candidate", "http://127.0.0.1:7210/mcp", "unused", manifests["candidate"], "correct-fixture")
            read_calls = [args for _, name, args in calls if name == "read"]
            self.assertTrue(read_calls)
            self.assertTrue(all(value["start_line"] == 1 and value["end_line"] == 1 for value in read_calls))

    def test_rejects_live_and_unbound_targets_before_process_access(self):
        urls = (
            "http://127.0.0.1:7200/mcp", "http://example.invalid:7210/mcp",
            "ftp://127.0.0.1:7210/mcp", "http://127.0.0.1:7210/mcp?token=secret",
            "http://:password@127.0.0.1:7210/mcp", "http://127.0.0.1:7210/other",
        )
        with mock.patch("telemetry_contract_suite.process_start_ticks") as process:
            for url in urls:
                with self.subTest(url=url), self.assertRaises(ContractFailure):
                    validate_launch({}, url, "/tmp/unused-token", 7210)
            process.assert_not_called()

    def test_protected_state_and_symlink_escape_are_refused(self):
        for relative in (".codexpro/state.sqlite", ".config/codexpro/token",
                         "Projects/codexpro/src/index.ts", "apps/codexpro/current"):
            with self.subTest(relative=relative), self.assertRaises(ContractFailure):
                path_allowed(Path.home() / relative, allow_missing=True)
        with tempfile.TemporaryDirectory() as directory:
            alias = Path(directory) / "private-looking"
            alias.symlink_to(Path.home() / ".codexpro", target_is_directory=True)
            with self.assertRaises(ContractFailure):
                path_allowed(alias / "audit.jsonl", allow_missing=True)

    def test_http_mutation_needs_verified_database_ownership(self):
        side = side_fixture()
        with mock.patch("telemetry_contract_suite.urllib.request.urlopen") as network:
            with self.assertRaises(ContractFailure):
                side.http("POST", "/usage/v1/pro/reset", {})
            network.assert_not_called()

    def test_json_equality_keeps_boolean_number_and_missing_null_distinct(self):
        self.assertNotEqual(canonical_json({"count": True}), canonical_json({"count": 1}))
        self.assertNotEqual(canonical_json({}), canonical_json({"value": None}))
        self.assertNotEqual(canonical_json({"count": 1.0}), canonical_json({"count": 1}))

    def test_utc_timestamp_requires_milliseconds_and_valid_calendar(self):
        for value in ("2026-10-31T12:00:00Z", "2026-02-30T12:00:00.000Z",
                      "2026-10-31T12:00:00.000+00:00", None):
            with self.subTest(value=value), self.assertRaises(ContractFailure):
                timestamp_ms(value)
        self.assertEqual(timestamp_ms("2026-10-31T12:00:00.001Z") -
                         timestamp_ms("2026-10-31T12:00:00.000Z"), 1)

    def test_usage_normalization_preserves_caller_times_counts_and_unknown_fields(self):
        side = side_fixture()
        now = iso()
        side.reset_at = now
        value = {"generated_at": now, "count": 2, "requested_mode_count": 1,
                 "settings": {"manual_reset_at": now, "limits_checked_at": now,
                              "limits_verified_at": now, "weekly_anchor_at": now,
                              "pending_reset": {"requested_at": now, "by": side.principal}},
                 "first_submission_at": now, "period_started_at": now, "future_field": {"enabled": False}}
        got = side.normalize(value, "usage")
        self.assertEqual(got["count"], 2)
        self.assertEqual(got["settings"]["limits_checked_at"], now)
        self.assertEqual(got["settings"]["limits_verified_at"], now)
        self.assertEqual(got["settings"]["weekly_anchor_at"], now)
        self.assertEqual(got["first_submission_at"], now)
        self.assertEqual(got["future_field"], {"enabled": False})
        self.assertEqual(got["settings"]["manual_reset_at"], "<server-clock>")
        self.assertEqual(set(got), set(value))
        altered = copy.deepcopy(value)
        altered["settings"]["manual_reset_at"] = iso(dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=1))
        with self.assertRaises(ContractFailure):
            side.normalize(altered, "usage")

    def test_inbox_normalization_refuses_changed_principal_and_out_of_window_clock(self):
        side = side_fixture()
        valid = {"created_at": iso(), "updated_at": iso(), "revision": 2,
                 "answer": {"by": side.principal, "text": "same"}, "deliveries": []}
        self.assertEqual(side.normalize(valid, "inbox")["answer"]["text"], "same")
        altered = copy.deepcopy(valid)
        altered["answer"]["by"] = "connector_" + "c" * 32
        with self.assertRaises(ContractFailure):
            side.normalize(altered, "inbox")
        altered = copy.deepcopy(valid)
        altered["created_at"] = "2000-01-01T00:00:00.000Z"
        with self.assertRaises(ContractFailure):
            side.normalize(altered, "inbox")

    def test_audit_hashes_and_time_evidence_are_bound_before_normalization(self):
        side = side_fixture()
        action, request_id, window = action_fixture(side)
        canonical = side.validate_action(action, "read-1", request_id, window)
        self.assertEqual(canonical["request_metadata"], action["request_metadata"])
        self.assertEqual(canonical["result_metadata"], action["result_metadata"])
        self.assertEqual(set(canonical), set(action))
        for field, value in (("actor_ref", "actor_" + "d" * 32),
                             ("request_ref", "request_" + "d" * 32),
                             ("transport_session_ref", "transport_" + "d" * 32),
                             ("request_fingerprint", "0" * 64),
                             ("duration_ms", action["duration_ms"] + 1),
                             ("project_id", "other"), ("sequence", True)):
            altered = {**action, field: value}
            with self.subTest(field=field), self.assertRaises(ContractFailure):
                side.validate_action(altered, "read-1", request_id, window)

    def test_activity_preserves_fields_and_checks_age_arithmetic_and_order(self):
        side = side_fixture()
        for number in range(1, 4):
            action, request_id, window = action_fixture(side, number)
            side.validate_action(action, "read-" + str(number), request_id, window)
        response = activity_fixture(side)
        projected = side.validate_activity(response)
        self.assertEqual(set(projected["body"]), set(response["body"]))
        self.assertEqual(projected["body"]["coverage"], response["body"]["coverage"])
        self.assertEqual(projected["body"]["project"]["recent_commands"][0]["result"], {"bytes": 21})
        bad_age = copy.deepcopy(response)
        bad_age["body"]["project"]["recent_commands"][0]["started_age_ms"] += 1
        with self.assertRaises(ContractFailure):
            side.validate_activity(bad_age)
        bad_order = copy.deepcopy(response)
        bad_order["body"]["project"]["recent_commands"].reverse()
        with self.assertRaises(ContractFailure):
            side.validate_activity(bad_order)
        duplicate = copy.deepcopy(response)
        duplicate["body"]["project"]["recent_commands"][1] = copy.deepcopy(
            duplicate["body"]["project"]["recent_commands"][0])
        with self.assertRaises(ContractFailure):
            side.validate_activity(duplicate)
        fabricated_job = copy.deepcopy(response)
        fabricated_job["body"]["project"]["recent_commands"][0]["job_ids"] = ["job_12345678"]
        with self.assertRaises(ContractFailure):
            side.validate_activity(fabricated_job)

    def test_raw_evidence_keeps_original_unicode_and_rejects_credentials(self):
        side = side_fixture()
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "raw.jsonl.gz"
            evidence = RawEvidence(path, [side.client])
            receipt = evidence.append({"body": {"text": "original 🙂", "count": 2, "none": None}})
            self.assertEqual(receipt["record"], 1)
            for secret in (side.client.credential, side.client.session):
                with self.assertRaises(RuntimeError):
                    evidence.append({"body": secret})
            self.assertEqual(evidence.records, 1)
            evidence.close()
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)
            with gzip.open(path, "rt") as stream:
                rows = [json.loads(line) for line in stream]
            self.assertEqual(rows, [{"body": {"text": "original 🙂", "count": 2, "none": None}}])


if __name__ == "__main__":
    unittest.main()

