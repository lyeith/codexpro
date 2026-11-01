# Shared helpers for cutover.sh / rollback.sh. Sourced, not executed.
# shellcheck shell=bash

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Approved retired descriptors (registry row 31, 31 Oct 2026 16:11 UTC).
RETIRED_TOOLS='["work_manage","work_update","codexpro","codexpro_inventory","read_handoff","wait_for_handoff","codex_context","export_pro_context","handoff_to_agent","codex_sessions","read_codex_session"]'

load_instance() {
  local file="$1"
  [[ -f "$file" ]] || { echo "instance file not found: $file" >&2; exit 2; }
  # shellcheck disable=SC1090
  source "$file"
  for v in INSTANCE UNIT BASE_URL TS_HOME GO_STATE TOKEN_FILE RELEASE DROPIN_DIR DROPIN_NAME EVIDENCE_ROOT SMOKE_PROJECT SMOKE_READ_PATH; do
    [[ -n "${!v:-}" ]] || { echo "instance file missing $v" >&2; exit 2; }
  done
  DROPIN="$DROPIN_DIR/$DROPIN_NAME"
  GATE="$DROPIN_DIR/71-codexpro-cutover-gate.conf"
  WORK_HISTORY="${WORK_HISTORY:-$GO_STATE/work-history/work.sqlite}"
  GLOBAL_SCOPE_CENSUS="${GLOBAL_SCOPE_CENSUS:-0}"
}

start_evidence() {
  local kind="$1"
  EVIDENCE="$EVIDENCE_ROOT/$INSTANCE-$kind-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$EVIDENCE"
  STEPS="$EVIDENCE/steps.jsonl"
  T_START="$(date +%s.%N)"
  echo "evidence: $EVIDENCE"
}

elapsed() { python3 -c "import sys;print(round(float(sys.argv[2])-float(sys.argv[1]),3))" "$1" "$(date +%s.%N)"; }

# record STEP STATUS MESSAGE [JSON-DETAIL]
record() {
  local detail="${4:-null}"
  python3 - "$STEPS" "$1" "$2" "$3" "$detail" "$(elapsed "$T_START")" <<'PY'
import json, sys, datetime
path, step, status, message, detail, t = sys.argv[1:7]
try:
    detail = json.loads(detail)
except ValueError:
    detail = detail
entry = {"at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds"),
         "t": float(t), "step": step, "status": status, "message": message, "detail": detail}
open(path, "a").write(json.dumps(entry) + "\n")
print(f"[{t:>8}s] {step:<26} {status:<8} {message}")
PY
}

run() {  # run CMD... unless dry run; echoes the command either way
  if [[ "${DRY_RUN:-0}" == 1 ]]; then
    echo "  (dry-run) $*"
  else
    "$@"
  fi
}

render_dropin() {
  cat <<EOF
# CodexPro Go release $(basename "$RELEASE") ($(cat "$RELEASE/SOURCE_REVISION" 2>/dev/null || echo unknown)).
# Installed by deploy/go-cutover/cutover.sh. Rollback: deploy/go-cutover/rollback.sh
# (removes this file, daemon-reload, restarts $UNIT.service on the TypeScript release).
# The socket, token file, EnvironmentFile and CODEXPRO_* keys are unchanged.
# --work off: CODEXPRO_WORK_MODE=on selects the retired orchestrator; Go keeps
# work_status as read-only history from --work-history.
[Service]
WorkingDirectory=$RELEASE
ExecStart=
ExecStart=$RELEASE/codexpro-go --state-dir $GO_STATE --token-file $TOKEN_FILE --work off --ast-grep-path $RELEASE/ast-grep --work-history $WORK_HISTORY
EOF
}

# Temporary start gate for the cutover window. Stopping the service does not
# stop the socket, and the next connection would socket-activate the service
# again at once. With the Go drop-in and this gate installed before the stop,
# such a start waits in ExecStartPre (connections stay queued in the socket)
# until the state import has written the marker. Removed once Go is running.
render_gate() {  # render_gate MARKER
  cat <<EOF
# Temporary: installed and removed by deploy/go-cutover/cutover.sh.
[Service]
ExecStartPre=/usr/bin/timeout 60 /bin/sh -c 'until [ -e "$1" ]; do sleep 0.1; done'
EOF
}

# TypeScript job census as JSON: records still "running" in the TS job store,
# terminal records whose scope/resource claim is not yet quiescent, and loaded
# codexpro-job_* scopes (all of them for production, only the ones named by this
# instance's records otherwise). total must be 0 before the service is stopped.
census() {
  local scopes
  scopes="$(systemctl --user list-units --type=scope --no-legend --plain 'codexpro-job_*' 2>/dev/null | awk '{print $1" "$3" "$4}')"
  python3 - "$TS_HOME/jobs/jobs.json" "$GLOBAL_SCOPE_CENSUS" "$scopes" <<'PY'
import json, sys
path, global_scopes, scopes = sys.argv[1], sys.argv[2] == "1", sys.argv[3]
try:
    data = json.load(open(path))
    records = data.get("jobs", data) if isinstance(data, dict) else data
    records = records if isinstance(records, list) else list(records.values())
except FileNotFoundError:
    records = []
running = [r for r in records if r.get("status") == "running"]
# A terminal job whose scope or host-resource claim TypeScript has not yet marked
# quiescent is still owned by TypeScript; Go imports it as archive-only and its
# output would not be readable by job ID after the switch.
unsettled = [r for r in records if r.get("status") != "running"
             and (r.get("scope_unit") or r.get("resource_claim")) and r.get("quiescent") is not True]
units = {}
for line in scopes.splitlines():
    parts = line.split()
    if parts:
        units[parts[0]] = " ".join(parts[1:])
named = {r.get("scope_unit") for r in running + unsettled if r.get("scope_unit")}
loaded = sorted(u for u in units if global_scopes or u in named)
brief = lambda r: {k: r.get(k) for k in ("id", "status", "command_label", "started_at", "workspace_id", "scope_unit")}
print(json.dumps({"records_running": len(running), "records_unsettled": len(unsettled), "scopes_loaded": loaded,
                  "running": [brief(r) for r in running][:20], "unsettled": [brief(r) for r in unsettled][:20],
                  "total": len(running) + len(unsettled) + len([u for u in loaded if u not in named])}))
PY
}

unit_state() {
  systemctl --user show "$UNIT.service" -p ActiveState,SubState,MainPID,ExecMainStartTimestamp,NRestarts --value | paste -sd' '
}

main_exe() {
  local pid
  pid="$(systemctl --user show "$UNIT.service" -p MainPID --value)"
  [[ "$pid" != 0 ]] && readlink -f "/proc/$pid/exe" 2>/dev/null || true
}

wait_health() {  # wait_health BUDGET_SECONDS -> prints ms to first 200, fails on timeout
  python3 - "$BASE_URL" "$TOKEN_FILE" "$1" <<'PY'
import sys, time, urllib.request, urllib.error
base, token_file, budget = sys.argv[1], sys.argv[2], float(sys.argv[3])
token = open(token_file).read().strip()
t0 = time.monotonic()
while time.monotonic() - t0 < budget:
    req = urllib.request.Request(base + "/healthz", headers={"Authorization": "Bearer " + token})
    try:
        with urllib.request.urlopen(req, timeout=1) as r:
            if r.status == 200:
                print(round((time.monotonic() - t0) * 1000, 1)); sys.exit(0)
    except Exception:
        pass
    time.sleep(0.05)
sys.exit(1)
PY
}

# ssd-dev maintenance holds the helper's cache lock while it runs (ssd-dev-gc
# took 44 s on 1 Nov 01:02); job admission then fails after its 10 s helper cap
# on either server. The switch must not overlap it.
# helper_quiet SECONDS: true when no ssd-dev maintenance unit is running and the
# gc timer is not due within SECONDS.
helper_quiet() {
  local unit state next due
  for unit in ssd-dev-gc.service ssd-dev-plans-maintenance.service; do
    state="$(systemctl --user is-active "$unit" 2>/dev/null || true)"
    [[ "$state" == active || "$state" == activating || "$state" == deactivating ]] && { echo "$unit is $state"; return 1; }
  done
  next="$(systemctl --user show ssd-dev-gc.timer -p NextElapseUSecRealtime --value 2>/dev/null || true)"
  if [[ -n "$next" && "$next" != n/a ]]; then
    due=$(( $(date -d "$next" +%s) - $(date +%s) ))
    (( due >= 0 && due < $1 )) && { echo "ssd-dev-gc.timer is due in ${due}s"; return 1; }
  fi
  return 0
}
