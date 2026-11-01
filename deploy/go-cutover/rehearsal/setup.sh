#!/usr/bin/env bash
# Create the temporary rehearsal instance: codexpro-rehearsal.socket on
# 127.0.0.1:7212 and codexpro-rehearsal.service, generated from the production
# unit files with only names and instance paths substituted. Uses a fresh token,
# a copy of the production EnvironmentFile with rehearsal paths, a scratch
# fixture project, and a TypeScript home seeded from a migrate-state.sh snapshot
# (consistent private copies of production audit, usage and inbox; no work
# history and no production jobs, so every job record names the rehearsal home).
#
#   rehearsal/setup.sh MIGRATION_SNAPSHOT_DIR
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
R="${REHEARSAL_ROOT:-$HOME/Projects/codexpro-cutover-evidence/rehearsal}"
SNAP="${1:?usage: setup.sh MIGRATION_SNAPSHOT_DIR}"
UNITS="$HOME/.config/systemd/user"
PORT=7212

[[ -e "$UNITS/codexpro-rehearsal.service" ]] && { echo "rehearsal units already exist; run teardown.sh" >&2; exit 1; }
[[ -e "$R/ts-home" || -e "$R/go-state" ]] && { echo "rehearsal state exists under $R; run teardown.sh" >&2; exit 1; }
if (exec 3<>/dev/tcp/127.0.0.1/$PORT) 2>/dev/null; then echo "port $PORT in use" >&2; exit 1; fi

mkdir -p "$R/config" "$R/ts-home" "$R/fixture"
chmod 700 "$R" "$R/config" "$R/ts-home"
# Fresh rehearsal bearer token (never the production one).
umask 077
python3 -c 'import secrets;print(secrets.token_urlsafe(48))' >"$R/config/http-token"
umask 022

# Scratch fixture project: a small Git repository for write/edit/batch/jobs.
git -C "$R/fixture" init -q
printf '# Rehearsal fixture\n\nCodexPro cutover rehearsal scratch project.\n' >"$R/fixture/README.md"
git -C "$R/fixture" add README.md
git -C "$R/fixture" -c user.name=rehearsal -c user.email=rehearsal@localhost commit -q -m "fixture"
python3 - "$R" <<'PY'
import json, os, sys
r = sys.argv[1]
json.dump({"version": 1, "defaultProject": "scratch", "projects": [
    {"id": "scratch", "label": "Rehearsal scratch", "root": os.path.join(r, "fixture")},
    {"id": "codexpro", "label": "CodexPro (read-only use)", "root": os.path.expanduser("~/Projects/codexpro")}]},
    open(os.path.join(r, "config", "projects.json"), "w"), indent=2)
PY
chmod 600 "$R/config/projects.json"

# EnvironmentFile: production keys and values, with instance paths replaced.
python3 - "$HOME/.config/codexpro/env" "$R" "$PORT" <<'PY'
import sys, os
src, r, port = sys.argv[1], sys.argv[2], sys.argv[3]
override = {"CODEXPRO_PROJECTS_FILE": os.path.join(r, "config", "projects.json"),
            "CODEXPRO_HOST": "127.0.0.1", "CODEXPRO_PORT": port,
            "CODEXPRO_HOME": os.path.join(r, "ts-home")}
out, seen = [], set()
for line in open(src).read().splitlines():
    key = line.split("=", 1)[0].strip() if "=" in line and not line.lstrip().startswith("#") else None
    if key in override:
        line = f"{key}={override[key]}"; seen.add(key)
    out.append(line)
out += [f"{k}={v}" for k, v in override.items() if k not in seen]
open(os.path.join(r, "config", "env"), "w").write("\n".join(out) + "\n")
os.chmod(os.path.join(r, "config", "env"), 0o600)
PY

# TypeScript home from the snapshot (never the live ~/.codexpro).
mkdir -p "$R/ts-home/audit/usage" "$R/ts-home/audit/inbox" "$R/ts-home/work" "$R/ts-home/jobs"
cp "$SNAP/audit/tool-calls.jsonl" "$SNAP/audit/tool-calls.jsonl.index.json" "$R/ts-home/audit/"
cp "$SNAP/usage/usage.sqlite" "$R/ts-home/audit/usage/"
cp "$SNAP/inbox/inbox.sqlite" "$R/ts-home/audit/inbox/"
# No work history: TypeScript work-mode recovery could act on the archived runs,
# whose checkouts live under the production home. The production archive's
# read-back through Go is covered by the migration dry run.
chmod -R u+w,go-rwx "$R/ts-home"

# Units: production files with names and instance paths substituted.
sed -e "s|ListenStream=0.0.0.0:7200|ListenStream=127.0.0.1:$PORT|" \
    -e "s|(:7200)|(rehearsal :$PORT)|" "$UNITS/codexpro.socket" >"$UNITS/codexpro-rehearsal.socket"
sed -e "s|codexpro.socket|codexpro-rehearsal.socket|g" \
    -e "s|%h/.config/codexpro/env|$R/config/env|g" \
    -e "s|%h/.config/codexpro/http-token|$R/config/http-token|g" \
    -e "s|SyslogIdentifier=codexpro|SyslogIdentifier=codexpro-rehearsal|" \
    -e "s|^Description=.*|Description=CodexPro rehearsal (temporary)|" \
    "$UNITS/codexpro.service" >"$UNITS/codexpro-rehearsal.service"
mkdir -p "$UNITS/codexpro-rehearsal.service.d"
cp "$UNITS/codexpro.service.d/ssd-dev-host-resources.conf" "$UNITS/codexpro.service.d/ssd-dev-path.conf" "$UNITS/codexpro-rehearsal.service.d/"
systemctl --user daemon-reload
systemctl --user start codexpro-rehearsal.socket
ls -la --time-style=full-iso "$HOME/.codexpro" "$HOME/.codexpro/runtime" >"$R/live-ts-home-listing-before.txt"
echo "rehearsal ready: socket 127.0.0.1:$PORT, root $R"
