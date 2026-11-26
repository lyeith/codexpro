#!/usr/bin/env python3
"""Disposable proof for the installed Fleet admission writer.

Run via fleet-remote inside the existing bounded external owner, using uv.
Supply the exact staged SSD Fleet payload and an existing policy source.
This helper never changes a live installation or the outer owner's environment.
All fixture subprocesses share one private home-state directory.
"""
from __future__ import annotations

import argparse
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import socket
import subprocess
import sys
import time

HEAVY_SHA256 = "9b9b3403a014716166f8d129eaf539d87e63cc2ff263b6655ba207ac4ee20876"
PAYLOAD_DIGEST = "a2ab2476bf0b6c142292609bfc8038cb418553c3e9eea28fb8622eae7aa093e5"
WAIT_SECONDS = 15.0
SCRIPT = str(Path(__file__).resolve())


def save(path: Path, value: object) -> None:
    with path.open("x", encoding="utf-8") as output:
        json.dump(value, output, sort_keys=True, indent=2)
        output.write("\n")


def emit(event: str, **fields: object) -> None:
    print(json.dumps({"event": event, **fields}, sort_keys=True), flush=True)


def identity(path: Path) -> list[int]:
    info = path.stat()
    return [info.st_dev, info.st_ino]


def payload(label: str, evidence: Path, entry_pid: int, mode: str) -> int:
    pool = Path(os.environ["PLANS_HEAVY_DIR"]).resolve()
    inherited = []
    for item in Path("/proc/self/fd").iterdir():
        try:
            target = os.readlink(item)
        except FileNotFoundError:
            continue
        if target.startswith(str(pool) + "/"):
            inherited.append(target)
    if mode == "managed":
        assert os.getpid() == entry_pid, "managed admission replaced its process owner"
        assert str(pool / "plans-heavy-active.lock") in inherited
        assert any("/plans-heavy-runs/" in name for name in inherited)
    observed = {
        "pid": os.getpid(),
        "entry_pid": entry_pid,
        "pool": str(pool),
        "gate": identity(pool / "plans-heavy-gate.lock"),
        "active": identity(pool / "plans-heavy-active.lock"),
        "inherited_admission_files": sorted(inherited),
        "cgroup": Path("/proc/self/cgroup").read_text(),
        "started_monotonic": time.monotonic(),
    }
    save(evidence / (label + ".started.json"), observed)
    emit("payload_ready", label=label, **observed)
    sys.stdin.buffer.read(1)
    save(evidence / (label + ".done.json"), {"pid": os.getpid(), "at": time.monotonic()})
    return 0


def entry(release: Path, mode: str, label: str, evidence: Path) -> int:
    sys.path.insert(0, str(release))
    from fleet_tools import heavy

    assert Path(heavy.__file__).resolve() == release / "fleet_tools/heavy.py"
    assert hashlib.sha256(Path(heavy.__file__).read_bytes()).hexdigest() == HEAVY_SHA256
    pool = Path(os.environ["PLANS_HEAVY_DIR"]).resolve()
    assert heavy.LOCK_DIR.resolve() == pool
    if mode == "inspect":
        records = heavy._records(strict=True)
        emit("records", records=records)
        return 0

    injection = {"calls": 0, "stage": "initial_roster_flush", "errno": errno.EDQUOT}
    if mode == "edquot":
        original_fdopen = os.fdopen

        class QuotaStream:
            def __init__(self, stream, path: str):
                self.stream, self.path = stream, path

            def __getattr__(self, name):
                return getattr(self.stream, name)

            def __enter__(self):
                return self

            def __exit__(self, kind, value, traceback):
                return self.stream.__exit__(kind, value, traceback)

            def flush(self):
                injection["calls"] += 1
                injection["path"] = self.path
                raise OSError(errno.EDQUOT, os.strerror(errno.EDQUOT), self.path)

        def fdopen(fd, *args, **kwargs):
            stream = original_fdopen(fd, *args, **kwargs)
            target = os.readlink(f"/proc/self/fd/{fd}")
            if Path(target).parent == pool / "plans-heavy-runs":
                return QuotaStream(stream, target)
            return stream

        heavy.os.fdopen = fdopen

    if mode == "ordinary":
        # Observe the real full-capacity branch without changing its delay or result.
        original_sleep = time.sleep
        reported = False

        def observe_wait(seconds):
            nonlocal reported
            if not reported:
                records = heavy._records(strict=True)
                emit(
                    "capacity_wait",
                    records=[{key: record.get(key) for key in ("pid", "state", "managed_external")} for record in records],
                    gate=identity(pool / "plans-heavy-gate.lock"),
                    pool=str(pool),
                )
                reported = True
            return original_sleep(seconds)

        heavy.time.sleep = observe_wait

    command = [
        sys.executable, "-I", "-B", "-u", SCRIPT, "_payload",
        label, str(evidence), str(os.getpid()), "managed" if mode != "ordinary" else "ordinary",
    ]
    flags = ["run", "--"] if mode == "ordinary" else ["run", "--managed-external", "--"]
    result = heavy.main([*flags, *command])
    if mode == "edquot":
        save(evidence / "edquot.injection.json", injection)
    return result


def installed_heavy(home: Path):
    """Load the release selected by the real private installed front door."""
    root = Path(os.environ["FLEET_TOOLS_ROOT"]).resolve(strict=True)
    expected = home / ".local/share/fleet-tools/releases" / PAYLOAD_DIGEST
    assert root == expected, "front door selected a different release"
    sys.path.insert(0, str(root))
    from fleet_tools.runtime import load_module, release_root

    assert release_root() == root
    heavy = load_module("heavy")
    assert Path(heavy.__file__).resolve() == root / "fleet_tools/heavy.py"
    assert hashlib.sha256(Path(heavy.__file__).read_bytes()).hexdigest() == HEAVY_SHA256
    assert heavy.LOCK_DIR.resolve() == Path(os.environ["PLANS_HEAVY_DIR"]).resolve()
    assert heavy.shared_capacity() == 1 and not heavy._single_heavy_host()
    return heavy, root


def frontdoor_observation(home: Path, owner_pid: int) -> dict:
    heavy, root = installed_heavy(home)
    pool = heavy.LOCK_DIR.resolve()
    records = heavy._records(strict=True)
    named = list((pool / "plans-heavy-runs").iterdir())
    assert len(records) == 1 and len(named) == 1 and named[0].suffix == ".json", \
        "nested work created another admission record"
    owner = records[0]
    assert owner["pid"] == owner_pid and owner["state"] == "running"
    assert owner.get("managed_external") is True
    assert heavy._admitted_ancestor() == owner, "nested work lost its admitted ancestor"
    inherited = {}
    for item in Path("/proc/self/fd").iterdir():
        try:
            target = os.readlink(item)
            if target.startswith(str(pool) + "/"):
                inherited[target] = identity(item)
        except FileNotFoundError:
            continue
    if os.getpid() == owner_pid:
        assert str(pool / "plans-heavy-active.lock") in inherited
        assert str(named[0]) in inherited
    return {
        "pid": os.getpid(), "owner_pid": owner_pid, "owner_start": owner["process_start"],
        "running_records": len(records), "roster": identity(named[0]),
        "pool": str(pool), "gate": identity(pool / "plans-heavy-gate.lock"),
        "active": identity(pool / "plans-heavy-active.lock"),
        "inherited_admission_files": inherited,
        "fleet_root": str(root), "fleet_python": os.environ["FLEET_TOOLS_PYTHON"],
        "cgroup": Path("/proc/self/cgroup").read_text(),
    }


def frontdoor_parent(home: Path, evidence: Path) -> int:
    owner_pid = os.getpid()
    before = frontdoor_observation(home, owner_pid)
    save(evidence / "frontdoor.before.json", before)
    emit("frontdoor_parent_ready", **before)
    # Ordinary nested work must reuse this managed ancestor. A second managed
    # or exclusive request would correctly be rejected rather than reuse a slot.
    command = [
        str(home / ".local/bin/plans-heavy"), "run", "--",
        os.environ["FLEET_TOOLS_PYTHON"], "-I", "-B", "-u", SCRIPT,
        "_frontdoor_leaf", str(home), str(evidence), str(owner_pid),
    ]
    nested = subprocess.run(command, timeout=WAIT_SECONDS + 5)
    assert nested.returncode == 0, "nested installed front door failed"
    after = frontdoor_observation(home, owner_pid)
    for key in ("owner_pid", "owner_start", "roster", "pool", "gate", "active", "fleet_root", "fleet_python", "cgroup"):
        assert after[key] == before[key], "nested release changed its ancestor's original lease"
    save(evidence / "frontdoor.after.json", after)
    return 0


def frontdoor_leaf(home: Path, evidence: Path, owner_pid: int) -> int:
    observed = frontdoor_observation(home, owner_pid)
    assert os.getpid() != owner_pid
    save(evidence / "frontdoor.nested.json", observed)
    emit("frontdoor_nested_ready", **observed)
    readable, _, _ = select.select([sys.stdin], [], [], WAIT_SECONDS)
    assert readable, "outer proof did not release its finite nested payload"
    sys.stdin.buffer.read(1)
    save(evidence / "frontdoor.nested.done.json", {"pid": os.getpid()})
    return 0


class Child:
    """Own one finite fixture process and save stdout before interpreting events."""

    def __init__(self, label: str, command: list[str], env: dict[str, str], evidence: Path):
        self.label = label
        self.stdout = (evidence / (label + ".stdout.log")).open("xb")
        self.stderr = (evidence / (label + ".stderr.log")).open("xb")
        self.process = subprocess.Popen(
            command, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=self.stderr, bufsize=0,
        )
        self.closed = False

    def event(self, expected: str) -> dict:
        deadline = time.monotonic() + WAIT_SECONDS
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError(f"{self.label}: waiting for {expected}")
            ready, _, _ = select.select([self.process.stdout], [], [], remaining)
            if not ready:
                raise TimeoutError(f"{self.label}: waiting for {expected}")
            line = self.process.stdout.readline()
            if not line:
                raise AssertionError(f"{self.label} exited before {expected}: {self.process.poll()}")
            self.stdout.write(line)
            self.stdout.flush()
            event = json.loads(line)
            if event.get("event") == expected:
                return event

    def finish(self, *, release: bool = True) -> int:
        if release and self.process.stdin is not None:
            self.process.stdin.close()
            self.process.stdin = None
        deadline = time.monotonic() + WAIT_SECONDS
        result = self.process.wait(timeout=WAIT_SECONDS)
        if not self.closed:
            try:
                while True:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise TimeoutError(f"{self.label}: inherited stdout did not close")
                    readable, _, _ = select.select([self.process.stdout], [], [], remaining)
                    if not readable:
                        raise TimeoutError(f"{self.label}: inherited stdout did not close")
                    data = os.read(self.process.stdout.fileno(), 65536)
                    if not data:
                        break
                    self.stdout.write(data)
                    self.stdout.flush()
            finally:
                self.stdout.close()
                self.stderr.close()
                self.process.stdout.close()
                self.closed = True
        return result

    def cleanup(self) -> None:
        try:
            self.finish()
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
            self.finish()


def run(args) -> int:
    assert sys.platform == "linux", "managed-external proof requires Linux"
    assert socket.gethostname().split(".", 1)[0] == "plans-worker", "use the existing fleet-remote worker route"
    release = Path(args.fleet_root).resolve(strict=True)
    heavy_path = release / "fleet_tools/heavy.py"
    assert hashlib.sha256(heavy_path.read_bytes()).hexdigest() == HEAVY_SHA256, "wrong pinned Fleet source"
    manifest = json.loads((release / "release.json").read_text())
    assert manifest == {"api_version": 1, "version": "0.1.4"}, "wrong pinned Fleet manifest"
    sys.path.insert(0, str(release))
    from fleet_tools.install import install_release, read_payload
    assert read_payload(release).digest == PAYLOAD_DIGEST, "wrong complete Fleet payload"
    launcher = Path(args.launcher_executable).resolve(strict=True)
    assert launcher.is_file() and os.access(launcher, os.X_OK), "supply the freshly built CodexPro launcher"
    evidence = Path(args.evidence_dir).resolve()
    assert evidence.is_relative_to(Path.home().resolve()), "evidence must be beneath the worker home"
    evidence.parent.mkdir(parents=True, exist_ok=True)
    evidence.mkdir(mode=0o700)  # Refuse an existing proof; preserve original results.
    os.umask(0o077)
    pool = evidence / "fixture-home/.local/state/fleet-tools/heavy"
    pool.mkdir(parents=True)
    policy_source = Path(args.policy_source).resolve(strict=True)
    values = json.loads(policy_source.read_text())
    hostname = socket.gethostname().split(".", 1)[0].casefold()
    # Same private-host mapping used by provider test_managed_external_exec_shares_the_actual_host_slot.
    for name, host in values["hosts"].items():
        if host["match"]["system"].casefold() == "linux":
            host["match"]["hostnames"] = [hostname if name == "ssd" else "unmatched-" + name]
    values["hosts"]["ssd"]["heavy"] = {"max_shared": 1, "supervised": False}
    policy = evidence / "fleet-runtime.fixture.json"
    save(policy, values)

    env = dict(os.environ)
    env.update(
        FLEET_CONFIG=str(policy), FLEET_TOOLS_ROOT=str(release),
        PLANS_HEAVY_DIR=str(pool), PYTHONDONTWRITEBYTECODE="1",
    )
    # Fixture-only policy context; the outer owner, kernel limits and parent environment stay intact.
    env.pop("FLEET_EXECUTION_CONTEXT", None)
    report = {
        "fleet_payload": PAYLOAD_DIGEST, "heavy_sha256": HEAVY_SHA256,
        "fleet_root": str(release), "pool": str(pool), "pool_device": pool.stat().st_dev,
        "launcher_executable": str(launcher),
        "outer_cgroup": Path("/proc/self/cgroup").read_text(),
        "injection": "synthetic EDQUOT at Python roster stream.flush; not a recovered original syscall",
        "cases": {},
    }
    save(evidence / "source.json", report)
    children = []

    def spawn(label: str, mode: str) -> Child:
        child = Child(
            label, [sys.executable, "-I", "-B", "-u", SCRIPT, "_entry",
                    str(release), mode, label, str(evidence)], env, evidence,
        )
        children.append(child)
        return child

    def launch_case(label: str, mode: str, expected_exit: int) -> tuple[Path, dict]:
        case = evidence / "composed" / label
        case.mkdir(parents=True)  # Preserve the direct case's original injection and logs.
        spec_path = case / "launcher.spec.json"
        spec = {
            "version": 1,
            "args": [sys.executable, "-I", "-B", "-u", SCRIPT, "_entry",
                     str(release), mode, label, str(case)],
            "cwd": str(case), "stdout": str(case / "launcher.stdout.log"),
            "stderr": str(case / "launcher.stderr.log"),
            "result": str(case / "launcher.result.json"), "limit": 16 * 1024,
            "pathRedactions": [[str(pool), "[fixture-pool]"]],
        }
        save(spec_path, spec)
        shim = Child("shim-" + label, [str(launcher), "launcher", str(spec_path)], env, case)
        children.append(shim)
        # RunLauncher supplies its child closed stdin, so managed success is finite.
        shim_exit = shim.finish()
        assert shim_exit == 0, "launcher shim failed to publish its child receipt"
        receipt = json.loads(Path(spec["result"]).read_text())
        assert receipt["version"] == 1 and receipt["finished_at_ms"] > 0
        assert receipt["exit_code"] == expected_exit and receipt["signal"] is None
        assert not receipt.get("output_truncated"), "composed diagnostic capture was incomplete"
        if expected_exit == 0:
            assert not receipt.get("error"), "successful composed launch reported a capture error"
        marker = json.loads(Path(str(spec_path) + ".started").read_text())
        assert marker["pid"] == shim.process.pid and marker.get("process_identity")
        assert not marker.get("revoked")
        return case, {"shim_exit": shim_exit, "receipt": receipt, "shim_identity": marker}

    try:
        failed = spawn("edquot", "edquot")
        assert failed.finish() == 2, "pre-runner I/O error must retain heavy.main exit 2"
        injected = json.loads((evidence / "edquot.injection.json").read_text())
        assert injected["calls"] == 1 and injected["errno"] == errno.EDQUOT
        error = (evidence / "edquot.stderr.log").read_text()
        assert "heavy:" in error and os.strerror(errno.EDQUOT) in error and injected["path"] in error
        assert not (evidence / "edquot.started.json").exists(), "failed admission executed its payload"
        assert not list((pool / "plans-heavy-runs").iterdir()), "failed initial write left a roster file"
        assert not (pool / "plans-heavy-gate.lock").exists(), "failure occurred after initial roster publication"
        report["cases"]["initial_flush_failure"] = {"exit": 2, "payload_started": False, **injected}

        holder = spawn("holder", "managed")
        first = holder.event("payload_ready")
        assert first["pid"] == holder.process.pid
        assert first["cgroup"] == report["outer_cgroup"], "managed proof left its existing outer cgroup"
        contender = spawn("contender", "ordinary")
        waiting = contender.event("capacity_wait")
        assert sorted(record["state"] for record in waiting["records"]) == ["running", "waiting"]
        assert waiting["gate"] == first["gate"] and waiting["pool"] == first["pool"]
        assert holder.process.poll() is None
        assert not (evidence / "contender.started.json").exists(), "shared capacity was bypassed"
        assert holder.finish() == 0
        second = contender.event("payload_ready")
        assert second["gate"] == first["gate"] and second["pool"] == first["pool"]
        assert second["cgroup"] == report["outer_cgroup"], "ordinary proof left the bounded outer cgroup"
        assert contender.finish() == 0
        report["cases"]["home_state_success_and_contention"] = {
            "holder": first, "waiting": waiting, "contender": second,
            "holder_exit": 0, "contender_exit": 0,
        }

        case, composed_failure = launch_case("edquot", "edquot", 2)
        injected = json.loads((case / "edquot.injection.json").read_text())
        assert injected["calls"] == 1 and injected["errno"] == errno.EDQUOT
        failed_path = Path(injected["path"])
        assert failed_path.parent == pool / "plans-heavy-runs" and failed_path.suffix == ".tmp"
        redacted_path = "[fixture-pool]/" + str(failed_path.relative_to(pool))
        captured = (case / "launcher.stderr.log").read_text()
        assert "heavy:" in captured and os.strerror(errno.EDQUOT) in captured
        assert redacted_path in captured and str(pool) not in captured
        assert not (case / "edquot.started.json").exists(), "composed failed admission executed its payload"
        assert not (case / "edquot.done.json").exists()
        assert not list((pool / "plans-heavy-runs").iterdir())
        report["cases"]["launcher_initial_flush_failure"] = {
            **composed_failure, "payload_started": False, "injection_calls": injected["calls"],
            "captured_error_path": redacted_path,
        }

        case, composed_success = launch_case("success", "managed", 0)
        observed = json.loads((case / "success.started.json").read_text())
        done = json.loads((case / "success.done.json").read_text())
        assert observed["pid"] == observed["entry_pid"] == done["pid"]
        assert observed["pid"] != composed_success["shim_identity"]["pid"]
        assert observed["pool"] == str(pool) and observed["gate"] == first["gate"]
        assert observed["active"] == first["active"]
        assert observed["cgroup"] == report["outer_cgroup"], "composed proof left its bounded outer cgroup"
        inherited = observed["inherited_admission_files"]
        assert str(pool / "plans-heavy-active.lock") in inherited
        assert any(name.startswith(str(pool / "plans-heavy-runs") + "/") for name in inherited)
        captured = (case / "launcher.stdout.log").read_text()
        ready = json.loads(captured)
        assert ready["event"] == "payload_ready" and ready["pid"] == observed["pid"]
        assert ready["pool"] == "[fixture-pool]" and str(pool) not in captured
        report["cases"]["launcher_managed_success"] = {**composed_success, "payload": observed}

        fixture_home = evidence / "fixture-home"
        pointer = install_release(release, home=fixture_home, python=sys.executable)
        assert pointer["digest"] == PAYLOAD_DIGEST and pointer["version"] == "0.1.4"
        frontdoor = fixture_home / ".local/bin/plans-heavy"
        assert frontdoor.is_file() and os.access(frontdoor, os.X_OK)
        save(evidence / "frontdoor.install.json", pointer)
        frontdoor_env = dict(env)
        # The bootstrap accepts only a pin inside its baked-in selected home.
        # Let the first invocation read this private pointer; descendants then
        # inherit the release/interpreter pins installed by the real entrypoint.
        frontdoor_env.pop("FLEET_TOOLS_ROOT", None)
        frontdoor_env.pop("FLEET_TOOLS_PYTHON", None)
        installed = Child(
            "frontdoor", [str(frontdoor), "run", "--managed-external", "--",
                          pointer["python"], "-I", "-B", "-u", SCRIPT,
                          "_frontdoor_parent", str(fixture_home), str(evidence)],
            frontdoor_env, evidence,
        )
        children.append(installed)
        parent_observed = installed.event("frontdoor_parent_ready")
        nested_observed = installed.event("frontdoor_nested_ready")
        assert parent_observed["pid"] == installed.process.pid
        assert parent_observed["gate"] == first["gate"] and parent_observed["active"] == first["active"]
        for observed in (parent_observed, nested_observed):
            assert observed["owner_pid"] == installed.process.pid and observed["running_records"] == 1
            assert observed["pool"] == str(pool) and observed["fleet_root"] == pointer["root"]
            assert observed["fleet_python"] == pointer["python"]
            assert observed["cgroup"] == report["outer_cgroup"], "installed proof left its bounded owner"
        for key in ("owner_start", "roster", "gate", "active"):
            assert nested_observed[key] == parent_observed[key]

        def installed_status(label: str) -> str:
            stdout = evidence / (label + ".stdout.log")
            stderr = evidence / (label + ".stderr.log")
            with stdout.open("xb") as out, stderr.open("xb") as err:
                status = subprocess.run(
                    [str(frontdoor), "status"], env=frontdoor_env,
                    stdout=out, stderr=err, timeout=WAIT_SECONDS,
                )
            assert status.returncode == 0, "installed admission reader failed"
            return stdout.read_text()

        status_held = installed_status("frontdoor-status-held")
        running_lines = [line for line in status_held.splitlines() if line.startswith("  running ")]
        assert len(running_lines) == 1 and f"pid {installed.process.pid} " in running_lines[0]
        assert "shared capacity 1" in status_held and "  waiting " not in status_held
        assert installed.finish() == 0
        after_nested = json.loads((evidence / "frontdoor.after.json").read_text())
        assert after_nested["running_records"] == 1 and after_nested["roster"] == parent_observed["roster"]
        nested_done = json.loads((evidence / "frontdoor.nested.done.json").read_text())
        assert nested_done["pid"] == nested_observed["pid"]
        status_released = installed_status("frontdoor-status-released")
        assert "  no heavy runs" in status_released
        assert not list((pool / "plans-heavy-runs").iterdir())
        assert identity(pool / "plans-heavy-gate.lock") == first["gate"]
        assert identity(pool / "plans-heavy-active.lock") == first["active"]
        # An empty status listing alone cannot establish released leases.
        # These are the same private inodes used throughout this one proof.
        with (pool / "plans-heavy-gate.lock").open() as gate, (pool / "plans-heavy-active.lock").open() as active:
            for key, stream in (("gate", gate), ("active", active)):
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                held = os.fstat(stream.fileno())
                assert [held.st_dev, held.st_ino] == first[key]
        report["cases"]["installed_frontdoor_nested_ancestor_and_release"] = {
            "installation": pointer, "frontdoor_sha256": hashlib.sha256(frontdoor.read_bytes()).hexdigest(),
            "parent": parent_observed, "nested": nested_observed, "after_nested": after_nested,
            "held_reader_running_records": 1, "released_reader_empty": True,
            "original_leases_released": True, "exit": 0,
        }

        inspector = spawn("inspect", "inspect")
        assert inspector.event("records")["records"] == []
        assert inspector.finish() == 0
        assert not list((pool / "plans-heavy-runs").iterdir())
        assert hashlib.sha256(heavy_path.read_bytes()).hexdigest() == HEAVY_SHA256
        report["passed"] = True
    except BaseException as error:
        report["passed"] = False
        report["error"] = repr(error)
        raise
    finally:
        cleanup_errors = []
        for child in children:
            try:
                child.cleanup()
            except BaseException as error:
                cleanup_errors.append(f"{child.label}: {error!r}")
        report["cleanup_errors"] = cleanup_errors
        if cleanup_errors:
            report["passed"] = False
        save(evidence / "result.json", report)
        emit("result", passed=report.get("passed", False), evidence=str(evidence))
    return 0 if report.get("passed") else 1


if __name__ == "__main__":
    if sys.argv[1:2] == ["_entry"]:
        raise SystemExit(entry(Path(sys.argv[2]).resolve(), sys.argv[3], sys.argv[4], Path(sys.argv[5])))
    if sys.argv[1:2] == ["_payload"]:
        raise SystemExit(payload(sys.argv[2], Path(sys.argv[3]), int(sys.argv[4]), sys.argv[5]))
    if sys.argv[1:2] == ["_frontdoor_parent"]:
        raise SystemExit(frontdoor_parent(Path(sys.argv[2]).resolve(), Path(sys.argv[3])))
    if sys.argv[1:2] == ["_frontdoor_leaf"]:
        raise SystemExit(frontdoor_leaf(Path(sys.argv[2]).resolve(), Path(sys.argv[3]), int(sys.argv[4])))
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fleet-root", required=True, help="exact SSD 0.1.4 payload staged for worker use")
    parser.add_argument("--policy-source", required=True, help="existing full policy; copied into a private fixture")
    parser.add_argument("--launcher-executable", required=True, help="freshly built CodexPro binary with the launcher subcommand")
    parser.add_argument("--evidence-dir", required=True, help="new private worker-home path; existing paths refuse")
    raise SystemExit(run(parser.parse_args()))
