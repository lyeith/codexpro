#!/usr/bin/env python3
"""Inactive, opt-in comparison policy for a narrowly recorded coordinator ruling.

The telemetry runner does not import this module. Activation requires the parent
to supply the actual approval record and wire only the two named cases after
reading the coordinator's ruling. There is no environment-variable fallback.
"""
from __future__ import annotations

import hashlib
import re

SOURCE_COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"
DELTA_ID = "http-413-node-development-stack-omission"
CASES = frozenset(("usage/usage-body-budget", "inbox/inbox-body-budget"))
HTML_PREFIX = (
    '<!DOCTYPE html>\n<html lang="en">\n<head>\n'
    '<meta charset="utf-8">\n<title>Error</title>\n</head>\n<body>\n<pre>'
)
ERROR_LINE = "PayloadTooLargeError: request entity too large"
HTML_SUFFIX = "</pre>\n</body>\n</html>\n"
STABLE_BODY = HTML_PREFIX + ERROR_LINE + HTML_SUFFIX

# Exact stack shape observed from release 44bb76b in both failed requests.
# Only the shared absolute release-directory prefix varies. Function names,
# module paths, line/column numbers, order, count, and HTML framing are fixed.
STACK_FRAMES = (
    ("readStream", "node_modules/raw-body/index.js", 163, 17, False),
    ("getRawBody", "node_modules/raw-body/index.js", 116, 12, False),
    ("read", "node_modules/body-parser/lib/read.js", 134, 3, False),
    ("jsonParser", "node_modules/body-parser/lib/types/json.js", 60, 5, False),
    ("Layer.handleRequest", "node_modules/router/lib/layer.js", 152, 17, False),
    ("next", "node_modules/router/lib/route.js", 157, 13, False),
    ("sameOriginAdminRequest", "dist/http.js", 1545, 13, True),
    ("Layer.handleRequest", "node_modules/router/lib/layer.js", 152, 17, False),
    ("next", "node_modules/router/lib/route.js", 157, 13, False),
    ("Route.dispatch", "node_modules/router/lib/route.js", 117, 3, False),
)


def _reference_pattern():
    pieces = [re.escape(HTML_PREFIX + ERROR_LINE)]
    for index, (name, module, line, column, file_url) in enumerate(STACK_FRAMES):
        pieces.append(re.escape("<br> &nbsp; &nbsp;at " + name + " ("))
        if file_url:
            pieces.append(re.escape("file://"))
        pieces.append(r"(?P<release>/[^\r\n<>()]+)" if index == 0 else r"(?P=release)")
        pieces.append(re.escape("/" + module + ":" + str(line) + ":" + str(column) + ")"))
    pieces.append(re.escape(HTML_SUFFIX))
    return re.compile("".join(pieces))


REFERENCE_PATTERN = _reference_pattern()


def _require(condition, message):
    if not condition:
        raise ValueError(message)


def _validated_approval(approval):
    _require(isinstance(approval, dict), "explicit coordinator approval record is required")
    _require(approval.get("delta_id") == DELTA_ID
             and approval.get("source_commit") == SOURCE_COMMIT
             and approval.get("approved") is True,
             "approval does not authorize this release-specific delta")
    _require(approval.get("authority") == "plans/coordinator"
             and approval.get("registry_path") == "/home/spite/Projects/workpackets/REGISTRY.md"
             and approval.get("registry_row") == 31,
             "approval provenance does not match the design review channel")
    _require(isinstance(approval.get("ruling_at"), str)
             and re.fullmatch(r"2026-10-31T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z", approval["ruling_at"]),
             "approval requires the observed ruling's UTC timestamp")
    _require(isinstance(approval.get("ruling_sha256"), str)
             and re.fullmatch(r"[a-f0-9]{64}", approval["ruling_sha256"]),
             "approval requires the recorded ruling text hash")
    return {key: approval[key] for key in (
        "delta_id", "source_commit", "approved", "authority", "registry_path",
        "registry_row", "ruling_at", "ruling_sha256",
    )}


def payload_too_large_projection(side, response, *, approval):
    """Validate all stable evidence, then omit only the approved Node frames.

    This is intended as an explicit normalize callback for the two named lane
    checks. It does not edit the response or raw evidence. Missing approval,
    unknown case/side, changed first line, stack shape, HTML or required headers
    fails the case instead of broadening the exception.
    """
    recorded = _validated_approval(approval)
    _require(side.current_case in CASES, "413 allowance used outside its two approved cases")
    _require(side.label in ("reference", "candidate"), "unknown telemetry side")
    _require(response.get("http_status") == 413, "approved 413 response status changed")
    headers = response.get("headers")
    _require(isinstance(headers, dict), "413 response headers are missing")
    for key, value in (
        ("content-type", "text/html; charset=utf-8"),
        ("content-security-policy", "default-src 'none'"),
        ("x-content-type-options", "nosniff"),
    ):
        _require(headers.get(key) == value, "413 response changed required header: " + key)
    body = response.get("body")
    _require(isinstance(body, str) and response.get("raw_body") == body,
             "413 response is not the unmodified raw HTML body")
    if side.label == "reference":
        _require(REFERENCE_PATTERN.fullmatch(body) is not None,
                 "reference 413 differs from the exact captured ten-frame Express response")
        frames = len(STACK_FRAMES)
    else:
        _require(body == STABLE_BODY, "candidate 413 differs from the approved stack-free HTML response")
        frames = 0
    side.normalizations.append({
        "path": "$.body", "rule": DELTA_ID, "case": side.current_case,
        "approval": recorded, "validated_stack_frames": frames,
        "raw_body_sha256": hashlib.sha256(body.encode("utf-8")).hexdigest(),
    })
    return {
        "http_status": response["http_status"],
        "headers": side.normalize(headers, "exact"),
        "body": STABLE_BODY,
    }
