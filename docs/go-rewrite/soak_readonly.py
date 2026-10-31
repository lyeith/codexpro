#!/usr/bin/env python3
"""Ten-minute bounded observation of an already owned private Go candidate.

This driver never starts, stops, signals, adopts or changes the candidate.
Only its own bounded profiler children are controlled. No HTTP request precedes
the first unchanged 160-call profile. Run under a 740s TERM/10s KILL wrapper.
"""
from __future__ import annotations
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import time

from lifecycle_proof import write_launch_receipt, process_identity, cgroup_for
from managed_contract_suite import OwnedServer, sanitized_environment
from paired_contract_suite import private_path, artifact_path
from process_contract_suite import private_http

IDLE_KB = 100 * 1024
PEAK_KB = 256 * 1024
OFFSETS = (0, 300, 600)


class Failure(AssertionError): pass


def check(ok, message):
    if not ok: raise Failure(message)


def bounded_json(path, limit=1024*1024):
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        info = os.fstat(stream.fileno())
        check(stat.S_ISREG(info.st_mode) and info.st_size <= limit, "metadata must be a bounded regular file")
        raw = stream.read(limit + 1)
    check(len(raw) <= limit, "metadata exceeded its byte bound")
    return json.loads(raw), hashlib.sha256(raw).hexdigest()


def cli_value(argv, flag):
    for index, value in enumerate(argv):
        if value == "--" + flag and index + 1 < len(argv): return argv[index+1]
        if value.startswith("--" + flag + "="): return value.split("=", 1)[1]
    return None


def assert_identity(binding):
    current = process_identity(binding["pid"])
    check(current is not None and current["state"] not in {"Z", "X"}
          and current["identity"] == str(binding["process_start_ticks"]),
          "candidate exited or its PID incarnation changed; no server action is authorized")
    if "binary_inode" in binding:
        executable = Path(f"/proc/{binding['pid']}/exe").stat()
        check((executable.st_dev, executable.st_ino) == (binding["binary_device"], binding["binary_inode"]),
              "candidate executable changed within the observed PID incarnation")
    return current


def listener_owned(pid, port):
    sockets = set()
    for fd in Path(f"/proc/{pid}/fd").iterdir():
        try: value = os.readlink(fd)
        except FileNotFoundError: continue
        match = re.fullmatch(r"socket:\[(\d+)\]", value)
        if match: sockets.add(match[1])
    for family in ("tcp", "tcp6"):
        path = Path(f"/proc/{pid}/net/{family}")
        if not path.exists(): continue
        for line in path.read_text().splitlines()[1:]:
            fields = line.split()
            if len(fields) > 9 and fields[3] == "0A" and fields[9] in sockets:
                if int(fields[1].rsplit(":", 1)[1], 16) == port: return True
    return False


def launch_paths(value):
    state, token, catalog = [private_path(value[key]) for key in ("state_root", "token_file", "projects_file")]
    home = private_path(value.get("config_home", state))
    check(state.is_dir() and token.is_relative_to(state) and catalog.is_relative_to(state)
          and home.is_relative_to(state), "home, token and catalog must be inside the declared private state")
    return state, home, token, catalog


def bind(manifest_path, binary, binary_sha, project):
    manifest_path = private_path(manifest_path)
    value, manifest_sha = bounded_json(manifest_path)
    required = {"pid", "process_start_ticks", "port", "state_root", "token_file", "projects_file"}
    check(isinstance(value, dict) and required <= value.keys(), "launch manifest lacks the required identity fields")
    check(type(value["pid"]) is int and value["pid"] > 1 and type(value["process_start_ticks"]) is int,
          "launch manifest process identity is invalid")
    check(value["port"] == 7210, "soak is restricted to the already owned private candidate on7210")
    state, home, token, catalog = launch_paths(value)
    check(token.is_file() and token.stat().st_mode & 0o077 == 0, "private token permissions are not restricted")
    executable = Path(binary).resolve(strict=True)
    check(re.fullmatch(r"[0-9a-f]{64}", binary_sha), "expected binary identity must be a SHA-256 digest")
    check(hashlib.sha256(executable.read_bytes()).hexdigest() == binary_sha, "candidate binary hash differs from the expected release")
    if "binary_sha256" in value: check(value["binary_sha256"] == binary_sha, "launch manifest records a different binary")
    if "binary" in value: check(Path(value["binary"]).resolve() == executable, "launch manifest records a different binary path")
    assert_identity(value)
    pid = value["pid"]
    check(Path(f"/proc/{pid}").stat().st_uid == os.getuid(), "candidate belongs to another user")
    check(Path(f"/proc/{pid}/exe").resolve() == executable and listener_owned(pid, 7210),
          "manifest PID does not own the expected executable and listener")
    executable_info = Path(f"/proc/{pid}/exe").stat()
    check((executable_info.st_dev, executable_info.st_ino) == (executable.stat().st_dev, executable.stat().st_ino),
          "candidate executable changed during binary verification")
    argv = Path(f"/proc/{pid}/cmdline").read_bytes().decode().split("\0")
    env_raw = Path(f"/proc/{pid}/environ").read_bytes()
    check(len(env_raw) <= 1024*1024, "candidate environment exceeds its metadata bound")
    env = dict(item.split("=", 1) for item in env_raw.decode().split("\0") if "=" in item)
    cwd = Path(f"/proc/{pid}/cwd").resolve()
    resolve = lambda raw: (Path(raw) if Path(raw).is_absolute() else cwd / raw).resolve()
    checks = (("state-dir", home, ("CODEXPRO_GO_HOME", "CODEXPRO_HOME")),
              ("token-file", token, ("CODEXPRO_GO_TOKEN_FILE",)),
              ("projects-file", catalog, ("CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_PROJECTS_FILE")))
    for flag, expected, keys in checks:
        actual = cli_value(argv, flag) or next((env[k] for k in keys if env.get(k)), None)
        check(actual and resolve(actual) == expected, "candidate " + flag + " differs from the private launch binding")
    check(env.get("GOMAXPROCS") == "2", "candidate must already have GOMAXPROCS=2; this observer will not reconfigure it")
    projects, catalog_sha = bounded_json(catalog)
    selected = [row for row in projects.get("projects", []) if row.get("id") == project]
    check(len(selected) == 1, "profile project is missing or ambiguous in the private catalog")
    root = Path(selected[0]["root"]).resolve(strict=True)
    check(root.is_dir(), "profile project root is not a directory")
    workspace = "ws_" + hashlib.sha256(str(root).encode()).hexdigest()[:24]
    assert_identity(value)
    return {"pid": pid, "process_start_ticks": value["process_start_ticks"], "port": 7210,
            "state_root": str(state), "config_home": str(home), "token_file": str(token), "projects_file": str(catalog),
            "catalog_sha256": catalog_sha, "manifest": str(manifest_path), "manifest_sha256": manifest_sha,
            "binary": str(executable), "binary_sha256": binary_sha, "project": project,
            "binary_device": executable_info.st_dev, "binary_inode": executable_info.st_ino,
            "project_root": str(root), "workspace": workspace, "gomaxprocs": 2,
            "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "cgroup": cgroup_for(pid)}


def load_profiler(path):
    spec = importlib.util.spec_from_file_location("sp6_exact_profile", path)
    module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module


def profile_summary(report):
    rows = report.get("measurements", [])
    return {"complete": report.get("requested_calls") == 160 and len(rows) == 160
            and {row.get("index") for row in rows} == set(range(160)),
            "failures": sum(not row.get("ok") for row in rows),
            "initialization_errors": len(report.get("initialization_errors", [])),
            "aggregate": report.get("aggregate"), "resources": report.get("resources")}


def growth_summary(samples, profiles):
    idle = [row for row in samples if row["phase"] == "idle"]
    post = [row["rss_after_kb"] for row in profiles if "rss_after_kb" in row]
    slope = None
    if len(idle) >= 2:
        xs, ys = [row["elapsed_seconds"] / 60 for row in idle], [row["rss_kb"] for row in idle]
        xm, ym = sum(xs)/len(xs), sum(ys)/len(ys)
        denom = sum((x-xm)**2 for x in xs)
        if denom: slope = sum((x-xm)*(y-ym) for x,y in zip(xs,ys))/denom
    return {"post_profile_rss_kb": post,
            "post_profile_rss_deltas_kb": [b-a for a,b in zip(post, post[1:])],
            "post_profile_strictly_increasing": len(post) == 3 and all(b>a for a,b in zip(post,post[1:])),
            "idle_first_kb": idle[0]["rss_kb"] if idle else None,
            "idle_last_kb": idle[-1]["rss_kb"] if idle else None,
            "idle_slope_kb_per_minute": slope,
            "assessment": "Raw bounded observations for review; no growth tolerance or unbounded stability claim is inferred."}


class Child:
    def __init__(self, soak, index, output):
        self.soak, self.index, self.output = soak, index, output
        self.child = self.log = self.witness = None
        self.receipt_path = soak.control / ("profile-" + str(index) + "-launch.json")
        self.receipt = {"kind": "sp6-soak-profiler", "phase": "prepared", "profile": index,
            "server_pid": soak.binding["pid"], "server_start_ticks": soak.binding["process_start_ticks"],
            "pid": None, "process_start_ticks": None, "boot_id": soak.binding["boot_id"],
            "profile_source_sha256": soak.profile_sha, "output": str(output)}

    def start(self):
        write_launch_receipt(self.receipt_path, self.receipt, new=True)
        self.log = (self.soak.control / ("profile-"+str(self.index)+".log")).open("xb")
        os.chmod(self.log.name, 0o600)
        command = ["nice", "-n", "10", sys.executable, str(Path(__file__).resolve()), "--profile-child",
                   str(self.soak.binding_file), "--profiler", str(self.soak.profiler), "--profile-sha", self.soak.profile_sha,
                   "--profile-index", str(self.index), "--output", str(self.output)]
        env = sanitized_environment(); env["GOMAXPROCS"] = "2"
        try:
            self.child = subprocess.Popen(command, cwd=self.soak.control, env=env, stdin=subprocess.DEVNULL,
                stdout=self.log, stderr=self.log, start_new_session=True)
        except Exception:
            self.receipt["phase"] = "launch_outcome_unknown"; write_launch_receipt(self.receipt_path, self.receipt)
            raise
        self.receipt.update(phase="spawned_unverified", pid=self.child.pid)
        write_launch_receipt(self.receipt_path, self.receipt)
        identity = process_identity(self.child.pid)
        check(identity and self.child.poll() is None, "profile child exited before launch identity verification")
        self.witness = int(identity["identity"])
        check(os.getpgid(self.child.pid) == self.child.pid and os.getsid(self.child.pid) == self.child.pid,
              "profile child lacks an owned group/session")
        self.receipt.update(phase="identified", process_start_ticks=self.witness, cgroup=cgroup_for(self.child.pid),
                            nice=os.getpriority(os.PRIO_PROCESS, self.child.pid))
        write_launch_receipt(self.receipt_path, self.receipt)

    def stop(self):
        try:
            check(self.receipt["phase"] != "launch_outcome_unknown", "unknown profile launch has no signal authority")
            result = OwnedServer.stop(self)
            self.receipt.update(phase="stopped", cleanup=result)
            write_launch_receipt(self.receipt_path, self.receipt)
            return result
        finally:
            if self.log: self.log.close()


class Soak:
    def __init__(self, args):
        self.started = time.monotonic(); self.deadline = self.started + 735
        self.binding = bind(args.manifest, args.binary, args.expected_binary_sha, args.project)
        check(os.getpriority(os.PRIO_PROCESS, 0) >= 10, "run the observer under nice -n10 or lower priority")
        if args.workspace: check(args.workspace == self.binding["workspace"], "workspace does not match the private catalog project root")
        self.profiler = Path(args.profiler).resolve(strict=True)
        self.profile_sha = hashlib.sha256(self.profiler.read_bytes()).hexdigest()
        check(self.profile_sha == args.profile_sha, "profiler differs from the expected unchanged source")
        self.module = load_profiler(self.profiler)
        self.control = private_path(args.control_dir, missing=True)
        check(self.control.is_relative_to(Path.home().resolve()/".codexpro-go-proof")
              and not self.control.exists(), "soak control directory must be a new private proof child")
        check(not self.control.is_relative_to(Path(self.binding["state_root"]))
              and not Path(self.binding["state_root"]).is_relative_to(self.control),
              "observer control files must remain separate from candidate state")
        self.output = artifact_path(args.output)
        self.outputs = [self.output.with_name(self.output.stem+"-profile-"+str(i)+".json") for i in range(3)]
        check(not self.output.exists() and all(not path.exists() for path in self.outputs), "soak refuses reused evidence")
        self.control.mkdir(parents=True, mode=0o700); self.output.parent.mkdir(parents=True, exist_ok=True)
        self.binding_file = self.control / "binding.json"
        write_launch_receipt(self.binding_file, self.binding, new=True)
        self.series_path = self.control / "samples.json"
        self.children, self.next_sample, self.phase = [], 0.0, "idle"
        self.report = {"kind": "sp6-bounded-readonly-soak", "binding": self.binding,
            "profiler_sha256": self.profile_sha, "cache_condition": args.cache_condition,
            "prior_profile_calls": args.prior_profile_calls, "no_http_warmup_by_driver": True,
            "requested_profile_offsets_seconds": list(OFFSETS), "sample_interval_seconds": 5,
            "idle_limit_kb": IDLE_KB, "peak_limit_kb": PEAK_KB, "internal_wall_limit_seconds": 735,
            "external_wall_limit_seconds": 750, "no_load_generated": True,
            "samples": [], "profiles": [], "cleanup": [], "cleanup_errors": [],
            "scope": "finite warm/cold-declared observation; soak latencies do not replace the separate cold-target profile"}
        check(args.cache_condition != "fresh-no-prior-profile" or args.prior_profile_calls == 0,
              "fresh cache declaration conflicts with prior calls")

    def sample(self, force=False):
        elapsed = time.monotonic() - self.started
        if not force and elapsed < self.next_sample: return None
        assert_identity(self.binding)
        sample = self.module.sample_process(self.binding["pid"])
        check(not sample.get("unavailable"), "candidate resource sample became unavailable")
        sample.update(elapsed_seconds=elapsed, phase=self.phase)
        self.report["samples"].append(sample)
        self.next_sample = elapsed + 5
        write_launch_receipt(self.series_path, self.report["samples"], new=not self.series_path.exists())
        return sample

    def until(self, target, child=None):
        while time.monotonic() < target:
            check(time.monotonic() < self.deadline, "soak exceeded its bounded wall budget")
            self.sample()
            if child is not None and child.child.poll() is not None: return
            time.sleep(min(.2, max(0, target-time.monotonic())))

    def run(self):
        try:
            for index, offset in enumerate(OFFSETS):
                self.until(self.started + offset)
                before = self.sample(force=True)
                self.phase = "profile-"+str(index)
                row = {"index": index, "scheduled_offset_seconds": offset,
                       "actual_start_seconds": time.monotonic()-self.started, "rss_before_kb": before["rss_kb"],
                       "evidence": str(self.outputs[index])}
                self.report["profiles"].append(row)
                child = Child(self, index, self.outputs[index]); self.children.append(child); child.start()
                self.until(min(self.deadline-10, time.monotonic()+130), child)
                check(child.child.poll() is not None, "bounded profile did not finish within130seconds")
                check(child.child.returncode == 0, "owned profile child failed; preserve its private log")
                raw, sha = bounded_json(self.outputs[index], 16*1024*1024)
                row.update(profile_summary(raw), evidence_sha256=sha, elapsed_seconds=raw.get("elapsed_seconds"))
                self.phase = "idle"
                row["rss_after_kb"] = self.sample(force=True)["rss_kb"]
                row["actual_end_seconds"] = time.monotonic()-self.started
                self.report["cleanup"].append({"profile": index, **child.stop()}); self.children.remove(child)
                print(json.dumps({"profile": index, "complete": row["complete"], "failures": row["failures"],
                    "rss_after_kb": row["rss_after_kb"]}), flush=True)
            assert_identity(self.binding)
        except Exception as error:
            self.report["failure"] = type(error).__name__ + ": " + str(error)[:1500]
        finally:
            for child in self.children:
                try: self.report["cleanup"].append({"profile": child.index, **child.stop()})
                except Exception as error: self.report["cleanup_errors"].append(type(error).__name__ + ": owned profiler cleanup failed")
            samples = self.report["samples"]
            # Keep the profiler's 0.5s samples as separate raw evidence and include
            # them in the peak check; the five-second series remains unfiltered.
            profile_peaks = [r.get("resources", {}).get("rss_peak_kb", 0) for r in self.report["profiles"]]
            idle = [r["rss_kb"] for r in samples if r["phase"] == "idle"]
            self.report["idle_peak_kb"] = max(idle, default=0)
            self.report["overall_peak_kb"] = max([r["rss_kb"] for r in samples]+profile_peaks, default=0)
            self.report["memory_targets_pass"] = bool(idle) and self.report["idle_peak_kb"] <= IDLE_KB and self.report["overall_peak_kb"] <= PEAK_KB
            self.report["growth_observations"] = growth_summary(samples, self.report["profiles"])
            self.report["elapsed_seconds"] = time.monotonic()-self.started
            self.report["server_control_actions"] = []
            self.report["passed_declared_error_and_memory_gates"] = (not self.report.get("failure") and not self.report["cleanup_errors"]
                and len(self.report["profiles"]) == 3 and all(r.get("complete") and r.get("failures") == 0
                and r.get("initialization_errors") == 0 for r in self.report["profiles"]) and self.report["memory_targets_pass"])
            write_launch_receipt(self.output, self.report, new=True)
        return self.report


def child_main(args):
    binding, unused = bounded_json(args.profile_child)
    path = Path(args.profiler).resolve(strict=True)
    check(hashlib.sha256(path.read_bytes()).hexdigest() == args.profile_sha, "profile source changed before child execution")
    assert_identity(binding); private_http()
    module = load_profiler(path)
    original = module.Client.rpc
    def guarded_rpc(self, *a, **kw):
        assert_identity(binding)
        return original(self, *a, **kw)
    module.Client.rpc = guarded_rpc
    sys.argv = [str(path), "--url", "http://127.0.0.1:7210/mcp", "--token-file", binding["token_file"],
        "--workspace", binding["workspace"], "--project", binding["project"], "--pid", str(binding["pid"]),
        "--count", "160", "--concurrency", "2", "--duration", "180", "--delay-ms", "50",
        "--label", "private-go-soak-profile-"+str(args.profile_index), "--out", args.output]
    module.main()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ("manifest", "binary", "expected-binary-sha", "profiler", "profile-sha", "control-dir", "output", "profile-child"):
        parser.add_argument("--"+key)
    parser.add_argument("--project", default="codexpro")
    parser.add_argument("--workspace")
    parser.add_argument("--profile-index", type=int)
    parser.add_argument("--cache-condition", choices=("warm-prior-traffic", "fresh-no-prior-profile"))
    parser.add_argument("--prior-profile-calls", type=int, default=0)
    args = parser.parse_args()
    if args.profile_child: child_main(args); return 0
    for key in ("manifest", "binary", "expected_binary_sha", "profiler", "profile_sha", "control_dir", "output", "cache_condition"):
        check(getattr(args, key), "missing required --"+key.replace("_", "-"))
    check(args.prior_profile_calls >= 0, "prior profile count cannot be negative")
    soak = Soak(args)
    def interrupted(signum, frame): raise Failure("soak interrupted; candidate remains externally owned")
    old = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try: result = soak.run()
    finally:
        for sig, handler in old.items(): signal.signal(sig, handler)
    print(json.dumps({"output": str(soak.output), "passed": result["passed_declared_error_and_memory_gates"],
        "idle_peak_kb": result["idle_peak_kb"], "overall_peak_kb": result["overall_peak_kb"],
        "cleanup_errors": len(result["cleanup_errors"])}))
    return 0 if result["passed_declared_error_and_memory_gates"] else 1


if __name__ == "__main__": raise SystemExit(main())
