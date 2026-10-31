#!/usr/bin/env python3
"""Run the four unchanged default contract CLIs against one explicit private pair."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

from paired_contract_suite import artifact_path, iso_now, private_path, require, validate_launch
from telemetry_approved_deltas import load_approval

LANES = (("paired", "paired_contract_suite.py", 120),
         ("process", "process_contract_suite.py", 180),
         ("telemetry", "telemetry_contract_suite.py", 120),
         ("admin", "admin_catalog_contract_suite.py", 120))
HERE = Path(__file__).resolve().parent
COUNT_KEYS = {
    "paired": {"planned_workflows", "planned_steps", "passed_workflows", "passed_steps", "failed_steps", "blocked_steps", "approved_deltas"},
    "telemetry": {"passed_unchanged", "passed_with_approved_delta", "failed"},
    "admin": {"pass", "approved_delta", "fail", "blocked", "pending"},
    "process": {"scenarios", "passed_scenarios", "failed_scenarios", "compared_calls_in_passed_scenarios"},
}


def read_json(path, limit):
    with Path(path).open("rb") as source:
        raw = source.read(limit + 1)
    require(len(raw) <= limit, "JSON input exceeds its byte bound")
    value = json.loads(raw)
    require(isinstance(value, dict), "JSON input must be an object")
    return value, hashlib.sha256(raw).hexdigest()


def prepare(args):
    sides = {}
    for label, port in (("reference", 7211), ("candidate", 7210)):
        path = private_path(getattr(args, label + "_manifest"))
        manifest, digest = read_json(path, 65536)
        endpoint = f"http://127.0.0.1:{port}/mcp"
        root, state, token = validate_launch(manifest, endpoint, manifest["token_file"], manifest["fixture_root"], port)
        require(0 < token.stat().st_size <= 4096, "private token file is empty or oversized")
        sides[label] = dict(path=path, manifest=manifest, digest=digest, endpoint=endpoint,
                            root=root, state=state, token=token, credential=token.read_bytes().strip())
    left, right = sides.values()
    require(left["manifest"]["project_id"] == right["manifest"]["project_id"], "fixture project IDs differ")
    for a in (left["root"], left["state"]):
        for b in (right["root"], right["state"]):
            require(not (a.is_relative_to(b) or b.is_relative_to(a)), "private pair paths overlap")
    approval = Path(args.approval_file).resolve(strict=True)
    load_approval(approval)
    output, control = artifact_path(args.output_dir), private_path(args.private_control_dir, missing=True)
    require(not output.exists() and not control.exists(), "output and private control directories must be new")
    require(not (output.is_relative_to(control) or control.is_relative_to(output)), "output and control directories overlap")
    for path in (output, control):
        for side in sides.values():
            for protected in (side["root"], side["state"]):
                require(not (path.is_relative_to(protected) or protected.is_relative_to(path)), "bundle directories overlap server state or reset fixtures")
    require(all((HERE / script).is_file() for _, script, _ in LANES), "a required contract CLI is missing")
    return sides, approval, output, control


def command_for(name, script, sides, approval, report, control):
    adjustment = max(0, 10 - os.getpriority(os.PRIO_PROCESS, 0))
    command = ["nice", "-n", str(adjustment), sys.executable, str(HERE / script), "--output", str(report)]
    for label, side in sides.items():
        command += ["--" + label, side["endpoint"], "--" + label + "-token", str(side["token"])]
        if name != "process":
            command += ["--" + label + "-manifest", str(side["path"])]
        if name in ("paired", "process"):
            command += ["--" + label + ("-fixture" if name == "paired" else "-root"), str(side["root"])]
    if name in ("paired", "process"):
        command += ["--project", sides["reference"]["manifest"]["project_id"]]
    if name in ("telemetry", "admin"):
        command += ["--raw-output", str(control / (name + ".raw.jsonl.gz"))]
    if name == "telemetry":
        command += ["--approval-file", str(approval)]
    return command


def run_bounded(command, control, name, budget, grace=30):
    started = time.monotonic()
    timed_out = interrupted = killed = False
    with (control / (name + ".stdout.log")).open("xb") as stdout, (control / (name + ".stderr.log")).open("xb") as stderr:
        os.fchmod(stdout.fileno(), 0o600)
        os.fchmod(stderr.fileno(), 0o600)
        child = subprocess.Popen(command, stdout=stdout, stderr=stderr, start_new_session=True)
        try:
            child.wait(timeout=max(0.01, budget - grace - 1))
        except (subprocess.TimeoutExpired, KeyboardInterrupt) as error:
            timed_out, interrupted = isinstance(error, subprocess.TimeoutExpired), isinstance(error, KeyboardInterrupt)
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGINT)
            try:
                child.wait(timeout=grace)
            except subprocess.TimeoutExpired:
                killed = True
                if child.poll() is None:
                    os.killpg(child.pid, signal.SIGKILL)
                child.wait(timeout=1)
    return dict(exit_code=child.returncode, timed_out=timed_out, interrupted=interrupted,
                cleanup_forced=killed, elapsed_seconds=round(time.monotonic() - started, 6), budget_seconds=budget)


def receipt(name, path, credentials):
    data, digest = read_json(path, 64 * 1024 * 1024)
    encoded = json.dumps(data, ensure_ascii=False).encode()
    require(not any(value and value in encoded for value in credentials), "credential found in private lane report")
    require(("cleanup" if name == "admin" else "cleanup_errors") in data, "lane cleanup evidence is missing")
    errors = data.get("cleanup_errors", {})
    clean = not (any(errors.values()) if isinstance(errors, dict) else bool(errors))
    if name == "admin":
        clean = clean and not any(value.get("error") or value.get("blocked") for value in data.get("cleanup", {}).values())
    if name == "paired":
        units = data["summary"]
    elif name == "telemetry":
        units = data["comparison_counts"]
    elif name == "admin":
        units = data["counts"]
    else:
        rows = data["scenarios"]
        units = {"scenarios": len(rows), "passed_scenarios": sum(row["status"] == "PASS" for row in rows),
                 "failed_scenarios": sum(row["status"] == "FAIL" for row in rows),
                 "compared_calls_in_passed_scenarios": sum(row["compared_calls"] for row in rows if row["status"] == "PASS")}
    require(isinstance(units, dict) and set(units) == COUNT_KEYS[name] and all(type(value) is int and value >= 0 for value in units.values()), "lane count units changed shape")
    return dict(report_path=str(path), report_sha256=digest, report_bytes=path.stat().st_size,
                reported_gate_passed=data.get("ok" if name == "admin" else "passed") is True,
                cleanup_verified=clean, units=units)


def run(args):
    sides, approval, output, control = prepare(args)  # No writes before both launch guards and approval pass.
    os.umask(0o077)
    control.mkdir(parents=True, mode=0o700)
    output.mkdir(parents=True, mode=0o700)
    summary = {"schema_version": 1, "scope": "four default contract CLIs; other stage-4 gates remain separate",
               "started_at": iso_now(), "launch_manifest_sha256": {k: v["digest"] for k, v in sides.items()},
               "approval_sha256": hashlib.sha256(approval.read_bytes()).hexdigest(), "lanes": []}
    credentials = [side["credential"] for side in sides.values()]
    for name, script, budget in LANES:
        entry = {"lane": name, "script": script, "script_sha256": hashlib.sha256((HERE / script).read_bytes()).hexdigest()}
        summary["lanes"].append(entry)
        try:
            for label, side in sides.items():
                require(read_json(side["path"], 65536)[1] == side["digest"], "launch manifest changed between lanes")
                validate_launch(side["manifest"], side["endpoint"], side["token"], side["root"], 7211 if label == "reference" else 7210)
            report = control / (name + ".json")
            entry.update(run_bounded(command_for(name, script, sides, approval, report, control), control, name, budget))
            entry.update(receipt(name, report, credentials))
        except (Exception, KeyboardInterrupt) as error:
            entry.update(reported_gate_passed=False, cleanup_verified=False, wrapper_error_type=type(error).__name__)
        if entry.get("timed_out") or entry.get("interrupted") or not entry.get("cleanup_verified"):
            break  # Do not mutate another fixture after unproven cleanup.
    summary["finished_at"] = iso_now()
    summary["not_run"] = [name for name, _, _ in LANES[len(summary["lanes"]):]]
    summary["all_cli_gates_passed"] = len(summary["lanes"]) == len(LANES) and all(
        entry.get("exit_code") == 0 and entry.get("reported_gate_passed") and entry.get("cleanup_verified")
        and not entry.get("timed_out") and not entry.get("interrupted") for entry in summary["lanes"])
    encoded = (json.dumps(summary, indent=2, ensure_ascii=False) + "\n").encode()
    require(not any(value and value in encoded for value in credentials), "credential found in aggregate")
    with (output / "summary.json").open("xb") as target:
        target.write(encoded)
    print(json.dumps({"all_cli_gates_passed": summary["all_cli_gates_passed"], "lanes": len(summary["lanes"]), "summary": str(output / "summary.json")}))
    return 0 if summary["all_cli_gates_passed"] else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ("reference-manifest", "candidate-manifest", "approval-file", "output-dir", "private-control-dir"):
        parser.add_argument("--" + flag, required=True)
    def interrupt(signum, frame):
        raise KeyboardInterrupt("bounded bundle interrupted")
    signal.signal(signal.SIGTERM, interrupt)
    try:
        return run(parser.parse_args())
    except (Exception, KeyboardInterrupt) as error:
        print("Contract bundle stopped: " + type(error).__name__ + "; no server lifecycle action was taken.", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
