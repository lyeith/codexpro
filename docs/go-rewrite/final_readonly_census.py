#!/usr/bin/env python3
"""Fresh bounded metadata census; never open live SQLite or control any process.

The only persistent write is one new atomic JSON receipt at the fixed authorized
Projects path. Live files use O_RDONLY|O_NOATIME with no fallback. A failed or
inconsistent observation stays open; this program never repairs its sources.
"""
from __future__ import annotations

import argparse
import collections
import datetime as dt
import errno
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import time
import uuid

MIB = 1024 * 1024
HOME = Path("/home/spite")
PROJECTS = HOME / "Projects"
WORKTREE = PROJECTS / "codexpro-go"
DOCS = WORKTREE / "docs/go-rewrite"
LIVE = HOME / ".codexpro"
LIVE_CONFIG = HOME / ".config/codexpro"
OUTPUT = DOCS / "evidence/final-readonly-census.json"
SOURCE_COMMIT = "80aee7b9da65a2f31682a86406dd5e4b5a4d4ece"
GO_TREE = "2b4205a029bf7e7f02595ab57551a3dae8123711"
BINARY_SHA = "c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2"
RETIRED = (
    "work_manage", "work_update", "codexpro", "codexpro_inventory",
    "read_handoff", "wait_for_handoff", "codex_context", "export_pro_context",
    "handoff_to_agent", "codex_sessions", "read_codex_session",
)
SYSTEM_PROPERTIES = (
    "MainPID", "ActiveState", "SubState", "NRestarts", "ControlGroup",
    "CPUWeight", "Delegate", "ExecMainStartTimestamp",
    "ExecMainStartTimestampMonotonic", "ActiveEnterTimestamp",
    "ActiveEnterTimestampMonotonic", "StateChangeTimestamp",
    "StateChangeTimestampMonotonic",
)
EVIDENCE = (
    "final-build-manifest.json", "final-native-gate-2.json",
    "production-source-equivalence.json", "contract-bundle-1/summary.json",
    "final-contract-cleanup.json", "final-go-ordinary-context.json",
    "final-go-ordinary-cleanup.json", "final-go-under-build-context.json",
    "final-go-under-build-cleanup.json",
    "contemporary-ts-ordinary-cleanup.json",
    "contemporary-ts-under-build-cleanup.json",
    "performance-contemporary-final.json", "performance-stage1-final.json",
    "performance-seeds-final.json", "soak-private-go-1.json",
    "soak-private-go-1-validation.json", "lifecycle-runtime-2.json",
    "direct-lease-isolation-native-1.json", "archive-capture-summary.json",
    "startup-recovery-1.json", "startup-loaded-1.json",
    "startup-loaded-2.json", "startup-loaded-2-startup.json",
    "startup-loaded-3.json", "startup-loaded-3-startup.json",
    "startup-loaded-4.json", "startup-loaded-4-startup.json",
    "startup-loaded-3-end.json", "startup-loaded-4-end.json",
    "startup-loaded-4-preflight.json",
)
REQUIRED_EVIDENCE = {
    "final-build-manifest.json", "final-native-gate-2.json",
    "contract-bundle-1/summary.json", "performance-contemporary-final.json",
    "performance-stage1-final.json", "soak-private-go-1.json",
    "startup-loaded-3.json", "startup-loaded-4.json",
    "startup-loaded-4-preflight.json", "startup-loaded-4-end.json",
}
PROOF_DIRS = (
    "contract-final-go", "contract-final-ts",
    "perf-final-ordinary-go", "perf-final-build-go",
    "perf-contemporary-ordinary-ts", "perf-contemporary-build-ts",
    "startup-loaded-1", "startup-loaded-2", "startup-loaded-3", "startup-loaded-4",
    "startup-loaded-1/candidate", "startup-loaded-2/candidate",
    "startup-loaded-3/candidate", "startup-loaded-4/candidate",
)
PROOF_FILES = ("launch.json", "contract-manifest.json", "manifest.json")
JOB_STATES = {"running", "succeeded", "failed", "stopped", "timed_out"}
ACTION_STATES = {
    "succeeded", "failed", "timed_out", "cancelled", "blocked", "running",
}
JOB_ID = re.compile(r"job_[0-9a-f]{8,64}\Z")
ISO_TIME = re.compile(r"\d{4}-\d\d-\d\dT[0-9:.]+(?:Z|[+-]\d\d:\d\d)\Z")
TOOL_NAME = re.compile(r"[A-Za-z][A-Za-z0-9_.:-]{0,127}\Z")
HEX_SHA = re.compile(r"[0-9a-f]{40,64}\Z")
PROC_LIMIT = 64 * 1024


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def failure(exc):
    """Never stringify exceptions: paths, commands or data may be in messages."""
    result = {"error_type": type(exc).__name__}
    if isinstance(exc, OSError):
        result["errno"] = exc.errno
    return result


def signature(value):
    return {
        "device": value.st_dev, "inode": value.st_ino, "bytes": value.st_size,
        "mtime_ns": value.st_mtime_ns, "ctime_ns": value.st_ctime_ns,
        "uid": value.st_uid, "mode": stat.S_IMODE(value.st_mode),
    }


def flags(directory=False):
    if not hasattr(os, "O_NOATIME"):
        raise RuntimeError("O_NOATIME unavailable")
    value = os.O_RDONLY | os.O_NOATIME | os.O_CLOEXEC | os.O_NOFOLLOW
    return value | os.O_DIRECTORY if directory else value


def strict_read(path, limit, *, tail=False, virtual=False):
    """Return bytes and an explicit stable-window receipt, with no retry fallback."""
    fd = os.open(path, flags())
    try:
        before = os.fstat(fd)
        if not stat.S_ISREG(before.st_mode):
            raise ValueError("source is not a regular file")
        linked_before = os.stat(path, follow_symlinks=False)
        if signature(before) != signature(linked_before):
            raise RuntimeError("source identity changed before read")
        if not virtual and before.st_size > limit and not tail:
            raise ValueError("source exceeds byte bound")
        offset = max(0, before.st_size - limit) if tail else 0
        if offset:
            os.lseek(fd, offset, os.SEEK_SET)
        remaining = limit if virtual else min(limit, max(0, before.st_size - offset))
        chunks = []
        while remaining:
            chunk = os.read(fd, min(1024 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        body = b"".join(chunks)
        after = os.fstat(fd)
        try:
            linked_after = os.stat(path, follow_symlinks=False)
            path_matches = signature(after) == signature(linked_after)
        except OSError:
            path_matches = False
        stable = signature(before) == signature(after) and path_matches
        if not virtual:
            stable = stable and len(body) == min(limit, before.st_size - offset)
        return body, {
            "read_flags": ["O_RDONLY", "O_NOATIME", "O_NOFOLLOW", "O_CLOEXEC"],
            "before": signature(before), "after": signature(after),
            "stable_window": stable, "path_identity_matches_after": path_matches,
            "atime_unchanged_observation": before.st_atime_ns == after.st_atime_ns,
            "read_bytes": len(body), "byte_limit": limit,
            "offset": offset, "prefix_omitted": offset > 0,
            "window_sha256": hashlib.sha256(body).hexdigest(),
            "virtual_file": virtual,
        }
    finally:
        os.close(fd)


def safe_stat(path):
    try:
        value = os.stat(path, follow_symlinks=False)
        return {
            "present": True, **signature(value),
            "regular_file": stat.S_ISREG(value.st_mode),
            "directory": stat.S_ISDIR(value.st_mode),
            "symlink": stat.S_ISLNK(value.st_mode),
        }
    except FileNotFoundError:
        return {"present": False}
    except OSError as exc:
        return {"present": None, **failure(exc)}


def safe_time(value):
    return value if isinstance(value, str) and ISO_TIME.fullmatch(value) else None


def safe_int(value):
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def json_source(path, limit):
    body, receipt = strict_read(path, limit)
    value = json.loads(body)
    return value, receipt


def journal_census():
    try:
        body, receipt = strict_read(LIVE / "audit/tool-calls.jsonl", 16 * MIB, tail=True)
        if receipt["prefix_omitted"]:
            cut = body.find(b"\n")
            receipt["partial_prefix_bytes_discarded"] = len(body) if cut < 0 else cut + 1
            body = b"" if cut < 0 else body[cut + 1:]
        tools = {}
        statuses = collections.Counter()
        malformed = 0
        sequences = []
        for raw in body.splitlines():
            if not raw.strip():
                continue
            try:
                row = json.loads(raw)
                if not isinstance(row, dict):
                    raise ValueError("nonobject action")
            except (ValueError, UnicodeError):
                malformed += 1
                continue
            name = row.get("tool_name")
            name = name if isinstance(name, str) and TOOL_NAME.fullmatch(name) else "unknown"
            status_value = row.get("status")
            status_value = status_value if isinstance(status_value, str) and status_value in ACTION_STATES else "unknown"
            at = safe_time(row.get("occurred_at")) or safe_time(row.get("finished_at"))
            item = tools.setdefault(name, {
                "retained_count": 0, "oldest": None, "newest": None,
                "statuses": collections.Counter(),
            })
            item["retained_count"] += 1
            item["statuses"][status_value] += 1
            statuses[status_value] += 1
            if at:
                item["oldest"] = min(item["oldest"], at) if item["oldest"] else at
                item["newest"] = max(item["newest"], at) if item["newest"] else at
            sequence = safe_int(row.get("sequence"))
            if sequence is not None:
                sequences.append(sequence)
        retired = {}
        for name in RETIRED:
            matches = [
                row for key, row in tools.items()
                if key == name or key.endswith("." + name) or key == "codexpro_" + name
            ]
            retired[name] = {"observed_count": sum(row["retained_count"] for row in matches)}
        result = {
            "source": "live_action_journal", "read": receipt,
            "retained_records_in_window": sum(statuses.values()),
            "malformed_or_partial_lines": malformed,
            "sequence_min": min(sequences, default=None),
            "sequence_max": max(sequences, default=None),
            "by_tool": dict(sorted(tools.items())), "by_status": dict(statuses),
            "retired_surface_observations": retired,
            "retention_scope": {
                "read_cap_bytes": 16 * MIB,
                "only_current_retained_journal": True,
                "older_segments_or_rotations_scanned": False,
                "configured_retention_policy_not_inferred_from_counts": True,
                "zero_observations_do_not_prove_nonuse": True,
            },
        }
        try:
            index, observed = json_source(LIVE / "audit/tool-calls.jsonl.index.json", MIB)
            allowed = {
                "version", "sequence", "nextSequence", "latestSequence",
                "oldestSequence", "retainedBytes", "retainedActions",
                "maxBytes", "maxActions", "retainActions",
            }
            result["retention_index"] = {
                "read": observed,
                "selected_numeric_fields": {
                    key: value for key, value in index.items()
                    if key in allowed and safe_int(value) is not None
                } if isinstance(index, dict) else {},
                "project_identifiers_and_other_fields_omitted": True,
                "not_a_transactional_pair_snapshot": True,
            }
        except (OSError, ValueError, RuntimeError) as exc:
            result["retention_index"] = failure(exc)
        return result
    except (OSError, ValueError, RuntimeError) as exc:
        return {"source": "live_action_journal", "byte_limit": 16 * MIB,
                "stat": safe_stat(LIVE / "audit/tool-calls.jsonl"), **failure(exc)}


def jobs_census(path, requested_ids):
    try:
        value, receipt = json_source(path, 32 * MIB)
        if not isinstance(value, dict) or value.get("version") != 1:
            raise ValueError("unsupported jobs schema")
        rows = value.get("jobs")
        if not isinstance(rows, list):
            raise ValueError("unsupported jobs list")
        counts = collections.Counter()
        selected = []
        unknown = 0
        unacknowledged_terminal = 0
        unsettled = 0
        for row in rows:
            if not isinstance(row, dict):
                unknown += 1
                continue
            state_value = row.get("status")
            state_value = state_value if isinstance(state_value, str) and state_value in JOB_STATES else "unknown"
            counts[state_value] += 1
            if state_value != "running" and row.get("acknowledged") is False:
                unacknowledged_terminal += 1
            if row.get("quiescent") is False:
                unsettled += 1
            if isinstance(row.get("id"), str) and row["id"] in requested_ids:
                selected.append({
                    "id": row["id"], "status": state_value,
                    "started_at": safe_time(row.get("started_at")),
                    "finished_at": safe_time(row.get("finished_at")),
                })
        present = {row["id"] for row in selected}
        return {
            "source": "live_jobs_table", "read": receipt, "schema_version": 1,
            "retained_records": len(rows), "by_status": dict(counts),
            "nonobject_records": unknown,
            "terminal_unacknowledged_count": unacknowledged_terminal,
            "explicit_nonquiescent_count": unsettled,
            "requested_sp6_jobs": sorted(selected, key=lambda row: row["id"]),
            "requested_sp6_ids_absent": sorted(requested_ids - present),
            "project_workspace_commands_paths_and_output_omitted": True,
            "absence_may_mean_retention_pruning": True,
        }
    except (OSError, ValueError, RuntimeError) as exc:
        return {"source": "live_jobs_table", "byte_limit": 32 * MIB,
                "stat": safe_stat(path), **failure(exc)}


def system_service():
    command = ["/usr/bin/systemctl", "--user", "show", "codexpro.service", "--no-pager"]
    command.extend("--property=" + name for name in SYSTEM_PROPERTIES)
    try:
        run = subprocess.run(command, capture_output=True, timeout=5, check=False)
        if run.returncode or len(run.stdout) > PROC_LIMIT:
            return {"exit_code": run.returncode, "available": False}
        values = {}
        for line in run.stdout.decode("utf-8", "replace").splitlines():
            name, separator, value = line.partition("=")
            if separator and name in SYSTEM_PROPERTIES and len(value) <= 2048:
                values[name] = int(value) if value.isdigit() else value
        return {"available": True, "properties": values, "operation": "show_only"}
    except (OSError, subprocess.TimeoutExpired) as exc:
        return {"available": False, **failure(exc)}


def proc_stat(pid):
    body, _ = strict_read(Path("/proc") / str(pid) / "stat", PROC_LIMIT, virtual=True)
    tail = body.decode("utf-8", "replace").rsplit(")", 1)[1].strip().split()
    return {
        "pid": pid, "state": tail[0], "parent_pid": int(tail[1]),
        "process_group": int(tail[2]), "session_id": int(tail[3]),
        "cpu_ticks": int(tail[11]) + int(tail[12]),
        "nice": int(tail[16]), "threads": int(tail[17]),
        "start_ticks": int(tail[19]),
        "rss_bytes": int(tail[21]) * os.sysconf("SC_PAGE_SIZE"),
    }


def proc_metadata(pid, expected_start=None):
    try:
        value = proc_stat(pid)
        if expected_start is not None and value["start_ticks"] != expected_start:
            return {
                "pid": pid, "present": True, "identity_matches": False,
                "expected_start_ticks": expected_start,
                "observed_start_ticks": value["start_ticks"],
                "other_process_metadata_omitted": True,
            }
        cgroup, _ = strict_read(Path("/proc") / str(pid) / "cgroup", PROC_LIMIT, virtual=True)
        later = proc_stat(pid)
        if later["start_ticks"] != value["start_ticks"]:
            return {"pid": pid, "present": True, "identity_matches": False}
        return {
            "present": True, **value,
            "cpu_seconds": value["cpu_ticks"] / os.sysconf("SC_CLK_TCK"),
            "cgroup": cgroup.decode("utf-8", "replace").strip(),
            "identity_matches": expected_start is not None,
            "pid_identity_stable_during_observation": True,
        }
    except FileNotFoundError:
        return {"pid": pid, "present": False, "identity_matches": False}
    except (OSError, ValueError, IndexError, RuntimeError) as exc:
        return {"pid": pid, "present": None, **failure(exc)}


def directory_names(path, limit):
    fd = os.open(path, flags(directory=True))
    try:
        names = os.listdir(fd)
        if len(names) > limit:
            raise ValueError("directory entry bound exceeded")
        return names
    finally:
        os.close(fd)


def listener_census():
    result = {"listeners": [], "errors": [], "ports": [7200, 7210, 7211]}
    for family, filename in (("ipv4", "tcp"), ("ipv6", "tcp6")):
        try:
            body, observed = strict_read(Path("/proc/net") / filename, 4 * MIB, virtual=True)
            if len(body) == 4 * MIB:
                raise ValueError("socket table reached observation bound")
            for line in body.decode("ascii", "replace").splitlines()[1:]:
                columns = line.split()
                if len(columns) < 10 or columns[3] != "0A":
                    continue
                address, port_hex = columns[1].rsplit(":", 1)
                port = int(port_hex, 16)
                if port not in result["ports"]:
                    continue
                address_scope = (
                    "wildcard" if set(address) <= {"0"} else
                    "loopback" if address in {
                        "0100007F", "00000000000000000000000001000000",
                        "00000000000000000000000000000001",
                    } else "other"
                )
                result["listeners"].append({
                    "family": family, "port": port, "address_scope": address_scope,
                    "socket_inode": int(columns[9]),
                })
            result[filename + "_read_bytes"] = observed["read_bytes"]
        except (OSError, ValueError, RuntimeError) as exc:
            result["errors"].append({"family": family, **failure(exc)})
    # Independent supported observation: refusal is loopback IPv4 reachability
    # at this instant, not a complete listener/PID inventory or ownership proof.
    # It does not relax O_NOATIME if the optional /proc socket table was denied.
    checks = []
    for port in (7210, 7211):
        begin = time.monotonic()
        check = {"port": port, "family": "ipv4", "host": "loopback",
                 "method": "tcp_connect_only", "budget_seconds": 0.25}
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as client:
                client.settimeout(0.25)
                code = client.connect_ex(("127.0.0.1", port))
                check["connect_errno"] = code
                check["reachability"] = (
                    "reachable" if code == 0 else
                    "connection_refused" if code == errno.ECONNREFUSED else "unknown"
                )
        except OSError as exc:
            check.update({"reachability": "unknown", **failure(exc)})
        check["latency_ms"] = round((time.monotonic() - begin) * 1000, 6)
        checks.append(check)
    result["observed_at"] = utc()
    result["private_endpoint_reachability"] = checks
    result["private_endpoint_checks_complete"] = all(
        row["reachability"] != "unknown" for row in checks
    )
    result["private_loopback_endpoints_refused"] = all(
        row["reachability"] == "connection_refused" for row in checks
    )
    result["private_endpoint_ipv6_not_probed"] = True
    result["port_presence_does_not_establish_ownership"] = True
    return result


def receipt_identities(value):
    """Extract private-proof identity tuples; never infer ownership from guard observations.

    safety_guard and admission/admission_before/admission_after contain host
    supervisor and landing/stack observations, including standalone preflights.
    Those observed PIDs are not private launches and must not enter this census of
    known proof identities. Actual launch, worker, compiler and cleanup branches
    remain visible. Every returned tuple remains observation-only, not authority.
    """
    result = {}
    pending = [(value, 0)]
    visited = 0
    while pending:
        item, depth = pending.pop()
        visited += 1
        if visited > 100000 or depth > 32:
            raise ValueError("receipt structure bound exceeded")
        if isinstance(item, dict):
            pid = safe_int(item.get("pid"))
            ticks = safe_int(item.get("process_start_ticks"))
            if ticks is None:
                ticks = safe_int(item.get("start_ticks"))
            if pid and pid > 1 and ticks and ticks > 0:
                pgid = next(
                    (safe_int(item[key]) for key in ("pgid", "process_group", "pgrp")
                     if safe_int(item.get(key)) is not None), None
                )
                sid = next(
                    (safe_int(item[key]) for key in ("session_id", "sid")
                     if safe_int(item.get(key)) is not None), None
                )
                identity = {"pid": pid, "start_ticks": ticks}
                if pgid:
                    identity["process_group"] = pgid
                if sid:
                    identity["session_id"] = sid
                result[(pid, ticks)] = {**result.get((pid, ticks), {}), **identity}
            pending.extend((child, depth + 1) for key, child in item.items()
                           if key not in {"request", "response", "body", "content",
                                          "structuredContent", "command", "env", "environment",
                                          "safety_guard", "admission",
                                          "admission_before", "admission_after"}
                           and isinstance(child, (dict, list)))
        elif isinstance(item, list):
            pending.extend((child, depth + 1) for child in item if isinstance(child, (dict, list)))
    return list(result.values())


def inspect_receipt(path):
    try:
        value, observed = json_source(path, 16 * MIB)
        identities = receipt_identities(value)
        return {"read": observed, "pid_identity_count": len(identities)}, identities
    except FileNotFoundError:
        return {"present": False}, []
    except (OSError, ValueError, RuntimeError) as exc:
        return failure(exc), []


def evidence_and_processes(proof_root):
    records = {}
    identities = {}
    for name in EVIDENCE:
        record, found = inspect_receipt(DOCS / "evidence" / name)
        records[name] = record
        if record.get("read", {}).get("stable_window"):
            for value in found:
                key = (value["pid"], value["start_ticks"])
                old = identities.setdefault(key, {"sources": [], **value})
                old.update(value)
                old["sources"].append("evidence/" + name)
    proof_records = {}
    for directory in PROOF_DIRS:
        for filename in PROOF_FILES:
            relative = directory + "/" + filename
            path = proof_root / relative
            # No discovery recursion and no traversal into cache/output trees.
            record, found = inspect_receipt(path)
            if record == {"present": False}:
                continue
            proof_records[relative] = record
            if record.get("read", {}).get("stable_window"):
                for value in found:
                    key = (value["pid"], value["start_ticks"])
                    old = identities.setdefault(key, {"sources": [], **value})
                    old.update(value)
                    old["sources"].append("private_receipt/" + relative)
    observations = []
    for _, witness in sorted(identities.items()):
        actual = proc_metadata(witness["pid"], witness["start_ticks"])
        actual["receipt_witness"] = witness
        if actual.get("identity_matches"):
            for name in ("process_group", "session_id"):
                if name in witness:
                    actual[name + "_matches_receipt"] = actual.get(name) == witness[name]
        observations.append(actual)
    return {
        "selected_evidence": records, "selected_private_receipts": proof_records,
        "process_observations": observations,
        "matching_live_identity_count": sum(row.get("identity_matches") is True for row in observations),
        "unknown_process_observation_count": sum(row.get("present") is None for row in observations),
        "scope": "Only bounded selected receipts; no cmdline/environ reads, no process signals.",
        "limits": [
            "Receipt PID/start/group matches are observations, never authority to adopt or signal.",
            "A historical receipt without a boot witness cannot establish cross-boot process continuity.",
            "Absent recorded leaders and absent ports do not prove all descendants quiescent.",
            "Historical cleanup proofs remain separate from this fresh metadata observation.",
            "No recursive proof-cache scan and no inference from process names.",
        ],
    }


def own_fingerprints():
    selected = {
        "candidate_binary": (HOME / ".codexpro-go/bin/codexpro-go", 64 * MIB),
        "census_source": (DOCS / "final_readonly_census.py", MIB),
        "go_mod": (WORKTREE / "go/go.mod", MIB),
        "go_sum": (WORKTREE / "go/go.sum", 4 * MIB),
        "results_document": (DOCS / "RESULTS.md", 4 * MIB),
        "cutover_runbook": (DOCS / "CUTOVER-RUNBOOK.md", 4 * MIB),
    }
    result = {}
    for label, (path, limit) in selected.items():
        try:
            _, observed = strict_read(path, limit)
            result[label] = observed
        except (OSError, ValueError, RuntimeError) as exc:
            result[label] = failure(exc)
    result["candidate_binary_matches_tested_sha"] = (
        result.get("candidate_binary", {}).get("stable_window") is True
        and result["candidate_binary"]["window_sha256"] == BINARY_SHA
    )
    return result


def source_identity():
    env = dict(os.environ)
    env.update({"GIT_OPTIONAL_LOCKS": "0", "GIT_TERMINAL_PROMPT": "0", "GIT_PAGER": "cat"})
    def git(arguments):
        run = subprocess.run(
            ["/usr/bin/git", "-C", str(WORKTREE), *arguments],
            env=env, capture_output=True, timeout=10, check=False,
        )
        if run.returncode or len(run.stdout) > MIB:
            raise RuntimeError("git observation failed")
        return run.stdout.decode("utf-8", "replace").strip()
    try:
        head = git(["rev-parse", "--verify", "HEAD"])
        current = git(["rev-parse", "--verify", "HEAD:go"])
        expected = git(["rev-parse", "--verify", SOURCE_COMMIT + ":go"])
        changed = git(["status", "--porcelain=v1", "--untracked-files=all", "--", "go"])
        if not all(HEX_SHA.fullmatch(value) for value in (head, current, expected)):
            raise ValueError("invalid source identity")
        return {
            "head": head, "head_go_tree": current, "tested_source_commit": SOURCE_COMMIT,
            "tested_go_tree": expected, "known_expected_go_tree": GO_TREE,
            "committed_go_tree_matches": current == expected == GO_TREE,
            "go_worktree_clean": not changed,
            "changed_go_record_count": len(changed.splitlines()) if changed else 0,
            "go_status_sha256": hashlib.sha256(changed.encode()).hexdigest(),
            "git_optional_locks_disabled": True,
            "git_read_only_observation": True, "no_fetch_or_index_refresh_requested": True,
        }
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as exc:
        return failure(exc)


def live_database_stats():
    result = {}
    for label, relative in (
        ("work", "work/work.sqlite"),
        ("usage", "audit/usage/usage.sqlite"),
        ("inbox", "audit/inbox/inbox.sqlite"),
    ):
        result[label] = {
            suffix or "database": safe_stat(LIVE / (relative + suffix))
            for suffix in ("", "-wal", "-shm")
        }
    return {
        "observed_at": utc(), "files": result,
        "sqlite_connections": 0, "content_reads": 0,
        "scope": "lstat-equivalent metadata only; no backup/checkpoint/SHM access.",
    }


def archive_directory_stats(root):
    result = {"root": safe_stat(root), "selected_archives": {}}
    for label, relative in (
        ("historical_work", "work-archive-20261031T1830Z/work.sqlite"),
        ("historical_work_legacy", "legacy-proof/work-archive-20261031T1830Z/work.sqlite"),
        ("historical_work_proof", "proof/work-archive-20261031T1830Z/work.sqlite"),
    ):
        observed = safe_stat(root / relative)
        if observed.get("present"):
            result["selected_archives"][label] = observed
    result["no_recursive_scan_or_large_archive_hash"] = True
    return result


def health_metadata(token_file):
    started = utc()
    begin = time.monotonic()
    result = {"started_at": started, "port": 7200, "budget_seconds": 1.0,
              "request": "GET /healthz", "authenticated": token_file is not None}
    credential = None
    if token_file is not None:
        try:
            raw, observed = strict_read(token_file, 8192)
            if not observed["stable_window"]:
                raise RuntimeError("credential source changed")
            credential = raw.strip()
            if not credential or any(value < 33 or value > 126 for value in credential):
                raise ValueError("invalid credential framing")
        except (OSError, ValueError, RuntimeError) as exc:
            return {**result, "passed": False, "status": "open", **failure(exc)}
    deadline = time.monotonic() + 1.0
    network_begin = time.monotonic()
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as client:
            def budget():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("health deadline")
                client.settimeout(remaining)
            budget()
            client.connect(("127.0.0.1", 7200))
            request = b"GET /healthz HTTP/1.1\r\nHost: 127.0.0.1:7200\r\nConnection: close\r\n"
            if credential is not None:
                request += b"Authorization: Bearer " + credential + b"\r\n"
            budget()
            client.sendall(request + b"\r\n")
            received = b""
            header_end = -1
            length = None
            while True:
                budget()
                chunk = client.recv(min(8192, 65536 - len(received)))
                if not chunk:
                    break
                received += chunk
                if len(received) >= 65536:
                    raise ValueError("health response bound")
                if header_end < 0:
                    header_end = received.find(b"\r\n\r\n")
                    if header_end < 0 and len(received) > 16384:
                        raise ValueError("health header bound")
                    if header_end >= 0:
                        for header in received[:header_end].split(b"\r\n")[1:]:
                            key, _, value = header.partition(b":")
                            if key.lower() == b"content-length":
                                length = int(value.strip())
                                if length < 0 or length > 49152:
                                    raise ValueError("health body bound")
                if header_end >= 0 and length is not None:
                    if len(received) >= header_end + 4 + length:
                        break
            latency = (time.monotonic() - network_begin) * 1000
            if header_end < 0:
                raise ValueError("incomplete health response")
            first = received[:header_end].split(b"\r\n", 1)[0].split()
            code = int(first[1])
            payload = received[header_end + 4:]
            if length is not None and len(payload) != length:
                raise ValueError("incomplete health body")
            try:
                parsed = json.loads(payload)
                body_ok = isinstance(parsed, dict) and parsed.get("ok") is True
            except (ValueError, UnicodeError):
                body_ok = False
            passed = code == 200 and body_ok and latency <= 1000
            result.update({"http_status": code, "latency_ms": round(latency, 6),
                           "body_ok": body_ok, "response_bytes": len(received),
                           "passed": passed, "status": "observed" if passed else "open"})
    except (OSError, ValueError, IndexError) as exc:
        result.update({"passed": False, "status": "open", **failure(exc),
                       "latency_ms": round((time.monotonic() - network_begin) * 1000, 6)})
    finally:
        credential = None
    result["elapsed_including_credential_read_ms"] = round((time.monotonic() - begin) * 1000, 6)
    result["finished_at"] = utc()
    return result


def private_path(value):
    path = value.expanduser().resolve(strict=True)
    forbidden = (LIVE.resolve(), LIVE_CONFIG.resolve())
    if any(path == root or root in path.parents for root in forbidden):
        raise ValueError("protected live root is not a proof root")
    allowed = (HOME / ".codexpro-go-proof", HOME / ".codexpro-go", PROJECTS)
    if not any(path == root or root in path.parents for root in allowed):
        raise ValueError("proof root is outside authorized private roots")
    if not path.is_dir():
        raise ValueError("proof root is not a directory")
    return path


def publish_new(path, report):
    """Atomic no-overwrite publication in the already-existing evidence directory."""
    if path != OUTPUT or path.parent.resolve(strict=True) != OUTPUT.parent:
        raise ValueError("output must be the authorized final census path")
    directory = os.open(path.parent, flags(directory=True))
    temporary = ".final-readonly-census-" + uuid.uuid4().hex + ".json"
    fd = None
    try:
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                     os.O_CLOEXEC | os.O_NOFOLLOW, 0o600, dir_fd=directory)
        data = (json.dumps(report, indent=2, sort_keys=True) + "\n").encode()
        with os.fdopen(fd, "wb") as handle:
            fd = None
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        # link() fails if final destination exists; unlike replace(), it cannot clobber.
        os.link(temporary, path.name, src_dir_fd=directory, dst_dir_fd=directory,
                follow_symlinks=False)
        os.unlink(temporary, dir_fd=directory)
        os.fsync(directory)
        return hashlib.sha256(data).hexdigest(), len(data)
    finally:
        if fd is not None:
            os.close(fd)
        try:
            os.unlink(temporary, dir_fd=directory)
        except FileNotFoundError:
            pass
        os.close(directory)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--proof-root", type=Path, default=HOME / ".codexpro-go-proof")
    parser.add_argument("--evidence-root", type=Path, default=PROJECTS / "sp6-evidence")
    parser.add_argument("--jobs-file", type=Path, default=LIVE / "jobs/jobs.json")
    parser.add_argument("--live-token-file", type=Path)
    parser.add_argument("--sp6-job-id", action="append", default=[])
    args = parser.parse_args()
    # Validate destinations before observations; this never creates directories.
    output = args.output.expanduser().absolute()
    if output != OUTPUT or output.parent.resolve(strict=True) != OUTPUT.parent:
        parser.error("output must be the fixed Projects final census destination")
    if os.path.lexists(output):
        parser.error("output exists; reconcile the prior receipt instead of overwriting")
    proof_root = private_path(args.proof_root)
    evidence_root = private_path(args.evidence_root)
    if evidence_root != PROJECTS and PROJECTS not in evidence_root.parents:
        parser.error("evidence root must be beneath Projects")
    jobs_file = args.jobs_file.expanduser().absolute()
    if LIVE not in jobs_file.resolve(strict=False).parents:
        parser.error("jobs source must be beneath protected live state for read-only census")
    token_file = args.live_token_file.expanduser().absolute() if args.live_token_file else None
    if token_file is not None:
        resolved = token_file.resolve(strict=False)
        if not any(root in resolved.parents for root in (LIVE, LIVE_CONFIG)):
            parser.error("credential must be in the known live state/config root")
    requested = {"job_19a613e8"}
    for value in args.sp6_job_id:
        if not JOB_ID.fullmatch(value):
            parser.error("invalid SP-6 job identifier")
        requested.add(value)
    started = utc()
    monotonic_start = time.monotonic()
    service_before = system_service()
    report = {
        "schema_version": 1, "kind": "fresh-metadata-only-readonly-census",
        "started_at": started, "no_live_store_writes": True,
        "no_service_lifecycle_actions": True, "sqlite_connections": 0,
        "service_before": service_before,
    }
    report["journal"] = journal_census()
    report["jobs"] = jobs_census(jobs_file, requested)
    report["live_databases_stat_only"] = live_database_stats()
    main_pid = service_before.get("properties", {}).get("MainPID")
    report["live_main_process"] = proc_metadata(main_pid) if isinstance(main_pid, int) and main_pid > 0 else {
        "available": False
    }
    report["private_proofs"] = evidence_and_processes(proof_root)
    report["listeners"] = listener_census()
    report["own_fingerprints"] = own_fingerprints()
    report["source_identity"] = source_identity()
    report["private_archive_metadata"] = archive_directory_stats(evidence_root)
    report["live_health"] = health_metadata(token_file)
    report["service_after"] = system_service()
    first = service_before.get("properties", {})
    last = report["service_after"].get("properties", {})
    service_stable = bool(first and last and first == last)
    report["source_consistency"] = {
        "service_properties_stable": service_stable,
        "journal_stable_window": report["journal"].get("read", {}).get("stable_window", False),
        "jobs_stable_window": report["jobs"].get("read", {}).get("stable_window", False),
        "global_transactional_snapshot": False,
        "sources_observed_sequentially_without_locks_or_source_pause": True,
    }
    open_reasons = []
    for name in ("journal", "jobs"):
        if report[name].get("read", {}).get("stable_window") is not True:
            open_reasons.append(name + "_read_unavailable_or_changed")
    if report["journal"].get("malformed_or_partial_lines"):
        open_reasons.append("journal_contains_malformed_or_partial_lines")
    if not service_stable or first.get("ActiveState") != "active":
        open_reasons.append("live_service_unavailable_or_transitioned")
    if not report["live_health"].get("passed"):
        open_reasons.append("live_health_unavailable_or_failed")
    source = report["source_identity"]
    if not source.get("committed_go_tree_matches") or not source.get("go_worktree_clean"):
        open_reasons.append("tested_go_source_identity_not_verified")
    if not report["own_fingerprints"].get("candidate_binary_matches_tested_sha"):
        open_reasons.append("tested_binary_identity_not_verified")
    if report["private_proofs"]["unknown_process_observation_count"]:
        open_reasons.append("private_process_observation_incomplete")
    if not report["listeners"]["private_endpoint_checks_complete"]:
        open_reasons.append("private_endpoint_reachability_incomplete")
    elif not report["listeners"]["private_loopback_endpoints_refused"]:
        open_reasons.append("private_loopback_endpoint_still_reachable")
    if report["private_proofs"]["matching_live_identity_count"]:
        open_reasons.append("known_private_process_identity_still_present")
    for name in sorted(REQUIRED_EVIDENCE):
        if report["private_proofs"]["selected_evidence"][name].get("read", {}).get("stable_window") is not True:
            open_reasons.append("required_evidence_unavailable:" + name)
    report["open_reasons"] = open_reasons
    report["census_observations_complete"] = not open_reasons
    report["limitations"] = [
        "This metadata census is not a contract, startup, ownership or cutover gate.",
        "Retained journal/job counts do not describe all historical traffic or deleted records.",
        "Zero retired-tool observations never establish that a feature was never used.",
        "No raw payload, command, environment, token, document or other-project detail is exported.",
        "No live SQLite DB, WAL or SHM is opened for content; historical session/run counts are not refreshed.",
        "Port listeners and PID/group observations do not confer ownership or prove all descendants quiescent.",
        "Private evidence uses only a bounded selected-file list; caches and large archives are not recursively hashed.",
        "The one-second health budget covers the network GET; credential-file reading is separately timed.",
        "The source files are read without atime updates; an unavailable O_NOATIME read is not retried without it.",
        "Private endpoint checks are bounded IPv4 loopback connect-only reachability, not a full listener inventory.",
        "Optional /proc socket-table denial remains recorded; independent refused connections can establish the observed private endpoint absence.",
        "No new test/load/benchmark or service-control action is executed.",
    ]
    report["finished_at"] = utc()
    report["elapsed_seconds"] = round(time.monotonic() - monotonic_start, 6)
    digest, length = publish_new(output, report)
    print(json.dumps({
        "receipt": "evidence/final-readonly-census.json",
        "sha256": digest, "bytes": length,
        "census_observations_complete": report["census_observations_complete"],
        "open_reasons": open_reasons,
    }, sort_keys=True))
    return 0 if report["census_observations_complete"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
