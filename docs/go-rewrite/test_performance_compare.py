import datetime as dt
import unittest

import performance_compare as subject


def fixture(port=7211, latency=800.0, cpu_ticks=3200, writes=1000000, peak_kib=300000):
    start = dt.datetime(2026, 10, 31, tzinfo=dt.timezone.utc)
    requests = subject.request_mix("ws_profile")
    rows = [{"index": i, "tool": requests[i % 8]["tool"],
             "started_at": (start + dt.timedelta(seconds=i / 10)).isoformat(),
             "ok": True, "duration_ms": latency} for i in range(160)]
    samples = [{"at": (start + dt.timedelta(seconds=offset)).isoformat(), "mono": 100.0 + offset,
                "cpu_ticks": 1000 + ticks, "rss_kb": rss, "threads": 8, "read_bytes": 0,
                "write_bytes": 100 + written, "nice": 10, "cgroup": "0::/private/test.scope"}
               for offset, ticks, rss, written in (
                   (0, 0, 60000, 0), (60, cpu_ticks // 2, peak_kib, writes // 2),
                   (120, cpu_ticks, min(peak_kib, 100000), writes))]
    by_tool = {}
    for name in {request["tool"] for request in requests}:
        selected = [row for row in rows if row["tool"] == name]
        by_tool[name] = {**subject.quantiles([row["duration_ms"] for row in selected]), "failed": 0}
    return {"url": "http://127.0.0.1:" + str(port) + "/mcp", "requested_calls": 160,
            "concurrency": 2, "delay_ms": 50, "duration_limit_seconds": 180,
            "started_at": start.isoformat(), "ended_at": (start + dt.timedelta(seconds=120)).isoformat(),
            "elapsed_seconds": 120.0, "rps": 160 / 120, "requests": requests, "measurements": rows,
            "aggregate": subject.quantiles([row["duration_ms"] for row in rows]), "failures": 0,
            "by_tool": by_tool, "initialization_errors": [], "process_samples": samples,
            "resources": {"pid": 1000 + port, "rss_start_kb": 60000, "rss_end_kb": min(peak_kib, 100000),
                          "rss_peak_kb": peak_kib, "cpu_seconds": cpu_ticks / 100,
                          "read_bytes_delta": 0, "write_bytes_delta": writes, "nice": 10,
                          "cgroup": samples[0]["cgroup"]}}


class PerformanceEvidenceTests(unittest.TestCase):
    def pair(self, candidate=None):
        reference = subject.validate_profile(fixture(), port=7211)
        candidate = candidate or fixture(7210, 100, 800, 50000, 80000)
        return subject.pair_metrics(reference, subject.validate_profile(candidate, port=7210, clock_ticks=100))

    def test_exact_ratios_and_binary_memory_units(self):
        result = self.pair()
        self.assertTrue(result["numeric_gates_passed"])
        self.assertEqual(result["p95_speedup"], 8)
        self.assertEqual(result["server_cpu_fraction"], .25)
        self.assertEqual(result["server_cpu_reduction_percent"], 75)
        self.assertEqual(result["written_bytes_reduction_percent"], 95)
        self.assertEqual(result["candidate_peak_rss_mib"], 78.125)
        self.assertTrue(result["cpu_stretch_at_most_quarter"])

    def test_rounded_display_cannot_turn_latency_failure_into_pass(self):
        candidate = fixture(7210, 100.0004, 800, 50000, 80000)
        self.assertEqual(candidate["aggregate"]["p95_ms"], 100.0)
        result = self.pair(candidate)
        self.assertFalse(result["numeric_gates"]["p95_absolute"])

    def test_written_bytes_threshold_uses_integer_arithmetic(self):
        result = self.pair(fixture(7210, 100, 800, 100001, 80000))
        self.assertFalse(result["numeric_gates"]["written_bytes_reduced_at_least_90_percent"])
        self.assertTrue(self.pair(fixture(7210, 100, 800, 100000, 80000))["numeric_gates"]["written_bytes_reduced_at_least_90_percent"])

    def test_cpu_boundary_uses_sampled_ticks(self):
        self.assertTrue(self.pair(fixture(7210, 100, 1600, 50000, 80000))["numeric_gates"]["server_cpu_at_most_half"])
        self.assertFalse(self.pair(fixture(7210, 100, 1601, 50000, 80000))["numeric_gates"]["server_cpu_at_most_half"])

    def test_tampered_aggregate_is_rejected(self):
        value = fixture()
        value["aggregate"]["p95_ms"] -= 1
        with self.assertRaisesRegex(subject.EvidenceError, "aggregate"):
            subject.validate_profile(value, port=7211)

    def test_resource_summary_and_counter_reset_are_rejected(self):
        value = fixture()
        value["resources"]["write_bytes_delta"] -= 1
        with self.assertRaisesRegex(subject.EvidenceError, "resource summary"):
            subject.validate_profile(value, port=7211)
        value = fixture()
        value["process_samples"][1]["cpu_ticks"] = 1
        with self.assertRaisesRegex(subject.EvidenceError, "counters decreased"):
            subject.validate_profile(value, port=7211)

    def test_workspace_identity_is_not_silently_normalized(self):
        reference = subject.validate_profile(fixture(), port=7211)
        value = fixture(7210, 100, 800, 50000, 80000)
        value["requests"] = subject.request_mix("ws_different")
        candidate = subject.validate_profile(value, port=7210, clock_ticks=100)
        with self.assertRaisesRegex(subject.EvidenceError, "identities differ"):
            subject.pair_metrics(reference, candidate)

    def test_candidate_failure_is_not_a_fast_success(self):
        value = fixture(7210, 100, 800, 50000, 80000)
        value["measurements"][0]["ok"] = False
        value["failures"] = 1
        value["by_tool"]["list_projects"]["failed"] = 1
        result = self.pair(value)
        self.assertFalse(result["numeric_gates_passed"])
        self.assertFalse(result["numeric_gates"]["p95_absolute"])
        self.assertIsNone(result["p95_speedup"])

    def test_nice_mismatch_cannot_pass_resource_gate(self):
        value = fixture(7210, 100, 800, 50000, 80000)
        value["process_samples"][1]["nice"] = 0
        result = self.pair(value)
        self.assertFalse(result["numeric_gates"]["consistent_sampled_process_nice_10"])

    def test_load_wrapper_is_not_continuous_overlap_proof(self):
        profile = subject.validate_profile(fixture(), port=7211)
        load = {"load_command": ["nice", "-n", "10", "timeout", "90s", "go", "build", "-p", "2", "-a", "-o", "/tmp/example/compiler", "cmd/compile"],
                "load_started_epoch": profile["started_epoch"] - 2, "elapsed_seconds": 125,
                "load_exit_code": 0, "bounded_timeout_expected": False, "profile_exit_code": 0}
        result = subject.validate_load(load, profile)
        self.assertTrue(result["bounded_build_launch_succeeded"])
        self.assertIsNone(result["continuous_overlap_verified"])
        load["load_finished_epoch"] = profile["ended_epoch"] - 1
        self.assertFalse(subject.validate_load(load, profile)["continuous_overlap_verified"])

    def test_duplicate_missing_and_wrong_request_identity(self):
        for mode in ("duplicate", "wrong-tool"):
            value = fixture()
            if mode == "duplicate":
                value["measurements"][1]["index"] = 0
                value["measurements"][1]["tool"] = "list_projects"
            else:
                value["measurements"][2]["tool"] = "activity_status"
            with self.assertRaises(subject.EvidenceError):
                subject.validate_profile(value, port=7211)
        value = fixture(7210, 100, 800, 50000, 80000)
        value["measurements"].pop()
        value["aggregate"] = subject.quantiles([row["duration_ms"] for row in value["measurements"]])
        value["by_tool"]["activity_status"] = {**subject.quantiles([100.0] * 19), "failed": 0}
        value["rps"] = 159 / 120
        result = self.pair(value)
        self.assertFalse(result["numeric_gates"]["complete_160_calls_zero_errors"])

    def test_source_nearest_rank_definition(self):
        values = list(range(1, 161))
        measured = subject.quantiles(values, rounded=False)
        self.assertEqual((measured["p50_ms"], measured["p95_ms"], measured["p99_ms"]), (80, 152, 159))


if __name__ == "__main__":
    unittest.main()
