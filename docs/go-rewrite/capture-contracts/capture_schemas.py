#!/usr/bin/env python3
"""Capture the actual MCP wire schema without changing projects or runtime config.

The token is read into memory and never printed, persisted, or placed in argv.
Writes only the selected output directory. Tool traffic is initialize,
notifications/initialized and tools/list.
"""
import argparse
import datetime
import hashlib
import json
from pathlib import Path
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--label", choices=("live", "private"), required=True)
    args = parser.parse_args()
    credential = Path(args.token_file).read_text().strip()
    session = None

    def rpc(body):
        headers = {
            "Authorization": "Bearer " + credential,
            "Accept": "application/json, text/event-stream",
            "Content-Type": "application/json",
        }
        if session:
            headers["Mcp-Session-Id"] = session
        request = urllib.request.Request(
            args.endpoint, data=json.dumps(body).encode(), headers=headers,
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read().decode()
            session_header = response.headers.get("Mcp-Session-Id")
        if not raw.strip():
            return None, session_header
        if raw.lstrip().startswith("{"):
            return json.loads(raw), session_header
        messages = [
            json.loads(line[5:].strip()) for line in raw.splitlines()
            if line.startswith("data:")
        ]
        if not messages:
            raise RuntimeError("No MCP message in response")
        return messages[-1], session_header

    initialized, session = rpc({
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {
            "protocolVersion": "2024-11-05", "capabilities": {},
            "clientInfo": {"name": "sp6-contract-capture", "version": "1"},
        },
    })
    if not session or "error" in initialized:
        raise RuntimeError("MCP initialization failed")
    rpc({"jsonrpc": "2.0", "method": "notifications/initialized"})
    result, _ = rpc({
        "jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {},
    })
    if "error" in result or not result.get("result", {}).get("tools"):
        raise RuntimeError("Tool schema capture failed")
    output = Path(args.output_dir)
    output.mkdir(parents=True, exist_ok=True)

    def save(name, value):
        rendered = (json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode()
        if credential.encode() in rendered or session.encode() in rendered:
            raise RuntimeError("Credential/session appeared in captured body")
        (output / name).write_bytes(rendered)
        return {"sha256": hashlib.sha256(rendered).hexdigest(), "bytes": len(rendered)}

    schema = save("tools-list-" + args.label + ".json", result)
    init = save("initialize-" + args.label + ".json", initialized)
    metadata = {
        "observed_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source_commit": "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a",
        "endpoint": args.endpoint,
        "protocol_version": initialized["result"]["protocolVersion"],
        "server_info": initialized["result"]["serverInfo"],
        "tool_count": len(result["result"]["tools"]),
        "schemas": schema, "initialize": init,
        "credential_recorded": False, "session_recorded": False,
    }
    save("tools-list-" + args.label + ".metadata.json", metadata)
    print(json.dumps(metadata))
    print(json.dumps([tool["name"] for tool in result["result"]["tools"]]))


if __name__ == "__main__":
    main()
