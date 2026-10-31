#!/usr/bin/env python3
"""Project frozen wire descriptors, changing only coordinator-approved removals."""
import hashlib
import json
from pathlib import Path

root = Path(__file__).resolve().parents[2]
capture = root / "docs/go-rewrite/capture-contracts"
destination = root / "go/internal/contracts/assets"
retired = {"work_manage", "work_update"}
source = capture / "tools-list-live.json"
tools = json.loads(source.read_text())["result"]["tools"]
assert len(tools) == 30
assert retired <= {tool["name"] for tool in tools}
destination.mkdir(parents=True, exist_ok=True)
(destination / "tools.json").write_text(json.dumps(
    [tool for tool in tools if tool["name"] not in retired],
    indent=2, ensure_ascii=False) + "\n")
init = json.loads((capture / "initialize-live.json").read_text())["result"]
(destination / "initialize.json").write_text(json.dumps(init, indent=2, ensure_ascii=False) + "\n")
receipt = {"source_commit": "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a",
           "source_sha256": hashlib.sha256(source.read_bytes()).hexdigest(),
           "approved_removed": sorted(retired), "retained_count": len(tools)-len(retired)}
(destination / "provenance.json").write_text(json.dumps(receipt, indent=2) + "\n")
print(json.dumps(receipt))
