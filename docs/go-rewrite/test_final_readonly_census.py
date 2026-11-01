#!/usr/bin/env python3
"""Offline regression for proof identities versus unrelated safety observations."""
import unittest

import final_readonly_census as census


class PrivateIdentityBoundaryTests(unittest.TestCase):
    def test_guard_observations_are_not_private_process_witnesses(self):
        receipt = {
            "load_samples": [{"workers": [
                {"pid": 41001, "start_ticks": 101, "cpu_ticks": 7},
            ]}],
            "compiler_identity": {"processes": [
                {"pid": 41002, "process_start_ticks": 102,
                 "process_group": 41002, "session_id": 41002},
            ]},
            "startup_receipt": {"unchanged_receipt": {"cleanup": [
                {"pid": 41003, "process_start_ticks": 103},
            ]}},
            "safety_guard": {"samples": [
                {"admission": {
                    "verified_user_supervisors_without_cwd": [
                        {"pid": 1602, "start_ticks": 621},
                        {"pid": 1643, "start_ticks": 626},
                        {"pid": 1559615, "start_ticks": 10269123},
                    ],
                    "blocking_processes": [
                        {"pid": 41004, "start_ticks": 104},
                    ],
                }},
            ]},
        }
        actual = sorted(census.receipt_identities(receipt), key=lambda row: row["pid"])
        self.assertEqual(actual, [
            {"pid": 41001, "start_ticks": 101},
            {"pid": 41002, "start_ticks": 102,
             "process_group": 41002, "session_id": 41002},
            {"pid": 41003, "start_ticks": 103},
        ])

    def test_standalone_preflight_supervisors_are_not_private_witnesses(self):
        admission = {
            "safe": True,
            "landing_worktrees": [],
            "blocking_processes": [],
            "verified_user_supervisors_without_cwd": [
                {"pid": 1602, "start_ticks": 621,
                 "classification": "exact-systemd-user-init-scope"},
                {"pid": 1643, "start_ticks": 626,
                 "classification": "sd-pam-child-of-exact-user-systemd"},
                {"pid": 1559615, "start_ticks": 10269123,
                 "classification": "user-sshd-session-with-root-parent"},
                {"pid": 1606715, "start_ticks": 29710833,
                 "classification": "user-sshd-session-with-root-parent"},
            ],
        }
        # The standalone preflight has these inventories at the top level,
        # outside safety_guard. Retain all three real private-proof branches.
        receipt = {
            "admission_before": admission,
            "admission_after": admission,
            "admission": admission,
            "load_samples": [{"workers": [
                {"pid": 42001, "start_ticks": 201},
            ]}],
            "compiler_identity": {"processes": [
                {"pid": 42002, "process_start_ticks": 202},
            ]},
            "startup_receipt": {"unchanged_receipt": {"cleanup": [
                {"pid": 42003, "process_start_ticks": 203},
            ]}},
        }
        self.assertEqual(
            sorted(census.receipt_identities(receipt), key=lambda row: row["pid"]),
            [
                {"pid": 42001, "start_ticks": 201},
                {"pid": 42002, "start_ticks": 202},
                {"pid": 42003, "start_ticks": 203},
            ],
        )


if __name__ == "__main__":
    unittest.main()
