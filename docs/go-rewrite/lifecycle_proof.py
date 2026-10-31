#!/usr/bin/env python3
"""Bounded, candidate-only lifecycle proof using a wholly fresh private instance.

Refuses an occupied port 7210. Starts and signals only its own Popen child; never takes
over an existing server or changes a user service. The generated Git/helper
shims inject failures only in this child's environment. Run under nice -n 10 and
an external timeout of 400 seconds with a bounded cleanup grace. State, fixtures
and evidence remain available for review.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import shlex
import shutil
import signal
import socket
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time
import urllib.parse

from contract_suite import Client, raw_result
from process_contract_suite import private_http


class ProofFailure(AssertionError):
    pass


def check(value, message):
    if not value:
        raise ProofFailure(message)


def write_exclusive(path, content, mode=0o600):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    with os.fdopen(fd, "w") as stream:
        stream.write(content)
        stream.flush()
        os.fsync(stream.fileno())


def write_launch_receipt(path, value, *, new=False):
    """Publish a complete private launch record and sync its directory entry."""
    raw = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode()
    descriptor, temporary = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        if new:
            # Atomic creation also refuses an existing incarnation receipt.
            os.link(temporary, path, follow_symlinks=False)
            os.unlink(temporary)
        else:
            info = path.lstat()
            check(stat.S_ISREG(info.st_mode) and info.st_nlink == 1,
                  "launch receipt was replaced by an unexpected file")
            os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if os.path.lexists(temporary): os.unlink(temporary)


def process_identity(pid):
    try:
        text = Path("/proc", str(pid), "stat").read_text()
    except FileNotFoundError:
        return None
    fields = text[text.rfind(")") + 1:].split()
    check(len(fields) >= 20, "short process identity")
    return {"pid": pid, "state": fields[0], "identity": fields[19]}


def alive(witness):
    current = process_identity(witness["pid"])
    return current is not None and current["state"] not in {"Z", "X"} and current["identity"] == witness["identity"]


def cgroup_for(pid):
    for line in Path("/proc", str(pid), "cgroup").read_text().splitlines():
        if line.startswith("0::/"):
            return line[3:]
    raise ProofFailure("unified cgroup identity is missing")


def cgroup_population(identity):
    check(identity and re.fullmatch(r"codexpro-job_[0-9a-f]{8}-[0-9a-f]{32}\.scope", identity.get("unit", "")),
          "job scope identity is invalid")
    group = identity.get("cgroup", "")
    check(group.startswith("/") and Path(group).name == identity["unit"], "scope cgroup does not match its unit")
    root = (Path("/sys/fs/cgroup") / group.lstrip("/")).resolve()
    check(root.is_relative_to(Path("/sys/fs/cgroup")), "scope cgroup escaped kernel root")
    if not root.exists():
        return {"exists": False, "populated": 0}
    properties = dict(line.split() for line in (root / "cgroup.events").read_text().splitlines())
    return {"exists": True, "populated": int(properties["populated"])}


CHILD_SOURCE = r'''#!/usr/bin/env python3
import json, os, pathlib, signal, sys, time
mode, destination, control, nonce = sys.argv[1:]
destination, control = pathlib.Path(destination), pathlib.Path(control)
def witness(path, role):
    fields = pathlib.Path('/proc/self/stat').read_text().rsplit(')', 1)[1].split()
    cgroup = next(line[3:] for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if line.startswith('0::/'))
    value = {'nonce': nonce, 'role': role, 'pid': os.getpid(), 'identity': fields[19],
             'pgid': os.getpgrp(), 'sid': os.getsid(0), 'nice': os.getpriority(os.PRIO_PROCESS, 0), 'cgroup': cgroup}
    temporary = pathlib.Path(str(path) + '.tmp')
    temporary.write_text(json.dumps(value)); temporary.chmod(0o600); temporary.replace(path)
    return value
with open(str(destination) + '.launched', 'x') as marker: marker.write('one accepted execution\n')
witness(pathlib.Path(str(destination) + '.parent'), 'command')
if mode == 'restart':
    witness(destination, 'command')
    print('before-restart', flush=True)
    deadline = time.monotonic() + 35
    while not control.exists() and time.monotonic() < deadline: time.sleep(.02)
    if not control.exists(): sys.exit(9)
    print('after-restart', flush=True); sys.exit(0)
pid = os.fork()
if pid == 0:
    os.setsid(); signal.signal(signal.SIGTERM, signal.SIG_IGN)
    null = os.open('/dev/null', os.O_RDWR)
    for stream in (0, 1, 2): os.dup2(null, stream)
    if null > 2: os.close(null)
    witness(destination, 'setsid-descendant')
    time.sleep(35); os._exit(0)
if mode != 'success': signal.signal(signal.SIGTERM, signal.SIG_IGN)
deadline = time.monotonic() + 4
while not destination.exists() and time.monotonic() < deadline: time.sleep(.01)
if not destination.exists(): sys.exit(8)
print('escaped-child-ready', flush=True)
deadline = time.monotonic() + 35
while not control.exists() and time.monotonic() < deadline: time.sleep(.02)
if mode == 'success' and control.exists(): print('normal-parent-finished', flush=True); sys.exit(0)
sys.exit(9)
'''


GIT_SOURCE = r'''#!/usr/bin/env python3
import os, pathlib, sys
control = pathlib.Path(__SP6_CONTROL__)
if (control / 'fail-git-probe').exists() and sys.argv[1:3] == ['rev-parse', '--show-toplevel']:
    (control / 'git-failure-observed').write_text('transient probe injected\n')
    sys.stderr.write('SP6 injected transient Git inspection failure\n'); sys.exit(75)
os.execv(__SP6_GIT__, [__SP6_GIT__] + sys.argv[1:])
'''


HELPER_SOURCE = r'''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys, time
control = pathlib.Path(__SP6_CONTROL__)
real = __SP6_HELPER__
def persist(name, value):
    destination = control / name; temporary = control / (name + '.tmp')
    temporary.write_text(json.dumps(value)); temporary.chmod(0o600); temporary.replace(destination)
if sys.argv[1:3] == ['external', 'begin'] and (control / 'delay-begin').exists():
    fields = pathlib.Path('/proc/self/stat').read_text().rsplit(')', 1)[1].split()
    args = sys.argv[1:]
    value = {'pid': os.getpid(), 'identity': fields[19],
             'run_id': args[args.index('--run-id') + 1], 'registration_id': args[args.index('--registration-id') + 1]}
    persist('begin-entered.json', value)
    deadline = time.monotonic() + 60
    while not (control / 'release-begin').exists() and time.monotonic() < deadline: time.sleep(.02)
    if not (control / 'release-begin').exists(): sys.exit(124)
    result = subprocess.run([real] + args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=12)
    value.update({'returncode': result.returncode, 'stdout': result.stdout.decode(errors='replace')[:16000],
                  'stderr': result.stderr.decode(errors='replace')[:4000]})
    persist('begin-returned.json', value)
    try:
        sys.stdout.buffer.write(result.stdout); sys.stderr.buffer.write(result.stderr)
    except BrokenPipeError: pass
    sys.exit(result.returncode)
os.execv(real, [real] + sys.argv[1:])
'''


class Probe:
    def __init__(self, binary, helper, parent, output):
        self.binary, self.helper = Path(binary).resolve(strict=True), Path(helper).resolve(strict=True)
        check(self.binary.is_file() and os.access(self.binary, os.X_OK) and self.binary.stat().st_size <= 268435456,
              "candidate must be a bounded explicit executable")
        with self.binary.open("rb") as executable:
            check(executable.read(4) == b"\x7fELF", "candidate must be an explicit built Go executable")
        check(self.helper.is_file() and os.access(self.helper, os.X_OK), "host resource helper must be an explicit executable")
        self.parent, self.output = Path(parent).expanduser().resolve(), Path(output).expanduser().resolve()
        homes = {Path.home().resolve(), Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()}
        private_roots = {home / ".codexpro-go-proof" for home in homes}
        check(any(self.parent.is_relative_to(root) and self.parent != root for root in private_roots)
              or str(self.parent).startswith("/tmp/sp6-lifecycle-"), "probe state must use a private proof root")
        forbidden = [root.resolve() for home in homes for root in (home / ".codexpro", home / ".config" / "codexpro",
                    home / "Projects" / "codexpro", home / "apps" / "codexpro")]
        check(not any(path.is_relative_to(root) for path in (self.parent, self.output) for root in forbidden), "live path refused")
        check(not self.parent.exists() and not self.output.exists(), "proof refuses to reuse state or overwrite evidence")
        self.assert_free_port()
        self.parent.mkdir(parents=True, mode=0o700)
        self.state, self.repository, self.control = self.parent / "state", self.parent / "repository", self.parent / "control"
        self.bin, self.witness_dir = self.parent / "bin", self.parent / "witnesses"
        self.launch_dir, self.launch_number = self.parent / "launches", 0
        for path in (self.state, self.repository, self.control, self.bin, self.witness_dir, self.launch_dir): path.mkdir(mode=0o700)
        self.nonce = secrets.token_hex(16)
        self.token = self.state / "http-token"
        write_exclusive(self.token, secrets.token_urlsafe(48) + "\n")
        self.child_script, self.helper_wrapper = self.bin / "fixture-child", self.bin / "fixture-helper"
        real_git = shutil.which("git")
        check(real_git, "Git is required for the own fixture")
        # Helper exec runs with the deliberately restricted child environment;
        # private shim controls therefore live in the script, not inherited vars.
        for path, body in ((self.child_script, CHILD_SOURCE), (self.helper_wrapper, HELPER_SOURCE), (self.bin / "git", GIT_SOURCE)):
            body = body.replace("__SP6_CONTROL__", repr(str(self.control))).replace("__SP6_HELPER__", repr(str(self.helper))).replace("__SP6_GIT__", repr(real_git))
            write_exclusive(path, body, 0o700)
        env = {k: v for k, v in os.environ.items() if not k.startswith("CODEXPRO_") and k != "CODEX_HOME"}
        env.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
                    "GIT_AUTHOR_NAME": "SP6 lifecycle fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
                    "GIT_COMMITTER_NAME": "SP6 lifecycle fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"})
        write_exclusive(self.repository / "README.md", "# Owned lifecycle fixture\n")
        self.command([real_git, "init", "--quiet", "--initial-branch=main"], cwd=self.repository, env=env)
        self.command([real_git, "add", "README.md"], cwd=self.repository, env=env)
        self.command([real_git, "-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "test: lifecycle proof fixture"], cwd=self.repository, env=env)
        write_exclusive(self.state / "projects.json", json.dumps({"version": 1, "defaultProject": "contract", "projects": [
            {"id": "contract", "label": "Owned lifecycle proof", "root": str(self.repository), "baseRef": "HEAD", "maxWorktrees": 4}]}))
        env.update({"PATH": str(self.bin) + os.pathsep + env.get("PATH", "/usr/bin:/bin"),
                    "CODEXPRO_JOB_SCOPES": "1",
                    "CODEXPRO_JOB_TIMEOUT_MS": "60000", "CODEXPRO_MAX_JOBS": "4", "CODEXPRO_MAX_JOBS_PER_WORKSPACE": "4",
                    "CODEXPRO_AUDIT_RETAIN_ACTIONS": "1000"})
        self.environment, self.process, self.client = env, None, None
        self.workspace, self.owned_jobs, self.witnesses = None, set(), []
        self.admission_intents = set()
        self.deadline = time.monotonic() + 240
        self.report = {"schema_version": 1, "candidate_only": True, "source_commit": "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a",
                       "started_at": dt.datetime.now(dt.timezone.utc).isoformat(), "state": str(self.state), "port": 7210,
                       "binary": str(self.binary), "binary_sha256": hashlib.sha256(self.binary.read_bytes()).hexdigest(),
                       "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "server_incarnations": [],
                       "launch_receipts": [], "scenarios": [], "cleanup_errors": []}
        self.log = (self.parent / "server.log").open("xb")
        private_http()

    @staticmethod
    def assert_free_port():
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try: listener.bind(("127.0.0.1", 7210))
            except OSError as error: raise ProofFailure("port7210 is occupied; this proof will not signal or replace that server") from error

    def command(self, argv, timeout=10, **options):
        result = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, **options)
        check(result.returncode == 0, "bounded fixture command failed: " + Path(argv[0]).name + ": " + result.stderr.decode(errors="replace")[:2000])
        return result

    def wait(self, label, predicate, seconds=10):
        end = min(self.deadline, time.monotonic() + seconds)
        while time.monotonic() < end:
            value = predicate()
            if value: return value
            time.sleep(.1)
        raise ProofFailure("bounded wait expired: " + label)

    def start_server(self):
        check(self.process is None or self.process.poll() is not None, "owned server is still running")
        self.assert_free_port()
        argv = [str(self.binary), "--state-dir", str(self.state), "--listen", "127.0.0.1:7210",
                "--token-file", str(self.token), "--projects-file", str(self.state / "projects.json"),
                "--host-resources-helper", str(self.helper_wrapper), "--codex-dir", str(self.parent / "codex"),
                "--worktree-mode", "mcp", "--tool-mode", "standard", "--bash", "full", "--write", "workspace"]
        self.launch_number += 1
        receipt_path = self.launch_dir / ("server-%03d.json" % self.launch_number)
        receipt = {"schema_version": 1, "kind": "sp6-lifecycle-owned-server", "incarnation": self.launch_number,
                   "phase": "prepared", "prepared_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                   "proof_root": str(self.parent), "state_root": str(self.state), "port": 7210,
                   "binary": str(self.binary), "binary_sha256": self.report["binary_sha256"],
                   "boot_id": self.report["boot_id"], "pid": None, "process_start_ticks": None,
                   "cgroup": None, "process_identity_verified": False}
        write_launch_receipt(receipt_path, receipt, new=True)
        self.report["launch_receipts"].append(str(receipt_path))
        try:
            self.process = subprocess.Popen(argv, cwd=self.parent, env=self.environment, stdin=subprocess.DEVNULL,
                                            stdout=self.log, stderr=self.log, start_new_session=True)
        except Exception as error:
            # An interrupted spawn can have an unknown outcome. This is never
            # authority to infer a PID from the port or a matching process name.
            receipt.update({"phase": "launch_outcome_unknown", "error_class": type(error).__name__})
            write_launch_receipt(receipt_path, receipt)
            raise
        receipt.update({"phase": "spawned_unverified", "pid": self.process.pid})
        write_launch_receipt(receipt_path, receipt)
        incarnation = self.wait("owned server identity", lambda: process_identity(self.process.pid), 3)
        incarnation["cgroup"] = cgroup_for(self.process.pid)
        current = process_identity(self.process.pid)
        check(self.process.poll() is None and current and current["identity"] == incarnation["identity"],
              "owned server identity changed before publication")
        receipt.update({"phase": "identified", "process_start_ticks": int(incarnation["identity"]),
                        "cgroup": incarnation["cgroup"], "process_identity_verified": True})
        write_launch_receipt(receipt_path, receipt)
        incarnation["launch_receipt"] = str(receipt_path)
        self.report["server_incarnations"].append(incarnation)
        end = time.monotonic() + 15
        while time.monotonic() < end:
            check(self.process.poll() is None, "owned candidate exited during startup; inspect private server.log")
            try:
                self.client = Client("http://127.0.0.1:7210/mcp", self.token, timeout=12)
                current = process_identity(self.process.pid)
                check(self.process.poll() is None and current and current["identity"] == incarnation["identity"],
                      "owned server identity changed during initialization")
                receipt.update({"phase": "ready", "ready_at": dt.datetime.now(dt.timezone.utc).isoformat()})
                write_launch_receipt(receipt_path, receipt)
                return
            except (OSError, RuntimeError): time.sleep(.2)
        raise ProofFailure("owned candidate did not initialize within15 seconds")

    def stop_server(self, hard=False):
        if self.process is None or self.process.poll() is not None: return
        if not hard and self.client is not None: self.client.close()
        self.client = None
        # Popen retains exact process ownership; no process lookup, name match,
        # system service, listening PID or process-group broadcast is used.
        self.process.send_signal(signal.SIGKILL if hard else signal.SIGTERM)
        try: self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill(); self.process.wait(timeout=3)

    def call(self, name, arguments, error=False):
        check(self.process is not None and self.process.poll() is None, "owned server is not available")
        check(time.monotonic() < self.deadline, "global lifecycle scenario deadline expired")
        # A lost RPC response must not erase our ownership of an already-durable
        # admission. Record exact submitted intents before crossing that boundary.
        commands = arguments.get("commands", []) if name == "start_jobs" else [arguments] if name == "bash" else []
        for command in commands:
            text = command.get("command", "")
            check(isinstance(text, str) and self.nonce in text, "lifecycle admission lacks this proof's nonce")
            self.admission_intents.add((arguments.get("workspace_id"), text, command.get("cwd", ".")))
        response = self.client.call(name, arguments)
        check(response["http_status"] == 200, "MCP transport failure in " + name)
        result = raw_result(response)
        check((result.get("isError") is True) == error, "unexpected tool outcome in " + name + ": " + str(result.get("structuredContent", {}).get("error", "")))
        data = result.get("structuredContent", {})
        if name == "start_jobs": self.owned_jobs.update(data.get("job_ids", []))
        if name == "bash" and data.get("job_id"): self.owned_jobs.add(data["job_id"])
        return data

    def rows(self, filename, table, column):
        path = self.state / filename
        if not path.exists(): return []
        uri = "file:" + urllib.parse.quote(str(path), safe="/") + "?mode=ro"
        with sqlite3.connect(uri, uri=True, timeout=1) as connection:
            rows = connection.execute("SELECT " + column + " FROM " + table + " LIMIT 32").fetchall()
        check(len(rows) < 32, "private fixture acquired an unexpected number of durable records")
        return [json.loads(row[0]) for row in rows]

    def jobs(self): return self.rows("jobs.sqlite", "jobs", "payload")
    def leases(self): return self.rows("workspaces.sqlite", "leases", "body")

    def job(self, job_id):
        return next((row for row in self.jobs() if row["id"] == job_id), None)

    def lease(self):
        return next((row for row in self.leases() if row["workspaceId"] == self.workspace), None)

    def record(self, name, evidence):
        self.report["scenarios"].append({"name": name, "status": "PASS", "evidence": evidence})
        print(json.dumps({"scenario": name, "status": "PASS"}), flush=True)

    def witness(self, path):
        def load():
            try: value = json.loads(path.read_text())
            except (FileNotFoundError, json.JSONDecodeError): return None
            check(value.get("nonce") == self.nonce and type(value.get("pid")) is int and value["pid"] > 1,
                  "child witness does not belong to this proof")
            check(re.fullmatch(r"[0-9]+", value.get("identity", "")), "child start identity is missing")
            return value
        value = self.wait("child witness", load, 4)
        if value not in self.witnesses: self.witnesses.append(value)
        return value

    def child_command(self, name, mode):
        witness, release = self.witness_dir / (name + ".json"), self.control / (name + ".release")
        return shlex.join([sys.executable, str(self.child_script), mode, str(witness), str(release), self.nonce]), witness, release

    def start_job(self, command):
        data = self.call("start_jobs", {"workspace_id": self.workspace, "commands": [{"command": command, "cwd": "."}]})
        check(len(data.get("job_ids", [])) == 1, "one-command admission did not return one job")
        return data["job_ids"][0]

    def quiet(self, job_id, witness=None):
        row = self.wait("durable process settlement", lambda: (r if (r := self.job(job_id)) and r["status"] != "running" and r.get("quiescent") else None), 15)
        check(row.get("quiescence_scope") == "systemd_scope", "scope job reported only process-group quiescence")
        check(row.get("resource_claim") and row.get("scope_identity"), "exact resource/scope identity was not retained")
        population = cgroup_population(row["scope_identity"])
        check(population["populated"] == 0, "terminal job still has populated scope")
        if witness:
            self.wait("actual descendant exit", lambda: not alive(witness), 2)
        return {"record": row, "cgroup": population}

    def create_workspace(self):
        data = self.call("create_workspace", {"project_id": "contract", "idempotency_key": self.nonce,
                         "include_tree": False, "include_skills": False, "include_global_skills": False})
        self.workspace = data["workspace_id"]
        lease = self.lease()
        check(lease and lease["state"] == "ready" and Path(lease["workspaceRoot"]).resolve().is_relative_to(self.state),
              "created workspace does not belong to private candidate state")
        repeated = self.call("create_workspace", {"project_id": "contract", "idempotency_key": self.nonce,
                            "include_tree": False, "include_skills": False, "include_global_skills": False})
        check(repeated["workspace_id"] == self.workspace and len(self.leases()) == 1, "idempotent creation duplicated lease")
        self.record("managed-create-idempotency", {"workspace_id": self.workspace, "lease": lease})

    def restart_running_job(self):
        command, path, release = self.child_command("restart", "restart")
        job_id = self.start_job(command)
        witness = self.witness(path)
        check(alive(witness), "restart command is not running before crash")
        initial = self.wait("initial scope identity", lambda: (r if (r := self.job(job_id)) and r.get("scope_identity") else None), 5)
        page = self.call("jobs", {"workspace_id": self.workspace, "job_ids": [job_id], "output": "incremental", "max_bytes": 256, "wait_ms": 1000})
        view = page["jobs"][0]
        check(view["stdout"] == "before-restart\n", "restart fixture prefix was not captured")
        cursor = view["next_cursor"]
        self.stop_server(hard=True)
        check(alive(witness), "detached accepted command died with the HTTP server")
        self.start_server()
        restored = self.wait("restored active record", lambda: (r if (r := self.job(job_id)) and r["status"] == "running" else None), 5)
        check(restored["scope_identity"] == initial["scope_identity"] and restored["resource_claim"] == initial["resource_claim"],
              "restart changed job scope or host registration identity")
        check(alive(witness), "restart replaced the accepted command")
        write_exclusive(release, "release\n")
        tail = self.call("jobs", {"workspace_id": self.workspace, "job_ids": [job_id], "output": "incremental", "cursor": cursor,
                                "max_bytes": 256, "wait_ms": 5000})
        check(tail["jobs"][0]["stdout"] == "after-restart\n", "saved cursor did not resume exact output after restart")
        settled = self.quiet(job_id, witness)
        check(settled["record"]["status"] == "succeeded", "accepted command did not finish successfully")
        self.record("server-crash-retains-command-and-cursor", {"job_id": job_id, "witness": witness, "settlement": settled})

    def descendant(self, outcome):
        command, path, release = self.child_command("descendant-" + outcome, "success" if outcome == "success" else "hold")
        if outcome == "timeout":
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as worker:
                future = worker.submit(self.call, "bash", {"workspace_id": self.workspace, "command": command, "cwd": ".", "timeout_ms": 3000, "on_timeout": "kill"})
                child = self.witness(path)
                check(alive(child), "timeout descendant was never observed alive")
                result = future.result(timeout=15)
            job_id = result["job_id"]
            check(result.get("timed_out") is True, "request-owned timeout was not enforced")
        else:
            job_id = self.start_job(command)
            child = self.witness(path)
            check(alive(child), "escaped descendant was never observed alive")
            refusal = self.call("remove_workspace", {"workspace_id": self.workspace}, error=True)
            check("Workspace still has active operations." in refusal.get("error", ""), "active job did not protect clean workspace from removal")
            if outcome == "success": write_exclusive(release, "release\n")
            else: self.call("stop_jobs", {"workspace_id": self.workspace, "job_ids": [job_id]})
        parent = self.witness(Path(str(path) + ".parent"))
        check(child["sid"] == child["pid"] and child["pgid"] == child["pid"], "fixture did not escape the original process group")
        check(child["nice"] >= 10, "tool child did not retain reduced scheduling priority")
        settled = self.quiet(job_id, child)
        check(child["cgroup"] == settled["record"]["scope_identity"]["cgroup"], "escaped child left its registered scope")
        check(child["cgroup"] != self.report["server_incarnations"][-1]["cgroup"], "tool child shares server cgroup")
        expected = {"success": "succeeded", "stop": "stopped", "timeout": "timed_out"}[outcome]
        check(settled["record"]["status"] == expected, "terminal outcome changed during descendant settlement")
        check(not alive(parent), "command parent survived scope settlement")
        self.record("setsid-descendant-" + outcome, {"child": child, "parent": parent, "settlement": settled})

    def crash_pending_begin(self):
        write_exclusive(self.control / "delay-begin", "delay\n")
        command = shlex.join([sys.executable, "-c", "from pathlib import Path; Path(" + repr(str(self.control / "must-not-run")) + ").write_text('executed')", self.nonce])
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as worker:
            future = worker.submit(self.start_job, command)
            pending_path = self.control / "begin-entered.json"
            pending = self.wait("delayed host registration", lambda: json.loads(pending_path.read_text()) if pending_path.exists() else None, 5)
            row = self.wait("prepared durable admission", lambda: next((r for r in self.jobs() if self.nonce in r["command"] and r.get("launch_state") == "prepared"), None), 3)
            self.owned_jobs.add(row["id"])
            check(row.get("pid") == 0 and row["resource_claim"]["run_id"] == pending["run_id"]
                  and row["resource_claim"]["registration_id"] == pending["registration_id"], "prepared admission did not persist exact caller identities")
            self.witnesses.append(pending)
            self.stop_server(hard=True)
            try: future.result(timeout=15)
            except Exception: pass
        self.start_server()
        restored = self.wait("prepared owner recovery", lambda: (r if (r := self.job(row["id"])) and r["status"] != "running" and r.get("quiescent") else None), 10)
        write_exclusive(self.control / "release-begin", "release\n")
        returned_path = self.control / "begin-returned.json"
        late = self.wait("late helper begin rejected", lambda: json.loads(returned_path.read_text()) if returned_path.exists() else None, 15)
        check(late["returncode"] != 0, "late host allocation activated after recovery declared it quiescent")
        check(not (self.control / "must-not-run").exists() and not Path(row["grant_path"]).exists(), "ungranted command executed after server crash")
        self.wait("delayed helper exited", lambda: not alive(pending), 2)
        self.record("crash-before-begin-late-activation-refused", {"prepared": row, "recovered": restored, "late_begin": late})

    def transient_lease_probe(self):
        baseline = self.lease()
        check(baseline["state"] == "ready", "lease was not ready before transient probe")
        identity = {k: baseline[k] for k in ("workspaceId", "ownerId", "workspaceRoot", "repositoryCommonDir", "branch", "baseCommit")}
        observed = []
        def health(expected):
            row = self.lease()
            check(row is not None and row["state"] == "ready", "transient probe changed lease ownership state")
            check({k: row[k] for k in identity} == identity, "transient probe changed lease identity")
            if not observed or observed[-1]["revision"] != row["revision"]:
                observed.append({k: row.get(k) for k in ("revision", "state", "health", "failure", "observedAt")})
            return row if row.get("health") == expected else None
        write_exclusive(self.control / "fail-git-probe", "fail only own candidate Git inspection\n")
        unavailable = self.wait("periodic transient Git failure", lambda: health("unavailable"), 40)
        check((self.control / "git-failure-observed").exists(), "fault did not reach actual Git inspection")
        (self.control / "fail-git-probe").unlink()
        recovered = self.wait("periodic lease health recovery", lambda: health("healthy"), 40)
        check(recovered["revision"] > unavailable["revision"], "healthy state did not follow unavailable observation")
        opened = self.call("open_workspace", {"workspace_id": self.workspace, "include_tree": False, "include_skills": False})
        check(opened["workspace_id"] == self.workspace, "recovered lease cannot be opened")
        self.record("transient-git-probe-preserves-owner-and-heals", {"identity": identity, "durable_observations": observed, "orphaned_observations": 0})

    def final_remove(self):
        rows = self.jobs()
        check(rows and all(r.get("quiescent") and r["status"] != "running" for r in rows), "unresolved job ownership remains before final removal")
        check(all(not alive(w) for w in self.witnesses), "an observed descendant remains alive")
        self.call("release_workspace", {"workspace_id": self.workspace})
        self.call("remove_workspace", {"workspace_id": self.workspace})
        check(not self.leases(), "removed lease remains durable")
        self.record("final-zero-unresolved-owners-and-clean-removal", {"job_records": len(rows), "unresolved_owners": 0,
                    "observed_processes": len(self.witnesses), "surviving_observed_processes": 0, "remaining_leases": 0})

    def cleanup(self):
        # First stop only our Popen server, freezing new admissions before the
        # durable snapshot. Its detached jobs still belong to their exact host
        # registrations. Cancel those before releasing the delayed begin shim.
        try: self.stop_server()
        except Exception as error: self.report["cleanup_errors"].append("owned server stop failed: " + type(error).__name__)
        failed = any(s["status"] != "PASS" for s in self.report["scenarios"])
        try: rows = self.jobs()
        except Exception as error:
            rows = []
            self.report["cleanup_errors"].append("private job snapshot unavailable during cleanup: " + type(error).__name__)
        for row in rows:
            identifier, command = row.get("id", ""), row.get("command", "")
            if not re.fullmatch(r"job_[0-9a-f]{8}", identifier) or self.nonce not in command: continue
            intent = (row.get("workspace_id"), command, row.get("cwd", "."))
            if intent not in self.admission_intents: continue
            root = Path(row.get("root", "")).resolve()
            if row.get("workspace_id") != self.workspace or not root.is_relative_to(self.state):
                self.report["cleanup_errors"].append("admission does not match this private workspace")
                continue
            if identifier not in self.owned_jobs:
                self.owned_jobs.add(identifier)
                self.report.setdefault("uncertain_admissions_recovered_for_cleanup", []).append(identifier)
            claim = row.get("resource_claim")
            if not claim or row.get("quiescent") and not failed: continue
            if claim.get("helper") != str(self.helper_wrapper) or not all(re.fullmatch(r"[a-f0-9]{32}", claim.get(k, "")) for k in ("run_id", "registration_id")):
                self.report["cleanup_errors"].append("owned record lacks verifiable resource identity"); continue
            try:
                result = self.command([str(self.helper), "external", "cancel", claim["run_id"], "--registration-id", claim["registration_id"]], timeout=12)
                receipt = json.loads(result.stdout)
                check(receipt.get("run_id") == claim["run_id"] and receipt.get("quiescent") and receipt.get("cancelled"), "cleanup receipt did not settle exact owner")
            except Exception as error: self.report["cleanup_errors"].append(type(error).__name__ + ": " + str(error))
        (self.control / "release-begin").touch(mode=0o600, exist_ok=True)
        for witness in self.witnesses:
            if alive(witness):
                self.report["cleanup_errors"].append("candidate left an observed own process alive; exact pidfd cleanup required")
                if hasattr(os, "pidfd_open") and hasattr(signal, "pidfd_send_signal"):
                    try:
                        fd = os.pidfd_open(witness["pid"])
                        try:
                            if alive(witness): signal.pidfd_send_signal(fd, signal.SIGKILL)
                        finally: os.close(fd)
                    except ProcessLookupError: pass
        self.log.close()
        self.report["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
        self.report["passed"] = len(self.report["scenarios"]) == 8 and all(s["status"] == "PASS" for s in self.report["scenarios"]) and not self.report["cleanup_errors"]
        credential = self.token.read_text().strip()
        encoded = json.dumps(self.report, ensure_ascii=False, indent=2) + "\n"
        check(credential not in encoded, "credential appeared in proof evidence")
        self.output.parent.mkdir(parents=True, exist_ok=True)
        write_exclusive(self.output, encoded)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--helper", required=True)
    parser.add_argument("--state-parent", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if os.getpriority(os.PRIO_PROCESS, 0) < 10: os.nice(10 - os.getpriority(os.PRIO_PROCESS, 0))
    probe = Probe(args.binary, args.helper, args.state_parent, args.output)
    def interrupted(signum, frame):
        raise ProofFailure("lifecycle proof interrupted by signal %d; settling owned resources" % signum)
    previous_term = signal.signal(signal.SIGTERM, interrupted)
    try:
        probe.start_server()
        probe.create_workspace()
        probe.restart_running_job()
        for outcome in ("success", "stop", "timeout"): probe.descendant(outcome)
        probe.crash_pending_begin()
        probe.transient_lease_probe()
        probe.final_remove()
    except Exception as error:
        probe.report["scenarios"].append({"name": "interrupted-proof", "status": "FAIL", "error": type(error).__name__ + ": " + str(error)})
        print(json.dumps({"status": "FAIL", "error": type(error).__name__ + ": " + str(error)}), flush=True)
    finally:
        try:
            probe.cleanup()
        finally:
            signal.signal(signal.SIGTERM, previous_term)
    print(json.dumps({"passed": probe.report["passed"], "output": str(probe.output)}), flush=True)
    return 0 if probe.report["passed"] else 1


if __name__ == "__main__": raise SystemExit(main())
