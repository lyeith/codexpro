#!/usr/bin/env python3
"""Bounded, private-only process/jobs/batch differential lane.

Uses contract_suite.Client without changing the general suite. Both explicit
fixture roots must already be registered under --project. A locally written
nonce read through MCP proves each workspace maps to the intended private root.
The lane never accepts port 7200 or stops jobs it did not create.

Run sequentially with other mutation lanes on this fixture project. Commands are
small, have bounded execution, and cleanup stops only owned job IDs. The parent
launches this script with nice -n 10 and an external timeout.
"""
from __future__ import annotations

import argparse
import base64
import datetime as dt
import difflib
import hashlib
import json
import math
import os
from pathlib import Path
import pwd
import re
import urllib.parse
import urllib.request
import uuid

from contract_suite import Client, SOURCE_COMMIT, raw_result


class ContractFailure(AssertionError):
    pass


def require(condition, message):
    if not condition:
        raise ContractFailure(message)


class RefusePrivateRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        raise ContractFailure("Private contract endpoints must not redirect")


def private_http():
    # This standalone lane uses the shared Client, whose urllib calls consult
    # this process-local opener. Never forward its bearer token via a redirect
    # or an environment-configured proxy.
    urllib.request.install_opener(urllib.request.build_opener(
        urllib.request.ProxyHandler({}), RefusePrivateRedirect()))


def command_label(command):
    label = " ".join(command.strip().splitlines()[0].split())
    return label if len(label) <= 80 else label[:77] + "…"


def decode_cursor(value):
    require(isinstance(value, str) and len(value) <= 2048, "cursor must be a bounded string")
    try:
        parsed = json.loads(base64.urlsafe_b64decode(value + "=" * (-len(value) % 4)))
    except (ValueError, UnicodeError) as error:
        raise ContractFailure("cursor is not valid base64url JSON") from error
    require(set(parsed) == {"v", "id", "offsets"} and parsed["v"] == 1,
            "cursor version/schema changed")
    offsets = parsed["offsets"]
    require(isinstance(parsed["id"], str) and isinstance(offsets, list) and len(offsets) == 2,
            "cursor identity/offset shape changed")
    require(all(type(n) is int and 0 <= n <= 9007199254740991 for n in offsets),
            "cursor offsets must be nonnegative safe integers")
    return parsed


def encode_cursor(parsed):
    return base64.urlsafe_b64encode(json.dumps(parsed, separators=(",", ":")).encode()).decode().rstrip("=")


def text_of(result):
    return "\n".join(block["text"] for block in result.get("content", [])
                     if block.get("type") == "text" and isinstance(block.get("text"), str))


def read_text(result):
    text = result["structuredContent"]["text"]
    return "\n".join(re.sub(r"^\s*\d+ \| ?", "", line) for line in text.splitlines())


def strict_timestamp(value):
    require(isinstance(value, str) and re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z", value),
            "timestamp must preserve the millisecond UTC contract")
    dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    return "<timestamp>"


class Side:
    def __init__(self, label, endpoint, token_file, fixture_root, project, run_id):
        parsed = urllib.parse.urlsplit(endpoint)
        expected_port = {"reference": 7211, "candidate": 7210}.get(label)
        require(parsed.scheme == "http" and parsed.hostname in {"localhost", "127.0.0.1", "::1"}
                and parsed.port == expected_port and parsed.path == "/mcp"
                and not parsed.username and not parsed.password and not parsed.query and not parsed.fragment,
                "This mutation lane requires exact private reference7211/candidate7210 MCP endpoints")
        root = Path(fixture_root).expanduser().resolve(strict=True)
        require(root.is_dir(), "private fixture root must already exist")
        homes = {Path.home().resolve(), Path(pwd.getpwuid(os.getuid()).pw_dir).resolve()}
        protected = [p.resolve() for home in homes for p in (home / ".codexpro", home / ".config" / "codexpro",
                     home / "Projects" / "codexpro", home / "apps" / "codexpro")]
        require(root != Path("/") and root not in homes and not any(root.is_relative_to(p) for p in protected),
                "protected live/project root refused")
        token_path = Path(token_file).expanduser().resolve(strict=True)
        require(not any(token_path.is_relative_to(p) for p in protected), "live/project token path refused")
        require(re.fullmatch(r"[a-f0-9]{16}", run_id), "fixture run identity must be a fresh16-hex nonce")
        self.label, self.root = label, root
        private_http()
        self.client = Client(endpoint, token_file, timeout=40)
        self.records = []
        self.jobs = {}
        self.previous_jobs = {}
        self.owned_jobs = set()
        self.owned_contexts = {}
        self.batch_paths = {}
        self.batch_tags = {}
        self.prefix = "sp6-process-contract/" + run_id
        self.workspace = None
        self.scenario = "setup"
        self.fixture = root / self.prefix
        try:
            self.bind_workspace(project, run_id)
        except BaseException:
            self.client.close()
            raise

    def bind_workspace(self, project, run_id):
        root = self.root
        namespace = self.fixture.parent
        require(not namespace.is_symlink() and namespace.resolve().is_relative_to(root),
                "fixture namespace must not be a symlink or leave its private root")
        self.fixture.mkdir(parents=True, exist_ok=False)
        projects = raw_result(self.client.call("list_projects", {}))["structuredContent"]["projects"]
        matches = [p for p in projects if p.get("id") == project]
        require(len(matches) == 1, "project must resolve to one configured private fixture")
        self.workspace = matches[0]["workspace_id"]
        marker = "codexpro-private-fixture-" + run_id
        (self.fixture / "binding-check.txt").write_text(marker + "\n")
        read = raw_result(self.client.call("read", {"workspace_id": self.workspace,
                          "path": self.prefix + "/binding-check.txt", "end_line": 1}))
        require(read.get("isError") is not True and read_text(read) == marker
                and read["structuredContent"]["sha256"] == hashlib.sha256((marker + "\n").encode()).hexdigest(),
                "MCP workspace does not map to the explicit private fixture root")
        active = raw_result(self.client.call("jobs", {"workspace_id": self.workspace,
                            "include_finished": False, "output": "none", "wait_ms": 0}))
        require(active.get("isError") is not True and not active["structuredContent"]["jobs"],
                "private fixture already has active jobs; run this lane separately")

    def begin(self, name, fixtures=None):
        # The source's job_not_found recovery includes recent workspace jobs,
        # including jobs created by earlier scenarios in this same private lane.
        # Keep their already-observed identities; never invent bindings merely
        # from a recovery list, and keep list order/count/error text exact.
        for actual, symbolic in self.jobs.items():
            self.previous_jobs[actual] = "job_<prior:" + self.scenario + ":" + symbolic[5:-1] + ">"
        self.scenario = name
        self.jobs = {}
        self.batch_paths = {}
        self.batch_tags = {}
        directory = self.fixture / name
        directory.mkdir(parents=True, exist_ok=False)
        for rel, content in (fixtures or {}).items():
            path = directory / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)

    @property
    def cwd(self):
        return self.prefix + "/" + self.scenario

    def path(self, name):
        return self.cwd + "/" + name

    def data(self, result):
        data = result.get("structuredContent")
        require(isinstance(data, dict), "tool result lacks structuredContent")
        return data

    def private_file(self, relative):
        require(isinstance(relative, str) and not Path(relative).is_absolute(), "fixture path must be relative")
        path = self.root / relative
        require(not path.is_symlink() and path.resolve().is_relative_to(self.root),
                "fixture path escapes its explicit private root")
        return path

    def own_job(self, view, command, cwd, workspace, full_command):
        job = view.get("job_id")
        require(isinstance(job, str) and re.fullmatch(r"job_[a-f0-9]{8}", job), "owned job identity is invalid")
        require(workspace == self.workspace and cwd == self.cwd and view.get("cwd") == cwd,
                "returned job does not belong to this run's private command directory")
        expected = command.strip() if full_command else command_label(command)
        require(view.get("command") == expected, "returned job command does not match its admission")
        context = {"cwd": cwd, "command": command_label(command), "started_at": view.get("started_at")}
        if context["started_at"] is not None:
            strict_timestamp(context["started_at"])
        previous = self.owned_contexts.get(job)
        require(previous is None or previous == context, "returned job identity was reused for a different command")
        self.owned_contexts[job] = context
        self.owned_jobs.add(job)

    def observe_admissions(self, name, args, data):
        if name == "bash" and isinstance(data.get("job_id"), str):
            self.own_job(data, args["command"], args.get("cwd", "."), data.get("workspace_id"), True)
        elif name == "start_jobs" and data.get("jobs"):
            views, commands = data["jobs"], args["commands"]
            require(len(views) == len(commands) and data.get("job_ids") == [v.get("job_id") for v in views],
                    "background admission response did not preserve command identity/order")
            for view, command in zip(views, commands):
                self.own_job(view, command["command"], command.get("cwd", "."), data.get("workspace_id"), False)
        elif name == "batch":
            operations = args.get("operations")
            if operations is None and args.get("path"):
                require(re.fullmatch(r"\.codexpro-batches/[A-Fa-f0-9]{4}\.json", args["path"]),
                        "resume path is not an owned automatic definition")
                operations = json.loads(self.private_file(args["path"]).read_text()).get("operations", [])
            for child in data.get("results", []):
                structured = child.get("structured", {})
                if child.get("tool") != "bash" or not isinstance(structured, dict) or not isinstance(structured.get("job_id"), str):
                    continue
                index = child.get("index")
                require(isinstance(operations, list) and type(index) is int and 0 <= index < len(operations),
                        "batch job is not bound to a submitted operation")
                operation = operations[index]
                require(operation.get("tool") == "bash", "batch job identity belongs to another operation kind")
                inputs = operation["args"]
                self.own_job(structured, inputs["command"], inputs.get("cwd", "."), structured.get("workspace_id"), True)

    def verify_cleanup_views(self, data, ids):
        require(data.get("workspace_id") == self.workspace, "cleanup metadata changed workspace")
        views = data.get("jobs", [])
        require(len(views) == len(ids) and {v.get("job_id") for v in views} == set(ids),
                "cleanup metadata did not return exactly the owned job IDs")
        for view in views:
            context = self.owned_contexts[view["job_id"]]
            require(view.get("cwd") == context["cwd"] and view.get("command") == context["command"],
                    "cleanup metadata does not match the owned command directory/label")
            strict_timestamp(view.get("started_at"))
            if context["started_at"] is not None:
                require(view["started_at"] == context["started_at"], "cleanup job incarnation changed")

    def observe(self, value):
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "job_id" and isinstance(item, str):
                    require(re.fullmatch(r"job_[a-f0-9]{8}", item), "job ID shape changed")
                    if item not in self.jobs and item not in self.previous_jobs:
                        self.jobs[item] = "job_<%d>" % (len(self.jobs) + 1)
                elif key == "batch_path" and isinstance(item, str) and re.fullmatch(r"\.codexpro-batches/[A-Fa-f0-9]{4}\.json", item):
                    if item not in self.batch_paths:
                        label = "batch-%d" % (len(self.batch_paths) + 1)
                        self.batch_paths[item] = ".codexpro-batches/<" + label + ">.json"
                        self.batch_tags[Path(item).stem.upper()] = "<" + label + ">"
                self.observe(item)
        elif isinstance(value, list):
            for item in value:
                self.observe(item)

    def call(self, label, name, args, *, expect_error=False):
        require(args.get("workspace_id", self.workspace) == self.workspace, "cross-workspace call refused by lane")
        inputs = {"workspace_id": self.workspace, **args}
        response = self.client.call(name, inputs)
        result = raw_result(response)
        self.records.append({"scenario": self.scenario, "step": label, "tool": name,
                             "arguments": inputs, "response": response})
        require(response["http_status"] == 200 and isinstance(response["body"], dict)
                and "result" in response["body"], "MCP tool call failed at transport/RPC boundary")
        self.observe(result)
        self.observe_admissions(name, args, result.get("structuredContent", {}))
        require((result.get("isError") is True) == expect_error,
                "%s error outcome differed from expectation" % label)
        return result

    def bash(self, label, command, **kwargs):
        return self.call(label, "bash", {"command": command, "cwd": self.cwd, **kwargs})

    def collect(self, label, job, **kwargs):
        return self.call(label, "jobs", {"job_ids": [job], "wait_ms": 5000, **kwargs})

    def cleanup(self):
        errors = []
        ids = sorted(self.owned_jobs)
        for start in range(0, len(ids), 32):
            try:
                chunk = ids[start:start + 32]
                metadata = raw_result(self.client.call("jobs", {"workspace_id": self.workspace,
                                      "job_ids": chunk, "output": "none", "wait_ms": 0}))
                require(not metadata.get("isError"), "cannot verify owned jobs before cleanup")
                self.verify_cleanup_views(metadata.get("structuredContent", {}), chunk)
                response = self.client.call("stop_jobs", {"workspace_id": self.workspace,
                                             "job_ids": chunk})
                self.client.assert_no_credential(response)
                result = raw_result(response)
                if not result.get("isError"):
                    self.verify_cleanup_views(result.get("structuredContent", {}), chunk)
                if result.get("isError"):
                    errors.append("owned-job cleanup returned an error")
                elif any(job["status"] == "running" for job in result.get("structuredContent", {}).get("jobs", [])):
                    errors.append("owned-job cleanup returned an unfinished job")
                else:
                    self.owned_jobs.difference_update(chunk)
            except Exception as error:
                errors.append(type(error).__name__ + ": " + str(error))
        return errors

    def normalize(self, value, key=None, owner=None):
        if isinstance(value, dict):
            return {k: self.normalize(v, k, value) for k, v in value.items()}
        if isinstance(value, list):
            return [self.normalize(v, key, owner) for v in value]
        if key in {"duration_ms", "elapsed_ms", "waited_ms", "deadline_in_ms"} and value is not None:
            require(type(value) in {int, float} and math.isfinite(value) and value >= 0,
                    "timing field is not a nonnegative finite number")
            return "<milliseconds>"
        if key in {"started_at", "finished_at", "output_expires_at"} and value is not None:
            return strict_timestamp(value)
        if key == "next_cursor" and value is not None:
            cursor = decode_cursor(value)
            if owner and "job_id" in owner:
                require(cursor["id"] == self.workspace + ":" + owner["job_id"] + ":" + owner["started_at"],
                        "cursor does not bind exact workspace/job/start identity")
                require(cursor["offsets"][0] <= owner["available_stdout_bytes"] and
                        cursor["offsets"][1] <= owner["available_stderr_bytes"],
                        "cursor exceeds retained stream lengths")
            return {"v": 1, "id": "<validated-job-incarnation>", "offsets": cursor["offsets"]}
        if not isinstance(value, str):
            return value
        text = value.replace(str(self.root), "<fixture-root>").replace(self.workspace, "<workspace>")
        for actual, symbolic in self.batch_paths.items():
            text = text.replace(actual, symbolic)
        for actual, symbolic in {**self.previous_jobs, **self.jobs}.items():
            text = text.replace(actual, symbolic)
        if key == "batch_tag":
            require(value in self.batch_tags, "batch tag was not bound to a valid automatic path")
            return self.batch_tags[value]
        for tag, symbolic in self.batch_tags.items():
            text = text.replace(" (" + tag + ")", " (" + symbolic + ")")
        if key == "text":
            # Only the legacy UI timing phrases are variable. Counts, commands,
            # output, timeout requests, exit codes and all remaining text remain.
            text = re.sub(r"(?m)^Duration: \d+ ms$", "Duration: <milliseconds> ms", text)
            text = re.sub(r"\b(running|after) \d+(?:\.\d+)? (?:ms|s|min)\b", r"\1 <elapsed>", text)
            text = re.sub(r"\(\d+ ms so far\)", "(<milliseconds> ms so far)", text)
            text = re.sub(r"Still running after \d+ ms;", "Still running after <milliseconds> ms;", text)
        return text


def bash_outcomes(side):
    side.begin("bash-outcomes")
    result = side.bash("exit-seven", "printf 'ok\\n'; printf 'warn\\n' >&2; exit 7")
    data = side.data(result)
    require(data["exit_code"] == 7 and data["job_status"] == "failed" and data["job_origin"] == "foreground",
            "nonzero Bash outcome did not preserve foreground failure")
    require(data["stdout"] == "ok\n" and data["stderr"] == "warn\n" and data["timed_out"] is False,
            "Bash output or timeout semantics changed")
    require(data["signal"] is None, "normal nonzero exit acquired a signal")
    side.collect("metadata", data["job_id"], output="none", wait_ms=0)
    side.collect("head", data["job_id"], output="head", tail_bytes=256, wait_ms=0)
    side.collect("legacy-head", data["job_id"], full_output=True, wait_ms=0)
    side.collect("tail", data["job_id"], output="tail", tail_bytes=256, wait_ms=0)


def incremental_output(side):
    side.begin("incremental-output")
    command = "for i in $(seq 1 60); do printf 'αβγδ\\n'; done; for i in $(seq 1 40); do printf 'εζη\\n' >&2; done"
    result = side.bash("produce-unicode", command)
    job = side.data(result)["job_id"]
    cursor = None
    stdout, stderr = [], []
    first_cursor = None
    for page in range(12):
        args = {"job_ids": [job], "output": "incremental", "max_bytes": 256, "wait_ms": 0}
        if cursor is not None:
            args["cursor"] = cursor
        result = side.call("page-%02d" % page, "jobs", args)
        data = side.data(result)
        view = data["jobs"][0]
        encoded_bytes = len(view["stdout"].encode()) + len(view["stderr"].encode())
        require(encoded_bytes == view["returned_bytes"] <= 256, "incremental UTF-8 budget/count changed")
        stdout.append(view["stdout"])
        stderr.append(view["stderr"])
        next_cursor = view["next_cursor"]
        require(next_cursor != cursor or view["output_complete"], "cursor stopped advancing with remaining output")
        cursor = next_cursor
        first_cursor = first_cursor or cursor
        if view["output_complete"]:
            break
    else:
        raise ContractFailure("bounded incremental drain exceeded twelve pages")
    require("".join(stdout) == "αβγδ\n" * 60 and "".join(stderr) == "εζη\n" * 40,
            "incremental output omitted/duplicated UTF-8 data")
    tampered = decode_cursor(first_cursor)
    tampered["offsets"][0] = 1
    side.call("split-character", "jobs", {"job_ids": [job], "output": "incremental",
               "cursor": encode_cursor(tampered), "max_bytes": 256}, expect_error=True)
    side.call("malformed-cursor", "jobs", {"job_ids": [job], "output": "incremental",
               "cursor": "not-a-cursor", "max_bytes": 256}, expect_error=True)
    second = side.bash("second-job", "printf 'second\\n'")
    side.call("wrong-incarnation", "jobs", {"job_ids": [side.data(second)["job_id"]],
               "output": "incremental", "cursor": first_cursor, "max_bytes": 256}, expect_error=True)


def dedupe_and_stop(side):
    side.begin("dedupe-and-stop")
    command = "printf 'ready\\n'; sleep 30; printf 'must-not-finish\\n'"
    args = {"commands": [{"command": command, "cwd": side.cwd, "label": "owned stop fixture"}]}
    first = side.call("start", "start_jobs", args)
    job = side.data(first)["job_ids"][0]
    require(side.data(first)["running_count"] == 1, "bounded slow command did not start running")
    second = side.call("deduplicate", "start_jobs", args)
    require(side.data(second)["job_ids"] == [job], "duplicate running command started a second process")
    side.call("list-running", "jobs", {"include_finished": False, "output": "none", "wait_ms": 0})
    stopped = side.call("stop", "stop_jobs", {"job_ids": [job, job]})
    data = side.data(stopped)
    require(data["stopped_ids"] == [job] and data["already_finished_ids"] == [], "stop deduplication changed")
    require(data["jobs"][0]["status"] == "stopped" and data["jobs"][0]["stdout_tail"] == "ready\n",
            "stop did not preserve terminal status and captured prefix")
    again = side.call("stop-again", "stop_jobs", {"job_ids": [job]})
    require(side.data(again)["stopped_ids"] == [] and side.data(again)["already_finished_ids"] == [job],
            "repeat stop was not idempotent")


def digest_acknowledgement(side):
    side.begin("digest-acknowledgement", {"visible.txt": "visible\n"})
    started = side.call("start", "start_jobs", {"commands": [{"command": "printf 'ack\\n'", "cwd": side.cwd}]})
    job = side.data(started)["job_ids"][0]
    side.collect("metadata-does-not-ack", job, output="none")
    side.collect("incremental-does-not-ack", job, output="incremental", max_bytes=256, wait_ms=0)
    first = side.call("digest-first", "read", {"path": side.path("visible.txt")})
    require(any(entry["job_id"] == job for entry in side.data(first).get("background_jobs", [])),
            "metadata/incremental read acknowledged a completion")
    second = side.call("digest-second", "read", {"path": side.path("visible.txt")})
    require(not any(entry["job_id"] == job for entry in side.data(second).get("background_jobs", [])),
            "ordinary tool digest repeated an acknowledged completion")
    started = side.call("start-another", "start_jobs", {"commands": [{"command": "printf 'tail-ack\\n'", "cwd": side.cwd}]})
    job = side.data(started)["job_ids"][0]
    side.collect("tail-acks", job, output="tail")
    read = side.call("after-tail", "read", {"path": side.path("visible.txt")})
    require(not any(entry["job_id"] == job for entry in side.data(read).get("background_jobs", [])),
            "explicit tail collection failed to acknowledge completion")


def request_timeout(side):
    side.begin("request-timeout")
    # A release file keeps the command running until after the foreground wait,
    # independent of host speed. The loop itself is bounded to approximately 15s.
    command = "printf 'before\\n'; for i in $(seq 1 300); do if test -f release; then printf 'after\\n'; exit 0; fi; sleep .05; done; exit 9"
    promoted = side.bash("promote", command, timeout_ms=1000)
    data = side.data(promoted)
    require(data["job_status"] == "running" and data["job_origin"] == "promoted",
            "request wait did not promote the accepted command")
    side.call("release", "write", {"path": side.path("release"), "content": "release\n"})
    final = side.collect("collect-promoted", data["job_id"])
    require(side.data(final)["all_succeeded"] is True and side.data(final)["jobs"][0]["stdout_tail"] == "before\nafter\n",
            "promoted command lost its accepted execution/output")
    killed = side.bash("kill-on-timeout", "printf 'before-kill\\n'; sleep 8; printf 'unreachable\\n'", timeout_ms=1000, on_timeout="kill")
    data = side.data(killed)
    require(data["timed_out"] is True and data["job_status"] == "timed_out" and "unreachable\n" not in data["stdout"],
            "on_timeout=kill failed to enforce request-owned deadline")


def output_pinning(side):
    side.begin("output-pinning")
    source = side.bash("source", "printf 'retained source\\n'")
    job = side.data(source)["job_id"]
    command = 'cat "$CODEXPRO_JOB_OUTPUT_DIR/' + job + '/stdout.log"'
    dependent = side.bash("read-pinned-output", command, input_job_ids=[job, job])
    require(side.data(dependent)["stdout"] == "retained source\n" and side.data(dependent)["exit_code"] == 0,
            "input job output environment/pin contract failed")
    side.call("unknown-input", "bash", {"command": "true", "cwd": side.cwd,
               "input_job_ids": ["job_00000000"]}, expect_error=True)


def batch_preflight(side):
    side.begin("batch-preflight", {"original.txt": "original\n"})
    (side.root / side.path("real")).mkdir()
    (side.root / side.path("alias")).symlink_to("real", target_is_directory=True)
    original = side.path("original.txt")
    new = side.path("must-not-exist.txt")
    definitions = [
        ("last-invalid", [{"id": "write", "tool": "write", "args": {"path": new, "content": "bad\n"}},
                          {"id": "invalid", "tool": "read", "args": {"path": 42}}]),
        ("duplicate-target", [{"id": "first", "tool": "write", "args": {"path": original, "content": "bad\n"}},
                              {"id": "second", "tool": "write", "args": {"path": original, "content": "worse\n"}}]),
        ("canonical-alias-target", [{"id": "real", "tool": "write", "args": {"path": side.path("real/new.txt"), "content": "bad\n"}},
                                    {"id": "alias", "tool": "write", "args": {"path": side.path("alias/new.txt"), "content": "worse\n"}}]),
        ("patch-exclusive", [{"id": "patch", "tool": "apply_patch", "args": {"patch": "*** Begin Patch\n*** Add File: " + new + "\n+bad\n*** End Patch"}},
                             {"id": "write", "tool": "write", "args": {"path": original, "content": "bad\n"}}]),
        ("verification-before-write", [{"id": "verify", "tool": "bash", "args": {"command": "true", "cwd": side.cwd}},
                                       {"id": "write", "tool": "write", "args": {"path": new, "content": "bad\n"}}]),
        ("inner-workspace", [{"tool": "read", "args": {"path": original, "workspace_id": side.workspace}}]),
        ("inner-session", [{"tool": "bash", "args": {"command": "true", "session_id": "not-a-real-session"}}]),
        ("background-verification", [{"tool": "bash", "args": {"command": "true", "background": True}}]),
    ]
    directory = side.root / ".codexpro-batches"
    require(not directory.is_symlink() and directory.resolve().is_relative_to(side.root),
            "automatic batch namespace escaped its private root")
    before = set(directory.iterdir()) if directory.exists() else set()
    for label, operations in definitions:
        side.call(label, "batch", {"operations": operations, "persist": True}, expect_error=True)
        require(not (side.root / new).exists(), "invalid batch executed an earlier child")
        require((side.root / original).read_text() == "original\n", "invalid batch changed a file")
        after = set(directory.iterdir()) if directory.exists() else set()
        require(after == before, "invalid batch persisted an automatic definition")


def batch_parallel_and_skip(side):
    side.begin("batch-parallel-and-skip", {"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"})
    reads = [{"id": label, "tool": "read", "args": {"path": side.path(label + ".txt")}} for label in ("a", "b", "c")]
    result = side.call("parallel", "batch", {"operations": reads, "mode": "parallel"})
    require([r["id"] for r in side.data(result)["results"]] == ["a", "b", "c"] and side.data(result)["succeeded_count"] == 3,
            "parallel batch reordered or lost child results")
    operations = [reads[0], {"id": "missing", "tool": "read", "args": {"path": side.path("missing.txt")}}, reads[2]]
    result = side.call("serial-stop", "batch", {"operations": operations}, expect_error=True)
    require(side.data(result)["skipped_count"] == 1 and side.data(result)["executed_operation_count"] == 2,
            "serial failure did not stop remaining children")
    result = side.call("continue-read-only", "batch", {"operations": operations, "continue_on_error": True}, expect_error=True)
    require(side.data(result)["skipped_count"] == 0 and side.data(result)["executed_operation_count"] == 3,
            "read-only continue_on_error failed to execute later child")


def batch_resume(side):
    side.begin("batch-resume")
    output = side.path("written.txt")
    operations = [
        {"id": "write", "tool": "write", "args": {"path": output, "content": "written once\n"}},
        {"id": "verify", "tool": "bash", "args": {"command": "test -f gate && printf 'verified\\n'", "cwd": side.cwd, "on_timeout": "background"}},
        {"id": "inspect", "tool": "read", "args": {"path": output}},
    ]
    failed = side.call("persisted-failure", "batch", {"operations": operations}, expect_error=True)
    data = side.data(failed)
    require(data["persisted"] is True and data["persistence_default"] is True and data["resumable_from"] == "verify",
            "verification workflow did not persist/resume by default")
    path = data["batch_path"]
    require(re.fullmatch(r"\.codexpro-batches/[A-Fa-f0-9]{4}\.json", path)
            and path in side.batch_paths, "batch returned an unowned automatic definition path")
    definition_file = side.private_file(path)
    definition = json.loads(definition_file.read_text())
    require(definition_file.stat().st_mode & 0o777 == 0o600, "automatic batch definition is not private")
    require(definition["version"] == 1 and definition["operations"][1]["args"]["on_timeout"] == "kill",
            "saved definition lost version or forced bounded verification")
    require(definition["operations"][0]["args"]["path"] == output,
            "saved definition does not belong to this run's mutation")
    require(all(not any(k in op["args"] for k in ("workspace_id", "session_id", "execution", "attempt_token", "session_token"))
                for op in definition["operations"]), "saved definition contains injected credentials")
    before = (side.root / output).stat().st_mtime_ns
    side.call("make-gate", "write", {"path": side.path("gate"), "content": "ready\n"})
    resumed = side.call("resume", "batch", {"path": path, "from": "verify"})
    data = side.data(resumed)
    require(data["start_index"] == 1 and data["total_operation_count"] == 3 and data["operation_count"] == 2 and data["succeeded"] is True,
            "stored resume lost original indices or success")
    require((side.root / output).stat().st_mtime_ns == before, "resume replayed an earlier successful mutation")
    tail = side.call("resume-read-only-index", "batch", {"path": path, "from_index": 2})
    require(side.data(tail)["start_index"] == 2 and side.data(tail)["operation_count"] == 1
            and side.data(tail)["mutating"] is False, "read-only resumed tail retained mutation attribution")
    side.call("unknown-from", "batch", {"path": path, "from": "not-there"}, expect_error=True)
    side.call("outside-index", "batch", {"path": path, "from_index": 11}, expect_error=True)
    side.call("forbidden-override", "batch", {"path": path, "mode": "serial"}, expect_error=True)
    definition["operations"][0]["args"]["path"] = 42
    definition_file.write_text(json.dumps(definition) + "\n")
    changed_at = definition_file.stat().st_mtime_ns
    side.call("resume-validates-skipped-prefix", "batch", {"path": path, "from_index": 2}, expect_error=True)
    require(definition_file.stat().st_mtime_ns == changed_at,
            "invalid resumed definition acquired maintenance effects before validation")


def batch_bounded_verification(side):
    side.begin("batch-bounded-verification")
    operations = [
        {"id": "write", "tool": "write", "args": {"path": side.path("kept.txt"), "content": "keep\n"}},
        {"id": "verify", "tool": "bash", "args": {"command": "printf 'before\\n'; sleep 8; printf 'unreachable\\n'", "cwd": side.cwd,
                                                        "timeout_ms": 1000, "on_timeout": "background"}},
        {"id": "after", "tool": "read", "args": {"path": side.path("kept.txt")}},
    ]
    result = side.call("bounded-verification", "batch", {"operations": operations, "persist": False}, expect_error=True)
    data = side.data(result)
    require(data["succeeded_count"] == 1 and data["failed_count"] == 1 and data["skipped_count"] == 1,
            "bounded verification did not fail/skip after mutation")
    require(data["results"][1]["error"] == "Bash command timed out.", "batch verification promoted instead of killing")
    require((side.root / side.path("kept.txt")).read_text() == "keep\n", "verification failure rolled back a committed mutation")


SCENARIOS = [bash_outcomes, incremental_output, dedupe_and_stop, digest_acknowledgement,
             request_timeout, output_pinning, batch_preflight, batch_parallel_and_skip,
             batch_resume, batch_bounded_verification]


def comparable(side, record):
    response = record["response"]
    return side.normalize({"scenario": record["scenario"], "step": record["step"], "tool": record["tool"],
                           "http_status": response["http_status"], "content_type": response["content_type"],
                           "result": raw_result(response)})


def compare(reference, candidate, name):
    left = [comparable(reference, r) for r in reference.records if r["scenario"] == name]
    right = [comparable(candidate, r) for r in candidate.records if r["scenario"] == name]
    if left == right:
        return {"scenario": name, "status": "PASS", "compared_calls": len(left)}
    before = json.dumps(left, sort_keys=True, ensure_ascii=False, indent=2).splitlines()
    after = json.dumps(right, sort_keys=True, ensure_ascii=False, indent=2).splitlines()
    difference = list(difflib.unified_diff(before, after, fromfile="typescript", tofile="go", lineterm=""))
    return {"scenario": name, "status": "FAIL", "reference_calls": len(left), "candidate_calls": len(right),
            "diff": difference[:400], "diff_truncated": len(difference) > 400}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", default="http://127.0.0.1:7211/mcp")
    parser.add_argument("--reference-token", required=True)
    parser.add_argument("--reference-root", required=True)
    parser.add_argument("--candidate", default="http://127.0.0.1:7210/mcp")
    parser.add_argument("--candidate-token", required=True)
    parser.add_argument("--candidate-root", required=True)
    parser.add_argument("--project", default="contract")
    parser.add_argument("--output", required=True)
    parser.add_argument("--scenario", action="append", choices=[f.__name__.replace("_", "-") for f in SCENARIOS])
    args = parser.parse_args()
    require(Path(args.reference_root).resolve() != Path(args.candidate_root).resolve(),
            "TypeScript and Go must use separate fixture roots")
    run_id = uuid.uuid4().hex[:16]
    sides = []
    report = {"schema_version": 1, "source_commit": SOURCE_COMMIT, "lane": "private-process-jobs-batch",
              "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(), "run_id": run_id,
              "reference": args.reference, "candidate": args.candidate, "scenarios": [], "cleanup_errors": []}
    try:
        sides.append(Side("reference", args.reference, args.reference_token, args.reference_root, args.project, run_id))
        sides.append(Side("candidate", args.candidate, args.candidate_token, args.candidate_root, args.project, run_id))
        for scenario in SCENARIOS:
            name = scenario.__name__.replace("_", "-")
            if args.scenario and name not in args.scenario:
                continue
            failures = {}
            for side in sides:
                try:
                    scenario(side)
                except Exception as error:
                    failures[side.label] = type(error).__name__ + ": " + str(error)
                finally:
                    cleanup = side.cleanup()
                    report["cleanup_errors"].extend({"side": side.label, "scenario": name, "error": error} for error in cleanup)
            if failures:
                result = {"scenario": name, "status": "FAIL", "assertion_failures": failures}
            else:
                try:
                    result = compare(sides[0], sides[1], name)
                except Exception as error:
                    result = {"scenario": name, "status": "FAIL", "normalization_error": type(error).__name__ + ": " + str(error)}
            report["scenarios"].append(result)
            print(json.dumps({"scenario": name, "status": result["status"]}), flush=True)
    finally:
        for side in sides:
            report[side.label + "_records"] = side.records
            report["cleanup_errors"].extend({"side": side.label, "error": error} for error in side.cleanup())
            side.client.assert_no_credential(report)
            side.client.close()
        report["passed"] = bool(report["scenarios"]) and all(r["status"] == "PASS" for r in report["scenarios"]) and not report["cleanup_errors"]
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "scenarios": len(report["scenarios"]), "output": str(output)}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
