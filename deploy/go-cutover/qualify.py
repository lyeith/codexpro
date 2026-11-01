#!/usr/bin/env python3
"""Loaded-startup qualification for the Go service (cutover window).

  qualify.py --base-url URL --token-file F --project P --read-path R \
             --start-cmd "systemctl --user start codexpro.service" [--workers 40] [--output F]

Starts --workers nice-19 CPU busy loops in one owned transient scope
(codexpro-cutover-load-<pid>.scope, each worker capped by timeout), lets the load
build, then runs --start-cmd and measures, from immediately before it:
  * first authenticated /healthz 200, and
  * MCP initialize plus the first read of --read-path.
Then three further health/read pairs while load continues.

Pass limits (STARTUP-AMENDMENT.md): every measurement <= 3000 ms.
Guard: once the server has answered, health is polled at most every 4 s with a
1 s timeout; a failed or slow response stops the load immediately (the guard
watches the service being qualified, which is the only server at this point).
The loaded window is capped at --max-window seconds (default 90, below 5 min).
Only the owned scope is stopped. Token is read from file, sent as a header only.
"""
import argparse
import json
import os
import subprocess
import sys
import time
import urllib.request

sys.path.insert(0, os.path.dirname(__file__))
from smoke import Client  # noqa: E402

LIMIT_MS = 3000.0


def health(base, token, timeout=1.0):
    req = urllib.request.Request(base + "/healthz", headers={"Authorization": "Bearer " + token})
    t0 = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status == 200, (time.monotonic() - t0) * 1000
    except Exception:
        return False, (time.monotonic() - t0) * 1000


def loadavg():
    return open("/proc/loadavg").read().split()[:3]


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", required=True)
    p.add_argument("--token-file", required=True)
    p.add_argument("--project", required=True)
    p.add_argument("--read-path", default="README.md")
    p.add_argument("--start-cmd", required=True)
    p.add_argument("--workers", type=int, default=40)
    p.add_argument("--warmup", type=float, default=3.0)
    p.add_argument("--max-window", type=float, default=90.0)
    p.add_argument("--output")
    a = p.parse_args()
    token = open(a.token_file).read().strip()
    unit = "codexpro-cutover-load-%d" % os.getpid()
    report = {"workers": a.workers, "load_unit": unit + ".scope", "limit_ms": LIMIT_MS, "load_before": loadavg()}
    worker_cap = int(a.max_window) + 10
    spawn = "for i in $(seq %d); do nice -n 19 timeout %d sh -c 'while :; do :; done' & done; wait" % (a.workers, worker_cap)
    load = subprocess.Popen(["systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit", unit, "--", "bash", "-c", spawn],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    load_started = time.monotonic()
    guard = {"fired": False}

    def stop_load(reason):
        if report.get("load_stopped"):
            return
        subprocess.run(["systemctl", "--user", "stop", unit + ".scope"], capture_output=True, timeout=30)
        try:
            load.wait(timeout=30)
        except subprocess.TimeoutExpired:
            pass
        report["load_stopped"] = {"reason": reason, "after_s": round(time.monotonic() - load_started, 3)}

    try:
        time.sleep(a.warmup)
        report["load_at_start"] = loadavg()
        t0 = time.monotonic()
        start = subprocess.run(["bash", "-c", a.start_cmd], capture_output=True, text=True, timeout=120)
        report["start_cmd"] = {"exit": start.returncode, "ms": round((time.monotonic() - t0) * 1000, 1),
                               "output": (start.stdout + start.stderr)[-500:]}
        # First health 200 from before the start command.
        health_ms = None
        while time.monotonic() - t0 < 30:
            ok, _ = health(a.base_url, token)
            if ok:
                health_ms = (time.monotonic() - t0) * 1000
                break
            time.sleep(0.05)
        report["health_ms"] = None if health_ms is None else round(health_ms, 1)
        read_ms = None
        if health_ms is not None:
            c = Client(a.base_url, token)
            c.initialize(30)
            ws = c.call("open_workspace", {"project_id": a.project, "include_tree": False}).get("workspace_id") or a.project
            c.call("read", {"workspace_id": ws, "path": a.read_path, "end_line": 5})
            read_ms = (time.monotonic() - t0) * 1000
            report["first_read_ms"] = round(read_ms, 1)
            further = []
            for _ in range(3):
                if time.monotonic() - load_started > a.max_window:
                    break
                ok, hms = health(a.base_url, token)
                if not ok or hms > 1000:
                    guard.update({"fired": True, "health_ms": round(hms, 1), "ok": ok})
                    stop_load("guard: health failed or over 1 s")
                    break
                r0 = time.monotonic()
                c.call("read", {"workspace_id": ws, "path": a.read_path, "end_line": 5})
                further.append({"health_ms": round(hms, 1), "read_ms": round((time.monotonic() - r0) * 1000, 1)})
                time.sleep(1.0)
            report["further"] = further
        report["load_during"] = loadavg()
    finally:
        stop_load("measurement complete")
    survivors = subprocess.run(["systemctl", "--user", "is-active", unit + ".scope"], capture_output=True, text=True).stdout.strip()
    report["load_scope_after"] = survivors or "inactive"
    report["guard"] = guard
    values = [report.get("health_ms"), report.get("first_read_ms")]
    values += [v for f in report.get("further", []) for v in f.values()]
    report["passed"] = (None not in values[:2]) and all(v is not None and v <= LIMIT_MS for v in values) and not guard["fired"] and len(report.get("further", [])) == 3
    text = json.dumps(report, indent=2)
    if a.output:
        open(a.output, "w").write(text + "\n")
    print(text)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
