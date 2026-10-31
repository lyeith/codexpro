#!/usr/bin/env python3
"""Own a bounded private TS-safe/Go-full managed-workspace differential pair.

Refuses occupied 7210/7211 before creating state. Never uses a service manager,
adopts an existing process, or sends Bash/job commands. Run under nice10 with an
external timeout; only this driver's new Popen children are stopped.
"""
from __future__ import annotations

import argparse
import base64
import copy
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import sqlite3
import stat
import subprocess
import time
import urllib.parse

from contract_suite import Client, SOURCE_COMMIT, raw_result
from paired_contract_suite import (
    APPROVAL_COMMIT, RETIRED, ContractFailure, Evidence, artifact_path, canonical,
    difference, digest, iso_now, private_path, process_start_ticks, require,
    safe_error, validate_launch,
)
from admin_catalog_contract_suite import (
    Side as AuditSide, COVERAGE, catalog_object, last_change, recent_actions,
    read_small, regular_private_file, replace_strings, timestamp,
    process_path_binding, private_role_aliases,
)
from process_contract_suite import private_http

WORKSPACE_TOOLS = {"create_workspace", "open_workspace", "release_workspace", "remove_workspace"}
FIXTURE_TOOLS = {"list_projects", "server_config", "activity_list", "activity_status", "read", "write", "apply_patch"}
FILE_NAME = "managed-proof.txt"
FILE_BYTES = b"Managed contract proof.\nSecond line.\n"
CASE_IDS = [
    "MANAGED-01-catalog-and-descriptors", "MANAGED-02-required-workspace-id",
    "MANAGED-03-create", "MANAGED-04-idempotency-and-cross-session",
    "MANAGED-05-conflicting-base", "MANAGED-06-project-quota",
    "MANAGED-07-open-across-sessions", "MANAGED-08-write-and-read",
    "MANAGED-09-release-and-reopen", "MANAGED-10-dirty-remove-refused",
    "MANAGED-11-delete-owned-proof", "MANAGED-12-clean-remove-preserves-branch",
]


def write_exclusive(path, raw, mode=0o600):
    if isinstance(raw, str):
        raw = raw.encode()
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def durable_json(path, value):
    write_exclusive(path, json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def assert_free_ports(ports=(7210, 7211)):
    reservations = []
    try:
        for port in ports:
            listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            reservations.append(listener)
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                listener.bind(("127.0.0.1", port))
            except OSError as error:
                raise ContractFailure(f"port {port} is occupied; the managed lane will not stop or replace its listener") from error
    finally:
        for listener in reservations:
            listener.close()


def command(arguments, *, cwd=None, env=None, timeout=8, limit=262144):
    completed = subprocess.run(arguments, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
    require(len(completed.stdout) <= limit and len(completed.stderr) <= limit,
            "private Git/fixture command exceeded its output bound")
    require(completed.returncode == 0, "private fixture command failed: " + Path(arguments[0]).name)
    return completed.stdout.decode("utf-8")


def sanitized_environment():
    allowed = {"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "LC_CTYPE",
               "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "TMPDIR"}
    env = {key: value for key, value in os.environ.items() if key in allowed}
    env.update({
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_TERMINAL_PROMPT": "0", "GIT_AUTHOR_NAME": "SP6 managed fixture",
        "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "SP6 managed fixture",
        "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
        "GIT_AUTHOR_DATE": "2025-01-01T00:00:00+00:00",
        "GIT_COMMITTER_DATE": "2025-01-01T00:00:00+00:00",
    })
    return env


def parse_worktrees(raw):
    result, current = [], {}
    for field in raw.split("\0"):
        if not field:
            if current:
                require("worktree" in current and "HEAD" in current, "incomplete private Git worktree record")
                result.append(current); current = {}
            continue
        key, separator, value = field.partition(" ")
        require(key not in current, "duplicate private Git worktree field")
        current[key] = value if separator else True
    require(not current, "unterminated private Git worktree record")
    return result


def process_group_members(group):
    members = {}
    examined = 0
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        examined += 1
        require(examined <= 20000, "process inventory exceeds the cleanup bound")
        try:
            fields = (entry / "stat").read_text().rsplit(")", 1)[1].split()
            if int(fields[2]) == group and int(fields[3]) == group and fields[0] not in {"Z", "X"}:
                require(entry.stat().st_uid == os.getuid(), "owned process group changed user identity")
                members[int(entry.name)] = int(fields[19])
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
    return members


def derive_lease_identity(repository, managed, workspace, principal, retry_key):
    require(isinstance(workspace, str) and re.fullmatch(r"wt_[A-Za-z0-9_-]{32}", workspace),
            "workspace ID does not match source 24-byte base64url format")
    require(len(base64.urlsafe_b64decode(workspace[3:] + "==")) == 24, "workspace ID payload is not 24 bytes")
    common = str((repository / ".git").resolve(strict=True))
    repository_id = digest(common + "\0")[:24]  # The fixture is exactly the Git top level.
    internal = digest(workspace)
    checkout = managed / "repositories" / repository_id / "checkouts" / internal[:32]
    return {
        "version": 2, "workspaceId": workspace, "projectId": "contract",
        "repositoryId": repository_id, "repositoryCommonDir": common,
        "checkoutRoot": str(checkout), "workspaceRoot": str(checkout),
        "branch": "codexpro/mcp/" + internal[:24],
        "ownerId": principal, "creationKeyHash": digest("\0".join(("contract", repository_id, principal, retry_key))),
    }


def normalize_permission_summary(value, tool, mode, differences, case):
    data = value["body"]["result"]["structuredContent"]
    if tool == "open_workspace":
        require(data.get("bash_mode") == mode, "workspace permission mode differs from its declared fixture")
        data["bash_mode"] = "<declared-permission-fixture>"
        differences.append({"case": case, "path": "/body/result/structuredContent/bash_mode", "observed": mode})
    texts = [item for item in value["body"]["result"]["content"] if item.get("type") == "text"]
    require(len(texts) == 1, "workspace summary must have one text block")
    old = "\nBash mode: " + mode + "\n"
    require(texts[0]["text"].count(old) == 1, "workspace summary permission line is missing or duplicated")
    texts[0]["text"] = texts[0]["text"].replace(old, "\nBash mode: <declared-permission-fixture>\n", 1)
    differences.append({"case": case, "path": "/body/result/content/0/text/Bash mode", "observed": mode})


def managed_instruction_aliases(manifest):
    """Alias only this owned process's explicit private instruction directory."""
    require(process_start_ticks(manifest["pid"]) == manifest["process_start_ticks"],
            "managed process changed before instruction-path binding")
    roles = process_path_binding(manifest)
    require(process_start_ticks(manifest["pid"]) == manifest["process_start_ticks"],
            "managed process changed during instruction-path binding")
    expected = private_path(Path(manifest["state_root"]) / "empty-codex")
    require(expected.is_dir() and roles["codex_dir"] == str(expected),
            "managed instruction directory differs from the owned launch fixture")
    # No whole [home] prefix is admitted. The helper includes only the exact
    # absolute directory and its source [home]/relative spelling when applicable.
    aliases = private_role_aliases({str(expected): "<private-codex-dir>"}, roles["account_home"])
    return aliases, str(expected)


class OwnedServer:
    def __init__(self, pair, label):
        self.pair, self.label = pair, label
        self.port = 7211 if label == "reference" else 7210
        self.mode = "safe" if label == "reference" else "full"
        self.state = pair.parent / label
        self.home, self.root, self.managed = self.state / "home", self.state / "fixture", self.state / "managed"
        self.token, self.catalog = self.state / "http-token", self.state / "projects.json"
        self.child, self.client, self.log, self.witness = None, None, None, None
        self.state.mkdir(mode=0o700)
        for name in ("home", "fixture", "managed", "jobs", "audit", "work", "empty-codex"):
            (self.state / name).mkdir(mode=0o700)
        write_exclusive(self.token, secrets.token_urlsafe(48) + "\n")
        write_exclusive(self.state / "limits.env", "")
        self.environment = sanitized_environment()
        git = lambda *args: command(["git", "-C", str(self.root), "-c", "core.hooksPath=/dev/null",
                                    "-c", "commit.gpgSign=false", *args], env=self.environment).strip()
        git("-c", "init.templateDir=", "init", "-q", "-b", "main")
        git("config", "core.hooksPath", os.devnull)
        write_exclusive(self.root / "README.md", "# Managed contract fixture\n")
        git("add", "README.md"); git("commit", "-qm", "test: deterministic managed fixture")
        self.main_commit = git("rev-parse", "HEAD")
        git("checkout", "-qb", "alternate")
        write_exclusive(self.root / "ALTERNATE.txt", "Distinct alternate base.\n")
        git("add", "ALTERNATE.txt"); git("commit", "-qm", "test: alternate managed base")
        self.alternate_commit = git("rev-parse", "HEAD")
        git("checkout", "-q", "main")
        require(self.main_commit != self.alternate_commit, "conflicting-base fixture is not distinct")
        durable_json(self.catalog, {"version": 1, "defaultProject": "contract",
            "projects": [{"id": "contract", "label": "Managed contract fixture", "root": str(self.root),
                          "baseRef": "main", "maxWorktrees": 1}], "creationRoots": []})
        self.environment.update({
            "CODEXPRO_ENV_FILE": str(self.state / "limits.env"),
            "CODEXPRO_JOB_SCOPES": "1", "CODEXPRO_AUDIT_RETAIN_ACTIONS": "1000",
            "CODEXPRO_MAX_WORKTREES": "8",
        })
        if label == "reference":
            self.environment.update({
                "CODEXPRO_HOME": str(self.home), "CODEXPRO_PROJECTS_FILE": str(self.catalog),
                "CODEXPRO_ROOT": str(self.root), "CODEXPRO_WORKTREE_ROOT": str(self.managed),
                "CODEXPRO_WORKTREE_BASE": "main", "CODEXPRO_JOBS_DIR": str(self.state / "jobs"),
                "CODEXPRO_AUDIT_LOG": str(self.state / "audit" / "tool-calls.jsonl"),
                "CODEXPRO_WORK_DIR": str(self.state / "work"),
                "CODEXPRO_CODEX_DIR": str(self.state / "empty-codex"),
                "CODEXPRO_HOST": "127.0.0.1", "CODEXPRO_PORT": str(self.port),
                "CODEXPRO_AUTH_MODE": "static-token", "CODEXPRO_REQUIRE_HTTP_TOKEN": "1",
                "CODEXPRO_BASH_MODE": "safe", "CODEXPRO_WRITE_MODE": "workspace",
                "CODEXPRO_TOOL_MODE": "standard", "CODEXPRO_WORKTREE_MODE": "mcp",
                "CODEXPRO_WORK_MODE": "on", "CODEXPRO_WORK_MANAGEMENT": "true",
                "CODEXPRO_HANDOFF_MODE": "off", "CODEXPRO_CODEX_SESSIONS": "off",
                "CODEXPRO_INHERIT_ENV": "0", "CODEXPRO_ANALYSIS": "true",
                "CODEXPRO_AUDIT_MODE": "metadata", "CODEXPRO_DEBUG_ACTIVITY": "metadata",
                "CODEXPRO_MAX_HTTP_SESSIONS": "6", "CODEXPRO_HTTP_SESSION_TTL_MS": "600000",
                "CODEXPRO_HTTP_TOKEN": self.token.read_text().strip(),
            })
            self.argv = ["nice", "-n", "10", "node", str(pair.release / "dist" / "http.js")]
        else:
            self.argv = ["nice", "-n", "10", str(pair.binary), "--state-dir", str(self.home),
                "--listen", "127.0.0.1:7210", "--token-file", str(self.token),
                "--projects-file", str(self.catalog), "--worktree-root", str(self.managed),
                "--worktree-base", "main", "--codex-dir", str(self.state / "empty-codex"),
                "--worktree-mode", "mcp", "--tool-mode", "standard", "--bash", "full",
                "--write", "workspace", "--audit", "metadata", "--max-worktrees", "8"]

    def start(self):
        assert_free_ports((self.port,))
        self.log = (self.state / "server.log").open("xb")
        os.chmod(self.log.name, 0o600)
        self.child = subprocess.Popen(self.argv, cwd=self.root, env=self.environment,
            stdin=subprocess.DEVNULL, stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
        self.witness = process_start_ticks(self.child.pid)
        self.manifest = {
            "pid": self.child.pid, "process_start_ticks": self.witness, "port": self.port,
            "state_root": str(self.state), "config_home": str(self.home),
            "fixture_root": str(self.root), "projects_file": str(self.catalog),
            "managed_root": str(self.managed), "project_id": "contract",
            "token_file": str(self.token), "source_commit": SOURCE_COMMIT,
        }
        durable_json(self.state / "launch.json", self.manifest)
        endpoint = f"http://127.0.0.1:{self.port}/mcp"
        deadline = min(self.pair.deadline, time.monotonic() + 25)
        while time.monotonic() < deadline:
            require(self.child.poll() is None, "owned server exited before initialization; inspect its private server.log")
            try:
                validate_launch(self.manifest, endpoint, self.token, self.root, self.port)
                self.client = Client(endpoint, self.token, timeout=8)
                require(self.child.poll() is None and process_start_ticks(self.child.pid) == self.witness,
                        "owned process changed during initialization")
                return
            except (OSError, RuntimeError, ContractFailure):
                time.sleep(0.15)
        raise ContractFailure("owned private server did not initialize within 25 seconds")

    def stop(self):
        if self.child is None:
            if self.log:
                self.log.close()
            return {"started": False}
        result = {"pid": self.child.pid, "process_start_ticks": self.witness}
        witnessed = {}
        if self.child.poll() is None:
            require(process_start_ticks(self.child.pid) == self.witness, "owned PID identity changed; refusing a signal")
            require(os.getpgid(self.child.pid) == self.child.pid and os.getsid(self.child.pid) == self.child.pid,
                    "owned server no longer anchors its private process group")
            witnessed = process_group_members(self.child.pid)
            require(witnessed.get(self.child.pid) == self.witness, "owned cleanup group lost its launch identity")
            # start_new_session created this exact private group. Terminate its
            # bounded Git children too; no listener/name/service lookup selects
            # a signal target.
            os.killpg(self.child.pid, signal.SIGTERM)
            try:
                self.child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                require(process_start_ticks(self.child.pid) == self.witness, "owned PID identity changed before escalation")
                os.killpg(self.child.pid, signal.SIGKILL)
                self.child.wait(timeout=3)
        remaining = process_group_members(self.child.pid)
        if remaining:
            require(any(remaining.get(pid) == birth for pid, birth in witnessed.items()),
                    "remaining group lacks a previously witnessed owner; refusing an ambiguous signal")
            os.killpg(self.child.pid, signal.SIGKILL)
            deadline = time.monotonic() + 2
            while time.monotonic() < deadline and process_group_members(self.child.pid):
                time.sleep(0.05)
            require(not process_group_members(self.child.pid), "owned process group remains populated")
        result["exit_code"] = self.child.returncode
        result["stopped"] = self.child.poll() is not None
        result["owned_group_quiescent"] = True
        if self.log:
            self.log.close()
        return result


class ManagedSide(AuditSide):
    def __init__(self, pair, server):
        self.pair, self.server = pair, server
        self.label, self.mode, self.port = server.label, server.mode, server.port
        self.manifest, self.root, self.state, self.home = server.manifest, server.root, server.state, server.home
        self.token, self.managed, self.catalog_path = server.token, server.managed, server.catalog
        self.endpoint = f"http://127.0.0.1:{self.port}/mcp"
        self.client = server.client
        self.clients = {"bootstrap": self.client}
        self.session_windows = {"bootstrap": int(time.time() * 1000)}
        self.call_records, self.actions, self.normalizations, self.approved = [], {}, [], []
        self.actor, self.evidence, self.early_evidence = None, pair.evidence, []
        self.unsettled, self.current, self.case_id, self.ordinal = False, "binding", "binding", 0
        self._workspace, self.checkout, self.lease, self.removed = None, None, None, False
        self.root_identity = (self.root.stat().st_dev, self.root.stat().st_ino)
        self.managed_identity = (self.managed.stat().st_dev, self.managed.stat().st_ino)
        self.catalog, self.catalog_hash = catalog_object(self.catalog_path)
        require(self.catalog == {"version": 1, "defaultProject": "contract", "projects": [{
            "id": "contract", "label": "Managed contract fixture", "root": str(self.root),
            "baseRef": "main", "maxWorktrees": 1}], "creationRoots": []},
            "private managed catalog differs from its owned fixture")
        raw_identity = read_small(self.managed / "connector-id")
        require(re.fullmatch(rb"[0-9a-f]{48}\n", raw_identity), "private connector identity is not source-formatted")
        connector_id = raw_identity.decode().strip()
        self.principal = "connector_" + digest(connector_id)[:32]
        self.expected_actor = "actor_" + digest(self.principal)[:32]
        self.aliases = {
            str(self.root): "<private-project>", str(self.managed): "<private-managed>",
            str(self.home): "<private-home>", str(self.state): "<private-state>",
            self.expected_actor: "<validated-actor>",
        }
        instruction_aliases, instruction_directory = managed_instruction_aliases(self.manifest)
        self.aliases.update(instruction_aliases)
        self.normalizations.append({"case": "binding",
            "rule": "exact owned process codex-dir and its source [home] spelling",
            "codex_dir": instruction_directory})
        self.main_commit, self.alternate_commit = server.main_commit, server.alternate_commit
        self.retry_key = "sp6-managed-" + pair.run_id
        self.permission_differences = []
        self.record({"initialize": self.client.initialization, "launch": self.manifest,
                     "connector_identity_sha256": hashlib.sha256(raw_identity).hexdigest()})
        config = self.data(self.mcp("server_config", {}))
        require(config.get("worktreeMode") == "mcp" and config.get("bashMode") == self.mode
                and config.get("writeMode") == "workspace" and config.get("toolMode") == "standard"
                and config.get("connectionTest") is False,
                "running server does not match the declared managed permission fixture")
        self.inventory_response = self.mcp_rpc("tools/list", {})
        descriptors = raw_result(self.inventory_response)["tools"]
        self.tools = {tool["name"]: tool for tool in descriptors}
        require(len(self.tools) == len(descriptors) and WORKSPACE_TOOLS | FIXTURE_TOOLS <= set(self.tools),
                "managed reference/candidate inventory is incomplete")
        require(not {"list_workspaces", "open_current_workspace"} & set(self.tools),
                "managed inventory contains direct-only workspace tools")
        observed_retirements = sorted(set(self.tools) & RETIRED)
        require(self.label == "reference" or not observed_retirements, "candidate exposed an approved retired descriptor")
        self.approved.append({"descriptor_removals": observed_retirements, "approval_commit": APPROVAL_COMMIT})
        require(self.worktrees() == [{"worktree": str(self.root), "HEAD": self.main_commit,
                                     "branch": "refs/heads/main"}], "fresh private repository already has another worktree")

    @property
    def workspace(self):
        require(self._workspace is not None, "managed workspace has not been bound")
        return self._workspace

    def alive(self):
        require(time.monotonic() < self.pair.deadline, "managed lane exceeded its five-minute budget")
        require(not self.unsettled, "previous private operation has an unsettled outcome")
        require(self.server.child.poll() is None and process_start_ticks(self.server.child.pid) == self.server.witness,
                "owned managed process exited or changed")
        require((self.root.stat().st_dev, self.root.stat().st_ino) == self.root_identity
                and (self.managed.stat().st_dev, self.managed.stat().st_ino) == self.managed_identity,
                "private repository or managed storage identity changed")
        require(digest(read_small(self.catalog_path).decode()) == self.catalog_hash, "catalog changed outside the managed lane")

    def mcp_rpc(self, method, params, session="bootstrap"):
        self.ordinal += 1
        self.current = f"{self.case_id}/{self.ordinal:03d}:{params.get('name', method)}"
        return super().mcp_rpc(method, params, session)

    def git(self, *arguments, root=None):
        return command(["git", "-C", str(root or self.root), "-c", "core.hooksPath=/dev/null", *arguments],
                       env=self.server.environment).strip()

    def worktrees(self):
        raw = command(["git", "-C", str(self.root), "worktree", "list", "--porcelain", "-z"],
                      env=self.server.environment)
        return parse_worktrees(raw)

    def persisted_lease(self):
        if self.label == "reference":
            path = self.managed / "leases" / (digest(self.workspace) + ".json")
            return json.loads(read_small(path))
        path = self.home / "workspaces.sqlite"
        regular_private_file(path, limit=16 * 1024 * 1024)
        location = "file:" + urllib.parse.quote(str(path), safe="/") + "?mode=ro"
        with sqlite3.connect(location, uri=True, timeout=3) as db:
            rows = db.execute("SELECT body FROM leases WHERE id=?", (self.workspace,)).fetchall()
        require(len(rows) == 1, "candidate did not persist exactly one matching managed lease")
        require(len(rows[0][0]) <= 1024 * 1024, "candidate lease exceeds its read bound")
        return json.loads(rows[0][0])

    def validate_lease(self, data=None, *, nonce=False):
        if data is not None:
            value = data.get("workspace_id")
            if self._workspace is None:
                self._workspace = value
            require(value == self._workspace, "idempotency changed the stable workspace ID")
        expected = derive_lease_identity(self.root, self.managed, self.workspace, self.principal, self.retry_key)
        expected["baseCommit"] = self.main_commit
        lease = self.persisted_lease()
        require(all(lease.get(key) == value for key, value in expected.items()),
                "persisted lease disagrees with source identity/ownership/path derivation")
        require(lease.get("state") == "ready", "managed lease is not ready")
        for field in ("createdAt", "updatedAt", "lastUsedAt"):
            timestamp(lease[field])
        checkout = private_path(lease["workspaceRoot"])
        require(checkout == Path(expected["workspaceRoot"]) and checkout.is_relative_to(self.managed),
                "workspace resolves outside its configured private managed root")
        require(self.git("rev-parse", "--show-toplevel", root=checkout) == str(checkout), "checkout top-level mismatch")
        common = self.git("rev-parse", "--git-common-dir", root=checkout)
        common_path = Path(common) if Path(common).is_absolute() else checkout / common
        require(common_path.resolve(strict=True) == self.root / ".git", "checkout is attached to another Git repository")
        require(self.git("rev-parse", "HEAD", root=checkout) == self.main_commit, "checkout base commit changed")
        entries = self.worktrees()
        require(len(entries) == 2 and {"worktree": str(checkout), "HEAD": self.main_commit,
            "branch": "refs/heads/" + expected["branch"]} in entries,
            "private Git inventory does not contain exactly the owned managed checkout")
        if data is not None:
            require(data.get("branch") == expected["branch"] and data.get("base_commit") == self.main_commit,
                    "tool branch/base differs from persisted Git identity")
        self.checkout, self.lease = checkout, lease
        self.aliases[self.workspace] = "<managed-workspace>"
        self.aliases[digest(self.workspace)[:24]] = "<workspace-hash24>"
        self.aliases[digest(self.workspace)[:32]] = "<workspace-hash32>"
        self.aliases[str(checkout)] = "<private-managed-checkout>"
        self.record({"validated_lease": lease, "git_worktrees": entries})
        if nonce:
            name = ".sp6-managed-binding-" + self.pair.run_id
            marker = f"sp6-managed:{self.label}:{self.server.child.pid}:{self.pair.run_id}"
            payload = (marker + "\n").encode()
            path = checkout / name
            write_exclusive(path, payload)
            try:
                read = self.data(self.mcp("read", {"workspace_id": self.workspace, "path": name,
                    "start_line": 1, "end_line": 1}))
                plain = "\n".join(re.sub(r"^\s*\d+ \| ?", "", line) for line in read["text"].splitlines())
                require(plain == marker and read.get("sha256") == hashlib.sha256(payload).hexdigest(),
                        "returned workspace is not bound to this side/PID's complete private nonce")
            finally:
                if not self.unsettled:
                    require(path.read_bytes() == payload, "owned binding nonce changed unexpectedly")
                    path.unlink()
        return lease

    def normalize_recent(self, response, before, after):
        value = copy.deepcopy(response)
        data = self.data(value)
        brief = data.get("recent_activity")
        require(isinstance(brief, dict), "successful managed summary lacks recent_activity")
        watermark = before["latest_sequence"]
        require(type(watermark) is int and brief["latest_sequence"] == watermark
                and brief["next_sequence"] == watermark and after["latest_sequence"] == watermark + 1,
                "managed summary audit watermark does not bind its adjacent call receipts")
        require(brief["earliest_sequence"] == before["retained_from_sequence"]
                <= after["retained_from_sequence"], "managed summary retention floor changed unexpectedly")
        rows, _ = self.audit_rows("contract")
        require(all(not label.startswith("prior-") for _, label in self.actions.values()),
                "fresh managed fixture contains unowned historical mutations")
        prior = [row for row in rows if row["sequence"] <= watermark]
        require(brief["enabled"] is True and brief["last_recorded_project_change"] == last_change(prior)
                and brief["latest_recorded_activity"] == (prior[-1]["finished_at"] if prior else None)
                and brief["actions"] == recent_actions(prior, data["workspace_id"])
                and brief["coverage"] == COVERAGE and brief["has_more"] is False
                and brief["gap_detected"] is False and "warning" not in brief,
                "managed recent-activity projection differs from exact retained receipts")
        for row in brief["actions"]:
            action, label = self.actions[row["action_id"]]
            require(row["sequence"] == action["sequence"] and row["at"] == action["finished_at"],
                    "managed brief action lost its durable receipt binding")
            row["at"] = "<finished:" + label + ">"
        changed = brief["last_recorded_project_change"]
        if changed is not None:
            previous = changed["at"]
            brief["last_recorded_project_change"] = self.normalize_change(changed)
            suffix = "\n\nLast recorded project change: " + previous + ". " + COVERAGE
            texts = [item for item in value["body"]["result"]["content"] if item.get("type") == "text"]
            require(texts and texts[0]["text"].endswith(suffix), "managed summary text lost its change receipt")
            texts[0]["text"] = texts[0]["text"][:-len(suffix)] + "\n\nLast recorded project change: " + brief["last_recorded_project_change"]["at"] + ". " + COVERAGE
        if prior:
            brief["latest_recorded_activity"] = "<finished:" + self.actions[prior[-1]["action_id"]][1] + ">"
        return value

    def summary(self, tool, args, session="bootstrap", *, created=None):
        before = self.data(self.mcp("activity_status", {}))
        response = self.mcp(tool, args, session)
        after = self.data(self.mcp("activity_status", {}))
        data = self.data(response)
        if created is not None:
            require(data.get("created") is created, "create/reuse flag changed")
        self.validate_lease(data)
        value = self.normalize_recent(response, before, after)
        normalize_permission_summary(value, tool, self.mode, self.permission_differences, self.case_id)
        return self.normalize_mcp(value)

    def create_args(self, **changes):
        return {"project_id": "contract", "base_ref": "main", "label": "SP-6 managed fixture",
            "idempotency_key": self.retry_key, "include_tree": False,
            "include_skills": False, "include_global_skills": False, **changes}

    def open_args(self):
        return {"workspace_id": self.workspace, "include_tree": False,
                "include_skills": False, "include_global_skills": False}

    def expected_error(self, tool, args, session="bootstrap"):
        response = self.mcp(tool, args, session)
        require(raw_result(response).get("isError") is True, "expected managed error was not returned")
        return self.normalize_mcp(response)

    def read_proof(self, session="bootstrap"):
        response = self.mcp("read", {"workspace_id": self.workspace, "path": FILE_NAME}, session)
        data = self.data(response)
        require(data.get("sha256") == hashlib.sha256(FILE_BYTES).hexdigest(), "managed read changed full-file bytes")
        require((self.checkout / FILE_NAME).read_bytes() == FILE_BYTES, "private proof bytes changed on disk")
        return self.normalize_mcp(response)

    def release(self):
        response = self.mcp("release_workspace", {"workspace_id": self.workspace})
        require(self.data(response).get("released") is True, "release flag missing")
        lease = self.validate_lease()
        timestamp(lease["releasedAt"])
        return self.normalize_mcp(response)


def run_case(side, index):
    if index == 1:
        response = side.mcp("list_projects", {})
        data = side.data(response)
        require(data.get("count") == 1 and data.get("creation_root_count") == 0
                and data.get("creation_roots") == [] and data.get("default_project_id") == "contract"
                and data.get("multi_open_supported") is False and data.get("max_multi_open_projects") == 0,
                "MCP project catalog behavior differs")
        require(data["projects"] == [{"id": "contract", "label": "Managed contract fixture", "default": True,
            "baseRef": "main", "maxWorktrees": 1, "last_recorded_project_change": None}],
            "MCP catalog unexpectedly exposes a direct workspace or changes project limits")
        return {"response": side.normalize_mcp(response),
                "workspace_descriptors": {name: side.tools[name] for name in sorted(WORKSPACE_TOOLS)}}
    if index == 2:
        return side.expected_error("open_workspace", {"project_id": "contract"})
    if index == 3:
        result = side.summary("create_workspace", side.create_args(), created=True)
        side.validate_lease(nonce=True)
        return result
    if index == 4:
        return [side.summary("create_workspace", side.create_args(), created=False),
                side.summary("create_workspace", side.create_args(), session="second", created=False)]
    if index in (5, 6):
        before = side.worktrees()
        args = side.create_args(**({"base_ref": "alternate"} if index == 5
                                  else {"idempotency_key": side.retry_key + "-another"}))
        result = side.expected_error("create_workspace", args)
        require(side.worktrees() == before, "failed create allocated another checkout")
        side.validate_lease()
        return result
    if index == 7:
        return [side.summary("open_workspace", side.open_args()),
                side.summary("open_workspace", side.open_args(), session="second")]
    if index == 8:
        response = side.mcp("write", {"workspace_id": side.workspace, "path": FILE_NAME,
                                    "content": FILE_BYTES.decode()})
        side.data(response)
        return [side.normalize_mcp(response), side.read_proof()]
    if index == 9:
        released = side.release()
        require((side.checkout / FILE_NAME).read_bytes() == FILE_BYTES, "release removed dirty work")
        return [released, side.summary("open_workspace", side.open_args(), session="second"),
                side.read_proof(session="second")]
    if index == 10:
        before = side.worktrees()
        response = side.expected_error("remove_workspace", {"workspace_id": side.workspace})
        require(side.worktrees() == before and (side.checkout / FILE_NAME).read_bytes() == FILE_BYTES,
                "dirty-worktree refusal changed private work")
        side.validate_lease()
        return response
    if index == 11:
        read = side.read_proof()
        response = side.mcp("apply_patch", {"workspace_id": side.workspace,
            "patch": "*** Begin Patch\n*** Delete File: " + FILE_NAME + "\n*** End Patch"})
        side.data(response)
        require(not (side.checkout / FILE_NAME).exists(), "native delete patch did not remove the owned proof file")
        require(side.git("status", "--porcelain=v1", "--untracked-files=all", root=side.checkout) == "",
                "owned managed checkout is not clean after deleting its proof file")
        return [read, side.normalize_mcp(response)]
    if index == 12:
        release = side.release()
        response = side.mcp("remove_workspace", {"workspace_id": side.workspace})
        data = side.data(response)
        require(data.get("removed") is True and data.get("branch_preserved") is True,
                "clean removal does not preserve the source result")
        require(not side.checkout.exists() and len(side.worktrees()) == 1, "clean removal left its checkout registered")
        require(side.git("rev-parse", "--verify", "refs/heads/" + side.lease["branch"]) == side.main_commit,
                "clean removal deleted or moved the managed branch")
        side.removed = True
        reopened = side.expected_error("open_workspace", side.open_args(), session="second")
        return [release, side.normalize_mcp(response), reopened]
    raise ContractFailure("unknown managed business case")


class Pair:
    def __init__(self, release, binary, parent, output):
        self.release, self.binary = Path(release).resolve(strict=True), Path(binary).resolve(strict=True)
        require(self.binary.is_file() and os.access(self.binary, os.X_OK), "candidate binary is not executable")
        package = json.loads((self.release / "package.json").read_text())
        require(package.get("version") == "0.31.0" and "44bb76b" in self.release.name,
                "reference must be the immutable 0.31.0/44bb76b release")
        require((self.release / "dist" / "http.js").resolve(strict=True).is_relative_to(self.release),
                "reference entry escapes its immutable release")
        self.parent, self.output = private_path(parent, missing=True), artifact_path(output)
        allowed = (Path.home() / ".codexpro-go-proof").resolve()
        require(self.parent.is_relative_to(allowed) and self.parent != allowed
                or str(self.parent).startswith("/tmp/sp6-managed-"), "managed proof state is not in an allowed private root")
        require(not self.parent.exists() and not self.output.exists(), "managed proof refuses existing state or output")
        assert_free_ports()  # Before creating directories or starting either child.
        self.parent.mkdir(parents=True, mode=0o700)
        self.output.parent.mkdir(parents=True, exist_ok=True)
        self.run_id = secrets.token_hex(12)
        self.deadline = time.monotonic() + 300
        self.servers, self.sides = [], []
        self.evidence = Evidence(self.parent / "managed-raw.jsonl.gz", self.all_clients)
        self.report = {
            "schema_version": 1, "source_commit": SOURCE_COMMIT, "approval_commit": APPROVAL_COMMIT,
            "started_at": iso_now(), "state_parent": str(self.parent), "ports": [7211, 7210],
            "binary_sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
            "reference_entry_sha256": hashlib.sha256((self.release / "dist" / "http.js").read_bytes()).hexdigest(),
            "permission_fixture": {"reference": "mcp/safe", "candidate": "mcp/full",
                "source_constraint": "src/config.ts rejects MCP plus full Bash",
                "normalization": "only validated workspace bash_mode and exact 'Bash mode:' summary line",
                "go_approval_machinery": False},
            "cases": [], "cleanup": [], "cleanup_errors": [], "stage4_complete": False,
            "managed_operational_migration_proven": False,
        }
        private_http()

    def all_clients(self):
        result = []
        for side in self.sides:
            result.extend(side.clients.values())
        for server in self.servers:
            if server.client and server.client not in result:
                result.append(server.client)
        return result

    def run(self):
        try:
            for label in ("reference", "candidate"):
                server = OwnedServer(self, label)
                self.servers.append(server)
                server.start()
                side = ManagedSide(self, server)
                self.sides.append(side)
            require(self.servers[0].main_commit == self.servers[1].main_commit
                    and self.servers[0].alternate_commit == self.servers[1].alternate_commit,
                    "paired private fixture commits are not identical")
            self.report["fixture_commits"] = {"main": self.servers[0].main_commit, "alternate": self.servers[0].alternate_commit}
            for index, case in enumerate(CASE_IDS, 1):
                results, errors = {}, {}
                counts = [len(side.permission_differences) for side in self.sides]
                for side in self.sides:
                    side.case_id = case
                    try:
                        results[side.label] = run_case(side, index)
                    except Exception as error:
                        errors[side.label] = safe_error(error, self.all_clients())
                different = not errors and canonical(results["reference"]) != canonical(results["candidate"])
                adjusted = any(len(side.permission_differences) > count for side, count in zip(self.sides, counts))
                record = {"id": case, "status": "failed" if errors or different
                    else "matched_with_declared_permission_fixture" if adjusted else "unchanged",
                    "errors": errors}
                if different:
                    record["difference"] = difference(results["reference"], results["candidate"])
                self.report["cases"].append(record)
                self.evidence.append({"case": case, "normalized": results, "comparison": record})
                if errors:
                    self.report["aborted_after"] = case
                    break
        except Exception as error:
            self.report["fatal_error"] = safe_error(error, self.all_clients())
        finally:
            self.report["normalizations"] = {side.label: side.normalizations for side in self.sides}
            self.report["permission_differences"] = {side.label: side.permission_differences for side in self.sides}
            self.report["approved_retirements"] = {side.label: side.approved for side in self.sides}
            self.report["private_workspace_cleanup"] = {side.label: {
                "workspace_removed": side.removed, "workspace_was_bound": side._workspace is not None,
                "retained_for_review_on_failure": bool(side._workspace and not side.removed)}
                for side in self.sides}
            for client in self.all_clients():
                try:
                    client.close()
                except Exception as error:
                    self.report["cleanup_errors"].append(safe_error(error, self.all_clients()))
            for server in reversed(self.servers):
                try:
                    self.report["cleanup"].append({"side": server.label, **server.stop()})
                except Exception as error:
                    self.report["cleanup_errors"].append(safe_error(error, self.all_clients()))
            self.evidence.close()
            self.report["finished_at"] = iso_now()
            self.report["raw_evidence"] = {"path": str(self.evidence.path), "records": self.evidence.records,
                "uncompressed_bytes": self.evidence.bytes, "sha256": hashlib.sha256(self.evidence.path.read_bytes()).hexdigest()}
            self.report["unchanged"] = sum(item["status"] == "unchanged" for item in self.report["cases"])
            self.report["permission_fixture_matches"] = sum(item["status"] == "matched_with_declared_permission_fixture" for item in self.report["cases"])
            self.report["failed"] = sum(item["status"] == "failed" for item in self.report["cases"])
            self.report["all_12_cases_completed"] = len(self.report["cases"]) == len(CASE_IDS) and not self.report.get("aborted_after")
            for client in self.all_clients():
                client.assert_no_credential(self.report)
            durable_json(self.output, self.report)
        return self.report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", required=True)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--state-parent", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    pair = Pair(args.release, args.binary, args.state_parent, args.output)
    def interrupted(signum, frame):
        raise ContractFailure(f"managed driver interrupted by signal {signum}; cleaning up only owned children")
    old_handlers = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        report = pair.run()
    finally:
        for sig, handler in old_handlers.items():
            signal.signal(sig, handler)
    print(json.dumps({"output": str(pair.output), "unchanged": report["unchanged"],
        "permission_fixture_matches": report["permission_fixture_matches"], "failed": report["failed"],
        "all_12_cases_completed": report["all_12_cases_completed"], "cleanup_errors": len(report["cleanup_errors"])}))
    if report.get("fatal_error") or report["failed"] or report["cleanup_errors"] or not report["all_12_cases_completed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
