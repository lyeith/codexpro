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


if __name__ == "__main__":
    unittest.main()
