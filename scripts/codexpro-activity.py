#!/usr/bin/env python3
"""Read bounded CodexPro activity using a private curl config (no token in argv)."""
import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import quote, urlsplit


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("project", nargs="?", help="Project id; omit for all-project summaries")
    parser.add_argument("--limit", type=int, choices=range(1, 11), default=8)
    parser.add_argument("--output-bytes", type=int, default=1024, help="Combined stdout/stderr bytes per command (0–4096)")
    parser.add_argument("--quiet-after-ms", type=int, default=300000)
    args = parser.parse_args()
    if not 0 <= args.output_bytes <= 4096 or not 30000 <= args.quiet_after_ms <= 86400000:
        parser.error("output-bytes must be 0–4096; quiet-after-ms must be 30000–86400000")
    settings_path = Path(os.environ.get("CODEXPRO_ACTIVITY_CONFIG", str(Path.home() / ".config/codexpro/activity-client.json")))
    try:
        settings = json.loads(settings_path.read_text())
        base = settings["base_url"].rstrip("/")
        config = Path(settings["curl_config"]).expanduser()
        url = urlsplit(base)
        if url.scheme not in ("http", "https") or not url.netloc or url.username or url.password or url.query or url.fragment:
            raise ValueError("base_url must be an HTTP(S) origin without credentials or query")
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        parser.error(f"Invalid activity client settings at {settings_path}: {error}")
    if not config.is_file():
        parser.error(f"Missing private authentication config: {config}")
    if config.stat().st_mode & 0o077:
        parser.error(f"Authentication config must be private: chmod 600 {config}")
    route = f"/activity/projects/{quote(args.project, safe='')}.json" if args.project else "/activity.json"
    command = ["curl", "--disable", "--config", str(config), "--silent", "--show-error", "--fail-with-body",
               "--connect-timeout", "8", "--max-time", "30", "--get",
               "--data-urlencode", f"limit={args.limit}", "--data-urlencode", f"output_bytes={args.output_bytes}",
               "--data-urlencode", f"quiet_after_ms={args.quiet_after_ms}", base + route]
    os.execvp(command[0], command)


if __name__ == "__main__":
    main()
