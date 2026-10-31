import unittest
from unittest.mock import patch

from profile_under_build_observed import LoadObserver, overlap_observation


class ObservationTests(unittest.TestCase):
    def test_alive_after_profile_proves_overlap(self):
        self.assertIs(overlap_observation(2, 9, 9.1, 12), True)

    def test_exit_before_profile_end_proves_incomplete(self):
        self.assertIs(overlap_observation(2, 9, 7, 7.1), False)

    def test_exit_observation_after_end_is_not_proof(self):
        self.assertIsNone(overlap_observation(2, 9, 8.9, 9.1))

    def test_unknown_observation_stays_unknown(self):
        self.assertIsNone(overlap_observation(2, 9, None, None))

    def test_alive_timestamp_is_before_poll_exit_after_poll(self):
        class Process:
            def __init__(self):
                self.codes = iter((None, 0))
            def poll(self):
                return next(self.codes)
        observer = LoadObserver(Process())
        with patch("profile_under_build_observed.time.time", side_effect=(10, 11, 12, 13)), \
             patch("profile_under_build_observed.time.monotonic", side_effect=(20, 21, 22, 23)):
            self.assertIsNone(observer.observe())
            self.assertEqual(observer.observe(), 0)
        self.assertEqual((observer.last_alive, observer.first_exit), (10, 13))
        self.assertEqual((observer.last_alive_mono, observer.first_exit_mono), (20, 23))


if __name__ == "__main__":
    unittest.main()
