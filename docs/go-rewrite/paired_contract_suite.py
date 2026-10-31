#!/usr/bin/env python3
"""Strict private differential lane for the declared file/search/Git workflows.

The fixture project roots are temporarily emptied after their listening process,
launch manifest, and MCP workspace binding are verified. Original entries are
moved to a private backup and restored in finally. Run sequentially with every
other mutation lane. Credentials and HTTP session IDs never enter evidence.
"""
from __future__ import annotations

import argparse
import base64
import copy
import datetime as dt
import difflib
import fcntl
import gzip
import hashlib
import json
import math
import os
from pathlib import Path, PurePosixPath
import pwd
import re
import shutil
import signal
import stat
import subprocess
import sys
import time
import urllib.parse
import uuid

from contract_suite import Client, SOURCE_COMMIT, raw_result
from contract_cases import CASES

MAX_RAW_BYTES = 64 * 1024 * 1024
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_FIXTURE_BYTES = 16 * 1024 * 1024
RETIRED = frozenset({
    "work_manage", "work_update", "codexpro", "codexpro_inventory",
    "read_handoff", "wait_for_handoff", "codex_context", "export_pro_context",
    "handoff_to_agent", "codex_sessions", "read_codex_session",
})
APPROVAL_COMMIT = "acf6b9b"
MISSING = object()


class ContractFailure(AssertionError):
    pass


def require(condition, message):
    if not condition:
        raise ContractFailure(message)


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":"), allow_nan=False)


def source_json(value):
    """JSON.stringify ordering for the explicit ASCII-keyed source payloads."""
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def digest(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def iso_now():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def safe_error(error, clients=()):
    message = type(error).__name__ + ": " + str(error)
    for client in clients:
        if client.credential in message or (client.session and client.session in message):
            return type(error).__name__ + ": detail withheld because it contained a credential"
    return message[:2000]


def forbidden_roots():
    homes = {Path.home(), Path(pwd.getpwuid(os.getuid()).pw_dir)}
    roots = set()
    for home in homes:
        roots.update((home, home / ".codexpro", home / ".config" / "codexpro",
                      home / "Projects", home / "apps" / "codexpro"))
    return roots


def private_path(value, *, missing=False):
    path = Path(value).expanduser().resolve(strict=not missing)
    require(path != Path("/"), "filesystem root cannot be a private fixture")
    homes = {Path.home().resolve(), Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()}
    for declared in forbidden_roots():
        root = declared.resolve(strict=False)
        if root in homes:
            require(path != root, "account home cannot be a private fixture")
        else:
            require(path != root and not path.is_relative_to(root), "live or repository path refused")
    return path


def artifact_path(value):
    path = Path(value).expanduser().resolve(strict=False)
    for home in {Path.home(), Path(pwd.getpwuid(os.getuid()).pw_dir)}:
        for declared in (home / ".codexpro", home / ".config" / "codexpro", home / "apps" / "codexpro"):
            root = declared.resolve(strict=False)
            require(path != root and not path.is_relative_to(root), "evidence cannot overwrite live state")
    return path


def process_start_ticks(pid):
    tail = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
    return int(tail[19])


def validate_launch(manifest, endpoint, token_file, fixture_root, expected_port):
    parsed = urllib.parse.urlsplit(endpoint)
    require(parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "localhost", "::1"}
            and parsed.port == expected_port and parsed.path == "/mcp"
            and not parsed.query and not parsed.fragment
            and parsed.username is None and parsed.password is None,
            "only the declared private loopback /mcp endpoint is allowed")
    require(type(manifest.get("port")) is int and manifest["port"] == expected_port,
            "manifest port does not match the private endpoint")
    pid = manifest.get("pid")
    require(type(pid) is int and pid > 1, "manifest must identify the listening PID")
    require(Path(f"/proc/{pid}").stat().st_uid == os.getuid(), "server is not owned by this user")
    require(process_start_ticks(pid) == manifest.get("process_start_ticks"),
            "server PID was reused or manifest is stale")
    sockets = set()
    for path in Path(f"/proc/{pid}/fd").iterdir():
        try:
            value = os.readlink(path)
        except FileNotFoundError:
            continue
        match = re.fullmatch(r"socket:\[(\d+)\]", value)
        if match:
            sockets.add(match[1])
    listening = False
    for family in ("tcp", "tcp6"):
        table = Path(f"/proc/{pid}/net/{family}")
        if not table.exists():
            continue
        for line in table.read_text().splitlines()[1:]:
            fields = line.split()
            if len(fields) > 9 and fields[3] == "0A":
                listening |= (int(fields[1].rsplit(":", 1)[1], 16) == expected_port
                              and fields[9] in sockets)
    require(listening, "declared PID does not own the private listening socket")
    root = private_path(fixture_root)
    state = private_path(manifest["state_root"])
    token = private_path(token_file)
    require(root == private_path(manifest["fixture_root"]), "fixture argument differs from manifest")
    require(token == private_path(manifest["token_file"]), "token argument differs from manifest")
    require(root.is_dir() and state.is_dir() and token.is_file(), "private launch paths are incomplete")
    require(root != state and not state.is_relative_to(root), "fixture would contain private server state")
    require(token.is_relative_to(state) and not token.is_relative_to(root), "token is not in separate private state")
    require(root.stat().st_dev == state.stat().st_dev, "fixture backup requires the same private filesystem")
    require(isinstance(manifest.get("project_id"), str) and manifest["project_id"],
            "manifest must bind a private project ID")
    if expected_port == 7211:
        require(manifest.get("source_commit") == SOURCE_COMMIT,
                "reference manifest must identify the exact captured source commit")
    return root, state, token


def relative_path(value):
    require(isinstance(value, str) and value and "\x00" not in value and "\\" not in value,
            "fixture path must be a nonempty POSIX relative path")
    path = PurePosixPath(value)
    require(not path.is_absolute() and ".." not in path.parts and path.parts[0] != ".git"
            and ":" not in path.parts[0] and str(path) == value,
            "fixture path escapes or changes repository metadata")
    return path


def get_pointer(value, pointer, default=MISSING):
    require(pointer == "" or pointer.startswith("/"), "invalid JSON pointer")
    if pointer == "":
        return value
    current = value
    for raw in pointer[1:].split("/"):
        key = raw.replace("~1", "/").replace("~0", "~")
        if isinstance(current, dict) and key in current:
            current = current[key]
        elif isinstance(current, list) and re.fullmatch(r"0|[1-9][0-9]*", key) and int(key) < len(current):
            current = current[int(key)]
        else:
            if default is not MISSING:
                return default
            raise ContractFailure("missing binding at " + pointer)
    return current


def replace_pointer(value, pointer, replacement):
    parts = pointer.rsplit("/", 1)
    parent = get_pointer(value, parts[0])
    key = parts[1].replace("~1", "/").replace("~0", "~")
    if isinstance(parent, list):
        parent[int(key)] = replacement
    else:
        parent[key] = replacement


def resolve_refs(value, bindings, root):
    if isinstance(value, dict):
        if set(value) == {"$ref"}:
            key = value["$ref"]
            require(key in bindings, "unavailable causal binding: " + str(key))
            return copy.deepcopy(bindings[key])
        return {key: resolve_refs(item, bindings, root) for key, item in value.items()}
    if isinstance(value, list):
        return [resolve_refs(item, bindings, root) for item in value]
    if isinstance(value, str) and (value == "$WORKSPACE" or value.startswith("$WORKSPACE/")):
        return str(root) + value[len("$WORKSPACE"):]
    return value


def validate_cases(cases):
    seen = set()
    for case in cases:
        require(set(case) <= {"id", "fixtures", "fixtures_base64", "fixture_mtime_ns", "steps", "setup"},
                "unsupported workflow setup key")
        require(isinstance(case.get("id"), str) and re.fullmatch(r"[A-Z0-9-]+", case["id"])
                and case["id"] not in seen, "workflow IDs must be unique")
        seen.add(case["id"])
        size = 0
        for name, value in case.get("fixtures", {}).items():
            relative_path(name)
            require(isinstance(value, str), "text fixture must be a string")
            size += len(value.encode("utf-8"))
        for name, value in case.get("fixtures_base64", {}).items():
            relative_path(name)
            require(name not in case.get("fixtures", {}), "binary/text fixture path collision")
            size += len(base64.b64decode(value, validate=True))
        require(size <= MAX_FIXTURE_BYTES, "workflow fixture exceeds bounded size")
        require(type(case.get("fixture_mtime_ns")) is int and case["fixture_mtime_ns"] >= 0,
                "workflow needs a deterministic fixture timestamp")
        require(set(case.get("setup", {})) <= {"git"}, "unsupported setup extension")
        if "git" in case.get("setup", {}):
            setup = case["setup"]["git"]
            require(set(setup) == {"branch", "user_name", "user_email", "date", "tracked"},
                    "unsupported Git setup")
            for name in setup["tracked"]:
                relative_path(name)
                require(name in case.get("fixtures", {}), "tracked path is not a seeded text fixture")
        step_ids = set()
        require(isinstance(case.get("steps"), list) and case["steps"], "workflow has no steps")
        for step in case["steps"]:
            require(set(step) <= {"id", "tool", "args", "bind", "dynamic", "expect_error", "session"},
                    "unsupported step extension")
            require(step["id"] not in step_ids, "duplicate step ID")
            step_ids.add(step["id"])
            require(step["tool"] not in RETIRED, "retired mutations do not belong in this lane")
            require(type(step["expect_error"]) is bool, "step error expectation must be boolean")


def signature_payload(tool, args, workspace, provider_version=None):
    before = max(0, min(args.get("context_before", 2), 20))
    after = max(0, min(args.get("context_after", 2), 20))
    if tool == "search":
        return {
            "version": 1, "workspace_id": workspace,
            "query": args.get("symbol") or args.get("query") or "",
            "regex": bool(args.get("regex", False)), "root": args.get("path", "."),
            "glob": args.get("glob", ""), "include_hidden": bool(args.get("include_hidden", False)),
            "kind": args.get("kind", "text"), "config_format": args.get("config_format", "auto"),
            "scope": args.get("scope", "workspace"), "base_ref": args.get("base_ref", "HEAD"),
            "diff_target": args.get("diff_target", "worktree"),
            "include_untracked": args.get("include_untracked", True),
            "context_before": before, "context_after": after,
            "group_by_file": args.get("group_by_file", True),
        }
    require(tool == "ast_grep", "fingerprint dynamic rule used by an unsupported tool")
    require(isinstance(provider_version, str) and provider_version, "AST provider version is missing")
    return {
        "cursor_version": 1, "workspace_id": workspace, "provider": "ast-grep-cli",
        "provider_version": provider_version, "mode": "pattern" if args.get("pattern") else "kind",
        "pattern": args.get("pattern", ""), "kind": args.get("kind", ""),
        "language": args.get("language", ""), "selector": args.get("selector", ""),
        "strictness": args.get("strictness", ""), "root": args.get("path", "."),
        "globs": args.get("globs", []), "include_hidden": bool(args.get("include_hidden", False)),
        "context_before": before, "context_after": after,
        "group_by_file": args.get("group_by_file", True),
    }


def cursor_checksum(tool, signature, position):
    label = "codexpro-search-cursor-v1" if tool == "search" else "codexpro-ast-grep-cursor-v1"
    return digest(label + "\x00" + signature + "\x00" + str(position))[:20]


def encode_cursor(payload):
    return base64.urlsafe_b64encode(source_json(payload).encode()).decode().rstrip("=")


def decode_cursor(value, tool, signature):
    require(isinstance(value, str) and 0 < len(value) <= 8192
            and re.fullmatch(r"[A-Za-z0-9_-]+", value), "invalid bounded cursor encoding")
    try:
        parsed = json.loads(base64.urlsafe_b64decode(value + "=" * (-len(value) % 4)))
    except (ValueError, UnicodeError) as error:
        raise ContractFailure("cursor is not base64url JSON") from error
    position_key = "after" if tool == "search" else "offset"
    require(isinstance(parsed, dict) and set(parsed) == {"v", "signature", position_key, "checksum"}
            and type(parsed["v"]) is int and parsed["v"] == 1
            and parsed["signature"] == signature, "cursor schema/signature mismatch")
    position = parsed[position_key]
    require((isinstance(position, str) if tool == "search"
             else type(position) is int and 0 <= position <= 9007199254740991),
            "cursor position type/range changed")
    require(parsed["checksum"] == cursor_checksum(tool, signature, position), "cursor checksum mismatch")
    expected = {"v": 1, "signature": signature, position_key: position, "checksum": parsed["checksum"]}
    require(value == encode_cursor(expected), "cursor no longer uses the exact source encoding")
    return expected


def normalize_cursor(value, tool, signature, normalized_signature, data, args):
    parsed = decode_cursor(value, tool, signature)
    matches = data.get("matches")
    require(isinstance(matches, list) and matches, "continuation cursor has no returned match")
    if tool == "search":
        last = matches[-1]
        expected = (last["path"] + "\x00" + str(last["line"]).zfill(12) + "\x00"
                    + str(last.get("end_line", last["line"])).zfill(12) + "\x00"
                    + last["source"] + "\x00" + last.get("address", ""))
        require(parsed["after"] == expected, "search cursor does not follow the last returned match")
        position_key = "after"
    else:
        previous = decode_cursor(args["cursor"], tool, signature)["offset"] if args.get("cursor") else 0
        require(parsed["offset"] == previous + len(matches), "AST cursor offset does not advance by returned hits")
        position_key = "offset"
    parsed["signature"] = normalized_signature
    parsed["checksum"] = cursor_checksum(tool, normalized_signature, parsed[position_key])
    return encode_cursor(parsed)


class Evidence:
    def __init__(self, path, clients):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.stream = gzip.open(self.path, "xb")
        os.chmod(self.path, 0o600)
        self.clients = clients
        self.bytes = self.records = 0

    def append(self, record):
        for client in self.clients():
            client.assert_no_credential(record)
        raw = (source_json(record) + "\n").encode()
        require(len(raw) <= MAX_RESPONSE_BYTES * 2, "single evidence record exceeds its bound")
        require(self.bytes + len(raw) <= MAX_RAW_BYTES, "raw evidence budget exceeded")
        self.stream.write(raw)
        self.stream.flush()
        self.bytes += len(raw)
        self.records += 1
        return {"record": self.records, "sha256": hashlib.sha256(raw).hexdigest()}

    def close(self):
        self.stream.close()


INVENTORY_SCRIPT = r"""
const fs = require("node:fs");
const crypto = require("node:crypto");
const entries = JSON.parse(process.argv[1]).map(path => {
  const st = fs.statSync(path), bytes = fs.readFileSync(path);
  return {path, bytes:st.size, modifiedMs:st.mtimeMs,
          sha256:crypto.createHash("sha256").update(bytes).digest("hex")};
});
entries.sort((a,b) => a.path.localeCompare(b.path));
const fingerprint=crypto.createHash("sha256").update(entries.map(x=>x.path+":"+x.bytes+":"+x.modifiedMs).join("\n")).digest("hex");
process.stdout.write(JSON.stringify({entries,fingerprint}));
"""


class Side:
    def __init__(self, label, endpoint, token_file, fixture_root, manifest_file, project, run_id):
        self.label = label
        self.manifest = json.loads(Path(manifest_file).read_text())
        port = 7211 if label == "reference" else 7210
        self.root, self.state, self.token = validate_launch(
            self.manifest, endpoint, token_file, fixture_root, port)
        require(project == self.manifest["project_id"], "project differs from launch manifest")
        self.endpoint = endpoint
        self.run_id = run_id
        self.bootstrap = Client(endpoint, str(self.token), timeout=35)
        self.clients = {"bootstrap": self.bootstrap}
        self.case_clients = {}
        self.bindings = {}
        self.dynamic_aliases = {}
        self.backup = None
        self.lock = None
        self.owned_fixture = False
        self.unsettled = False
        self.guard_owned = []
        self.guard_payload = ("sp6-parent-guard-" + run_id + "-" + label + "\n").encode()
        namespace = Path(os.path.commonpath((self.root, self.state)))
        private_path(namespace)
        require(namespace not in {Path("/tmp"), Path("/var/tmp")},
                "fixture and state need their own shared private namespace")
        self.guard_paths = [self.root.parent / name for name in ("outside", "escape.txt")]
        for target in self.guard_paths:
            private_path(target, missing=True)
            require(target.is_relative_to(namespace) and not os.path.lexists(target),
                    "negative-path fixture target already exists or is outside the private namespace")
        self.root_identity = (self.root.stat().st_dev, self.root.stat().st_ino)
        self.state_identity = (self.state.stat().st_dev, self.state.stat().st_ino)
        projects_response = self.bootstrap.call("list_projects", {})
        projects = raw_result(projects_response)["structuredContent"]["projects"]
        matches = [item for item in projects if item.get("id") == project]
        require(len(matches) == 1, "project did not resolve uniquely")
        self.workspace = matches[0]["workspace_id"]
        require(isinstance(self.workspace, str) and re.fullmatch(r"ws_[a-f0-9]{24}", self.workspace),
                "workspace ID shape changed")
        self.root_matches()
        nonce_name = ".sp6-binding-" + run_id + "-" + label + ".txt"
        nonce = self.root / nonce_name
        marker = "sp6-private-workflow-binding-" + run_id + "-" + label + "-" + str(self.manifest["pid"])
        fd = os.open(nonce, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as output:
            output.write(marker + "\n")
        try:
            result = raw_result(self.bootstrap.call("read", {
                "workspace_id": self.workspace, "path": nonce_name, "end_line": 1}))
            text = result.get("structuredContent", {}).get("text", "")
            plain = "\n".join(re.sub(r"^\s*\d+ \| ?", "", line) for line in text.splitlines())
            require(result.get("isError") is not True and plain == marker
                    and result["structuredContent"].get("sha256") == digest(marker + "\n"),
                    "MCP workspace does not map to the explicit fixture root")
        finally:
            self.root_matches()
            nonce.unlink()
        jobs = raw_result(self.bootstrap.call("jobs", {
            "workspace_id": self.workspace, "include_finished": False, "output": "none", "wait_ms": 0}))
        require(jobs.get("isError") is not True and jobs.get("structuredContent", {}).get("jobs") == [],
                "private fixture has active jobs; run mutation lanes sequentially")
        config = raw_result(self.bootstrap.call("server_config", {}))["structuredContent"]
        limits = config["analysisLimits"]
        keys = ("maxInventoryFiles", "maxAnalyzedFiles", "maxScannedBytes", "maxSymbols", "maxRelationships")
        require(isinstance(limits, dict) and set(limits) == set(keys), "analysis limit shape changed")
        self.analysis_limits = {key: limits[key] for key in keys}
        require(all(type(value) is int and value > 0 for value in self.analysis_limits.values()),
                "analysis limits must be positive integers")
        self.registry_response = self.bootstrap.rpc("tools/list", {})
        self.validate_response(self.registry_response, self.bootstrap.sequence)
        self.workflow = None

    @classmethod
    def open(cls, *args):
        instance = cls.__new__(cls)
        try:
            instance.__init__(*args)
        except BaseException:
            for client in getattr(instance, "clients", {}).values():
                client.close()
            raise
        return instance

    def all_clients(self):
        return list(self.clients.values())

    def root_matches(self):
        require(self.root.resolve(strict=True) == self.root
                and (self.root.stat().st_dev, self.root.stat().st_ino) == self.root_identity,
                "fixture root identity changed during the lane")

    def state_child(self, relative):
        require(self.state.resolve(strict=True) == self.state
                and (self.state.stat().st_dev, self.state.stat().st_ino) == self.state_identity,
                "private state directory identity changed")
        target = self.state / relative
        resolved = target.resolve(strict=False)
        require(resolved.is_relative_to(self.state), "fixture backup escaped private state")
        private_path(resolved, missing=True)
        return target

    def claim(self):
        self.root_matches()
        lock_path = self.state_child("paired-fixture-" + digest(str(self.root))[:16] + ".lock")
        fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        self.lock = os.fdopen(fd, "w")
        fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        parent = self.state_child("paired-fixture-backups")
        parent.mkdir(mode=0o700, exist_ok=True)
        backup = self.state_child("paired-fixture-backups/" + self.run_id + "-" + self.label)
        backup.mkdir(mode=0o700)
        entries = list(self.root.iterdir())
        require(len(entries) <= 10000, "private fixture has too many top-level entries")
        manifest = {"fixture_root": str(self.root), "root_device": self.root_identity[0],
                    "root_inode": self.root_identity[1], "original_names": [p.name for p in entries],
                    "negative_guard_paths": [str(path) for path in self.guard_paths],
                    "negative_guard_sha256": hashlib.sha256(self.guard_payload).hexdigest(),
                    "created_at": iso_now(), "note": "Restore original entries after clearing only the bound fixture."}
        (backup / "RESTORE.json").write_text(source_json(manifest) + "\n")
        originals = backup / "originals"
        originals.mkdir(mode=0o700)
        self.backup = backup
        for path in self.guard_paths:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            self.guard_owned.append(path)
            with os.fdopen(fd, "wb") as stream:
                stream.write(self.guard_payload)
        for entry in entries:
            os.replace(entry, originals / entry.name)
        self.owned_fixture = True

    def clear_owned_fixture(self):
        self.root_matches()
        require(not self.unsettled, "refusing fixture cleanup while a tool outcome is unsettled")
        require(self.owned_fixture and self.backup is not None and (self.backup / "RESTORE.json").is_file(),
                "refusing to clear a fixture without its owned backup")
        require(shutil.rmtree.avoids_symlink_attacks, "platform lacks safe directory removal")
        for entry in self.root.iterdir():
            if entry.is_symlink() or not entry.is_dir():
                entry.unlink()
            else:
                shutil.rmtree(entry)

    def command(self, args, *, env_extra=None, limit=1024 * 1024, timeout=10):
        self.root_matches()
        environment = {
            "PATH": os.environ.get("PATH", os.defpath), "HOME": str(self.state),
            "XDG_CONFIG_HOME": str(self.state / "paired-empty-config"),
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0", "LC_ALL": "C", "TZ": "UTC",
        }
        if env_extra:
            environment.update(env_extra)
        result = subprocess.run(["nice", "-n", "10", *args], cwd=self.root, env=environment,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout,
                                check=False)
        require(len(result.stdout) + len(result.stderr) <= limit, "bounded fixture helper output exceeded")
        require(result.returncode == 0, "fixture helper failed: " + args[0] + " exit=" + str(result.returncode))
        return result.stdout.decode("utf-8")

    def git(self, *args, env_extra=None):
        return self.command(["git", *args], env_extra=env_extra)

    def begin(self, case):
        require(not self.unsettled, "previous tool outcome is unsettled")
        require(all(item["unchanged"] for item in self.guard_snapshot()),
                "a previous operation modified a reserved parent-path guard")
        validate_launch(self.manifest, self.endpoint, self.token, self.root, 7211 if self.label == "reference" else 7210)
        self.close_case()
        self.clear_owned_fixture()
        self.workflow = case
        self.bindings = {"workspace": self.workspace}
        self.dynamic_aliases = {}
        payloads = {name: text.encode("utf-8") for name, text in case.get("fixtures", {}).items()}
        payloads.update({name: base64.b64decode(encoded, validate=True)
                         for name, encoded in case.get("fixtures_base64", {}).items()})
        for name, content in payloads.items():
            target = self.root.joinpath(*relative_path(name).parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content)
            target.chmod(0o644)
            os.utime(target, ns=(case["fixture_mtime_ns"], case["fixture_mtime_ns"]))
        if "git" in case.get("setup", {}):
            setup = case["setup"]["git"]
            empty_templates = self.state_child("paired-empty-git-template")
            empty_templates.mkdir(mode=0o700, exist_ok=True)
            env = {"GIT_AUTHOR_DATE": setup["date"], "GIT_COMMITTER_DATE": setup["date"],
                   "GIT_TEMPLATE_DIR": str(empty_templates)}
            self.git("init", "--quiet", "--object-format=sha1", "--initial-branch=" + setup["branch"], env_extra=env)
            for key, value in {
                "user.name": setup["user_name"], "user.email": setup["user_email"],
                "commit.gpgsign": "false", "core.hooksPath": os.devnull,
                "core.autocrlf": "false", "core.fileMode": "true", "core.abbrev": "7",
                "core.attributesFile": os.devnull, "core.excludesFile": os.devnull,
            }.items():
                self.git("config", "--local", key, value)
            self.git("add", "--", *setup["tracked"])
            self.git("commit", "--quiet", "-m", "test: initial contract fixture", env_extra=env)

    def client_for(self, session):
        session = session or "default"
        if session not in self.case_clients:
            client = Client(self.endpoint, str(self.token), timeout=35)
            self.case_clients[session] = client
            self.clients[self.workflow["id"] + "/" + session] = client
        return self.case_clients[session]

    def call(self, step, args):
        self.root_matches()
        require(not self.unsettled, "previous tool outcome is unsettled")
        require(process_start_ticks(self.manifest["pid"]) == self.manifest["process_start_ticks"],
                "server process changed during the lane")
        client = self.client_for(step.get("session"))
        expected_id = client.sequence + 1
        started_ms = int(time.time() * 1000)
        self.unsettled = True
        response = client.call(step["tool"], args)
        self.unsettled = False
        ended_ms = int(time.time() * 1000)
        return response, started_ms, ended_ms, expected_id

    def validate_response(self, response, expected_id):
        require(len(source_json(response).encode()) <= MAX_RESPONSE_BYTES, "tool response exceeds evidence bound")
        body = response["body"]
        require(response["http_status"] == 200 and isinstance(body, dict)
                and body.get("jsonrpc") == "2.0" and type(body.get("id")) is int
                and body["id"] == expected_id and isinstance(body.get("result"), dict),
                "MCP response failed its HTTP/JSON-RPC/request-ID contract")
        require(isinstance(response["duration_ms"], (int, float))
                and not isinstance(response["duration_ms"], bool)
                and math.isfinite(response["duration_ms"]) and response["duration_ms"] >= 0,
                "client observation duration is invalid")

    def bind(self, step, response):
        for name, pointer in step.get("bind", {}).items():
            value = get_pointer(response["body"], pointer)
            if pointer.endswith("/edit_tag"):
                require(isinstance(value, str) and re.fullmatch(r"[A-F0-9]{4}", value), "edit tag shape changed")
            elif pointer.endswith("/sha256"):
                require(isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value), "SHA-256 shape changed")
            elif pointer.endswith("/next_start_line"):
                require(type(value) is int and value >= 1, "line continuation binding is not positive")
            elif pointer.endswith("/next_cursor"):
                require(isinstance(value, str) and value, "cursor binding is missing")
            self.bindings[name] = copy.deepcopy(value)

    def working_files(self):
        files = []
        for directory, directories, names in os.walk(self.root, followlinks=False):
            kept = []
            for name in sorted(directories):
                if name == ".git":
                    continue
                path = Path(directory) / name
                if path.is_symlink():
                    files.append({"path": path.relative_to(self.root).as_posix(), "kind": "symlink",
                                  "target": os.readlink(path)})
                else:
                    kept.append(name)
            directories[:] = kept
            for name in sorted(names):
                path = Path(directory) / name
                relative = path.relative_to(self.root).as_posix()
                info = path.lstat()
                require(stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode), "unexpected fixture file type")
                if stat.S_ISLNK(info.st_mode):
                    files.append({"path": relative, "kind": "symlink", "target": os.readlink(path)})
                else:
                    require(info.st_size <= MAX_FIXTURE_BYTES, "tool created an oversized fixture file")
                    data = path.read_bytes()
                    files.append({"path": relative, "kind": "file", "mode": stat.S_IMODE(info.st_mode),
                                  "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
        return sorted(files, key=lambda entry: entry["path"])

    def guard_snapshot(self):
        values = []
        for path in self.guard_owned:
            info = path.lstat() if os.path.lexists(path) else None
            ordinary = info is not None and stat.S_ISREG(info.st_mode) and info.st_nlink == 1
            data = path.read_bytes() if ordinary and info.st_size <= MAX_FIXTURE_BYTES else None
            same = data == self.guard_payload
            value = {"name": path.name, "unchanged": same}
            if not same:
                value.update({"exists": info is not None, "regular_single_link": ordinary,
                              "bytes": info.st_size if info else None,
                              "sha256": hashlib.sha256(data).hexdigest() if data is not None else None})
            values.append(value)
        return values

    def cache_key(self, value):
        require(isinstance(value, str), "cache key must be a string")
        prefix = self.workspace + ":"
        require(value.startswith(prefix), "cache key has the wrong workspace identity")
        rest = value[len(prefix):]
        fingerprint, separator, limits_text = rest.partition(":")
        require(separator and re.fullmatch(r"[a-f0-9]{64}", fingerprint), "cache fingerprint shape changed")
        require(limits_text == source_json(self.analysis_limits), "cache key changed its exact analysis limits")
        files = self.working_files()
        require(all(entry["kind"] == "file" for entry in files), "cache validation fixture contains a symlink")
        paths = [entry["path"] for entry in files]
        require(len(paths) <= 128 and sum(entry["bytes"] for entry in files) <= MAX_FIXTURE_BYTES,
                "cache validation inventory exceeds its bound")
        require(self.workflow["id"] in {"ANALYSIS-SCOPE-15", "GIT-REVIEW-SESSION-18", "GIT-COMMIT-EXPLICIT-19"},
                "cache rule needs an explicit source-compatible inventory fixture")
        observed = json.loads(self.command(["node", "-e", INVENTORY_SCRIPT, source_json(paths)], limit=65536))
        require(observed["fingerprint"] == fingerprint,
                "cache fingerprint differs from the source algorithm over the actual fixture")
        semantic = [{"path": entry["path"], "bytes": entry["bytes"], "sha256": entry["sha256"]}
                    for entry in observed["entries"]]
        normalized = "<workspace>:" + digest(source_json(semantic)) + ":" + limits_text
        return normalized, {"inventory": semantic, "limits": self.analysis_limits}

    def commit_value(self, value, started_ms, ended_ms):
        require(isinstance(value, str) and re.fullmatch(r"[a-f0-9]{40}", value), "commit must be a SHA-1 object ID")
        require(self.git("rev-parse", "HEAD").strip() == value, "returned commit does not name HEAD")
        raw = self.git("cat-file", "-p", value)
        raw_bytes = raw.encode("utf-8")
        require(hashlib.sha1(b"commit " + str(len(raw_bytes)).encode() + b"\x00" + raw_bytes).hexdigest() == value,
                "commit object contents do not hash to the returned identity")
        lines = raw.splitlines(keepends=True)
        found = set()
        for index, line in enumerate(lines):
            if not line.strip():
                break
            for role in ("author", "committer"):
                if line.startswith(role + " "):
                    match = re.fullmatch(r"(" + role + r" .+ <[^>\n]+>) ([0-9]+) ([+-][0-9]{4})\n", line)
                    require(match is not None, "commit identity/date header shape changed")
                    stamp = int(match.group(2))
                    require(started_ms // 1000 - 2 <= stamp <= ended_ms // 1000 + 2,
                            "commit time is outside the observed call window")
                    lines[index] = match.group(1) + " <call-time> " + match.group(3) + "\n"
                    found.add(role)
        require(found == {"author", "committer"}, "commit object omitted identity headers")
        semantic = "".join(lines)
        alias = "<commit:" + digest(semantic) + ">"
        return alias, semantic

    def normalize(self, step, args, response, started_ms, ended_ms):
        result = copy.deepcopy(response)
        body = result["body"]
        body["id"] = 1  # call() already proved an exact echo of this side's own request ID.
        result["duration_ms"] = "<client-observed-ms>"
        evidence = {}
        data = copy.deepcopy(body["result"].get("structuredContent", {}))
        aliases = {str(self.root): "<fixture-root>", str(self.state): "<private-state>",
                   self.workspace: "<workspace>", **self.dynamic_aliases}
        signature = normalized_signature = None
        if any(pointer.endswith("/query_fingerprint") or pointer.endswith("/next_cursor")
               for pointer in step.get("dynamic", [])):
            if "query_fingerprint" in data:
                payload = signature_payload(step["tool"], args, self.workspace, data.get("provider_version"))
                signature = digest(source_json(payload))
                require(data["query_fingerprint"] == signature,
                        "query fingerprint differs from the exact source algorithm")
                payload["workspace_id"] = "<workspace>"
                normalized_signature = digest(source_json(payload))
                evidence["query"] = payload
        for pointer in step.get("dynamic", []):
            try:
                value = get_pointer(body, pointer)
            except ContractFailure:
                continue  # Keep absence unchanged; the full-envelope comparison still checks it.
            normalized = value
            if pointer.endswith("/query_fingerprint"):
                require(signature is not None, "query fingerprint is not independently validated")
                normalized = normalized_signature
            elif pointer.endswith("/next_cursor"):
                if value is not None:
                    require(signature is not None, "cursor lacks its validated query fingerprint")
                    normalized = normalize_cursor(value, step["tool"], signature, normalized_signature, data, args)
            elif pointer.endswith("/cache/key"):
                normalized, detail = self.cache_key(value)
                evidence[pointer] = detail
            elif pointer.endswith("/commit"):
                normalized, detail = self.commit_value(value, started_ms, ended_ms)
                evidence["commit_object"] = detail
                short = data.get("commit_short")
                require(short == value[:12], "short commit does not match the full object ID")
                aliases[value[:12]] = normalized + ":12"
                aliases[value[:7]] = normalized + ":7"
                self.dynamic_aliases.update({value: normalized, value[:12]: normalized + ":12",
                                            value[:7]: normalized + ":7"})
            elif pointer.endswith("/commit_short"):
                require(data.get("commit") and value == data["commit"][:12],
                        "short commit is not causally bound to the full commit")
                normalized = aliases.get(value, self.dynamic_aliases.get(value))
                require(normalized is not None, "short commit was not validated in this call")
            else:
                raise ContractFailure("unsupported declared dynamic pointer: " + pointer)
            replace_pointer(body, pointer, normalized)
            if isinstance(value, str) and isinstance(normalized, str) and value != normalized:
                aliases[value] = normalized
        replacement_pattern = re.compile("|".join(re.escape(value)
            for value in sorted(aliases, key=len, reverse=True)))
        def replace(value):
            if isinstance(value, dict):
                return {key: replace(item) for key, item in value.items()}
            if isinstance(value, list):
                return [replace(item) for item in value]
            if isinstance(value, str):
                value = replacement_pattern.sub(lambda match: aliases[match.group(0)], value)
            return value
        return {"response": replace(result), "validated_dynamic": replace(evidence)}

    def snapshot(self):
        result = {"files": self.working_files(), "parent_path_guards": self.guard_snapshot()}
        if (self.root / ".git").is_dir():
            head = self.git("rev-parse", "HEAD").strip()
            result["git"] = {
                "head": self.dynamic_aliases.get(head, head),
                "tree": self.git("rev-parse", "HEAD^{tree}").strip(),
                "index": self.git("ls-files", "--stage", "-z"),
                "status": self.git("status", "--porcelain=v1", "-z", "--untracked-files=all"),
            }
        return result

    def close_case(self):
        for client in self.case_clients.values():
            client.close()
        self.case_clients = {}

    def restore(self):
        errors = []
        self.close_case()
        if self.backup is not None:
            try:
                require(not self.unsettled, "tool outcome is unsettled; preserve backup until the private server is quiescent")
                self.root_matches()
                self.state_child("paired-fixture-backups")
                for path in self.guard_owned:
                    if os.path.lexists(path):
                        require(not path.is_dir() or path.is_symlink(),
                                "reserved parent-path guard became a directory; preserve recovery evidence")
                        path.unlink()
                self.guard_owned = []
                if self.owned_fixture:
                    self.clear_owned_fixture()
                    self.owned_fixture = False
                originals = self.backup / "originals"
                for entry in originals.iterdir():
                    require(not (self.root / entry.name).exists() and not (self.root / entry.name).is_symlink(),
                            "restore would overwrite an original fixture entry")
                    os.replace(entry, self.root / entry.name)
                originals.rmdir()
                (self.backup / "RESTORE.json").unlink()
                self.backup.rmdir()
                self.backup = None
                self.owned_fixture = False
            except Exception as error:
                errors.append(safe_error(error, self.all_clients()))
        self.bootstrap.close()
        if self.lock is not None:
            self.lock.close()
            self.lock = None
        return errors


def difference(left, right, limit=16000):
    if canonical(left) == canonical(right):
        return None
    lines = list(difflib.unified_diff(
        json.dumps(left, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False).splitlines(),
        json.dumps(right, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False).splitlines(),
        fromfile="TypeScript", tofile="Go", lineterm=""))
    text = "\n".join(lines)
    return {"text": text[:limit], "truncated": len(text) > limit,
            "reference_sha256": digest(canonical(left)), "candidate_sha256": digest(canonical(right))}


def compare_inventory(reference, candidate):
    left, right = raw_result(reference)["tools"], raw_result(candidate)["tools"]
    require(isinstance(left, list) and isinstance(right, list), "tools/list shape changed")
    require(len({tool["name"] for tool in left}) == len(left)
            and len({tool["name"] for tool in right}) == len(right), "tools/list repeats a descriptor")
    removed = [tool["name"] for tool in left if tool["name"] in RETIRED]
    retained = [tool for tool in left if tool["name"] not in RETIRED]
    require(not any(tool["name"] in RETIRED for tool in right), "candidate exposed an approved retired tool")
    compared = []
    for response in (reference, candidate):
        value = copy.deepcopy(response)
        value["duration_ms"] = "<client-observed-ms>"
        value["body"]["id"] = 1
        compared.append(value)
    compared[0]["body"]["result"]["tools"] = retained
    diff = difference(*compared)
    return {"status": "fail" if diff else "approved_delta" if removed else "pass",
            "removed_descriptors": removed, "approval_commit": APPROVAL_COMMIT,
            "retained_descriptor_diff": diff,
            "note": "Approved removals are not counted as unchanged passes."}


def run_workflows(sides, cases, evidence, report=None):
    if report is None:
        report = []
    for case in cases:
        item = {"id": case["id"], "steps": [], "setup_errors": {}, "status": "running"}
        report.append(item)
        for side in sides:
            try:
                side.begin(case)
            except Exception as error:
                item["setup_errors"][side.label] = safe_error(error, side.all_clients())
        if item["setup_errors"]:
            item["status"] = "blocked"
            item["blocked_steps"] = len(case["steps"])
            continue
        setup_snapshots = [side.snapshot() for side in sides]
        evidence.append({"workflow": case["id"], "phase": "seeded", "sides": setup_snapshots})
        item["setup_diff"] = difference(*setup_snapshots)
        if item["setup_diff"]:
            item["status"] = "blocked"
            item["blocked_steps"] = len(case["steps"])
            continue
        for step in case["steps"]:
            record = {"id": step["id"], "tool": step["tool"], "session": step.get("session", "default"),
                      "expect_error": step["expect_error"], "status": "running", "errors": {}, "raw": {}}
            item["steps"].append(record)
            arguments = {}
            for side in sides:
                try:
                    arguments[side.label] = resolve_refs(step["args"], side.bindings, side.root)
                except Exception as error:
                    record["errors"][side.label] = safe_error(error, side.all_clients())
            if record["errors"]:
                record["status"] = "blocked"
                item["status"] = "fail"
                continue
            normalized = {}
            for side in sides:
                try:
                    response, started, ended, expected_id = side.call(step, arguments[side.label])
                    record["raw"][side.label] = evidence.append({
                        "workflow": case["id"], "step": step["id"], "side": side.label,
                        "session_label": record["session"], "arguments": arguments[side.label],
                        "started_ms": started, "finished_ms": ended, "response": response})
                    side.validate_response(response, expected_id)
                    result = raw_result(response)
                    actual_error = result.get("isError") is True
                    if actual_error != step["expect_error"]:
                        record["errors"][side.label] = "Error outcome differs from the corpus expectation"
                    try:
                        side.bind(step, response)
                    except Exception as error:
                        record["errors"][side.label + "_binding"] = safe_error(error, side.all_clients())
                    normalized[side.label] = side.normalize(step, arguments[side.label], response, started, ended)
                    evidence.append({"workflow": case["id"], "step": step["id"], "side": side.label,
                                     "phase": "normalized", "value": normalized[side.label]})
                except Exception as error:
                    record["errors"][side.label] = safe_error(error, side.all_clients())
            if set(normalized) == {"reference", "candidate"}:
                record["diff"] = difference(normalized["reference"], normalized["candidate"])
            else:
                record["diff"] = None
            if record["errors"] or record["diff"]:
                record["status"] = "fail"
                item["status"] = "fail"
            else:
                record["status"] = "pass"
        snapshots = {}
        for side in sides:
            try:
                snapshots[side.label] = side.snapshot()
                evidence.append({"workflow": case["id"], "side": side.label, "phase": "final-state",
                                 "value": snapshots[side.label]})
                if not all(value["unchanged"] for value in snapshots[side.label]["parent_path_guards"]):
                    item["setup_errors"][side.label + "_parent_path_guard"] = "tool modified a reserved path outside its fixture"
                    item["status"] = "fail"
            except Exception as error:
                item["setup_errors"][side.label + "_final_state"] = safe_error(error, side.all_clients())
                item["status"] = "fail"
        if set(snapshots) == {"reference", "candidate"}:
            item["final_state_diff"] = difference(snapshots["reference"], snapshots["candidate"])
            if item["final_state_diff"]:
                item["status"] = "fail"
        for side in sides:
            side.close_case()
        if item["status"] == "running":
            item["status"] = "pass"
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", default="http://127.0.0.1:7211/mcp")
    parser.add_argument("--candidate", default="http://127.0.0.1:7210/mcp")
    for label in ("reference", "candidate"):
        parser.add_argument("--" + label + "-token", required=True)
        parser.add_argument("--" + label + "-fixture", required=True)
        parser.add_argument("--" + label + "-manifest", required=True)
    parser.add_argument("--project", default="contract")
    parser.add_argument("--output", required=True)
    parser.add_argument("--only", action="append", default=[], help="Exact workflow ID; repeatable")
    args = parser.parse_args()
    require(os.getpriority(os.PRIO_PROCESS, 0) >= 10, "run the paired lane under nice -n 10")
    cases = [case for case in CASES if not args.only or case["id"] in args.only]
    require(cases and (not args.only or set(args.only) == {case["id"] for case in cases}),
            "unknown or empty workflow selection")
    validate_cases(cases)
    output = artifact_path(args.output)
    require(not output.exists(), "choose a new evidence basename for each run")
    run_id = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%S") + "-" + uuid.uuid4().hex[:8]
    sides = []
    evidence = None
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "run_id": run_id,
              "started_at": iso_now(), "lane": "files-search-git", "workflows": [],
              "normalization_rules": [
                  "Exact JSON-RPC request-ID echo before substituting a common correlation ID",
                  "Only manifest/MCP-bound own roots and workspace identity",
                  "Search/AST fingerprint payload, canonical cursor encoding/checksum and causal position",
                  "Declared cache keys verified against exact Node filesystem inventory and analysis limits",
                  "Declared commit IDs verified against HEAD and the complete object with bounded call timestamps",
                  "Client-observed wall duration; no arbitrary tool fields, hashes, tags or text are dropped",
              ]}
    def interrupted(signum, frame):
        raise KeyboardInterrupt("bounded lane interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    try:
        for label in ("reference", "candidate"):
            sides.append(Side.open(label, getattr(args, label), getattr(args, label + "_token"),
                              getattr(args, label + "_fixture"), getattr(args, label + "_manifest"),
                              args.project, run_id))
        require(sides[0].root != sides[1].root and sides[0].state != sides[1].state,
                "reference and candidate require separate fixture and state directories")
        require(not set(sides[0].guard_paths) & set(sides[1].guard_paths),
                "reference and candidate need separate parent-path guard targets")
        for side in sides:
            require(not output.is_relative_to(side.root), "evidence cannot be stored inside the reset fixture")
        evidence = Evidence(output.with_suffix(".raw.jsonl.gz"),
                            lambda: [client for side in sides for client in side.all_clients()])
        for side in sides:
            evidence.append({"phase": "tools-list", "side": side.label, "response": side.registry_response})
            side.claim()
        report["inventory"] = compare_inventory(sides[0].registry_response, sides[1].registry_response)
        run_workflows(sides, cases, evidence, report["workflows"])
    except BaseException as error:
        report["fatal_error"] = safe_error(error, [client for side in sides for client in side.all_clients()])
    finally:
        report["cleanup_errors"] = {}
        for side in reversed(sides):
            errors = side.restore()
            if errors:
                report["cleanup_errors"][side.label] = errors
                if side.backup:
                    report.setdefault("recovery_backups", {})[side.label] = str(side.backup)
        if evidence:
            evidence.close()
            report["raw_evidence"] = {"path": str(evidence.path), "records": evidence.records,
                                      "uncompressed_bytes": evidence.bytes,
                                      "sha256": hashlib.sha256(evidence.path.read_bytes()).hexdigest()}
        report["finished_at"] = iso_now()
        steps = [step for case in report["workflows"] for step in case["steps"]]
        report["summary"] = {
            "planned_workflows": len(cases), "planned_steps": sum(len(case["steps"]) for case in cases),
            "passed_workflows": sum(case["status"] == "pass" for case in report["workflows"]),
            "passed_steps": sum(step["status"] == "pass" for step in steps),
            "failed_steps": sum(step["status"] == "fail" for step in steps),
            "blocked_steps": sum(step["status"] == "blocked" for step in steps)
                + sum(case.get("blocked_steps", 0) for case in report["workflows"]),
            "approved_deltas": len(report.get("inventory", {}).get("removed_descriptors", [])),
        }
        report["passed"] = (not report.get("fatal_error") and not report["cleanup_errors"]
                            and report.get("inventory", {}).get("status") != "fail"
                            and len(report["workflows"]) == len(cases)
                            and all(case["status"] == "pass" for case in report["workflows"]))
        for side in sides:
            for client in side.all_clients():
                client.assert_no_credential(report)
        output.parent.mkdir(parents=True, exist_ok=True)
        with output.open("x") as stream:
            os.chmod(output, 0o600)
            stream.write(json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False) + "\n")
        print(source_json({"output": str(output), "passed": report["passed"], **report["summary"]}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
