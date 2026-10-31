#!/usr/bin/env python3
"""Evaluate exact captured FileLeaseStore predicate without initializing it."""
from __future__ import annotations
import argparse
import base64
import copy
import hashlib
import json
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
SOURCE = HERE.parent / "sp6-reference" / "leaseStore-source.ts"
EXPECTED = "ac01aa3a57e4e6fdbeb19102b62d4052f8c057c526818bea0db918b32003c515"

def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=SOURCE)
    parser.add_argument("--output", type=Path, default=HERE / "testdata" / "source-lease-oracle.json")
    args = parser.parse_args()
    source = args.source.read_bytes()
    if hashlib.sha256(source).hexdigest() != EXPECTED:
        raise RuntimeError("exact captured lease store changed")
    text = source.decode()
    predicate = text[text.index("function isLease("):text.index("\nexport class FileLeaseStore")]
    predicate = predicate.replace("function isLease(value: unknown): value is WorktreeLease", "function isLease(value)")
    predicate = predicate.replace("value as Partial<WorktreeLease>", "value")
    base = {
        "version": 2, "revision": 7, "workspaceId": "wt_fixture",
        "projectId": "contract", "repositoryId": "repository",
        "repositoryCommonDir": "/captured/repository/.git",
        "checkoutRoot": "/captured/worktrees/checkout",
        "workspaceRoot": "/captured/worktrees/checkout/subproject",
        "branch": "codexpro/fixture", "baseCommit": "a" * 40,
        "ownerId": "connector_" + "b" * 32, "state": "ready",
        "createdAt": "2026-10-31T12:00:00.000Z",
        "updatedAt": "2026-10-31T12:00:01.000Z",
        "lastUsedAt": "2026-10-31T12:00:02.000Z",
    }
    cases = []
    def add(name, value, filename=None, raw=None):
        if raw is None:
            raw = (json.dumps(value, ensure_ascii=True, separators=(",", ":")) + "\n").encode()
        if filename is None:
            identity = value.get("workspaceId", "") if isinstance(value, dict) else ""
            if not isinstance(identity, str):
                identity = ""
            # Compute through JS for surrogate edge cases; the ordinary Python
            # encoding below is deliberately only used for normal strings.
            filename = hashlib.sha256(identity.encode()).hexdigest() + ".json"
        cases.append({"name": name, "raw_base64": base64.b64encode(raw).decode(), "filename": filename})
    add("normal-v2", base)
    for state in ["provisioning", "ready", "removing", "orphaned", "failed"]:
        value = copy.deepcopy(base); value["state"] = state; add("state-" + state, value)
    for field in list(base):
        value = copy.deepcopy(base); del value[field]; add("missing-" + field, value)
        value = copy.deepcopy(base); value[field] = None; add("null-" + field, value)
    for version in [1, 2, 0, 3, "2", True, 1.0, 2.0]:
        value = copy.deepcopy(base); value["version"] = version; add("version-" + repr(version), value)
    for project in [None, False, 9, {}, [], ""]:
        value = copy.deepcopy(base); value["version"] = 1; value["projectId"] = project
        add("v1-project-" + repr(project), value)
    value = copy.deepcopy(base); value["version"] = 1; del value["projectId"]; add("v1-no-project", value)
    for state in [["ready"], [[["ready"]]], ["ready", None], [], ["rea", "dy"], {"toString": "ready"}, 0, True, "unknown"]:
        value = copy.deepcopy(base); value["state"] = state; add("state-edge-" + repr(state), value)
    for field in ["revision", "creationKeyHash", "label", "releasedAt", "failure", "futureField"]:
        value = copy.deepcopy(base); value[field] = {"kept": [None, False, 0]}
        add("unvalidated-" + field, value)
    add("wrong-filename", base, "wrong.json")
    add("array-root", [base])
    add("null-root", None)
    add("malformed-json", None, "invalid.json", b"{invalid\n")
    add("bom-json", base, raw=b"\xef\xbb\xbf" + json.dumps(base).encode())
    value = copy.deepcopy(base); value["workspaceId"] = ""; add("empty-workspace-id", value)
    value = copy.deepcopy(base); value["createdAt"] = "not an ISO date"; add("unvalidated-timestamp", value)
    script = """const crypto=require('node:crypto');\n""" + predicate + """
const fs=require('node:fs');
const cases=JSON.parse(fs.readFileSync(0,'utf8'));
for(const item of cases){
  try {
    const value=JSON.parse(Buffer.from(item.raw_base64,'base64').toString('utf8'));
    item.valid=isLease(value)&&crypto.createHash('sha256').update(value.workspaceId).digest('hex')+'.json'===item.filename;
  } catch { item.valid=false; }
}
process.stdout.write(JSON.stringify({node:process.version,cases}));
"""
    completed = subprocess.run(["node", "-e", script], input=json.dumps(cases).encode(),
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, check=True)
    oracle = json.loads(completed.stdout)
    result = {"source_sha256": EXPECTED, "node": oracle["node"], "cases": oracle["cases"]}
    destination = args.output
    destination.parent.mkdir(exist_ok=True)
    destination.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"file": str(destination), "cases": len(cases), "node": oracle["node"],
                      "accepted": sum(case["valid"] for case in oracle["cases"])}))

if __name__ == "__main__":
    main()
