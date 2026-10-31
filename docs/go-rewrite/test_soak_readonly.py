#!/usr/bin/env python3
"""Offline guards; no profile, listener or server control action is performed."""
import hashlib
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock
import soak_readonly as s


class SoakGuards(unittest.TestCase):
    def test_pid_reuse_refuses_observation(self):
        with mock.patch.object(s, "process_identity", return_value={"pid": 88, "identity": "new", "state": "S"}):
            with self.assertRaisesRegex(s.Failure, "incarnation changed"):
                s.assert_identity({"pid": 88, "process_start_ticks": 123})

    def test_dead_pid_refuses_observation(self):
        with mock.patch.object(s, "process_identity", return_value=None):
            with self.assertRaises(s.Failure): s.assert_identity({"pid": 88, "process_start_ticks": 123})

    def test_live_port_refused_before_any_process_probe(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/"launch.json"
            path.write_text(json.dumps({"pid": 88, "process_start_ticks": 123, "port": 7200,
                "state_root": temp, "token_file": temp+"/token", "projects_file": temp+"/projects"}))
            with mock.patch.object(s, "process_identity") as probed, mock.patch.object(s.subprocess, "Popen") as launched:
                with self.assertRaisesRegex(s.Failure, "private candidate"):
                    s.bind(path, "/missing", "0"*64, "codexpro")
                probed.assert_not_called(); launched.assert_not_called()

    def test_config_home_is_independent_of_outer_private_state(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); home=root/"home"; home.mkdir()
            token=root/"http-token"; token.write_text("test-only")
            catalog=root/"projects.json"; catalog.write_text("{}")
            state, observed, _, _ = s.launch_paths({"state_root":str(root),"config_home":str(home),
                "token_file":str(token),"projects_file":str(catalog)})
            self.assertEqual(state,root); self.assertEqual(observed,home)

    def test_config_home_outside_private_state_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); state=root/"state"; state.mkdir(); other=root/"other"; other.mkdir()
            token=state/"token"; token.write_text("test-only")
            catalog=state/"projects"; catalog.write_text("{}")
            with self.assertRaises(s.Failure):
                s.launch_paths({"state_root":str(state),"config_home":str(other),"token_file":str(token),"projects_file":str(catalog)})

    def test_partial_or_duplicate_profile_never_counts_as_160(self):
        report={"requested_calls":160,"measurements":[{"index":i,"ok":True} for i in range(160)],"initialization_errors":[]}
        self.assertTrue(s.profile_summary(report)["complete"])
        report["measurements"][159]["index"]=158
        self.assertFalse(s.profile_summary(report)["complete"])
        report["measurements"].pop()
        self.assertFalse(s.profile_summary(report)["complete"])

    def test_profile_error_is_retained(self):
        report={"requested_calls":160,"measurements":[{"index":i,"ok":i!=2} for i in range(160)],"initialization_errors":[{"error":"failure"}]}
        summary=s.profile_summary(report)
        self.assertEqual(summary["failures"],1);self.assertEqual(summary["initialization_errors"],1)

    def test_growth_observation_does_not_round_away_positive_growth(self):
        samples=[{"phase":"idle","elapsed_seconds":t,"rss_kb":100+n} for n,t in enumerate((0,300,600))]
        value=s.growth_summary(samples,[{"rss_after_kb":n} for n in (101,102,103)])
        self.assertTrue(value["post_profile_strictly_increasing"])
        self.assertEqual(value["post_profile_rss_deltas_kb"],[1,1])
        self.assertGreater(value["idle_slope_kb_per_minute"],0)
        self.assertNotIn("stable", value)

    def test_unknown_profiler_launch_has_no_signal_authority(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp); soak=types.SimpleNamespace(control=root,binding={"pid":321,"process_start_ticks":123,"boot_id":"boot"},profile_sha="a"*64)
            child=s.Child(soak,0,root/"out.json");child.receipt["phase"]="launch_outcome_unknown"
            with mock.patch.object(s.os,"killpg") as signalled:
                with self.assertRaisesRegex(s.Failure,"no signal authority"): child.stop()
                signalled.assert_not_called()


if __name__=="__main__":unittest.main()
