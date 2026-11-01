"""Offline safety-guard tests: no real HTTP, compiler, load or signals."""
import errno
import json
from pathlib import Path
import tempfile
import threading
import types
import unittest
from unittest.mock import Mock, patch

import loaded_startup_guards as m


HEALTHY = {"status": 200, "ok": True, "duration_ms": 0.1}


class Guards(unittest.TestCase):
    def guard(self, **kwargs):
        defaults = {"health_probe": Mock(return_value=dict(HEALTHY)),
                    "admission_probe": Mock(return_value={"safe": True}), "interval": .01}
        defaults.update(kwargs)
        return m.SafetyGuard(**defaults)

    def test_preflight_failure_never_starts_monitor_or_callback(self):
        health = Mock(return_value=HEALTHY)
        guard = self.guard(health_probe=health, admission_probe=lambda: {"safe": False})
        with self.assertRaises(m.GuardAbort):
            guard.before_load()
        health.assert_not_called()
        self.assertIsNone(guard.thread)
        self.assertFalse(guard.report()["passed"])
        self.assertEqual(guard.report()["stop_callback_calls"], 0)

    def test_slow_or_failed_live_health_is_aborted_once(self):
        cases = [{"status": 200, "ok": True, "duration_ms": 1000.01},
                 {"status": 503, "ok": True, "duration_ms": 1},
                 {"status": 200, "ok": False, "duration_ms": 1},
                 {"status": None, "ok": False, "error_class": "TimeoutError"}]
        for result in cases:
            with self.subTest(result=result):
                callback = Mock()
                guard = self.guard(health_probe=lambda: result)
                guard.callback = callback
                guard._health("monitor")
                guard._trip("another-failure")
                callback.assert_called_once_with()
                self.assertTrue(guard.report()["aborted"])
                self.assertFalse(guard.report()["passed"])
                self.assertEqual(guard.report()["stop_callback_calls"], 1)

    def test_wall_latency_overrides_incorrect_probe_metric(self):
        now = [100.0]
        def probe():
            now[0] += 1.01
            return dict(HEALTHY)
        guard = self.guard(health_probe=probe, clock=lambda: now[0])
        guard._health("monitor")
        self.assertTrue(guard.report()["aborted"])

    def test_probe_exception_is_redacted_and_aborts(self):
        guard = self.guard(health_probe=Mock(side_effect=RuntimeError("secret-not-for-log")))
        with self.assertRaises(m.GuardAbort):
            guard.before_load()
        report = json.dumps(guard.report())
        self.assertNotIn("secret-not-for-log", report)
        self.assertIn("RuntimeError", report)

    def test_healthy_monitor_covers_cleanup_and_can_pass(self):
        callback = Mock()
        guard = self.guard()
        with patch.object(m.socket, "socket", side_effect=AssertionError("real network forbidden")):
            guard.before_load()
            self.assertFalse(guard.report()["passed"])
            guard.start_monitor(callback)
            guard.check()
            report = guard.stop()
        callback.assert_not_called()
        self.assertTrue(report["passed"])
        self.assertEqual(report["samples"][-1]["phase"], "after-owned-cleanup")
        self.assertGreaterEqual(len([s for s in report["samples"] if "health" in s]), 3)

    def test_background_monitor_aborts_without_parent_poll(self):
        unhealthy = threading.Event()
        stopped = threading.Event()
        def probe():
            return {"status": 503, "ok": False} if unhealthy.is_set() else dict(HEALTHY)
        guard = self.guard(health_probe=probe)
        guard.before_load()
        guard.start_monitor(stopped.set)
        unhealthy.set()
        self.assertTrue(stopped.wait(1), "independent monitor did not stop owned load")
        report = guard.stop()
        self.assertTrue(report["aborted"])
        self.assertEqual(report["stop_callback_calls"], 1)

    def test_new_stack_bringup_aborts_during_monitor(self):
        blocked = threading.Event()
        stopped = threading.Event()
        guard = self.guard(admission_probe=lambda: {"safe": not blocked.is_set()})
        guard.before_load()
        guard.start_monitor(stopped.set)
        blocked.set()
        self.assertTrue(stopped.wait(1))
        self.assertEqual(guard.stop()["failure"]["reason"], "landing-or-stack-admission-not-clear")

    def test_stale_dead_monitor_and_window_are_not_passes(self):
        for reason in ("stale", "dead", "window"):
            with self.subTest(reason=reason):
                now = [100.0]
                guard = self.guard(clock=lambda: now[0])
                guard.before_load()
                guard.callback = Mock()
                guard.monitor_started = 100
                guard.thread = types.SimpleNamespace(is_alive=lambda: reason != "dead")
                guard.last_health_started = 100
                if reason == "stale":
                    now[0] = 105.001
                elif reason == "window":
                    now[0] = 400
                with self.assertRaises(m.GuardAbort):
                    guard.check()
                guard.callback.assert_called_once_with()
                self.assertFalse(guard.report()["passed"])

    def test_observed_health_gap_is_retained(self):
        now = [100.0]
        guard = self.guard(clock=lambda: now[0])
        guard._health("preflight")
        now[0] = 105.1
        guard._health("monitor")
        report = guard.report()
        self.assertTrue(report["aborted"])
        self.assertAlmostEqual(report["maximum_observed_gap_seconds"], 5.1)

    def test_callback_exception_still_marks_abort(self):
        guard = self.guard()
        guard.callback = Mock(side_effect=RuntimeError("secret"))
        guard._trip("health-failed")
        report = guard.report()
        self.assertEqual(report["stop_callback_error_class"], "RuntimeError")
        self.assertTrue(report["aborted"])
        self.assertNotIn("secret", json.dumps(report))

    def test_monitor_requires_successful_preflight(self):
        guard = self.guard()
        with self.assertRaises(m.GuardAbort):
            guard.start_monitor(Mock())
        self.assertIsNone(guard.thread)

    def test_literal_process_detection_does_not_match_shell_script_text(self):
        self.assertEqual(m.process_reasons(["bash", "-lc", "python3 - <<'PY'\n# just stack up\nPY"], "/home/spite/Projects/codexpro-go"), [])
        self.assertIn("just-stack-bring-up", m.process_reasons(["/usr/bin/just", "stack", "up"], "/"))
        self.assertIn("just-landing", m.process_reasons(["just", "land-check"], "/"))
        self.assertIn("stack-script-bring-up", m.process_reasons(["python3", "scripts/dev_stack.py", "reseed"], "/"))
        self.assertIn("process-in-landing-worktree", m.process_reasons(["go", "test"], "/home/spite/Projects/plans-land-test/go"))

    def process(self, root, pid, argv, cwd):
        directory = root / str(pid)
        directory.mkdir()
        (directory / "stat").write_text(str(pid) + " (fake) " + " ".join(["S"] + ["0"] * 18 + ["123"]))
        (directory / "cmdline").write_bytes(b"\0".join(arg.encode() for arg in argv) + b"\0")
        (directory / "cwd").symlink_to(cwd, target_is_directory=True)

    def test_admission_covers_retained_worktrees_processes_and_bound(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            projects, proc = root / "Projects", root / "proc"
            projects.mkdir()
            proc.mkdir()
            self.process(proc, 123, ["bash", "-lc", "# just stack up"], projects)
            self.assertTrue(m.inspect_admission(projects, proc)["safe"])
            landing = projects / "plans-land-retained"
            landing.mkdir()
            report = m.inspect_admission(projects, proc)
            self.assertFalse(report["safe"])
            self.assertEqual(report["landing_worktrees"], ["plans-land-retained"])
            landing.rmdir()
            (proc / "123" / "cmdline").write_bytes(b"just\0stack\0up\0")
            report = m.inspect_admission(projects, proc)
            self.assertFalse(report["safe"])
            self.assertEqual(report["blocking_processes"][0]["pid"], 123)
            self.assertNotIn("argv", json.dumps(report))
            self.assertFalse(m.inspect_admission(projects, proc, maximum=0)["safe"])

    def test_missing_admission_root_is_uncertain(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            report = m.inspect_admission(root / "absent", root)
            self.assertFalse(report["safe"])
            self.assertEqual(report["errors"][0]["error_class"], "FileNotFoundError")

    def test_raw_health_uses_fixed_endpoint_and_no_auth(self):
        fake = Mock()
        fake.__enter__ = Mock(return_value=fake)
        fake.__exit__ = Mock(return_value=False)
        fake.connect_ex.return_value = errno.EINPROGRESS
        fake.getsockopt.return_value = 0
        sent = []
        fake.send.side_effect = lambda data: sent.append(bytes(data)) or len(data)
        body = b'{"ok":true}'
        fake.recv.return_value = b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
        with patch.object(m.socket, "socket", return_value=fake), patch.object(m.select, "select", side_effect=lambda r, w, x, t: (r, w, [])):
            result = m.probe_live_health()
        self.assertTrue(result["ok"])
        fake.connect_ex.assert_called_once_with(("127.0.0.1", 7200))
        self.assertIn(b"GET /healthz HTTP/1.1", b"".join(sent))
        self.assertNotIn(b"Authorization", b"".join(sent))

    def test_raw_health_deadline_fails_closed_without_other_action(self):
        fake = Mock()
        fake.__enter__ = Mock(return_value=fake)
        fake.__exit__ = Mock(return_value=False)
        fake.connect_ex.return_value = errno.EINPROGRESS
        with patch.object(m.socket, "socket", return_value=fake), patch.object(m.select, "select", return_value=([], [], [])):
            result = m.probe_live_health()
        self.assertFalse(result["ok"])
        self.assertEqual(result["error_class"], "TimeoutError")
        fake.send.assert_not_called()




    def test_token_read_is_noatime_nofollow_and_bounded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            token = root / "token"
            token.write_bytes(b"offline-test-token\n")
            original = m.os.open
            with patch.object(m.os, "open", wraps=original) as opened:
                self.assertEqual(m.read_token_file(token), b"offline-test-token")
            flags = opened.call_args.args[1]
            self.assertTrue(flags & m.os.O_NOATIME)
            self.assertTrue(flags & m.os.O_NOFOLLOW)
            link = root / "link"
            link.symlink_to(token)
            with self.assertRaises(OSError):
                m.read_token_file(link)
            token.write_bytes(b"x" * 4097)
            with self.assertRaises(ValueError):
                m.read_token_file(token)
            token.write_bytes(b"bad\r\nheader")
            with self.assertRaises(ValueError):
                m.read_token_file(token)

    def test_optional_token_is_only_on_wire(self):
        fake = Mock()
        fake.__enter__ = Mock(return_value=fake)
        fake.__exit__ = Mock(return_value=False)
        fake.connect_ex.return_value = errno.EINPROGRESS
        fake.getsockopt.return_value = 0
        sent = []
        fake.send.side_effect = lambda data: sent.append(bytes(data)) or len(data)
        body = b'{"ok":true}'
        fake.recv.return_value = b"HTTP/1.1 200 OK\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
        with patch.object(m, "read_token_file", return_value=b"[REDACTED_SECRET]") as read, patch.object(m.socket, "socket", return_value=fake), patch.object(m.select, "select", side_effect=lambda r, w, x, t: (r, w, [])):
            result = m.probe_live_health(live_token_file="/exact/token")
        read.assert_called_once_with("/exact/token")
        self.assertTrue(result["ok"])
        self.assertIn(b"Authorization: Bearer [REDACTED_SECRET]", b"".join(sent))
        self.assertNotIn("[REDACTED_SECRET]", json.dumps(result))

    def test_only_exact_systemd_user_supervisor_can_have_protected_cwd(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            projects, proc = root / "Projects", root / "proc"
            projects.mkdir()
            proc.mkdir()
            self.process(proc, 1602, ["/usr/lib/systemd/systemd", "--user"], projects)
            (proc / "1602" / "comm").write_text("systemd\n")
            uid = m.os.geteuid()
            (proc / "1602" / "cgroup").write_text(f"0::/user.slice/user-{uid}.slice/user@{uid}.service/init.scope\n")
            with patch.object(m.os, "readlink", side_effect=PermissionError):
                report = m.inspect_admission(projects, proc)
                self.assertTrue(report["safe"])
                self.assertEqual(report["verified_user_supervisors_without_cwd"][0]["pid"], 1602)
                (proc / "1602" / "comm").write_text("python\n")
                self.assertFalse(m.inspect_admission(projects, proc)["safe"])

    def test_monitor_enforces_window_without_parent_check(self):
        callback = Mock()
        guard = self.guard(clock=lambda: 301.0)
        guard.monitor_started = 0
        guard.callback = callback
        guard._run()
        callback.assert_called_once_with()
        self.assertEqual(guard.report()["failure"]["reason"], "loaded-window-exceeded-five-minutes")

    def supervisor_fixture(self, root, child_argv, child_comm, group, parent_argv, parent_comm):
        proc = root / "proc"
        proc.mkdir()
        self.process(proc, 1602, parent_argv, root)
        self.process(proc, 1643, child_argv, root)
        for pid, comm in ((1602, parent_comm), (1643, child_comm)):
            (proc / str(pid) / "comm").write_text(comm + "\n")
            (proc / str(pid) / "cgroup").write_text(group + "\n")
        fields = ["S", "1602"] + ["0"] * 17 + ["456"]
        (proc / "1643" / "stat").write_text("1643 (fake) " + " ".join(fields))
        return proc, proc / "1643", proc / "1602"

    def test_sd_pam_requires_exact_systemd_parent_and_init_scope(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            uid = m.os.geteuid()
            group = f"0::/user.slice/user-{uid}.slice/user@{uid}.service/init.scope"
            proc, child, parent = self.supervisor_fixture(root, ["(sd-pam)"], "(sd-pam)",
                group, ["/usr/lib/systemd/systemd", "--user"], "systemd")
            self.assertEqual(m.verified_user_supervisor(child, ["(sd-pam)"])["classification"],
                             "sd-pam-child-of-exact-user-systemd")
            (parent / "cmdline").write_bytes(b"python3\0")
            self.assertIsNone(m.verified_user_supervisor(child, ["(sd-pam)"]))

    def test_ssh_supervisor_requires_root_parent_and_exact_shared_session(self):
        import pwd
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            uid = m.os.geteuid()
            username = pwd.getpwuid(uid).pw_name
            argv = [f"sshd-session: {username}@notty"]
            group = f"0::/user.slice/user-{uid}.slice/session-678.scope"
            proc, child, parent = self.supervisor_fixture(root, argv, "sshd-session",
                group, [f"sshd-session: {username} [priv]"], "sshd-session")
            original = Path.stat
            def root_parent(path, *args, **kwargs):
                return types.SimpleNamespace(st_uid=0) if path == parent else original(path, *args, **kwargs)
            with patch.object(Path, "stat", autospec=True, side_effect=root_parent):
                self.assertEqual(m.verified_user_supervisor(child, argv)["classification"],
                                 "user-sshd-session-with-root-parent")
                self.assertIsNone(m.verified_user_supervisor(child, argv + ["unexpected"]))
                (parent / "cgroup").write_text(f"0::/user.slice/user-{uid}.slice/session-999.scope\n")
                self.assertIsNone(m.verified_user_supervisor(child, argv))
                (parent / "cgroup").write_text(group + "\n")
            if uid != 0:
                self.assertIsNone(m.verified_user_supervisor(child, argv))


    def test_stop_enforces_strict_window_before_and_after_final_health(self):
        for elapsed, over_limit in ((299.5, False), (300.0, True), (301.0, True)):
            with self.subTest(elapsed=elapsed):
                now = [elapsed]
                guard = self.guard(clock=lambda: now[0])
                guard.monitor_started = 0
                guard.last_health_started = elapsed - .1
                guard.data.update(preflight_passed=True, monitor_started=True)
                guard.thread = Mock()
                guard.thread.is_alive.return_value = False
                guard.callback = Mock()
                result = guard.stop()
                self.assertEqual(result["aborted"], over_limit)
                self.assertEqual(result["passed"], not over_limit)
                self.assertEqual(result["monitor_elapsed_seconds"], elapsed)
                self.assertIn("monitor_finished_utc", result)
                self.assertEqual(guard.callback.call_count, int(over_limit))
        now = [299.99]
        def crosses_deadline():
            now[0] += .02
            return dict(HEALTHY)
        guard = self.guard(clock=lambda: now[0], health_probe=crosses_deadline)
        guard.monitor_started = 0
        guard.last_health_started = 299.5
        guard.data.update(preflight_passed=True, monitor_started=True)
        guard.thread = Mock()
        guard.thread.is_alive.return_value = False
        guard.callback = Mock()
        self.assertTrue(guard.stop()["aborted"])
        guard.callback.assert_called_once_with()

if __name__ == "__main__":
    unittest.main()
