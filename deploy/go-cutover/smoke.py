#!/usr/bin/env python3
"""CodexPro smoke suite over the HTTP MCP route.

  smoke.py --base-url http://127.0.0.1:7200 --token-file ~/.config/codexpro/http-token \
           --mode readonly|full --project codexpro --read-path README.md \
           [--fixture-project scratch] [--expect-tools tools.json] [--output result.json]

inventory: health, MCP initialize and tools/list only.
readonly: health, MCP initialize (retried while the server reports it is
starting), tool inventory, list_projects, open_workspace, read, search, batch of
reads, activity_status, work_status list, usage and inbox reads, plus a Bash echo
and a background job start/output/stop (no repository writes).
full: additionally write, read-back, tag edit and a two-step write/bash batch in
--fixture-project, which must be a disposable scratch project.

The bearer token is read from --token-file and sent only as a header; it is never
printed or written to the output.
"""
import argparse
import json
import sys
import time
import urllib.error
import urllib.request
import uuid

PROTOCOL = "2025-06-18"


class Client:
    def __init__(self, base, token):
        self.base = base.rstrip("/")
        self.token = token
        self.session = None
        self.next_id = 1

    def http(self, method, path, body=None, headers=None, timeout=60):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + self.token)
        req.add_header("Accept", "application/json, text/event-stream")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if self.session:
            req.add_header("Mcp-Session-Id", self.session)
            req.add_header("MCP-Protocol-Version", PROTOCOL)
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.status, dict(r.headers), r.read()
        except urllib.error.HTTPError as e:
            return e.code, dict(e.headers), e.read()

    @staticmethod
    def decode(raw):
        try:
            return json.loads(raw)
        except ValueError:
            pass
        for line in raw.decode(errors="replace").splitlines():
            if line.startswith("data: "):
                try:
                    return json.loads(line[6:])
                except ValueError:
                    continue
        raise ValueError("no JSON-RPC message in response: %.200r" % raw[:200])

    def rpc(self, method, params=None, timeout=120):
        msg = {"jsonrpc": "2.0", "id": self.next_id, "method": method, "params": params or {}}
        self.next_id += 1
        status, headers, raw = self.http("POST", "/mcp", msg, timeout=timeout)
        return status, headers, self.decode(raw) if raw else None

    def initialize(self, budget):
        deadline = time.monotonic() + budget
        attempts = 0
        while True:
            attempts += 1
            self.session = None
            status, headers, reply = self.rpc("initialize", {
                "protocolVersion": PROTOCOL, "capabilities": {},
                "clientInfo": {"name": "codexpro-cutover-smoke", "version": "1"}})
            if status == 200 and reply and "result" in reply:
                self.session = headers.get("Mcp-Session-Id") or headers.get("mcp-session-id")
                st, _, _ = self.http("POST", "/mcp", {"jsonrpc": "2.0", "method": "notifications/initialized"})
                if st not in (200, 202):
                    raise RuntimeError("initialized notification status %d" % st)
                return reply["result"], attempts
            if status == 503 and time.monotonic() < deadline:
                time.sleep(0.5)
                continue
            raise RuntimeError("initialize failed: status %d %.300s" % (status, reply))

    def call(self, name, args, timeout=180):
        status, _, reply = self.rpc("tools/call", {"name": name, "arguments": args}, timeout=timeout)
        if status != 200 or not reply or "result" not in reply:
            raise RuntimeError("%s: status %d %.300s" % (name, status, reply))
        result = reply["result"]
        if result.get("isError"):
            text = " ".join(c.get("text", "") for c in result.get("content", []) if c.get("type") == "text")
            raise RuntimeError("%s returned an error: %.400s" % (name, text))
        return result.get("structuredContent") or {}


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", required=True)
    p.add_argument("--token-file", required=True)
    p.add_argument("--mode", choices=["inventory", "readonly", "full"], default="readonly")
    p.add_argument("--project", required=True, help="project used for reads, Bash echo and jobs")
    p.add_argument("--read-path", default="README.md")
    p.add_argument("--search", default="CodexPro")
    p.add_argument("--fixture-project", help="disposable scratch project for writes (full mode)")
    p.add_argument("--expect-tools", help="JSON file with the expected tool name list")
    p.add_argument("--save-tools", help="write the observed tool name list here")
    p.add_argument("--ready-budget", type=float, default=30.0)
    p.add_argument("--output")
    a = p.parse_args()
    with open(a.token_file) as f:
        token = f.read().strip()
    c = Client(a.base_url, token)
    steps = []
    started = time.monotonic()
    nonce = uuid.uuid4().hex[:12]
    state = {}

    def step(name, fn):
        t0 = time.monotonic()
        try:
            detail = fn()
            steps.append({"step": name, "ok": True, "ms": round((time.monotonic() - t0) * 1000, 1), "detail": detail})
            return True
        except Exception as e:  # report and continue so one failure does not hide the rest
            steps.append({"step": name, "ok": False, "ms": round((time.monotonic() - t0) * 1000, 1), "error": str(e)[:600]})
            return False

    def health():
        st, _, raw = c.http("GET", "/healthz", timeout=10)
        if st != 200:
            raise RuntimeError("health status %d" % st)
        body = json.loads(raw)
        return {"ok": body.get("ok"), "keys": sorted(body)[:20]}

    def init():
        result, attempts = c.initialize(a.ready_budget)
        return {"server": result.get("serverInfo"), "protocol": result.get("protocolVersion"), "attempts": attempts}

    def inventory():
        st, _, reply = c.rpc("tools/list")
        names = [t["name"] for t in reply["result"]["tools"]]
        state["tools"] = names
        if a.save_tools:
            with open(a.save_tools, "w") as f:
                json.dump(names, f, indent=1)
        detail = {"count": len(names)}
        if a.expect_tools:
            with open(a.expect_tools) as f:
                expected = json.load(f)
            missing = sorted(set(expected) - set(names))
            extra = sorted(set(names) - set(expected))
            detail.update({"missing": missing, "extra": extra, "order_equal": names == [n for n in expected if n in names]})
            if missing or extra:
                raise RuntimeError("inventory differs: missing=%s extra=%s" % (missing, extra))
        return detail

    def workspace(project):
        if project not in state:
            data = c.call("open_workspace", {"project_id": project, "include_tree": False})
            state[project] = data.get("workspace_id") or data.get("workspace", {}).get("workspace_id") or project
        return state[project]

    def projects():
        data = c.call("list_projects", {})
        ids = [p.get("id") or p.get("project_id") for p in data.get("projects", [])]
        if a.project not in ids:
            raise RuntimeError("project %s not listed" % a.project)
        return {"count": len(ids)}

    def read():
        data = c.call("read", {"workspace_id": workspace(a.project), "path": a.read_path, "end_line": 20})
        return {"lines": data.get("total_lines") or data.get("line_count"), "edit_tag": bool(data.get("edit_tag"))}

    def search():
        data = c.call("search", {"workspace_id": workspace(a.project), "query": a.search, "max_results": 5, "intent": "text"})
        return {"matches": data.get("total_matches", data.get("count"))}

    def batch_reads():
        data = c.call("batch", {"workspace_id": workspace(a.project), "operations": [
            {"id": "r1", "tool": "read", "args": {"path": a.read_path, "end_line": 5}},
            {"id": "t1", "tool": "tree", "args": {"max_depth": 1}},
            {"id": "s1", "tool": "search", "args": {"query": a.search, "max_results": 3}}], "mode": "parallel"})
        return {"ok": data.get("ok"), "keys": sorted(data)[:12]}

    def bash():
        data = c.call("bash", {"workspace_id": workspace(a.project), "command": "echo smoke-" + nonce, "timeout_ms": 30000})
        if ("smoke-" + nonce) not in (data.get("stdout") or ""):
            raise RuntimeError("bash output missing: %.200r" % data.get("stdout"))
        return {"exit_code": data.get("exit_code"), "job_status": data.get("job_status")}

    def job_cycle():
        ws = workspace(a.project)
        data = c.call("start_jobs", {"workspace_id": ws, "commands": [
            {"command": "for i in 1 2 3; do echo tick-$i; sleep 1; done", "label": "smoke-ticks"},
            {"command": "sleep 300", "label": "smoke-stop"}]})
        ids = [j.get("job_id") for j in data.get("jobs", [])]
        if len(ids) != 2 or not all(ids):
            raise RuntimeError("start_jobs ids: %r" % data)
        done = c.call("jobs", {"workspace_id": ws, "job_ids": [ids[0]], "wait_ms": 15000, "output": "tail"})
        view = done.get("jobs", [{}])[0]
        if view.get("status") != "succeeded" or "tick-3" not in (view.get("stdout_tail") or ""):
            raise RuntimeError("ticks job: %.300r" % view)
        stopped = c.call("stop_jobs", {"workspace_id": ws, "job_ids": [ids[1]]})
        after = c.call("jobs", {"workspace_id": ws, "job_ids": [ids[1]], "wait_ms": 10000, "output": "none"})
        sview = after.get("jobs", [{}])[0]
        if sview.get("status") == "running":
            raise RuntimeError("stopped job still running: %.300r" % sview)
        return {"ticks": view.get("status"), "stopped": sview.get("status"),
                "scope": view.get("scope_unit") or view.get("quiescence_scope"), "stop_keys": sorted(stopped)[:8]}

    def activity():
        data = c.call("activity_status", {})
        return {k: data.get(k) for k in ("storage_format", "retained_actions", "enabled") if k in data}

    def work_history():
        data = c.call("work_status", {"action": "list"})
        return {"runs": len(data.get("runs", [])) if isinstance(data.get("runs"), list) else data.get("count")}

    def usage_inbox():
        out = {}
        for path in ("/usage/v1/pro", "/inbox/v1/items"):
            st, _, raw = c.http("GET", path, timeout=30)
            if st != 200:
                raise RuntimeError("%s status %d" % (path, st))
            body = json.loads(raw)
            out[path] = {"keys": sorted(body)[:10], "items": len(body.get("items", [])) if isinstance(body.get("items"), list) else None}
        return out

    def fixture_writes():
        ws = workspace(a.fixture_project)
        path = "smoke/" + nonce + ".txt"
        c.call("write", {"workspace_id": ws, "path": path, "content": "one\ntwo\n", "create_dirs": True})
        got = c.call("read", {"workspace_id": ws, "path": path})
        tag = got.get("edit_tag")
        c.call("edit", {"workspace_id": ws, "path": path, "edit_tag": tag, "edits": [{"op": "replace", "start_line": 2, "content": "two-edited"}]})
        back = c.call("read", {"workspace_id": ws, "path": path})
        text = back.get("content") or back.get("text") or ""
        if "two-edited" not in text:
            raise RuntimeError("edit not visible: %.200r" % text)
        b = c.call("batch", {"workspace_id": ws, "operations": [
            {"id": "w", "tool": "write", "args": {"path": "smoke/" + nonce + "-batch.txt", "content": "batch\n", "create_dirs": True}},
            {"id": "v", "tool": "bash", "args": {"command": "cat smoke/" + nonce + "-batch.txt"}}]})
        changes = c.call("show_changes", {"workspace_id": ws})
        return {"edit": True, "batch_keys": sorted(b)[:10], "changes_keys": sorted(changes)[:8]}

    ok = step("health", health) and step("initialize", init)
    if ok:
        step("tools_list", inventory)
    if ok and a.mode != "inventory":
        step("list_projects", projects)
        step("read", read)
        step("search", search)
        step("batch_reads", batch_reads)
        step("bash_echo", bash)
        step("job_start_output_stop", job_cycle)
        step("activity_status", activity)
        step("work_status_list", work_history)
        step("usage_inbox_routes", usage_inbox)
        if a.mode == "full":
            if not a.fixture_project:
                steps.append({"step": "fixture_writes", "ok": False, "error": "--fixture-project required"})
            else:
                step("fixture_writes", fixture_writes)
    report = {"base_url": a.base_url, "mode": a.mode, "project": a.project,
              "total_ms": round((time.monotonic() - started) * 1000, 1),
              "passed": all(s["ok"] for s in steps), "failed": [s["step"] for s in steps if not s["ok"]],
              "steps": steps, "tool_count": len(state.get("tools", []))}
    text = json.dumps(report, indent=2)
    if a.output:
        with open(a.output, "w") as f:
            f.write(text + "\n")
    print(text)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
