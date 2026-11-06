#!/usr/bin/env python3
"""One disposable same-pair recovery proof; never an installer or downgrade tool."""

from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import tempfile
import time
from datetime import datetime, timedelta, timezone

MARKER = "DISPOSABLE-NAMED-POLICY-FIXTURE"
MARKER_BODY = "named-policy-fixture-v1\n"
EXPECTED_HEADER = "X-Agentmail-Expected-Credential"
REQUEST_SECONDS = 4
START_SECONDS = 5
STOP_SECONDS = 5
MAX_BODY = 64 * 1024


class Refusal(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise Refusal(message)


def private_directory(path):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path, "private path must be absolute without symlinks")
    info = path.stat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid() and
            stat.S_IMODE(info.st_mode) == 0o700, "private directory must be owned mode 0700")
    for parent in path.parents:
        meta = parent.stat()
        writable = meta.st_mode & 0o022
        sticky_root = meta.st_uid == 0 and meta.st_mode & stat.S_ISVTX
        require(meta.st_uid in (0, os.geteuid()) and (not writable or sticky_root),
                "private path has an unsafe ancestor")
    return path


def fixture_environment(root):
    # An explicit child environment; never copy the caller's credentials/config.
    return {
        "PATH": "/usr/local/go/bin:/usr/bin:/bin",
        "HOME": str(root / "home"),
        "TMPDIR": str(root / "tmp"),
        "LANG": "C.UTF-8",
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": "/dev/null",
        "CODEXPRO_JOB_SCOPES": "0",
        "NAMED_POLICY_FIXTURE_ROOT": str(root),
    }


def capture(argv, env, *, cwd=None, timeout=15):
    result = subprocess.run(argv, cwd=cwd, env=env, capture_output=True,
                            text=True, timeout=timeout, check=False)
    require(result.returncode == 0, "bounded metadata/control command failed")
    return result.stdout


def source_pin(tree, expected, env):
    require(re.fullmatch(r"[0-9a-f]{40}", expected) is not None, "full source SHA required")
    tree = Path(tree)
    require(tree.is_absolute() and tree.resolve() == tree, "explicit real source tree required")
    actual = capture(["git", "-C", str(tree), "rev-parse", "HEAD"], env).strip()
    require(actual == expected, "source HEAD differs from the accepted pin")
    require(capture(["git", "-C", str(tree), "status", "--porcelain"], env) == "",
            "accepted source tree must be clean")
    return tree


def fixture_binary(path, parent):
    path = Path(path)
    require(path.is_absolute() and path.resolve() == path and path.is_relative_to(parent),
            "binary must be a test-local real file beneath the private fixture parent")
    info = path.stat()
    require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and
            not info.st_mode & 0o022 and info.st_nlink == 1 and os.access(path, os.X_OK),
            "binary must be owned, unshared, executable and not writable by other users")
    return path


def main_binary_pin(go_tool, binary, expected_sha, expected_module, _fixture_env):
    # Compiler metadata uses the maintained host resource owner and its caches.
    # Only server/owner-fixture processes get the isolated runtime environment.
    info = capture([str(go_tool), "version", "-m", str(binary)], None)
    fields = {}
    module = None
    for line in info.splitlines()[1:]:
        parts = line.strip().split("\t")
        if len(parts) == 2 and parts[0] == "path":
            module = parts[1]
        if len(parts) == 2 and parts[0] == "build" and "=" in parts[1]:
            key, value = parts[1].split("=", 1)
            fields[key] = value
    require(module == expected_module and fields.get("vcs.revision") == expected_sha and
            fields.get("vcs.modified") == "false", "binary lacks the exact clean source pin")
    return hashlib.sha256(binary.read_bytes()).hexdigest()


def write_private(path, body):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(body)


def save_policy(root, policy):
    temporary = root / "policy.next"
    write_private(temporary, json.dumps(policy, separators=(",", ":")).encode())
    os.replace(temporary, root / "policy.json")


def make_policy():
    tokens = {}
    for name in ("owner", "collector", "rotating", "revoked", "disabled-person",
                 "disabled-agent", "later", "host", "unknown"):
        tokens[name] = "pa1_" + base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
    for name in ("fleet-connector", "old-fleet", "initial-legacy", "recovery-legacy"):
        tokens[name] = "fixture-" + base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")
    policy = {
        "version": 1,
        "principals": [
            {"id": "fleet", "type": "system"},
            {"id": "david", "type": "person", "role": "owner", "projects": ["fixture"]},
            {"id": "retired", "type": "person", "role": "engineer", "disabled": True},
            {"id": "later", "type": "person", "role": "owner", "projects": ["fixture"]},
        ],
        "agents": [
            {"id": "sp-fleet", "principal_id": "fleet", "kind": "leaf", "address": "sp/fleet"},
            {"id": "concierge", "principal_id": "david", "kind": "concierge",
             "address": "people/david/concierge"},
            {"id": "disabled-agent", "principal_id": "david", "kind": "crew",
             "address": "people/david/disabled", "disabled": True},
            {"id": "retired", "principal_id": "retired", "kind": "crew", "address": "people/retired/crew"},
            {"id": "later", "principal_id": "later", "kind": "concierge", "address": "people/later/concierge"},
        ],
        "credentials": [],
        "project_streams": [
            {"project_id": "fixture", "stream": "cutover", "decides": "person:david",
             "informed": ["agent:concierge"]},
        ],
    }
    def credential(name, principal="david", agent="concierge", **extra):
        value = {"id": name, "device": "disposable-fixture", "principal_id": principal,
                 "agent_id": agent, "audience": "both",
                 "sha256": hashlib.sha256(tokens[name].encode()).hexdigest()}
        value.update(extra)
        policy["credentials"].append(value)
    credential("fleet-connector", "fleet", "sp-fleet", format="imported_fleet_v1")
    credential("old-fleet", "fleet", "sp-fleet", format="imported_fleet_v1", revoked=True)
    for name in ("owner", "rotating"):
        credential(name)
    credential("collector", audience="status-collector")
    credential("revoked", revoked=True)
    credential("disabled-person", "retired", "retired")
    credential("disabled-agent", agent="disabled-agent")
    credential("later", "later", "later")
    credential("host", "fleet", "", audience="agentmail-host", host="fixture-host")
    return policy, tokens


def retain_new_denials(policy, tokens):
    # Add a tombstone; never remove one to make a reader accept the policy.
    policy["credentials"].append({
        "id": "former-legacy", "device": "disposable-fixture", "principal_id": "fleet",
        "agent_id": "sp-fleet", "audience": "both", "format": "imported_fleet_v1",
        "sha256": hashlib.sha256(tokens["initial-legacy"].encode()).hexdigest(), "revoked": True,
    })
    next(c for c in policy["credentials"] if c["id"] == "rotating")["revoked"] = True
    next(p for p in policy["principals"] if p["id"] == "later")["disabled"] = True


def request(port, path, token, *, method="GET", body=None, pin=None):
    headers = {"Accept": "application/json, text/event-stream"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if pin is not None:
        headers[EXPECTED_HEADER] = pin
    encoded = None
    if body is not None:
        encoded = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=REQUEST_SECONDS)
    try:
        connection.request(method, path, body=encoded, headers=headers)
        response = connection.getresponse()
        data = response.read(MAX_BODY + 1)
        require(len(data) <= MAX_BODY, "fixture response exceeds its bound")
        return response.status, dict(response.getheaders()), data
    finally:
        connection.close()


def expect(port, path, token, status, label, **kwargs):
    actual, headers, body = request(port, path, token, **kwargs)
    require(actual == status, label + ": unexpected HTTP status " + str(actual))
    require("Location" not in headers, label + ": unexpected redirect")
    return headers, body


def initialize(port, path, token):
    _, body = expect(port, path, token, 200, "named MCP initialize", method="POST", body={
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                   "clientInfo": {"name": "disposable-recovery", "version": "1"}},
    })
    if body.startswith(b"event:") or body.startswith(b"data:"):
        messages = [json.loads(line[5:].strip()) for line in body.splitlines() if line.startswith(b"data:")]
        value = next((item for item in messages if item.get("id") == 1), {})
    else:
        value = json.loads(body)
    require("result" in value and "serverInfo" in value["result"] and "error" not in value,
            "named MCP initialize did not return an actual server result")


def collector_check(service, port, tokens):
    path = "/status/collector" if service == "go" else "/v1/status/collector"
    headers, body = expect(port, path, tokens["collector"], 200, "collector GET")
    require(len(body) <= 16 * 1024 and headers.get("Cache-Control") == "no-store" and
            headers.get("X-Content-Type-Options") == "nosniff", "collector response contract changed")
    value = json.loads(body)
    keys = {"version", "observed_at", "health", "activity"} if service == "go" else {"version", "observed_at", "mail"}
    require(set(value) == keys and value["version"] == 1, "collector schema version/keys changed")
    scope = value["activity"]["scope"] if service == "go" else value["mail"]["scope"]
    require(scope == ("all_projects" if service == "go" else "plans_control"), "collector scope changed")
    _, head = expect(port, path, tokens["collector"], 200, "collector HEAD", method="HEAD")
    require(head == b"", "collector HEAD emitted a body")
    expect(port, path + "?fixture=1", tokens["collector"], 403, "collector query refusal")
    expect(port, path, tokens["owner"], 401, "ordinary audience cannot collect")
    expect(port, path, tokens["host"], 401, "host audience cannot collect")


def auth_checks(service, port, root, policy, tokens, *, recovered):
    ordinary = "/healthz"
    fleet_pin = "fleet-connector" if service == "mail" else None
    expect(port, ordinary, tokens["fleet-connector"], 200, "unchanged named fleet", pin=fleet_pin)
    if service == "mail":
        expect(port, "/v1/identity", tokens["owner"], 200, "owner identity", pin="owner")
        expect(port, "/v1/identity", tokens["owner"], 401, "wrong expected credential", pin="wrong")
        expect(port, ordinary, tokens["fleet-connector"], 401, "imported REST requires pin")
    else:
        initialize(port, "/mcp", tokens["owner"])
    if not recovered:
        for name in ("rotating", "later"):
            if service == "mail":
                expect(port, "/v1/identity", tokens[name], 200, "initial active " + name, pin=name)
            else:
                initialize(port, "/mcp", tokens[name])
    for name in ("old-fleet", "revoked", "disabled-person", "disabled-agent", "unknown"):
        expect(port, ordinary, tokens[name], 401, "refused " + name, pin=name if service == "mail" else None)
    collector_check(service, port, tokens)
    expect(port, ordinary, tokens["collector"], 401, "collector cannot use ordinary HTTP")
    legacy = "recovery-legacy" if recovered else "initial-legacy"
    expect(port, ordinary, tokens[legacy], 401 if recovered else 200, "configured bridge control")
    if recovered:
        expect(port, ordinary, tokens["initial-legacy"], 401, "retained imported tombstone")
    # Fresh owner/concierge changes are checked on the confined collector path.
    path = "/status/collector" if service == "go" else "/v1/status/collector"
    for field in ("owner", "agent"):
        reduced = copy.deepcopy(policy)
        if field == "owner":
            next(p for p in reduced["principals"] if p["id"] == "david")["disabled"] = True
        else:
            next(a for a in reduced["agents"] if a["id"] == "concierge")["disabled"] = True
        save_policy(root, reduced)
        try:
            expect(port, path, tokens["collector"], 401, "fresh " + field + " reduction")
        finally:
            save_policy(root, policy)
    # Restore the latest policy, which already includes new revocations after stop.
    write_private(root / "policy.json", b"{")
    try:
        expect(port, path, tokens["collector"], 401 if service == "go" else 503,
               "invalid policy refuses collector")
    finally:
        save_policy(root, policy)


def new_denial_checks(service, port, tokens):
    # These entries were active/unrecognized at initial startup and must remain
    # denied after recovery. Collector checks avoid the ordinary auth rate budget.
    ordinary = "/mcp" if service == "go" else "/v1/identity"
    for name in ("rotating", "later"):
        expect(port, ordinary, tokens[name], 401, "new retained denial " + name,
               pin=name if service == "mail" else None)
    expect(port, "/status/collector" if service == "go" else "/v1/status/collector",
           tokens["initial-legacy"], 401, "legacy token has no collector grant")


def public_checks(port, tokens):
    expect(port, "/agentmail/healthz", tokens["owner"], 200, "public named health", pin="owner")
    initialize(port, "/agentmail/mcp", tokens["owner"])
    for path in ("/agentmail", "/agentmail/v1/identity", "/agentmail/v1/status/collector",
                 "/agentmail/healthz/", "/%61gentmail/healthz", "/agentmail//healthz"):
        expect(port, path, tokens["owner"], 404, "public path refusal")
    expect(port, "/agentmail/healthz", tokens["owner"], 401, "public wrong pin", pin="wrong")
    expect(port, "/agentmail/healthz", tokens["revoked"], 401, "public revoked credential")
    for path in ("/agentmail/mcp?agentmail_token=fixture", "/agentmail/healthz?agentmail_token="):
        expect(port, path, tokens["owner"], 401, "public query credential refusal")
    expect(port, "/v1/identity", tokens["owner"], 200, "private route retained", pin="owner")


def free_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def service_command(service, binary, root, port, recovered):
    common = ["--token-file", str(root / "shared-fixture.token"),
              "--principals-file", str(root / "policy.json"),
              "--listen", "127.0.0.1:" + str(port)]
    if service == "go":
        return [str(binary), *common, "--state-dir", str(root / "go-state"),
                "--projects-file", str(root / "projects.json"), "--limits-file", str(root / "limits.env"),
                "--codex-dir", str(root / "codex"), "--host-resources-helper", "",
                "--worktree-mode", "mcp", "--worktree-root", str(root / "worktrees"),
                "--work", "off", "--analysis=false", "--legacy-bridge=" + str(not recovered).lower()]
    command = [str(binary), "serve", *common, "--data-dir", str(root / "mail-state"),
               "--socket", str(root / "mail-state" / "fixture.sock"),
               "--host", "fixture-host", "--no-adapters", "--access-phase", "roles",
               "--allow-legacy=" + str(not recovered).lower()]
    if recovered:
        command += ["--public-prefix", "/agentmail"]
    return command


def start_service(service, binary, root, env, tokens, recovered):
    port = free_port()
    log = root / "logs" / (service + ("-recovered.log" if recovered else "-initial.log"))
    fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as output:
        process = subprocess.Popen(service_command(service, binary, root, port, recovered),
                                   cwd=root, env=env, stdout=output, stderr=subprocess.STDOUT)
    path = "/status/collector" if service == "go" else "/v1/status/collector"
    deadline = time.monotonic() + START_SECONDS
    try:
        while time.monotonic() < deadline:
            require(process.poll() is None, service + " exited before readiness; inspect its private fixture log")
            try:
                status, _, _ = request(port, path, tokens["collector"])
                if status == 200:
                    return process, port
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.05)
        raise Refusal(service + " did not publish a compatible collector within the fixed startup bound")
    except BaseException:
        stop_service(process, failure=True)
        raise


def stop_service(process, *, failure):
    if process.poll() is not None:
        return
    if failure:
        process.kill()
    else:
        process.terminate()
    try:
        process.wait(timeout=STOP_SECONDS)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=STOP_SECONDS)
        raise Refusal("fixture process did not stop within the fixed bound")


def owner_controls(binaries, root, env, phase):
    owner_env = dict(env, NAMED_POLICY_FIXTURE_PHASE=phase)
    controls = (
        ("workspace", "TestNamedPolicyWorkspaceStateTransfer"),
        ("jobs", "TestNamedPolicyJobStateTransfer"),
        ("mail", "TestNamedPolicyMailStateTransfer"),
    )
    for owner, test in controls:
        log = root / "logs" / (owner + "-" + phase + "-" + str(time.monotonic_ns()) + ".log")
        with log.open("xb") as stream:
            os.chmod(log, 0o600)
            result = subprocess.run([str(binaries[owner]), "-test.run=^" + test + "$", "-test.v",
                                     "-test.timeout=30s"], cwd=root, env=owner_env,
                                    stdout=stream, stderr=subprocess.STDOUT, timeout=35, check=False)
        require(result.returncode == 0, owner + " retained-state control failed; inspect its private fixture log")
        output = log.read_text()
        require("--- PASS: " + test in output and "--- SKIP:" not in output,
                owner + " fixture control did not execute")


def run(args):
    parent = private_directory(args.fixture_parent)
    root = Path(tempfile.mkdtemp(prefix="named-policy-recovery-", dir=parent))
    for name in ("home", "tmp", "codex", "logs"):
        (root / name).mkdir(mode=0o700)
    write_private(root / MARKER, MARKER_BODY.encode())
    env = fixture_environment(root)
    source_pin(args.go_tree, args.go_sha, env)
    source_pin(args.mail_tree, args.mail_sha, env)
    binaries = {name: fixture_binary(getattr(args, name + "_binary"), parent)
                for name in ("go", "mail", "workspace", "jobs", "store")}
    digests = {
        "go": main_binary_pin(args.go_tool, binaries["go"], args.go_sha,
                             "github.com/lyeith/codexpro/go/cmd/codexpro", env),
        "mail": main_binary_pin(args.go_tool, binaries["mail"], args.mail_sha,
                               "github.com/lyeith/agent-mailbox/cmd/agentmail", env),
    }
    controls = {"workspace": binaries["workspace"], "jobs": binaries["jobs"], "mail": binaries["store"]}
    owner_controls(controls, root, env, "seed")
    policy, tokens = make_policy()
    save_policy(root, policy)
    fleet_digest = hashlib.sha256(tokens["fleet-connector"].encode()).hexdigest()
    processes = {}
    try:
        for recovered in (False, True):
            token_name = "recovery-legacy" if recovered else "initial-legacy"
            write_private(root / "shared-fixture.token", (tokens[token_name] + "\n").encode())
            for service in ("go", "mail"):
                processes[service] = start_service(service, binaries[service], root, env, tokens, recovered)
            for service, (_, port) in processes.items():
                auth_checks(service, port, root, policy, tokens, recovered=recovered)
            if not recovered:
                retain_new_denials(policy, tokens)
                save_policy(root, policy)
                for service, (_, port) in processes.items():
                    # The configured shared value is now a retained imported tombstone:
                    # an ON bridge must not admit it as legacy.
                    expect(port, "/healthz", tokens["initial-legacy"], 401,
                           "recognized denial before enabled legacy bridge")
                    new_denial_checks(service, port, tokens)
            else:
                for service, (_, port) in processes.items():
                    new_denial_checks(service, port, tokens)
                public_checks(processes["mail"][1], tokens)
            expected_policy = json.dumps(policy, separators=(",", ":")).encode()
            require((root / "policy.json").read_bytes() == expected_policy,
                    "HTTP probes changed the latest policy")
            require(hashlib.sha256(tokens["fleet-connector"].encode()).hexdigest() == fleet_digest,
                    "fixture fleet bearer changed")
            for process, _ in processes.values():
                stop_service(process, failure=not recovered)
            processes.clear()
            owner_controls(controls, root, env, "check")
        return {
            "grade": "same-pair-recovery",
            "go_source": args.go_sha, "mail_source": args.mail_sha,
            "binary_sha256": digests,
            "fixture": str(root),
            "expires_at": (datetime.now(timezone.utc) + timedelta(days=7)).isoformat(),
            "checks": ["both bridge settings", "recognized denial before legacy", "fresh owner and concierge",
                       "correct and wrong expected ID", "retained revocation and disabled principal",
                       "invalid policy", "collector confinement and schema", "public-prefix routing",
                       "jobs and legacy provenance", "lease ownership", "native queue and managed intent"],
            "limitation": "Controlled failure and restart of the same accepted pair; no binary downgrade claim.",
        }
    finally:
        for process, _ in processes.values():
            stop_service(process, failure=True)


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("fixture-parent", "go-tool", "go-tree", "mail-tree", "go-sha", "mail-sha",
                 "go-binary", "mail-binary", "workspace-binary", "jobs-binary", "store-binary"):
        parser.add_argument("--" + name, required=True)
    return parser.parse_args()


if __name__ == "__main__":
    try:
        print(json.dumps(run(arguments()), sort_keys=True))
    except (Refusal, OSError, ValueError, subprocess.SubprocessError, http.client.HTTPException) as error:
        print(json.dumps({"grade": "FAILED", "reason": str(error)}))
        raise SystemExit(1)
