#!/usr/bin/env python3
"""Collect confirmed SessionPilot Pro submissions and align resets with the Codex API."""
import argparse
from datetime import datetime, timezone
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import re
import selectors
import signal
import sqlite3
import subprocess
import sys
import time
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("monitor", Path(__file__).with_name("codexpro-ralph-monitor.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def iso(seconds=None):
    return datetime.fromtimestamp(time.time() if seconds is None else seconds, timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def stamp(value):
    return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()


def project_key(value):
    parsed = urlsplit(value or "")
    match = re.match(r"/g/(g-p-[a-f0-9]{32})(?:[-/]|$)", parsed.path)
    return match.group(1) if parsed.hostname == "chatgpt.com" and match else None


def weekly_window(result, now=None):
    """Select by duration, not primary/secondary: weekly can be either."""
    now = time.time() if now is None else now
    bucket = (result.get("rateLimitsByLimitId") or {}).get("codex") or result.get("rateLimits") or {}
    if bucket.get("limitId") not in (None, "codex"):
        raise ValueError("Codex rate-limit bucket unavailable")
    windows = [bucket.get(key) for key in ("primary", "secondary")]
    matches = [w for w in windows if isinstance(w, dict) and w.get("windowDurationMins") == 10080]
    if len(matches) != 1:
        raise ValueError("Expected one Codex weekly window")
    value = matches[0]
    reset, used = value.get("resetsAt"), value.get("usedPercent")
    if not isinstance(reset, (int, float)) or not now < reset <= now + 604800 + 300:
        raise ValueError("Invalid Codex weekly reset timestamp")
    if not isinstance(used, (int, float)) or not 0 <= used <= 100:
        raise ValueError("Invalid Codex usage percentage")
    return {"resets_at": iso(reset), "window_minutes": 10080, "used_percent": used}


def read_codex_limits(command="codex"):
    # Read-only account API. No threads, model requests, or reset-credit consumption.
    process = subprocess.Popen([command, "app-server", "--stdio"], stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    buffer = b""

    def send(value):
        process.stdin.write((json.dumps(value) + "\n").encode()); process.stdin.flush()

    def receive(wanted):
        nonlocal buffer
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            while b"\n" in buffer:
                line, buffer = buffer.split(b"\n", 1)
                value = json.loads(line)
                if value.get("id") == wanted:
                    if "error" in value:
                        raise ValueError("Codex account API failed; check Codex login")
                    return value["result"]
            if not selector.select(min(1, max(0, deadline-time.monotonic()))):
                continue
            chunk = os.read(process.stdout.fileno(), 65536)
            if not chunk:
                raise ValueError("Codex account API exited")
            buffer += chunk
            if len(buffer) > 2_000_000:
                raise ValueError("Codex API response too large")
        raise ValueError("Codex account API timed out")

    try:
        send({"id": 1, "method": "initialize", "params": {"clientInfo": {"name": "codexpro_usage", "version": "1.0"}}})
        receive(1)
        send({"method": "initialized"})
        send({"id": 2, "method": "account/rateLimits/read"})
        result = receive(2)
        return weekly_window(result)
    finally:
        selector.close()
        try:
            os.killpg(process.pid, signal.SIGTERM)
            process.wait(timeout=5)
        except ProcessLookupError:
            process.wait()
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL); process.wait()
        process.stdin.close(); process.stdout.close()


class Ledger:
    def __init__(self, directory):
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        database = directory / "collector.sqlite"
        self.db = sqlite3.connect(database)
        os.chmod(database, 0o600)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute("PRAGMA synchronous=FULL")
        self.db.executescript("""
          CREATE TABLE IF NOT EXISTS turns(q TEXT,t TEXT,body TEXT NOT NULL,sent TEXT,PRIMARY KEY(q,t));
          CREATE TABLE IF NOT EXISTS cursors(identity TEXT PRIMARY KEY,offset INTEGER NOT NULL);
        """)

    def merge(self, query, turn, fields):
        if not query or not turn:
            return
        old = self.db.execute("SELECT body FROM turns WHERE q=? AND t=?", (query, turn)).fetchone()
        body = json.loads(old[0]) if old else {}
        body.update({k:v for k,v in fields.items() if v is not None})
        self.db.execute("INSERT INTO turns(q,t,body) VALUES (?,?,?) ON CONFLICT(q,t) DO UPDATE SET body=excluded.body",
                        (query, turn, json.dumps(body, sort_keys=True)))

    def query(self, query):
        if query.get("adapterId") != "chatgpt":
            return
        for turn in query.get("turns", []):
            evidence = turn.get("acceptanceEvidence") or {}
            self.merge(query.get("id"), turn.get("id"), {
                "project_url": query.get("projectUrl"), "created_at": turn.get("createdAt"),
                "submitted_at": turn.get("submittedAt"), "selected_state": evidence.get("selectedEffort"),
                "requested": turn.get("effort"),
                "failed_before_send": str(turn.get("error", "")).startswith("ChatGPT submission preparation failed before Send:")
            })

    def event(self, record):
        payload = record.get("payload") or {}
        event = payload.get("event") or {}
        if record.get("kind") != "query.event" or payload.get("adapterId") != "chatgpt" or event.get("name") != "turn-submitted":
            return
        snapshot = event.get("snapshot") or {}
        self.merge(payload.get("queryId"), event.get("turnId"), {
            "event_at": event.get("at"), "selected_event": snapshot.get("effort"),
            "event_url": snapshot.get("href")
        })

    def scan_lines(self, file, accept):
        # Cursor keys follow the inode through rotation. Commit receipts and cursor together.
        with file.open("rb") as stream:
            info = os.fstat(stream.fileno())
            identity = str(info.st_dev) + ":" + str(info.st_ino)
            old = self.db.execute("SELECT offset FROM cursors WHERE identity=?", (identity,)).fetchone()
            offset = old[0] if old and old[0] <= info.st_size else 0
            stream.seek(offset)
            while True:
                line = stream.readline()
                if not line or not line.endswith(b"\n"):
                    break
                accept(json.loads(line))
                offset = stream.tell()
            self.db.execute("INSERT INTO cursors VALUES (?,?) ON CONFLICT(identity) DO UPDATE SET offset=excluded.offset", (identity, offset))

    def scan(self, root):
        # State is atomically replaced by SessionPilot. No daemon/extension calls or tab interaction.
        with self.db:
            for file in sorted((root / "archive").glob("queries-*.jsonl")):
                self.scan_lines(file, self.query)
            state = json.loads((root / "state.json").read_text())
            for query in state.get("queries", {}).values():
                self.query(query)
            for file in sorted(root.glob("query-events*.jsonl")):
                self.scan_lines(file, self.event)

    def pending(self, projects):
        coverage = {"unconfirmed": 0, "failed_before_send": 0, "unknown_mode": 0, "oldest_record_at": None}
        pending = []
        for query, turn, raw, sent in self.db.execute("SELECT q,t,body,sent FROM turns ORDER BY q,t"):
            body = json.loads(raw)
            at = body.get("submitted_at") or body.get("event_at")
            mode = body.get("selected_state") or body.get("selected_event") or body.get("requested")
            oldest = body.get("created_at") or at
            if oldest and (not coverage["oldest_record_at"] or stamp(oldest) < stamp(coverage["oldest_record_at"])):
                coverage["oldest_record_at"] = iso(stamp(oldest))
            if not at:
                if mode == "Pro":
                    coverage["failed_before_send" if body.get("failed_before_send") else "unconfirmed"] += 1
                continue
            if not mode:
                coverage["unknown_mode"] += 1
                continue
            if mode != "Pro":
                continue
            key = project_key(body.get("project_url")) or project_key(body.get("event_url"))
            value = {"query_id": query, "turn_id": turn, "submitted_at": iso(stamp(at)),
                     "project_id": projects.get(key),
                     "mode_evidence": "selected" if body.get("selected_state") or body.get("selected_event") else "requested"}
            payload = json.dumps(value, sort_keys=True)
            if sent != payload:
                pending.append((value, payload))
        return pending, coverage

    def acknowledge(self, batch):
        with self.db:
            for value, payload in batch:
                self.db.execute("UPDATE turns SET sent=? WHERE q=? AND t=?", (payload, value["query_id"], value["turn_id"]))


def exchange(config, route, body=None):
    source = m.Sources({"activity_config": str(config)})
    command = ["curl", "--disable", "--config", str(source.auth), "--silent", "--show-error", "--fail",
               "--connect-timeout", "8", "--max-time", "30"]
    if body is not None:
        command += ["--header", "Content-Type: application/json", "--data-binary", "@-"]
    return m.run_json(command + [source.base + "/usage/v1/pro" + route], body, max_bytes=500000)


def cycle(args, ledger, api=exchange, read_limits=read_codex_limits):
    targets = json.loads(args.monitor_config.read_text()).get("targets", [])
    projects = {project_key(t.get("chatgpt_project_url")): t["project_id"] for t in targets if project_key(t.get("chatgpt_project_url"))}
    ledger.scan(args.sessionpilot_home)
    pending, coverage = ledger.pending(projects)
    # Persist local receipts before upload. Crash-after-send is safe: server deduplicates by query+turn.
    for start in range(0, max(1, len(pending)), 100):
        batch = pending[start:start+100]
        api(args.config, "/submissions", {"schema_version": 1, "collector_id": args.collector_id,
                                         "submissions": [x[0] for x in batch], "coverage": coverage})
        ledger.acknowledge(batch)
    status = api(args.config, "")
    settings = status["settings"]
    reset = settings.get("pending_reset")
    checked = settings.get("limits_checked_at")
    if reset or settings.get("limits_error") or status.get("reset_date_stale") or not checked or time.time()-stamp(checked) >= 300:
        value = {"schema_version": 1, "weekly": None}
        if reset:
            value["reset_request_id"] = reset["request_id"]
        try:
            value["weekly"] = read_limits(args.codex)
        except (OSError, ValueError) as error:
            print("Codex reset date unavailable: " + type(error).__name__, file=sys.stderr, flush=True)
        value["checked_at"] = iso()
        api(args.config, "/limits", value)
    return {"uploaded": len(pending), "coverage": coverage}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path.home()/".config/codexpro/activity-client.json")
    parser.add_argument("--monitor-config", type=Path, default=Path.home()/".config/codexpro/ralph-monitor.json")
    parser.add_argument("--sessionpilot-home", type=Path, default=Path.home()/".sessionpilot")
    parser.add_argument("--state-dir", type=Path, default=Path.home()/".local/state/codexpro-pro-usage")
    parser.add_argument("--collector-id", default="laptop-sessionpilot")
    parser.add_argument("--codex", default="codex")
    parser.add_argument("--watch", action="store_true")
    parser.add_argument("--replay", action="store_true", help="Replay locally retained receipts to recover the server ledger")
    parser.add_argument("--account-reset", action="store_true", help="Print only the weekly API reset metadata and exit")
    args = parser.parse_args()
    os.umask(0o077)
    if args.account_reset:
        print(json.dumps(read_codex_limits(args.codex))); return
    args.state_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (args.state_dir / "collector.lock").open("a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        ledger = Ledger(args.state_dir)
        if args.replay:
            with ledger.db:
                ledger.db.execute("UPDATE turns SET sent=NULL")
        try:
            while True:
                try:
                    result = cycle(args, ledger)
                    print(json.dumps({"at": iso(), **result}), flush=True)
                except (OSError, ValueError, sqlite3.Error, m.MonitorError) as error:
                    # Avoid logging transport bodies or credentials.
                    print(iso() + " usage sync failed: " + type(error).__name__, file=sys.stderr, flush=True)
                    if not args.watch:
                        raise SystemExit(1)
                if not args.watch:
                    break
                time.sleep(30)
        finally:
            ledger.db.close()


if __name__ == "__main__":
    main()
