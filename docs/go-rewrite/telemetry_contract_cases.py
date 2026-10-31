"""Cases for the explicitly private telemetry lane; never import a launcher."""
from __future__ import annotations

import copy
import datetime as dt
import hashlib
import json
from pathlib import Path
import re
import time
import urllib.parse

from contract_suite import raw_result
from telemetry_contract_suite import (
    ContractFailure, canonical_json, iso, require, timestamp_ms,
)


def q(value):
    return urllib.parse.quote(value, safe="")


def all_success(values):
    require(all(value is not None for value in values), "earlier case failed its prerequisite")


def body_is(response, predicate, message):
    require(predicate(response["body"]), message)
    return response


def exact_receipt(side, response, name):
    old = getattr(side, name)
    require(canonical_json(old["body"]) == canonical_json(response["body"]),
            "idempotent replay did not return its original receipt")
    return response


def settings(response, revision, pending=None, manual=None):
    value = response["body"]["settings"]
    require(value["revision"] == revision, "settings revision did not follow the committed transition")
    if pending is not None:
        require((value["pending_reset"] is not None) is pending, "pending reset transition changed")
    if manual is not None:
        require((value["manual_reset_at"] is not None) is manual, "manual reset transition changed")
    return response


def partial_sections(side, response):
    require(response["http_status"] == 200 and isinstance(response["body"], dict),
            "dashboard partial response is unavailable")
    data = response["body"]
    observed = timestamp_ms(data["generatedAt"])
    require(response["started_ms"] - 2000 <= observed <= response["finished_ms"] + 2000,
            "dashboard partial clock escapes the GET")
    sections = data["sections"]
    require(isinstance(sections, list) and len(sections) == 7, "dashboard must keep all seven sections")
    keys = [section["key"] for section in sections]
    require(len(set(keys)) == len(keys), "dashboard section keys are not unique")
    require(all(isinstance(section.get("html"), str) for section in sections),
            "dashboard section HTML is absent")
    return {section["key"]: section["html"] for section in sections}


def preflight(lane, run_id):
    def fresh_usage(side):
        response = side.http("GET", "/usage/v1/pro")
        data = response["body"]
        require(response["http_status"] == 200 and data["all_time_count"] == 0
                and data["count"] == 0 and data["collectors"] == []
                and data["settings"]["revision"] == 1
                and data["settings"]["pending_reset"] is None
                and data["settings"]["weekly_anchor_at"] is None
                and data["settings"]["manual_reset_at"] is None,
                "usage stores must be fresh; restart only the private instances with fresh state before rerunning")
        side.fresh_usage = response
        return response
    all_success(lane.check("fresh-usage", fresh_usage, expected_status=200, domain="usage"))
    all_success(lane.check("fresh-inbox", lambda side: body_is(
        side.http("GET", "/inbox/v1/items?project_id=" + q(side.project)),
        lambda data: data["items"] == [] and data["total"] == 0 and data["next_offset"] is None,
        "private inbox fixture must be empty"), expected_status=200, domain="inbox"))
    lane.check("fresh-usage-history", lambda side: side.http("GET", "/usage/v1/pro/history"),
               expected_status=200, domain="usage")
    lane.check("fresh-inbox-events", lambda side: side.http(
        "GET", "/inbox/v1/events?project_id=" + q(side.project)), expected_status=200, domain="inbox")
    for mode in ("missing", "wrong"):
        lane.check("auth-" + mode, lambda side, mode=mode: side.http(
            "GET", "/activity.json", auth=mode), expected_status=401)
    def html(side):
        response = side.http("GET", "/activity", auth="query")
        require(response["http_status"] == 200 and isinstance(response["body"], str),
                "query-token dashboard authentication failed")
        page = response["body"]
        for marker in ("CodexPro", "/activity/data", "/activity/live", "/usage/v1/pro", "/activity/inbox"):
            require(marker in page, "dashboard lost its functional link: " + marker)
        require("<script" in page and "<style" in page, "dashboard assets are missing")
        return response
    def html_projection(side, response):
        return {"http_status": response["http_status"], "headers": side.normalize(response["headers"]),
                "comparison_scope": "functional dashboard links/assets; full HTML retained in raw evidence",
                "functional_links_present": True}
    lane.check("query-token-dashboard", html, normalize=html_projection, expected_status=200)
    lane.check("cross-site-reset-rejected", lambda side: side.http("POST", "/usage/v1/pro/reset",
               {"schema_version": 1, "request_id": "cross-site-" + run_id, "expected_revision": 1},
               headers={"Origin": "https://untrusted.example.invalid", "Sec-Fetch-Site": "cross-site"}),
               expected_status=403)
    lane.check("unknown-usage-project", lambda side: side.http(
        "GET", "/usage/v1/pro?project_id=sp6-unknown-project"), expected_status=404)
    lane.check("duplicate-project-query", lambda side: side.http(
        "GET", "/usage/v1/pro?project_id=" + q(side.project) + "&project_id=" + q(side.project)),
        expected_status=404)


def usage(lane, run_id):
    now = dt.datetime.now(dt.timezone.utc)
    submitted = iso(now - dt.timedelta(seconds=10))
    checked_before_click = iso(now - dt.timedelta(seconds=5))
    anchor = iso(now + dt.timedelta(days=6))
    collector = "sp6-collector-" + run_id
    request_id = "sp6-reset-" + run_id
    coverage = {"unconfirmed": 1, "failed_before_send": 2, "unknown_mode": 3, "oldest_record_at": submitted}
    def submission(side, identity, project, mode="selected", at=submitted):
        return {"query_id": "query-" + identity + "-" + run_id, "turn_id": "turn-" + identity + "-" + run_id,
                "project_id": project, "submitted_at": at, "mode_evidence": mode}
    def payload(side, values, ident=collector, extra_coverage=None):
        return {"schema_version": 1, "collector_id": ident, "submissions": values,
                "coverage": extra_coverage if extra_coverage is not None else coverage}
    def ingest(side, values):
        return side.http("POST", "/usage/v1/pro/submissions", payload(side, values))
    lane.check("insert-two-submissions", lambda side: body_is(ingest(side, [
        submission(side, "first", side.project), submission(side, "second", None, "requested")]),
        lambda value: value["accepted"] == 2 and value["inserted"] == 2, "insert counts changed"),
        expected_status=200, domain="usage")
    lane.check("replay-submissions", lambda side: body_is(ingest(side, [
        submission(side, "first", side.project), submission(side, "second", None, "requested")]),
        lambda value: value["accepted"] == 2 and value["inserted"] == 0, "submission replay duplicated counts"),
        expected_status=200, domain="usage")
    lane.check("enrich-project-and-mode", lambda side: ingest(side, [
        submission(side, "second", side.project, "selected")]), expected_status=200, domain="usage")
    lane.check("scoped-counts", lambda side: body_is(side.http(
        "GET", "/usage/v1/pro?project_id=" + q(side.project)),
        lambda value: value["count"] == 2 and value["all_time_count"] == 2
        and value["requested_mode_count"] == 0 and value["first_submission_at"] == submitted,
        "project/mode enrichment changed counts"), expected_status=200, domain="usage")
    lane.check("conflict-rolls-back-whole-batch", lambda side: ingest(side, [
        submission(side, "rolled-back", side.project),
        submission(side, "first", side.project, at=iso(now - dt.timedelta(seconds=20)))]),
        expected_status=409, domain="usage")
    lane.check("post-conflict-count", lambda side: body_is(side.http("GET", "/usage/v1/pro"),
        lambda value: value["all_time_count"] == 2, "conflicting batch partially committed"),
        expected_status=200, domain="usage")
    lane.check("reset-wrong-revision", lambda side: side.http("POST", "/usage/v1/pro/reset",
        {"schema_version": 1, "request_id": "bad-" + request_id, "expected_revision": 2}),
        expected_status=409, domain="usage")
    reset = {"schema_version": 1, "request_id": request_id, "expected_revision": 1}
    def begin_reset(side):
        response = settings(side.http("POST", "/usage/v1/pro/reset", reset), 2, pending=True, manual=False)
        pending = response["body"]["settings"]["pending_reset"]
        side.reset_at = pending["requested_at"]
        side.identity(pending["by"])
        require(pending["request_id"] == request_id, "pending reset request identity changed")
        side.reset_receipt = copy.deepcopy(response)
        return response
    all_success(lane.check("request-reset", begin_reset, expected_status=200, domain="usage"))
    lane.check("reset-idempotent-original-receipt", lambda side: exact_receipt(
        side, side.http("POST", "/usage/v1/pro/reset", reset), "reset_receipt"),
        expected_status=200, domain="usage")
    lane.check("reset-idempotency-conflict", lambda side: side.http("POST", "/usage/v1/pro/reset",
        {**reset, "expected_revision": 2}), expected_status=409, domain="usage")
    lane.check("second-pending-reset", lambda side: side.http("POST", "/usage/v1/pro/reset",
        {"schema_version": 1, "request_id": "other-" + request_id, "expected_revision": 2}),
        expected_status=409, domain="usage")
    weekly = {"resets_at": anchor, "window_minutes": 10080, "used_percent": 12.5}
    old_observation = {"schema_version": 1, "checked_at": checked_before_click,
                       "reset_request_id": request_id, "weekly": weekly}
    lane.check("pre-click-observation-does-not-clear-reset", lambda side: settings(
        side.http("POST", "/usage/v1/pro/limits", old_observation), 3, pending=True, manual=False),
        expected_status=200, domain="usage")
    unverified_at = iso()
    lane.check("unverified-observation-retains-pending-reset", lambda side: settings(
        side.http("POST", "/usage/v1/pro/limits",
                  {"schema_version": 1, "checked_at": unverified_at,
                   "reset_request_id": request_id, "weekly": None}),
        3, pending=True, manual=False), expected_status=200, domain="usage")
    lane.check("stale-limit-observation", lambda side: side.http("POST", "/usage/v1/pro/limits",
        {"schema_version": 1, "checked_at": iso(dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=180)),
         "weekly": weekly}), expected_status=400, domain="usage")
    verified_at = iso(dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=1))
    def complete(side):
        response = settings(side.http("POST", "/usage/v1/pro/limits",
            {"schema_version": 1, "checked_at": verified_at, "reset_request_id": request_id, "weekly": weekly}),
            4, pending=False, manual=True)
        value = response["body"]["settings"]
        require(value["manual_reset_at"] == side.reset_at
                and value["limits_verified_at"] == verified_at and value["limits_error"] is None,
                "verified reset lost its original click boundary")
        return response
    all_success(lane.check("complete-verified-reset", complete, expected_status=200, domain="usage"))
    lane.check("replay-reset-after-completion-keeps-original-receipt", lambda side: exact_receipt(
        side, side.http("POST", "/usage/v1/pro/reset", reset), "reset_receipt"),
        expected_status=200, domain="usage")
    lane.check("completed-counter-boundary", lambda side: body_is(side.http("GET", "/usage/v1/pro"),
        lambda value: value["settings"]["revision"] == 4 and value["settings"]["pending_reset"] is None
        and value["all_time_count"] == 2 and value["count"] == 0
        and value["period_started_at"] == side.reset_at and value["reset_date_stale"] is False,
        "reset replay altered the current counter boundary"), expected_status=200, domain="usage")
    lane.check("reset-history-revision-order", lambda side: body_is(side.http("GET", "/usage/v1/pro/history"),
        lambda value: [entry["kind"] for entry in value["changes"]] ==
        ["reset.completed", "weekly.synced", "reset.requested"]
        and [entry["sequence"] for entry in value["changes"]] == [3, 2, 1],
        "reset history lost an atomic state transition"), expected_status=200, domain="usage")
    after_reset = iso()
    lane.check("submission-after-reset", lambda side: ingest(side, [
        submission(side, "after-reset", side.project, at=after_reset)]), expected_status=200, domain="usage")
    lane.check("counts-after-reset-submission", lambda side: body_is(side.http("GET", "/usage/v1/pro"),
        lambda value: value["all_time_count"] == 3 and value["count"] == 1
        and value["first_submission_at"] == after_reset, "new submission did not use the reset boundary"),
        expected_status=200, domain="usage")
    negatives = [
        ("reset-null-version", "/reset", {**reset, "schema_version": None}),
        ("reset-quoted-revision", "/reset", {**reset, "request_id": "quoted-" + run_id, "expected_revision": "4"}),
        ("reset-null-revision", "/reset", {**reset, "request_id": "null-" + run_id, "expected_revision": None}),
        ("reset-unknown-field", "/reset", {**reset, "unexpected": True}),
        ("limits-null-request", "/limits", {"schema_version": 1, "checked_at": verified_at, "reset_request_id": None, "weekly": weekly}),
        ("limits-null-used-percent", "/limits", {"schema_version": 1, "checked_at": verified_at, "weekly": {**weekly, "used_percent": None}}),
        ("limits-quoted-used-percent", "/limits", {"schema_version": 1, "checked_at": verified_at, "weekly": {**weekly, "used_percent": "12.5"}}),
        ("limits-quoted-window", "/limits", {"schema_version": 1, "checked_at": verified_at, "weekly": {**weekly, "window_minutes": "10080"}}),
        ("limits-missing-weekly", "/limits", {"schema_version": 1, "checked_at": verified_at}),
    ]
    for label, route, value in negatives:
        lane.check(label, lambda side, route=route, value=value: side.http(
            "POST", "/usage/v1/pro" + route, value), expected_status=400, domain="usage")
    lane.check("usage-body-budget", lambda side: side.http("POST", "/usage/v1/pro/submissions",
        b'{"padding":"' + b"x" * (128 * 1024) + b'"}'), expected_status=413, domain="usage")
    # These edge cases deliberately have no status expectation. A strict-safe-
    # integer tightening against the release must appear as a real paired diff.
    lane.check("large-integer-revision-contract", lambda side: side.http("POST", "/usage/v1/pro/reset",
        {"schema_version": 1, "request_id": "large-" + run_id, "expected_revision": 9007199254740992}),
        domain="usage")
    lane.check("large-integer-coverage-contract", lambda side: side.http("POST", "/usage/v1/pro/submissions",
        payload(side, [], "large-collector-" + run_id,
                {**coverage, "unconfirmed": 9007199254740992})), domain="usage")


def inbox(lane, run_id):
    identity = "sp6-question-" + run_id
    request_id = "sp6-answer-" + run_id
    consumer = "sp6-consumer-" + run_id
    def path(side):
        return "/inbox/v1/items/" + q(side.project) + "/" + identity
    def question(side):
        return {"schema_version": 1, "id": identity, "project_id": side.project,
                "source": "  telemetry fixture  ", "title": "  Safe fixture title  ",
                "question": "  Which option?  ", "context": "<img src=x onerror=alert(1)>",
                "options": ["  First  ", "Second"], "blocking_scope": "ticket",
                "blocked_work": [""], "source_url": "https://example.invalid/fixture"}
    def publish(side):
        response = side.http("POST", "/inbox/v1/items", question(side))
        item = response["body"]
        require(item["revision"] == 1 and item["status"] == "pending"
                and item["title"] == "Safe fixture title" and item["options"] == ["First", "Second"]
                and item["recommendation"] == "" and item["deliveries"] == []
                and item["created_at"] == item["updated_at"],
                "question creation/default/trim contract changed")
        side.question_receipt = copy.deepcopy(response)
        return response
    all_success(lane.check("publish-question", publish, expected_status=200, domain="inbox"))
    lane.check("publish-idempotent-receipt", lambda side: exact_receipt(
        side, side.http("POST", "/inbox/v1/items", question(side)), "question_receipt"),
        expected_status=200, domain="inbox")
    lane.check("publish-conflicting-question", lambda side: side.http("POST", "/inbox/v1/items",
        {**question(side), "title": "different question content"}), expected_status=409, domain="inbox")
    lane.check("get-pending-question", lambda side: exact_receipt(
        side, side.http("GET", path(side)), "question_receipt"), expected_status=200, domain="inbox")
    lane.check("pending-filter", lambda side: body_is(side.http(
        "GET", "/inbox/v1/items?status=pending&limit=1&project_id=" + q(side.project)),
        lambda value: len(value["items"]) == 1 and value["total"] == 1 and value["next_offset"] is None,
        "pending filter/pagination changed"), expected_status=200, domain="inbox")
    delivery = {"schema_version": 1, "consumer": consumer, "revision": 2}
    answer = {"schema_version": 1, "request_id": request_id, "expected_revision": 1, "answer": "  First 🙂  "}
    lane.check("delivery-before-answer", lambda side: side.http(
        "POST", path(side) + "/deliveries", {**delivery, "revision": 1}), expected_status=409, domain="inbox")
    lane.check("answer-wrong-revision", lambda side: side.http(
        "POST", path(side) + "/answers", {**answer, "expected_revision": 2}), expected_status=409, domain="inbox")
    def answer_question(side):
        response = side.http("POST", path(side) + "/answers", answer)
        item = response["body"]
        require(item["revision"] == 2 and item["status"] == "answered"
                and item["answer"]["text"] == "First 🙂" and item["answer"]["at"] == item["updated_at"]
                and item["created_at"] == side.question_receipt["body"]["created_at"],
                "answer revision/content/timestamp contract changed")
        require(item["answer"]["by"] == side.principal, "inbox answer uses a different HTTP principal")
        side.answer_receipt = copy.deepcopy(response)
        return response
    all_success(lane.check("answer-question", answer_question, expected_status=200, domain="inbox"))
    lane.check("answer-replay-original-receipt", lambda side: exact_receipt(
        side, side.http("POST", path(side) + "/answers", answer), "answer_receipt"),
        expected_status=200, domain="inbox")
    lane.check("answer-idempotency-conflict", lambda side: side.http(
        "POST", path(side) + "/answers", {**answer, "answer": "Different"}), expected_status=409, domain="inbox")
    def deliver(side):
        response = side.http("POST", path(side) + "/deliveries", delivery)
        item = response["body"]
        require(item["revision"] == 2 and item["updated_at"] == side.answer_receipt["body"]["updated_at"]
                and len(item["deliveries"]) == 1 and item["deliveries"][0]["revision"] == 2,
                "delivery must not change the question revision or answer timestamp")
        side.delivery_receipt = copy.deepcopy(response)
        return response
    all_success(lane.check("delivery-receipt", deliver, expected_status=200, domain="inbox"))
    lane.check("delivery-idempotent-receipt", lambda side: exact_receipt(
        side, side.http("POST", path(side) + "/deliveries", delivery), "delivery_receipt"),
        expected_status=200, domain="inbox")
    lane.check("delivered-consumer-filter", lambda side: body_is(side.http(
        "GET", "/inbox/v1/items?consumer=" + consumer + "&project_id=" + q(side.project)),
        lambda value: value["items"] == [] and value["total"] == 0, "consumer received an acknowledged revision"),
        expected_status=200, domain="inbox")
    lane.check("other-consumer-filter", lambda side: body_is(side.http(
        "GET", "/inbox/v1/items?consumer=sp6-other&project_id=" + q(side.project)),
        lambda value: len(value["items"]) == 1 and value["total"] == 1, "delivery hid the answer from other consumers"),
        expected_status=200, domain="inbox")
    lane.check("answer-replay-after-delivery-original-receipt", lambda side: exact_receipt(
        side, side.http("POST", path(side) + "/answers", answer), "answer_receipt"),
        expected_status=200, domain="inbox")
    lane.check("answer-replay-does-not-rewind-delivery", lambda side: exact_receipt(
        side, side.http("GET", path(side)), "delivery_receipt"), expected_status=200, domain="inbox")
    lane.check("replace-answer-next-revision", lambda side: body_is(side.http(
        "POST", path(side) + "/answers", {**answer, "request_id": request_id + "-next",
                                         "expected_revision": 2, "answer": "Second"}),
        lambda value: value["revision"] == 3 and value["answer"]["text"] == "Second",
        "next answer revision failed"), expected_status=200, domain="inbox")
    lane.check("consumer-sees-new-answer-revision", lambda side: body_is(side.http(
        "GET", "/inbox/v1/items?consumer=" + consumer + "&project_id=" + q(side.project)),
        lambda value: len(value["items"]) == 1 and value["items"][0]["revision"] == 3,
        "consumer filter incorrectly hid a newer answer"), expected_status=200, domain="inbox")
    lane.check("events-first-page", lambda side: body_is(side.http(
        "GET", "/inbox/v1/events?limit=2&project_id=" + q(side.project)),
        lambda value: [event["sequence"] for event in value["events"]] == [1, 2]
        and value["next_after"] == 2, "inbox event cursor/order changed"), expected_status=200, domain="inbox")
    lane.check("events-second-page", lambda side: body_is(side.http(
        "GET", "/inbox/v1/events?after=2&limit=2&project_id=" + q(side.project)),
        lambda value: [event["sequence"] for event in value["events"]] == [3, 4]
        and value["next_after"] == 4, "idempotent receipts added spurious events"),
        expected_status=200, domain="inbox")
    lane.check("events-empty-forward-cursor", lambda side: body_is(side.http(
        "GET", "/inbox/v1/events?after=4&project_id=" + q(side.project)),
        lambda value: value["events"] == [] and value["next_after"] == 4, "empty event cursor regressed"),
        expected_status=200, domain="inbox")
    def inbox_html(side):
        response = side.http("GET", "/activity/inbox")
        require(response["http_status"] == 200 and isinstance(response["body"], str), "inbox UI unavailable")
        for marker in ("/inbox/v1/items", "expected_revision", "textContent"):
            require(marker in response["body"], "inbox UI lost its functional behavior: " + marker)
        return response
    lane.check("inbox-functional-html", inbox_html, normalize=lambda side, response: {
        "http_status": response["http_status"], "headers": side.normalize(response["headers"]),
        "comparison_scope": "functional inbox API/revision/safe text hooks; full HTML in raw evidence",
        "functional_hooks_present": True}, expected_status=200)
    for label, key, value in (
        ("null-blocked-work-member", "blocked_work", [None]),
        ("null-option-member", "options", [None]),
        ("null-context", "context", None),
        ("invalid-source-scheme", "source_url", "javascript:alert(1)"),
        ("question-unknown-field", "extra", True),
    ):
        lane.check(label, lambda side, key=key, value=value: side.http("POST", "/inbox/v1/items",
            {**question(side), "id": identity + "-" + key, key: value}), expected_status=400, domain="inbox")
    for label, query in (
        ("negative-offset", "offset=-1"), ("decimal-limit", "limit=1.0"),
        ("hex-limit", "limit=0x2"), ("duplicate-limit", "limit=1&limit=2"),
        ("invalid-status", "status=unknown"), ("invalid-consumer", "consumer=bad%20consumer"),
    ):
        lane.check(label, lambda side, query=query: side.http(
            "GET", "/inbox/v1/items?" + query + "&project_id=" + q(side.project)),
            expected_status=400, domain="inbox")
    lane.check("unknown-inbox-project", lambda side: side.http(
        "GET", "/inbox/v1/items?project_id=sp6-unknown"), expected_status=404, domain="inbox")
    lane.check("missing-question", lambda side: side.http(
        "GET", "/inbox/v1/items/" + q(side.project) + "/sp6-absent"), expected_status=404, domain="inbox")
    lane.check("inbox-body-budget", lambda side: side.http("POST", "/inbox/v1/items",
        b'{"padding":"' + b"x" * (32 * 1024) + b'"}'), expected_status=413, domain="inbox")
    lane.check("large-answer-revision-contract", lambda side: side.http(
        "POST", path(side) + "/answers", {**answer, "request_id": request_id + "-large",
                                         "expected_revision": 9007199254740992}), domain="inbox")


def content_text(result):
    blocks = result.get("content", [])
    require(len(blocks) == 1 and blocks[0].get("type") == "text"
            and isinstance(blocks[0].get("text"), str), "single-text MCP result contract changed")
    return blocks[0]["text"]


def mcp_result(side, response, structured, text):
    result = copy.deepcopy(raw_result(response))
    result["structuredContent"] = structured
    result["content"][0]["text"] = text
    return {"http_status": response["http_status"], "content_type": response["content_type"],
            "result": side.normalize(result)}


def audit_store_projection(side, response):
    data = side.data(response)
    require(data["enabled"] is True and data["mode"] == "metadata"
            and data["namespace"] == "debug" and data["schema_version"] == "1.0"
            and data["storage_format"] == "jsonl" and data["journal_ref"] == "codexpro://actions",
            "audit status stable fields changed")
    require(data["gap_detected"] is False and data["malformed_records"] == 0,
            "private audit fixture already has a gap or malformed records")
    require(type(data["action_count"]) is int and 0 < data["action_count"] <= 500,
            "bounded status verification needs at most 500 retained fixture actions")
    listed = side.data(side.mcp("activity_list", {"limit": 500}))
    actions = listed["actions"]
    require(len(actions) == data["action_count"], "status/list retained action counts disagree")
    sequences = [action["sequence"] for action in actions]
    require(sequences == sorted(set(sequences)) and sequences[0] == data["retained_from_sequence"]
            and sequences[-1] == data["latest_sequence"]
            and data["next_sequence"] == data["latest_sequence"] + 1,
            "audit sequence/count projection is inconsistent")
    cursor = len(sequences) - 1
    while cursor > 0 and sequences[cursor - 1] + 1 == sequences[cursor]:
        cursor -= 1
    retention = data["retention"]
    require(retention["dropped_through_sequence"] == max(0, sequences[0] - 1)
            and retention["cursor_floor_sequence"] == sequences[cursor] - 1
            and retention["planned_gap_count"] == sequences[-1] - sequences[0] + 1 - len(sequences),
            "audit retention cursor/gap arithmetic changed")
    descriptor = side.manifest["audit"]
    audit_path = Path(descriptor["path"])
    if descriptor["format"] == "jsonl":
        actual_bytes = audit_path.stat().st_size
    else:
        files = list(audit_path.glob("segment-*.jsonl")) + list(audit_path.glob("compact-*.jsonl"))
        require(all(value.is_file() and not value.is_symlink() for value in files),
                "private segment evidence has a non-file path")
        actual_bytes = sum(value.stat().st_size for value in files)
    require(actual_bytes == data["storage_bytes"], "audit storage_bytes disagrees with private files")
    require(type(data["deduplicated_requests"]) is int and data["deduplicated_requests"] >= 0,
            "audit deduplication count must be nonnegative")
    prior = getattr(side, "status_before", None)
    if prior is not None:
        require(data["latest_sequence"] == prior["latest_sequence"] + 3
                and data["action_count"] == prior["action_count"] + 3,
                "three controlled reads did not produce exactly three durable actions")
        require(data["retention"].get("rotation_count") == prior["retention"].get("rotation_count")
                and data["deduplicated_requests"] == prior["deduplicated_requests"],
                "read-only audit inspection unexpectedly compacted or deduplicated requests")
    canonical = copy.deepcopy(data)
    for key in ("retained_from_sequence", "latest_sequence", "next_sequence", "action_count",
                "storage_bytes", "deduplicated_requests"):
        canonical[key] = "<validated-private-store:" + key + ">"
    for key in ("dropped_through_sequence", "cursor_floor_sequence", "planned_gap_count", "rotation_count"):
        canonical["retention"][key] = "<validated-private-retention:" + key + ">"
    if "compacted_at" in canonical["retention"]:
        timestamp_ms(canonical["retention"]["compacted_at"])
        canonical["retention"]["compacted_at"] = "<validated-private-compaction-clock>"
    text = content_text(raw_result(response))
    expected = ("# CodexPro Debug Activity Status\n\nretained_from_sequence=" +
                str(data["retained_from_sequence"]) + "; latest_sequence=" + str(data["latest_sequence"]) +
                "; next_sequence=" + str(data["next_sequence"]) + "; gap_detected=false.")
    require(text == expected, "audit status text no longer agrees with its structured content")
    return mcp_result(side, response, canonical, "<validated-status-text>")
    

def audit_and_activity(lane, run_id):
    def baseline(side):
        response = side.mcp("activity_status", {})
        projection = audit_store_projection(side, response)
        side.status_before = copy.deepcopy(side.data(response))
        return {"response": response, "projection": projection}
    all_success(lane.check("audit-status-private-store", baseline,
                           normalize=lambda side, value: value["projection"]))
    for number in range(3):
        label = "read-" + str(number)
        def read_owned(side, label=label):
            path = side.prefix + "/" + label + ".txt"
            (side.root / path).write_text("telemetry " + label + " 🙂\n")
            response = side.mcp("read", {"workspace_id": side.workspace, "path": path})
            data = side.data(response)
            require(data["path"] == path and data["bytes"] == len(("telemetry " + label + " 🙂\n").encode()),
                    "controlled read returned unexpected fixture metadata")
            side.last_submit_window[label] = {
                "request_id": response["request_id"], "window": (response["started_ms"], response["finished_ms"]),
                "path": path}
            return response
        # Read formatting is already covered by the full files lane. Keep its
        # complete response as evidence, compare the controlled source content.
        lane.check(label, read_owned, normalize=lambda side, response: {
            "http_status": response["http_status"], "content_type": response["content_type"],
            "comparison_scope": "controlled read content/bytes; full files lane compares the complete tool DTO",
            "path": side.data(response)["path"], "text": side.data(response)["text"],
            "bytes": side.data(response)["bytes"]})
        time.sleep(0.004)
    def list_owned(side):
        baseline = side.status_before["latest_sequence"]
        response = side.mcp("activity_list", {"project_id": side.project,
                                             "after_sequence": baseline, "limit": 3})
        data = side.data(response)
        actions = data["actions"]
        require(len(actions) == 3 and [action["sequence"] for action in actions] ==
                [baseline + 1, baseline + 2, baseline + 3], "audit order or sequence continuity changed")
        normalized = copy.deepcopy(data)
        normalized["actions"] = []
        for index, action in enumerate(actions):
            label = "read-" + str(index)
            call = side.last_submit_window[label]
            require(action.get("request_metadata", {}).get("path") == call["path"],
                    "audit read metadata lost its input path")
            normalized["actions"].append(side.validate_action(
                action, label, call["request_id"], call["window"]))
        require(data["next_sequence"] == baseline + 3 and data["latest_sequence"] == baseline + 3
                and data["earliest_sequence"] == side.status_before["retained_from_sequence"]
                and data["has_more"] is False, "audit list forward cursor contract changed")
        normalized["next_sequence"] = "<baseline+3>"
        normalized["latest_sequence"] = "<baseline+3>"
        normalized["earliest_sequence"] = "<validated-retained-start>"
        text = content_text(raw_result(response))
        require(text == "# CodexPro Debug Activity\n\n3 action(s); next_sequence=" + str(baseline + 3)
                + "; earliest_sequence=" + str(data["earliest_sequence"])
                + "; latest_sequence=" + str(baseline + 3) + "; has_more=false.",
                "audit list text lost its structured pagination values")
        return {"response": response, "projection": mcp_result(
            side, response, normalized, "<validated-list-text>")}
    all_success(lane.check("three-durable-action-contracts", list_owned,
                           normalize=lambda side, value: value["projection"]))
    for number in range(3):
        label = "read-" + str(number)
        def get_owned(side, label=label):
            action_id = next(key for key, value in side.action_labels.items() if value == label)
            response = side.mcp("activity_get", {"action_id": action_id})
            data = side.data(response)
            require(canonical_json(data["action"]) == canonical_json(side.actions[action_id]),
                    "activity_get differs from the retained list action")
            text = content_text(raw_result(response))
            action = data["action"]
            require(text == "# CodexPro Debug Action\n\n" + str(action["sequence"]) + " · read · succeeded",
                    "activity_get text summary changed")
            canonical = copy.deepcopy(data)
            canonical["action"] = side.canonical_action(action)
            return {"projection": mcp_result(side, response, canonical, "<validated-get-text>")}
        lane.check("get-" + label, get_owned, normalize=lambda side, value: value["projection"])
    for format in ("json", "jsonl"):
        def export(side, format=format):
            response = side.mcp("activity_export", {"project_id": side.project, "limit": 3,
                "after_sequence": side.status_before["latest_sequence"], "format": format})
            data = side.data(response)
            text = content_text(raw_result(response))
            require(data["export_bytes"] == len(text.encode())
                    and data["export_sha256"] == hashlib.sha256(text.encode()).hexdigest(),
                    "audit export byte count or digest does not describe the actual text")
            require(text.endswith("\n"), "audit export must retain its terminating newline")
            actions = json.loads(text) if format == "json" else [json.loads(line) for line in text.splitlines()]
            require(len(actions) == data["action_count"] == 3 and data["export_format"] == format
                    and data["has_more"] is False and data["truncated_by_bytes"] is False,
                    "bounded export omitted or invented owned actions")
            canonical_actions = []
            for action in actions:
                require(action["action_id"] in side.actions and canonical_json(action) ==
                        canonical_json(side.actions[action["action_id"]]), "export action differs from its durable receipt")
                canonical_actions.append(side.canonical_action(action))
            require(data["next_sequence"] == data["latest_sequence"] == side.status_before["latest_sequence"] + 3
                    and data["earliest_sequence"] == side.status_before["retained_from_sequence"],
                    "export sequence boundaries changed")
            canonical = copy.deepcopy(data)
            for key in ("next_sequence", "latest_sequence"):
                canonical[key] = "<baseline+3>"
            canonical["earliest_sequence"] = "<validated-retained-start>"
            canonical["export_bytes"] = "<validated-export-UTF8-length>"
            canonical["export_sha256"] = "<validated-export-SHA256>"
            return {"projection": mcp_result(side, response, canonical, canonical_actions)}
        lane.check("audit-export-" + format, export, normalize=lambda side, value: value["projection"])
    lane.check("audit-status-after-inspection", lambda side: side.mcp("activity_status", {}),
               normalize=audit_store_projection)
    lane.check("project-activity-json", lambda side: side.http("GET",
        "/activity/projects/" + q(side.project) + ".json?limit=3&output_bytes=0"),
        normalize=lambda side, response: side.validate_activity(response), expected_status=200)
    lane.check("project-activity-query-alias", lambda side: side.http("GET",
        "/activity.json?project_id=" + q(side.project) + "&limit=3&output_bytes=0"),
        normalize=lambda side, response: side.validate_activity(response), expected_status=200)
    for label, path, status in (
        ("unknown-project-precedes-invalid-limit", "/activity.json?project_id=sp6-unknown&limit=no", 404),
        ("activity-invalid-limit", "/activity.json?limit=no", 400),
        ("activity-duplicate-limit", "/activity.json?limit=1&limit=2", 400),
        ("activity-output-too-large", "/activity.json?output_bytes=4097", 400),
    ):
        lane.check(label, lambda side, path=path: side.http("GET", path), expected_status=status)
    lane.check("activity-get-does-not-append-audit", lambda side: side.mcp("activity_status", {}),
               normalize=audit_store_projection)


def activity_nonack_and_cache(lane, run_id):
    def start(side):
        response = side.mcp("start_jobs", {"workspace_id": side.workspace,
            "commands": [{"command": "printf 'telemetry-job\\n'", "cwd": side.prefix}]})
        data = side.data(response)
        jobs = data.get("jobs", [])
        require(len(jobs) == 1 and re.fullmatch(r"job_[a-f0-9]{8}", jobs[0]["job_id"]),
                "owned job launch did not return one compact job identity")
        job_id = jobs[0]["job_id"]
        side.telemetry_job = job_id
        side.jobs[job_id] = "<telemetry-job>"
        side.owned_jobs.add(job_id)
        require(jobs[0]["status"] in ("running", "succeeded"), "owned command failed before acceptance")
        return {"job_id": "<telemetry-job>", "accepted": True,
                "comparison_scope": "owned acceptance; bounded jobs wait below verifies exact terminal outcome"}
    all_success(lane.check("launch-owned-nonack-fixture", start, normalize=lambda side, value: value))
    def wait(side):
        response = side.mcp("jobs", {"workspace_id": side.workspace,
            "job_ids": [side.telemetry_job], "output": "none", "wait_ms": 5000})
        jobs = side.data(response).get("jobs", [])
        require(len(jobs) == 1 and jobs[0]["job_id"] == side.telemetry_job and jobs[0]["status"] == "succeeded"
                and jobs[0].get("exit_code") == 0, "owned nonack fixture failed to complete")
        return {"status": jobs[0]["status"], "exit_code": jobs[0]["exit_code"],
                "comparison_scope": "owned terminal fixture; full job DTO is covered by process lane"}
    all_success(lane.check("terminal-without-output-ack", wait, normalize=lambda side, value: value))
    def inspect_job(side):
        responses = [
            side.http("GET", "/activity.json?project_id=" + q(side.project) + "&output_bytes=128&limit=10"),
            side.http("GET", "/activity/live?project_id=" + q(side.project)),
            side.http("GET", "/activity/job?job_id=" + q(side.telemetry_job)
                      + "&workspace_id=" + q(side.workspace)),
        ]
        require(all(value["http_status"] == 200 for value in responses), "a job activity route failed")
        project = responses[0]["body"]["project"]
        commands = [item for item in project["recent_commands"] if side.telemetry_job in item.get("job_ids", [])]
        require(commands, "activity JSON omitted the owned terminal job")
        require(any("telemetry-job" in canonical_json(item["output"]) for item in commands),
                "bounded activity output omitted the owned stdout")
        require("telemetry-job" in responses[2]["body"], "job fragment lost retained output")
        return {"comparison_scope": "owned retained output and all read-only monitor route statuses",
                "statuses": [value["http_status"] for value in responses], "retained_output": "telemetry-job"}
    lane.check("monitor-owned-terminal-output", inspect_job, normalize=lambda side, value: value)
    def digest_read(side, first):
        response = side.mcp("read", {"workspace_id": side.workspace, "path": side.prefix + "/binding.txt"})
        data = side.data(response)
        notifications = data.get("background_jobs", [])
        owned = [item for item in notifications if item.get("job_id") == side.telemetry_job]
        require(bool(owned) is first, "activity requests acknowledged output, or ordinary tool digest failed to acknowledge")
        if first:
            require(len(owned) == 1 and owned[0]["status"] == "succeeded" and owned[0]["exit_code"] == 0,
                    "completion digest changed the owned terminal outcome")
        return {"comparison_scope": "owned completion notification across monitor and ordinary read",
                "owned_completion_visible": bool(owned)}
    lane.check("first-normal-read-still-sees-completion", lambda side: digest_read(side, True),
               normalize=lambda side, value: value)
    lane.check("second-normal-read-has-acknowledged-completion", lambda side: digest_read(side, False),
               normalize=lambda side, value: value)
    # Let previous dashboard reads expire so each warm GET establishes a new
    # comparable cache age. The only wait is bounded and never exceeds 11 s.
    time.sleep(10.2)
    cache_name = "000-sp6-cache-" + run_id + ".txt"
    def warm_and_change(side):
        require((side.root / ".git").exists(), "Git cache contract requires an initialized private fixture")
        path = "/activity/data?project_id=" + q(side.project)
        response = side.http("GET", path)
        sections = partial_sections(side, response)
        require("projects" in sections and cache_name not in sections["projects"],
                "cache fixture is not absent before creation")
        side.cache_file = side.root / cache_name
        side.cache_file.write_text("owned cache refresh fixture\n")
        cached = side.http("GET", path)
        require(cached["finished_ms"] - response["started_ms"] < 9500,
                "host load exhausted the cache precondition window")
        cached_sections = partial_sections(side, cached)
        require(cache_name not in cached_sections["projects"], "dashboard ignored its ten-second Git cache")
        side.cache_observed_at = time.monotonic()
        return {"section_keys": list(sections), "cached_section_keys": list(cached_sections),
                "comparison_scope": "Git cache freshness and section protocol; full HTML in raw evidence",
                "new_untracked_path_visible_within_TTL": False,
                "headers": side.normalize(cached["headers"])}
    try:
        all_success(lane.check("git-cache-serves-warm-snapshot", warm_and_change,
                               normalize=lambda side, value: value))
        time.sleep(10.2)
        def refreshed(side):
            response = side.http("GET", "/activity/data?project_id=" + q(side.project))
            sections = partial_sections(side, response)
            require(cache_name in sections["projects"], "Git cache failed to refresh after its TTL")
            return {"section_keys": list(sections), "new_untracked_path_visible_after_TTL": True,
                    "headers": side.normalize(response["headers"]),
                    "comparison_scope": "cache expiration and refreshed Git path evidence"}
        lane.check("git-cache-refreshes-after-TTL", refreshed, normalize=lambda side, value: value)
    finally:
        for side in lane.sides:
            owned = getattr(side, "cache_file", None)
            if owned is not None and owned.parent == side.root and owned.name == cache_name:
                owned.unlink(missing_ok=True)


PHASES = (
    ("preflight-auth", preflight),
    ("usage", usage),
    ("inbox", inbox),
    ("audit-activity", audit_and_activity),
    ("activity-nonack-cache", activity_nonack_and_cache),
)

