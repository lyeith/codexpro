#!/usr/bin/env python3
"""Read/resume an actual private TypeScript batch definition in the Go build.

No Bash commands or jobs are submitted. The only server-side persistence is the
reference's new three-read recipe. Both resumed copies use fresh custom names,
so reading them does not run automatic batch retention or modify Git excludes.
Run sequentially with other private mutation lanes, under nice10/timeout120s.
"""
from __future__ import annotations

import argparse
import datetime as dt
import difflib
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import stat
import uuid

from contract_suite import SOURCE_COMMIT, raw_result
from process_contract_suite import Side, require

LIMIT = 2 * 1024 * 1024


def bounded_regular(path, limit=LIMIT):
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    with os.fdopen(os.open(path, flags), "rb") as stream:
        before = os.fstat(stream.fileno())
        require(stat.S_ISREG(before.st_mode) and before.st_size <= limit,
                "batch source must be a bounded regular file")
        body = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
        signature = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        require(len(body) <= limit and signature(before) == signature(after),
                "batch source changed during bounded read")
        require(signature(path.stat(follow_symlinks=False)) == signature(after),
                "batch source path changed during bounded read")
        return body


def validate_definition(body, expected):
    require(len(body) <= LIMIT, "batch definition exceeds proof budget")
    definition = json.loads(body)
    require(definition == expected, "persisted reference definition differs from the submitted read-only recipe")
    require(set(definition) == {"version", "mode", "continue_on_error", "operations"}
            and definition["version"] == 1 and definition["mode"] == "serial"
            and definition["continue_on_error"] is False,
            "unsupported persisted definition envelope")
    operations = definition["operations"]
    require(len(operations) == 3 and len({operation["id"] for operation in operations}) == 3,
            "proof requires three distinct stored operations")
    for operation in operations:
        require(set(operation) == {"id", "tool", "args"} and operation["tool"] == "read"
                and set(operation["args"]) == {"path"}, "proof refuses an executable or credential-bearing recipe")
        require(isinstance(operation["args"]["path"], str)
                and not Path(operation["args"]["path"]).is_absolute(), "stored read path must be relative")
    return definition


def write_new(path, body):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())
    directory = os.open(path.parent, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def copy_definition(source, destination, expected):
    require(not source.is_symlink() and not destination.exists() and not destination.is_symlink(),
            "batch proof refuses source symlinks or existing destinations")
    body = bounded_regular(source)
    validate_definition(body, expected)
    write_new(destination, body)
    require(bounded_regular(destination) == body, "private batch copy differs from source bytes")
    return {"bytes": len(body), "sha256": hashlib.sha256(body).hexdigest(),
            "operations": len(expected["operations"])}


def verify_resume(result, start):
    require(not result.get("isError"), "resumed read-only definition returned an error")
    data = result["structuredContent"]
    require(data["batch_source"] == "file" and data["persisted"] is True
            and data["auto_stored"] is False and data["mutating"] is False,
            "custom stored recipe changed execution or persistence attribution")
    require(data["start_index"] == start and data["total_operation_count"] == 3
            and data["operation_count"] == 3 - start and data["executed_operation_count"] == 3 - start
            and data["succeeded_count"] == 3 - start and data["failed_count"] == 0 and data["skipped_count"] == 0,
            "existing-definition resume counts or original indices changed")
    require([row["index"] for row in data["results"]] == list(range(start, 3))
            and [row["id"] for row in data["results"]] == ["read-" + str(index) for index in range(start, 3)],
            "existing-definition resume lost source operation identities")


def call_recipe(side, label, path, selector):
    # Side.call's process-admission helper intentionally permits only observed
    # automatic recipe paths. This proof submits a validated read-only custom
    # copy directly and records its result without adopting any job identity.
    args = {"workspace_id": side.workspace, "path": path, **selector}
    response = side.client.call("batch", args)
    side.records.append({"scenario": side.scenario, "step": label, "tool": "batch",
                         "arguments": args, "response": response})
    require(response["http_status"] == 200 and "result" in response["body"], "stored recipe failed at transport boundary")
    result = raw_result(response)
    side.observe(result)
    return response, result


def compare_responses(reference, candidate, left, right):
    def normalized(side, response):
        return side.normalize({"http_status": response["http_status"], "content_type": response["content_type"],
                               "result": raw_result(response)})
    left, right = normalized(reference, left), normalized(candidate, right)
    if left == right:
        return {"status": "PASS"}
    difference = list(difflib.unified_diff(json.dumps(left, sort_keys=True, ensure_ascii=False, indent=2).splitlines(),
                     json.dumps(right, sort_keys=True, ensure_ascii=False, indent=2).splitlines(),
                     fromfile="typescript", tofile="go", lineterm=""))
    return {"status": "FAIL", "diff": difference[:200], "diff_truncated": len(difference) > 200}


def prove(reference, candidate):
    fixtures = {"a.txt": "existing definition A\n", "b.txt": "existing definition B\n", "c.txt": "existing definition C\n"}
    for side in (reference, candidate):
        side.begin("batch-state-migration", fixtures)
    operations = [{"id": "read-" + str(index), "tool": "read", "args": {"path": reference.path(name)}}
                  for index, name in enumerate(fixtures)]
    require([candidate.path(name) for name in fixtures] == [operation["args"]["path"] for operation in operations],
            "private fixture relative paths differ")
    expected = {"version": 1, "mode": "serial", "continue_on_error": False, "operations": operations}
    created = reference.call("create-reference-definition", "batch", {"operations": operations, "persist": True})
    data = reference.data(created)
    path = data.get("batch_path")
    require(data["persisted"] is True and data["auto_stored"] is True
            and isinstance(path, str) and re.fullmatch(r"\.codexpro-batches/[a-fA-F0-9]{4}\.json", path),
            "reference did not persist an automatic definition")
    source = reference.private_file(path)
    before = bounded_regular(source)
    validate_definition(before, expected)
    copied = []
    for side in (reference, candidate):
        destination = side.private_file(side.path("reference-definition.json"))
        require(destination.resolve().is_relative_to(side.fixture.resolve()), "recipe copy left nonce-owned fixture")
        copied.append({"side": side.label, **copy_definition(source, destination, expected)})
    require(copied[0]["sha256"] == copied[1]["sha256"], "copied recipe hashes differ")
    checks = []
    for label, selector, start in (("resume-by-id", {"from": "read-1"}, 1),
                                   ("resume-by-index", {"from_index": 2}, 2)):
        values = []
        for side in (reference, candidate):
            response, result = call_recipe(side, label, side.path("reference-definition.json"), selector)
            verify_resume(result, start)
            values.append(response)
        checks.append({"check": label, **compare_responses(reference, candidate, *values)})
    require(bounded_regular(source) == before, "reference source definition changed during proof")
    for side in (reference, candidate):
        require(bounded_regular(side.private_file(side.path("reference-definition.json"))) == before,
                "resuming changed the copied definition bytes")
    return {"source_definition": {"path": path, "sha256": hashlib.sha256(before).hexdigest(), "bytes": len(before), "operations": 3},
            "copies": copied, "checks": checks, "bash_calls": 0, "job_adoptions": 0,
            "passed": all(check["status"] == "PASS" for check in checks)}


def output_path(value):
    path = Path(value).expanduser().resolve()
    homes = {Path.home().resolve(), Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()}
    protected = [(home / suffix).resolve() for home in homes for suffix in
                 (".codexpro", ".config/codexpro", "Projects/codexpro", "apps/codexpro")]
    require(not path.exists() and not any(path.is_relative_to(root) for root in protected),
            "proof output must be fresh and outside protected live paths")
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for side, port in (("reference", 7211), ("candidate", 7210)):
        parser.add_argument("--" + side, default="http://127.0.0.1:" + str(port) + "/mcp")
        parser.add_argument("--" + side + "-token", required=True)
        parser.add_argument("--" + side + "-root", required=True)
    parser.add_argument("--project", default="contract")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    require(Path(args.reference_root).resolve() != Path(args.candidate_root).resolve(), "proof roots must differ")
    output = output_path(args.output)
    if os.getpriority(os.PRIO_PROCESS, 0) < 10:
        os.nice(10 - os.getpriority(os.PRIO_PROCESS, 0))
    run = uuid.uuid4().hex[:16]
    sides = []
    report = {"schema_version": 1, "lane": "existing-reference-batch-definition", "source_commit": SOURCE_COMMIT,
              "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(), "run_id": run, "passed": False}
    try:
        sides.append(Side("reference", args.reference, args.reference_token, args.reference_root, args.project, run))
        sides.append(Side("candidate", args.candidate, args.candidate_token, args.candidate_root, args.project, run))
        report.update(prove(*sides))
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        for side in sides:
            report[side.label + "_records"] = side.records
            side.client.assert_no_credential(report)
            side.client.close()
        output.parent.mkdir(parents=True, exist_ok=True)
        write_new(output, (json.dumps(report, ensure_ascii=False, indent=2) + "\n").encode())
    print(json.dumps({"passed": report["passed"], "output": str(output), "checks": report.get("checks", [])}), flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
