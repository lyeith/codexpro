#!/usr/bin/env python3
"""Prove an HTTP-saved profile is consumed by fresh, owned Go processes.

Uses only port 7210 and a new private state directory. The default, profile, and
explicit-override launches are separate OS processes; no service is controlled.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import stat
import subprocess
import time
import urllib.error
import urllib.request

from contract_suite import Client, SOURCE_COMMIT, raw_result
from paired_contract_suite import (
    APPROVAL_COMMIT, RETIRED, ContractFailure, Evidence, artifact_path, digest,
    iso_now, private_path, process_start_ticks, require, safe_error, validate_launch,
)
from admin_catalog_contract_suite import read_small, regular_private_file
from managed_contract_suite import (
    OwnedServer, assert_free_ports, command, durable_json, sanitized_environment,
    write_exclusive,
)
from process_contract_suite import private_http

PORT = 7210
ENDPOINT = "http://127.0.0.1:7210/mcp"
PROFILE_FIELDS = {
    "toolMode": "minimal", "bashTranscript": "full", "maxWorktrees": "7",
    "worktreeBase": "main", "bashSession": "sp6-profile-restart",
}
DEFAULTS = {"toolMode": "standard", "bashTranscript": "compact", "maxWorktrees": 64,
            "worktreeBaseRef": "HEAD", "bashSessionId": None}
SAVED = {"toolMode": "minimal", "bashTranscript": "full", "maxWorktrees": 7,
         "worktreeBaseRef": "main", "bashSessionId": "sp6-profile-restart"}


def checked_hash(path):
    return hashlib.sha256(read_small(path)).hexdigest()


def expected_inventory(oracle, tier):
    require(oracle.get("source_commit") == SOURCE_COMMIT,
            "registry oracle does not identify the captured source commit")
    options = {"worktree_mode": "off", "tool_mode": tier, "project_count": 1,
               "analysis_enabled": True, "audit_mode": "metadata", "connection_test": False}
    matches = [item for item in oracle["cases"] if item["options"] == options]
    require(len(matches) == 1, "registry oracle must contain this exact private profile")
    names = matches[0]["source_tool_names"]
    require(len(names) == len(set(names)), "source registry oracle contains duplicate names")
    return sorted(set(names) - RETIRED)


def validate_runtime(config, tools, expected, oracle):
    for key, value in expected.items():
        require(key in config and type(config[key]) is type(value) and config[key] == value,
                "actual process did not apply expected runtime field: " + key)
    require(config.get("port") == PORT and config.get("bashMode") == "full"
            and config.get("writeMode") == "workspace" and config.get("authEnabled") is True
            and config.get("worktreeMode") == "off", "private full-permission runtime changed")
    expected_names = expected_inventory(oracle, expected["toolMode"])
    names = [tool["name"] for tool in tools]
    require(len(names) == len(set(names)) and sorted(names) == expected_names,
            "actual tools/list does not match the profile's retained source inventory")
    require(sorted(config.get("registeredTools", [])) == expected_names
            and config.get("registeredToolCount") == len(expected_names),
            "server_config and actual registration disagree")
    return {"runtime": {key: config[key] for key in expected},
            "registered_tools": sorted(names), "registered_tool_count": len(names),
            "permission": "full", "port": PORT}


class RestartServer(OwnedServer):
    """Reuse the managed driver's guarded process-group cleanup, not its fixture."""
    def __init__(self, proof, phase, overrides=()):
        self.proof, self.phase = proof, phase
        self.child = self.client = self.log = self.witness = None
        self.stopped_report = None
        self.argv = ["nice", "-n", "10", str(proof.binary), "--state-dir", str(proof.home),
            "--listen", "127.0.0.1:7210", "--token-file", str(proof.token),
            "--projects-file", str(proof.catalog), "--limits-file", str(proof.limits),
            "--codex-dir", str(proof.parent / "empty-codex"), "--worktree-mode", "off",
            "--bash", "full", "--write", "workspace", "--audit", "metadata", "--analysis=true",
            "--host-resources-helper", "", *overrides]

    def verify(self):
        require(self.child is not None and self.child.poll() is None,
                "owned restart-proof process is not running")
        validate_launch(self.manifest, ENDPOINT, self.proof.token, self.proof.root, PORT)
        require(Path(f"/proc/{self.child.pid}/exe").resolve(strict=True) == self.proof.binary,
                "owned listener is not the declared Go executable")

    def start(self):
        assert_free_ports((PORT,))
        self.proof.check_time()
        require(self.proof.binary_hash() == self.proof.binary_digest, "candidate binary changed")
        log_path = self.proof.parent / (self.phase + ".log")
        self.log = log_path.open("xb")
        os.chmod(log_path, 0o600)
        self.child = subprocess.Popen(self.argv, cwd=self.proof.root,
            env=self.proof.environment, stdin=subprocess.DEVNULL,
            stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True)
        self.witness = process_start_ticks(self.child.pid)
        self.manifest = {"pid": self.child.pid, "process_start_ticks": self.witness,
            "port": PORT, "state_root": str(self.proof.parent),
            "config_home": str(self.proof.home), "fixture_root": str(self.proof.root),
            "projects_file": str(self.proof.catalog), "project_id": "contract",
            "token_file": str(self.proof.token), "phase": self.phase}
        durable_json(self.proof.parent / (self.phase + "-launch.json"), self.manifest)
        deadline = min(self.proof.deadline, time.monotonic() + 25)
        while time.monotonic() < deadline:
            require(self.child.poll() is None, "owned Go process exited; inspect its private phase log")
            try:
                self.verify()
            except (OSError, RuntimeError, ContractFailure):
                time.sleep(0.1)
                continue
            self.client = Client(ENDPOINT, self.proof.token, timeout=5)
            self.verify()
            self.proof.record({"phase": self.phase, "launch": self.manifest,
                               "initialize": self.client.initialization})
            return
        raise ContractFailure("owned Go process did not initialize within 25 seconds")

    def stop(self):
        if self.stopped_report is not None:
            return self.stopped_report
        if self.client and self.child is not None and self.child.poll() is None:
            try:
                self.verify()
            except (OSError, RuntimeError, ContractFailure):
                pass  # Process cleanup still uses exact PID/start-time ownership.
            else:
                self.client.close()
        self.stopped_report = super().stop()
        self.stopped_report["phase"] = self.phase
        return self.stopped_report


class Proof:
    def __init__(self, binary, parent, output, oracle):
        self.binary = Path(binary).resolve(strict=True)
        require(self.binary.is_file() and os.access(self.binary, os.X_OK), "Go binary is not executable")
        self.parent, self.output = private_path(parent, missing=True), artifact_path(output)
        declared = Path(parent).expanduser().absolute()
        require(declared == self.parent, "proof state path contains a symlink or relative traversal")
        allowed = (Path.home() / ".codexpro-go-proof").resolve()
        require(self.parent.is_relative_to(allowed) and self.parent != allowed
                or str(self.parent).startswith("/tmp/sp6-profile-restart-"),
                "restart proof state is not in an allowed private root")
        require(not self.parent.exists() and not self.output.exists(), "proof refuses existing state or output")
        require(Path(oracle).stat().st_size <= 2 * 1024 * 1024, "registry oracle is too large")
        self.oracle = json.loads(Path(oracle).read_text())
        for tier in ("standard", "minimal", "full"):
            expected_inventory(self.oracle, tier)
        assert_free_ports((PORT,))  # No state, token, or child exists before this gate.
        self.parent.mkdir(parents=True, mode=0o700)
        self.output.parent.mkdir(parents=True, exist_ok=True)
        self.home, self.root = self.parent / "home", self.parent / "fixture"
        for path in (self.home, self.root, self.parent / "empty-codex"):
            path.mkdir(mode=0o700)
        self.token, self.catalog, self.limits = (self.parent / "http-token",
            self.parent / "projects.json", self.parent / "limits.env")
        write_exclusive(self.token, secrets.token_urlsafe(48) + "\n")
        write_exclusive(self.limits, "")
        self.environment = sanitized_environment()
        git = lambda *args: command(["git", "-C", str(self.root), "-c", "core.hooksPath=/dev/null",
            "-c", "commit.gpgSign=false", *args], env=self.environment).strip()
        git("-c", "init.templateDir=", "init", "-q", "-b", "main")
        git("config", "core.hooksPath", os.devnull)
        write_exclusive(self.root / "README.md", "# Private saved-profile restart fixture\n")
        git("add", "README.md"); git("commit", "-qm", "test: private profile restart fixture")
        durable_json(self.catalog, {"version": 1, "defaultProject": "contract",
            "projects": [{"id": "contract", "label": "Profile restart fixture", "root": str(self.root)}],
            "creationRoots": []})
        self.profile = self.home / "profiles" / (digest(str(self.root))[:24] + ".json")
        self.binary_digest = self.binary_hash()
        self.deadline = time.monotonic() + 120
        self.servers, self.saved_hash = [], None
        self.evidence = Evidence(self.parent / "profile-restart-raw.jsonl.gz", self.clients)
        self.report = {"schema_version": 1, "source_commit": SOURCE_COMMIT,
            "approval_commit": APPROVAL_COMMIT, "started_at": iso_now(),
            "state_parent": str(self.parent), "port": PORT, "binary_sha256": self.binary_digest,
            "registry_oracle_sha256": hashlib.sha256(Path(oracle).read_bytes()).hexdigest(),
            "scope": "Go-only actual-process consumption of HTTP-saved profile, with explicit CLI precedence",
            "cases": [], "cleanup": [], "cleanup_errors": [], "stage4_complete": False,
            "profile_restart_proven": False, "approved_retired_descriptors": sorted(RETIRED)}

    def binary_hash(self):
        with self.binary.open("rb") as stream:
            return hashlib.file_digest(stream, "sha256").hexdigest()

    def clients(self):
        return [server.client for server in self.servers if server.client is not None]

    def check_time(self):
        require(time.monotonic() < self.deadline, "profile proof exceeded its 120-second budget")

    def record(self, value):
        self.check_time()
        self.evidence.append(value)

    def mcp(self, server, name, args):
        server.verify(); self.check_time()
        response = server.client.call(name, args)
        self.record({"phase": server.phase, "tool": name, "arguments": args, "response": response})
        result = raw_result(response)
        require(response["http_status"] == 200 and result.get("isError") is not True,
                "private MCP proof call failed: " + name)
        return result.get("structuredContent", {})

    def snapshot(self, server):
        config = self.mcp(server, "server_config", {})
        server.verify()
        response = server.client.rpc("tools/list", {})
        self.record({"phase": server.phase, "method": "tools/list", "response": response})
        tools = response["body"]["result"]["tools"]
        projects = self.mcp(server, "list_projects", {})
        rows = [p for p in projects["projects"] if p["id"] == "contract"]
        require(len(rows) == 1 and rows[0].get("workspace_id") == "ws_" + digest(str(self.root))[:24],
                "private catalog did not resolve its fixture workspace")
        nonce = f"sp6-profile-{server.phase}-{server.child.pid}-{server.witness}-{secrets.token_hex(8)}"
        marker = self.root / ("profile-binding-" + server.phase + ".txt")
        payload = (nonce + "\n").encode()
        write_exclusive(marker, payload)
        read = self.mcp(server, "read", {"workspace_id": rows[0]["workspace_id"],
            "path": marker.name, "start_line": 1, "end_line": 1})
        require(read.get("sha256") == hashlib.sha256(payload).hexdigest()
                and read.get("text") == "1 | " + nonce,
                "MCP listener is not reading this exact private process fixture")
        require(marker.read_bytes() == payload, "private nonce changed during read")
        return config, tools

    def http(self, server, method, payload=None):
        server.verify(); self.check_time()
        headers = {"Authorization": "Bearer " + server.client.credential,
                   "Content-Type": "application/json", "Origin": "http://127.0.0.1:7210"}
        request = urllib.request.Request("http://127.0.0.1:7210/admin/profile",
            data=None if payload is None else json.dumps(payload).encode(), headers=headers, method=method)
        try:
            response = urllib.request.urlopen(request, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(1024 * 1024 + 1)
            require(len(raw) <= 1024 * 1024, "profile HTTP response exceeds its proof bound")
            result = {"http_status": response.status, "body": json.loads(raw)}
        server.client.assert_no_credential(result)
        self.record({"phase": server.phase, "http_method": method, "path": "/admin/profile",
                     "request": payload, "response": result})
        require(result["http_status"] == 200 and result["body"].get("ok") is True,
                "private profile HTTP call failed")
        return result["body"]

    def save(self, server):
        require(not self.profile.exists(), "fresh proof unexpectedly has a saved profile")
        result = self.http(server, "POST", {**PROFILE_FIELDS, "port": str(PORT), "tunnel": "none"})
        require(result.get("saved") is True and result.get("profile_path") == str(self.profile),
                "HTTP save did not identify the private derived profile")
        info = regular_private_file(self.profile)
        require(stat.S_IMODE(info.st_mode) == 0o600, "saved profile mode is not 0600")
        raw = read_small(self.profile); persisted = json.loads(raw)
        require(persisted.get("root") == str(self.root) and persisted.get("version") == 1,
                "saved profile does not bind the exact private root")
        require(persisted.get("token") == server.client.credential,
                "saved profile credential is not this isolated instance's credential")
        require("cloudflareToken" not in persisted, "fresh private profile gained an unrelated tunnel credential")
        for key, value in PROFILE_FIELDS.items():
            require(persisted.get(key) == value, "saved setting does not match HTTP request: " + key)
        safe = copy.deepcopy(persisted); safe["token"] = "<saved>"
        self.saved_hash = hashlib.sha256(raw).hexdigest()
        self.record({"profile_state": safe, "profile_sha256": self.saved_hash, "mode": "0600"})

    def start(self, phase, overrides=()):
        server = RestartServer(self, phase, overrides)
        self.servers.append(server)
        server.start()
        return server

    def stop(self, server):
        result = server.stop()
        self.report["cleanup"].append(result)
        require(result.get("stopped") is True and result.get("owned_group_quiescent") is True,
                "owned private process did not stop cleanly")
        assert_free_ports((PORT,))

    def check_saved_unchanged(self):
        require(self.saved_hash is not None and checked_hash(self.profile) == self.saved_hash,
                "restart or explicit override changed the saved profile")

    def run(self):
        try:
            first = self.start("initial")
            config, tools = self.snapshot(first)
            self.report["cases"].append({"id": "PROFILE-RESTART-01-default-process", "status": "passed",
                **validate_runtime(config, tools, DEFAULTS, self.oracle)})
            self.save(first)
            unchanged = self.mcp(first, "server_config", {})
            first.verify()
            unchanged_tools = first.client.rpc("tools/list", {})
            self.record({"phase": first.phase, "after_save_tools": unchanged_tools})
            require(unchanged == config and unchanged_tools["body"]["result"]["tools"] == tools,
                    "HTTP profile save changed the existing process runtime")
            self.check_saved_unchanged()
            self.report["cases"].append({"id": "PROFILE-RESTART-02-save-without-runtime-change", "status": "passed"})
            self.stop(first)
            second = self.start("saved-profile")
            require((second.child.pid, second.witness) != (first.child.pid, first.witness),
                    "restart did not create a new process identity")
            config, tools = self.snapshot(second)
            self.check_saved_unchanged()
            self.report["cases"].append({"id": "PROFILE-RESTART-03-new-process-consumes-profile", "status": "passed",
                **validate_runtime(config, tools, SAVED, self.oracle)})
            self.stop(second)
            third = self.start("explicit-override", ("--tool-mode", "full", "--max-worktrees", "11"))
            config, tools = self.snapshot(third)
            self.check_saved_unchanged()
            expected = {**SAVED, "toolMode": "full", "maxWorktrees": 11}
            self.report["cases"].append({"id": "PROFILE-RESTART-04-explicit-startup-override", "status": "passed",
                **validate_runtime(config, tools, expected, self.oracle)})
            self.stop(third)
            self.report["profile_restart_proven"] = True
        except Exception as error:
            self.report["failure"] = safe_error(error, self.clients())
        finally:
            signal.alarm(0)
            for server in reversed(self.servers):
                if server.stopped_report is None:
                    try:
                        self.report["cleanup"].append(server.stop())
                    except Exception as error:
                        self.report["cleanup_errors"].append(safe_error(error, self.clients()))
            self.report["finished_at"] = iso_now()
            self.report["passed"] = bool(self.report["profile_restart_proven"]
                and not self.report.get("failure") and not self.report["cleanup_errors"])
            self.evidence.close()
            self.report["raw_evidence"] = {"path": str(self.evidence.path), "records": self.evidence.records,
                "uncompressed_bytes": self.evidence.bytes,
                "sha256": hashlib.sha256(self.evidence.path.read_bytes()).hexdigest()}
            for client in self.clients():
                client.assert_no_credential(self.report)
            durable_json(self.output, self.report)
        return self.report["passed"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--state-parent", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--registry-oracle", default=str(Path(__file__).resolve().parents[2] /
        "go/internal/contracts/testdata/registry-capability-oracle.json"))
    args = parser.parse_args()
    private_http()
    def interrupted(signum, frame):
        raise ContractFailure("bounded profile proof interrupted by signal " + str(signum))
    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM):
        signal.signal(sig, interrupted)
    proof = Proof(args.binary, args.state_parent, args.output, args.registry_oracle)
    signal.alarm(120)
    passed = proof.run()
    print(json.dumps({"passed": passed, "cases_passed": len(proof.report["cases"]),
        "profile_restart_proven": proof.report["profile_restart_proven"], "output": str(proof.output)}))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
