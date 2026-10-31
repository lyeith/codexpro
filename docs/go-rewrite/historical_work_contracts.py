#!/usr/bin/env python3
"""Prepare and compare pure historical work_status contracts on a disposable copy.

No live SQLite connection, coordinator, process launcher, or session is used.
The source must be a normalized archive produced by archive_work.py.
Run under nice -n 10 and an external timeout.
"""
from __future__ import annotations

import argparse
from contextlib import closing
import copy
import datetime as dt
import difflib
import hashlib
import json
import os
from pathlib import Path
import pwd
import sqlite3
import sys
from urllib.parse import quote

from archive_work import copy_readonly, hash_readonly, identity, write_json

SOURCE_COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"
MAX_CASES = 1024
MAX_EVIDENCE_BYTES = 64 * 1024 * 1024


def require(value, message):
    if not value:
        raise ValueError(message)


def private_path(path, *, exists=True):
    path = Path(path).expanduser().resolve(strict=exists)
    homes = {Path.home(), Path(pwd.getpwuid(os.getuid()).pw_dir)}
    for home in homes:
        for relative in (".codexpro", ".config/codexpro"):
            blocked = home / relative
            require(not (path == blocked or path.is_relative_to(blocked)
                         or path == blocked.resolve(strict=False)
                         or path.is_relative_to(blocked.resolve(strict=False))),
                    "protected live state path refused")
    return path


def read_json(path, limit=1024 * 1024):
    require(path.is_file() and path.stat().st_size <= limit, "input must be a bounded regular file")
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def open_immutable(path):
    connection = sqlite3.connect("file:" + quote(str(path), safe="/") + "?mode=ro&immutable=1", uri=True)
    connection.execute("PRAGMA query_only=ON")
    require(connection.execute("PRAGMA query_only").fetchone()[0] == 1, "archive connection is not query-only")
    require(connection.execute("PRAGMA journal_mode").fetchone()[0] == "delete",
            "only a normalized DELETE-journal archive may be inspected")
    connection.row_factory = sqlite3.Row
    return connection


def canonical(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def utc_clock(value):
    require(isinstance(value, str) and len(value) == 24 and value.endswith("Z"),
            "clock must use exact UTC milliseconds")
    parsed = dt.datetime.fromisoformat(value[:-1] + "+00:00")
    require(parsed.isoformat(timespec="milliseconds").replace("+00:00", "Z") == value,
            "clock must use exact UTC milliseconds")
    return value


def build_cases(connection):
    runs = [json.loads(row["body"]) for row in connection.execute("SELECT body FROM runs ORDER BY rowid DESC")]
    require(runs and len(runs) <= 100, "bounded historical lane requires 1–100 archived runs")
    principals = sorted({run["principal_id"] for run in runs})
    cases = []
    coverage = {"runs": len(runs), "principals": len(principals), "run_states": {},
                "document_revisions_sampled": 0, "all_document_revisions_preserved": None}
    def add(label, principal, arguments, expected_error=False):
        require(len(cases) < MAX_CASES, "historical case budget exceeded")
        cases.append({"case_id": label, "tool": "work_status", "principal_id": principal,
                      "arguments": arguments, "expected_error": expected_error})
    for index, principal in enumerate(principals):
        prefix = "principal-" + str(index)
        owned = [run for run in runs if run["principal_id"] == principal]
        add(prefix + "-list-default", principal, {})
        add(prefix + "-list-page-one", principal, {"action": "list", "offset": 0, "limit": 2})
        add(prefix + "-list-page-two", principal, {"action": "list", "offset": 2, "limit": 2})
        for key in ("claimed", "needs_attention"):
            for flag in (False, True):
                add(prefix + "-" + key + "-" + str(flag).lower(), principal, {"action": "list", key: flag, "limit": 50})
        for state in sorted({run["state"] for run in owned}):
            add(prefix + "-state-" + state, principal, {"action": "list", "state": state, "limit": 50})
        for project_number, project in enumerate(sorted({run["project_id"] for run in owned})):
            add(prefix + "-project-" + str(project_number), principal,
                {"action": "list", "project_id": project, "limit": 50})
            add(prefix + "-project-search-none-" + str(project_number), principal,
                {"action": "search_memory", "project_id": project, "query": "SP6_NO_SUCH_ARCHIVE_TEXT_7c420d"})
        add(prefix + "-missing-run", principal, {"action": "get"}, True)
        add(prefix + "-unknown-run", principal, {"action": "get", "run_id": "run_SP6missing_8c42a101"}, True)
        add(prefix + "-search-without-scope", principal, {"action": "search_memory", "query": "fixture"}, True)
        # Include an ambiguous short reference only if this retained corpus has one.
        candidates = {}
        for run in owned:
            if len(run["id"]) >= 12:
                candidates.setdefault(run["id"][:12], []).append(run)
        ambiguous = next((key for key, values in candidates.items() if len(values) > 1), None)
        if ambiguous:
            add(prefix + "-ambiguous-prefix", principal, {"action": "get", "run_id": ambiguous}, True)
    outsider = "connector_" + hashlib.sha256(b"SP6-archive-inaccessible-principal").hexdigest()[:32]
    require(outsider not in principals, "synthetic outsider unexpectedly owns archived work")
    add("outsider-empty-list", outsider, {"action": "list", "limit": 50})
    add("outsider-exact-id", outsider, {"action": "get", "run_id": runs[0]["id"], "section": "summary"}, True)
    for index, run in enumerate(runs):
        principal, run_id = run["principal_id"], run["id"]
        tag = "run-" + str(index)
        coverage["run_states"][run["state"]] = coverage["run_states"].get(run["state"], 0) + 1
        common = {"action": "get", "run_id": run_id}
        add(tag + "-summary", principal, {**common, "section": "summary"})
        add(tag + "-packet", principal, {**common, "section": "packet", "offset": 0, "limit": 3})
        add(tag + "-packet-next", principal, {**common, "section": "packet", "offset": 3, "limit": 3})
        add(tag + "-exact-id-precedes-project-filter", principal,
            {**common, "section": "summary", "project_id": "sp6-different-project"})
        owned = [value for value in runs if value["principal_id"] == principal]
        length = 12
        while length < len(run_id) and sum(value["id"].startswith(run_id[:length]) for value in owned) != 1:
            length += 1
        if length <= len(run_id):
            add(tag + "-unique-prefix", principal, {**common, "run_id": run_id[:length], "section": "summary"})
        if len(run_id) > 16:
            add(tag + "-compact-reference", principal,
                {**common, "run_id": run_id[:12] + "…" + run_id[-4:], "section": "summary"})
        for section in ("todos", "acceptance", "documents", "iterations", "operations", "jobs"):
            add(tag + "-" + section + "-first", principal, {**common, "section": section, "offset": 0, "limit": 3})
            add(tag + "-" + section + "-second", principal, {**common, "section": section, "offset": 3, "limit": 3})
        add(tag + "-source-inert", principal, {**common, "section": "source"})
        add(tag + "-activity-disabled", principal, {**common, "section": "activity"})
        add(tag + "-activity-after-checkpoint", principal,
            {**common, "section": "activity", "after_sequence": 1})
        add(tag + "-history-first", principal, {"action": "history", "run_id": run_id, "limit": 3})
        boundary = connection.execute("SELECT max(sequence) FROM events WHERE run_id=?", (run_id,)).fetchone()[0] or 0
        add(tag + "-history-tail", principal,
            {"action": "history", "run_id": run_id, "after_sequence": max(0, boundary - 2), "limit": 3})
        add(tag + "-history-empty-cursor", principal,
            {"action": "history", "run_id": run_id, "after_sequence": boundary, "limit": 3})
        operations = connection.execute(
            "SELECT id FROM operations WHERE run_id=? ORDER BY rowid", (run_id,)).fetchall()
        operation_ids = list(dict.fromkeys([row["id"] for row in operations[:1] + operations[-1:]]))
        for number, operation in enumerate(operation_ids):
            add(tag + "-operation-" + str(number), principal,
                {"action": "operation", "run_id": run_id, "operation_id": operation})
        add(tag + "-missing-operation", principal,
            {"action": "operation", "run_id": run_id, "operation_id": "SP6_missing_operation"}, True)
        add(tag + "-missing-document-argument", principal, {"action": "read_document", "run_id": run_id}, True)
        add(tag + "-unknown-document", principal,
            {"action": "read_document", "run_id": run_id, "document_id": "SP6_missing_document"}, True)
        documents = connection.execute(
            "SELECT id,revision,kind,title,content FROM documents WHERE run_id=? ORDER BY id,revision",
            (run_id,)).fetchall()
        selected = []
        for kind in ("spec", "handoff", "project_memory"):
            members = [row for row in documents if row["kind"] == kind]
            if members:
                chosen_id = members[0]["id"]
                revisions = [row for row in members if row["id"] == chosen_id]
                selected.extend(revisions[:1] + revisions[-1:])
        selected.extend(documents[:1] + documents[-1:])
        selected = list({(row["id"], row["revision"]): row for row in selected}.values())[:8]
        coverage["document_revisions_sampled"] += len(selected)
        for number, document in enumerate(selected):
            args = {"action": "read_document", "run_id": run_id, "document_id": document["id"],
                    "document_revision": document["revision"], "max_bytes": 512}
            add(tag + "-document-" + str(number) + "-start", principal, {**args, "offset": 0})
            encoded = document["content"].encode()
            continuation = next((position for position, byte in enumerate(encoded) if byte & 0xC0 == 0x80), None)
            offset = continuation if continuation is not None else max(0, len(encoded) - 1)
            add(tag + "-document-" + str(number) + "-boundary", principal, {**args, "offset": offset})
            if number == 0:
                add(tag + "-document-past-end", principal, {**args, "offset": len(encoded) + 20})
                add(tag + "-document-current-revision", principal,
                    {"action": "read_document", "run_id": run_id, "document_id": document["id"], "max_bytes": 512})
                needle = next((part for part in document["title"].split() if part), "Specification")
                add(tag + "-memory-search", principal,
                    {"action": "search_memory", "run_id": run_id, "query": needle[:80], "offset": 0, "limit": 3})
        add(tag + "-memory-search-empty", principal,
            {"action": "search_memory", "run_id": run_id, "query": "SP6_NO_SUCH_ARCHIVE_TEXT_7c420d"})
    coverage["all_document_revisions_preserved"] = connection.execute("SELECT count(*) FROM documents").fetchone()[0]
    require(len({case["case_id"] for case in cases}) == len(cases), "generated case identity collision")
    return cases, coverage


def prepare(archive_manifest, destination, *, packet_bytes=16384, clock=None):
    archive_manifest = private_path(archive_manifest)
    manifest = read_json(archive_manifest, 8 * 1024 * 1024)
    normalized = manifest["normalized_archive"]
    require(normalized["journal_mode"] == "delete" and normalized["integrity_check"] == "ok"
            and normalized["table_digests_match_raw_snapshot"] is True,
            "source archive lacks normalized consistency proof")
    source = private_path(archive_manifest.parent / normalized["path"])
    require(source.is_relative_to(archive_manifest.parent) and source.is_file(),
            "normalized source escapes the archive")
    destination = private_path(destination, exists=False)
    require(not destination.exists() and not destination.is_relative_to(archive_manifest.parent),
            "use a new fixture outside the published archive directory")
    require(4096 <= packet_bytes <= 65536, "packet budget must be 4096–65536")
    for suffix in ("-wal", "-journal"):
        path = Path(str(source) + suffix)
        require(not path.exists() or path.stat().st_size == 0, "normalized archive has a nonempty sidecar")
    source_identity = identity(source)
    require(source_identity["bytes"] == normalized["bytes"] and source_identity["bytes"] <= 1024**3,
            "normalized archive size differs from its manifest")
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    fixture = destination / "database"
    fixture.mkdir(mode=0o700)
    disposable = fixture / "work.sqlite"
    digest = copy_readonly(source, disposable, source_identity)
    require(digest == normalized["sha256"] and hash_readonly(disposable) == digest,
            "disposable copy differs from the captured archive")
    os.chmod(disposable, 0o400)
    with closing(open_immutable(disposable)) as connection:
        expected_tables = manifest["inventory"]["tables"]
        actual_counts = {name: connection.execute('SELECT count(*) FROM "' + name.replace('"', '""') + '"').fetchone()[0]
                         for name in expected_tables}
        require(all(actual_counts[name] == info["rows"] for name, info in expected_tables.items()),
                "disposable table counts differ from the archive inventory")
        cases, coverage = build_cases(connection)
    require(coverage["runs"] == len(manifest["inventory"]["runs"]),
            "contract cases did not cover every archived run")
    cases_path = destination / "cases.jsonl"
    fd = os.open(cases_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        for case in cases:
            stream.write(json.dumps(case, ensure_ascii=False, separators=(",", ":")) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    plan = {
        "schema_version": 1, "source_commit": SOURCE_COMMIT, "disposable_copy": True,
        "source_archive": str(source), "source_manifest_sha256": hash_readonly(archive_manifest),
        "archive": {"path": str(disposable), "sha256": digest, "bytes": disposable.stat().st_size},
        "clock": utc_clock(clock or manifest["completed_at"]), "packet_bytes": packet_bytes,
        "context_dir": ".ai-bridge", "cases_path": str(cases_path), "cases": len(cases),
        "coverage": coverage, "preserved_table_counts": actual_counts,
        "principal_policy": "Each case explicitly supplies an archived principal; inaccessible-principal cases verify exact filtering. No ownership is rewritten.",
        "runtime_observers": "Fixed clock; source explicitly unobserved; audit disabled; no in-flight execution. Archived job records are read as historical evidence.",
    }
    plan_path = destination / "plan.json"
    write_json(plan_path, plan)
    require(hash_readonly(disposable) == digest, "fixture preparation changed the archive")
    return {"plan": str(plan_path), "cases": len(cases), "work_runs": coverage["runs"],
            "document_revisions_preserved": coverage["all_document_revisions_preserved"],
            "document_revisions_sampled": coverage["document_revisions_sampled"]}


def read_jsonl(path):
    require(path.is_file() and path.stat().st_size <= MAX_EVIDENCE_BYTES, "evidence exceeds 64 MiB")
    rows = []
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            if line.strip():
                rows.append(json.loads(line))
    require(len(rows) <= MAX_CASES, "evidence case budget exceeded")
    require(len({row["case_id"] for row in rows}) == len(rows), "duplicate case in evidence")
    return rows


def comparable(row):
    result = copy.deepcopy(row)
    if "result" in result:
        envelope = result["result"]
        blocks = envelope.get("content")
        require(isinstance(blocks, list) and len(blocks) == 1
                and blocks[0].get("type") == "text" and isinstance(blocks[0].get("text"), str),
                "work_status no longer returns one text content block")
        # The tool deliberately renders a JSON payload in its text block.
        # Compare every decoded value, allowing only insignificant object key
        # ordering/whitespace. Plain strings and all metadata remain exact.
        try:
            decoded = json.loads(blocks[0]["text"])
        except json.JSONDecodeError:
            pass
        else:
            blocks[0]["text"] = {"comparison_encoding": "parsed JSON", "value": decoded}
    return result


def compare(plan_path, reference_path, candidate_path, output_path):
    plan_path, reference_path, candidate_path = map(private_path, (plan_path, reference_path, candidate_path))
    plan = read_json(plan_path)
    source = read_jsonl(reference_path)
    candidate = read_jsonl(candidate_path)
    requested = read_jsonl(private_path(plan["cases_path"]))
    require([row["case_id"] for row in source] == [row["case_id"] for row in requested] ==
            [row["case_id"] for row in candidate], "reference/candidate case order or membership changed")
    reference_manifest = read_json(Path(str(reference_path) + ".manifest.json"))
    candidate_manifest = read_json(Path(str(candidate_path) + ".manifest.json"))
    for path, receipt in ((reference_path, reference_manifest), (candidate_path, candidate_manifest)):
        require(receipt["archive_unchanged"] is True
                and receipt["archive_sha256"] == plan["archive"]["sha256"]
                and receipt["cases"] == len(requested)
                and receipt["output_sha256"] == hash_readonly(path), "evidence attestation is inconsistent")
    cases = []
    for expected, left, right in zip(requested, source, candidate):
        lvalue, rvalue = comparable(left), comparable(right)
        error_expected = expected["expected_error"]
        outcomes_match_expectation = ("error" in left) is error_expected and ("error" in right) is error_expected
        equal = canonical(lvalue) == canonical(rvalue)
        case = {"case_id": expected["case_id"], "passed": equal and outcomes_match_expectation,
                "expected_error": error_expected, "reference_error": "error" in left,
                "candidate_error": "error" in right}
        if not equal:
            case["diff"] = "\n".join(difflib.unified_diff(
                json.dumps(lvalue, ensure_ascii=False, indent=2, sort_keys=True).splitlines(),
                json.dumps(rvalue, ensure_ascii=False, indent=2, sort_keys=True).splitlines(),
                fromfile="release-read-reference", tofile="go-workhistory"))[:24000]
        cases.append(case)
    archive_hash = hash_readonly(private_path(plan["archive"]["path"]))
    require(archive_hash == plan["archive"]["sha256"], "archive changed after the paired lane")
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT,
              "lane": "pure-historical-work-status", "passed": all(case["passed"] for case in cases),
              "cases": cases, "coverage": plan["coverage"], "preserved_table_counts": plan["preserved_table_counts"],
              "archive_unchanged": True, "archive_sha256": archive_hash,
              "reference_sha256": hash_readonly(reference_path), "candidate_sha256": hash_readonly(candidate_path),
              "clock": plan["clock"], "packet_bytes": plan["packet_bytes"],
              "comparison_rule": "All envelope fields, values, errors, byte metadata, arrays and null presence exact; JSON text content decoded without dropping fields.",
              "approved_archive_semantics": "No reconciliation, coordinator/store constructor, session, launcher, or current-source inspection. Source is explicitly incomplete; execution and audit observers are inert.",
              "scope": "Historical read/format/principal contracts, alongside the separate paired HTTP/MCP lanes."}
    output = private_path(output_path, exists=False)
    require(output.is_relative_to(plan_path.parent) and not output.exists(),
            "use a new report path inside the disposable fixture")
    write_json(output, report)
    return {"passed": report["passed"], "cases": len(cases),
            "failures": sum(not case["passed"] for case in cases), "report": str(output)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("--archive-manifest", type=Path, required=True)
    prepare_parser.add_argument("--destination", type=Path, required=True)
    prepare_parser.add_argument("--packet-bytes", type=int, default=16384)
    prepare_parser.add_argument("--clock")
    compare_parser = commands.add_parser("compare")
    compare_parser.add_argument("--plan", type=Path, required=True)
    compare_parser.add_argument("--reference", type=Path, required=True)
    compare_parser.add_argument("--candidate", type=Path, required=True)
    compare_parser.add_argument("--output", type=Path, required=True)
    options = parser.parse_args()
    if options.command == "prepare":
        result = prepare(options.archive_manifest, options.destination,
                         packet_bytes=options.packet_bytes, clock=options.clock)
    else:
        result = compare(options.plan, options.reference, options.candidate, options.output)
    print(json.dumps(result))
    if result.get("passed") is False:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
