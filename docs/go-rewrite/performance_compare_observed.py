#!/usr/bin/env python3
"""Validate profile.py receipts and calculate the unchanged numeric design gates.

This reads files only. It does not launch services, access credentials or infer
fixture identity, audit saturation, idle memory, soak stability or scope weight.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
from pathlib import Path
import statistics
import sys
from urllib.parse import urlsplit


class EvidenceError(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise EvidenceError(message)


def number(value, name, *, integer=False, minimum=0):
    require(isinstance(value, (int, float)) and not isinstance(value, bool)
            and math.isfinite(value) and value >= minimum, name + " is not a valid finite number")
    require(not integer or isinstance(value, int), name + " must be an integer")
    return value


def timestamp(value):
    require(isinstance(value, str), "timestamp is not a string")
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    require(parsed.tzinfo is not None, "timestamp lacks timezone")
    return parsed.timestamp()


def read_receipt(path):
    path = Path(path)
    before = path.stat()
    require(path.is_file() and before.st_size <= 16 * 1024 * 1024, "receipt exceeds the regular-file size bound")
    data = path.read_bytes()
    after = path.stat()
    require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns)
            == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), "receipt changed while reading")
    value = json.loads(data, parse_constant=lambda _: (_ for _ in ()).throw(EvidenceError("nonfinite JSON constant")))
    require(isinstance(value, dict), "receipt is not an object")
    return value, {"file": path.name, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def request_mix(workspace):
    return [
        {"tool": "list_projects", "arguments": {}},
        {"tool": "server_config", "arguments": {}},
        {"tool": "read", "arguments": {"workspace_id": workspace, "path": "package.json", "max_bytes": 8000}},
        {"tool": "read", "arguments": {"workspace_id": workspace, "path": "src/server.ts", "start_line": 1, "end_line": 80, "max_bytes": 10000}},
        {"tool": "tree", "arguments": {"workspace_id": workspace, "path": "src/tools", "max_depth": 2, "max_entries": 100}},
        {"tool": "search", "arguments": {"workspace_id": workspace, "path": "src/worktrees/manager.ts", "query": "orphaned", "intent": "text", "max_results": 20}},
        {"tool": "show_changes", "arguments": {"workspace_id": workspace, "include_diff": False, "mark_reviewed": False}},
        {"tool": "activity_status", "arguments": {}},
    ]


def quantiles(values, rounded=True):
    values = sorted(values)
    if not values:
        return {"n": 0}
    def q(p):
        return values[max(0, math.ceil(p * len(values)) - 1)]
    out = {"n": len(values), "p50_ms": q(.5), "p95_ms": q(.95), "p99_ms": q(.99),
           "max_ms": q(1), "mean_ms": statistics.mean(values)}
    return {key: round(value, 3) if rounded and key != "n" else value for key, value in out.items()}


def validate_profile(value, *, port, clock_ticks=None):
    endpoint = urlsplit(value["url"])
    require(endpoint.scheme == "http" and endpoint.hostname in ("127.0.0.1", "localhost", "::1")
            and endpoint.port == port and endpoint.path == "/mcp" and not endpoint.query
            and not endpoint.fragment and endpoint.username is None and endpoint.password is None,
            "receipt is not the explicit private MCP endpoint")
    require(value["requested_calls"] == 160 and value["concurrency"] == 2 and value["delay_ms"] == 50,
            "workload must retain 160 calls, concurrency two and 50 ms delay")
    number(value["duration_limit_seconds"], "duration limit", integer=True, minimum=1)
    require(value["duration_limit_seconds"] <= 600, "unbounded profile duration")
    elapsed = number(value["elapsed_seconds"], "elapsed seconds", minimum=1e-9)
    started, ended = timestamp(value["started_at"]), timestamp(value["ended_at"])
    require(ended >= started, "profile end precedes start")
    requests = value["requests"]
    require(isinstance(requests, list) and len(requests) == 8, "wrong request cycle")
    workspace = requests[2]["arguments"]["workspace_id"]
    require(isinstance(workspace, str) and workspace and requests == request_mix(workspace), "request arguments differ from profile.py")
    rows = value["measurements"]
    require(isinstance(rows, list) and len(rows) <= 160, "invalid measured call count")
    indices = []
    for row in rows:
        index = number(row["index"], "request index", integer=True)
        require(index < 160 and row["tool"] == requests[index % 8]["tool"], "measurement has wrong request identity")
        require(type(row["ok"]) is bool, "measurement status is not a boolean")
        number(row["duration_ms"], "latency", minimum=1e-12)
        require(started <= timestamp(row["started_at"]) <= ended, "measurement timestamp escapes run")
        indices.append(index)
    require(indices == sorted(set(indices)), "measurements are duplicate or unordered")
    require(value["aggregate"] == quantiles([row["duration_ms"] for row in rows]), "aggregate does not match raw measurements")
    failures = sum(not row["ok"] for row in rows)
    require(value["failures"] == failures, "failure count does not match measurements")
    expected_by_tool = {}
    for name in {request["tool"] for request in requests}:
        selected = [row for row in rows if row["tool"] == name]
        expected_by_tool[name] = {**quantiles([row["duration_ms"] for row in selected]), "failed": sum(not row["ok"] for row in selected)}
    require(value["by_tool"] == expected_by_tool, "per-tool statistics do not match raw measurements")
    require(math.isclose(value["rps"], len(rows) / elapsed, rel_tol=1e-12, abs_tol=1e-12), "throughput does not match elapsed time")
    require(isinstance(value["initialization_errors"], list), "missing initialization error ledger")
    samples = value["process_samples"]
    require(isinstance(samples, list) and 2 <= len(samples) <= 2000, "invalid process sample count")
    good = [sample for sample in samples if "cpu_ticks" in sample]
    require(len(good) >= 2, "insufficient available process samples")
    for sample in good:
        for key in ("cpu_ticks", "rss_kb", "threads", "read_bytes", "write_bytes"):
            number(sample[key], key, integer=True)
        number(sample["mono"], "process monotonic time")
        number(sample["nice"], "nice", integer=True)
    for previous, current in zip(good, good[1:]):
        require(current["mono"] > previous["mono"], "process sample clock did not advance")
        require(all(current[key] >= previous[key] for key in ("cpu_ticks", "read_bytes", "write_bytes")),
                "process counters decreased; possible identity change")
    first, last = good[0], good[-1]
    resources = value["resources"]
    number(resources["pid"], "server PID", integer=True, minimum=1)
    ticks = last["cpu_ticks"] - first["cpu_ticks"]
    seconds = number(resources["cpu_seconds"], "CPU seconds")
    if clock_ticks is None:
        require(ticks > 0 and seconds > 0, "reference CPU clock cannot be inferred from zero counters")
        clock_ticks = round(ticks / seconds)
        require(1 <= clock_ticks <= 1000000 and math.isclose(ticks / seconds, clock_ticks, rel_tol=1e-10), "invalid inferred CPU clock")
    require(math.isclose(seconds, ticks / clock_ticks, rel_tol=1e-12, abs_tol=1e-12), "CPU summary differs from sampled ticks")
    derived = {"rss_start_kb": first["rss_kb"], "rss_end_kb": last["rss_kb"],
               "rss_peak_kb": max(sample["rss_kb"] for sample in good),
               "read_bytes_delta": last["read_bytes"] - first["read_bytes"],
               "write_bytes_delta": last["write_bytes"] - first["write_bytes"],
               "nice": first["nice"], "cgroup": first["cgroup"]}
    require(all(resources[key] == item for key, item in derived.items()), "resource summary differs from process samples")
    return {
        "raw_quantiles": quantiles([row["duration_ms"] for row in rows], rounded=False),
        "display_quantiles": value["aggregate"], "by_tool": expected_by_tool,
        "failures": failures, "initialization_errors": len(value["initialization_errors"]),
        "complete_160_calls": indices == list(range(160)), "samples_complete": len(good) == len(samples),
        "nice_10_throughout": all(sample["nice"] == 10 for sample in good),
        "stable_cgroup": all(sample["cgroup"] == first["cgroup"] for sample in good),
        "resources": resources, "cpu_ticks_delta": ticks, "clock_ticks_per_second": clock_ticks,
        "elapsed_seconds": elapsed, "workspace": workspace, "requests": requests,
        "duration_limit_seconds": value["duration_limit_seconds"], "started_epoch": started, "ended_epoch": ended,
    }


def validate_load(value, profile):
    command = value["load_command"]
    require(isinstance(command, list) and len(command) == 13
            and command[:11] == ["nice", "-n", "10", "timeout", "90s", "go", "build", "-p", "2", "-a", "-o"]
            and isinstance(command[11], str) and command[-1] == "cmd/compile", "unexpected load command")
    start = number(value["load_started_epoch"], "load start", minimum=1)
    elapsed = number(value["elapsed_seconds"], "load wrapper duration", minimum=1e-9)
    require(start <= profile["started_epoch"] and start + elapsed + .02 >= profile["ended_epoch"], "load wrapper does not enclose profile")
    require(type(value["bounded_timeout_expected"]) is bool and value["bounded_timeout_expected"] == (value["load_exit_code"] == 124), "load timeout classification differs")
    launched = value["load_exit_code"] in (0, 124) and value["profile_exit_code"] == 0
    overlap = None
    method = "Legacy wrapper elapsed time does not record the actual compiler exit timestamp."
    if "observation_version" in value:
        require(value["observation_version"] == 1 and value["load_finish_is_observed_upper_bound"] is True,
                "unsupported load observation schema")
        require(value["load_environment"]["GOMAXPROCS"] == "2" and value["load_environment"]["fresh_cache"] is True,
                "observed load changed worker count or cache freshness")
        number(value["observer_interval_seconds"], "observer interval", minimum=1e-9)
        number(value["observer_count"], "observer count", integer=True, minimum=1)
        profile_start = number(value["profile_started_epoch"], "profile subprocess start", minimum=start)
        profile_finish = number(value["profile_finished_epoch"], "profile subprocess finish", minimum=profile_start)
        require(profile_start <= profile["started_epoch"] and profile_finish >= profile["ended_epoch"],
                "observed profile subprocess interval does not enclose the receipt")
        mono_start = number(value["load_started_monotonic"], "monotonic load start")
        mono_profile_start = number(value["profile_started_monotonic"], "monotonic profile start", minimum=mono_start)
        mono_profile_end = number(value["profile_finished_monotonic"], "monotonic profile finish", minimum=mono_profile_start)
        mono_alive = number(value["load_last_alive_monotonic"], "last observed alive", minimum=mono_start)
        mono_exit = number(value["load_finished_monotonic"], "first observed exit", minimum=mono_alive)
        number(value["load_last_alive_epoch"], "last alive epoch", minimum=start)
        number(value["load_finished_epoch"], "observed completion epoch", minimum=start)
        overlap = True if mono_alive >= mono_profile_end else False if mono_exit < mono_profile_end else None
        claimed = value["continuous_overlap_verified"]
        require(claimed is None or type(claimed) is bool, "invalid overlap classification")
        require(claimed is overlap, "overlap classification differs from monotonic observations")
        method = "Process alive lower bound and observed exit upper bound around the complete profile subprocess."
    elif "load_finished_epoch" in value:
        # A legacy first-observed finish can disprove overlap but cannot prove
        # that the compiler was still running at the profile's final request.
        finish = number(value["load_finished_epoch"], "observed load finish", minimum=start)
        if finish < profile["ended_epoch"]:
            overlap = False
    return {"bounded_build_launch_succeeded": launched, "load_exit_code": value["load_exit_code"],
            "continuous_overlap_verified": overlap, "observation_method": method,
            "measurement_limit": method if overlap is None else None}


def pair_metrics(reference, candidate, *, under_build=False, load_valid=True):
    require(reference["requests"] == candidate["requests"], "reference/candidate request identities differ; no implicit workspace normalization")
    require(reference["duration_limit_seconds"] == candidate["duration_limit_seconds"], "reference/candidate duration bounds differ")
    require(reference["complete_160_calls"] and reference["failures"] == 0 and reference["initialization_errors"] == 0,
            "reference is not a complete zero-error baseline")
    require(reference["samples_complete"] and reference["nice_10_throughout"] and reference["stable_cgroup"],
            "reference process evidence is incomplete or has inconsistent priority/identity")
    calls_valid = candidate["complete_160_calls"] and candidate["failures"] == 0 and candidate["initialization_errors"] == 0
    resources_valid = candidate["samples_complete"] and candidate["nice_10_throughout"] and candidate["stable_cgroup"]
    qref, qgo = reference["raw_quantiles"], candidate["raw_quantiles"]
    rr, gr = reference["resources"], candidate["resources"]
    require(rr["write_bytes_delta"] > 0 and reference["cpu_ticks_delta"] > 0, "reference relative metric has a zero denominator")
    p95, p99 = qgo.get("p95_ms"), qgo.get("p99_ms")
    gates = {
        "complete_160_calls_zero_errors": calls_valid,
        "consistent_sampled_process_nice_10": resources_valid,
        "p95_absolute": calls_valid and p95 is not None and p95 <= (250 if under_build else 100),
        "p99_absolute": calls_valid and p99 is not None and p99 <= (500 if under_build else 250),
        "p95_at_least_twice_as_fast": calls_valid and p95 is not None and p95 * 2 <= qref["p95_ms"],
        "server_cpu_at_most_half": calls_valid and resources_valid and candidate["cpu_ticks_delta"] * 2 <= reference["cpu_ticks_delta"],
        "sampled_peak_rss_at_most_256_mib": calls_valid and resources_valid and gr["rss_peak_kb"] <= 256 * 1024,
        "written_bytes_reduced_at_least_90_percent": calls_valid and resources_valid and gr["write_bytes_delta"] * 10 <= rr["write_bytes_delta"],
    }
    if under_build:
        gates["bounded_build_launch_without_known_overlap_failure"] = load_valid
    return {
        "reference": {"latency_ms": reference["display_quantiles"], **rr},
        "candidate": {"latency_ms": candidate["display_quantiles"], **gr, "failures": candidate["failures"],
                      "initialization_errors": candidate["initialization_errors"], "complete_160_calls": candidate["complete_160_calls"]},
        "p95_speedup": qref["p95_ms"] / p95 if p95 and calls_valid else None,
        "server_cpu_fraction": candidate["cpu_ticks_delta"] / reference["cpu_ticks_delta"],
        "server_cpu_reduction_percent": 100 * (1 - candidate["cpu_ticks_delta"] / reference["cpu_ticks_delta"]),
        "written_bytes_reduction_percent": 100 * (1 - gr["write_bytes_delta"] / rr["write_bytes_delta"]),
        "candidate_peak_rss_mib": gr["rss_peak_kb"] / 1024,
        "candidate_initial_rss_mib_observation_only": gr["rss_start_kb"] / 1024,
        "candidate_rss_change_kib": gr["rss_end_kb"] - gr["rss_start_kb"],
        "cpu_stretch_at_most_quarter": calls_valid and resources_valid and candidate["cpu_ticks_delta"] * 4 <= reference["cpu_ticks_delta"],
        "numeric_gates": gates, "numeric_gates_passed": all(gates.values()),
        "by_tool": {name: {"reference": reference["by_tool"][name], "candidate": candidate["by_tool"][name]} for name in sorted(reference["by_tool"])},
    }


def compare_files(paths):
    values, receipts = {}, {}
    for role, path in paths.items():
        values[role], receipts[role] = read_receipt(path)
    ts = validate_profile(values["ordinary_ts"], port=7211)
    hz = ts["clock_ticks_per_second"]
    go = validate_profile(values["ordinary_go"], port=7210, clock_ticks=hz)
    ts_load = validate_profile(values["build_ts"], port=7211, clock_ticks=hz)
    go_load = validate_profile(values["build_go"], port=7210, clock_ticks=hz)
    require(ts["requests"] == ts_load["requests"] and go["requests"] == go_load["requests"], "ordinary and build profiles use different workspaces/request cycles")
    reference_load = validate_load(values["build_ts_load"], ts_load)
    candidate_load = validate_load(values["build_go_load"], go_load)
    ordinary = pair_metrics(ts, go)
    loaded = pair_metrics(ts_load, go_load, under_build=True,
                          load_valid=reference_load["bounded_build_launch_succeeded"] and candidate_load["bounded_build_launch_succeeded"]
                          and reference_load["continuous_overlap_verified"] is not False
                          and candidate_load["continuous_overlap_verified"] is not False)
    return {
        "schema_version": 2, "lane": "validated-matched-performance-comparison", "inputs": receipts,
        "clock_ticks_per_second_inferred_and_cross_checked": hz, "ordinary": ordinary, "under_build": loaded,
        "load_evidence": {"reference": reference_load, "candidate": candidate_load},
        "all_computed_numeric_gates_passed": ordinary["numeric_gates_passed"] and loaded["numeric_gates_passed"],
        "method": "Nearest-rank percentiles from unrounded raw samples; source display summaries independently recomputed. CPU and IO from validated process-counter deltas. Integer arithmetic for CPU/write ratio gates. Linux /proc kB interpreted as KiB.",
        "not_proven_by_these_receipts": [
            "Fixture/catalog/Git identity and audit saturation/retention equivalence require separate launch/state evidence.",
            "PID start identity, filesystem type, compiler version and actual scope CPUWeight require separate evidence.",
            "RSS is maximum sampled VmRSS every approximately0.5s, not a guaranteed between-sample or lifetime high-water mark.",
            "Initial RSS is an observation, not an idle-memory acceptance result; sustained growth needs the separate repetition/soak record.",
            "The legacy build wrapper proves bounded launch but not continuous compiler overlap without an actual finish timestamp.",
            "These numeric gates do not prove correctness, ownership, isolation, crash durability or authorize cutover.",
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("ordinary-ts", "ordinary-go", "build-ts", "build-go", "build-ts-load", "build-go-load"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    report = compare_files({name: getattr(args, name) for name in (
        "ordinary_ts", "ordinary_go", "build_ts", "build_go", "build_ts_load", "build_go_load")})
    with args.out.open("x", encoding="utf-8") as stream:
        json.dump(report, stream, ensure_ascii=False, indent=2, allow_nan=False)
        stream.write("\n")
    print(json.dumps({"all_computed_numeric_gates_passed": report["all_computed_numeric_gates_passed"],
                      "output": str(args.out), "ordinary_gates": report["ordinary"]["numeric_gates"],
                      "build_gates": report["under_build"]["numeric_gates"]}, sort_keys=True))
    return 0 if report["all_computed_numeric_gates_passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (EvidenceError, KeyError, TypeError, ValueError, OverflowError, OSError) as error:
        print("performance comparison refused: " + str(error), file=sys.stderr)
        raise SystemExit(2)
