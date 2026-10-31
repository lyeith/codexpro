#!/usr/bin/env python3
"""Freeze conditional workspace MCP descriptors from two private TS profiles.

Runs the immutable release on port 7212, sequentially, with a fresh token,
fixture repository and all state directories per profile. Only initialize,
initialized notification, tools/list and session DELETE are sent; tools/call is
never used. The helper terminates only each exact Popen process it created.

The baseline forbids full Bash with worktree=mcp (src/config.ts571-572).
The mcp/safe profile exists solely to capture workspace descriptors. Its Bash
descriptors and initialization instructions MUST NOT configure the Go runtime,
which remains full Bash and workspace write as requested and approved.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import time
import urllib.error

from contract_suite import Client, SOURCE_COMMIT, raw_result


WORKSPACE_NAMES = {"create_workspace", "list_workspaces", "open_current_workspace",
                   "open_workspace", "release_workspace", "remove_workspace"}
PROFILES = [
    {"name": "mcp-standard", "worktree_mode": "mcp", "bash_mode": "safe", "tool_mode": "standard",
     "present": {"create_workspace", "open_workspace", "release_workspace", "remove_workspace"},
     "absent": {"list_workspaces", "open_current_workspace"}},
    {"name": "direct-single-full", "worktree_mode": "off", "bash_mode": "full", "tool_mode": "full",
     "present": {"list_workspaces", "open_current_workspace", "open_workspace"},
     "absent": {"create_workspace", "release_workspace", "remove_workspace"}},
]


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def durable_json(path, value):
    text = json.dumps(value, indent=2, ensure_ascii=False) + "\n"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(text)
        handle.flush()
        os.fsync(handle.fileno())


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(("127.0.0.1", 7212))


def command(args, *, cwd=None, env=None):
    result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=8, check=False)
    check(result.returncode == 0, "Private fixture command failed: " + args[0])
    return result.stdout.decode().strip()


def prepare(state, profile):
    state.mkdir(mode=0o700, parents=False, exist_ok=False)
    token_file = state / "http-token"
    fd = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(secrets.token_urlsafe(48) + "\n")
    for directory in ("home", "worktrees", "jobs", "audit", "work", "empty-codex", "created-projects", "fixture"):
        (state / directory).mkdir(mode=0o700)
    (state / "limits.env").touch(mode=0o600)
    fixture = state / "fixture"
    git_env = os.environ.copy()
    git_env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_TERMINAL_PROMPT": "0", "GIT_AUTHOR_DATE": "2025-01-01T00:00:00+00:00",
                    "GIT_COMMITTER_DATE": "2025-01-01T00:00:00+00:00"})
    command(["git", "-c", "init.templateDir=", "init", "-q", "-b", "main", str(fixture)], env=git_env)
    (fixture / "README.md").write_text("# Conditional schema fixture\n\nNo tool commands are executed during capture.\n")
    command(["git", "-C", str(fixture), "add", "README.md"], env=git_env)
    command(["git", "-C", str(fixture), "-c", "core.hooksPath=/dev/null", "-c", "user.name=Contract Fixture",
             "-c", "user.email=fixture@example.invalid", "commit", "-qm", "test: conditional schema fixture"], env=git_env)
    catalog = {"version": 1, "defaultProject": "contract", "projects": [{
        "id": "contract", "label": "Private conditional schema fixture", "root": str(fixture),
        "baseRef": "main", "maxWorktrees": 4}],
        "creationRoots": [{"id": "fixtures", "label": "Private fixture projects", "root": str(state / "created-projects")}]}
    durable_json(state / "projects.json", catalog)
    # Preserve the user's actual HOME; redirect each CodexPro state selector,
    # never repurpose a shell/system home variable to disguise isolation.
    env = {key: value for key, value in os.environ.items()
           if key in {"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE",
                      "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "TMPDIR"}}
    env.update({
        "CODEXPRO_HOME": str(state / "home"), "CODEXPRO_ENV_FILE": str(state / "limits.env"),
        "CODEXPRO_PROJECTS_FILE": str(state / "projects.json"), "CODEXPRO_ROOT": str(fixture),
        "CODEXPRO_WORKTREE_ROOT": str(state / "worktrees"), "CODEXPRO_WORKTREE_BASE": "main",
        "CODEXPRO_JOBS_DIR": str(state / "jobs"), "CODEXPRO_AUDIT_LOG": str(state / "audit" / "tool-calls.jsonl"),
        "CODEXPRO_WORK_DIR": str(state / "work"), "CODEXPRO_CODEX_DIR": str(state / "empty-codex"),
        "CODEXPRO_HOST": "127.0.0.1", "CODEXPRO_PORT": "7212", "CODEXPRO_AUTH_MODE": "static-token",
        "CODEXPRO_REQUIRE_HTTP_TOKEN": "1", "CODEXPRO_BASH_MODE": profile["bash_mode"],
        "CODEXPRO_WRITE_MODE": "workspace", "CODEXPRO_TOOL_MODE": profile["tool_mode"],
        "CODEXPRO_WORKTREE_MODE": profile["worktree_mode"], "CODEXPRO_MAX_WORKTREES": "4",
        "CODEXPRO_WORK_MODE": "on", "CODEXPRO_WORK_MANAGEMENT": "true", "CODEXPRO_HANDOFF_MODE": "off",
        "CODEXPRO_CODEX_SESSIONS": "off", "CODEXPRO_INHERIT_ENV": "0", "CODEXPRO_ANALYSIS": "true",
        "CODEXPRO_AUDIT_MODE": "metadata", "CODEXPRO_DEBUG_ACTIVITY": "metadata",
        "CODEXPRO_MAX_HTTP_SESSIONS": "4", "CODEXPRO_HTTP_SESSION_TTL_MS": "60000",
    })
    env["CODEXPRO_HTTP_TOKEN"] = token_file.read_text().strip()
    return fixture, token_file, env


def stop_owned(child):
    if child.poll() is not None:
        return child.returncode
    # start_new_session made this child's PID its private process-group ID.
    # No service manager, shared name, saved PID file, or listening port is used
    # to select a process for termination.
    child.send_signal(signal.SIGTERM)
    try:
        return child.wait(timeout=5)
    except subprocess.TimeoutExpired:
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGKILL)
        return child.wait(timeout=3)


def capture(release, parent, profile):
    free_port()
    state = parent / profile["name"]
    fixture, token_file, env = prepare(state, profile)
    entry = release / "dist" / "http.js"
    client = None
    child = None
    capture = {"profile": profile["name"], "source_commit": SOURCE_COMMIT,
               "configuration": {"worktree_mode": profile["worktree_mode"], "bash_mode": profile["bash_mode"],
                                 "tool_mode": profile["tool_mode"], "write_mode": "workspace", "project_count": 1},
               "state": str(state), "port": 7212, "release": str(release),
               "entry_sha256": hashlib.sha256(entry.read_bytes()).hexdigest(), "tools_called": [],
               "capture_started_at": dt.datetime.now(dt.timezone.utc).isoformat()}
    try:
        with (state / "server.log").open("xb") as log:
            child = subprocess.Popen(["node", str(entry)], cwd=fixture, env=env,
                                     stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            capture["owned_child_pid"] = child.pid
            deadline = time.monotonic() + 25
            last_error = None
            while time.monotonic() < deadline:
                check(child.poll() is None, "Private TS reference exited before capture; inspect its private server.log")
                try:
                    client = Client("http://127.0.0.1:7212/mcp", token_file, timeout=4)
                    break
                except (OSError, urllib.error.URLError, TimeoutError) as error:
                    last_error = type(error).__name__
                    time.sleep(0.2)
            check(client is not None, "Private TS reference did not become ready: " + str(last_error))
            capture["initialize"] = client.initialization
            pages, tools, cursor = [], [], None
            for _ in range(8):
                params = {} if cursor is None else {"cursor": cursor}
                response = client.rpc("tools/list", params)
                check(response["http_status"] == 200 and "result" in response["body"], "tools/list failed")
                pages.append(response)
                data = raw_result(response)
                check(isinstance(data.get("tools"), list), "tools/list lacks descriptor array")
                tools.extend(data["tools"])
                cursor = data.get("nextCursor")
                if cursor is None:
                    break
            else:
                raise RuntimeError("tools/list pagination exceeded bounded eight pages")
            names = [tool["name"] for tool in tools]
            check(len(names) == len(set(names)), "tools/list contains duplicate tool names")
            check(profile["present"].issubset(names), "conditional workspace descriptors missing from reference")
            check(not profile["absent"].intersection(names), "reference conditional availability differs from source")
            capture["tools_list_pages"] = pages
            capture["tool_names"] = names
            capture["workspace_descriptors"] = {tool["name"]: tool for tool in tools if tool["name"] in WORKSPACE_NAMES}
            capture["retired_full_inventory_present"] = "codexpro_inventory" in names
            client.assert_no_credential(capture)
    finally:
        if client is not None:
            client.close()
        if child is not None:
            capture["owned_child_exit_code"] = stop_owned(child)
        capture["capture_finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        # The report never includes the process environment or token contents.
        durable_json(state / "capture.json", capture)
    free_port()
    return capture


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", required=True)
    parser.add_argument("--state-parent", required=True,
                        help="New, nonexistent directory under ~/.codexpro-go-proof/ or /tmp/sp6-*")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    release = Path(args.release).expanduser().resolve(strict=True)
    check((release / "dist" / "http.js").is_file(), "immutable release lacks dist/http.js")
    package = json.loads((release / "package.json").read_text())
    check(package.get("version") == "0.31.0" and "44bb76b" in release.name,
          "conditional capture must use the specified immutable 0.31.0/44bb76b release")
    parent = Path(args.state_parent).expanduser().resolve()
    allowed = (Path.home() / ".codexpro-go-proof").resolve()
    check((allowed in parent.parents) or (str(parent).startswith("/tmp/sp6-") and parent != Path("/tmp")),
          "conditional capture state must be wholly private")
    check(not parent.exists(), "conditional capture refuses to reuse any existing state")
    parent.mkdir(mode=0o700, parents=True, exist_ok=False)
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "source_guard": {
        "path": "src/config.ts", "lines": [571, 572],
        "rule": "TypeScript rejects worktree=mcp with full Bash; only descriptor inventory uses safe mode.",
        "go_runtime": "full Bash and workspace write; do not import safe-mode Bash descriptors or initialization instructions"},
        "availability_source": {"path": "src/tools/registry.ts", "lines": [165, 189]},
        "approved_retirement": "codexpro_inventory and codexpro supertool remain reference-only and are not restored to Go",
        "captures": []}
    for profile in PROFILES:
        report["captures"].append(capture(release, parent, profile))
        print(json.dumps({"profile": profile["name"], "tool_count": len(report["captures"][-1]["tool_names"]),
                          "workspace_descriptors": sorted(report["captures"][-1]["workspace_descriptors"])}), flush=True)
    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    durable_json(output, report)
    encoded = output.read_bytes()
    print(json.dumps({"output": str(output), "sha256": hashlib.sha256(encoded).hexdigest(),
                      "profiles": len(report["captures"]), "tools_called": 0, "port": 7212}))


if __name__ == "__main__":
    main()
