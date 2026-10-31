#!/usr/bin/env python3
"""Bounded differential admin/catalog lane for two explicitly private processes.

The lane never starts, stops, signals, or reconfigures a running server. Saved
profiles are test fixtures under each private config home; original bytes are
restored after settled requests. Created projects remain registered and are
reported as private test artifacts. Run sequentially with other mutation lanes.
"""
from __future__ import annotations

import argparse
import base64
import copy
import datetime as dt
import email.utils
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from contract_suite import Client, SOURCE_COMMIT, raw_result
from paired_contract_suite import (
    APPROVAL_COMMIT, RETIRED, ContractFailure, Evidence, artifact_path, canonical,
    compare_inventory, difference, digest, iso_now, private_path,
    process_start_ticks, require, safe_error, source_json, validate_launch,
)

MAX_HTTP_BYTES = 2 * 1024 * 1024
MAX_STATE_BYTES = 1024 * 1024
HEADERS = (
    "content-type", "cache-control", "pragma", "referrer-policy",
    "permissions-policy", "x-content-type-options", "x-frame-options",
    "content-security-policy", "www-authenticate", "allow", "location",
    "retry-after", "access-control-allow-origin", "access-control-allow-methods",
    "access-control-allow-headers", "access-control-expose-headers",
)
COVERAGE = (
    "Recorded CodexPro activity only; the last-change scan covers at most 500 "
    "actions. Launch is not completion. External edits, older changes and expired "
    "records may be missing; inspect current source. A different worktree's "
    "changes may not be present here."
)
GAP_GUIDANCE = (
    "Activity history is incomplete. This is a recent retained snapshot, not a "
    "complete replay since the checkpoint. Inspect current source, the saved "
    "handoff and durable operation/job receipts before continuing. Missing "
    "activity is not evidence that work did not happen."
)


def timestamp(value):
    require(isinstance(value, str) and re.fullmatch(
        r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z", value),
        "timestamp must preserve the source millisecond UTC form")
    return int(dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp() * 1000)


def regular_private_file(path, *, missing=False, limit=MAX_STATE_BYTES):
    path = Path(path)
    resolved = private_path(path, missing=missing)
    require(path.is_absolute() and path == resolved, "private file contains a symlink or relative component")
    if not path.exists():
        require(missing, "required private file is absent")
        return None
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == os.getuid(),
            "private file is not a singly linked regular file owned by this user")
    require(info.st_size <= limit, "private file exceeds its bounded read")
    return info


def read_small(path):
    regular_private_file(path)
    with Path(path).open("rb") as source:
        value = source.read(MAX_STATE_BYTES + 1)
    require(len(value) <= MAX_STATE_BYTES, "private file grew beyond its bound")
    return value


def cli_option(argv, name):
    value = None
    flag = "--" + name
    for index, item in enumerate(argv):
        if item.startswith(flag + "="):
            value = item[len(flag) + 1:]
        elif item == flag:
            require(index + 1 < len(argv), "launch flag lacks a value")
            value = argv[index + 1]
    return value


def explicit_setting(argv, env, flag, env_keys):
    value = cli_option(argv, flag)
    if value is not None:
        return value
    for key in env_keys:
        if env.get(key):
            return env[key]
    return None


def process_path_binding(manifest):
    """Read only whitelisted settings; raw argv/environment never leave memory."""
    pid = manifest["pid"]
    raw_argv = Path(f"/proc/{pid}/cmdline").read_bytes()
    raw_env = Path(f"/proc/{pid}/environ").read_bytes()
    require(len(raw_argv) <= MAX_STATE_BYTES and len(raw_env) <= MAX_STATE_BYTES,
            "process metadata exceeds its read bound")
    argv = raw_argv.decode("utf-8").split("\0")
    env = {}
    allowed = {
        "CODEXPRO_HOME", "CODEXPRO_GO_HOME", "CODEXPRO_PROJECTS_FILE",
        "CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_CODEX_DIR", "CODEXPRO_GO_TOKEN_FILE",
        "CODEX_HOME", "CODEXPRO_GO_LIMITS_FILE", "CODEXPRO_ENV_FILE", "CODEXPRO_WORK_DIR", "HOME",
    }
    for pair in raw_env.decode("utf-8").split("\0"):
        key, sep, value = pair.partition("=")
        if sep and key in allowed:
            env[key] = value
    cwd = Path(f"/proc/{pid}/cwd").resolve(strict=True)
    return private_launch_paths(manifest, argv, env, cwd)


def private_launch_paths(manifest, argv, env, cwd):
    """Bind role paths to the running process before normalizing their echoes."""
    home = explicit_setting(argv, env, "state-dir", ["CODEXPRO_GO_HOME", "CODEXPRO_HOME"])
    catalog = explicit_setting(argv, env, "projects-file",
                               ["CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_PROJECTS_FILE"])
    require(home and catalog, "admin lane requires explicit process home/catalog launch bindings")
    cwd = Path(cwd)
    resolve = lambda value: (Path(value) if Path(value).is_absolute() else cwd / value).resolve(strict=True)
    require(resolve(home) == private_path(manifest["config_home"]),
            "running process config home differs from the explicit private manifest")
    require(resolve(catalog) == private_path(manifest["projects_file"]),
            "running process catalog differs from the explicit private manifest")
    state = private_path(manifest["state_root"])
    codex = explicit_setting(argv, env, "codex-dir", ["CODEXPRO_CODEX_DIR", "CODEX_HOME"])
    limits = explicit_setting(argv, env, "limits-file", ["CODEXPRO_GO_LIMITS_FILE", "CODEXPRO_ENV_FILE"])
    require(codex and limits, "admin lane requires explicit private instruction and limits paths")
    codex = private_path(Path(codex) if Path(codex).is_absolute() else cwd / codex, missing=True)
    limits = private_path(resolve(limits))
    require((not codex.exists() or codex.is_dir()) and limits.is_file()
            and codex.is_relative_to(state) and limits.is_relative_to(state),
            "instruction or limits path is not bound to private instance state")
    work = explicit_setting(argv, env, "work-dir", ["CODEXPRO_WORK_DIR"]) or str(resolve(home) / "work")
    work = private_path(Path(work) if Path(work).is_absolute() else cwd / work, missing=True)
    require(work.is_relative_to(state), "private work path escapes the explicit instance state")
    account_home = Path(env.get("HOME") or Path.home()).expanduser().resolve(strict=True)
    require(account_home.is_absolute() and account_home.is_dir(), "process account home is not a directory")
    return {"config_home": str(resolve(home)), "projects_file": str(resolve(catalog)),
            "codex_dir": str(codex), "limits_file": str(limits), "work_dir": str(work),
            "account_home": str(account_home)}


def private_role_aliases(paths, account_home):
    """Exact, independently bound paths plus their source [home] spelling only.

    Do not alias [workspace:*], [project:*], [codex-data], or a whole [home]
    prefix: those labels and unknown path suffixes remain contract-visible.
    """
    home = Path(account_home)
    aliases = {}
    for declared, label in paths.items():
        path = private_path(declared, missing=True)
        require(path == Path(declared), "role alias is not a canonical private path")
        aliases[str(path)] = label
        if path.is_relative_to(home):
            aliases["[home]/" + path.relative_to(home).as_posix()] = label
    return aliases


def catalog_object(path):
    raw = read_small(path)
    value = json.loads(raw)
    require(isinstance(value, dict) and isinstance(value.get("projects"), list),
            "catalog is not an object containing projects")
    seen = set()
    for project in value["projects"]:
        require(isinstance(project, dict) and isinstance(project.get("id"), str)
                and project["id"] not in seen and isinstance(project.get("root"), str)
                and Path(project["root"]).is_absolute(),
                "catalog project identities are not unique and explicit")
        seen.add(project["id"])
    require(isinstance(value.get("creationRoots"), list), "catalog has no explicit creation roots")
    require(value.get("defaultProject") in seen, "catalog default project is not explicit")
    return value, digest(raw.decode("utf-8"))


def replace_strings(value, aliases):
    if isinstance(value, dict):
        return {key: replace_strings(item, aliases) for key, item in value.items()}
    if isinstance(value, list):
        return [replace_strings(item, aliases) for item in value]
    if isinstance(value, str):
        for original in sorted(aliases, key=len, reverse=True):
            value = value.replace(original, aliases[original])
    return value


def last_change(actions):
    changed = [
        action for action in actions
        if action["changed_path_count"] > 0 or
        (action.get("git_before", {}).get("head") and action.get("git_after", {}).get("head")
         and action["git_before"]["head"] != action["git_after"]["head"])
    ]
    if not changed:
        return None
    action = changed[-1]
    result = {"at": action["finished_at"], "action_id": action["action_id"],
              "changed_paths": action["changed_paths"][:10]}
    if "workspace_id" in action:
        result["workspace_id"] = action["workspace_id"]
    return result


def recent_actions(actions, workspace):
    result = []
    for action in actions[-10:]:
        value = {
            "action_id": action["action_id"], "sequence": action["sequence"],
            "at": action["finished_at"], "tool": action["tool_name"],
            "status": action["status"], "actor_ref": action["actor_ref"],
            "changed_paths": action["changed_paths"][:10],
            "changed_path_count": action["changed_path_count"],
            "attribution": ("this workspace; see actor/operation evidence"
                            if action.get("workspace_id") == workspace
                            else "other or unattributed project work"),
        }
        for name in ("workspace_id", "work"):
            if name in action:
                value[name] = copy.deepcopy(action[name])
        result.append(value)
    return result


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


class Side:
    def __init__(self, label, endpoint, token_file, manifest_file, run_id):
        self.label, self.run_id = label, run_id
        self.manifest = json.loads(Path(manifest_file).read_text())
        port = 7211 if label == "reference" else 7210
        self.root, self.state, self.token = validate_launch(
            self.manifest, endpoint, token_file, self.manifest["fixture_root"], port)
        self.home = private_path(self.manifest["config_home"])
        self.catalog_path = private_path(self.manifest["projects_file"])
        self.creation_root = private_path(self.manifest["creation_root"])
        self.creation_id = self.manifest["creation_root_id"]
        require(self.home == self.state or self.home.is_relative_to(self.state),
                "config home is not inside the private instance state")
        require(self.catalog_path.is_relative_to(self.state),
                "writable catalog is not inside private instance state")
        require(self.creation_root.is_relative_to(self.state) and self.creation_root.is_dir(),
                "creation root is not an existing private instance directory")
        require(not self.root.is_relative_to(self.creation_root)
                and self.root != self.creation_root,
                "creation root must not contain the existing fixture")
        self.path_bindings = process_path_binding(self.manifest)
        self.codex_dir = Path(self.path_bindings["codex_dir"])
        self.work_dir = Path(self.path_bindings["work_dir"])
        self.catalog, self.catalog_hash = catalog_object(self.catalog_path)
        creation = [item for item in self.catalog["creationRoots"] if item.get("id") == self.creation_id]
        require(len(creation) == 1 and Path(creation[0]["root"]).resolve(strict=True) == self.creation_root,
                "creation-root ID is not bound to the owned private directory")
        fixture = [item for item in self.catalog["projects"]
                   if item["id"] == self.manifest["project_id"]]
        require(len(fixture) == 1 and Path(fixture[0]["root"]).resolve(strict=True) == self.root,
                "catalog fixture project does not match the private root")
        self.default_root = Path(next(item["root"] for item in self.catalog["projects"]
                                     if item["id"] == self.catalog["defaultProject"])).resolve(strict=True)
        connector_id = digest(str(self.default_root))[:48]
        self.principal = "connector_" + digest(connector_id)[:32]
        self.expected_actor = "actor_" + digest(self.principal)[:32]
        self.profile_path = self.home / "profiles" / (digest(str(self.default_root))[:24] + ".json")
        require(private_path(self.profile_path, missing=True).is_relative_to(self.home),
                "derived profile escapes the private config home")
        self.endpoint = endpoint
        self.origin = urllib.parse.urlunsplit((*urllib.parse.urlsplit(endpoint)[:2], "", "", ""))
        self.port = port
        self.client = Client(endpoint, str(self.token), timeout=30)
        self.clients = {"bootstrap": self.client}
        self.session_windows = {"bootstrap": int(time.time() * 1000)}
        self.early_evidence = []
        self.evidence = None
        self.current = "binding"
        self.unsettled = False
        self.profile_backup = None
        self.profile_original_mode = None
        self.profile_armed = False
        self.profile_last_hash = None
        self.profile_saved_clock = None
        self.profile_saved_label = None
        self.created = {}
        self.call_records = []
        self.actions = {}
        self.actor = None
        self.normalizations = []
        self.approved = []
        self.aliases = {
            str(self.profile_path): "<private-profile>",
            str(self.catalog_path): "<private-catalog>",
            str(self.creation_root): "<private-creation-root>",
            str(self.home): "<private-config-home>",
            str(self.state): "<private-state>",
            self.origin: "<private-origin>",
            self.expected_actor: "<validated-actor>",
        }
        self.aliases.update(private_role_aliases({
            str(self.profile_path): "<private-profile>",
            str(self.catalog_path): "<private-catalog>",
            str(self.creation_root): "<private-creation-root>",
            str(self.codex_dir): "<private-instruction-dir>",
            self.path_bindings["limits_file"]: "<private-limits-file>",
            str(self.work_dir): "<private-work-dir>",
        }, self.path_bindings["account_home"]))
        self.normalizations.append({"case": "binding", "rule":
            "exact process-bound private path roles and their source [home] spellings",
            "roles": self.path_bindings})
        self.root_identity = (self.root.stat().st_dev, self.root.stat().st_ino)
        self.creation_identity = (self.creation_root.stat().st_dev, self.creation_root.stat().st_ino)
        self.lock = open(self.state / ".admin-catalog-contract.lock", "a+b")
        os.chmod(self.lock.name, 0o600)
        fcntl.flock(self.lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.refresh_catalog(expect_change=False)
        initial = self.mcp("list_projects", {})
        self.bind_projects(self.data(initial))
        self.bind_fixture(self.manifest["project_id"], self.root)
        listed_jobs = self.data(self.mcp("jobs", {"workspace_id": self.workspace, "wait_ms": 0}))
        jobs = listed_jobs.get("jobs", [])
        require(isinstance(jobs, list) and not any(item.get("status") in
                {"queued", "starting", "running", "stopping"} for item in jobs),
                "private fixture has active jobs; serialize the mutation lanes")
        self.inventory_response = self.mcp_rpc("tools/list", {})
        self.tools = [tool["name"] for tool in raw_result(self.inventory_response)["tools"]]
        require({"list_projects", "create_project", "server_config", "codexpro_self_test",
                 "open_workspace", "activity_list", "activity_status"} <= set(self.tools),
                "this lane requires direct mode, audit metadata, and a retained standard/full tier")
        self.initial_config_response = self.mcp("server_config", {})
        self.initial_config = self.data(self.initial_config_response)
        require(self.initial_config.get("worktreeMode") == "off"
                and self.initial_config.get("bashMode") == "full"
                and self.initial_config.get("writeMode") == "workspace"
                and self.initial_config.get("connectionTest") is False,
                "default lane requires direct, full-permission, non-connection-test mode")
        require(self.initial_config.get("projectCreationEnabled") is True,
                "private catalog creation is not enabled")

    @property
    def workspace(self):
        return self.workspaces[self.manifest["project_id"]]

    def alive(self):
        require(not self.unsettled, "a previous private request has an unsettled outcome")
        require(process_start_ticks(self.manifest["pid"]) == self.manifest["process_start_ticks"],
                "private server changed during the lane")
        require((self.root.stat().st_dev, self.root.stat().st_ino) == self.root_identity,
                "private fixture identity changed")
        require((self.creation_root.stat().st_dev, self.creation_root.stat().st_ino) == self.creation_identity,
                "private creation-root identity changed")

    def record(self, value):
        if self.evidence:
            return self.evidence.append({"side": self.label, "case": self.current, **value})
        self.early_evidence.append({"side": self.label, "case": self.current, **value})
        require(len(source_json(self.early_evidence).encode()) <= 4 * 1024 * 1024,
                "bootstrap evidence exceeds its memory bound")
        return None

    def session(self, name):
        if name not in self.clients:
            self.session_windows[name] = int(time.time() * 1000)
            self.clients[name] = Client(self.endpoint, str(self.token), timeout=30)
        return self.clients[name]

    def mcp_rpc(self, method, params, session="bootstrap"):
        self.alive()
        client = self.session(session)
        expected = client.sequence + 1
        start = int(time.time() * 1000)
        self.unsettled = True
        response = client.rpc(method, params)
        self.unsettled = False
        end = int(time.time() * 1000)
        require(response["http_status"] == 200 and isinstance(response["body"], dict)
                and response["body"].get("jsonrpc") == "2.0"
                and type(response["body"].get("id")) is int
                and response["body"].get("id") == expected
                and isinstance(response["body"].get("result"), dict),
                "MCP HTTP/JSON-RPC/request-ID contract failed")
        self.record({"request": {"method": method, "params": params, "session": session,
                                 "id": expected}, "response": response,
                     "window_ms": [start, end]})
        if method == "tools/call":
            self.call_records.append({"label": self.current, "tool": params["name"],
                "request_ref": "request_" + digest(str(expected))[:32],
                "transport_ref": "transport_" + digest(client.session)[:32],
                "window": [start, end]})
        return response

    def mcp(self, tool, args, session="bootstrap"):
        return self.mcp_rpc("tools/call", {"name": tool, "arguments": args}, session)

    def data(self, response):
        result = raw_result(response)
        require(result.get("isError") is not True and isinstance(result.get("structuredContent"), dict),
                "required private tool returned an error or lacks structuredContent")
        return result["structuredContent"]

    def refresh_catalog(self, *, expect_change):
        value, checksum = catalog_object(self.catalog_path)
        if not expect_change:
            require(checksum == self.catalog_hash, "catalog changed outside the authorized project call")
        self.catalog, self.catalog_hash = value, checksum
        for project in value["projects"]:
            root = Path(project["root"]).resolve(strict=True)
            self.aliases[str(root)] = "<project-root:" + project["id"] + ">"
        self.workspaces = getattr(self, "workspaces", {})

    def bind_projects(self, data):
        require(data["count"] == len(data["projects"])
                and data["creation_root_count"] == len(data["creation_roots"]),
                "catalog list counts are not exact")
        require([item["id"] for item in data["projects"]] ==
                [item["id"] for item in self.catalog["projects"]],
                "MCP catalog order/identities differ from the persisted private catalog")
        for row, project in zip(data["projects"], self.catalog["projects"]):
            expected = "ws_" + digest(str(Path(project["root"]).resolve(strict=True)))[:24]
            require(row.get("workspace_id") == expected,
                    "direct workspace ID does not derive from the configured canonical root")
            self.workspaces[project["id"]] = expected
            self.aliases[expected] = "<workspace:" + project["id"] + ">"
        roots = [{"id": item["id"], "label": item.get("label", item["id"])}
                 for item in self.catalog["creationRoots"]]
        require(data["creation_roots"] == roots and data["multi_open_supported"] is True
                and data["max_multi_open_projects"] == 12,
                "direct creation-root or multi-open contract changed")

    def bind_fixture(self, project_id, root):
        self.alive()
        private_path(root)
        require(root == self.root or root.parent == self.creation_root,
                "nonce may only touch the private fixture or an owned created project")
        marker = "sp6-admin-binding-" + self.run_id + "-" + self.label + "-" + str(self.manifest["pid"])
        payload = (marker + "\n").encode()
        name = ".sp6-admin-binding-" + self.run_id + ".txt"
        path = root / name
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "wb") as output:
            output.write(payload)
        try:
            data = self.data(self.mcp("read", {"workspace_id": self.workspaces[project_id],
                "path": name, "start_line": 1, "end_line": 1}))
            plain = "\n".join(re.sub(r"^\s*\d+ \| ?", "", line) for line in data["text"].splitlines())
            require(plain == marker and data.get("sha256") == hashlib.sha256(payload).hexdigest(),
                    "MCP workspace is not bound to this side's private nonce")
        finally:
            if not self.unsettled:
                require(path.read_bytes() == payload, "binding nonce changed unexpectedly")
                path.unlink()

    def http(self, method, path, payload=None, *, auth="bearer", origin=None, raw=None,
             content_type="application/json"):
        self.alive()
        require(path.startswith("/") and not path.startswith("//") and "?" not in path,
                "HTTP probe path must be an uncredentialed same-origin path")
        url = self.origin + path
        headers = {"Accept": "application/json"}
        if auth in {"bearer", "mixed-case", "wrong"}:
            token = self.client.credential if auth != "wrong" else "sp6-intentionally-invalid"
            headers["Authorization"] = ("bEaReR   " if auth == "mixed-case" else "Bearer ") + token
        elif auth in {"query", "query-alias"}:
            key = "codexpro_token" if auth == "query" else "token"
            url += "?" + urllib.parse.urlencode({key: self.client.credential})
        else:
            require(auth == "none", "unsupported HTTP auth test")
        if origin is not None:
            headers["Origin"] = self.origin if origin == "same" else (
                self.origin.replace("http://", "https://") if origin == "same-host-https" else origin)
        body = raw
        if payload is not None:
            require(raw is None, "request cannot have both JSON and raw payloads")
            body = source_json(payload).encode()
        if body is not None:
            headers["Content-Type"] = content_type
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        request = urllib.request.Request(url, data=body, headers=headers, method=method)
        start = int(time.time() * 1000)
        self.unsettled = True
        try:
            reply = opener.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            reply = error
        with reply:
            raw_body = reply.read(MAX_HTTP_BYTES + 1)
            require(len(raw_body) <= MAX_HTTP_BYTES, "HTTP response exceeds the evidence bound")
            response_headers = {name: reply.headers.get(name) for name in HEADERS if name in reply.headers}
            end = int(time.time() * 1000)
            content_length = reply.headers.get("Content-Length")
            if content_length is not None and method != "HEAD":
                require(content_length.isdigit() and int(content_length) == len(raw_body),
                        "HTTP Content-Length does not describe the actual body")
            date = reply.headers.get("Date")
            if date:
                observed = email.utils.parsedate_to_datetime(date).timestamp() * 1000
                require(start - 2000 <= observed <= end + 2000, "HTTP Date escapes its request window")
            text = raw_body.decode("utf-8")
            try:
                decoded = json.loads(text)
            except json.JSONDecodeError:
                decoded = text
            response = {"http_status": reply.status, "headers": response_headers, "body": decoded}
        self.unsettled = False
        self.client.assert_no_credential(response)
        self.record({"request": {"method": method, "path": path, "auth": auth,
            "origin": origin, "content_type": content_type if body is not None else None,
            "json": payload, "raw_utf8": raw.decode("utf-8") if raw is not None else None},
            "response": response, "raw_utf8": text, "window_ms": [start, end],
            "derived_http": {"content_length": content_length, "date": date}})
        return response, (start, end)

    def arm_profile(self):
        self.alive()
        require(not self.profile_armed, "profile fixture is already armed")
        info = regular_private_file(self.profile_path, missing=True)
        self.profile_backup = read_small(self.profile_path) if info else None
        self.profile_original_mode = stat.S_IMODE(info.st_mode) if info else None
        self.profile_armed = True
        self.profile_last_hash = hashlib.sha256(self.profile_backup).hexdigest() if info else None
        self.seed_profile(None)

    def seed_profile(self, value):
        self.alive()
        require(self.profile_armed, "profile fixture is not armed")
        if self.profile_path.exists():
            require(hashlib.sha256(read_small(self.profile_path)).hexdigest() == self.profile_last_hash,
                    "profile changed outside a settled lane request")
        else:
            require(self.profile_last_hash is None, "profile disappeared outside the lane")
        if value is None:
            if self.profile_path.exists():
                self.profile_path.unlink()
            self.profile_last_hash = None
        else:
            self.write_profile_bytes(source_json(value).encode() + b"\n", 0o600)
        self.profile_saved_clock = self.profile_saved_label = None

    def write_profile_bytes(self, payload, mode):
        directory = self.profile_path.parent
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        require(directory.resolve(strict=True) == directory
                and directory.is_relative_to(self.home),
                "profile directory contains a symlink or escapes private state")
        temp = directory / (".admin-contract-" + self.run_id + "-" + self.label)
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
        try:
            with os.fdopen(fd, "wb") as output:
                output.write(payload)
                output.flush()
                os.fsync(output.fileno())
            os.chmod(temp, mode)
            os.replace(temp, self.profile_path)
        finally:
            if temp.exists():
                temp.unlink()
        self.profile_last_hash = hashlib.sha256(payload).hexdigest()

    def profile_saved(self, response, window):
        require(response["http_status"] == 200 and response["body"].get("saved") is True,
                "profile save did not succeed")
        data = response["body"]
        require(data["profile_path"] == str(self.profile_path),
                "profile POST did not report this exact derived private path")
        info = regular_private_file(self.profile_path)
        require(stat.S_IMODE(info.st_mode) == 0o600, "saved profile is not mode 0600")
        payload = read_small(self.profile_path)
        state = json.loads(payload)
        require(state["root"] == str(self.default_root) and state["version"] == 1,
                "saved profile root/version do not bind the private default project")
        clock = timestamp(state["updatedAt"])
        require(window[0] - 1000 <= clock <= window[1] + 1000,
                "saved profile timestamp escapes its POST")
        require(state.get("token") == self.client.credential,
                "saved private profile did not preserve its own current bearer credential")
        require("cloudflareToken" not in state, "controlled profile fixture gained a tunnel credential")
        require(data["exists"] is True and data["profile"].get("token") == "<saved>"
                and data["profile"].get("updatedAt") == state["updatedAt"],
                "public profile does not reflect the saved state safely")
        path_keys = {"root", "codexDir", "workDir", "worktreeRoot", "projectsFile",
                     "ngrokConfig", "cloudflareConfig", "cloudflareTokenFile", "allowedRoots"}
        for key, item in state.items():
            if key not in path_keys and key != "token":
                require(data["profile"].get(key) == item,
                        "public saved profile differs from the persisted scalar setting: " + key)
        self.profile_last_hash = hashlib.sha256(payload).hexdigest()
        self.profile_saved_clock = state["updatedAt"]
        self.profile_saved_label = self.current
        safe = copy.deepcopy(state)
        safe["token"] = "<saved>"
        self.record({"profile_state": safe, "mode": "0600",
                     "rule": "credential equality checked only in memory"})
        return safe

    def normalize_profile(self, response):
        value = copy.deepcopy(response)
        data = value["body"]
        if not isinstance(data, dict) or data.get("ok") is not True:
            return self.normalize(value)
        require(type(data.get("runtime", {}).get("port")) is int
                and data["runtime"]["port"] == self.port, "profile runtime port changed after save")
        require(type(data.get("exists")) is bool and data["exists"] == self.profile_path.is_file(),
                "profile exists does not distinguish missing from a valid empty object")
        for key in ("bashMode", "bashTranscript", "codexSessions", "writeMode", "handoffMode",
                    "toolMode", "worktreeMode", "worktreeBaseRef", "maxWorktrees", "toolCards"):
            require(data["runtime"][key] == self.initial_config[key],
                    "saved profile changed current runtime field: " + key)
        data["runtime"]["port"] = "<private-port>"
        for container in (data.get("effective", {}), data.get("profile", {})):
            if "port" in container:
                require(type(container["port"]) is str and container["port"] == str(self.port),
                        "saved/effective profile port lost its source string representation")
                container["port"] = "<private-port>"
        if "updatedAt" in data.get("profile", {}):
            require(data["profile"]["updatedAt"] == self.profile_saved_clock,
                    "profile GET changed the confirmed persisted timestamp")
            data["profile"]["updatedAt"] = "<profile-save:" + self.profile_saved_label + ">"
        runtime = data.get("runtime_connection")
        require(isinstance(runtime, dict), "profile runtime_connection must preserve object/empty-object")
        if runtime:
            require(runtime.get("pid") == self.manifest["pid"],
                    "runtime profile belongs to another process")
            runtime["pid"] = "<private-listening-pid>"
            if "updatedAt" in runtime:
                require(timestamp(runtime["updatedAt"]) <= int(time.time() * 1000) + 1000,
                        "runtime connection timestamp is in the future")
                runtime["updatedAt"] = "<verified-runtime-start-clock>"
        effective = data["effective"]
        if effective.get("work") == "on":
            require(self.label == "reference", "candidate enabled retired work orchestration")
            self.approved.append({"case": self.current, "path": "/body/effective/work",
                                  "reference": "on", "candidate": "off"})
            effective["work"] = "off"
        return self.normalize(value)

    def normalize(self, value):
        return replace_strings(value, self.aliases)

    def normalize_mcp(self, response):
        value = copy.deepcopy(response)
        require(isinstance(value.get("duration_ms"), (int, float))
                and not isinstance(value["duration_ms"], bool)
                and math.isfinite(value["duration_ms"]) and value["duration_ms"] >= 0,
                "client duration is not a finite nonnegative observation")
        value["duration_ms"] = "<client-observed-ms>"
        value["body"]["id"] = "<validated-request-id>"
        return self.normalize(value)

    def config_value(self, data):
        value = copy.deepcopy(data)
        require(type(value["port"]) is int and value["port"] == self.port,
                "server_config port does not match the listener")
        value["port"] = "<private-port>"
        names = value["registeredTools"]
        require(len(names) == len(set(names)) and set(names) == set(self.tools)
                and value["registeredToolCount"] == len(names),
                "server_config registry differs from the actual tools/list")
        removed = [name for name in names if name in RETIRED]
        require(self.label == "reference" or not removed, "candidate exposed retired tools")
        value["registeredTools"] = [name for name in names if name not in RETIRED]
        value["registeredToolCount"] = len(value["registeredTools"])
        if removed:
            self.approved.append({"case": self.current, "path": "/registeredTools",
                                  "removed": removed})
        if isinstance(value.get("work"), dict):
            for field in ("enabled", "management"):
                if value["work"].get(field) is True:
                    require(self.label == "reference", "candidate enabled retired work orchestration")
                    self.approved.append({"case": self.current, "path": "/work/" + field,
                                          "reference": True, "candidate": False})
                    value["work"][field] = False
        return self.normalize(value)

    def normalize_config(self, response):
        value = copy.deepcopy(response)
        data = self.data(value)
        content = value["body"]["result"]["content"]
        require(len(content) == 1 and content[0].get("type") == "text", "server_config content shape changed")
        prefix = "# CodexPro Server Config\n\n"
        require(content[0]["text"].startswith(prefix), "server_config text heading changed")
        parsed = json.loads(content[0]["text"][len(prefix):])
        require(content[0]["text"] == prefix + json.dumps(parsed, ensure_ascii=False, indent=2),
                "server_config text JSON formatting changed")
        require(all(key in data and data[key] == item for key, item in parsed.items()),
                "server_config text disagrees with structured fields")
        content[0]["text"] = prefix + json.dumps(self.config_value(parsed), ensure_ascii=False, indent=2)
        value["body"]["result"]["structuredContent"] = self.config_value(data)
        return self.normalize_mcp(value)

    def normalize_self_test(self, response, window):
        value = copy.deepcopy(response)
        data = self.data(value)
        checks = data["checks"]
        require(isinstance(checks, list) and len({item["name"] for item in checks}) == len(checks),
                "self-test check names are not unique")
        counts = {name: sum(item["status"] == name for item in checks) for name in ("pass", "warn", "fail")}
        require((data["passed"], data["warned"], data["failed"]) ==
                (counts["pass"], counts["warn"], counts["fail"]), "self-test counts do not match checks")
        expected_status = "fail" if counts["fail"] else "warn" if counts["warn"] else "pass"
        require(data["status"] == expected_status, "self-test overall status is inconsistent")
        duration = data["duration_ms"]
        require(type(duration) is int and 0 <= duration <= window[1] - window[0] + 1000,
                "self-test duration exceeds its measured request")
        original_count = len(self.tools)
        retained = sorted(name for name in self.tools if name not in RETIRED)
        for names, count in (("expected_tools", "expected_tool_count"),
                             ("registered_tools", "registered_tool_count")):
            require(data[names] == sorted(self.tools) and data[count] == original_count,
                    "self-test registry does not match independently captured tools/list")
            data[names], data[count] = retained, len(retained)
        tool_mode = data["tool_mode"]
        for item in checks:
            if item["name"] == "tool mode":
                require(item["detail"] == f"{tool_mode}; expected tools: {original_count}",
                        "self-test mode detail count changed")
                item["detail"] = f"{tool_mode}; expected tools: {len(retained)}"
            if item["name"] == "registered tool set":
                require(item["detail"] == f"{original_count} tools registered for {tool_mode} mode",
                        "self-test registered-set diagnostic changed")
                item["detail"] = f"{len(retained)} tools registered for {tool_mode} mode"
        content = value["body"]["result"]["content"]
        require(len(content) == 1 and content[0].get("type") == "text", "self-test content shape changed")
        text = content[0]["text"]
        match = re.search(r"(?m)^Duration: (\d+) ms$", text)
        require(match is not None and 0 <= int(match[1]) <= duration,
                "self-test text duration does not bind the structured duration")
        text = text[:match.start()] + "Duration: <validated-duration-ms> ms" + text[match.end():]
        for old, new in (
            (f"Expected tools: {original_count}", f"Expected tools: {len(retained)}"),
            (f"Registered tools: {original_count}", f"Registered tools: {len(retained)}"),
            (f"{tool_mode}; expected tools: {original_count}", f"{tool_mode}; expected tools: {len(retained)}"),
            (f"{original_count} tools registered for {tool_mode} mode", f"{len(retained)} tools registered for {tool_mode} mode"),
        ):
            require(old in text, "self-test text lost its corresponding registry count")
            text = text.replace(old, new, 1)
        content[0]["text"] = text
        data["duration_ms"] = "<validated-duration-ms>"
        if len(retained) != original_count:
            self.approved.append({"case": self.current, "path": "/self-test-tool-inventory",
                                  "removed": sorted(set(self.tools) & RETIRED)})
        return self.normalize_mcp(value)

    def audit_rows(self, project):
        response = self.mcp("activity_list", {"project_id": project, "mutating_only": True, "limit": 500})
        data = self.data(response)
        require(data.get("enabled") is True and data.get("gap_detected") is False,
                "admin lane requires an enabled, gap-free private audit journal")
        rows = data["actions"]
        require(isinstance(rows, list) and len(rows) <= 500
                and [row["sequence"] for row in rows] == sorted(row["sequence"] for row in rows),
                "audit rows are not a bounded chronological list")
        for index, action in enumerate(rows):
            require(action["project_id"] == project and action["mutating"] is True
                    and re.fullmatch(r"cpa_[a-f0-9]{32}", action["action_id"])
                    and type(action["sequence"]) is int and action["sequence"] > 0
                    and isinstance(action["changed_paths"], list)
                    and type(action["changed_path_count"]) is int,
                    "recent activity receipt has invalid identity or evidence")
            begin, end = timestamp(action["occurred_at"]), timestamp(action["finished_at"])
            require(begin <= end <= int(time.time() * 1000) + 1000
                    and action["duration_ms"] == end - begin,
                    "recent activity receipt timestamps/duration are inconsistent")
            matches = [record for record in self.call_records
                       if record["tool"] == action["tool_name"]
                       and record["request_ref"] == action["request_ref"]
                       and record["transport_ref"] == action.get("transport_session_ref")]
            if matches:
                record = matches[-1]
                require(record["window"][0] - 1000 <= begin <= end <= record["window"][1] + 1000,
                        "recent activity receipt escapes its owning call")
                label = record["label"]
                require(action["actor_ref"] == self.expected_actor,
                        "actor reference does not bind the source direct-connector principal")
                if self.actor is None:
                    self.actor = action["actor_ref"]
                require(action["actor_ref"] == self.actor, "same-token audit actor changed across sessions")
                fingerprint = digest("\0".join((action["actor_ref"],
                    action.get("transport_session_ref") or action["server_session_ref"],
                    action["request_ref"], action["tool_name"])))
                require(action.get("request_fingerprint") == fingerprint,
                        "recent action request fingerprint lost its causal binding")
            else:
                # Prior history is not erased. Its relative order and semantic
                # projection remain comparable; only independently read IDs/time
                # are substituted. A different tool/status/path map still differs.
                semantic = {key: action[key] for key in ("tool_name", "status",
                    "changed_paths", "changed_path_count", "actor_ref") if key in action}
                label = "prior-" + str(index) + "-" + digest(canonical(self.normalize(semantic)))[:16]
            self.actions[action["action_id"]] = (action, label)
            self.aliases[action["action_id"]] = "<action:" + label + ">"
            # Other historical actors are preserved. Only the independently
            # derived identity of this direct connector receives an alias.
            if action["actor_ref"] == self.expected_actor:
                self.aliases[action["actor_ref"]] = "<validated-actor>"
        return rows, data

    def normalize_change(self, value):
        if value is None:
            return None
        action, label = self.actions[value["action_id"]]
        require(value["at"] == action["finished_at"], "latest-change time lost its receipt binding")
        out = copy.deepcopy(value)
        out["at"] = "<finished:" + label + ">"
        return out

    def normalize_projects(self, response):
        value = copy.deepcopy(response)
        data = self.data(value)
        self.bind_projects(data)
        for project in data["projects"]:
            rows, _ = self.audit_rows(project["id"])
            require(project["last_recorded_project_change"] == last_change(rows),
                    "project latest-change projection differs from retained receipts")
            project["last_recorded_project_change"] = self.normalize_change(project["last_recorded_project_change"])
        return self.normalize_mcp(value)

    def open_and_normalize(self, args, session="catalog", tool="open_workspace"):
        before = self.data(self.mcp("activity_status", {}))
        response = self.mcp(tool, args, session)
        if raw_result(response).get("isError") is True:
            return self.normalize_mcp(response)
        value = copy.deepcopy(response)
        data = self.data(value)
        after = self.data(self.mcp("activity_status", {}))
        if "recent_activity" in data:
            project = data.get("project_id")
            require(isinstance(project, str), "recent activity is not associated with one project")
            rows, listed = self.audit_rows(project)
            brief = data["recent_activity"]
            require(brief["enabled"] is True
                    and brief["last_recorded_project_change"] == last_change(rows)
                    and brief["latest_recorded_activity"] == (rows[-1]["finished_at"] if rows else None)
                    and brief["actions"] == recent_actions(rows, data.get("workspace_id"))
                    and brief["coverage"] == COVERAGE and brief["has_more"] is False,
                    "workspace recent-activity projection differs from source receipt rules")
            # The four activity tools are explicitly exempt from audit in the
            # release wrapper. Only this open call advances the durable cursor.
            require(before["latest_sequence"] == brief["latest_sequence"]
                    and brief["next_sequence"] == brief["latest_sequence"]
                    and after["latest_sequence"] == brief["latest_sequence"] + 1,
                    "recent activity watermark does not lie between its two owned audit receipts")
            require(before["retained_from_sequence"] == brief["earliest_sequence"]
                    <= after["retained_from_sequence"], "recent activity retention floor escapes its window")
            require(brief["gap_detected"] is False and "warning" not in brief,
                    "gap-free private journal introduced a recent-activity warning")
            for row in brief["actions"]:
                action, label = self.actions[row["action_id"]]
                require(row["sequence"] == action["sequence"], "recent sequence does not bind its receipt")
                row["sequence"] = "<sequence:" + label + ">"
                row["at"] = "<finished:" + label + ">"
            changed = brief["last_recorded_project_change"]
            if changed is not None:
                old_at = changed["at"]
                brief["last_recorded_project_change"] = self.normalize_change(changed)
                expected = "\n\nLast recorded project change: " + old_at + ". " + COVERAGE
                texts = [item for item in value["body"]["result"]["content"] if item.get("type") == "text"]
                require(texts and texts[0]["text"].endswith(expected),
                        "workspace briefing text does not match its latest-change projection")
                texts[0]["text"] = texts[0]["text"][:-len(expected)] + (
                    "\n\nLast recorded project change: " + brief["last_recorded_project_change"]["at"] +
                    ". " + COVERAGE)
            if rows:
                brief["latest_recorded_activity"] = "<finished:" + self.actions[rows[-1]["action_id"]][1] + ">"
            for key in ("latest_sequence", "next_sequence"):
                brief[key] = "<validated-call-watermark>"
            brief["earliest_sequence"] = "<validated-retained-floor>"
            self.normalizations.append({"case": self.current, "rule":
                "recent IDs/times/sequence from exact source projection and adjacent audit watermarks"})
        return self.normalize_mcp(value)

    def list_workspaces(self, session, expected_projects, selected_project):
        response = self.mcp("list_workspaces", {}, session)
        value = copy.deepcopy(response)
        data = self.data(value)
        require([item["id"] for item in data["workspaces"]] ==
                [self.workspaces[project] for project in expected_projects]
                and data["count"] == len(expected_projects)
                and data["selected_workspace_id"] == self.workspaces[selected_project],
                "workspace session membership/order/selection leaked across sessions")
        aliases = {}
        for item in data["workspaces"]:
            opened = timestamp(item["openedAt"])
            require(self.session_windows[session] - 1000 <= opened <= int(time.time() * 1000) + 1000,
                    "workspace openedAt escapes its owning session")
            aliases[item["openedAt"]] = "<opened:" + item["id"] + ">"
            item["openedAt"] = aliases[item["openedAt"]]
        for item in value["body"]["result"]["content"]:
            if item.get("type") == "text":
                # Bind each timestamp to its own workspace line even if two
                # workspaces were opened in the same millisecond.
                lines = item["text"].split("\n")
                for index, row in enumerate(data["workspaces"]):
                    require(lines[index].startswith("- " + row["id"] + " "),
                            "workspace list text lost its corresponding identity")
                    lines[index], count = re.subn(r"\(opened [^)]+\)$",
                        "(opened " + row["openedAt"] + ")", lines[index])
                    require(count == 1, "workspace list text lacks openedAt")
                item["text"] = "\n".join(lines)
        return self.normalize_mcp(value)

    def create_project(self, project_id, source):
        self.refresh_catalog(expect_change=False)
        before_catalog = copy.deepcopy(self.catalog)
        directory = project_id
        root = self.creation_root / directory
        require(not os.path.lexists(root), "new project path already exists")
        args = {"project_id": project_id, "parent_id": self.creation_id,
                "label": "SP-6 " + source + " fixture", "directory": directory,
                "source": source, "max_worktrees": 7}
        if source == "git":
            args["initial_branch"] = "main"
        start = int(time.time() * 1000)
        response = self.mcp("create_project", args, "catalog")
        end = int(time.time() * 1000)
        data = self.data(response)
        require(root.is_dir() and root.resolve(strict=True) == root, "created project root escaped its bound")
        self.refresh_catalog(expect_change=True)
        added = [item for item in self.catalog["projects"] if item["id"] == project_id]
        require(len(added) == 1 and Path(added[0]["root"]).resolve(strict=True) == root,
                "successful project creation was not persisted at its owned path")
        preserved = copy.deepcopy(self.catalog)
        preserved["projects"] = [item for item in preserved["projects"] if item["id"] != project_id]
        require(preserved == before_catalog and self.catalog["projects"][-1]["id"] == project_id,
                "project creation changed preexisting catalog entries or append order")
        require(data["project_id"] == project_id and data["parent_id"] == self.creation_id
                and data["source"] == source and data["cloned"] is False
                and data["git_initialized"] is (source == "git")
                and data["initial_commit_created"] is (source == "git")
                and data["next_tool"] == "open_workspace",
                "created project output does not describe the actual operation")
        self.created[project_id] = {"root": str(root), "source": source}
        listed = self.mcp("list_projects", {}, "catalog")
        self.bind_projects(self.data(listed))
        self.bind_fixture(project_id, root)
        if source == "empty":
            require(not list(root.iterdir()), "source=empty created unexpected files")
        else:
            self.bind_initial_commit(root, project_id, (start, end))
        return self.normalize_mcp(response)

    def git(self, root, *arguments):
        require(root.parent == self.creation_root and root.is_dir(), "Git probe root is not lane-owned")
        result = subprocess.run(["nice", "-n", "10", "git", "-C", str(root), *arguments],
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=10, env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"})
        require(result.returncode == 0 and len(result.stdout) <= 128 * 1024,
                "bounded private Git observation failed")
        return result.stdout

    def bind_initial_commit(self, root, project_id, window):
        head = self.git(root, "rev-parse", "HEAD").decode().strip()
        require(re.fullmatch(r"[a-f0-9]{40}", head), "initial Git commit is not SHA-1")
        commit = self.git(root, "cat-file", "-p", head)
        require(hashlib.sha1(b"commit " + str(len(commit)).encode() + b"\0" + commit).hexdigest() == head,
                "initial Git commit hash does not match its object")
        text = commit.decode("utf-8")
        require(text.startswith("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n")
                and not re.search(r"(?m)^parent ", text), "Git initialization did not create one empty root commit")
        for role in ("author", "committer"):
            match = re.search(r"(?m)^" + role + r" (.*) (\d+) ([+-]\d{4})$", text)
            require(match is not None and window[0] - 2000 <= int(match[2]) * 1000 <= window[1] + 1000,
                    "initial Git identity/time does not bind its create_project call")
            text = text[:match.start(2)] + "<validated-creation-time>" + text[match.end(2):]
        self.created[project_id]["commit_object"] = text
        self.created[project_id]["head"] = head
        for length in (40, 12, 7):
            self.aliases[head[:length]] = "<initial-commit:" + project_id + ">"
        self.record({"git_creation": {"project_id": project_id, "head": head,
                     "normalized_commit_object": text}})

    def self_test(self, project_id, *, probes):
        root = Path(self.created[project_id]["root"])
        path = root / ".ai-bridge" / "codexpro-self-test.md"
        require(not path.exists(), "self-test output already exists before this probe")
        start = int(time.time() * 1000)
        response = self.mcp("codexpro_self_test", {
            "workspace_id": self.workspaces[project_id], "include_global_skills": False,
            "write_probe": probes, "bash_probe": probes, "pro_context_probe": probes}, "catalog")
        end = int(time.time() * 1000)
        data = self.data(response)
        if probes:
            require(data["files_touched"] == [".ai-bridge/codexpro-self-test.md"] and path.is_file(),
                    "self-test did not restrict its write to the documented probe file")
            content = read_small(path).decode("utf-8")
            lines = content.split("\n")
            require(len(lines) == 6 and lines[:2] == ["# CodexPro Self Test", ""]
                    and lines[3:] == ["Workspace: " + str(root), "marker: after", ""]
                    and lines[2].startswith("Updated: "), "self-test write/edit probe content changed")
            at = lines[2][len("Updated: "):]
            require(start - 1000 <= timestamp(at) <= end + 1000, "self-test file timestamp escapes the probe")
            self.record({"self_test_file": {"path": ".ai-bridge/codexpro-self-test.md",
                "content": self.normalize(content.replace(at, "<validated-probe-clock>")),
                "sha256": hashlib.sha256(content.encode()).hexdigest()}})
            require(not (root / ".ai-bridge" / "pro-context.md").exists(),
                    "selected-only diagnostic unexpectedly published a handoff artifact")
        else:
            require(data["files_touched"] == [] and not path.exists(),
                    "default self-test mutated the repository")
        return self.normalize_self_test(response, (start, end))

    def restore(self):
        result = {"profile_restored": False, "created_projects": self.created}
        if self.unsettled:
            result["blocked"] = "unsettled request; private profile and catalog state retained for inspection"
            return result
        self.alive()
        if self.profile_armed:
            if self.profile_path.exists():
                require(hashlib.sha256(read_small(self.profile_path)).hexdigest() == self.profile_last_hash,
                        "refusing to restore over a concurrent private profile change")
            else:
                require(self.profile_last_hash is None, "private profile unexpectedly disappeared")
            if self.profile_backup is None:
                if self.profile_path.exists():
                    self.profile_path.unlink()
                self.profile_last_hash = None
            else:
                self.write_profile_bytes(self.profile_backup, self.profile_original_mode)
            result["profile_restored"] = True
            self.profile_armed = False
        for client in self.clients.values():
            client.close()
        self.lock.close()
        return result


class Lane:
    def __init__(self, sides, evidence):
        self.sides, self.evidence = sides, evidence
        self.cases = []
        for side in sides:
            side.evidence = evidence
            for record in side.early_evidence:
                evidence.append(record)
            side.early_evidence.clear()

    def check(self, label, operation, *, approved=False):
        values, errors = [], []
        for side in self.sides:
            side.current = label
            side.approved = []
            try:
                values.append(operation(side))
                errors.append(None)
            except Exception as error:
                values.append(None)
                errors.append(safe_error(error, side.clients.values()))
        diff = difference(*values) if not any(errors) else None
        deltas = [item for side in self.sides for item in side.approved]
        status = "fail" if any(errors) or diff else "approved_delta" if approved or deltas else "pass"
        record = {"id": label, "status": status, "errors": errors, "diff": diff,
                  "approved_deltas": deltas,
                  "approval_commit": APPROVAL_COMMIT if approved or deltas else None}
        self.evidence.append({"case": label, "comparison": record, "normalized": values})
        self.cases.append(record)
        return not any(errors)

    def http(self, label, method, path, *, status=200, payload=None, auth="bearer", origin=None,
             raw=None, profile=False):
        def operation(side):
            response, window = side.http(method, path, payload(side) if callable(payload) else payload,
                                         auth=auth, origin=origin, raw=raw)
            if method == "POST" and path == "/admin/profile" and response["http_status"] == 200:
                side.profile_saved(response, window)
            require(response["http_status"] == status, "HTTP status differs from the fixture expectation")
            return side.normalize_profile(response) if profile else side.normalize(response)
        return self.check(label, operation)


def run_lane(lane, run_id):
    sides = lane.sides
    lane.check("ADMIN-CONFIG", lambda side: side.normalize_config(side.initial_config_response))
    lane.check("CATALOG-LIST-INITIAL", lambda side: side.normalize_projects(side.mcp("list_projects", {})))
    for label, auth, status in (
        ("AUTH-MISSING", "none", 401), ("AUTH-WRONG", "wrong", 401),
        ("AUTH-BEARER", "bearer", 200), ("AUTH-BEARER-CASE", "mixed-case", 200),
        ("AUTH-QUERY", "query", 200), ("AUTH-QUERY-ALIAS", "query-alias", 200),
    ):
        lane.http(label, "GET", "/healthz", auth=auth, status=status)
    lane.http("PROFILE-METHOD", "PUT", "/admin/profile", status=405)
    for side in sides:
        side.arm_profile()
    lane.http("PROFILE-MISSING", "GET", "/admin/profile", profile=True)
    for side in sides:
        side.seed_profile({})
    lane.http("PROFILE-EMPTY-OBJECT", "GET", "/admin/profile", profile=True)
    for label, payload, status, origin, raw in (
        ("PROFILE-ORIGIN-DENIED", {}, 403, "https://other.example.invalid", None),
        ("PROFILE-ORIGIN-MALFORMED", {}, 403, "not a URL", None),
        ("PROFILE-INVALID-JSON", None, 400, "same", b'{"broken":'),
        ("PROFILE-UNKNOWN-KEY", {"extra_probe": True}, 400, "same", None),
        ("PROFILE-BOUNDS", {"port": 0, "maxWorktrees": 513}, 400, "same", None),
        ("PROFILE-WRONG-TYPE", {"requireBashSession": "true"}, 400, "same", None),
        ("PROFILE-INVALID-ENUM", {"toolMode": "unknown"}, 400, "same", None),
        ("PROFILE-HOSTNAME-REQUIRED", {"tunnel": "ngrok", "hostname": ""}, 400, "same", None),
        ("PROFILE-OVERSIZE", None, 413, "same", b'{"hostname":"' + b"x" * 32768 + b'"}'),
    ):
        lane.http(label, "POST", "/admin/profile", payload=payload, status=status, origin=origin, raw=raw)
    base_patch = lambda side: {
        "port": str(side.port), "mode": "agent", "tunnel": "none", "bash": "full",
        "write": "workspace", "handoffMode": "off", "codexSessions": "off", "work": "off",
        "worktreeMode": "off", "projectsFile": str(side.catalog_path),
        "workDir": str(side.work_dir), "worktreeRoot": str(side.home / "worktrees"),
        "codexDir": str(side.codex_dir), "maxWorktrees": "24",
        "widgetDomain": "https://widgets.example.invalid/", "bashTranscript": "full",
    }
    for tier in ("minimal", "standard", "full"):
        lane.http("PROFILE-SAVE-" + tier.upper(), "POST", "/admin/profile",
                  payload=lambda side, tier=tier: {**base_patch(side), "toolMode": tier},
                  origin="same-host-https", profile=True)
        lane.http("PROFILE-GET-" + tier.upper(), "GET", "/admin/profile", profile=True)
        lane.check("PROFILE-RUNTIME-UNCHANGED-" + tier.upper(), lambda side:
                   unchanged_config(side))
    prefix = "sp6-admin-" + run_id
    empty, git = prefix + "-empty", prefix + "-git"
    for label, args in (
        ("CATALOG-INVALID-ID", {"project_id": "Invalid-ID", "parent_id": "$creation", "source": "empty"}),
        ("CATALOG-UNKNOWN-PARENT", {"project_id": prefix + "-bad", "parent_id": "sp6-no-such-parent", "source": "empty"}),
        ("CATALOG-TRAVERSAL", {"project_id": prefix + "-bad", "parent_id": "$creation", "directory": "../escape", "source": "empty"}),
    ):
        def negative(side, args=args):
            side.refresh_catalog(expect_change=False)
            before = sorted(path.name for path in side.creation_root.iterdir())
            resolved = {key: side.creation_id if value == "$creation" else value for key, value in args.items()}
            response = side.mcp("create_project", resolved, "catalog")
            require(raw_result(response).get("isError") is True, "invalid project request was accepted")
            side.refresh_catalog(expect_change=False)
            require(sorted(path.name for path in side.creation_root.iterdir()) == before,
                    "invalid project request changed its creation root")
            return side.normalize_mcp(response)
        lane.check(label, negative)
    empty_ok = lane.check("CATALOG-CREATE-EMPTY", lambda side: side.create_project(empty, "empty"))
    git_ok = lane.check("CATALOG-CREATE-GIT", lambda side: side.create_project(git, "git"))
    if empty_ok and git_ok and all(empty in side.created and git in side.created for side in sides):
        lane.check("CATALOG-CREATE-GIT-OBJECT", lambda side: side.created[git]["commit_object"])
        lane.check("CATALOG-LIST-AFTER-CREATE", lambda side: side.normalize_projects(side.mcp("list_projects", {})))
        lane.check("CATALOG-DUPLICATE", lambda side: duplicate_project(side, empty))
        lane.check("OPEN-FIRST", lambda side: side.open_and_normalize({"project_id": empty}))
        lane.check("OPEN-REPEATED", lambda side: side.open_and_normalize({"project_id": empty}))
        lane.check("OPEN-SECOND-SESSION", lambda side: side.open_and_normalize({"project_id": empty}, "other"))
        lane.check("OPEN-MULTI-DEDUP", lambda side: side.open_and_normalize(
            {"project_ids": [empty, git, empty], "include_skills": False}))
        lane.check("OPEN-MULTI-TREE-BUDGET", lambda side: side.open_and_normalize(
            {"project_ids": [git, empty], "include_tree": True, "max_entries": 3, "max_depth": 2}))
        lane.check("OPEN-CONFLICT", lambda side: expected_tool_error(side, "open_workspace",
            {"project_id": empty, "project_ids": [git]}))
        lane.check("OPEN-UNKNOWN", lambda side: expected_tool_error(side, "open_workspace",
            {"project_id": prefix + "-unknown"}))
        lane.check("SELF-TEST-DEFAULT", lambda side: side.self_test(git, probes=False))
        lane.check("SELF-TEST-PROBES", lambda side: side.self_test(git, probes=True))
        lane.check("OPEN-AFTER-WRITE-RECEIPT", lambda side: side.open_and_normalize(
            {"project_id": git, "include_tree": False}))
        lane.check("CATALOG-LIST-NEW-SESSION", lambda side: side.normalize_projects(
            side.mcp("list_projects", {}, "other")))
        lane.check("CATALOG-PERSISTED", lambda side: side.normalize(side.catalog))
        if all("list_workspaces" in side.tools for side in sides):
            lane.check("WORKSPACES-SESSION-CATALOG", lambda side:
                side.list_workspaces("catalog", [empty, git], git))
            lane.check("WORKSPACES-SESSION-OTHER", lambda side:
                side.list_workspaces("other", [empty], empty))
    else:
        lane.cases.append({"id": "CATALOG-DEPENDENT-WORKFLOWS", "status": "blocked",
                           "reason": "one or both private creation prerequisites failed"})
    for tool in ("list_workspaces", "open_current_workspace"):
        available = [tool in side.tools for side in sides]
        if tool == "open_current_workspace" and all(available):
            lane.check("CONDITIONAL-OPEN-CURRENT", lambda side: side.open_and_normalize(
                {"include_tree": False, "include_skills": False, "include_global_skills": False},
                "current", tool="open_current_workspace"))
        elif any(available) and not all(available):
            lane.cases.append({"id": "CONDITIONAL-" + tool, "status": "pending",
                "reason": "descriptor is not present on both sides; inventory comparison reports the mismatch",
                "available": available})
    lane.cases.append({"id": "MANAGED-WORKSPACE-BUSINESS-CALLS", "status": "pending",
        "reason": "separate root-owned MCP/safe TypeScript versus full-permission Go pair required"})
    lane.cases.append({"id": "PROFILE-ACTUAL-NEXT-START", "status": "pending",
        "reason": "this running-process lane proves persistence and unchanged runtime; parent owns restart proof"})


def unchanged_config(side):
    response = side.mcp("server_config", {}, "profile-check")
    initial = side.normalize_config(side.initial_config_response)
    current = side.normalize_config(response)
    require(initial == current, "saving a private profile reconfigured the running server")
    return current


def expected_tool_error(side, tool, args):
    response = side.mcp(tool, args, "catalog")
    require(raw_result(response).get("isError") is True, "negative workflow unexpectedly succeeded")
    return side.normalize_mcp(response)


def duplicate_project(side, project):
    side.refresh_catalog(expect_change=False)
    value = expected_tool_error(side, "create_project", {
        "project_id": project, "parent_id": side.creation_id, "source": "empty"})
    side.refresh_catalog(expect_change=False)
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", default="http://127.0.0.1:7211/mcp")
    parser.add_argument("--candidate", default="http://127.0.0.1:7210/mcp")
    for side in ("reference", "candidate"):
        parser.add_argument("--" + side + "-token", required=True)
        parser.add_argument("--" + side + "-manifest", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--raw-output")
    parser.add_argument("--run-id", default=uuid.uuid4().hex[:12])
    args = parser.parse_args()
    require(os.nice(0) >= 10, "run the bounded differential lane under nice -n 10")
    require(re.fullmatch(r"[a-f0-9]{8,16}", args.run_id), "run-id must be 8–16 lowercase hex characters")
    output = artifact_path(args.output)
    raw_path = artifact_path(args.raw_output or str(output) + ".jsonl.gz")
    require(not output.exists() and not raw_path.exists(), "evidence paths must be new")
    report = {"schema_version": 1, "lane": "admin-catalog-direct", "source_commit": SOURCE_COMMIT,
              "started_at": iso_now(), "run_id": args.run_id, "approval_commit": APPROVAL_COMMIT,
              "cases": [], "cleanup": {}, "limitations": [
                  "Default direct/full-permission standard-or-full catalog pair only.",
                  "No actual next-start application, managed business-call pair, tunnel launcher, or UI pixel parity claim.",
                  "HTTP framing length and Date validated independently; listed semantic headers and decoded bodies compared.",
                  "Created private projects are retained; catalog deletion behind an active manager is deliberately avoided.",
              ]}
    sides, evidence = [], None
    try:
        for label in ("reference", "candidate"):
            sides.append(Side(label, getattr(args, label), getattr(args, label + "_token"),
                              getattr(args, label + "_manifest"), args.run_id))
        require(sides[0].root != sides[1].root and sides[0].state != sides[1].state
                and sides[0].home != sides[1].home and sides[0].catalog_path != sides[1].catalog_path
                and sides[0].creation_root != sides[1].creation_root,
                "reference and candidate must own distinct private paths")
        evidence = Evidence(raw_path, lambda: [client for side in sides for client in side.clients.values()])
        lane = Lane(sides, evidence)
        report["cases"] = lane.cases
        report["inventory"] = compare_inventory(sides[0].inventory_response, sides[1].inventory_response)
        evidence.append({"inventory": [side.inventory_response for side in sides],
                         "comparison": report["inventory"]})
        run_lane(lane, args.run_id)
        report["cases"] = lane.cases
        report["normalizations"] = {side.label: side.normalizations for side in sides}
    except Exception as error:
        report["fatal"] = safe_error(error, [client for side in sides for client in side.clients.values()])
    finally:
        for side in sides:
            try:
                report["cleanup"][side.label] = side.restore()
            except Exception as error:
                report["cleanup"][side.label] = {"error": safe_error(error, side.clients.values())}
        report["finished_at"] = iso_now()
        report["counts"] = {status: sum(case.get("status") == status for case in report["cases"])
                            for status in ("pass", "approved_delta", "fail", "blocked", "pending")}
        cleanup_failed = any(value.get("error") or value.get("blocked")
                             for value in report["cleanup"].values())
        report["ok"] = (not report.get("fatal") and not report["counts"]["fail"]
                        and not report["counts"]["blocked"] and not cleanup_failed
                        and report.get("inventory", {}).get("status") != "fail")
        report["stage4_complete"] = False
        for side in sides:
            for client in side.clients.values():
                client.assert_no_credential(report)
        if evidence:
            evidence.close()
            report["raw_evidence"] = {"path": str(raw_path), "records": evidence.records, "bytes": evidence.bytes}
        output.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as target:
            json.dump(report, target, indent=2, ensure_ascii=False, allow_nan=False)
            target.write("\n")
    print(json.dumps({"lane": report["lane"], "ok": report["ok"], "counts": report["counts"],
                      "fatal": report.get("fatal"), "output": str(output)}))
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
