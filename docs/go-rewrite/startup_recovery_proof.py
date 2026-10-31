#!/usr/bin/env python3
"""Bounded candidate-only startup proof with real pending recovery and CPU load."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import sqlite3
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request

from contract_suite import Client, SOURCE_COMMIT, raw_result
from paired_contract_suite import artifact_path, private_path, validate_launch
from managed_contract_suite import OwnedServer, assert_free_ports, sanitized_environment
from lifecycle_proof import write_launch_receipt, process_identity, alive, cgroup_for
from process_contract_suite import private_http

ARCHIVE_BYTES = 257908736
ARCHIVE_SHA = "50d09a5cc88c068f897e8734676d784488c9828d65b4da2ae7024f48483f0adf"
TARGET_MS = 3000


class Failure(AssertionError): pass


def check(value, message):
    if not value: raise Failure(message)


def now(): return dt.datetime.now(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def exclusive(path, value, mode=0o600):
    raw = value.encode() if isinstance(value, str) else value
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as file:
        file.write(raw); file.flush(); os.fsync(file.fileno())


def archive_identity(path, expected_sha=ARCHIVE_SHA, expected_size=ARCHIVE_BYTES):
    path = private_path(path)
    homes = {Path.home().resolve()}
    check(any(path.is_relative_to(home / ".codexpro-go-proof") for home in homes), "archive must be an existing private proof snapshot")
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as file:
        before = os.fstat(file.fileno())
        check(stat.S_ISREG(before.st_mode) and before.st_size == expected_size, "private archive has the wrong type/size")
        digest = hashlib.file_digest(file, "sha256").hexdigest()
        after = os.fstat(file.fileno())
    signature = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
    check(signature(before) == signature(after) == signature(path.stat()), "archive changed during provenance verification")
    check(digest == expected_sha, "private archive does not match the captured source hash")
    check(not Path(str(path) + "-wal").exists() or Path(str(path) + "-wal").stat().st_size == 0, "archive has an uncheckpointed WAL")
    return path, {"path": str(path), "sha256": digest, "bytes": before.st_size,
                  "device": before.st_dev, "inode": before.st_ino, "mtime_ns": before.st_mtime_ns, "ctime_ns": before.st_ctime_ns}


def process_io(pid):
    return {key: int(value.strip()) for key, value in (line.split(":", 1) for line in Path(f"/proc/{pid}/io").read_text().splitlines())}


def cpu_seconds(pid):
    tail = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
    return (int(tail[11]) + int(tail[12])) / os.sysconf("SC_CLK_TCK")


def valid_helper_witness(value, nonce, helper):
    check(isinstance(value, dict) and value.get("nonce") == nonce and value.get("helper") == str(helper), "helper marker is not bound to this proof")
    check(type(value.get("pid")) is int and value["pid"] > 1 and re.fullmatch(r"[0-9]+", value.get("identity", "")), "helper marker lacks an exact process identity")
    check(value.get("group") == value["pid"], "control helper did not enter its own temporary group")
    check(isinstance(value.get("cgroup"), str) and value["cgroup"].startswith("/"), "control helper lacks its cgroup identity")
    check(type(value.get("entered_ns")) is int and value["entered_ns"] > 0, "control helper lacks its entry time")
    return value


def valid_cpu_witness(value, nonce, role, pid, identity):
    check(isinstance(value, dict) and value.get("nonce") == nonce and value.get("role") == role,
          "CPU marker is not bound to this proof and role")
    check(value.get("pid") == pid and value.get("identity") == str(identity), "CPU marker has a different process incarnation")
    check(type(value.get("nice")) is int and value["nice"] >= 10, "CPU worker priority exceeds the declared load budget")
    check(value.get("group") == pid and value.get("session") == pid, "CPU worker lacks its own group/session")
    check(isinstance(value.get("cgroup"), str) and value["cgroup"].startswith("/"), "CPU marker lacks its cgroup identity")
    return value


def thresholds_pass(report):
    rows = report.get("observations", [])
    return (report.get("first_health_ms", float("inf")) <= TARGET_MS
            and report.get("first_file_read_ms", float("inf")) <= TARGET_MS
            and len(rows) == 3
            and all(row["health"]["duration_ms"] <= TARGET_MS
                    and row["file_read"]["duration_ms"] <= TARGET_MS for row in rows))


WORKER = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,tempfile,time
root,nonce,role=sys.argv[1:]
root=pathlib.Path(root)
tail=pathlib.Path('/proc/self/stat').read_text().rsplit(')',1)[1].split()
group=next(line[3:] for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if line.startswith('0::/'))
value={'pid':os.getpid(),'identity':tail[19],'nonce':nonce,'role':role,'nice':os.getpriority(os.PRIO_PROCESS,0),
       'group':os.getpgrp(),'session':os.getsid(0),'cgroup':group,'entered_ns':time.monotonic_ns()}
path=root/(role+'.json')
fd,temporary=tempfile.mkstemp(prefix='.'+path.name+'.',dir=root)
try:
    with os.fdopen(fd,'w') as file:
        os.fchmod(file.fileno(),0o600);json.dump(value,file);file.flush();os.fsync(file.fileno())
    os.link(temporary,path,follow_symlinks=False)
    directory=os.open(root,os.O_RDONLY|os.O_DIRECTORY)
    try: os.fsync(directory)
    finally: os.close(directory)
finally: os.unlink(temporary)
deadline=time.monotonic()+45
payload=b'sp6 bounded CPU sibling'*4096
while time.monotonic()<deadline:
    payload=hashlib.sha256(payload).digest()*4096
'''


HELPER = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,tempfile,time,urllib.request
control=pathlib.Path(__CONTROL__)
config=json.loads((control/'helper-config.json').read_text())
assert sys.argv[1:]==['external','cancel',config['run_id'],'--registration-id',config['registration_id']]
tail=pathlib.Path('/proc/self/stat').read_text().rsplit(')',1)[1].split()
group=next(line[3:] for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if line.startswith('0::/'))
value={'pid':os.getpid(),'identity':tail[19],'group':os.getpgrp(),'nonce':config['nonce'],
       'helper':str(pathlib.Path(__file__).resolve()),'nice':os.getpriority(os.PRIO_PROCESS,0),
       'entered_ns':time.monotonic_ns(),'cgroup':group,'session':os.getsid(0),'health_status':None,'health_ms':None,
       'run_sha256':hashlib.sha256(config['run_id'].encode()).hexdigest(),
       'registration_sha256':hashlib.sha256(config['registration_id'].encode()).hexdigest()}
def publish(path,value):
    fd,temporary=tempfile.mkstemp(prefix='.'+path.name+'.',dir=control)
    try:
        with os.fdopen(fd,'w') as file:
            os.fchmod(file.fileno(),0o600);json.dump(value,file);file.flush();os.fsync(file.fileno())
        os.link(temporary,path,follow_symlinks=False)
        directory=os.open(control,os.O_RDONLY|os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally: os.unlink(temporary)
start=time.monotonic()
try:
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,*args,**kwargs): raise RuntimeError('private health redirect refused')
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    token=pathlib.Path(config['token_file']).read_text().strip()
    request=urllib.request.Request('http://127.0.0.1:7210/healthz',headers={'Authorization':'Bearer '+token})
    with opener.open(request,timeout=3) as response:
        body=response.read(65537)
        assert len(body)<=65536 and json.loads(body).get('ok') is True
        value['health_status']=response.status
except Exception as error:
    value['health_error_class']=type(error).__name__
value['health_ms']=(time.monotonic()-start)*1000
path=control/('helper-entered-'+str(os.getpid())+'.json')
publish(path,value)
deadline=time.monotonic()+30
while not (control/'release-helper').exists():
    if time.monotonic()>=deadline: sys.exit(125)
    time.sleep(.02)
value['released_ns']=time.monotonic_ns()
value['release_reason']='exact-private-release-marker'
publish(control/('helper-released-'+str(os.getpid())+'.json'),value)
print(json.dumps({'run_id':config['run_id'],'status':'cancelled','cancelled':True,'quiescent':True}))
'''


class Owned:
    def __init__(self, proof, role, argv):
        self.proof, self.role, self.argv = proof, role, argv
        self.child = self.log = self.witness = None
        self.stopped = None
        self.receipt_path = proof.launches / (role + ".json")
        self.receipt = {"schema_version": 1, "kind": "sp6-startup-owned-process", "role": role,
            "phase": "prepared", "proof_root": str(proof.parent), "state_root": str(proof.state),
            "binary": str(proof.binary) if role == "server" else str(Path(sys.executable).resolve()),
            "binary_sha256": proof.binary_sha if role == "server" else hashlib.sha256(Path(sys.executable).read_bytes()).hexdigest(),
            "port": 7210 if role == "server" else None, "boot_id": proof.report["boot_id"],
            "pid": None, "process_start_ticks": None, "cgroup": None, "process_identity_verified": False}

    def start(self):
        write_launch_receipt(self.receipt_path, self.receipt, new=True)
        self.log = (self.proof.parent / (self.role + ".log")).open("xb")
        os.chmod(self.log.name, 0o600)
        try:
            self.child = subprocess.Popen(self.argv, cwd=self.proof.parent, env=self.proof.environment,
                stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log, start_new_session=True)
        except Exception as error:
            self.receipt.update(phase="launch_outcome_unknown", error_class=type(error).__name__)
            write_launch_receipt(self.receipt_path, self.receipt)
            raise
        self.receipt.update(phase="spawned_unverified", pid=self.child.pid)
        write_launch_receipt(self.receipt_path, self.receipt)
        identity = process_identity(self.child.pid)
        check(identity and self.child.poll() is None, "owned process exited before identity verification")
        self.witness = int(identity["identity"])
        check(os.getpgid(self.child.pid) == self.child.pid and os.getsid(self.child.pid) == self.child.pid,
              "owned process lacks its private group/session")
        self.receipt.update(phase="identified", process_start_ticks=self.witness,
            cgroup=cgroup_for(self.child.pid), process_identity_verified=True)
        write_launch_receipt(self.receipt_path, self.receipt)

    def stop(self):
        if self.stopped is None:
            try:
                check(self.receipt.get("phase") != "launch_outcome_unknown",
                      "launch outcome remains unknown; no exact PID authority exists for cleanup")
                self.stopped = OwnedServer.stop(self)
                self.receipt.update(phase="stopped", stopped_at=now(), cleanup=self.stopped)
                write_launch_receipt(self.receipt_path, self.receipt)
            finally:
                if self.log: self.log.close()
        return self.stopped


class Proof:
    def __init__(self, binary, parent, output, archive, expected_binary_sha=None):
        self.binary = Path(binary).resolve(strict=True)
        check(self.binary.is_file() and os.access(self.binary, os.X_OK), "candidate must be an explicit executable")
        with self.binary.open("rb") as file: check(file.read(4) == b"\x7fELF", "candidate must be a built ELF executable")
        self.parent, self.output = private_path(parent, missing=True), artifact_path(output)
        check(self.parent == Path(parent).expanduser().absolute(), "state path cannot contain a symlink or traversal")
        check(self.parent.is_relative_to(Path.home().resolve() / ".codexpro-go-proof")
              and self.parent != Path.home().resolve() / ".codexpro-go-proof", "state must be a private proof child")
        check(not self.parent.exists() and not self.output.exists(), "proof refuses reused state/evidence")
        assert_free_ports((7210,))  # Before any write, load process, token or listener.
        self.binary_sha = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        check(expected_binary_sha is None or self.binary_sha == expected_binary_sha,
              "candidate binary does not match the expected build hash")
        self.archive, archive_info = archive_identity(archive)
        self.parent.mkdir(parents=True, mode=0o700)
        self.state, self.root, self.control, self.launches = [self.parent / name for name in ("state", "fixture", "control", "launches")]
        for path in (self.state, self.root, self.control, self.launches, self.state / "jobs", self.parent / "empty-codex"):
            path.mkdir(mode=0o700)
        self.output.parent.mkdir(parents=True, exist_ok=True)
        self.nonce = secrets.token_hex(16)
        self.token, self.helper, self.worker = self.state / "http-token", self.control / "ssd-dev-standin", self.control / "cpu-worker"
        exclusive(self.token, secrets.token_urlsafe(48) + "\n")
        exclusive(self.state / "limits.env", "")
        exclusive(self.worker, WORKER, 0o700)
        exclusive(self.helper, HELPER.replace("__CONTROL__", repr(str(self.control))), 0o700)
        self.payload = ("startup-file-" + self.nonce + "\n").encode()
        exclusive(self.root / "readiness.txt", self.payload)
        self.workspace = "ws_" + hashlib.sha256(str(self.root).encode()).hexdigest()[:24]
        exclusive(self.state / "projects.json", json.dumps({"version": 1, "defaultProject": "contract",
            "projects": [{"id": "contract", "label": "Owned startup fixture", "root": str(self.root)}]}))
        self.environment = sanitized_environment()
        self.environment.update({"CODEXPRO_JOB_SCOPES": "1", "CODEXPRO_ENV_FILE": str(self.state / "limits.env"),
            "CODEXPRO_MAX_JOBS": "4", "CODEXPRO_MAX_JOBS_PER_WORKSPACE": "4", "GOMAXPROCS": "2"})
        self.job_id = "job_" + secrets.token_hex(4)
        self.run_id, self.registration = secrets.token_hex(16), secrets.token_hex(16)
        exclusive(self.control / "helper-config.json", json.dumps({"nonce": self.nonce, "run_id": self.run_id,
            "registration_id": self.registration, "token_file": str(self.token)}))
        self.seed_pending()
        self.owned, self.client, self.server = [], None, None
        self.deadline = time.monotonic() + 90
        self.report = {"schema_version": 1, "candidate_only": True, "source_commit": SOURCE_COMMIT,
            "ruling": "coordinator row31 observed2026-10-31T21:22Z", "started_at": now(),
            "binary": str(self.binary), "binary_sha256": self.binary_sha, "state_root": str(self.state),
            "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
            "archive": archive_info, "thresholds": {"health_from_spawn_ms": TARGET_MS, "file_read_from_spawn_ms": TARGET_MS,
                "blocked_health_ms": TARGET_MS, "blocked_file_read_ms": TARGET_MS},
            "cpu_workers": 2, "cpu_worker_cap_seconds": 45, "helper_cap_seconds": 30,
            "helper_is_standin": True, "real_ssd_dev_allocations_created": 0,
            "scope_probe": "normal candidate systemd-run capability probe may create its bounded transient true scope",
            "observations": [], "cleanup": [], "cleanup_errors": [], "stage4_complete": False}
        private_http()

    def seed_pending(self):
        stamp = int(time.time() * 1000) - 2000
        started_at = dt.datetime.fromtimestamp(stamp / 1000, dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
        base = self.state / "jobs" / self.job_id
        record = {"id": self.job_id, "workspace_id": self.workspace, "project_id": "contract", "root": str(self.root),
            "cwd": ".", "command": "printf must-not-run > forbidden-start.txt", "command_label": "ungranted startup proof",
            "pid": 0, "origin": "background", "status": "running", "started_at": started_at, "started_at_ms": stamp,
            "deadline_ms": stamp + 60000, "timeout_ms": 60000, "output_limit_bytes": 8192, "exit_code": None,
            "acknowledged": False, "runner_version": 2, "input_job_ids": [], "launch_state": "prepared", "quiescent": False,
            "scope_unit": "codexpro-" + self.job_id + "-" + secrets.token_hex(16),
            "resource_claim": {"version": 1, "helper": str(self.helper), "run_id": self.run_id, "registration_id": self.registration}}
        for field, suffix in (("stdout_path", ".out"), ("stderr_path", ".err"), ("exit_path", ".exit"),
                              ("result_path", ".result.json"), ("control_path", ".control"), ("grant_path", ".grant"), ("spec_path", ".spec.json")):
            record[field] = str(base) + suffix
        database = self.state / "jobs.sqlite"
        with sqlite3.connect(database, timeout=1) as db:
            db.execute("CREATE TABLE jobs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,started_ms INTEGER NOT NULL,status TEXT NOT NULL,origin TEXT NOT NULL,payload BLOB NOT NULL)")
            db.execute("INSERT INTO jobs VALUES(?,?,?,?,?,?)", (self.job_id, self.workspace, stamp, "running", "background", json.dumps(record)))
        os.chmod(database, 0o600)

    def job(self):
        with sqlite3.connect((self.state / "jobs.sqlite").as_uri() + "?mode=ro", uri=True, timeout=1) as db:
            db.execute("PRAGMA query_only=ON")
            row = db.execute("SELECT payload FROM jobs WHERE id=?", (self.job_id,)).fetchone()
        check(row is not None, "pending recovery record disappeared")
        value = json.loads(row[0])
        claim = value.get("resource_claim", {})
        check(claim == {"version": 1, "helper": str(self.helper), "run_id": self.run_id, "registration_id": self.registration},
              "recovery changed the exact persisted owner")
        return value

    def wait(self, label, probe, seconds):
        deadline = min(self.deadline, time.monotonic() + seconds)
        while time.monotonic() < deadline:
            value = probe()
            if value: return value
            time.sleep(.03)
        raise Failure("bounded wait expired: " + label)

    def start_owned(self, role, argv):
        owned = Owned(self, role, argv)
        self.owned.append(owned)
        owned.start()
        return owned

    def health(self, timeout=.5):
        request = urllib.request.Request("http://127.0.0.1:7210/healthz", headers={"Authorization": "Bearer " + self.token.read_text().strip()})
        started = time.monotonic_ns()
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read(65537)
            check(response.status == 200 and len(raw) <= 65536, "health response violated its status/size contract")
            body = json.loads(raw)
        check(body.get("ok") is True and body.get("name") == "CodexPro" and body.get("defaultProjectId") == "contract" and "ready" not in body,
              "health was replaced by a success-shaped startup stub")
        return {"duration_ms": (time.monotonic_ns()-started)/1e6, "body": body}

    def file_read(self):
        started = time.monotonic_ns()
        response = self.client.call("read", {"workspace_id": self.workspace, "path": "readiness.txt", "start_line": 1, "end_line": 1})
        result = raw_result(response)
        check(result.get("isError") is not True and result.get("structuredContent", {}).get("sha256") == hashlib.sha256(self.payload).hexdigest(),
              "MCP read did not return this exact private file")
        self.client.assert_no_credential(response)
        return {"duration_ms": (time.monotonic_ns()-started)/1e6, "response": response}

    def helper_markers(self):
        return [valid_helper_witness(json.loads(path.read_text()), self.nonce, self.helper)
                for path in sorted(self.control.glob("helper-entered-*.json"))]

    def archive_open(self):
        for fd in Path(f"/proc/{self.server.child.pid}/fd").iterdir():
            try:
                info = fd.stat()
                if (info.st_dev, info.st_ino) == (self.report["archive"]["device"], self.report["archive"]["inode"]): return True
            except FileNotFoundError: pass
        return False

    def exercise(self):
        cpus = [self.start_owned("cpu-" + str(i), ["nice", "-n", "10", sys.executable, str(self.worker), str(self.control), self.nonce, "cpu-" + str(i)]) for i in range(2)]
        self.wait("both bounded CPU siblings active", lambda: all(item.child.poll() is None and cpu_seconds(item.child.pid) >= .1 for item in cpus), 8)
        workers = []
        for item in cpus:
            path = self.control / (item.role + ".json")
            self.wait("atomic CPU marker " + item.role, path.exists, 2)
            witness = valid_cpu_witness(json.loads(path.read_text()), self.nonce, item.role, item.child.pid, item.witness)
            check(witness["cgroup"] == item.receipt["cgroup"] and alive(witness), "CPU witness changed after launch")
            workers.append({k: v for k, v in witness.items() if k != "nonce"})
        self.report["load_start"] = [{**worker, "cpu_seconds": cpu_seconds(worker["pid"])} for worker in workers]
        self.report["host_loadavg_start"] = os.getloadavg()
        argv = [str(self.binary), "--state-dir", str(self.state), "--listen", "127.0.0.1:7210",
            "--token-file", str(self.token), "--projects-file", str(self.state / "projects.json"),
            "--limits-file", str(self.state / "limits.env"), "--host-resources-helper", str(self.helper),
            "--work-history", str(self.archive), "--codex-dir", str(self.parent / "empty-codex"),
            "--worktree-mode", "off", "--tool-mode", "standard", "--bash", "full", "--write", "workspace"]
        self.spawn_ns = time.monotonic_ns()
        self.server = self.start_owned("server", argv)
        self.report["candidate_process"] = {"pid": self.server.child.pid, "process_start_ticks": self.server.witness,
            "cgroup": self.server.receipt["cgroup"], "nice": os.getpriority(os.PRIO_PROCESS, self.server.child.pid)}
        same_scope = all(worker["cgroup"] == self.server.receipt["cgroup"] for worker in workers)
        self.report["cpu_load_relationship"] = {"same_cgroup_as_candidate": same_scope,
            "scope": "same-scope conservative CPU load" if same_scope else "observed separate cgroups; no CPUWeight separation assertion",
            "priority_claim": "only the recorded per-process nice values; no cgroup weight guarantee"}
        self.report["io_after_spawn"] = process_io(self.server.child.pid)
        def available_health():
            check(self.server.child.poll() is None, "owned candidate exited during startup")
            try: return self.health()
            except (OSError, urllib.error.URLError): return None
        first = self.wait("authenticated health", available_health, 12)
        self.report["first_health_ms"] = (time.monotonic_ns()-self.spawn_ns)/1e6
        self.report["first_health"] = first
        manifest = {"pid": self.server.child.pid, "process_start_ticks": self.server.witness, "port": 7210,
            "state_root": str(self.state), "fixture_root": str(self.root), "token_file": str(self.token), "project_id": "contract"}
        validate_launch(manifest, "http://127.0.0.1:7210/mcp", self.token, self.root, 7210)
        check(Path(f"/proc/{self.server.child.pid}/exe").resolve() == self.binary, "health is not served by the explicit candidate")
        def initialize():
            try: return Client("http://127.0.0.1:7210/mcp", self.token, timeout=3)
            except (OSError, RuntimeError): return None
        self.client = self.wait("actual MCP dispatcher", initialize, 12)
        first_read = self.file_read()
        self.report["first_file_read_ms"] = (time.monotonic_ns()-self.spawn_ns)/1e6
        self.report["first_file_read"] = first_read
        self.server.receipt.update(phase="ready", ready_at=now())
        write_launch_receipt(self.server.receipt_path, self.server.receipt)
        marker = self.wait("actual pending helper recovery entered", lambda: next((v for v in self.helper_markers() if alive(v)), None), 12)
        check(marker["health_status"] == 200 and marker["health_ms"] <= TARGET_MS, "recovery entered before usable authenticated health")
        check(marker["nice"] >= 10, "recovery helper displaced control-plane priority")
        check(marker["run_sha256"] == hashlib.sha256(self.run_id.encode()).hexdigest()
              and marker["registration_sha256"] == hashlib.sha256(self.registration.encode()).hexdigest(),
              "helper entered for a different persisted owner")
        check(not self.job().get("quiescent"), "blocked helper was falsely treated as settled")
        self.wait("actual private historical SQLite reader opened", self.archive_open, 5)
        for index in range(3):
            check(alive(marker) and not self.job().get("quiescent"), "recovery ceased to be blocked during read measurements")
            check(all(item.child.poll() is None for item in cpus), "CPU siblings ended before the blocked-read gate")
            health, read = self.health(timeout=3), self.file_read()
            self.report["observations"].append({"iteration": index, "health": health, "file_read": read,
                "pending_quiescent": False, "load_cpu_seconds": [cpu_seconds(item.child.pid) for item in cpus]})
            time.sleep(.15)
        self.report["io_while_blocked"] = process_io(self.server.child.pid)
        self.report["helper_entries"] = [{k: v for k, v in item.items() if k != "nonce"} for item in self.helper_markers()]
        self.report["helper_release_requested_ns"] = time.monotonic_ns()
        exclusive(self.control / "release-helper", self.nonce)
        self.wait("exact prepared owner settled", lambda: self.job().get("quiescent") is True, 12)
        releases = []
        for path in sorted(self.control.glob("helper-released-*.json")):
            value = valid_helper_witness(json.loads(path.read_text()), self.nonce, self.helper)
            check(type(value.get("released_ns")) is int and value["released_ns"] >= self.report["helper_release_requested_ns"],
                  "helper release has no matching current release request")
            releases.append({k: v for k, v in value.items() if k != "nonce"})
        check(releases, "settlement completed without a witnessed helper release")
        self.report["helper_releases"] = releases
        check(not (self.root / "forbidden-start.txt").exists(), "ungranted command executed during recovery")
        self.report["owned_recovery"] = {"job_id": self.job_id, "launch_state": self.job()["launch_state"], "quiescent": True,
            "exact_owner_verified": True, "ungranted_command_executed": False}
        consumed = self.report["io_while_blocked"]["rchar"] - self.report["io_after_spawn"]["rchar"]
        total_read = self.report["io_while_blocked"]["rchar"]
        self.report["archive_startup_read_observation"] = {"opened": True, "process_rchar_delta": consumed,
            "process_rchar_total": total_read, "archive_bytes": ARCHIVE_BYTES,
            "below_one_quarter_archive": total_read < ARCHIVE_BYTES//4,
            "scope": "candidate total read-syscall bytes through blocked reads; driver provenance hashing is excluded",
            "limitation": "supporting I/O observation, not a standalone proof against mmap/page-cache access; constructor/source audit is separate"}
        self.report["threshold_pass"] = thresholds_pass(self.report)
        self.report["host_loadavg_end"] = os.getloadavg()
        check(self.report["threshold_pass"], "predeclared 3-second health/file-read target was missed")
        check(total_read < ARCHIVE_BYTES//4, "startup read a substantial fraction of the archive; full-scan avoidance is not proven")

    def cleanup(self):
        if not (self.control / "release-helper").exists(): exclusive(self.control / "release-helper", self.nonce)
        if self.client:
            try: self.client.close()
            except Exception as error: self.report["cleanup_errors"].append(type(error).__name__ + ": MCP session close failed")
        for owned in reversed(self.owned):
            try: self.report["cleanup"].append({"role": owned.role, **owned.stop()})
            except Exception as error: self.report["cleanup_errors"].append(type(error).__name__ + ": owned " + owned.role + " cleanup failed")
        for marker in self.helper_markers():
            deadline = time.monotonic() + 2
            while alive(marker) and time.monotonic() < deadline: time.sleep(.03)
            if alive(marker):
                self.report["cleanup_errors"].append("owned control helper required emergency PID-identity cleanup")
                try: fd = os.pidfd_open(marker["pid"])
                except ProcessLookupError: continue
                try:
                    if alive(marker): signal.pidfd_send_signal(fd, signal.SIGKILL)
                finally: os.close(fd)
                deadline = time.monotonic() + 2
                while alive(marker) and time.monotonic() < deadline: time.sleep(.03)
            check(not alive(marker), "exact owned helper survived cleanup")
        self.report["cleanup_verified"] = not self.report["cleanup_errors"]

    def run(self):
        try: self.exercise()
        except Exception as error:
            detail = str(error)
            if self.client:
                detail = detail.replace(self.client.credential, "<credential>")
                if self.client.session: detail = detail.replace(self.client.session, "<session>")
            self.report["failure"] = type(error).__name__ + ": " + detail[:2000]
        finally:
            try: self.cleanup()
            except Exception as error: self.report["cleanup_errors"].append(type(error).__name__ + ": cleanup incomplete")
            self.report["finished_at"] = now()
            self.report["launch_receipts"] = [str(item.receipt_path) for item in self.owned]
            self.report["passed"] = not self.report.get("failure") and self.report.get("threshold_pass") is True and not self.report["cleanup_errors"]
            if self.client: self.client.assert_no_credential(self.report)
            exclusive(self.output, json.dumps(self.report, indent=2, ensure_ascii=False) + "\n")
        return self.report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "state-parent", "output", "archive"): parser.add_argument("--" + name, required=True)
    parser.add_argument("--expected-binary-sha", required=True)
    args = parser.parse_args()
    check(re.fullmatch(r"[0-9a-f]{64}", args.expected_binary_sha), "expected binary hash must be a SHA-256 digest")
    proof = Proof(args.binary, args.state_parent, args.output, args.archive, args.expected_binary_sha)
    def interrupted(signum, frame): raise Failure("startup proof interrupted; cleaning only witnessed owned processes")
    old = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try: report = proof.run()
    finally:
        for sig, handler in old.items(): signal.signal(sig, handler)
    print(json.dumps({"output": str(proof.output), "passed": report["passed"],
        "first_health_ms": report.get("first_health_ms"), "first_file_read_ms": report.get("first_file_read_ms"),
        "cleanup_errors": len(report["cleanup_errors"])}))
    return 0 if report["passed"] else 1


if __name__ == "__main__": raise SystemExit(main())
