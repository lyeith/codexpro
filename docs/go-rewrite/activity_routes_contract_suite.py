#!/usr/bin/env python3
"""Paired saved-batch and diff route supplement over an already owned pair.

No server lifecycle is adopted. Exact manifests, PID/start/listener identities
and a nonce MCP file read bind both private fixtures before any mutation. The
passed 104-case telemetry runner is imported for its client/guards, not changed
or rerun. Full credential-free HTTP bodies remain in private raw evidence.
"""
from __future__ import annotations

import argparse
import copy
import difflib
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time
import urllib.parse
import uuid

from contract_suite import SOURCE_COMMIT, raw_result
from managed_contract_suite import sanitized_environment
from telemetry_contract_suite import (
    Lane, RawEvidence, Side, canonical_json, iso, path_allowed, require,
)

MAX_FILE = 2 * 1024 * 1024
MAX_JOURNAL = 24 * 1024 * 1024
INJECTION_TAG = "sp6-render-proof"
PAYLOAD = '<sp6-render-proof data-value="quoted">& apostrophe\'</sp6-render-proof>'


def sha(body):
    return hashlib.sha256(body).hexdigest()


def file_signature(value):
    return (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns)


def stable_file(path, limit=MAX_FILE):
    path = Path(path)
    with os.fdopen(os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)), "rb") as stream:
        before = os.fstat(stream.fileno())
        require(stat.S_ISREG(before.st_mode) and before.st_size <= limit,
                "proof source must be a bounded regular file")
        body = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
    require(len(body) <= limit and file_signature(before) == file_signature(after)
            == file_signature(path.stat(follow_symlinks=False)), "proof source changed during read")
    return body, {"bytes": len(body), "sha256": sha(body),
                  "mtime_ns": after.st_mtime_ns, "ctime_ns": after.st_ctime_ns,
                  "device": after.st_dev, "inode": after.st_ino}


def create_file(path, body):
    require(isinstance(body, bytes), "fixture writes must be explicit bytes")
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600), "wb") as stream:
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())


class RenderedHTML(HTMLParser):
    """Visible-text/code/link projection; script/style source stays in raw HTML."""
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.skip = []
        self.text, self.pre, self.headings, self.links, self.tags, self.scripts = [], [], [], [], [], []
        self.active_pre = self.active_heading = None

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        self.tags.append((tag, values))
        if tag in ("script", "style"):
            self.skip.append(tag)
            if tag == "script":
                self.scripts.append(values)
        if self.skip:
            return
        if tag == "pre":
            require(self.active_pre is None, "nested preformatted block")
            self.active_pre = []
            self.text.append(" [preformatted-block-" + str(len(self.pre)) + "] ")
        if re.fullmatch(r"h[1-6]", tag):
            self.active_heading = [tag, []]
        if tag == "a":
            self.links.append(values.get("href", ""))
        if tag in ("br", "div", "section", "article", "p", "tr", "li", "td", "th", "pre", "summary"):
            self.text.append(" ")

    def handle_endtag(self, tag):
        if self.skip:
            if tag == self.skip[-1]:
                self.skip.pop()
            return
        if tag == "pre" and self.active_pre is not None:
            self.pre.append("".join(self.active_pre).replace("\r\n", "\n"))
            self.active_pre = None
        if self.active_heading is not None and tag == self.active_heading[0]:
            self.headings.append([tag, " ".join("".join(self.active_heading[1]).split())])
            self.active_heading = None
        if tag not in ("span", "code", "strong", "em", "b", "i"):
            self.text.append(" ")

    def handle_data(self, value):
        if self.skip:
            return
        if self.active_pre is not None:
            self.active_pre.append(value)
            return
        self.text.append(value)
        if self.active_heading is not None:
            self.active_heading[1].append(value)

    def projection(self):
        require(self.active_pre is None and not self.skip, "unterminated page content")
        require(not any(tag == INJECTION_TAG for tag, _ in self.tags),
                "caller-controlled evidence became an executable/structural HTML element")
        require(not any(any(key.lower().startswith("on") for key in attrs) and
                        INJECTION_TAG in canonical_json(attrs) for _, attrs in self.tags),
                "caller-controlled evidence became an event handler")
        return {"visible_text": " ".join("".join(self.text).split()),
                "preformatted_blocks": self.pre, "headings": self.headings, "links": self.links}


def rendered_projection(side, response, *, kind, page=False):
    require(response["http_status"] == 200 and isinstance(response["body"], str),
            "dashboard success did not return HTML")
    require(response["headers"].get("content-type") == "text/html; charset=utf-8",
            "dashboard HTML content type changed")
    parsed = RenderedHTML()
    parsed.feed(response["body"])
    parsed.close()
    projection = parsed.projection()
    visible = projection["visible_text"]
    if kind == "batch":
        definition = getattr(side, "display_definition", side.definition)
        require(all(operation["id"] in visible and operation["tool"] in visible
                    for operation in definition["operations"]),
                "saved batch viewer omitted a persisted operation identity")
        decoded = [json.loads(block) for block in projection["preformatted_blocks"]]
        expected = [operation["args"] for operation in definition["operations"]] + [definition]
        require(canonical_json(decoded) == canonical_json(expected),
                "saved batch viewer changed arguments or raw definition JSON")
        # The source displays parsed JSON; insignificant key ordering and
        # indentation are represented as full JSON values, never field drops.
        projection["preformatted_blocks"] = [{"json": value} for value in decoded]
        if page:
            require(side.display_path in visible and side.project_label in visible and side.project in visible,
                    "saved batch page lost exact recipe/project attribution")
            require(side.display_workspace in visible, "saved batch page lost the direct workspace display identity")
            require(["h2", "Operations"] in projection["headings"], "saved batch page omitted Operations heading")
        require("&lt;" in response["body"] and INJECTION_TAG not in [tag for tag, _ in parsed.tags],
                "saved batch argument HTML escaping was not exercised")
        require(any(tag == "html" for tag, _ in parsed.tags) is page,
                "full-page versus fragment representation changed")
    else:
        require(side.tracked_path in visible and PAYLOAD in visible,
                "diff fragment omitted the controlled tracked path or changed content")
        require("&lt;" in response["body"], "diff HTML escaping was not exercised")
    return {"http_status": response["http_status"], "headers": side.normalize(response["headers"]),
            "comparison_scope": "complete non-code visible text/headings/links plus full JSON code values or exact non-JSON code text; raw HTML retained",
            "rendered": side.normalize(projection)}


class ActivitySide(Side):
    def __init__(self, label, manifest_path, run_id, deadline):
        self.deadline = deadline
        manifest = json.loads(Path(manifest_path).read_text())
        require(manifest.get("source_commit") == SOURCE_COMMIT, "launch manifest is not bound to the pinned source")
        endpoint = "http://127.0.0.1:" + str(7211 if label == "reference" else 7210) + "/mcp"
        super().__init__(label, endpoint, manifest["token_file"], manifest_path, "activity-routes-" + run_id)
        self.client.timeout = 8
        self.current_case = "setup"

    def check_time(self, allowance=8):
        require(time.monotonic() + allowance < self.deadline, "activity supplement reached its bounded deadline")

    def mcp(self, name, args):
        self.check_time()
        return super().mcp(name, args)

    def http(self, *args, **kwargs):
        self.check_time(25)
        return super().http(*args, **kwargs)

    def private_file(self, relative):
        require(isinstance(relative, str) and relative and not Path(relative).is_absolute()
                and ".." not in Path(relative).parts, "fixture path is not a safe relative path")
        result = self.root / relative
        require(result.resolve(strict=False).is_relative_to(self.root) and not result.is_symlink(),
                "fixture path escaped the explicit private root")
        return result

    def git(self, *args, expected=0):
        self.check_time(15)
        env = dict(sanitized_environment(), GIT_AUTHOR_NAME="SP-6 fixture", GIT_AUTHOR_EMAIL="sp6-fixture@example.invalid",
                   GIT_COMMITTER_NAME="SP-6 fixture", GIT_COMMITTER_EMAIL="sp6-fixture@example.invalid",
                   GIT_AUTHOR_DATE="2026-10-31T00:00:00+00:00", GIT_COMMITTER_DATE="2026-10-31T00:00:00+00:00")
        command = ["nice", "-n", "10", "git", "-C", str(self.root), "-c", "core.hooksPath=/dev/null",
                   "-c", "commit.gpgSign=false", *args]
        result = subprocess.run(command, env=env, capture_output=True, timeout=15)
        require(len(result.stdout) + len(result.stderr) <= MAX_FILE and result.returncode == expected,
                "bounded private Git fixture command failed: " + " ".join(args[:2]))
        return result.stdout.decode("utf-8")

    def source_files(self):
        source = self.manifest["audit"]
        path = path_allowed(source["path"])
        if source["format"] == "jsonl":
            return [path]
        require(path.is_dir(), "private audit segment directory is missing")
        return sorted([*path.glob("segment-*.jsonl"), *path.glob("compact-*.jsonl")])

    def journal(self, wait_for_job=False):
        deadline = min(self.deadline - 25, time.monotonic() + 5)
        while True:
            files, rows, total = [], [], 0
            for path in self.source_files():
                body, detail = stable_file(path, MAX_JOURNAL)
                total += len(body)
                require(total <= MAX_JOURNAL, "private journal exceeds the bounded supplement budget")
                require(body.endswith(b"\n"), "journal observation caught an incomplete record")
                rows.extend(json.loads(line) for line in body.splitlines() if line)
                files.append({"name": path.name, **detail})
            receipts = [row for row in rows if row.get("tool_name") == "bash_job"
                        and row.get("request_metadata", {}).get("job_id") == getattr(self, "sentinel", None)]
            if not wait_for_job or len(receipts) == 1:
                require(not wait_for_job or receipts[0].get("status") == "succeeded",
                        "owned terminal job receipt has the wrong outcome")
                return {"files": files, "retained_records": len(rows), "bytes": total,
                        "owned_completion_receipts": len(receipts)}
            require(time.monotonic() < deadline, "owned completion has no unique durable audit record")
            time.sleep(.05)

    def filesystem_witness(self):
        tracked = [self.private_file(path) for path in (self.recipe_path, self.custom_recipe, self.tracked_path,
                   self.prefix + "/invalid.json", self.prefix + "/bad-version.json")]
        tracked += [self.root / ".git" / "index", self.root / ".git" / "info" / "exclude"]
        return {str(path.relative_to(self.root)): stable_file(path)[1] for path in tracked}


def stable_review_baseline(side):
    # Analyze's first response for changed inventory has cache.hit=false; the
    # second has cache.hit=true. Warm without changing the review checkpoint,
    # then retain the complete response for exact same-session comparison.
    arguments = {"workspace_id": side.workspace, "since": "last_shown",
                 "include_diff": True, "mark_reviewed": False}
    side.data(side.mcp("show_changes", arguments))
    response = side.mcp("show_changes", arguments)
    side.data(response)
    return copy.deepcopy(raw_result(response))


def prepare(sides):
    for side in sides:
        require(Path(side.git("rev-parse", "--show-toplevel").strip()).resolve() == side.root,
                "fixture is not its own Git repository")
        require((side.root / ".git").is_dir() and not (side.root / ".git").is_symlink(),
                "supplement needs an independent fixture Git directory")
        require(side.git("diff", "--cached", "--name-only") == "", "fixture has prior staged changes")
        names = ["first.txt", '<sp6-render-proof data-value="path">&\'.txt', "third.txt"]
        for index, name in enumerate(names):
            create_file(side.fixture / name, ("batch read " + str(index) + "\n").encode())
        side.definition = {"version": 1, "mode": "serial", "continue_on_error": False,
            "operations": [{"id": "read-" + str(index), "tool": "read", "args": {"path": side.prefix + "/" + name}}
                           for index, name in enumerate(names)]}
        response = side.mcp("batch", {"workspace_id": side.workspace,
            "operations": side.definition["operations"], "persist": True})
        data = side.data(response)
        path = data.get("batch_path")
        require(data.get("persisted") is True and data.get("auto_stored") is True
                and re.fullmatch(r"\.codexpro-batches/[A-Fa-f0-9]{4}\.json", path or ""),
                "reference/candidate failed to create an owned version-one saved batch")
        body, _ = stable_file(side.private_file(path))
        require(json.loads(body) == side.definition, "persisted definition differs from the exact submitted reads")
        side.created_recipe = path
    reference = sides[0]
    body, _ = stable_file(reference.private_file(reference.created_recipe))
    for side in sides:
        side.recipe_path = reference.created_recipe
        if side.created_recipe != side.recipe_path:
            original = side.private_file(side.created_recipe)
            require(json.loads(stable_file(original)[0]) == side.definition,
                    "owned candidate recipe changed before its private fixture replacement")
            create_file(side.private_file(side.recipe_path), body)
            original.unlink()
        else:
            require(stable_file(side.private_file(side.recipe_path))[0] == body,
                    "same-path persisted recipe does not match reference bytes")
        side.custom_recipe = side.prefix + "/reference-definition.json"
        create_file(side.private_file(side.custom_recipe), body)
        create_file(side.fixture / "invalid.json", b"{\n")
        bad = {**side.definition, "version": 2}
        create_file(side.fixture / "bad-version.json", (json.dumps(bad) + "\n").encode())
        side.tracked_path = side.prefix + "/tracked-<sp6-render-proof>&.txt"
        target = side.private_file(side.tracked_path)
        create_file(target, b"original tracked line\n")
        side.git("add", "--", side.tracked_path)
        side.git("commit", "--quiet", "--only", "-m", "test: seed owned activity diff fixture", "--", side.tracked_path)
        side.data(side.mcp("show_changes", {"workspace_id": side.workspace, "include_diff": True, "mark_reviewed": True}))
        # This one tracked-file edit belongs solely to this nonce fixture.
        with target.open("ab") as stream:
            stream.write((PAYLOAD + "\n").encode())
            stream.flush()
            os.fsync(stream.fileno())
        side.review_before = stable_review_baseline(side)
        require(side.tracked_path in canonical_json(side.review_before) and INJECTION_TAG in canonical_json(side.review_before),
                "tracked fixture change is not visible from the original review checkpoint")
    require(sides[0].definition == sides[1].definition, "paired saved recipe requests differ")


def batch_url(side, path=None, *, fragment=False, project=None, workspace=True):
    pairs = [("project_id", side.project if project is None else project), ("path", side.recipe_path if path is None else path)]
    if workspace:
        pairs.append(("workspace_id", side.workspace if workspace is True else workspace))
    if fragment:
        pairs.append(("fragment", "1"))
    return "/activity/batch?" + urllib.parse.urlencode(pairs)


def run_cases(lane):
    lane.phase = "activity-routes"
    def start(side):
        data = side.data(side.mcp("start_jobs", {"workspace_id": side.workspace,
            "commands": [{"command": "printf 'activity-route-sentinel\\n'", "cwd": side.prefix}]}))
        jobs = data.get("jobs", [])
        require(len(jobs) == 1 and re.fullmatch(r"job_[a-f0-9]{8}", jobs[0].get("job_id", "")),
                "sentinel launch did not produce one owned compact ID")
        side.sentinel = jobs[0]["job_id"]
        side.owned_jobs.add(side.sentinel)
        side.jobs[side.sentinel] = "<owned-activity-sentinel>"
        data = side.data(side.mcp("jobs", {"workspace_id": side.workspace, "job_ids": [side.sentinel],
            "output": "none", "wait_ms": 5000}))
        rows = data.get("jobs", [])
        require(len(rows) == 1 and rows[0]["job_id"] == side.sentinel and rows[0]["status"] == "succeeded"
                and rows[0].get("exit_code") == 0, "owned bounded sentinel did not settle successfully")
        side.before_journal = side.journal(wait_for_job=True)
        side.before_files = side.filesystem_witness()
        return {"owned_completion_ready": True, "exit_code": 0}
    lane.check("prepare-unacknowledged-owned-completion", start, normalize=lambda s, v: v)
    require(all(row["passed"] for row in lane.cases), "owned job precondition failed")

    for label, getter, status in (
        ("batch-missing-arguments", lambda s: "/activity/batch", 400),
        ("batch-missing-path", lambda s: "/activity/batch?" + urllib.parse.urlencode([("project_id", s.project)]), 400),
        ("batch-unknown-project", lambda s: batch_url(s, project="sp6-missing-project"), 404),
        ("batch-project-too-long", lambda s: batch_url(s, project="p" * 161), 400),
        ("batch-path-too-long", lambda s: batch_url(s, "p" * 4097), 400),
        ("batch-missing-file", lambda s: batch_url(s, s.prefix + "/missing.json"), 404),
        ("batch-traversal-refused", lambda s: batch_url(s, "../outside-private-workspace.json"), 400),
        ("batch-invalid-json", lambda s: batch_url(s, s.prefix + "/invalid.json"), 400),
        ("diff-missing-project", lambda s: "/activity/diff", 404),
        ("diff-unknown-project", lambda s: "/activity/diff?project_id=sp6-missing-project", 404),
    ):
        lane.check(label, lambda s, getter=getter: s.http("GET", getter(s)), expected_status=status)

    for label, custom, fragment, auth in (
        ("saved-batch-full-page", False, False, "bearer"),
        ("saved-batch-fragment", False, True, "bearer"),
        ("saved-batch-query-auth-fragment", False, True, "query"),
        ("custom-saved-batch-page", True, False, "bearer"),
    ):
        def request(side, custom=custom, fragment=fragment, auth=auth):
            side.display_path = side.custom_recipe if custom else side.recipe_path
            side.display_workspace = side.workspace
            side.display_definition = side.definition
            return side.http("GET", batch_url(side, side.display_path, fragment=fragment), auth=auth)
        lane.check(label, request, normalize=lambda s, v, fragment=fragment:
                   rendered_projection(s, v, kind="batch", page=not fragment), expected_status=200)
    def unknown_version(side):
        # loadBatchDefinition intentionally parses any JSON for the viewer;
        # execution's strict version/tool gate is not part of this GET.
        side.display_path = side.prefix + "/bad-version.json"
        side.display_workspace = side.workspace
        side.display_definition = {**side.definition, "version": 2}
        return side.http("GET", batch_url(side, side.display_path, fragment=True))
    lane.check("saved-batch-unknown-version-is-viewable", unknown_version,
               normalize=lambda s, v: rendered_projection(s, v, kind="batch"), expected_status=200)
    for label, selected in (("saved-batch-direct-without-workspace", False),
                            ("saved-batch-direct-display-workspace", "sp6-display-only-workspace")):
        def request(side, selected=selected):
            side.display_path = side.recipe_path
            side.display_workspace = "activity_" + side.project if selected is False else selected
            side.display_definition = side.definition
            return side.http("GET", batch_url(side, workspace=selected))
        lane.check(label, request, normalize=lambda s, v: rendered_projection(s, v, kind="batch", page=True), expected_status=200)
    for label in ("controlled-tracked-diff", "repeated-tracked-diff"):
        lane.check(label, lambda s: s.http("GET", "/activity/diff?" + urllib.parse.urlencode([("project_id", s.project)])),
            normalize=lambda s, v: rendered_projection(s, v, kind="diff"), expected_status=200)

    def readonly(side):
        require(side.journal() == side.before_journal, "dashboard GET appended/rewrote retained audit records")
        require(side.filesystem_witness() == side.before_files,
                "dashboard GET changed recipe contents/timestamps, tracked file, Git index or excludes")
        return {"journal_unchanged": True, "recipe_content_and_metadata_unchanged": True,
                "git_index_and_file_bytes_unchanged": True}
    lane.check("http-readonly-storage-witnesses", readonly, normalize=lambda s, v: v)
    for first in (True, False):
        def digest_read(side, first=first):
            data = side.data(side.mcp("read", {"workspace_id": side.workspace,
                "path": side.prefix + "/binding.txt", "start_line": 1, "end_line": 1}))
            rows = [row for row in data.get("background_jobs", []) if row.get("job_id") == side.sentinel]
            require(bool(rows) is first, "dashboard acknowledged the completion or ordinary digest failed its one-shot contract")
            if first:
                require(len(rows) == 1 and rows[0]["status"] == "succeeded" and rows[0]["exit_code"] == 0,
                        "owned completion digest lost its terminal outcome")
            return {"owned_completion_visible": bool(rows)}
        lane.check("completion-visible-after-drawers" if first else "completion-acknowledged-by-normal-read",
                   digest_read, normalize=lambda s, v: v)
    def review(side):
        response = side.mcp("show_changes", {"workspace_id": side.workspace, "since": "last_shown",
            "include_diff": True, "mark_reviewed": False})
        require(canonical_json(raw_result(response)) == canonical_json(side.review_before),
                "dashboard changed the MCP session's previous Git review checkpoint or diff response")
        # The source inventory cache key includes real filesystem timestamps.
        # Keep complete same-side equality; preserve each side's digest rather
        # than equating independently seeded cache identities across machines.
        side.review_witness = {"before_sha256": sha(canonical_json(side.review_before).encode()),
            "after_sha256": sha(canonical_json(raw_result(response)).encode()),
            "complete_same_session_response_unchanged": True}
        side.record({"transport": "local-proof", "proof": "same-session-review", **side.review_witness})
        return {"complete_same_session_review_response_unchanged": True}
    lane.check("git-review-checkpoint-unchanged", review, normalize=lambda s, v: v)


def safe_error(error, sides):
    text = type(error).__name__ + ": " + str(error)
    for side in sides:
        client = getattr(side, "client", None)
        if client:
            text = text.replace(client.credential, "<credential>")
            if client.session:
                text = text.replace(client.session, "<session>")
    return text


def cleanup_side(side):
    """Stop only observed owned IDs, verify settlement, always attempt close."""
    errors = []
    try:
        owned = sorted(getattr(side, "owned_jobs", set()))
        if owned:
            response = side.client.call("stop_jobs", {"workspace_id": side.workspace, "job_ids": owned})
            result = raw_result(response)
            require(result.get("isError") is not True, "owned sentinel cleanup returned an error")
            rows = result.get("structuredContent", {}).get("jobs", [])
            require(sorted(row.get("job_id", "") for row in rows) == owned
                    and all(row.get("status") in {"succeeded", "failed", "stopped", "timed_out"} for row in rows),
                    "owned sentinel cleanup did not prove exact terminal settlement")
    except Exception as error:
        errors.append(safe_error(error, [side]))
    finally:
        try:
            side.client.close()
        except Exception as error:
            errors.append(safe_error(error, [side]))
    return errors


def run_existing_pair(reference_manifest, candidate_manifest, output):
    output = path_allowed(output, allow_missing=True)
    require(not output.exists(), "supplement evidence directory must be new")
    output.mkdir(parents=True, mode=0o700)
    run_id, deadline = uuid.uuid4().hex, time.monotonic() + 150
    sides, evidence, lane = [], None, None
    report = {"schema_version": 1, "lane": "saved-batch-and-diff-route-supplement",
              "source_commit": SOURCE_COMMIT, "started_at": iso(), "run_id": run_id,
              "cases": [], "cleanup_errors": [], "servers_started_or_stopped": 0}
    try:
        for label, path in (("reference", reference_manifest), ("candidate", candidate_manifest)):
            side = ActivitySide.__new__(ActivitySide)
            sides.append(side)
            side.__init__(label, path, run_id, deadline)
        require(sides[0].root != sides[1].root and sides[0].state != sides[1].state,
                "paired fixtures/state are not isolated")
        report["instances"] = [{"side": side.label, "manifest": str(path),
            "manifest_sha256": sha(stable_file(Path(path))[0]), "pid": side.pid,
            "process_start_ticks": side.manifest["process_start_ticks"], "port": side.manifest["port"],
            "project_id": side.project, "workspace_id": side.workspace, "fixture_root": str(side.root),
            "nonce_relative_path": side.prefix + "/binding.txt",
            "nonce_sha256": stable_file(side.fixture / "binding.txt")[1]["sha256"],
            "mcp_nonce_echo_and_hash_verified": True}
            for side, path in zip(sides, (reference_manifest, candidate_manifest))]
        evidence = RawEvidence(output / "raw.jsonl.gz", [side.client for side in sides])
        for side in sides:
            side.set_evidence(evidence)
        lane = Lane(sides)
        prepare(sides)
        report["recipe"] = {"path": sides[0].recipe_path,
            "sha256": stable_file(sides[0].private_file(sides[0].recipe_path))[1]["sha256"],
            "version": 1, "source": "actual reference batch persist response; exact bytes copied into candidate fixture"}
        run_cases(lane)
    except Exception as error:
        report["fatal_error"] = safe_error(error, sides)
    finally:
        if lane is not None:
            report["cases"] = lane.cases
        report["review_witnesses"] = {side.label: side.review_witness for side in sides
                                      if getattr(side, "review_witness", None)}
        for side in sides:
            if not getattr(side, "client", None):
                continue
            report["cleanup_errors"].extend(cleanup_side(side))
        if evidence:
            evidence.close()
            report["raw_evidence"] = {"path": str(evidence.path), "records": evidence.records,
                "uncompressed_bytes": evidence.bytes, "sha256": sha(evidence.path.read_bytes())}
        report["finished_at"] = iso()
        report["unchanged"] = sum(row["passed"] for row in report["cases"])
        report["failed"] = len(report["cases"]) - report["unchanged"]
        report["passed"] = (not report.get("fatal_error") and not report["failed"]
                            and len(report["cases"]) == 24 and not report["cleanup_errors"])
        report["comparison_scope"] = "HTTP status/selected headers/error text; complete non-code visible text/headings/links and full JSON code values or exact non-JSON code text; saved-recipe and audit immutability; owned completion nonack; complete same-session Git review response. JSON key order/indentation and HTML/CSS/script byte identity are not claimed."
        for side in sides:
            if getattr(side, "client", None):
                side.client.assert_no_credential(report)
        create_file(output / "comparison.json", (json.dumps(report, indent=2, ensure_ascii=False) + "\n").encode())
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference-manifest", required=True)
    parser.add_argument("--candidate-manifest", required=True)
    parser.add_argument("--out-dir", required=True)
    args = parser.parse_args()
    result = run_existing_pair(args.reference_manifest, args.candidate_manifest, args.out_dir)
    print(json.dumps({"passed": result["passed"], "unchanged": result["unchanged"], "failed": result["failed"],
                      "cleanup_errors": len(result["cleanup_errors"]), "output": args.out_dir}))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
