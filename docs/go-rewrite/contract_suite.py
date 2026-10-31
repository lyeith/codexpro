#!/usr/bin/env python3
"""One differential MCP suite for private TypeScript and Go, with a read-only live lane.

Credentials never enter argv, evidence, or logs. Every mutating test refuses the
live endpoint and requires an explicitly private fixture root.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

LIVE_READ_ONLY = {"list_projects", "server_config", "activity_list", "activity_get",
                  "activity_status", "activity_export", "load_skill", "tree",
                  "search", "ast_grep", "read", "view_image", "inspect_workspace",
                  "work_status"}
RETIRED = {"work_manage", "work_update"}
SOURCE_COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"

class Client:
    def __init__(self, endpoint, token_file, timeout=30):
        self.endpoint = endpoint
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.hostname not in ("127.0.0.1", "localhost", "::1"):
            raise ValueError("Contract endpoints must be local")
        self.live = parsed.port == 7200
        self.credential = Path(token_file).read_text().strip()
        self.timeout = timeout
        self.sequence = 0
        self.session = None
        self.initialization = self.rpc("initialize", {
            "protocolVersion": "2024-11-05", "capabilities": {},
            "clientInfo": {"name": "sp6-contract-suite", "version": "1"}})
        if "error" in self.initialization["body"]:
            raise RuntimeError("initialize failed")
        self.rpc("notifications/initialized", {}, notification=True)

    def rpc(self, method, params, notification=False):
        self.sequence += 1
        request_id = self.sequence
        payload = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            payload["id"] = request_id
        headers = {"Authorization": "Bearer " + self.credential,
                   "Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": "2024-11-05"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        req = urllib.request.Request(self.endpoint,
            data=json.dumps(payload, ensure_ascii=False).encode(),
            headers=headers, method="POST")
        start = time.monotonic()
        try:
            response = urllib.request.urlopen(req, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            self.session = response.headers.get("Mcp-Session-Id") or self.session
            if response.status == 202 or notification:
                response.read()
                body = None
            elif "text/event-stream" in response.headers.get("Content-Type", ""):
                data, body = [], None
                for raw in response:
                    line = raw.decode().rstrip("\r\n")
                    if line.startswith("data:"):
                        data.append(line[5:].lstrip())
                    elif not line and data:
                        message = json.loads("\n".join(data))
                        data = []
                        if message.get("id") == request_id:
                            body = message
                            break
                if body is None:
                    raise RuntimeError("SSE ended without request response")
            else:
                raw = response.read().decode()
                try:
                    body = json.loads(raw)
                except json.JSONDecodeError:
                    body = raw
            result = {"http_status": response.status,
                      "content_type": response.headers.get("Content-Type", "").split(";")[0],
                      "body": body, "duration_ms": (time.monotonic()-start)*1000}
        self.assert_no_credential(result)
        return result

    def assert_no_credential(self, result):
        encoded = json.dumps(result, ensure_ascii=False)
        if self.credential in encoded or (self.session and self.session in encoded):
            raise RuntimeError("Credential/session appeared in response evidence")

    def call(self, name, args):
        if self.live and name not in LIVE_READ_ONLY:
            if name != "show_changes" or args.get("mark_reviewed") is not False:
                raise ValueError("Live mutation refused by suite allowlist: " + name)
        return self.rpc("tools/call", {"name": name, "arguments": args})

    def close(self):
        if not self.session:
            return
        req = urllib.request.Request(self.endpoint, headers={
            "Authorization": "Bearer " + self.credential,
            "Mcp-Session-Id": self.session,
            "MCP-Protocol-Version": "2024-11-05"}, method="DELETE")
        try:
            with urllib.request.urlopen(req, timeout=3) as response:
                response.read()
        except (OSError, urllib.error.HTTPError):
            pass

def raw_result(response):
    return response["body"].get("result", {})

def validation_cases(workspace):
    return [
        ("read_missing", "read", {}),
        ("read_missing_path", "read", {"workspace_id": workspace}),
        ("read_wrong_type", "read", {"workspace_id": workspace, "path": 42}),
        ("read_null_workspace", "read", {"workspace_id": None, "path": "README.md"}),
        ("read_bounds", "read", {"workspace_id": workspace, "path": "README.md", "start_line": 0, "max_bytes": -1}),
        ("search_bad_enum", "search", {"workspace_id": workspace, "query": "fixture", "intent": "unknown"}),
        ("edit_invalid_union", "edit", {"workspace_id": workspace, "path": "README.md", "edit_tag": "0000", "edits": [{"op": "nope"}]}),
        ("batch_unknown_member", "batch", {"workspace_id": workspace, "operations": [{"tool": "read", "args": {"path": "README.md"}, "unexpected": True}]}),
        ("unknown_tool", "not_a_real_tool", {}),
        ("read_missing_file", "read", {"workspace_id": workspace, "path": "does-not-exist.txt"}),
        ("read_blocked", "read", {"workspace_id": workspace, "path": ".env"}),
        ("tree_wrong_path", "tree", {"workspace_id": workspace, "path": "README.md"}),
        ("open_unknown", "open_workspace", {"project_id": "not-configured", "include_tree": False}),
        ("read_extra_key", "read", {"workspace_id": workspace, "path": "README.md", "unused": True})
    ]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", default="http://127.0.0.1:7211/mcp")
    parser.add_argument("--reference-token", required=True)
    parser.add_argument("--candidate")
    parser.add_argument("--candidate-token")
    parser.add_argument("--project", default="contract")
    parser.add_argument("--output", required=True)
    parser.add_argument("--lane", choices=["validation"], default="validation")
    args = parser.parse_args()
    ref = Client(args.reference, args.reference_token)
    if ref.live:
        raise ValueError("Validation lane contains negative mutation-shaped requests; private reference required")
    try:
        projects = raw_result(ref.call("list_projects", {}))["structuredContent"]["projects"]
        workspace = next(p["workspace_id"] for p in projects if p["id"] == args.project)
        records = []
        for label, name, inputs in validation_cases(workspace):
            records.append({"case": label, "tool": name, "arguments": inputs,
                            "reference": ref.call(name, inputs)})
        report = {"schema_version": 1, "source_commit": SOURCE_COMMIT,
                  "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  "lane": args.lane, "reference": args.reference, "cases": records}
        ref.assert_no_credential(report)
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
        print(json.dumps({"lane": args.lane, "cases": len(records),
                          "output": str(output), "candidate_compared": False}))
    finally:
        ref.close()

if __name__ == "__main__":
    main()
