#!/usr/bin/env python3
"""Small stdio MCP bridge to one project's allowlisted read-only repository inspector."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("monitor", Path(__file__).resolve().with_name("codexpro-ralph-monitor.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

TOOL = {"name": "project_inspect", "description": "Read the authoritative project repository on its host. Inspect state, Git status/history, directory entries, file excerpts, or a file's diff. Cannot execute commands or change files.",
        "inputSchema": {"type": "object", "properties": {
            "operation": {"type": "string", "enum": ["state", "status", "history", "files", "read", "diff"]},
            "path": {"type": "string", "description": "Relative repository path. Use . for root directory entries."},
            "start_line": {"type": "integer", "minimum": 1, "maximum": 100000}}, "required": ["operation"], "additionalProperties": False},
        "annotations": {"readOnlyHint": True, "destructiveHint": False, "idempotentHint": True, "openWorldHint": False}}


def handle(request, project, command):
    method = request.get("method")
    if method == "initialize":
        return {"protocolVersion": request["params"]["protocolVersion"], "capabilities": {"tools": {}},
                "serverInfo": {"name": "codexpro-ralph-repo", "version": "1.0.0"}}
    if method == "ping":
        return {}
    if method == "tools/list":
        return {"tools": [TOOL]}
    if method == "tools/call":
        params = request.get("params", {})
        args = params.get("arguments", {})
        if params.get("name") != TOOL["name"] or set(args) - {"operation", "path", "start_line"}:
            raise m.MonitorError("Unsupported inspection request.")
        result = m.run_json(command, {**args, "project_id": project}, timeout=30, max_bytes=100000)
        return {"content": [{"type": "text", "text": json.dumps(result, ensure_ascii=False)}]}
    raise m.MonitorError("Unsupported MCP method.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", required=True)
    parser.add_argument("--command-file", required=True, type=Path)
    args = parser.parse_args()
    command = m.read_json(args.command_file)
    for line in sys.stdin:
        if len(line) > 16000:
            break
        request = json.loads(line)
        if "id" not in request:
            continue
        try:
            reply = {"jsonrpc": "2.0", "id": request["id"], "result": handle(request, args.project, command)}
        except (m.MonitorError, ValueError, TypeError, KeyError, OSError):
            reply = {"jsonrpc": "2.0", "id": request["id"], "error": {"code": -32602, "message": "Read-only repository inspection failed; check the operation, path and host connection."}}
        print(json.dumps(reply, ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
