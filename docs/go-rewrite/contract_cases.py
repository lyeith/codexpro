"""Differential scenarios for retained file, search, analysis and Git tools.

Execute every workflow independently against both private servers. No credentials
or HTTP session identifiers belong in fixtures or output. Resolve per-server
bindings before each call. Dynamic pointers identify a concrete value whose shape
must be validated; they never authorize dropping surrounding text or envelopes.

Optional harness setup extensions:
- fixture_mtime_ns: identical timestamps after seeding all files.
- fixtures_base64: exact decoded binary fixtures.
- setup.git: fresh repo, local identity, named tracked paths, fixed initial date.
- step.session: a named MCP session on the SAME server/principal.
- step.bind: JSON pointer inside the raw JSON-RPC response body.
Report unsupported setup as SKIPPED, never PASS.
"""
from __future__ import annotations

WORKSPACE = {"$ref": "workspace"}
FIXTURE_MTIME_NS = 1735689600000000000
GIT_SETUP = {
    "branch": "main",
    "user_name": "CodexPro contract fixture",
    "user_email": "contract@example.invalid",
    "date": "2025-01-01T00:00:00+00:00",
    "tracked": ["a.txt", "b.txt"],
}


def step(identifier, tool, args, *, bind=None, dynamic=(), expect_error=False, session=None):
    value = {
        "id": identifier,
        "tool": tool,
        "args": {"workspace_id": WORKSPACE, **args},
        "bind": bind or {},
        "dynamic": list(dynamic),
        "expect_error": expect_error,
    }
    if session is not None:
        value["session"] = session
    return value


def workflow(identifier, fixtures, steps, **extras):
    return {
        "id": identifier,
        "fixtures": fixtures,
        "fixture_mtime_ns": FIXTURE_MTIME_NS,
        "steps": steps,
        **extras,
    }


TAG = "/result/structuredContent/edit_tag"
SHA = "/result/structuredContent/sha256"
CURSOR = "/result/structuredContent/next_cursor"
CACHE = "/result/structuredContent/cache/key"
ANALYSIS_CACHE = "/result/structuredContent/analysis/cache/key"

CASES = [
    workflow("FILE-UTF8-CRLF-01", {"unicode.txt": "\ufeffalpha\r\ncafé ✓ 😀\r\nlast\r\n"}, [
        step("full", "read", {"path": "unicode.txt"}, bind={"unicode_tag": TAG, "unicode_sha": SHA}),
        step("page", "read", {"path": "unicode.txt", "start_line": 2, "end_line": 2}),
        step("empty-tail", "read", {"path": "unicode.txt", "start_line": 4}),
        step("past-eof", "read", {"path": "unicode.txt", "start_line": 50}, expect_error=True),
    ]),
    workflow("FILE-PAGINATION-02", {"long.txt": "A" * 900 + "\n" + "B" * 900 + "\n" + "C" * 900 + "\nend\n"}, [
        step("first", "read", {"path": "long.txt", "max_bytes": 1000},
             bind={"next_line": "/result/structuredContent/next_start_line", "paged_tag": TAG}),
        step("second", "read", {"path": "long.txt", "max_bytes": 1000, "start_line": {"$ref": "next_line"}},
             bind={"last_line": "/result/structuredContent/next_start_line"}),
        step("tail", "read", {"path": "long.txt", "max_bytes": 1000, "start_line": {"$ref": "last_line"}}),
    ]),
    workflow("FILE-TAG-PRINCIPAL-03", {"a.txt": "one\ntwo\nthree\n"}, [
        step("read-first", "read", {"path": "a.txt", "start_line": 1, "end_line": 1},
             bind={"first_tag": TAG}, session="reader"),
        step("unseen", "edit", {"path": "a.txt", "edit_tag": {"$ref": "first_tag"},
             "edits": [{"op": "replace", "start_line": 2, "content": "TWO"}]},
             expect_error=True, session="editor"),
        step("read-second", "read", {"path": "a.txt", "start_line": 2, "end_line": 2}, session="reader"),
        step("cross-session-edit", "edit", {"path": "a.txt", "edit_tag": {"$ref": "first_tag"},
             "edits": [{"op": "replace", "start_line": 2, "content": "TWO"}]},
             bind={"new_tag": TAG}, session="editor"),
        step("stale", "edit", {"path": "a.txt", "edit_tag": {"$ref": "first_tag"},
             "edits": [{"op": "replace", "start_line": 1, "content": "ONE"}]},
             expect_error=True, session="reader"),
        step("new-tag-unseen", "edit", {"path": "a.txt", "edit_tag": {"$ref": "new_tag"},
             "edits": [{"op": "replace", "start_line": 1, "content": "ONE"}]},
             expect_error=True, session="editor"),
        step("verify", "read", {"path": "a.txt"}, session="editor"),
    ]),
    workflow("FILE-EDIT-COORDINATES-04", {"a.txt": "\ufeffa\r\nb\r\nc\r\nd\r\n"}, [
        step("read", "read", {"path": "a.txt"}, bind={"edit_tag": TAG}),
        step("multi-hunk", "edit", {"path": "a.txt", "edit_tag": {"$ref": "edit_tag"}, "edits": [
            {"op": "replace", "start_line": 1, "content": "A\nB"},
            {"op": "delete", "start_line": 3},
            {"op": "insert_before", "line": 4, "content": "X"},
        ]}),
        step("verify", "read", {"path": "a.txt"}, bind={"edited_tag": TAG}),
        step("no-op", "edit", {"path": "a.txt", "edit_tag": {"$ref": "edited_tag"},
             "edits": [{"op": "replace", "start_line": 2, "content": "B"}]}, expect_error=True),
        step("overlap", "edit", {"path": "a.txt", "edit_tag": {"$ref": "edited_tag"}, "edits": [
            {"op": "delete", "start_line": 1, "end_line": 3},
            {"op": "replace", "start_line": 2, "content": "overlap"},
        ]}, expect_error=True),
    ]),
    workflow("FILE-WRITE-CAS-05", {"a.txt": "old\n"}, [
        step("read", "read", {"path": "a.txt"}, bind={"before_sha": SHA}),
        step("write", "write", {"path": "a.txt", "content": "new\n", "expected_sha256": {"$ref": "before_sha"}}),
        step("stale-sha", "write", {"path": "a.txt", "content": "lost update\n",
             "expected_sha256": {"$ref": "before_sha"}}, expect_error=True),
        step("exclusive", "write", {"path": "a.txt", "content": "no\n", "overwrite": False}, expect_error=True),
        step("new-nested", "write", {"path": "nested/new.txt", "content": "created\n", "overwrite": False}),
        step("verify", "read", {"path": "a.txt"}),
    ]),
    workflow("FILE-GUARDS-06", {"visible.txt": "safe\n", ".env": "not a real credential\n"}, [
        step("read-secret", "read", {"path": ".env"}, expect_error=True),
        step("parent-escape", "read", {"path": "../outside"}, expect_error=True),
        step("write-parent", "write", {"path": "../outside", "content": "x"}, expect_error=True),
        step("missing", "read", {"path": "missing.txt"}, expect_error=True),
        step("directory", "read", {"path": "."}, expect_error=True),
        step("tree", "tree", {"include_hidden": True}),
    ]),
    workflow("FILE-TREE-07", {
        "a/child.txt": "x\n", "a/deep/leaf.txt": "y\n", "B.txt": "B\n",
        "é.txt": "e\n", ".visible": "dot\n", "node_modules/ignored.js": "ignored\n",
    }, [
        step("default", "tree", {}),
        step("hidden", "tree", {"include_hidden": True, "max_depth": 1}),
        step("bounded", "tree", {"max_entries": 2}),
        step("file-root", "tree", {"path": "B.txt"}, expect_error=True),
    ]),
    workflow("FILE-NATIVE-PATCH-08", {"a.txt": "old\n", "remove.txt": "delete\n"}, [
        step("native", "apply_patch", {"patch": "*** Begin Patch\n*** Update File: a.txt\n@@\n-old\n+new\n*** Add File: added.txt\n+created\n*** Delete File: remove.txt\n*** End Patch"}),
        step("verify-updated", "read", {"path": "a.txt"}),
        step("verify-added", "read", {"path": "added.txt"}),
        step("stale-preflight", "apply_patch", {"patch": "*** Begin Patch\n*** Update File: a.txt\n@@\n-new\n+should not apply\n*** Delete File: missing.txt\n*** End Patch"}, expect_error=True),
        step("verify-atomic-preflight", "read", {"path": "a.txt"}),
        step("move", "apply_patch", {"patch": "*** Begin Patch\n*** Update File: added.txt\n*** Move to: nested/moved.txt\n@@\n-created\n+moved\n*** End Patch"}),
        step("verify-moved", "read", {"path": "nested/moved.txt"}),
    ]),
    workflow("FILE-UNIFIED-PATCH-09", {"a.txt": "old\n"}, [
        step("unified", "apply_patch", {"patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"}),
        step("verify", "read", {"path": "a.txt"}),
        step("stale", "apply_patch", {"patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+again\n"}, expect_error=True),
        step("escape", "apply_patch", {"patch": "--- /dev/null\n+++ b/../escape.txt\n@@ -0,0 +1 @@\n+no\n"}, expect_error=True),
    ]),
    workflow("FILE-SKILLS-10", {
        ".codex/skills/demo/SKILL.md": "---\nname: demo\ndescription: \"Primary skill\"\n---\nPrimary body.\n",
        ".agents/skills/demo/SKILL.md": "---\nname: demo\n---\nShadowed body.\n",
        "skills/other/SKILL.md": "---\nname: other\n---\nOther body.\n",
    }, [
        step("primary", "load_skill", {"name": "demo", "include_global_skills": False}),
        step("exact-shadow", "load_skill", {"name": "demo", "source": "workspace",
             "path": "$WORKSPACE/.agents/skills/demo/SKILL.md", "include_global_skills": False}, expect_error=True),
        step("other", "load_skill", {"name": "other", "include_global_skills": False}),
        step("missing", "load_skill", {"name": "missing", "include_global_skills": False}, expect_error=True),
    ]),
    workflow("FILE-IMAGE-11", {"not-image.txt": "plain text\n"}, [
        step("image", "view_image", {"path": "pixel.png"}),
        step("bad-image", "view_image", {"path": "not-image.txt"}, expect_error=True),
    ], fixtures_base64={
        "pixel.png": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6ioAAAAAASUVORK5CYII="
    }),
    workflow("SEARCH-CURSOR-EDIT-12", {"a.txt": "needle one\nneedle two\nneedle three\n"}, [
        step("first", "search", {"query": "needle", "max_results": 1, "context_before": 0, "context_after": 0},
             bind={"search_cursor": CURSOR, "search_tag": "/result/structuredContent/matches/0/edit_tag"},
             dynamic=("/result/structuredContent/query_fingerprint", CURSOR)),
        step("second", "search", {"query": "needle", "max_results": 1, "context_before": 0, "context_after": 0,
             "cursor": {"$ref": "search_cursor"}}, dynamic=("/result/structuredContent/query_fingerprint", CURSOR)),
        step("cursor-mismatch", "search", {"query": "other", "max_results": 1, "context_before": 0,
             "context_after": 0, "cursor": {"$ref": "search_cursor"}}, expect_error=True),
        step("edit-shown", "edit", {"path": "a.txt", "edit_tag": {"$ref": "search_tag"},
             "edits": [{"op": "replace", "start_line": 1, "content": "changed"}]}),
        step("verify", "read", {"path": "a.txt"}),
    ]),
    workflow("SEARCH-REGEX-13", {"a.txt": "Needle\nneedle\nother\n", ".hidden.txt": "needle\n"}, [
        step("regex", "search", {"query": "(?i)needle", "regex": True, "context_before": 0, "context_after": 0},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("hidden", "search", {"query": "needle", "include_hidden": True, "group_by_file": False},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("invalid-regex", "search", {"query": "[", "regex": True}, expect_error=True),
        step("regex-config", "search", {"query": "x", "regex": True, "kind": "config"}, expect_error=True),
    ]),
    workflow("SEARCH-CONFIG-14", {
        "config.jsonc": '{\n // comment\n "routes": [{"path": "/one"}, {"path": "/two"}],\n "a.b": {"x/y": true},\n}\n',
        "config.yaml": "server:\n  host: localhost\n  port: 8080\n",
        "config.toml": '[server]\nhost = "localhost"\nport = 8080\n',
    }, [
        step("json-wildcard", "search", {"query": "routes[*].path", "kind": "config", "glob": "*.jsonc"},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("json-pointer", "search", {"query": "/a.b/x~1y", "kind": "config", "glob": "*.jsonc"},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("yaml", "search", {"query": "server.port", "kind": "config", "glob": "*.yaml"},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("toml", "search", {"query": "server.port", "kind": "config", "glob": "*.toml"},
             dynamic=("/result/structuredContent/query_fingerprint",)),
    ]),
    workflow("ANALYSIS-SCOPE-15", {
        "src/auth.ts": "export function login() {}\n",
        "src/main.ts": "import { login } from './auth.js';\nlogin();\n",
        "tests/auth.test.ts": "import { login } from '../src/auth.js';\nlogin();\n",
        "package.json": '{"scripts":{"test":"node test.js"}}\n',
    }, [
        step("scoped", "inspect_workspace", {"path": "src", "max_symbols": 1}, dynamic=(CACHE,)),
        step("cached", "inspect_workspace", {"path": "src", "include_symbols": False, "include_relationships": False},
             dynamic=(CACHE,)),
        step("grouped", "search", {"query": "login", "intent": "references", "include_tests": True, "path": "src"},
             dynamic=("/result/structuredContent/query_fingerprint", ANALYSIS_CACHE)),
    ]),
    workflow("AST-PATTERN-16", {"code.ts": 'console.log("first");\nconsole.log("✓");\n'}, [
        step("first", "ast_grep", {"pattern": "console.log($ARG)", "language": "typescript", "max_results": 1,
             "context_before": 0, "context_after": 0},
             bind={"ast_cursor": CURSOR, "ast_tag": "/result/structuredContent/matches/0/edit_tag"},
             dynamic=("/result/structuredContent/query_fingerprint", CURSOR)),
        step("second", "ast_grep", {"pattern": "console.log($ARG)", "language": "typescript", "max_results": 1,
             "context_before": 0, "context_after": 0, "cursor": {"$ref": "ast_cursor"}},
             dynamic=("/result/structuredContent/query_fingerprint", CURSOR)),
        step("edit", "edit", {"path": "code.ts", "edit_tag": {"$ref": "ast_tag"},
             "edits": [{"op": "replace", "start_line": 2, "content": 'console.log("edited");'}]}),
        step("verify", "read", {"path": "code.ts"}),
        step("both-modes", "ast_grep", {"pattern": "x", "kind": "identifier"}, expect_error=True),
    ]),
    workflow("AST-KIND-17", {"code.ts": "export function greet() { return 1; }\n"}, [
        step("kind", "ast_grep", {"kind": "function_declaration", "language": "typescript"},
             dynamic=("/result/structuredContent/query_fingerprint",)),
        step("selector-without-pattern", "ast_grep", {"kind": "identifier", "selector": "identifier"}, expect_error=True),
        step("strictness-without-pattern", "ast_grep", {"kind": "identifier", "strictness": "ast"}, expect_error=True),
        step("missing-mode", "ast_grep", {}, expect_error=True),
    ]),
    workflow("GIT-REVIEW-SESSION-18", {"a.txt": "old a\n", "b.txt": "old b\n"}, [
        step("modify", "write", {"path": "a.txt", "content": "new a\n"}),
        step("first", "show_changes", {"since": "last_shown"}, session="reviewer",
             dynamic=(ANALYSIS_CACHE,)),
        step("repeat", "show_changes", {"since": "last_shown"}, session="reviewer"),
        step("new-session", "show_changes", {"since": "last_shown"}, session="other",
             dynamic=(ANALYSIS_CACHE,)),
        step("full", "show_changes", {"since": "workspace"}, session="reviewer",
             dynamic=(ANALYSIS_CACHE,)),
        step("omit", "show_changes", {"include_diff": False, "mark_reviewed": False}, session="fresh",
             dynamic=(ANALYSIS_CACHE,)),
        step("after-omit", "show_changes", {"since": "last_shown"}, session="fresh",
             dynamic=(ANALYSIS_CACHE,)),
    ], setup={"git": GIT_SETUP}),
    workflow("GIT-COMMIT-EXPLICIT-19", {"a.txt": "old a\n", "b.txt": "old b\n"}, [
        step("modify", "write", {"path": "a.txt", "content": "new a\n"}),
        step("review", "show_changes", {}, dynamic=(ANALYSIS_CACHE,)),
        step("commit", "commit_changes", {"message": "fix: explicit contract fixture", "paths": ["a.txt"]},
             bind={"commit": "/result/structuredContent/commit", "commit_short": "/result/structuredContent/commit_short"},
             dynamic=("/result/structuredContent/commit", "/result/structuredContent/commit_short")),
        step("verify-clean", "show_changes", {}),
        step("nothing-to-commit", "commit_changes", {"message": "fix: no change"}, expect_error=True),
    ], setup={"git": GIT_SETUP}),
    workflow("FILE-IMPORT-NEGATIVE-20", {}, [
        step("bare-string", "import_file", {"file": "https://example.invalid/file", "destination": "download.bin"},
             expect_error=True),
        step("empty-reference", "import_file", {"file": {"download_url": "", "file_id": ""}, "destination": "download.bin"},
             expect_error=True),
        step("blocked-origin", "import_file", {"file": {"download_url": "https://example.invalid/file", "file_id": "fixture-file"},
             "destination": "download.bin"}, expect_error=True),
        step("non-https", "import_file", {"file": {"download_url": "http://example.invalid/file", "file_id": "fixture-file"},
             "destination": "download.bin"}, expect_error=True),
        step("file-scheme", "import_file", {"file": {"download_url": "file:///tmp/fixture", "file_id": "fixture-file"},
             "destination": "download.bin"}, expect_error=True),
    ]),
]

# Additional coverage remains separate from the first twenty:
# same-inode/hardlink writes, malformed UTF-8 bytes, inode/mtime race injection,
# import success/redirect/size/hash/MIME with the isolated local HTTP fixture,
# native command missing/timeout/output-cap/failure, and full Git diff-target
# matrix. Go tests cover many of these; they still need differential records
# before stage 4 can claim complete parity.
