import unittest

import performance_compare_observed as subject
from test_performance_compare import fixture


class ObservedComparisonTests(unittest.TestCase):
    def setUp(self):
        self.profile = subject.validate_profile(fixture(), port=7211)
        start, end = self.profile["started_epoch"], self.profile["ended_epoch"]
        self.receipt = {
            "load_command": ["nice", "-n", "10", "timeout", "90s", "go", "build", "-p", "2", "-a", "-o", "/tmp/test/compiler", "cmd/compile"],
            "load_started_epoch": start - 2, "elapsed_seconds": 125,
            "load_exit_code": 0, "bounded_timeout_expected": False, "profile_exit_code": 0,
            "observation_version": 1, "load_finish_is_observed_upper_bound": True,
            "load_environment": {"GOMAXPROCS": "2", "fresh_cache": True},
            "observer_interval_seconds": .05, "observer_count": 3,
            "profile_started_epoch": start, "profile_finished_epoch": end,
            "load_started_monotonic": 0, "profile_started_monotonic": 2,
            "profile_finished_monotonic": 122, "load_last_alive_monotonic": 122.1,
            "load_finished_monotonic": 124, "load_last_alive_epoch": end + .1,
            "load_finished_epoch": end + 2, "continuous_overlap_verified": True,
        }

    def test_independent_alive_witness(self):
        self.assertTrue(subject.validate_load(self.receipt, self.profile)["continuous_overlap_verified"])

    def test_late_exit_observation_without_alive_witness_stays_unknown(self):
        self.receipt.update(load_last_alive_monotonic=121.9, continuous_overlap_verified=None)
        self.assertIsNone(subject.validate_load(self.receipt, self.profile)["continuous_overlap_verified"])

    def test_early_exit_is_incomplete(self):
        self.receipt.update(load_last_alive_monotonic=110, load_finished_monotonic=111,
                            continuous_overlap_verified=False)
        self.assertFalse(subject.validate_load(self.receipt, self.profile)["continuous_overlap_verified"])

    def test_false_overlap_claim_is_rejected(self):
        self.receipt["load_last_alive_monotonic"] = 121.9
        with self.assertRaisesRegex(subject.EvidenceError, "classification differs"):
            subject.validate_load(self.receipt, self.profile)

    def test_profile_interval_must_enclose_raw_receipt(self):
        self.receipt["profile_finished_epoch"] = self.profile["ended_epoch"] - 1
        with self.assertRaisesRegex(subject.EvidenceError, "does not enclose"):
            subject.validate_load(self.receipt, self.profile)

    def test_legacy_finish_timestamp_cannot_prove_overlap(self):
        for key in list(self.receipt):
            if key not in {"load_command", "load_started_epoch", "elapsed_seconds", "load_exit_code", "bounded_timeout_expected", "profile_exit_code", "load_finished_epoch"}:
                del self.receipt[key]
        self.assertIsNone(subject.validate_load(self.receipt, self.profile)["continuous_overlap_verified"])


if __name__ == "__main__":
    unittest.main()
