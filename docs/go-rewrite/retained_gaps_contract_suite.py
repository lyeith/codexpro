#!/usr/bin/env python3
"""Own a bounded private pair for attachment and direct-workspace contracts.

No existing listener is adopted. The optional activity supplement runs only on
this pair, sequentially after these mutations and before owned-process cleanup.
"""
from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import secrets
import signal
import threading
import time
import urllib.parse

from contract_suite import Client, SOURCE_COMMIT, raw_result
from paired_contract_suite import (
    APPROVAL_COMMIT, RETIRED, ContractFailure, Evidence, artifact_path, canonical,
    compare_inventory, difference, digest, iso_now, private_path,
    process_start_ticks, require, safe_error, validate_launch,
)
from admin_catalog_contract_suite import (
    Side as AdminSide, catalog_object, replace_strings, timestamp,
)
from managed_contract_suite import (
    OwnedServer, assert_free_ports, durable_json, write_exclusive,
    managed_instruction_aliases as owned_instruction_aliases,
)
from process_contract_suite import private_http

IMPORT_LIMIT = 1024
IMPORT_PREFIX = "import-contract"
TEXT = "Private text attachment.\nUnicode: 🙂.\n".encode("utf-8")
REPLACEMENT = b"Replacement text has distinct bytes.\n"
BINARY = bytes(range(64)) + b"\x00\xff\x80\xfe"
PNG = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a1foAAAAASUVORK5CYII=")

# Exact source capture at the deployed commit: importAttachmentFile passes the
# parsed boolean to assertSafeImportUrl, which admits HTTP only with that flag.
IMPORT_POLICY = {
    "source_commit": SOURCE_COMMIT,
    "source_path": "src/importOps.ts",
    "source_sha256": "293fdfd1ba6c9412ed1d0e33b22ed585102eeb02ecf6429f7f733d9ab957d0c1",
    "source_lines": {"protocol_and_host": [130, 151], "caller_override": [332, 348],
                     "native_url_constructor": [344, 344]},
    "environment": {"CODEXPRO_IMPORT_ALLOW_LOOPBACK": "1"},
}

DIRECT_CASES = [
    "DIRECT-01-first-session-list", "DIRECT-02-open-current",
    "DIRECT-03-list-after-open", "DIRECT-04-repeat-open-with-tree",
    "DIRECT-05-repeat-list-preserves-open-time", "DIRECT-06-second-session-list",
    "DIRECT-07-open-current-second-session", "DIRECT-08-independent-session-state",
]
IMPORT_CASES = [
    "IMPORT-01-text", "IMPORT-02-binary", "IMPORT-03-png-verified",
    "IMPORT-04-view-imported-png", "IMPORT-05-existing-destination",
    "IMPORT-06-overwrite", "IMPORT-07-redirect", "IMPORT-08-hash-mismatch",
    "IMPORT-09-mime-mismatch", "IMPORT-10-exact-byte-bound",
    "IMPORT-11-advertised-overflow", "IMPORT-12-streamed-overflow",
    "IMPORT-13-http-not-found", "IMPORT-14-redirect-without-location",
    "IMPORT-15-redirect-limit", "IMPORT-16-invalid-url-native-name",
]


def verify_policy(policy):
    require(isinstance(policy, dict)
            and policy.get("source_commit") == SOURCE_COMMIT
            and re.fullmatch(r"[a-f0-9]{64}", policy.get("source_sha256", ""))
            and policy.get("environment") == {"CODEXPRO_IMPORT_ALLOW_LOOPBACK": "1"},
            "exact TypeScript loopback import policy has not been captured; refusing launch")
    return dict(policy["environment"])


def sha(data):
    return hashlib.sha256(data).hexdigest()


def request_spec(path):
    """Pure HTTP fixture plan, also used by offline fixture tests."""
    parsed = urllib.parse.urlsplit(path)
    require(not parsed.scheme and not parsed.netloc and not parsed.query and not parsed.fragment,
            "attachment fixture accepts only its fixed relative paths")
    payloads = {
        "/text": (TEXT, "text/plain; charset=utf-8"),
        "/replacement": (REPLACEMENT, "text/plain"),
        "/binary": (BINARY, "application/octet-stream"),
        "/png": (PNG, "image/png"),
        "/exact": (b"e" * IMPORT_LIMIT, "application/octet-stream"),
        "/oversized": (b"x" * (IMPORT_LIMIT + 1), "application/octet-stream"),
        "/streamed": (b"y" * (IMPORT_LIMIT + 1), "application/octet-stream"),
    }
    if path == "/redirect":
        return 302, {"Location": "/png", "Content-Length": "0"}, b""
    if path == "/redirect-loop":
        return 302, {"Location": "/redirect-loop", "Content-Length": "0"}, b""
    if path == "/missing-location":
        return 302, {"Content-Length": "0"}, b""
    if path not in payloads:
        return 404, {"Content-Type": "text/plain", "Content-Length": "0"}, b""
    body, mime = payloads[path]
    headers = {"Content-Type": mime}
    if path != "/streamed":
        headers["Content-Length"] = str(len(body))
    return 200, headers, body


class AttachmentFixture:
    def __init__(self, deadline):
        self.deadline, self.requests = deadline, []
        self.lock = threading.Lock()
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.0"

            def log_message(self, *unused):
                pass

            def do_GET(self):
                if time.monotonic() >= fixture.deadline:
                    self.send_error(503); return
                # No credential is expected or retained at the attachment host.
                if self.headers.get("Authorization") or self.headers.get("Cookie"):
                    with fixture.lock:
                        fixture.requests.append({"credential_forwarded": True})
                    self.send_error(400); return
                try:
                    code, headers, body = request_spec(self.path)
                except ContractFailure:
                    self.send_error(400); return
                with fixture.lock:
                    if len(fixture.requests) >= 64:
                        self.send_error(429); return
                    fixture.requests.append({"method": "GET", "path": self.path,
                        "status": code, "bytes": len(body)})
                self.send_response(code)
                for key, value in headers.items():
                    self.send_header(key, value)
                self.send_header("Connection", "close")
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass  # An over-limit reader may close as soon as headers arrive.

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever,
            kwargs={"poll_interval": 0.1}, daemon=True)
        self.port = self.server.server_address[1]
        if self.port in (7200, 7210, 7211):
            self.server.server_close()
            raise ContractFailure("attachment fixture selected a reserved service port")
        self.origin = "http://127.0.0.1:" + str(self.port)
        self.thread.start()

    def count(self):
        with self.lock:
            return len(self.requests)

    def since(self, offset):
        with self.lock:
            return copy.deepcopy(self.requests[offset:])

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)
        require(not self.thread.is_alive(), "owned attachment listener did not stop")


class DirectServer(OwnedServer):
    def __init__(self, pair, label):
        super().__init__(pair, label)
        self.mode = "full"
        self.stopped_report = None
        self.creation = self.state / "created-projects"
        self.creation.mkdir(mode=0o700)
        temporary = self.state / "tmp"
        temporary.mkdir(mode=0o700)
        catalog = json.loads(self.catalog.read_text())
        catalog["creationRoots"] = [{"id": "fixtures", "label": "Private creation root",
                                     "root": str(self.creation)}]
        self.catalog.write_text(json.dumps(catalog, indent=2) + "\n")
        os.chmod(self.catalog, 0o600)
        self.environment.update(verify_policy(IMPORT_POLICY))
        self.environment.update({"CODEXPRO_MAX_IMPORT_BYTES": str(IMPORT_LIMIT),
            "CODEXPRO_MAX_HTTP_SESSIONS": "12", "TMPDIR": str(temporary)})
        if label == "reference":
            self.environment.update({"CODEXPRO_BASH_MODE": "full",
                "CODEXPRO_WORKTREE_MODE": "off", "CODEXPRO_TOOL_MODE": "full"})
        else:
            for flag, value in (("--worktree-mode", "off"), ("--tool-mode", "full")):
                self.argv[self.argv.index(flag) + 1] = value
            self.argv.extend(["--limits-file", str(self.state / "limits.env")])

    def start(self):
        super().start()
        audit = self.state / "audit" if self.label == "reference" else self.home / "audit"
        self.manifest.update({
            "creation_root": str(self.creation), "creation_root_id": "fixtures",
            "binary_sha256": self.pair.binary_hash,
            "audit": {"format": "jsonl" if self.label == "reference" else "segments",
                      "path": str(audit / "tool-calls.jsonl" if self.label == "reference" else audit)},
            "usage_database": str((audit if self.label == "reference" else self.home) / "usage" / "usage.sqlite"),
            "inbox_database": str((audit if self.label == "reference" else self.home) / "inbox" / "inbox.sqlite"),
        })
        # This file was exclusively created by this exact owned launch.
        temporary = self.state / "launch.complete.json"
        durable_json(temporary, self.manifest)
        os.replace(temporary, self.state / "launch.json")

    def verify(self):
        require(self.child is not None and self.child.poll() is None,
                "owned direct fixture server is not running")
        validate_launch(self.manifest, f"http://127.0.0.1:{self.port}/mcp",
                        self.token, self.root, self.port)
        if self.label == "candidate":
            require(Path(f"/proc/{self.child.pid}/exe").resolve(strict=True) == self.pair.binary,
                    "owned listener is not the declared Go binary")

    def stop(self):
        if self.stopped_report is None:
            self.stopped_report = super().stop()
        return self.stopped_report


class Side(AdminSide):
    """Reuse the strict audit/summary normalizer with this owned one-project fixture."""
    def __init__(self, pair, server):
        self.pair, self.server, self.label = pair, server, server.label
        self.run_id, self.port = pair.run_id, server.port
        self.manifest, self.root, self.state, self.home = server.manifest, server.root, server.state, server.home
        self.token, self.catalog_path = server.token, server.catalog
        self.creation_root, self.creation_id = server.creation, "fixtures"
        self.endpoint = f"http://127.0.0.1:{self.port}/mcp"
        self.origin = f"http://127.0.0.1:{self.port}"
        self.client = server.client
        self.clients = {"bootstrap": self.client}
        self.session_windows = {"bootstrap": int(time.time() * 1000)}
        self.evidence, self.early_evidence = pair.evidence, []
        self.current, self.unsettled = "binding", False
        self.call_records, self.actions, self.normalizations, self.approved = [], {}, [], []
        self.actor = None
        self.principal = "connector_" + digest(digest(str(self.root))[:48])[:32]
        self.expected_actor = "actor_" + digest(self.principal)[:32]
        self.aliases = {str(self.root): "<project-root:contract>",
            str(self.creation_root): "<private-creation-root>",
            self.expected_actor: "<validated-actor>"}
        self.bind_instruction_directory()
        self.root_identity = (self.root.stat().st_dev, self.root.stat().st_ino)
        self.creation_identity = (self.creation_root.stat().st_dev, self.creation_root.stat().st_ino)
        self.catalog, self.catalog_hash = catalog_object(self.catalog_path)
        require(len(self.catalog["projects"]) == 1 and self.catalog["defaultProject"] == "contract"
                and Path(self.catalog["projects"][0]["root"]).resolve() == self.root,
                "direct-helper pair must contain exactly its owned default project")
        self.workspaces, self.opened, self.initial_shape = {}, {}, None
        self.bind_projects(self.data(self.mcp("list_projects", {})))
        self.bind_fixture("contract", self.root)
        self.inventory_response = self.mcp_rpc("tools/list", {})
        self.tools = [item["name"] for item in raw_result(self.inventory_response)["tools"]]
        require({"open_current_workspace", "list_workspaces", "import_file", "view_image"} <= set(self.tools),
                "direct full-tier fixture lacks required retained tools")
        cfg = self.data(self.mcp("server_config", {}))
        require(cfg.get("worktreeMode") == "off" and cfg.get("toolMode") == "full"
                and cfg.get("bashMode") == "full" and cfg.get("writeMode") == "workspace"
                and cfg.get("maxImportBytes") == IMPORT_LIMIT and cfg.get("connectionTest") is False,
                "owned process did not apply the explicit direct/import fixture configuration")
        jobs = self.data(self.mcp("jobs", {"workspace_id": self.workspace, "wait_ms": 0}))
        require(jobs.get("jobs") == [], "fresh direct pair has unexpected job history")
        self.import_root = self.root / IMPORT_PREFIX
        self.import_root.mkdir(mode=0o700)

    def bind_instruction_directory(self):
        # Both owned drivers explicitly launch with state/empty-codex. Reuse
        # its PID/starttick and actual-process binding, without aliasing [home]
        # generally or changing any tool-mode field or summary text.
        aliases, directory = owned_instruction_aliases(self.manifest)
        self.aliases.update(aliases)
        self.normalizations.append({"case": "binding",
            "rule": "exact owned process codex-dir and its source [home] spelling",
            "codex_dir": directory})

    def alive(self):
        self.pair.check_time()
        self.server.verify()
        super().alive()

    def session(self, name):
        if name not in self.clients:
            self.session_windows[name] = int(time.time() * 1000)
            self.clients[name] = Client(self.endpoint, str(self.token), timeout=8)
        return self.clients[name]

    def current_open(self, session, *, tree=False):
        value = self.open_and_normalize({"include_tree": tree, "max_depth": 1,
            "include_skills": False, "include_global_skills": False},
            session, tool="open_current_workspace")
        result = raw_result(value)
        require(result.get("isError") is not True, "open_current_workspace returned a business error")
        data = result["structuredContent"]
        require(data.get("workspace_id") == "<workspace:contract>"
                and data.get("selected_workspace_id") == "<workspace:contract>"
                and data.get("project_id") == "contract" and data.get("tool_mode") == "full"
                and data.get("bash_mode") == "full" and data.get("write_mode") == "workspace",
                "open_current_workspace lost selected/default/full-permission identity")
        return value

    def workspace_list(self, session, *, expected_count=None, first=False):
        response = self.mcp("list_workspaces", {}, session)
        value = copy.deepcopy(response)
        data = self.data(value)
        rows = data.get("workspaces")
        require(isinstance(rows, list) and len(rows) <= 1 and data.get("count") == len(rows),
                "single-project session list contains unexpected workspaces")
        require(data.get("selected_workspace_id") in (None, self.workspace),
                "session selected an unbound workspace")
        if expected_count is not None:
            require(len(rows) == expected_count and data.get("selected_workspace_id") == self.workspace,
                    "open_current_workspace did not register/select the workspace in its own session")
        shape = (len(rows), data.get("selected_workspace_id"))
        if first and self.initial_shape is None:
            self.initial_shape = shape
        elif first:
            require(shape == self.initial_shape, "an opened workspace leaked into a new session")
        for item in rows:
            require(item.get("id") == self.workspace and item.get("projectId") == "contract",
                    "listed workspace is not the bound default project")
            when = timestamp(item.get("openedAt"))
            require(self.session_windows[session] - 1000 <= when <= int(time.time() * 1000) + 1000,
                    "openedAt is outside its owning session lifetime")
            if session in self.opened:
                require(item["openedAt"] == self.opened[session],
                        "repeated select/list changed the existing session's open time")
            else:
                self.opened[session] = item["openedAt"]
            original = item["openedAt"]
            item["openedAt"] = "<validated-openedAt:contract>"
            blocks = value["body"]["result"]["content"]
            require(len(blocks) == 1 and blocks[0].get("type") == "text"
                    and blocks[0]["text"].endswith("(opened " + original + ")"),
                    "workspace list text is not bound to its openedAt field")
            blocks[0]["text"] = blocks[0]["text"][:-len(original)-1] + "<validated-openedAt:contract>)"
        return self.normalize_mcp(value)

    def file_bytes(self, relative, *, missing=False):
        path = self.root / relative
        require(path.parent == self.import_root and not path.is_symlink(),
                "import proof path is not a direct child of its owned directory")
        if not path.exists():
            require(missing, "expected imported destination does not exist")
            return None
        require(path.is_file() and path.resolve().parent == self.import_root
                and path.stat().st_size <= IMPORT_LIMIT, "import destination escaped its byte/path budget")
        return path.read_bytes()

    def import_one(self, route, filename, payload, *, expected=False, declared=None,
                   detected=None, mime_status="unknown", overwrite=False, error=False,
                   wrong_hash=False, file_name=None):
        relative = IMPORT_PREFIX + "/" + filename
        previous = self.file_bytes(relative, missing=True)
        reference = {"download_url": self.pair.attachments.origin + route,
                     "file_id": "fixture-" + filename}
        if declared is not None:
            reference["mime_type"] = declared
        if file_name is not None:
            reference["file_name"] = file_name
        args = {"workspace_id": self.workspace, "file": reference, "destination": relative}
        if expected or wrong_hash:
            args["expected_sha256"] = "0" * 64 if wrong_hash else sha(payload)
        if overwrite:
            args["overwrite"] = True
        offset = self.pair.attachments.count()
        response = self.mcp("import_file", args, "alpha")
        requests = self.pair.attachments.since(offset)
        expected_requests = ([route, "/png"] if route == "/redirect"
                             else [route] * 6 if route == "/redirect-loop" else [route])
        require([row.get("path") for row in requests] == expected_requests
                and not any(row.get("credential_forwarded") for row in requests),
                "import did not use exactly the declared uncredentialed attachment fixture")
        result = raw_result(response)
        require((result.get("isError") is True) is error,
                "import success/error outcome differs from its explicit case")
        if hasattr(self, "server"):
            require(not list((self.server.state / "tmp").glob("codexpro-import-*")),
                    "attachment import left an owned temporary download behind")
        after = self.file_bytes(relative, missing=True)
        if error:
            require(after == previous, "failed attachment import changed the destination")
        else:
            require(after == payload, "imported raw bytes differ from the served attachment")
            data = self.data(response)
            _, headers, _ = request_spec("/png" if route == "/redirect" else route)
            expected_declared = declared.strip() if declared is not None else headers.get("Content-Type", "").split(";", 1)[0].strip()
            fields = {"workspace_id": self.workspace, "path": relative,
                "bytes": len(payload), "sha256": sha(payload), "file_id": reference["file_id"],
                "declared_mime_type": expected_declared or None, "detected_mime_type": detected,
                "mime_type_status": mime_status, "verified": expected,
                "overwritten": overwrite and previous is not None,
                "file_name": file_name.strip() if file_name is not None else None}
            for key, wanted in fields.items():
                require(key in data and type(data[key]) is type(wanted) and data[key] == wanted,
                        "import result disagrees with the actual fixture bytes/metadata: " + key)
        return {"response": self.normalize_mcp(response), "fixture_requests": requests,
                "destination": None if after is None else {"bytes": len(after), "sha256": sha(after)}}

    def invalid_url_import(self):
        relative = IMPORT_PREFIX + "/invalid-url.bin"
        require(self.file_bytes(relative, missing=True) is None,
                "invalid-URL fixture destination already exists")
        offset = self.pair.attachments.count()
        response = self.mcp("import_file", {"workspace_id": self.workspace,
            "destination": relative,
            "file": {"download_url": "not a URL", "file_id": "fixture-invalid-url"}}, "alpha")
        requests = self.pair.attachments.since(offset)
        require(requests == [], "invalid URL reached the attachment fixture")
        expected = {"isError": True,
            "content": [{"type": "text", "text": "TypeError: Invalid URL"}],
            "structuredContent": {"codexpro_tool": "import_file",
                "codexpro_title": "Import Attachment File", "error": "TypeError: Invalid URL"}}
        require(raw_result(response) == expected,
                "invalid URL lost its exact native TypeError envelope or acquired domain metadata")
        require(self.file_bytes(relative, missing=True) is None,
                "invalid URL created a destination")
        if hasattr(self, "server"):
            require(not list((self.server.state / "tmp").glob("codexpro-import-*")),
                    "invalid URL left a temporary download behind")
        return {"response": self.normalize_mcp(response), "fixture_requests": requests,
                "destination": None}

    def view_import(self):
        response = self.mcp("view_image", {"workspace_id": self.workspace,
            "path": IMPORT_PREFIX + "/image.png"}, "alpha")
        data = self.data(response)
        require(data.get("bytes") == len(PNG) and data.get("sha256") == sha(PNG)
                and data.get("width") == 1 and data.get("height") == 1
                and data.get("mime_type") == "image/png", "imported PNG metadata changed")
        images = [block for block in raw_result(response)["content"] if block.get("type") == "image"]
        require(len(images) == 1 and images[0].get("mimeType") == "image/png"
                and base64.b64decode(images[0].get("data", ""), validate=True) == PNG,
                "view_image did not return the exact imported image block")
        return self.normalize_mcp(response)


def case_function(index):
    functions = [
        lambda s: s.workspace_list("alpha", first=True),
        lambda s: s.current_open("alpha"),
        lambda s: s.workspace_list("alpha", expected_count=1),
        lambda s: s.current_open("alpha", tree=True),
        lambda s: s.workspace_list("alpha", expected_count=1),
        lambda s: s.workspace_list("beta", first=True),
        lambda s: s.current_open("beta"),
        lambda s: [s.workspace_list("alpha", expected_count=1), s.workspace_list("beta", expected_count=1)],
        lambda s: s.import_one("/text", "text.txt", TEXT, declared=" text/plain ", file_name=" note.txt "),
        lambda s: s.import_one("/binary", "binary.bin", BINARY),
        lambda s: s.import_one("/png", "image.png", PNG, expected=True, detected="image/png", mime_status="matched"),
        lambda s: s.view_import(),
        lambda s: s.import_one("/replacement", "text.txt", REPLACEMENT, error=True),
        lambda s: s.import_one("/replacement", "text.txt", REPLACEMENT, overwrite=True),
        lambda s: s.import_one("/redirect", "redirect.png", PNG, detected="image/png", mime_status="matched"),
        lambda s: s.import_one("/text", "hash-failure.txt", TEXT, wrong_hash=True, error=True),
        lambda s: s.import_one("/png", "mime-mismatch.png", PNG, declared="application/pdf",
                             detected="image/png", mime_status="mismatched"),
        lambda s: s.import_one("/exact", "exact.bin", b"e" * IMPORT_LIMIT),
        lambda s: s.import_one("/oversized", "header-overflow.bin", b"x" * (IMPORT_LIMIT + 1), error=True),
        lambda s: s.import_one("/streamed", "stream-overflow.bin", b"y" * (IMPORT_LIMIT + 1), error=True),
        lambda s: s.import_one("/http-not-found", "not-found.bin", b"", error=True),
        lambda s: s.import_one("/missing-location", "missing-location.bin", b"", error=True),
        lambda s: s.import_one("/redirect-loop", "redirect-loop.bin", b"", error=True),
        lambda s: s.invalid_url_import(),
    ]
    return functions[index]


class Pair:
    def __init__(self, release, binary, parent, output, *, activity_supplement=False):
        self.release, self.binary = Path(release).resolve(strict=True), Path(binary).resolve(strict=True)
        require(self.binary.is_file() and os.access(self.binary, os.X_OK), "candidate is not executable")
        package = json.loads((self.release / "package.json").read_text())
        require(package.get("version") == "0.31.0" and "44bb76b" in self.release.name,
                "reference must be the immutable 0.31.0/44bb76b release")
        require((self.release / "dist" / "http.js").resolve(strict=True).is_relative_to(self.release),
                "reference HTTP entry escapes its immutable release")
        self.parent, self.output = private_path(parent, missing=True), artifact_path(output)
        require(Path(parent).expanduser().absolute() == self.parent, "private state path contains a symlink or traversal")
        allowed = (Path.home() / ".codexpro-go-proof").resolve()
        require(self.parent.is_relative_to(allowed) and self.parent != allowed
                or str(self.parent).startswith("/tmp/sp6-retained-gaps-"), "private proof state root is not allowed")
        require(not self.parent.exists() and not self.output.exists(), "proof refuses existing state or output")
        verify_policy(IMPORT_POLICY)
        assert_free_ports()  # Before any directory, credential, fixture listener or child.
        self.parent.mkdir(parents=True, mode=0o700)
        self.output.parent.mkdir(parents=True, exist_ok=True)
        self.run_id, self.deadline = secrets.token_hex(12), time.monotonic() + 330
        self.binary_hash = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.servers, self.sides, self.attachments = [], [], None
        self.activity_supplement = activity_supplement
        self.evidence = Evidence(self.parent / "retained-gaps-raw.jsonl.gz", self.all_clients)
        self.report = {"schema_version": 1, "source_commit": SOURCE_COMMIT,
            "approval_commit": APPROVAL_COMMIT, "started_at": iso_now(),
            "state_parent": str(self.parent), "binary_sha256": self.binary_hash,
            "reference_entry_sha256": sha((self.release / "dist" / "http.js").read_bytes()),
            "permission_fixture": {"reference": "off/full", "candidate": "off/full",
                "tool_tier": "full", "project_count": 1},
            "import_policy": IMPORT_POLICY, "max_import_bytes": IMPORT_LIMIT,
            "cases": [], "cleanup": [], "cleanup_errors": [], "stage4_complete": False}
        private_http()

    def all_clients(self):
        clients = [client for side in self.sides for client in side.clients.values()]
        for server in self.servers:
            if server.client and server.client not in clients:
                clients.append(server.client)
        return clients

    def check_time(self):
        require(time.monotonic() < self.deadline, "retained-gap driver exceeded its 330-second budget")

    def run(self):
        try:
            self.attachments = AttachmentFixture(self.deadline)
            for label in ("reference", "candidate"):
                server = DirectServer(self, label)
                self.servers.append(server)
                server.start()
                self.sides.append(Side(self, server))
            require(self.servers[0].main_commit == self.servers[1].main_commit,
                    "private fixture Git histories differ")
            self.report["inventory"] = compare_inventory(
                self.sides[0].inventory_response, self.sides[1].inventory_response)
            require(self.report["inventory"]["status"] != "fail", "retained full-tier descriptors differ")
            for index, case in enumerate(DIRECT_CASES + IMPORT_CASES):
                values, errors = {}, {}
                for side in self.sides:
                    self.check_time()
                    side.current = case
                    try:
                        values[side.label] = case_function(index)(side)
                    except Exception as error:
                        errors[side.label] = safe_error(error, self.all_clients())
                diff = None if errors else difference(values["reference"], values["candidate"])
                record = {"id": case, "status": "failed" if errors or diff else "unchanged", "errors": errors}
                if diff:
                    record["difference"] = diff
                self.report["cases"].append(record)
                self.evidence.append({"case": case, "normalized": values, "comparison": record})
                if errors:
                    self.report["aborted_after"] = case
                    break
            self.report["fixture_digests"] = {}
            for side in self.sides:
                files = {p.name: {"bytes": len(p.read_bytes()), "sha256": sha(p.read_bytes())}
                         for p in sorted(side.import_root.iterdir()) if p.is_file() and not p.is_symlink()}
                self.report["fixture_digests"][side.label] = files
            if len(self.report["cases"]) == len(DIRECT_CASES + IMPORT_CASES) and not self.report.get("aborted_after"):
                expected_files = {"text.txt": REPLACEMENT, "binary.bin": BINARY,
                    "image.png": PNG, "redirect.png": PNG, "mime-mismatch.png": PNG,
                    "exact.bin": b"e" * IMPORT_LIMIT}
                expected = {name: {"bytes": len(raw), "sha256": sha(raw)}
                            for name, raw in expected_files.items()}
                require(all(value == expected for value in self.report["fixture_digests"].values()),
                        "final import directory contains missing, extra or different files")
            if self.activity_supplement and len(self.report["cases"]) == len(DIRECT_CASES + IMPORT_CASES) \
                    and all(row["status"] == "unchanged" for row in self.report["cases"]):
                from activity_routes_contract_suite import run_existing_pair
                require(time.monotonic() + 150 <= self.deadline,
                        "full 150-second activity-supplement budget does not remain; refusing partial execution")
                self.report["activity_supplement"] = run_existing_pair(
                    self.servers[0].state / "launch.json", self.servers[1].state / "launch.json",
                    self.parent / "activity-routes")
        except Exception as error:
            self.report["fatal_error"] = safe_error(error, self.all_clients())
        finally:
            self.report["normalizations"] = {side.label: side.normalizations for side in self.sides}
            for client in self.all_clients():
                try:
                    client.close()
                except Exception as error:
                    self.report["cleanup_errors"].append(safe_error(error, self.all_clients()))
            for server in reversed(self.servers):
                try:
                    self.report["cleanup"].append({"side": server.label, **server.stop()})
                except Exception as error:
                    self.report["cleanup_errors"].append(safe_error(error, self.all_clients()))
            if self.attachments:
                try:
                    self.attachments.close()
                    self.report["attachment_fixture_stopped"] = True
                    self.report["attachment_requests"] = self.attachments.since(0)
                except Exception as error:
                    self.report["cleanup_errors"].append(safe_error(error, self.all_clients()))
            self.evidence.close()
            self.report["raw_evidence"] = {"path": str(self.evidence.path), "records": self.evidence.records,
                "uncompressed_bytes": self.evidence.bytes, "sha256": sha(self.evidence.path.read_bytes())}
            self.report["finished_at"] = iso_now()
            self.report["unchanged"] = sum(row["status"] == "unchanged" for row in self.report["cases"])
            self.report["failed"] = sum(row["status"] == "failed" for row in self.report["cases"])
            self.report["all_cases_completed"] = len(self.report["cases"]) == len(DIRECT_CASES + IMPORT_CASES)
            supplemental = self.report.get("activity_supplement", {})
            self.report["passed"] = (not self.report.get("fatal_error") and not self.report["failed"]
                and self.report["all_cases_completed"] and not self.report["cleanup_errors"]
                and (not self.activity_supplement or supplemental.get("passed") is True))
            for client in self.all_clients():
                client.assert_no_credential(self.report)
            durable_json(self.output, self.report)
        return self.report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("release", "binary", "state-parent", "output"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--activity-supplement", action="store_true")
    args = parser.parse_args()
    pair = Pair(args.release, args.binary, args.state_parent, args.output,
                activity_supplement=args.activity_supplement)
    def interrupted(signum, frame):
        raise ContractFailure(f"retained-gap proof interrupted by signal {signum}; cleaning up only owned children")
    old = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        report = pair.run()
    finally:
        for sig, handler in old.items():
            signal.signal(sig, handler)
    print(json.dumps({"output": str(pair.output), "passed": report["passed"],
        "unchanged": report["unchanged"], "failed": report["failed"],
        "all_cases_completed": report["all_cases_completed"],
        "cleanup_errors": len(report["cleanup_errors"])}))
    if not report["passed"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
