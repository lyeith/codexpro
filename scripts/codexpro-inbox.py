#!/usr/bin/env python3
"""Portable inbox adapter: one JSON request on stdin, one JSON response on stdout."""
import argparse
import importlib.util
import json
from pathlib import Path
from urllib.parse import quote

spec = importlib.util.spec_from_file_location("monitor", Path(__file__).with_name("codexpro-ralph-monitor.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def exchange(request, config):
    source = m.Sources({"activity_config": str(config)})
    base, auth = source.base, source.auth
    project = request.get("project_id")
    if not isinstance(project, str) or not project or len(project) > 160:
        raise m.MonitorError("Inbox requests require a project_id.")
    operation = request.get("operation")
    body = None
    if operation == "list":
        route = "/inbox/v1/items?project_id=" + quote(project, safe="") + "&limit=100&consumer=" + quote("ralph-" + project, safe="")
    elif operation == "publish":
        body = request["question"]
        if body.get("project_id") != project:
            raise m.MonitorError("Question project mismatch.")
        route = "/inbox/v1/items"
    elif operation in ("answer", "deliver"):
        key = request.get("id")
        if not isinstance(key, str) or not key:
            raise m.MonitorError("Question id is required.")
        route = "/inbox/v1/items/" + quote(project, safe="") + "/" + quote(key, safe="") + ("/answers" if operation == "answer" else "/deliveries")
        body = request["answer"] if operation == "answer" else request["delivery"]
    else:
        raise m.MonitorError("Unknown inbox operation.")
    command = ["curl", "--disable", "--config", str(auth), "--silent", "--show-error", "--fail", "--connect-timeout", "8", "--max-time", "30"]
    if body is not None:
        command += ["--header", "Content-Type: application/json", "--data-binary", "@-"]
    result = m.run_json(command + [base + route], body, max_bytes=2_000_000)
    if result.get("schema_version") != 1:
        raise m.MonitorError("Unknown inbox response schema.")
    if operation == "list" and result.get("next_offset") is not None:
        raise m.MonitorError("Project inbox exceeds 100 pending/undelivered items; add pagination to this adapter before continuing.")
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path.home()/".config/codexpro/activity-client.json")
    args = parser.parse_args()
    print(json.dumps(exchange(json.load(__import__('sys').stdin), args.config), ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (m.MonitorError, KeyError, ValueError) as error:
        raise SystemExit(str(error))
