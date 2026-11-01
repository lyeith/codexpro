"""Read-only live-service/admission guards for the bounded SP-6 startup load.

The guard never launches or signals a process. Its abort callback is supplied by
the owner and must immediately signal only witnessed load processes. Keep the
monitor running through owned cleanup; stop it only after all load is quiescent.
"""
from __future__ import annotations

import errno
import http.client
import io
import json
import os
from pathlib import Path
import select
import socket
import stat
import threading
import time
from datetime import datetime, timezone

HEALTH_SECONDS = 1.0
POLL_SECONDS = 4.0
MAX_GAP_SECONDS = 5.0
WINDOW_SECONDS = 300.0
MAX_HTTP_BYTES = 131072


class GuardAbort(RuntimeError):
    """A safety condition failed; this run cannot count as a pass."""


def utc_now():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def read_token_file(path):
    """Read a fixed credential path without atime updates; never return metadata."""
    descriptor = os.open(path, os.O_RDONLY | os.O_NOATIME | os.O_NOFOLLOW)
    try:
        before = os.fstat(descriptor)
        if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= 4096:
            raise ValueError("invalid token file")
        token = os.read(descriptor, 4097).strip()
        after = os.fstat(descriptor)
        identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if identity(before) != identity(after) or not token or len(token) > 4096:
            raise ValueError("token file changed or exceeded bound")
        if any(byte < 33 or byte > 126 for byte in token):
            raise ValueError("invalid token encoding")
        return token
    finally:
        os.close(descriptor)


def probe_live_health(live_token_file=None):
    """One raw loopback GET with a total 1 s deadline and optional read-only auth."""
    begin = time.monotonic()
    deadline = begin + HEALTH_SECONDS
    raw = bytearray()
    result = {"status": None, "ok": False}
    try:
        authentication = b"" if live_token_file is None else b"Authorization: Bearer " + read_token_file(live_token_file) + b"\r\n"
        request = b"GET /healthz HTTP/1.1\r\nHost: 127.0.0.1:7200\r\nConnection: close\r\n" + authentication + b"\r\n"
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as connection:
            connection.setblocking(False)
            error = connection.connect_ex(("127.0.0.1", 7200))
            if error not in (0, errno.EINPROGRESS, errno.EWOULDBLOCK, errno.EALREADY):
                raise OSError(error, "loopback connect failed")

            def ready(write=False):
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("absolute health deadline")
                readable, writable, _ = select.select(
                    [] if write else [connection], [connection] if write else [], [], remaining)
                if not (writable if write else readable):
                    raise TimeoutError("absolute health deadline")

            ready(write=True)
            if connection.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR):
                raise ConnectionError("loopback connection failed")
            pending = memoryview(request)
            while pending:
                ready(write=True)
                sent = connection.send(pending)
                if not sent:
                    raise ConnectionError("loopback request closed")
                pending = pending[sent:]
            expected = None
            while True:
                ready()
                chunk = connection.recv(16384)
                if not chunk:
                    break
                raw.extend(chunk)
                if len(raw) > MAX_HTTP_BYTES:
                    raise ValueError("health response exceeded bound")
                if expected is None and b"\r\n\r\n" in raw:
                    head, _ = raw.split(b"\r\n\r\n", 1)
                    lengths = [line.split(b":", 1)[1].strip() for line in head.split(b"\r\n")[1:]
                               if line.lower().startswith(b"content-length:")]
                    if len(lengths) > 1:
                        raise ValueError("duplicate health body length")
                    if lengths:
                        length = int(lengths[0])
                        if not 0 <= length <= 65536:
                            raise ValueError("health body exceeded bound")
                        expected = len(head) + 4 + length
                if expected is not None and len(raw) >= expected:
                    break

        class BufferedSocket:
            def makefile(self, *args, **kwargs):
                return io.BytesIO(raw)

        response = http.client.HTTPResponse(BufferedSocket())
        response.begin()
        body = response.read(65537)
        if len(body) > 65536:
            raise ValueError("health body exceeded bound")
        result.update(status=response.status, ok=response.status == 200 and json.loads(body).get("ok") is True)
    except Exception as error:
        result["error_class"] = type(error).__name__
    result["duration_ms"] = (time.monotonic() - begin) * 1000
    return result


def process_reasons(argv, cwd):
    """Inspect actual argv tokens, never shell -c script contents."""
    reasons = []
    if any(part.startswith("plans-land-") for part in Path(cwd or "/").parts):
        reasons.append("process-in-landing-worktree")
    if not argv:
        return reasons
    executable = Path(argv[0]).name
    arguments = argv[1:]
    if executable == "just":
        for index, word in enumerate(arguments):
            if word == "stack" and index + 1 < len(arguments) and arguments[index + 1] in (
                    "up", "reseed", "restart", "reset"):
                reasons.append("just-stack-bring-up")
            if word in ("land", "land-check", "landing"):
                reasons.append("just-landing")
    # Script filenames are literal argv, not text embedded in a shell argument.
    script = next((Path(arg).name for arg in arguments
                   if not arg.startswith("-") and not any(c.isspace() for c in arg)), "")
    if executable in ("python", "python3", "bash", "sh", "node") and script in (
            "stack.py", "dev_stack.py", "stack.mjs", "dev-stack.mjs", "stack.sh"):
        if any(word in ("up", "reseed", "restart", "reset") for word in arguments):
            reasons.append("stack-script-bring-up")
    return sorted(set(reasons))


def verified_user_supervisor(entry, argv):
    """Recognize only parent-backed supervisors whose cwd protection is expected."""
    import pwd
    import re

    def bounded(path, limit):
        with path.open("rb") as source:
            value = source.read(limit + 1)
        if len(value) > limit:
            raise ValueError("supervisor metadata exceeded bound")
        return value.decode(errors="strict").strip()

    uid = os.geteuid()
    comm = bounded(entry / "comm", 128)
    group = bounded(entry / "cgroup", 4096)
    init_group = f"0::/user.slice/user-{uid}.slice/user@{uid}.service/init.scope"
    if argv == ["/usr/lib/systemd/systemd", "--user"] and comm == "systemd" and group == init_group:
        return {"classification": "exact-systemd-user-init-scope"}
    fields = bounded(entry / "stat", 8192).rsplit(")", 1)[1].split()
    parent_pid = int(fields[1])
    if parent_pid <= 1:
        return None
    parent = entry.parent / str(parent_pid)
    before = bounded(parent / "stat", 8192).rsplit(")", 1)[1].split()[19]
    owner = parent.stat().st_uid
    parent_comm = bounded(parent / "comm", 128)
    parent_group = bounded(parent / "cgroup", 4096)
    parent_argv = bounded(parent / "cmdline", 65536).rstrip("\0").split("\0")
    after = bounded(parent / "stat", 8192).rsplit(")", 1)[1].split()[19]
    if before != after:
        return None
    classification = None
    if (argv == ["(sd-pam)"] and comm == "(sd-pam)" and group == init_group
            and owner == uid and parent_comm == "systemd" and parent_group == init_group
            and parent_argv == ["/usr/lib/systemd/systemd", "--user"]):
        classification = "sd-pam-child-of-exact-user-systemd"
    username = pwd.getpwuid(uid).pw_name
    if (argv in ([f"sshd-session: {username}"], [f"sshd-session: {username}@notty"])
            and comm == "sshd-session" and owner == 0 and parent_comm == "sshd-session"
            and parent_group == group
            and re.fullmatch(rf"0::/user.slice/user-{uid}\.slice/session-[1-9][0-9]*\.scope", group)):
        classification = "user-sshd-session-with-root-parent"
    return ({"classification": classification, "parent_pid": parent_pid,
             "parent_start_ticks": int(before)} if classification else None)


def inspect_admission(projects=Path("/home/spite/Projects"), proc=Path("/proc"), *, timeout=1.5, maximum=8192):
    """Fail closed on worktree existence, active startup, or incomplete inventory.

    A retained plans-land-* directory is not proof of an active landing, but its
    absence of observed processes is also not proof of quiescence. Record it as
    unverified and refuse admission; the coordinator must release that window.
    Other-user cwd links normally deny access. Their readable argv is inspected;
    same-user cwd/argv failures and any unreadable argv make admission uncertain.
    """
    began = time.monotonic()
    result = {"safe": False, "landing_worktrees": [], "blocking_processes": [],
              "errors": [], "processes_examined": 0, "other_user_cwd_not_inspected": 0,
              "verified_user_supervisors_without_cwd": []}
    try:
        result["landing_worktrees"] = sorted(
            path.name for path in projects.iterdir() if path.name.startswith("plans-land-"))
        entries = [entry for entry in proc.iterdir() if entry.name.isdigit()]
        if len(entries) > maximum:
            raise ValueError("process inventory exceeded bound")
        for entry in entries:
            if time.monotonic() - began > timeout:
                raise TimeoutError("process inventory exceeded deadline")
            try:
                owner = entry.stat().st_uid
                before = (entry / "stat").read_text().rsplit(")", 1)[1].split()[19]
                with (entry / "cmdline").open("rb") as source:
                    raw = source.read(65537)
                if len(raw) > 65536:
                    result["errors"].append({"pid": int(entry.name), "reason": "argv-too-large"})
                    continue
                argv = [part.decode(errors="replace") for part in raw.split(b"\0") if part]
                cwd = None
                if owner == os.geteuid():
                    try:
                        cwd = os.readlink(entry / "cwd")
                    except PermissionError:
                        supervisor = verified_user_supervisor(entry, argv)
                        if not supervisor:
                            raise
                        result["verified_user_supervisors_without_cwd"].append(
                            {"pid": int(entry.name), "start_ticks": int(before), **supervisor})
                    except FileNotFoundError:
                        # Exited processes and zombies have no cwd.
                        if not argv:
                            continue
                        if not entry.exists():
                            continue
                        raise
                else:
                    result["other_user_cwd_not_inspected"] += 1
                after = (entry / "stat").read_text().rsplit(")", 1)[1].split()[19]
                if before != after:
                    result["errors"].append({"pid": int(entry.name), "reason": "process-identity-changed"})
                    continue
                result["processes_examined"] += 1
                reasons = process_reasons(argv, cwd)
                if reasons:
                    result["blocking_processes"].append(
                        {"pid": int(entry.name), "start_ticks": int(before), "reasons": reasons})
            except (FileNotFoundError, ProcessLookupError):
                continue
            except Exception as error:
                result["errors"].append({"pid": int(entry.name), "error_class": type(error).__name__})
    except Exception as error:
        result["errors"].append({"error_class": type(error).__name__})
    result["duration_ms"] = (time.monotonic() - began) * 1000
    result["safe"] = not any(result[key] for key in ("landing_worktrees", "blocking_processes", "errors"))
    return result


class SafetyGuard:
    def __init__(self, *, health_probe=None, admission_probe=None, clock=time.monotonic,
                 utc=utc_now, interval=POLL_SECONDS):
        if not 0 < interval <= POLL_SECONDS:
            raise ValueError("poll interval must be positive and at most four seconds")
        self.health_probe = health_probe or probe_live_health
        self.admission_probe = admission_probe or inspect_admission
        self.clock, self.utc, self.interval = clock, utc, interval
        self.lock = threading.RLock()
        self.stopping = threading.Event()
        self.ready = threading.Event()
        self.thread = None
        self.callback = None
        self.last_health_started = None
        self.monitor_started = None
        self.data = {"schema_version": 1, "endpoint": "http://127.0.0.1:7200/healthz",
                     "health_limit_ms": 1000, "poll_interval_seconds": interval,
                     "maximum_allowed_gap_seconds": MAX_GAP_SECONDS, "maximum_observed_gap_seconds": 0,
                     "preflight_passed": False, "monitor_started": False, "monitor_stopped": False,
                     "failure": None, "stop_callback_calls": 0, "samples": []}

    def _trip(self, reason):
        with self.lock:
            if self.data["failure"] is not None:
                return
            self.data["failure"] = {"reason": reason, "utc": self.utc(), "monotonic": self.clock()}
            callback = self.callback
            self.stopping.set()
            if callback:
                self.data["stop_callback_calls"] += 1
        if callback:
            try:
                callback()
            except Exception as error:
                with self.lock:
                    self.data["stop_callback_error_class"] = type(error).__name__

    def _health(self, phase):
        started = self.clock()
        with self.lock:
            prior = self.last_health_started
            self.last_health_started = started
        gap = 0 if prior is None else started - prior
        row = {"phase": phase, "utc": self.utc(), "monotonic": started, "gap_seconds": gap}
        try:
            result = self.health_probe()
            row["health"] = result
            elapsed = max((self.clock() - started) * 1000, float(result.get("duration_ms", 0)))
            row["duration_ms"] = elapsed
            failed = result.get("status") != 200 or result.get("ok") is not True or elapsed > 1000
        except Exception as error:
            row.update(error_class=type(error).__name__, duration_ms=(self.clock() - started) * 1000)
            failed = True
        with self.lock:
            self.data["samples"].append(row)
            self.data["maximum_observed_gap_seconds"] = max(gap, self.data["maximum_observed_gap_seconds"])
        if gap > MAX_GAP_SECONDS:
            self._trip("health-monitor-gap-exceeded")
        elif failed:
            self._trip("live-health-failed-or-slower-than-one-second")
        return row

    def _admission(self, phase):
        try:
            record = self.admission_probe()
        except Exception as error:
            record = {"safe": False, "error_class": type(error).__name__}
        with self.lock:
            self.data["samples"].append({"phase": phase, "utc": self.utc(),
                                         "monotonic": self.clock(), "admission": record})
        if record.get("safe") is not True:
            self._trip("landing-or-stack-admission-not-clear")

    def _raise_failure(self):
        with self.lock:
            failure = self.data["failure"]
        if failure:
            raise GuardAbort(failure["reason"])

    def before_load(self):
        self._admission("preflight")
        self._raise_failure()
        self._health("preflight")
        self._raise_failure()
        with self.lock:
            self.data["preflight_passed"] = True
        return self.report()

    def start_monitor(self, stop_callback):
        self._raise_failure()
        if not self.data["preflight_passed"] or self.thread is not None or not callable(stop_callback):
            raise GuardAbort("monitor requires one successful preflight and an owned-stop callback")
        self.callback = stop_callback
        self.monitor_started = self.clock()
        self.data["monitor_started"] = True
        self.data["monitor_started_monotonic"] = self.monitor_started
        self.data["monitor_started_utc"] = self.utc()
        self.thread = threading.Thread(target=self._run, name="sp6-live-health-guard", daemon=True)
        self.thread.start()
        if not self.ready.wait(3):
            self._trip("monitor-did-not-complete-first-sample")
        self.check()

    def _run(self):
        try:
            while not self.stopping.is_set():
                started = self.clock()
                if started - self.monitor_started >= WINDOW_SECONDS:
                    self._trip("loaded-window-exceeded-five-minutes")
                    break
                self._health("monitor")
                self._raise_failure()
                self._admission("monitor")
                self._raise_failure()
                self.ready.set()
                remaining_window = self.monitor_started + WINDOW_SECONDS - self.clock()
                if self.stopping.wait(max(0, min(remaining_window, self.interval - (self.clock() - started)))):
                    break
        except GuardAbort:
            pass
        except Exception as error:
            self._trip("monitor-exception-" + type(error).__name__)
        finally:
            self.ready.set()

    def check(self):
        self._raise_failure()
        now = self.clock()
        if self.monitor_started is not None and now - self.monitor_started >= WINDOW_SECONDS:
            self._trip("loaded-window-exceeded-five-minutes")
        elif self.last_health_started is not None and now - self.last_health_started > MAX_GAP_SECONDS:
            self._trip("health-monitor-stale")
        elif self.thread is None or not self.thread.is_alive():
            self._trip("health-monitor-not-running")
        self._raise_failure()

    def stop(self):
        # The owner calls this only after the last owned load is quiescent.
        if self.monitor_started is not None and self.clock() - self.monitor_started >= WINDOW_SECONDS:
            self._trip("loaded-window-exceeded-five-minutes")
        self.stopping.set()
        if self.thread:
            self.thread.join(timeout=3)
            if self.thread.is_alive():
                self._trip("monitor-did-not-stop")
            elif self.data["failure"] is None:
                self._health("after-owned-cleanup")
        finished = self.clock()
        elapsed = None if self.monitor_started is None else finished - self.monitor_started
        if elapsed is not None and elapsed >= WINDOW_SECONDS:
            self._trip("loaded-window-exceeded-five-minutes")
        with self.lock:
            self.data["monitor_stopped"] = bool(self.thread and not self.thread.is_alive())
            self.data["monitor_finished_monotonic"] = finished
            self.data["monitor_finished_utc"] = self.utc()
            self.data["monitor_elapsed_seconds"] = elapsed
        return self.report()

    def report(self):
        with self.lock:
            result = json.loads(json.dumps(self.data))
        result["aborted"] = result["failure"] is not None
        result["passed"] = (result["preflight_passed"] and result["monitor_started"]
                            and result["monitor_stopped"] and not result["aborted"])
        return result