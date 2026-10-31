#!/usr/bin/env python3
"""Bounded, read-only MCP latency and /proc sampler; never records credentials."""
import argparse
import concurrent.futures
import datetime
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import threading
import time
import urllib.error
import urllib.request

def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()

class Client:
    def __init__(self, url, token, timeout=20):
        self.url, self.token, self.timeout = url, token, timeout
        self.session = None
        self.sequence = 0
        init = self.rpc("initialize", {"protocolVersion": "2024-11-05",
                    "capabilities": {}, "clientInfo": {"name": "sp6-readonly-profile", "version": "1"}})
        if "error" in init:
            raise RuntimeError("initialize: " + str(init["error"]))
        self.rpc("notifications/initialized", {}, notification=True)

    def rpc(self, method, params, notification=False):
        self.sequence += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notification:
            body["id"] = self.sequence
        headers = {"Authorization": "Bearer " + self.token,
                   "Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": "2024-11-05"}
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        req = urllib.request.Request(self.url, json.dumps(body).encode(), headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=self.timeout) as response:
            self.session = response.headers.get("Mcp-Session-Id") or self.session
            if response.status == 202 or notification:
                response.read()
                return {}
            if "text/event-stream" in response.headers.get("Content-Type", ""):
                data = []
                for raw in response:
                    line = raw.decode().rstrip("\r\n")
                    if line.startswith("data:"):
                        data.append(line[5:].lstrip())
                    elif not line and data:
                        obj = json.loads("\n".join(data))
                        if obj.get("id") == self.sequence:
                            return obj
                        data = []
                raise RuntimeError("SSE ended without matching result")
            return json.loads(response.read())

    def close(self):
        if not self.session:
            return
        request = urllib.request.Request(self.url, headers={
            "Authorization": "Bearer " + self.token, "Mcp-Session-Id": self.session,
            "MCP-Protocol-Version": "2024-11-05"}, method="DELETE")
        try:
            with urllib.request.urlopen(request, timeout=3) as r:
                r.read()
        except (urllib.error.HTTPError, OSError):
            pass

def quantiles(values):
    values = sorted(values)
    if not values:
        return {"n": 0}
    def q(p):
        return round(values[max(0, math.ceil(p * len(values)) - 1)], 3)
    return {"n": len(values), "p50_ms": q(.5), "p95_ms": q(.95), "p99_ms": q(.99),
            "max_ms": q(1), "mean_ms": round(statistics.mean(values), 3)}

def choose_pid():
    main = int(subprocess.check_output(["systemctl", "--user", "show", "codexpro.service",
                                       "--property=MainPID", "--value"], text=True))
    children = Path("/proc/" + str(main) + "/task/" + str(main) + "/children").read_text().split()
    return int(children[0]) if children else main

def sample_process(pid):
    base = Path("/proc") / str(pid)
    try:
        stat = base.joinpath("stat").read_text().rsplit(")", 1)[1].split()
        status = dict(x.split(":", 1) for x in base.joinpath("status").read_text().splitlines() if ":" in x)
        io = dict(x.split(":", 1) for x in base.joinpath("io").read_text().splitlines() if ":" in x)
        return {"at": utc(), "mono": time.monotonic(), "cpu_ticks": int(stat[11])+int(stat[12]),
                "rss_kb": int(status["VmRSS"].split()[0]), "threads": int(status["Threads"]),
                "read_bytes": int(io.get("read_bytes", "0")), "write_bytes": int(io.get("write_bytes", "0")),
                "nice": int(stat[16]), "loadavg": os.getloadavg(),
                "cgroup": base.joinpath("cgroup").read_text().strip()}
    except (OSError, ValueError, KeyError):
        return {"at": utc(), "unavailable": True}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://127.0.0.1:7200/mcp")
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--workspace", default="ws_4d43e03b8d338283c2776d95")
    parser.add_argument("--project", default="codexpro")
    parser.add_argument("--count", type=int, default=160)
    parser.add_argument("--concurrency", type=int, default=2)
    parser.add_argument("--duration", type=int, default=180)
    parser.add_argument("--delay-ms", type=int, default=50)
    parser.add_argument("--pid", type=int)
    parser.add_argument("--label", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    if args.concurrency < 1 or args.concurrency > 4 or args.count > 1000 or args.duration > 600:
        parser.error("bounded profiling: concurrency 1..4, count <=1000, duration <=600")
    token = Path(args.token_file).expanduser().read_text().strip()
    pid = args.pid or choose_pid()
    calls = [
        ("list_projects", {}),
        ("server_config", {}),
        ("read", {"workspace_id": args.workspace, "path": "package.json", "max_bytes": 8000}),
        ("read", {"workspace_id": args.workspace, "path": "src/server.ts", "start_line": 1,
                  "end_line": 80, "max_bytes": 10000}),
        ("tree", {"workspace_id": args.workspace, "path": "src/tools", "max_depth": 2, "max_entries": 100}),
        ("search", {"workspace_id": args.workspace, "path": "src/worktrees/manager.ts",
                    "query": "orphaned", "intent": "text", "max_results": 20}),
        ("show_changes", {"workspace_id": args.workspace, "include_diff": False, "mark_reviewed": False}),
        ("activity_status", {}),
    ]
    rows, samples, errors = [], [], []
    lock, done = threading.Lock(), threading.Event()
    started = utc()
    begin = time.monotonic()
    deadline = begin + args.duration
    def sampler():
        while not done.wait(.5):
            samples.append(sample_process(pid))
    monitor = threading.Thread(target=sampler, daemon=True)
    samples.append(sample_process(pid))
    monitor.start()
    def worker(index):
        try:
            client = Client(args.url, token)
        except Exception as exc:
            with lock:
                errors.append({"worker": index, "initialize_error": type(exc).__name__})
            return
        try:
            for i in range(index, args.count, args.concurrency):
                if time.monotonic() >= deadline:
                    break
                name, arguments = calls[i % len(calls)]
                start = time.monotonic()
                row = {"index": i, "tool": name, "started_at": utc()}
                try:
                    reply = client.rpc("tools/call", {"name": name, "arguments": arguments})
                    result = reply.get("result", {})
                    row["ok"] = not ("error" in reply or result.get("isError"))
                    structured = result.get("structuredContent", {})
                    row["error_code"] = structured.get("error_code") or reply.get("error", {}).get("code")
                    row["response_bytes"] = len(json.dumps(reply).encode())
                    row["result_keys"] = sorted(structured)
                except Exception as exc:
                    row["ok"] = False
                    row["exception"] = type(exc).__name__
                row["duration_ms"] = (time.monotonic() - start) * 1000
                with lock:
                    rows.append(row)
                time.sleep(args.delay_ms/1000)
        finally:
            client.close()
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            list(pool.map(worker, range(args.concurrency)))
    finally:
        done.set()
        monitor.join(2)
        samples.append(sample_process(pid))
    elapsed = time.monotonic() - begin
    by_tool = {}
    for name, unused in calls:
        selected = [r for r in rows if r["tool"] == name]
        by_tool[name] = {**quantiles([r["duration_ms"] for r in selected]),
                         "failed": sum(not r["ok"] for r in selected)}
    good_samples = [s for s in samples if "cpu_ticks" in s]
    resources = {}
    if len(good_samples) >= 2:
        first, last = good_samples[0], good_samples[-1]
        resources = {"pid": pid, "rss_start_kb": first["rss_kb"], "rss_end_kb": last["rss_kb"],
                     "rss_peak_kb": max(s["rss_kb"] for s in good_samples),
                     "cpu_seconds": (last["cpu_ticks"]-first["cpu_ticks"])/os.sysconf("SC_CLK_TCK"),
                     "read_bytes_delta": last["read_bytes"]-first["read_bytes"],
                     "write_bytes_delta": last["write_bytes"]-first["write_bytes"],
                     "cgroup": first["cgroup"], "nice": first["nice"]}
    report = {"label": args.label, "started_at": started, "ended_at": utc(),
              "url": args.url, "requested_calls": args.count, "concurrency": args.concurrency,
              "duration_limit_seconds": args.duration, "delay_ms": args.delay_ms,
              "elapsed_seconds": elapsed, "rps": len(rows)/elapsed,
              "aggregate": quantiles([r["duration_ms"] for r in rows]),
              "failures": sum(not r["ok"] for r in rows), "by_tool": by_tool,
              "resources": resources, "initialization_errors": errors,
              "requests": [{"tool": n, "arguments": a} for n,a in calls],
              "measurements": sorted(rows, key=lambda r:r["index"]), "process_samples": samples}
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({k: report[k] for k in ("label","aggregate","failures","by_tool","resources","elapsed_seconds","initialization_errors")}, indent=2))

if __name__ == "__main__":
    main()
