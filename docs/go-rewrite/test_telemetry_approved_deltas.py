#!/usr/bin/env python3
"""Guard the approved 413 exception against broader or implicit normalization."""
import copy
import hashlib
from pathlib import Path
import tempfile
import types
import unittest

from telemetry_approved_deltas import (
    APPROVAL_FILE_SHA256, DELTA_ID, ERROR_LINE, HTML_PREFIX, HTML_SUFFIX, RULING_SHA256, SOURCE_COMMIT,
    STACK_FRAMES, STABLE_BODY, load_approval, payload_too_large_projection,
)


def fixture_approval():
    # In-memory unit-test stand-in. CLI activation still requires the exact
    # observed approval artifact, whose bytes and ruling text are checked.
    return {
        "delta_id": DELTA_ID, "source_commit": SOURCE_COMMIT, "approved": True,
        "authority": "plans/coordinator",
        "registry_path": "/home/spite/Projects/workpackets/REGISTRY.md",
        "registry_row": 31, "ruling_at": "2026-10-31T19:25:00Z",
        "ruling_sha256": RULING_SHA256, "approval_file_sha256": APPROVAL_FILE_SHA256,
    }


def reference_body():
    frames = []
    for name, module, line, column, file_url in STACK_FRAMES:
        prefix = "file://" if file_url else ""
        frames.append(f"<br> &nbsp; &nbsp;at {name} ({prefix}/tmp/private-reference-release/{module}:{line}:{column})")
    return HTML_PREFIX + ERROR_LINE + "".join(frames) + HTML_SUFFIX


def fixture(label="reference"):
    side = types.SimpleNamespace(
        label=label, current_case="usage/usage-body-budget", normalizations=[],
        normalize=lambda value, domain: copy.deepcopy(value),
    )
    body = reference_body() if label == "reference" else STABLE_BODY
    response = {
        "http_status": 413,
        "headers": {"content-type": "text/html; charset=utf-8",
                    "content-security-policy": "default-src 'none'",
                    "x-content-type-options": "nosniff", "cache-control": "no-store"},
        "body": body, "raw_body": body,
    }
    return side, response


class ApprovedDeltaTests(unittest.TestCase):
    def test_explicit_approval_artifact_is_hash_bound(self):
        artifact = Path(__file__).with_name("http413-approval.json")
        if not artifact.exists():
            artifact = Path(__file__).with_name("evidence") / "http413-approval.json"
        if not artifact.exists():
            self.skipTest("recorded approval fixture is stored separately by the parent")
        original = artifact.read_bytes()
        self.assertEqual(hashlib.sha256(original).hexdigest(), APPROVAL_FILE_SHA256)
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "approval.json"
            path.write_bytes(original)
            approval, evidence = load_approval(path)
            self.assertEqual(approval["ruling_sha256"], RULING_SHA256)
            self.assertEqual(evidence["approval_file_sha256"], APPROVAL_FILE_SHA256)
            path.write_bytes(original.replace(b'"approved": true', b'"approved": false'))
            with self.assertRaises(ValueError):
                load_approval(path)
        with self.assertRaises(ValueError):
            load_approval(None)

    def test_exact_pair_and_immutable_evidence(self):
        reference, raw_reference = fixture()
        candidate, raw_candidate = fixture("candidate")
        saved_reference, saved_candidate = copy.deepcopy(raw_reference), copy.deepcopy(raw_candidate)
        left = payload_too_large_projection(reference, raw_reference, approval=fixture_approval())
        right = payload_too_large_projection(candidate, raw_candidate, approval=fixture_approval())
        self.assertEqual(left, right)
        self.assertEqual(raw_reference, saved_reference)
        self.assertEqual(raw_candidate, saved_candidate)
        self.assertEqual(reference.normalizations[0]["validated_stack_frames"], 10)
        self.assertEqual(candidate.normalizations[0]["validated_stack_frames"], 0)
        self.assertNotEqual(reference.normalizations[0]["raw_body_sha256"],
                            candidate.normalizations[0]["raw_body_sha256"])

    def test_missing_or_wrong_approval_fails_closed(self):
        side, response = fixture()
        variants = [None, {}, {**fixture_approval(), "approved": False},
                    {**fixture_approval(), "delta_id": "other"},
                    {**fixture_approval(), "registry_row": 30},
                    {**fixture_approval(), "source_commit": "other"},
                    {**fixture_approval(), "ruling_sha256": ""},
                    {**fixture_approval(), "approval_file_sha256": ""}]
        for approval in variants:
            with self.subTest(approval=approval), self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=approval)
        self.assertEqual(side.normalizations, [])

    def test_scope_cannot_expand(self):
        for case in ("usage/other", "inbox/inbox-missing", "mcp/usage-body-budget"):
            side, response = fixture()
            side.current_case = case
            with self.subTest(case=case), self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=fixture_approval())
        side, response = fixture()
        side.current_case = "inbox/inbox-body-budget"
        self.assertEqual(payload_too_large_projection(side, response, approval=fixture_approval())["body"], STABLE_BODY)

    def test_all_reference_frame_details_are_validated(self):
        original = reference_body()
        variants = (
            original.replace("PayloadTooLargeError", "OtherError"),
            original.replace("request entity too large", "other message"),
            original.replace("raw-body/index.js:163:17", "raw-body/index.js:164:17"),
            original.replace("at getRawBody", "at anotherFunction"),
            original.replace("file:///tmp/private-reference-release/dist/http.js", "file:///tmp/other/dist/http.js"),
            original.replace("</pre>", "<br> unexpected extra text</pre>"),
            original.replace("<!DOCTYPE html>", "<!doctype html>"),
            original[:-1], STABLE_BODY,
        )
        for body in variants:
            side, response = fixture()
            response["body"] = response["raw_body"] = body
            with self.subTest(body=body[:80]), self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=fixture_approval())

    def test_candidate_must_match_exact_stack_free_shell(self):
        for body in (reference_body(), STABLE_BODY.replace(ERROR_LINE, "request entity too large"),
                     STABLE_BODY + " ", {"error": "request entity too large"}):
            side, response = fixture("candidate")
            response["body"] = response["raw_body"] = body
            with self.subTest(body=str(body)[:80]), self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=fixture_approval())

    def test_headers_status_and_raw_body_are_preserved(self):
        for field, value in (("content-type", "application/json"),
                             ("content-security-policy", "default-src *"),
                             ("x-content-type-options", None)):
            side, response = fixture()
            response["headers"][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=fixture_approval())
        for mutation in (lambda r: r.update(http_status=400),
                         lambda r: r.update(raw_body="altered")):
            side, response = fixture()
            mutation(response)
            with self.assertRaises(ValueError):
                payload_too_large_projection(side, response, approval=fixture_approval())
        left, left_response = fixture()
        right, right_response = fixture("candidate")
        right_response["headers"]["cache-control"] = "private"
        self.assertNotEqual(payload_too_large_projection(left, left_response, approval=fixture_approval()),
                            payload_too_large_projection(right, right_response, approval=fixture_approval()))

    def test_lane_reports_delta_separately_and_missing_approval_stays_strict(self):
        from telemetry_contract_suite import Lane
        sides, responses = [], {}
        for label in ("reference", "candidate"):
            side, response = fixture(label)
            side.client = types.SimpleNamespace(credential="unit-test-credential", session=None)
            side.canonical_http = lambda response, domain: {
                key: copy.deepcopy(response[key]) for key in ("http_status", "headers", "body")
            }
            sides.append(side)
            responses[label] = response
        approved = Lane(sides, approval=fixture_approval())
        approved.phase = "usage"
        approved.check("usage-body-budget", lambda side: responses[side.label], expected_status=413)
        self.assertTrue(approved.cases[0]["passed"])
        self.assertEqual(approved.cases[0]["comparison_status"], "passed_with_approved_delta")
        self.assertEqual(approved.cases[0]["approved_deltas"], [DELTA_ID])

        strict = Lane(sides)
        strict.phase = "usage"
        strict.check("usage-body-budget", lambda side: responses[side.label], expected_status=413)
        self.assertFalse(strict.cases[0]["passed"])
        self.assertEqual(strict.cases[0]["comparison_status"], "failed")
        self.assertEqual(strict.cases[0]["approved_deltas"], [])

        approved.check("ordinary-unchanged", lambda side: {"http_status": 200, "headers": {}, "body": {"ok": True}})
        self.assertEqual(approved.cases[1]["comparison_status"], "passed_unchanged")
        self.assertEqual(approved.cases[1]["approved_deltas"], [])
        responses["candidate"]["body"] = responses["candidate"]["raw_body"] = STABLE_BODY + "changed"
        approved.check("usage-body-budget", lambda side: responses[side.label], expected_status=413)
        self.assertFalse(approved.cases[2]["passed"])
        self.assertEqual(approved.cases[2]["comparison_status"], "failed")
        self.assertEqual(approved.cases[2]["approved_deltas"], [])


if __name__ == "__main__":
    unittest.main()
