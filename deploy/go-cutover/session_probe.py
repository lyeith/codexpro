#!/usr/bin/env python3
"""Observe what an MCP client with an open session sees across a service switch.

  session_probe.py --base-url http://127.0.0.1:7212 --token-file TOKEN \
      --project scratch --switch-cmd "<command that performs the switch>" [--output probe.json]

1. Initialises session S and confirms a read works.
2. Starts an in-flight call on S (bash: sleep --inflight-seconds; echo) and,
   once it is executing, runs --switch-cmd in a subprocess.
3. While the switch runs, polls tools/list on S every --interval seconds with a
   new TCP connection each time, recording HTTP status, JSON-RPC error code and
   message, and latency, until a response comes from the new process.
4. Records how the in-flight call ended (completed result, error, reset).
5. Re-initialises (as an MCP client must after HTTP 404) and runs a read.

Token is read from the file and only sent as a header.
"""
import argparse
import json
import subprocess
import sys
import threading
import time

sys.path.insert(0, __import__("os").path.dirname(__file__))
from smoke import Client  # noqa: E402


def now():
    return time.monotonic()


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", required=True)
    p.add_argument("--token-file", required=True)
    p.add_argument("--project", required=True)
    p.add_argument("--read-path", default="README.md")
    p.add_argument("--switch-cmd", required=True)
    p.add_argument("--inflight-seconds", type=float, default=8)
    p.add_argument("--interval", type=float, default=0.25)
    p.add_argument("--max-seconds", type=float, default=180)
    p.add_argument("--output")
    a = p.parse_args()
    token = open(a.token_file).read().strip()
    t0 = now()
    rel = lambda: round(now() - t0, 3)  # noqa: E731
    events = []
    c = Client(a.base_url, token)
    result, _ = c.initialize(10)
    old_session = c.session
    ws = c.call("open_workspace", {"project_id": a.project, "include_tree": False}).get("workspace_id") or a.project
    c.call("read", {"workspace_id": ws, "path": a.read_path, "end_line": 3})
    events.append({"t": rel(), "event": "session_ready", "server": result.get("serverInfo")})

    inflight = {}

    def run_inflight():
        k = Client(a.base_url, token)
        k.session = old_session
        k.next_id = 1000
        started = rel()
        try:
            st, _, reply = k.rpc("tools/call", {"name": "bash", "arguments": {
                "workspace_id": ws, "command": "sleep %s; echo inflight-done" % a.inflight_seconds,
                "timeout_ms": int((a.inflight_seconds + 30) * 1000)}}, timeout=a.max_seconds)
            res = (reply or {}).get("result") or {}
            sc = res.get("structuredContent") or {}
            inflight.update({"started": started, "ended": rel(), "status": st,
                             "is_error": res.get("isError"), "stdout": (sc.get("stdout") or "")[:80],
                             "job_status": sc.get("job_status"), "rpc_error": (reply or {}).get("error")})
        except Exception as e:
            inflight.update({"started": started, "ended": rel(), "exception": str(e)[:300]})

    th = threading.Thread(target=run_inflight, daemon=True)
    th.start()
    time.sleep(1.0)
    events.append({"t": rel(), "event": "switch_started"})
    switch = subprocess.Popen(["bash", "-c", a.switch_cmd], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)

    polls = []
    new_process_seen = None
    deadline = now() + a.max_seconds
    while now() < deadline:
        k = Client(a.base_url, token)
        k.session = old_session
        k.next_id = 5000 + len(polls)
        sent = rel()
        try:
            st, headers, reply = k.rpc("tools/list", timeout=a.max_seconds)
            err = (reply or {}).get("error") or {}
            entry = {"sent": sent, "done": rel(), "status": st, "code": err.get("code"), "message": err.get("message"),
                     "retry_after": headers.get("Retry-After"), "tools": len(((reply or {}).get("result") or {}).get("tools", []))}
        except Exception as e:
            entry = {"sent": sent, "done": rel(), "exception": str(e)[:200]}
        polls.append(entry)
        if entry.get("status") == 404 and new_process_seen is None:
            new_process_seen = entry
        if new_process_seen and switch.poll() is not None:
            break
        time.sleep(a.interval)
    switch_out, _ = switch.communicate(timeout=a.max_seconds)
    events.append({"t": rel(), "event": "switch_finished", "exit": switch.returncode})
    th.join(timeout=a.max_seconds)

    # Collapse consecutive identical poll outcomes to keep the record readable.
    collapsed = []
    for e in polls:
        key = (e.get("status"), e.get("code"), e.get("message"), e.get("exception"))
        if collapsed and collapsed[-1]["key"] == key:
            collapsed[-1]["count"] += 1
            collapsed[-1]["last_sent"] = e["sent"]
            collapsed[-1]["max_latency"] = max(collapsed[-1]["max_latency"], round(e["done"] - e["sent"], 3))
        else:
            collapsed.append({"key": key, "first_sent": e["sent"], "last_sent": e["sent"], "count": 1,
                              "max_latency": round(e["done"] - e["sent"], 3), "sample": e})
    for e in collapsed:
        e.pop("key")

    recovery = {}
    t1 = now()
    try:
        r = Client(a.base_url, token)
        result, attempts = r.initialize(30)
        ws2 = r.call("open_workspace", {"project_id": a.project, "include_tree": False}).get("workspace_id") or a.project
        r.call("read", {"workspace_id": ws2, "path": a.read_path, "end_line": 3})
        recovery = {"ok": True, "ms": round((now() - t1) * 1000, 1), "attempts": attempts,
                    "server": result.get("serverInfo"), "new_session_differs": r.session != old_session}
    except Exception as e:
        recovery = {"ok": False, "error": str(e)[:300]}

    report = {"base_url": a.base_url, "events": events, "inflight_call": inflight, "polls_on_old_session": collapsed,
              "first_response_from_new_process": new_process_seen, "reinitialize": recovery,
              "switch_output_tail": switch_out[-3000:]}
    text = json.dumps(report, indent=2)
    if a.output:
        open(a.output, "w").write(text + "\n")
    print(text)
    return 0 if recovery.get("ok") else 1


if __name__ == "__main__":
    sys.exit(main())
