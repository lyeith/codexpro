#!/usr/bin/env python3
"""Prepare/verify actual Go readers over disposable legacy telemetry copies."""
from __future__ import annotations

import argparse
from contextlib import closing
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import sqlite3

from archive_work import copy_readonly, hash_readonly, identity, sqlite_uri, sync_directory, write_json
from legacy_sqlite_migration import inventory, private_path, require

SOURCE_COMMIT = "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a"
WEEK_MS = 7 * 24 * 60 * 60 * 1000
COVERAGE_NOTE = "SessionPilot Pro submissions, including in-flight requests and requests that later fail or are canceled. Pre-send failures and unconfirmed sends are excluded. This count is not OpenAI's remaining allowance."


def read_json(path, max_bytes=16 * 1024 * 1024):
    require(path.is_file() and path.stat().st_size <= max_bytes, "JSON proof input exceeds its size bound")
    return json.loads(path.read_text(encoding="utf-8"))


def millis(value):
    if value is None:
        return 0
    require(isinstance(value, str), "stored date is not a string")
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    require(parsed.tzinfo is not None, "stored date lacks a timezone")
    elapsed = parsed.astimezone(dt.timezone.utc) - dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)
    return elapsed.days * 86400000 + elapsed.seconds * 1000 + elapsed.microseconds // 1000


def iso_ms(value):
    return (dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc) + dt.timedelta(milliseconds=value)).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def usage_expected(db, clock, project):
    now = millis(clock)
    row = db.execute("SELECT body FROM usage_settings WHERE id=1").fetchone()
    require(row is not None, "legacy settings row is missing")
    settings = json.loads(row[0])
    since, next_reset = 0, None
    anchor = settings.get("weekly_anchor_at")
    if anchor is not None:
        since = millis(anchor)
        if now < since:
            since -= WEEK_MS
            next_reset = anchor
    since = max(since, millis(settings.get("manual_reset_at")), 0)
    selection = "(? IS NULL OR project=?)"
    count, requested, first, last = db.execute(
        "SELECT count(*),COALESCE(sum(mode_evidence='requested'),0),min(submitted_at),max(submitted_at) "
        "FROM pro_submissions WHERE submitted_ms>=? AND submitted_ms<=? AND " + selection,
        (since, now, project, project)).fetchone()
    all_time = db.execute("SELECT count(*) FROM pro_submissions WHERE submitted_ms<=? AND " + selection,
                          (now, project, project)).fetchone()[0]
    projects = [{"project_id": ident, "count": number} for ident, number in db.execute(
        "SELECT project,count(*) FROM pro_submissions WHERE submitted_ms>=? AND submitted_ms<=? AND " +
        selection + " GROUP BY project ORDER BY count(*) DESC,project", (since, now, project, project))]
    collectors = []
    for body, in db.execute("SELECT body FROM usage_collectors ORDER BY id"):
        value = json.loads(body)
        value["stale"] = now - millis(value.get("last_sync_at")) > 120000
        collectors.append(value)
    verified = settings.get("limits_verified_at")
    return {"schema_version": 1, "generated_at": clock, "settings": settings,
            "period_started_at": iso_ms(since) if since else None, "next_weekly_reset_at": next_reset,
            "reset_date_stale": anchor is None or now >= millis(anchor) or verified is None or now-millis(verified) > 600000,
            "count": count, "requested_mode_count": requested, "first_submission_at": first,
            "last_submission_at": last, "all_time_count": all_time,
            "projects": projects, "collectors": collectors, "coverage_note": COVERAGE_NOTE}


def sequenced(rows):
    output = []
    for sequence, body in rows:
        value = json.loads(body)
        require(isinstance(value, dict), "stored event body is not an object")
        value["sequence"] = sequence
        output.append(value)
    return output


def expected_reads(db, kind, clock):
    if kind == "usage":
        projects = [None] + [row[0] for row in db.execute(
            "SELECT DISTINCT project FROM pro_submissions WHERE project IS NOT NULL ORDER BY project")]
        receipts = [{"id": key, "payload": json.loads(payload), "result": json.loads(result)}
                    for key, payload, result in db.execute("SELECT id,payload,result FROM usage_requests ORDER BY id")]
        return {"snapshots": [{"project": project, "value": usage_expected(db, clock, project)} for project in projects],
                "history": sequenced(db.execute("SELECT sequence,body FROM usage_changes ORDER BY sequence DESC LIMIT 30")),
                "reset_receipts": receipts}
    projects = [None] + [row[0] for row in db.execute("SELECT DISTINCT project FROM inbox_items ORDER BY project")]
    listings = []
    for project in projects:
        items = [json.loads(row[0]) for row in db.execute(
            "SELECT body FROM inbox_items WHERE (? IS NULL OR project=?) ORDER BY status DESC,updated DESC,project,id",
            (project, project))]
        events = sequenced(db.execute(
            "SELECT sequence,body FROM inbox_events WHERE (? IS NULL OR project=?) ORDER BY sequence", (project, project)))
        listings.append({"project": project, "items": items, "events": events})
    receipts = [{"project": project, "id": ident, "request": request,
                 "payload": json.loads(payload), "result": json.loads(result)}
                for project, ident, request, payload, result in db.execute(
                    "SELECT project,id,request,payload,result FROM inbox_answers ORDER BY project,id,request")]
    return {"listings": listings, "answer_receipts": receipts}


def capture_metadata(kind, manifest_path):
    manifest_path = private_path(manifest_path)
    manifest = read_json(manifest_path, 2 * 1024 * 1024)
    require(manifest.get("schema_version") == 1 and manifest.get("kind") == kind,
            "capture manifest has the wrong kind or schema")
    require(manifest.get("source_sqlite_connections") == 0 and
            manifest.get("source_stable_through_copy_and_second_hash_pass") is True,
            "capture lacks the approved source-access proof")
    receipt = manifest["normalized_archive"]
    require(receipt.get("path") == kind + ".sqlite" and receipt.get("journal_mode") == "delete" and
            receipt.get("integrity_check") == "ok" and receipt.get("foreign_key_check") == "ok" and
            receipt.get("table_digests_match_raw_snapshot") is True, "capture was not fully normalized/verified")
    database = private_path(manifest_path.parent / receipt["path"])
    require(database.parent == manifest_path.parent and not (manifest_path.parent / receipt["path"]).is_symlink(),
            "published capture database escapes its directory")
    require(hash_readonly(database) == receipt["sha256"] and database.stat().st_size == receipt["bytes"],
            "published capture database differs from its manifest")
    require(not any(Path(str(database) + suffix).exists() for suffix in ("-wal", "-journal", "-shm")),
            "published normalized capture unexpectedly has a sidecar")
    return manifest_path, manifest, database


def prepare(usage_manifest, inbox_manifest, destination):
    require(not destination.exists() and not destination.is_symlink(), "proof destination already exists")
    destination = private_path(destination, exists=False)
    archives = {kind: capture_metadata(kind, file) for kind, file in (("usage", usage_manifest), ("inbox", inbox_manifest))}
    require(all(not destination.is_relative_to(path.parent) for path, _, _ in archives.values()),
            "proof must be separate from published captures")
    clock = max(manifest["completed_at"] for _, manifest, _ in archives.values())
    require(iso_ms(millis(clock)) == clock, "capture clock is not exact UTC milliseconds")
    destination.mkdir(mode=0o700, parents=True)
    plan = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "disposable_copy": True, "clock": clock, "stores": {}}
    expected = {"usage": None, "inbox": None}
    for kind, (manifest_path, manifest, database) in archives.items():
        directory = destination / kind
        directory.mkdir(mode=0o700)
        consumer = directory / (kind + ".sqlite")
        copied = copy_readonly(database, consumer, identity(database))
        require(copied == manifest["normalized_archive"]["sha256"], "disposable reader copy changed")
        with closing(sqlite3.connect(sqlite_uri(consumer, "mode=ro&immutable=1"), uri=True)) as db:
            db.execute("PRAGMA query_only=ON")
            require(inventory(db, kind) == manifest["inventory"], "consumer copy has different logical rows")
            expected[kind] = expected_reads(db, kind, clock)
        require(hash_readonly(consumer) == copied, "read preparation altered the disposable file")
        require(sum(value["rows"] for value in manifest["inventory"]["tables"].values()) <= 100000,
                "legacy proof exceeds the bounded row budget")
        plan["stores"][kind] = {"directory": str(directory), "database": str(consumer), "initial_sha256": copied,
                                "archive": str(database), "capture_manifest": str(manifest_path),
                                "capture_manifest_sha256": hash_readonly(manifest_path), "inventory": manifest["inventory"]}
    payload = json.dumps(expected, ensure_ascii=False, separators=(",", ":")).encode()
    require(len(payload) <= 16 * 1024 * 1024, "legacy read expectations exceed 16 MiB")
    write_json(destination / "expected.json", expected)
    plan["expected"] = str(destination / "expected.json")
    plan["expected_sha256"] = hash_readonly(destination / "expected.json")
    write_json(destination / "plan.json", plan)
    sync_directory(destination / "usage")
    sync_directory(destination / "inbox")
    sync_directory(destination)
    return {"plan": str(destination / "plan.json"), "clock": clock,
            "table_counts": {kind: {name: table["rows"] for name, table in store["inventory"]["tables"].items()}
                             for kind, store in plan["stores"].items()}}


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def verify(plan_path, result_path, output_path):
    plan_path, result_path = private_path(plan_path), private_path(result_path)
    root = plan_path.parent
    require(result_path.is_relative_to(root), "candidate evidence escapes its proof directory")
    plan = read_json(plan_path)
    require(plan.get("schema_version") == 1 and plan.get("disposable_copy") is True and plan.get("source_commit") == SOURCE_COMMIT,
            "invalid legacy reader plan")
    expected_path = private_path(Path(plan["expected"]))
    require(expected_path.is_relative_to(root) and hash_readonly(expected_path) == plan["expected_sha256"],
            "legacy expected values were altered")
    expected, result = read_json(expected_path), read_json(result_path, 32 * 1024 * 1024)
    require(result.get("schema_version") == 1 and result.get("clock") == plan["clock"], "candidate proof has wrong schema or clock")
    stores = {}
    for kind, store in plan["stores"].items():
        archive = private_path(Path(store["archive"]))
        manifest_path = private_path(Path(store["capture_manifest"]))
        require(hash_readonly(archive) == store["initial_sha256"] and hash_readonly(manifest_path) == store["capture_manifest_sha256"],
                "published capture or manifest changed during migration proof")
        consumer = private_path(Path(store["database"]))
        require(consumer.is_relative_to(root) and consumer != archive, "candidate is not a separate disposable database")
        # Go has closed all stores. A private, read-only connection may read a
        # remaining WAL without modifying original/captured state.
        with closing(sqlite3.connect(sqlite_uri(consumer, "mode=ro"), uri=True, timeout=5)) as db:
            db.execute("PRAGMA query_only=ON")
            logical = inventory(db, kind)
            require(logical == store["inventory"], "Go startup/read/replay changed legacy schema or logical rows")
            require(db.execute("PRAGMA integrity_check").fetchall() == [("ok",)], "Go consumer failed integrity_check")
        actual = result.get(kind)
        matched = canonical(actual) == canonical(expected[kind])
        stores[kind] = {"read_dtos_match": matched, "logical_rows_and_schema_unchanged": True,
                        "archive_unchanged": True, "archive_sha256": store["initial_sha256"],
                        "tables": {name: table["rows"] for name, table in logical["tables"].items()},
                        "expected_sha256": hashlib.sha256(canonical(expected[kind]).encode()).hexdigest(),
                        "actual_sha256": hashlib.sha256(canonical(actual).encode()).hexdigest()}
        if not matched:
            # Keep detailed DTO contents private in a separately named proof
            # artifact; aggregate report is safe for repository evidence.
            import difflib
            difference = "\n".join(difflib.unified_diff(json.dumps(expected[kind], ensure_ascii=False, sort_keys=True, indent=2).splitlines(),
                                                        json.dumps(actual, ensure_ascii=False, sort_keys=True, indent=2).splitlines(),
                                                        fromfile="archived-storage-projection", tofile="Go-public-readers"))
            detail = root / (kind + "-difference.json")
            write_json(detail, {"diff": difference[:128 * 1024]})
            stores[kind]["private_difference"] = detail.name
    passed = all(store["read_dtos_match"] for store in stores.values())
    output_path = private_path(output_path, exists=False)
    require(output_path.is_relative_to(root) and not output_path.exists(), "report must be a new private proof file")
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "lane": "legacy-usage-inbox-native-readers",
              "passed": passed, "clock": plan["clock"], "stores": stores,
              "candidate_evidence_sha256": hash_readonly(result_path),
              "scope": "Existing native schema copied and read through Go Snapshot/History/List/Events; all legacy reset and answer receipts replayed; every schema/table row digest unchanged. No live SQLite connections or writes."}
    write_json(output_path, report)
    return {"passed": passed, "output": str(output_path), "stores": stores}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    p = commands.add_parser("prepare")
    p.add_argument("--usage-manifest", type=Path, required=True)
    p.add_argument("--inbox-manifest", type=Path, required=True)
    p.add_argument("--destination", type=Path, required=True)
    v = commands.add_parser("verify")
    v.add_argument("--plan", type=Path, required=True)
    v.add_argument("--candidate", type=Path, required=True)
    v.add_argument("--output", type=Path, required=True)
    options = parser.parse_args()
    result = prepare(options.usage_manifest, options.inbox_manifest, options.destination) if options.command == "prepare" else verify(options.plan, options.candidate, options.output)
    print(json.dumps(result, sort_keys=True))
    if result.get("passed") is False:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
