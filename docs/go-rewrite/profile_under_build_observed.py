#!/usr/bin/env python3
"""Profile the same bounded compiler load, with witnessed load overlap.

This preserves profile_under_build.py's workload. Completion is the first
observed exit, bounded by the last successful alive observation; it is not
presented as the exact kernel exit time. The original receipts stay unchanged.
"""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import threading
import time


def overlap_observation(started, finished, last_alive, first_exit):
    """Return True/False only when an observation proves the entire interval."""
    if last_alive is not None and last_alive >= finished:
        return True
    if first_exit is not None and first_exit < finished:
        return False
    return None


class LoadObserver:
    def __init__(self, process):
        self.process = process
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.last_alive = self.first_exit = None
        self.last_alive_mono = self.first_exit_mono = None
        self.observations = 0
        self.thread = threading.Thread(target=self.monitor, name="compiler-observer", daemon=True)

    def observe(self):
        # The timestamp before poll is a conservative alive lower bound. An
        # exit timestamp after poll is a conservative completion upper bound.
        before, before_mono = time.time(), time.monotonic()
        code = self.process.poll()
        after, after_mono = time.time(), time.monotonic()
        with self.lock:
            self.observations += 1
            if code is None:
                self.last_alive = max(before, self.last_alive or before)
                self.last_alive_mono = max(before_mono, self.last_alive_mono or before_mono)
            elif self.first_exit is None:
                self.first_exit, self.first_exit_mono = after, after_mono
        return code

    def monitor(self):
        while not self.stop.is_set():
            if self.observe() is not None:
                return
            self.stop.wait(.05)

    def close(self):
        self.observe()
        self.stop.set()
        self.thread.join(timeout=1)
        if self.thread.is_alive():
            raise RuntimeError("compiler observer did not stop")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--url", default="http://127.0.0.1:7210/mcp")
    p.add_argument("--token-file", required=True)
    p.add_argument("--label", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--pid", type=int)
    p.add_argument("--workspace", default="ws_4d43e03b8d338283c2776d95")
    a = p.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,100}", a.label):
        p.error("label must be a bounded filename component")
    evidence = Path(a.out)
    load_receipt = evidence.with_name(evidence.stem + "-load.json")
    if evidence.exists() or load_receipt.exists():
        p.error("profile and load evidence outputs must be new")
    evidence.parent.mkdir(parents=True, exist_ok=True)
    run_dir = Path("/tmp") / ("sp6-load-" + a.label)
    # Refuse a previous cache instead of silently making a repeated build warm.
    run_dir.mkdir(mode=0o700, exist_ok=False)
    env = dict(os.environ, GOMAXPROCS="2", GOCACHE=str(run_dir / "gocache"))
    load_command = ["nice", "-n", "10", "timeout", "90s", "go", "build", "-p", "2", "-a",
                    "-o", str(run_dir / "compiler"), "cmd/compile"]
    started, started_mono = time.time(), time.monotonic()
    profile = None
    profile_started = profile_finished = profile_started_mono = profile_finished_mono = None
    failure = None
    with (run_dir / "build.log").open("x") as log:
        load = subprocess.Popen(load_command, env=env, stdout=log, stderr=subprocess.STDOUT,
                                start_new_session=True)
        observer = LoadObserver(load)
        observer.observe()
        observer.thread.start()
        try:
            time.sleep(2)
            command = [sys.executable, str(Path(__file__).with_name("profile.py")),
                       "--url", a.url, "--token-file", a.token_file, "--workspace", a.workspace,
                       "--count", "160", "--concurrency", "2", "--duration", "180", "--delay-ms", "50",
                       "--label", a.label, "--out", str(evidence)]
            if a.pid:
                command.extend(["--pid", str(a.pid)])
            profile_started, profile_started_mono = time.time(), time.monotonic()
            try:
                profile = subprocess.run(command, timeout=205)
            finally:
                profile_finished, profile_finished_mono = time.time(), time.monotonic()
                observer.observe()
            load.wait(timeout=95)
        except (OSError, subprocess.SubprocessError) as error:
            # Preserve a bounded error type, never a command containing a token.
            failure = type(error).__name__
        finally:
            if load.poll() is None:
                os.killpg(load.pid, signal.SIGTERM)
                try:
                    load.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(load.pid, signal.SIGKILL)
                    load.wait(timeout=3)
            observer.close()
    overlap = None if profile_finished_mono is None else overlap_observation(
        profile_started_mono, profile_finished_mono, observer.last_alive_mono, observer.first_exit_mono)
    record = {
        "load_command": load_command, "load_started_epoch": started,
        "elapsed_seconds": time.monotonic() - started_mono, "load_pid": load.pid,
        "load_exit_code": load.returncode, "bounded_timeout_expected": load.returncode == 124,
        "profile_exit_code": profile.returncode if profile else None,
        "build_log": str(run_dir / "build.log"), "failure": failure,
        "observation_version": 1, "observer_interval_seconds": .05,
        "observer_count": observer.observations,
        "load_finished_epoch": observer.first_exit,
        "load_last_alive_epoch": observer.last_alive,
        "load_finish_is_observed_upper_bound": True,
        "profile_started_epoch": profile_started, "profile_finished_epoch": profile_finished,
        "load_started_monotonic": started_mono,
        "load_last_alive_monotonic": observer.last_alive_mono,
        "load_finished_monotonic": observer.first_exit_mono,
        "profile_started_monotonic": profile_started_mono,
        "profile_finished_monotonic": profile_finished_mono,
        "continuous_overlap_verified": overlap,
        "overlap_method": "Monotonic last-alive lower bound and first-observed-exit upper bound against the complete profile subprocess interval.",
        "load_environment": {"GOMAXPROCS": "2", "GOCACHE": str(run_dir / "gocache"), "fresh_cache": True},
    }
    with load_receipt.open("x") as output:
        json.dump(record, output, indent=2)
        output.write("\n")
    print(json.dumps(record, indent=2))
    return 0 if profile and profile.returncode == 0 and failure is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
