#!/usr/bin/env python3
"""Read repository Ralph context, or judge a notification in a persistent CLI session."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import uuid

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("monitor", Path(__file__).resolve().with_name("codexpro-ralph-monitor.py"))
monitor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monitor)


def project_root(project, catalog):
    if not isinstance(project, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,200}", project):
        raise monitor.MonitorError("Invalid project id.")
    matches = [p for p in monitor.read_json(catalog)["projects"] if p["id"] == project]
    if len(matches) != 1:
        raise monitor.MonitorError("Project must be registered in the local CodexPro catalog.")
    return Path(matches[0]["root"]).resolve(strict=True)


def git_read(root, arguments, limit=16000):
    with tempfile.TemporaryFile() as out:
        result = subprocess.run(["git", "--no-optional-locks", "--no-pager", "-c", "core.fsmonitor=false",
            "-c", "core.hooksPath=/dev/null", *arguments], cwd=root, stdout=out, stderr=subprocess.DEVNULL, timeout=15)
        if result.returncode:
            raise monitor.MonitorError("Read-only Git inspection failed.")
        out.seek(0); data = out.read(limit + 1)
    return {"text": data[:limit].decode("utf-8", errors="replace"), "truncated": len(data) > limit}


def repository_context(project, catalog):
    root = project_root(project, catalog)
    files = []
    # Keep inputs compact and include explicit truncation evidence. These are
    # evidence only; neither provider executes project instructions or tools.
    for name, limit in [("AGENTS.override.md", 6000), ("AGENTS.md", 6000), ("STATE.md", 8000),
                        ("HANDOFF.md", 8000), ("BACKLOG.md", 6000)]:
        path = root / name
        if not path.exists():
            continue
        if not path.resolve().is_relative_to(root) or not path.is_file():
            raise monitor.MonitorError("Context files must be regular files inside the project.")
        with path.open("rb") as stream:
            data = stream.read(limit + 1)
            # Full file hash tracks changes outside the excerpt without loading
            # an arbitrarily large file into the decision prompt.
            sha = __import__("hashlib").sha256(data)
            while chunk := stream.read(65536):
                sha.update(chunk)
        files.append({"path": name, "sha256": sha.hexdigest(), "truncated": len(data) > limit,
                      "text": data[:limit].decode("utf-8", errors="replace")})
    if not any(f["path"] in ("STATE.md", "HANDOFF.md", "BACKLOG.md") for f in files):
        raise monitor.MonitorError("No saved Ralph STATE.md, HANDOFF.md or BACKLOG.md was found.")
    return {"project_id": project, "root": str(root), "files": files,
            "git_head": git_read(root, ["rev-parse", "HEAD"])["text"].strip(),
            "git_status": git_read(root, ["status", "--short", "--untracked-files=normal"]),
            "note": "Current repository excerpts; missing or truncated acceptance evidence is not proof of completion."}


def inspect_repository(request, catalog):
    root = project_root(request["project_id"], catalog)
    operation = request.get("operation", "state")
    if operation == "state":
        return repository_context(request["project_id"], catalog)
    if operation == "history":
        return git_read(root, ["log", "-12", "--format=%h %aI %s"])
    if operation == "status":
        return git_read(root, ["status", "--short", "--untracked-files=normal"])
    name = request.get("path", "")
    path = Path(name)
    if not name or path.is_absolute() or ".." in path.parts or ".git" in path.parts or len(name) > 400:
        raise monitor.MonitorError("Inspection requires a relative repository path without traversal.")
    resolved = (root / path).resolve(strict=True)
    if not resolved.is_relative_to(root):
        raise monitor.MonitorError("Inspection cannot leave the registered project.")
    if operation == "files":
        if not resolved.is_dir():
            raise monitor.MonitorError("files requires a directory.")
        names = sorted(p.name + ("/" if p.is_dir() else "") for p in resolved.iterdir() if p.name != ".git")
        return {"path": name, "entries": names[:100], "truncated": len(names) > 100}
    if operation == "diff":
        return git_read(root, ["diff", "--no-ext-diff", "--no-textconv", "HEAD", "--", name])
    if operation == "read":
        start = request.get("start_line", 1)
        if type(start) is not int or not 1 <= start <= 100000:
            raise monitor.MonitorError("start_line must be between 1 and 100000.")
        if not resolved.is_file():
            raise monitor.MonitorError("read requires a regular file.")
        with resolved.open("rb") as stream:
            # File-size bound also protects newline-free generated/binary files.
            data = stream.read(2_000_001)
        if len(data) > 2_000_000 or b'\0' in data:
            raise monitor.MonitorError("Inspect a text file of at most 2 MB.")
        lines = data.decode("utf-8", errors="replace").splitlines()
        excerpt = "\n".join(lines[start - 1:start + 199])
        return {"path": name, "start_line": start, "total_lines": len(lines), "text": monitor.clip(excerpt, 16000),
                "truncated": start + 199 < len(lines) or len(excerpt) > 16000}
    raise monitor.MonitorError("Unsupported read-only inspection operation.")


def decision_schema(packet):
    properties = {"schema_version": {"type": "integer", "enum": [1]},
                  "fingerprint": {"type": "string", "enum": [packet["fingerprint"]]},
                  "action": {"type": "string", "enum": packet["allowed_actions"]},
                  "project_status": {"type": "string", "enum": ["active", "complete", "blocked_human", "unknown"]},
                  "reason": {"type": "string", "minLength": 1, "maxLength": 1000},
                  "context_request": {"type": "string", "maxLength": 1000},
                  "next_step": {"type": "string", "maxLength": monitor.MAX_NEXT_STEP_CHARS}}
    return {"type": "object", "properties": properties, "required": list(properties), "additionalProperties": False}


def decide(packet, args):
    project = packet["target"]["project_id"]
    if packet.get("schema") != monitor.SCHEMA or not re.fullmatch(r"[A-Za-z0-9_-]{1,200}", project):
        raise monitor.MonitorError("Invalid decision packet.")
    directory = args.state_dir / project / args.provider
    with monitor.target_lock(directory):
        session_path = directory / "session.json"
        session = monitor.read_json(session_path, {})
        schema = decision_schema(packet)
        schema_path, result_path = directory / "schema.json", directory / "result.json"
        monitor.save_json(schema_path, schema)
        result_path.unlink(missing_ok=True)
        prompt = {"role": "You are the persistent project orchestrator. Own planning, continuity, reconciliation and acceptance; inspect repository evidence when needed and own routine recovery and direct disposable ChatGPT workers with a short next action referring to saved Ralph state. Do not perform implementation work yourself.",
                  "policy": monitor.POLICY, "notification": packet,
                  "session_note": "This is the newest authoritative snapshot. Older messages are history, not current state. If excerpts are insufficient, request context; never guess completion."}
        server = None
        if args.context_command_file:
            server = {"command": sys.executable, "args": [str(Path(__file__).resolve().with_name("codexpro-ralph-inspect.py")),
                "--project", project, "--command-file", str(args.context_command_file.resolve())]}
        if args.provider == "codex":
            command = [args.executable or "codex", "exec"]
            if session.get("session_id"):
                command += ["resume", session["session_id"]]
            command += ["--json", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
                        "-c", 'sandbox_mode="read-only"', "-c", 'approval_policy="never"',
                        "-c", "features.shell_tool=false",
                        "--output-schema", str(schema_path), "--output-last-message", str(result_path)]
            if args.model:
                command += ["--model", args.model]
            if server:
                command += ["-c", "mcp_servers.ralph_repo.command=" + json.dumps(server["command"]),
                            "-c", "mcp_servers.ralph_repo.args=" + json.dumps(server["args"])]
            command += ["-"]
            events = monitor.run_json(command, prompt, timeout=args.timeout, cwd=directory, json_lines=True)
            ids = [e["thread_id"] for e in events if e.get("type") == "thread.started" and e.get("thread_id")]
            if ids:
                if session.get("session_id") and session["session_id"] != ids[-1]:
                    raise monitor.MonitorError("Codex resumed an unexpected session.")
                session["session_id"] = ids[-1]
            if not session.get("session_id"):
                raise monitor.MonitorError("Codex returned no persistent thread ID.")
            # Persist the conversation even if its last decision is invalid.
            monitor.save_json(session_path, session)
            decision = monitor.read_json(result_path)
        else:
            session_id = session.get("session_id") or str(uuid.uuid4())
            command = [args.executable or "claude", "-p", "--output-format", "json", "--json-schema", json.dumps(schema),
                       "--tools", "", "--strict-mcp-config", "--mcp-config", json.dumps({"mcpServers": {"ralph_repo": server} if server else {}}),
                       "--setting-sources", "", "--disable-slash-commands", "--no-chrome", "--permission-mode", "dontAsk",
                       "--resume" if session.get("session_id") else "--session-id", session_id]
            if server:
                command += ["--allowedTools", "mcp__ralph_repo__project_inspect"]
            if args.model:
                command += ["--model", args.model]
            result = monitor.run_json(command, prompt, timeout=args.timeout, cwd=directory)
            if result.get("is_error") or result.get("session_id") != session_id:
                raise monitor.MonitorError("Claude did not return a successful result in the expected session.")
            session["session_id"] = session_id
            monitor.save_json(session_path, session)
            decision = result.get("structured_output")
            if decision is None:
                decision = json.loads(result["result"])
        decision = monitor.validate_decision(decision, packet)
        session.update({"project_id": project, "provider": args.provider, "notifications": session.get("notifications", 0) + 1,
                        "last_fingerprint": packet["fingerprint"]})
        monitor.save_json(session_path, session)
        monitor.save_json(directory / "decision.json", decision)
        return decision


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", action="store_true", help="Read bounded repository state without calling an LLM")
    parser.add_argument("--inspect", action="store_true", help="Perform an allowlisted read-only repository inspection")
    parser.add_argument("--context-command-file", type=Path, help="JSON argv for the repository inspection helper, possibly over SSH")
    parser.add_argument("--catalog", type=Path, default=Path.home() / ".config/codexpro/projects.json")
    parser.add_argument("--state-dir", type=Path, default=Path.home() / ".local/state/codexpro-ralph-decider")
    parser.add_argument("--provider", choices=["codex", "claude"], default="codex")
    parser.add_argument("--executable", help="Provider CLI executable")
    parser.add_argument("--model", help="Optional provider model; otherwise use its default")
    parser.add_argument("--timeout", type=int, default=150)
    args = parser.parse_args()
    os.umask(0o077)
    raw = sys.stdin.buffer.read(100001)
    if len(raw) > 100000:
        raise monitor.MonitorError("Input packet exceeds 100 KB.")
    packet = json.loads(raw)
    result = inspect_repository(packet, args.catalog) if args.inspect else repository_context(packet["project_id"], args.catalog) if args.context else decide(packet, args)
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (monitor.MonitorError, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({"error": str(error)}), file=sys.stderr)
        sys.exit(1)
