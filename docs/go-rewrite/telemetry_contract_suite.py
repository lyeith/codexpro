#!/usr/bin/env python3
"""Private paired HTTP/observability contracts using contract_suite.Client.

Run sequentially with other mutation lanes, against fresh usage/inbox stores.
Both explicit launch manifests and MCP fixture bindings are verified before any
HTTP mutation. Ports 7200 and all protected live state paths are refused.
Raw response evidence is gzip JSONL, checked for credentials before writing.
"""
from __future__ import annotations

import argparse
import copy
import datetime as dt
import difflib
import gzip
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sqlite3
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from contract_suite import Client, SOURCE_COMMIT, raw_result
from telemetry_approved_deltas import (
    CASES as APPROVED_413_CASES, DELTA_ID as APPROVED_413_DELTA,
    load_approval, payload_too_large_projection,
)

MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_EVIDENCE_BYTES = 64 * 1024 * 1024
COMPARED_HEADERS = (
    "content-type", "cache-control", "content-security-policy",
    "x-content-type-options", "www-authenticate",
)


class ContractFailure(AssertionError):
    pass


def require(condition, message):
    if not condition:
        raise ContractFailure(message)


def canonical_json(value):
    # JSON text equality distinguishes true from 1, unlike Python ==.
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def iso(value=None):
    value = value or dt.datetime.now(dt.timezone.utc)
    return value.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def timestamp_ms(value):
    require(isinstance(value, str) and re.fullmatch(
        r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z", value),
        "timestamp must preserve the exact millisecond UTC format")
    try:
        return round(dt.datetime.fromisoformat(value[:-1] + "+00:00").timestamp() * 1000)
    except ValueError as error:
        raise ContractFailure("invalid timestamp calendar value") from error


def path_allowed(path, *, allow_missing=False):
    path = Path(path).expanduser().resolve(strict=not allow_missing)
    home = Path.home().resolve()
    require(path not in (Path("/"), home), "root/home cannot be a private fixture")
    protected = (home / ".codexpro", home / ".config" / "codexpro",
                 home / "Projects" / "codexpro", home / "apps" / "codexpro")
    require(not any(path == root or path.is_relative_to(root) for root in protected),
            "protected live/project path refused")
    return path


def process_start_ticks(pid):
    raw = Path(f"/proc/{pid}/stat").read_text()
    tail = raw.rsplit(")", 1)[1].split()
    return int(tail[19])


def process_fds(pid):
    sockets, files = set(), set()
    for fd in Path(f"/proc/{pid}/fd").iterdir():
        try:
            value = os.readlink(fd)
        except FileNotFoundError:
            continue
        match = re.fullmatch(r"socket:\[(\d+)\]", value)
        if match:
            sockets.add(match[1])
        elif value.startswith("/") and not value.endswith(" (deleted)"):
            try:
                files.add(Path(value).resolve(strict=True))
            except FileNotFoundError:
                pass
    return sockets, files


def validate_launch(manifest, endpoint, token_file, expected_port):
    parsed = urllib.parse.urlsplit(endpoint)
    require(parsed.scheme == "http" and parsed.hostname in ("127.0.0.1", "localhost", "::1")
            and parsed.port == expected_port and parsed.path == "/mcp"
            and not parsed.query and not parsed.fragment
            and parsed.username is None and parsed.password is None,
            "lane accepts only the declared private loopback /mcp port")
    require(manifest.get("port") == expected_port, "launch manifest port mismatch")
    pid = manifest.get("pid")
    require(type(pid) is int and pid > 1, "manifest needs the listening server PID")
    require(Path(f"/proc/{pid}").stat().st_uid == os.getuid(), "server must belong to the current unprivileged user")
    require(process_start_ticks(pid) == manifest.get("process_start_ticks"),
            "server PID was reused or launch manifest is stale")
    sockets, _ = process_fds(pid)
    listener = False
    for family in ("tcp", "tcp6"):
        path = Path(f"/proc/{pid}/net/{family}")
        if not path.exists():
            continue
        for line in path.read_text().splitlines()[1:]:
            columns = line.split()
            if len(columns) > 9 and columns[3] == "0A":
                port = int(columns[1].rsplit(":", 1)[1], 16)
                listener |= port == expected_port and columns[9] in sockets
    require(listener, "declared PID does not own the private listening socket")
    state = path_allowed(manifest["state_root"])
    root = path_allowed(manifest["fixture_root"])
    token = path_allowed(token_file)
    require(token == path_allowed(manifest["token_file"]), "token path does not match private launch")
    require(root.is_dir() and state.is_dir(), "private fixture/state directories must exist")
    for key in ("usage_database", "inbox_database"):
        path = path_allowed(manifest[key], allow_missing=True)
        require(path.is_relative_to(state), f"{key} is outside the private state root")
    audit = manifest["audit"]
    require(audit.get("format") in ("jsonl", "segments"), "manifest needs an audit storage format")
    audit_path = path_allowed(audit["path"], allow_missing=True)
    require(audit_path.is_relative_to(state), "audit path is outside private state")
    require(isinstance(manifest.get("project_id"), str) and manifest["project_id"],
            "manifest must bind one configured project")
    return root, state, token


class RawEvidence:
    def __init__(self, path, clients):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.file = gzip.open(self.path, "xb")
        os.chmod(self.path, 0o600)
        self.clients = clients
        self.records = self.bytes = 0

    def append(self, value):
        for client in self.clients:
            client.assert_no_credential(value)
        encoded = (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode()
        require(self.bytes + len(encoded) <= MAX_EVIDENCE_BYTES, "raw evidence byte budget exceeded")
        self.file.write(encoded)
        self.bytes += len(encoded)
        self.records += 1
        return {"record": self.records, "sha256": hashlib.sha256(encoded).hexdigest()}

    def close(self):
        self.file.close()


class Side:
    def __init__(self, label, endpoint, token_file, manifest_path, run_id):
        self.label = label
        self.manifest = json.loads(Path(manifest_path).read_text())
        port = 7211 if label == "reference" else 7210
        self.root, self.state, self.token_file = validate_launch(self.manifest, endpoint, token_file, port)
        self.project = self.manifest["project_id"]
        self.pid = self.manifest["pid"]
        self.origin = urllib.parse.urlunsplit((*urllib.parse.urlsplit(endpoint)[:2], "", "", ""))
        self.client = Client(endpoint, str(self.token_file), timeout=25)
        self.prefix = "sp6-telemetry-contract/" + run_id
        self.fixture = self.root / self.prefix
        self.fixture.mkdir(parents=True, exist_ok=False)
        self.clock_start_ms = int(time.time() * 1000)
        self.principal = None
        self.reset_at = None
        self.jobs = {}
        self.owned_jobs = set()
        self.actions = {}
        self.action_labels = {}
        self.last_submit_window = {}
        self.normalizations = []
        self.workspace = None
        self.evidence = None
        self.current_case = "setup"
        response = self.client.call("list_projects", {})
        projects = raw_result(response)["structuredContent"]["projects"]
        matches = [value for value in projects if value.get("id") == self.project]
        require(len(matches) == 1, "private project is not uniquely configured")
        self.project_label = matches[0]["label"]
        self.workspace = matches[0]["workspace_id"]
        marker = "private-telemetry-fixture-" + self.label + "-" + str(self.pid) + "-" + run_id
        marker_bytes = (marker + "\n").encode()
        (self.fixture / "binding.txt").write_bytes(marker_bytes)
        response = self.client.call("read", {"workspace_id": self.workspace,
                   "path": self.prefix + "/binding.txt", "start_line": 1, "end_line": 1})
        result = raw_result(response)
        require(result.get("isError") is not True, "private fixture read failed")
        lines = result["structuredContent"]["text"].splitlines()
        plain = "\n".join(re.sub(r"^\s*\d+ \| ?", "", line) for line in lines)
        require(plain == marker, "MCP workspace does not map to the explicit private root")
        require(result["structuredContent"].get("sha256") == hashlib.sha256(marker_bytes).hexdigest(),
                "MCP nonce hash does not bind the complete private fixture file")

    def set_evidence(self, evidence):
        self.evidence = evidence

    def record(self, value):
        if self.evidence:
            return self.evidence.append({"side": self.label, "case": self.current_case, **value})
        return None

    def http(self, method, path, body=None, *, headers=None, auth="bearer"):
        require(path.startswith("/") and not path.startswith("//"), "HTTP route must be local")
        if method not in ("GET", "HEAD"):
            require(getattr(self, "stores_bound", False), "HTTP mutation before private DB binding")
        target = self.origin + path
        actual_headers = {"Accept": "application/json"}
        if auth == "bearer":
            actual_headers["Authorization"] = "Bearer " + self.client.credential
        elif auth == "query":
            separator = "&" if "?" in target else "?"
            target += separator + urllib.parse.urlencode([("codexpro_token", self.client.credential)])
        elif auth == "wrong":
            actual_headers["Authorization"] = "Bearer fixture-invalid-credential"
        elif auth != "missing":
            raise ValueError("invalid auth mode")
        if headers:
            actual_headers.update(headers)
        encoded = None
        if body is not None:
            encoded = body if isinstance(body, bytes) else json.dumps(body, ensure_ascii=False).encode()
            actual_headers["Content-Type"] = "application/json"
        request = urllib.request.Request(target, data=encoded, headers=actual_headers, method=method)
        start_ms, start = int(time.time() * 1000), time.monotonic()
        try:
            response = urllib.request.urlopen(request, timeout=25)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
            require(len(raw) <= MAX_RESPONSE_BYTES, "HTTP response exceeds bounded evidence budget")
            text = raw.decode("utf-8")
            try:
                value = json.loads(text)
            except json.JSONDecodeError:
                value = text
            selected = {name: response.headers.get(name) for name in COMPARED_HEADERS
                        if response.headers.get(name) is not None}
            result = {"http_status": response.status, "headers": selected, "body": value,
                      "raw_body": text, "started_ms": start_ms, "finished_ms": int(time.time() * 1000),
                      "duration_ms": (time.monotonic() - start) * 1000}
        self.client.assert_no_credential(result)
        self.record({"transport": "http", "method": method, "path": path, "auth": auth,
                     "request_body": body.decode() if isinstance(body, bytes) else body,
                     "response": result})
        return result

    def mcp(self, name, args):
        start_ms = int(time.time() * 1000)
        request_id = self.client.sequence + 1
        response = self.client.call(name, args)
        require(isinstance(response["body"], dict) and response["body"].get("id") == request_id,
                "MCP response does not bind the exact request ID")
        response["request_id"] = request_id
        response["started_ms"] = start_ms
        response["finished_ms"] = int(time.time() * 1000)
        self.client.assert_no_credential(response)
        self.record({"transport": "mcp", "tool": name, "arguments": args, "response": response})
        return response

    def bind_stores(self):
        # Initial GETs are read-only route operations. Both database descriptors
        # must then belong to the declared listening PID before any POST.
        self.http("GET", "/usage/v1/pro")
        self.http("GET", "/inbox/v1/items?project_id=" + urllib.parse.quote(self.project))
        _, opened = process_fds(self.pid)
        for key in ("usage_database", "inbox_database"):
            expected = path_allowed(self.manifest[key])
            require(expected in opened, f"private server did not open its declared {key}")
        self.stores_bound = True

    def clock(self, value, path, response=None):
        measured = timestamp_ms(value)
        end = response["finished_ms"] if response else int(time.time() * 1000)
        require(self.clock_start_ms - 2000 <= measured <= end + 2000,
                "server-generated clock lies outside this lane's observation window: " + path)
        self.normalizations.append({"path": path, "rule": "validated server UTC clock"})
        return "<server-clock>"

    def identity(self, value):
        require(isinstance(value, str) and re.fullmatch(r"connector_[a-f0-9]{32}", value),
                "static private HTTP principal shape changed")
        if self.principal is None:
            self.principal = value
        require(value == self.principal, "HTTP principal changed within the same private token")
        declared = self.manifest.get("principal_id")
        if declared:
            require(value == declared, "HTTP principal differs from the launch manifest")
        return "<private-static-principal>"

    def normalize(self, value, domain="exact", path="$", key=None, response=None):
        if isinstance(value, dict):
            return {name: self.normalize(item, domain, path + "." + name, name, response)
                    for name, item in value.items()}
        if isinstance(value, list):
            return [self.normalize(item, domain, path + f"[{index}]", key, response)
                    for index, item in enumerate(value)]
        if domain in ("usage", "inbox") and key == "by" and value is not None:
            return self.identity(value)
        if domain == "usage" and value is not None:
            if key in ("generated_at", "last_sync_at", "requested_at", "manual_reset_at"):
                if key in ("requested_at", "manual_reset_at") and self.reset_at is not None:
                    require(value == self.reset_at, "reset boundary lost the original request time")
                return self.clock(value, path, response)
            if key == "period_started_at" and self.reset_at is not None and value == self.reset_at:
                return self.clock(value, path, response)
        if domain == "inbox" and key in ("created_at", "updated_at", "at") and value is not None:
            return self.clock(value, path, response)
        if isinstance(value, str):
            value = value.replace(str(self.root), "<private-fixture>").replace(self.workspace, "<workspace>")
            value = value.replace(self.origin, "<private-origin>")
            for original, label in self.jobs.items():
                value = value.replace(original, label)
        return value

    def canonical_http(self, response, domain):
        return {"http_status": response["http_status"],
                "headers": self.normalize(response["headers"], "exact"),
                "body": self.normalize(response["body"], domain, response=response)}

    def data(self, response):
        require(response["http_status"] == 200 and isinstance(response["body"], dict)
                and "result" in response["body"], "MCP failed at transport/RPC boundary")
        result = raw_result(response)
        require(result.get("isError") is not True, "MCP returned a tool error")
        require(isinstance(result.get("structuredContent"), dict), "MCP lacks structuredContent")
        return result["structuredContent"]

    def audit_status(self):
        return self.data(self.mcp("activity_status", {}))

    def validate_action(self, action, label, request_id, call_window):
        require(type(action["sequence"]) is int and action["sequence"] > 0,
                "action sequence must be a positive integer")
        require(action["schema_version"] == "1.0" and action["namespace"] == "debug", "audit schema changed")
        require(re.fullmatch(r"cpa_[a-f0-9]{32}", action["action_id"]), "action ID shape changed")
        require(action["tool_name"] == "read" and action["status"] == "succeeded"
                and action["mutating"] is False, "read action attribution/outcome changed")
        require(action["project_id"] == self.project and action["workspace_id"] == self.workspace,
                "audit action lost exact project/workspace attribution")
        require(action["result_ref"] == "codexpro://actions/" + action["action_id"], "action reference is unbound")
        start, end = timestamp_ms(action["occurred_at"]), timestamp_ms(action["finished_at"])
        require(call_window[0] - 2000 <= start <= end <= call_window[1] + 2000, "action times escape its request")
        require(type(action["duration_ms"]) is int and action["duration_ms"] == end - start,
                "action duration does not match its timestamps")
        require(re.fullmatch(r"srv_[a-f0-9]{32}", action["server_session_ref"]), "server audit identity shape changed")
        require(self.principal is not None, "HTTP principal must be bound before audit attribution")
        expected_actor = "actor_" + digest(self.principal)[:32]
        expected_request = "request_" + digest(str(request_id))[:32]
        require(action["actor_ref"] == expected_actor, "audit actor does not bind the authenticated principal")
        require(action["request_ref"] == expected_request, "audit request reference does not bind the MCP ID")
        transport = action.get("transport_session_ref")
        if transport is not None:
            require(transport == "transport_" + digest(self.client.session)[:32],
                    "audit transport reference does not bind the MCP session")
        expected_fp = digest("\0".join((expected_actor, transport or action["server_session_ref"],
                                      expected_request, "read")))
        require(action.get("request_fingerprint") == expected_fp, "audit request fingerprint changed")
        self.actions[action["action_id"]] = copy.deepcopy(action)
        self.action_labels[action["action_id"]] = label
        return self.canonical_action(action)

    def canonical_action(self, action):
        label = self.action_labels[action["action_id"]]
        result = self.normalize(copy.deepcopy(action))
        result["sequence"] = "<owned-sequence:" + label + ">"
        result["action_id"] = "<action:" + label + ">"
        result["result_ref"] = "codexpro://actions/<action:" + label + ">"
        for name in ("occurred_at", "finished_at"):
            result[name] = "<validated-action-clock>"
        result["duration_ms"] = "<validated-action-duration>"
        result["actor_ref"] = "<validated-principal-hash>"
        result["request_ref"] = "<validated-request-id-hash>"
        result["server_session_ref"] = "<validated-server-instance>"
        if "transport_session_ref" in result:
            result["transport_session_ref"] = "<validated-MCP-session-hash>"
        result["request_fingerprint"] = "<validated-request-fingerprint>"
        self.normalizations.append({"path": "audit.actions." + label,
            "rule": "validated positive sequence, bound request/principal/session hashes, request window and exact duration"})
        return result

    def validate_activity(self, response, limit=3):
        data = response["body"]
        require(response["http_status"] == 200 and isinstance(data, dict), "activity JSON unavailable")
        require(data["schema_version"] == 1 and data["limits"] == {
            "recent_commands": limit, "output_bytes_per_command": 0,
            "inflight_per_kind": 10, "quiet_after_ms": 300000}, "activity limits contract changed")
        require(data["coverage"] == {
            "tool_calls": "current_server_process", "jobs": "persistent_job_store",
            "audit_enabled": True, "recent_receipts_scanned_per_project": limit * 3,
            "history_incomplete": False,
            "note": "Recorded CodexPro activity only. Quiet signals invite review, not automatic cancellation. Output is a bounded retained tail; requests do not acknowledge jobs or heartbeat claims."},
            "activity coverage must describe the independently verified private stores")
        generated = timestamp_ms(data["generated_at"])
        require(response["started_ms"] - 2000 <= generated <= response["finished_ms"] + 2000,
                "activity generated_at escapes its GET")
        project = data["project"]
        require(project["project_id"] == self.project and project["label"] == self.project_label,
                "activity project attribution changed")
        require(project["url"] == "/activity/projects/" + urllib.parse.quote(self.project, safe="") + ".json",
                "activity project URL changed")
        require(project["inflight_counts"] == {"tool_calls": 0, "jobs": 0, "claims": 0}
                and project["has_inflight_work"] is False, "monitor fabricated in-flight work")
        require(project["work_runs"] == [] and project["work_runs_total"] == 0,
                "this lane requires an empty private work archive")
        require(project["inflight"] == {"calls": [], "jobs": [], "truncated": False},
                "unexpected in-flight payload")
        recent = project["recent_commands"]
        require(len(recent) == limit, "recent activity count changed")
        ordered = sorted(self.actions, key=lambda key: timestamp_ms(self.actions[key]["finished_at"]), reverse=True)
        require([item["action_id"] for item in recent] == ordered[:limit],
                "recent actions are duplicated, omitted, or out of completion order")
        normalized = copy.deepcopy(data)
        normalized["generated_at"] = "<validated-GET-clock>"
        normalized["project"] = self.normalize(normalized["project"])
        normalized_recent = normalized["project"]["recent_commands"]
        for index, item in enumerate(recent):
            require(item["action_id"] in self.actions, "activity introduced an unrecorded recent action")
            action = self.actions[item["action_id"]]
            require(item["tool"] == action["tool_name"] and item["status"] == action["status"],
                    "activity changed recorded outcome")
            require(item["started_at"] == action["occurred_at"] and item["finished_at"] == action["finished_at"],
                    "activity does not use the original receipt timestamps")
            require(item["duration_ms"] == action["duration_ms"], "activity duration differs from its receipt")
            for field, at in (("started_age_ms", item["started_at"]), ("finished_age_ms", item["finished_at"])):
                require(item[field] == max(0, generated - timestamp_ms(at)), "activity age arithmetic changed")
                normalized_recent[index][field] = "<validated-age>"
            require(item["job_ids"] == [] and item["job_ids_total"] == 0
                    and item["output"] == {"available": False, "reason": "not_retained_or_not_a_shell_command"},
                    "read activity fabricated job output")
            label = self.action_labels[item["action_id"]]
            normalized_recent[index]["action_id"] = "<action:" + label + ">"
            normalized_recent[index]["started_at"] = "<validated-action-clock>"
            normalized_recent[index]["finished_at"] = "<validated-action-clock>"
            normalized_recent[index]["duration_ms"] = "<validated-action-duration>"
        starts = [timestamp_ms(value["occurred_at"]) for value in self.actions.values()]
        finishes = [timestamp_ms(value["finished_at"]) for value in self.actions.values()]
        for key, expected in (("last_command_started_at", max(starts)),
                              ("last_command_finished_at", max(finishes)),
                              ("last_activity_at", max(starts + finishes))):
            require(timestamp_ms(project[key]) == expected, "activity newest timestamp selection changed")
            age_key = key[:-3] + "_age_ms"
            require(project[age_key] == max(0, generated - expected), "activity summary age arithmetic changed")
            normalized["project"][key] = "<validated-latest-action>"
            normalized["project"][age_key] = "<validated-latest-age>"
        require(project["review_recommended"] is False and project["signals"] == []
                and project["signals_total"] == 0, "idle fresh fixture received a synthetic alert")
        return {"http_status": response["http_status"], "headers": response["headers"], "body": normalized}

    def cleanup(self):
        errors = []
        if self.owned_jobs:
            try:
                result = raw_result(self.client.call("stop_jobs", {
                    "workspace_id": self.workspace, "job_ids": sorted(self.owned_jobs)}))
                require(result.get("isError") is not True, "owned-job cleanup failed")
            except Exception as error:
                errors.append(type(error).__name__ + ": " + str(error))
        self.client.close()
        return errors


class Lane:
    def __init__(self, sides, approval=None):
        self.sides, self.cases, self.phase = sides, [], "setup"
        self.approval = approval

    def check(self, label, function, normalize=None, expected_status=None, domain="exact"):
        entries, canonical, errors = [], [], []
        approved_case = self.phase + "/" + label in APPROVED_413_CASES and self.approval is not None
        for side in self.sides:
            side.current_case = self.phase + "/" + label
            try:
                value = function(side)
                if expected_status is not None:
                    require(value["http_status"] == expected_status,
                            f"expected HTTP {expected_status}, got {value['http_status']}")
                if approved_case:
                    require(normalize is None, "approved 413 case has an unexpected second comparison policy")
                    comparison = payload_too_large_projection(side, value, approval=self.approval)
                else:
                    comparison = normalize(side, value) if normalize else side.canonical_http(value, domain)
                entries.append(value)
                canonical.append(comparison)
                errors.append(None)
            except Exception as error:
                message = type(error).__name__ + ": " + str(error)
                message = message.replace(side.client.credential, "<credential>")
                if side.client.session:
                    message = message.replace(side.client.session, "<session>")
                entries.append(None)
                canonical.append(None)
                errors.append(message)
        passed = not any(errors) and canonical_json(canonical[0]) == canonical_json(canonical[1])
        diff = None
        if not passed and not any(errors):
            left = json.dumps(canonical[0], indent=2, ensure_ascii=False, sort_keys=True).splitlines()
            right = json.dumps(canonical[1], indent=2, ensure_ascii=False, sort_keys=True).splitlines()
            diff = "\n".join(difflib.unified_diff(left, right, fromfile="reference", tofile="candidate"))[:24000]
        self.cases.append({"phase": self.phase, "case": label, "passed": passed,
                           "validation_errors": errors, "canonical": canonical, "diff": diff,
                           "comparison_status": ("passed_with_approved_delta" if approved_case else "passed_unchanged") if passed else "failed",
                           "approved_deltas": [APPROVED_413_DELTA] if approved_case and passed else []})
        return entries

    def phase_error(self, phase, error):
        message = type(error).__name__ + ": " + str(error)
        for side in self.sides:
            message = message.replace(side.client.credential, "<credential>")
            if side.client.session:
                message = message.replace(side.client.session, "<session>")
        self.cases.append({"phase": phase, "case": "phase-precondition", "passed": False,
                           "validation_errors": [message], "comparison_status": "failed", "approved_deltas": []})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", default="http://127.0.0.1:7211/mcp")
    parser.add_argument("--candidate", default="http://127.0.0.1:7210/mcp")
    parser.add_argument("--reference-token", required=True)
    parser.add_argument("--candidate-token", required=True)
    parser.add_argument("--reference-manifest", required=True)
    parser.add_argument("--candidate-manifest", required=True)
    parser.add_argument("--approval-file", required=True,
                        help="exact recorded coordinator approval artifact for the two HTTP 413 cases")
    parser.add_argument("--output", required=True)
    parser.add_argument("--raw-output")
    args = parser.parse_args()
    run_id = uuid.uuid4().hex[:12]
    sides, evidence = [], None
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "lane": "telemetry",
              "run_id": run_id, "observed_at": iso(), "compared_headers": list(COMPARED_HEADERS)}
    try:
        approval, approval_evidence = load_approval(args.approval_file)
        report["approved_delta_manifest"] = approval_evidence
        sides.append(Side("reference", args.reference, args.reference_token, args.reference_manifest, run_id))
        sides.append(Side("candidate", args.candidate, args.candidate_token, args.candidate_manifest, run_id))
        require(sides[0].project == sides[1].project and sides[0].project_label == sides[1].project_label,
                "paired fixture project ID/label must match")
        require(sides[0].root != sides[1].root and sides[0].state != sides[1].state,
                "paired fixtures and state must be separate")
        raw_path = args.raw_output or str(Path(args.output).with_suffix(".raw.jsonl.gz"))
        evidence = RawEvidence(raw_path, [side.client for side in sides])
        for side in sides:
            side.set_evidence(evidence)
            side.bind_stores()
        lane = Lane(sides, approval=approval)
        from telemetry_contract_cases import PHASES
        for name, phase in PHASES:
            lane.phase = name
            try:
                phase(lane, run_id)
            except Exception as error:
                lane.phase_error(name, error)
                if name == "preflight-auth":
                    break
        report["cases"] = lane.cases
        report["passed"] = all(case["passed"] for case in lane.cases)
        report["comparison_counts"] = {
            name: sum(case["comparison_status"] == name for case in lane.cases)
            for name in ("passed_unchanged", "passed_with_approved_delta", "failed")
        }
        report["normalization_ledger"] = {side.label: side.normalizations for side in sides}
        report["raw_evidence"] = {"path": str(evidence.path), "records": evidence.records,
                                  "uncompressed_bytes": evidence.bytes}
    except Exception as error:
        report["passed"] = False
        report["setup_error"] = type(error).__name__ + ": " + str(error)
        for side in sides:
            report["setup_error"] = report["setup_error"].replace(side.client.credential, "<credential>")
            if side.client.session:
                report["setup_error"] = report["setup_error"].replace(side.client.session, "<session>")
    finally:
        report["cleanup_errors"] = {side.label: side.cleanup() for side in sides}
        if evidence:
            evidence.close()
        for side in sides:
            side.client.assert_no_credential(report)
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        os.chmod(output, 0o600)
    print(json.dumps({"passed": report["passed"], "cases": len(report.get("cases", [])),
                      "comparison_counts": report.get("comparison_counts", {}),
                      "output": str(output), "raw_output": report.get("raw_evidence", {}).get("path")}))
    if not report["passed"] or any(report["cleanup_errors"].values()):
        raise SystemExit(1)


if __name__ == "__main__":
    main()

