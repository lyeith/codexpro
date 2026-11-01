#!/usr/bin/env bash
# Switch codexpro.service from the TypeScript release to the Go release.
#
#   deploy/go-cutover/cutover.sh [--instance FILE] [--dry-run] [--drain-timeout SECONDS]
#                                [--skip-load-qualification] [--no-auto-rollback]
#
# The socket unit keeps listening throughout; only the service process changes.
# Connections that arrive while the service is stopped wait in the socket's
# backlog and are answered by the Go process.
#
# Steps (each recorded in <evidence>/steps.jsonl):
#   1 preflight    release hashes, unit/socket state, Go state dir absent, no landing,
#                  no ssd-dev maintenance running or due within 10 minutes,
#                  TypeScript tool inventory (expected Go inventory = it minus retired)
#   2 drain        wait until no TypeScript job is running (records and scopes);
#                  REFUSE (exit 3, nothing changed) if any is still running at the
#                  timeout. Jobs are never stopped by this script.
#   3 stop         stop the service (socket stays); re-check the census; if a job
#                  slipped in, restart TypeScript and abort (exit 4)
#   4 migrate      final snapshot of the TypeScript stores and import into Go state
#   5 drop-in      install the Go drop-in, daemon-reload, verify ExecStart
#   6 start        loaded-startup qualification (40 nice-19 workers in an owned
#                  scope; health and first read <= 3 s) while starting the service
#   7 smoke        smoke suite through the socket with the production token,
#                  inventory equal to expected, process identity checks
# Any failure after step 3 runs rollback.sh unless --no-auto-rollback is given.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

INSTANCE_FILE="$HERE/production.env"
DRY_RUN=0
DRAIN_TIMEOUT=900
LOAD_QUAL=1
AUTO_ROLLBACK=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --instance) INSTANCE_FILE="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --drain-timeout) DRAIN_TIMEOUT="$2"; shift 2 ;;
    --skip-load-qualification) LOAD_QUAL=0; shift ;;
    --no-auto-rollback) AUTO_ROLLBACK=0; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
load_instance "$INSTANCE_FILE"
start_evidence cutover
[[ $DRY_RUN == 1 ]] && record mode info "DRY RUN: no state-changing command is executed"

fail_after_stop() {
  local step="$1" msg="$2"
  record "$step" FAIL "$msg"
  if [[ $DRY_RUN == 0 && $AUTO_ROLLBACK == 1 ]]; then
    record rollback start "running rollback.sh (trigger: $step)"
    "$HERE/rollback.sh" --instance "$INSTANCE_FILE" --reason "cutover $step: $msg" || true
  fi
  record result FAIL "cutover failed at $step after the service was stopped; see rollback evidence"
  exit 5
}

# ---- 1 preflight -----------------------------------------------------------
expected_drop="$(render_dropin)"
if [[ -f "$DROPIN" ]]; then
  if [[ "$(cat "$DROPIN")" == "$expected_drop" && "$(main_exe)" == "$RELEASE/codexpro-go" ]]; then
    record preflight DONE "already cut over: drop-in installed and $UNIT.service runs $(main_exe)"
    exit 0
  fi
  record preflight REFUSE "a different drop-in exists at $DROPIN; inspect it (or run rollback.sh) first"
  exit 2
fi
python3 - "$RELEASE" <<'PY' || { record preflight REFUSE "release manifest hashes do not match"; exit 2; }
import hashlib, json, os, sys
rel = sys.argv[1]
m = json.load(open(os.path.join(rel, "manifest.json")))
for name, meta in m["files"].items():
    h = hashlib.sha256(open(os.path.join(rel, name), "rb").read()).hexdigest()
    if h != meta["sha256"]:
        sys.exit(f"{name}: {h} != {meta['sha256']}")
PY
record preflight ok "release $RELEASE matches its manifest ($(cut -c1-12 <<<"$(sha256sum "$RELEASE/codexpro-go")"))"
[[ "$(stat -c %a "$TOKEN_FILE")" == 600 ]] || { record preflight REFUSE "token file must be mode 0600"; exit 2; }
if [[ -e "$GO_STATE" || -L "$GO_STATE" ]]; then
  record preflight REFUSE "Go state $GO_STATE already exists (the importer needs a never-used directory); move it aside after inspection"
  exit 2
fi
if compgen -G "$HOME/Projects/plans-land-*" >/dev/null; then
  record preflight REFUSE "a Plans landing is running; wait for it to finish"
  exit 2
fi
if ! why="$(helper_quiet 600)"; then
  record preflight REFUSE "SSD helper maintenance would overlap the switch ($why); retry after it"
  exit 2
fi
socket_state="$(systemctl --user is-active "$UNIT.socket" || true)"
[[ "$socket_state" == active ]] || { record preflight REFUSE "$UNIT.socket is $socket_state"; exit 2; }
record preflight ok "service: $(unit_state); exe $(main_exe); load $(cut -d' ' -f1-3 /proc/loadavg)" \
  "$(python3 -c 'import json,sys;print(json.dumps({"ts_release":sys.argv[1]}))' "$(readlink -f "$HOME/apps/codexpro/current" 2>/dev/null || true)")"
systemctl --user cat "$UNIT.service" >"$EVIDENCE/unit-before.txt" 2>&1 || true
if python3 "$HERE/smoke.py" --base-url "$BASE_URL" --token-file "$TOKEN_FILE" --mode inventory \
     --project "$SMOKE_PROJECT" --save-tools "$EVIDENCE/tools-typescript.json" --output "$EVIDENCE/inventory-typescript.json" >/dev/null; then
  python3 - "$EVIDENCE/tools-typescript.json" "$RETIRED_TOOLS" "$EVIDENCE/tools-expected-go.json" <<'PY'
import json, sys
ts = json.load(open(sys.argv[1])); retired = set(json.loads(sys.argv[2]))
json.dump([n for n in ts if n not in retired], open(sys.argv[3], "w"), indent=1)
PY
  record preflight ok "TypeScript inventory $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))))' "$EVIDENCE/tools-typescript.json") tools; expected Go $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))))' "$EVIDENCE/tools-expected-go.json")"
else
  record preflight REFUSE "could not read the TypeScript tool inventory through $BASE_URL"
  exit 2
fi

# Capture the large work-history archive before the window; inside it the
# migration reuses it if the live database is unchanged (else recaptures).
if [[ -z "${PRESTAGED_WORK:-}" && $DRY_RUN == 0 ]]; then
  PRESTAGED_WORK="$EVIDENCE/work-prestaged"
  if nice -n 10 "$HERE/migrate-state.sh" --source-home "$TS_HOME" --prestage-work "$PRESTAGED_WORK" >"$EVIDENCE/prestage.log" 2>&1; then
    record preflight ok "work history prestaged: $(tail -n1 "$EVIDENCE/prestage.log")"
  else
    record preflight note "work prestage failed (the window will capture it): $(tail -n1 "$EVIDENCE/prestage.log")"
    PRESTAGED_WORK=""
  fi
fi

# ---- 2 drain ---------------------------------------------------------------
deadline=$(( $(date +%s) + DRAIN_TIMEOUT ))
while :; do
  c="$(census)"
  total="$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["total"])' "$c")"
  if [[ "$total" == 0 ]]; then
    record drain ok "no running TypeScript jobs" "$c"
    break
  fi
  if (( $(date +%s) >= deadline )); then
    record drain REFUSE "$total TypeScript job(s) still running after ${DRAIN_TIMEOUT}s; nothing was changed. Let them finish or have their owner stop them, then rerun" "$c"
    exit 3
  fi
  record drain wait "$total running; waiting (deadline in $(( deadline - $(date +%s) ))s)" "$c"
  [[ $DRY_RUN == 1 ]] && { record drain REFUSE "dry run stops at a non-empty census"; exit 3; }
  sleep 10
done

if ! why="$(helper_quiet 240)"; then
  record drain REFUSE "SSD helper maintenance would overlap the switch ($why); nothing was changed, rerun after it"
  exit 3
fi

# ---- 3 stop ----------------------------------------------------------------
T_DOWN="$(date +%s.%N)"
record stop start "stopping $UNIT.service (socket keeps listening; TypeScript drains in-flight calls)"
run systemctl --user stop "$UNIT.service"
record stop ok "service: $(unit_state); socket $(systemctl --user is-active "$UNIT.socket" || true)"
c="$(census)"
if [[ "$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["total"])' "$c")" != 0 ]]; then
  record stop ABORT "a TypeScript job started during the stop; restarting TypeScript unchanged" "$c"
  run systemctl --user start "$UNIT.service"
  record result ABORTED "TypeScript restored: $(unit_state)"
  exit 4
fi

# ---- 4 migrate -------------------------------------------------------------
t="$(date +%s.%N)"
if ! run nice -n 10 "$HERE/migrate-state.sh" --source-home "$TS_HOME" ${CATALOG:+--catalog "$CATALOG"} ${ORIGINAL_JOBS_DIR:+--original-jobs-dir "$ORIGINAL_JOBS_DIR"} \
      ${PRESTAGED_WORK:+--prestaged-work "$PRESTAGED_WORK"} --tools "$RELEASE/tools" \
      "$EVIDENCE/snapshot" "$GO_STATE" >"$EVIDENCE/migrate.log" 2>&1; then
  fail_after_stop migrate "migrate-state.sh failed; see $EVIDENCE/migrate.log"
fi
summary="$(tail -n1 "$EVIDENCE/migrate.log" 2>/dev/null)"
if [[ $DRY_RUN == 0 ]] && ! python3 -c 'import json,sys;sys.exit(0 if json.loads(sys.argv[1]).get("ok") is True else 1)' "$summary" 2>/dev/null; then
  fail_after_stop migrate "migration summary is not ok: $summary"
fi
record migrate ok "final snapshot and import in $(elapsed "$t")s" "${summary:-null}"

# ---- 5 drop-in ---------------------------------------------------------------
if [[ $DRY_RUN == 1 ]]; then
  echo "$expected_drop" >"$EVIDENCE/dropin.conf"
  record dropin dry-run "would install $DROPIN (content in evidence)"
else
  mkdir -p "$DROPIN_DIR"
  printf '%s\n' "$expected_drop" >"$DROPIN.tmp"
  chmod 0600 "$DROPIN.tmp"
  mv "$DROPIN.tmp" "$DROPIN"
  cp "$DROPIN" "$EVIDENCE/dropin.conf"
  systemctl --user daemon-reload
  exec_start="$(systemctl --user show "$UNIT.service" -p ExecStart --value)"
  [[ "$exec_start" == *"$RELEASE/codexpro-go"* ]] || fail_after_stop dropin "ExecStart does not name the Go release after daemon-reload"
  record dropin ok "installed $DROPIN; daemon-reload done"
fi

# ---- 6 start (+ loaded-startup qualification) --------------------------------
if [[ $DRY_RUN == 1 ]]; then
  record start dry-run "would start $UNIT.service under the loaded-startup qualification"
elif [[ $LOAD_QUAL == 1 ]] && ! compgen -G "$HOME/Projects/plans-land-*" >/dev/null; then
  if python3 "$HERE/qualify.py" --base-url "$BASE_URL" --token-file "$TOKEN_FILE" --project "$SMOKE_PROJECT" \
       --read-path "$SMOKE_READ_PATH" --start-cmd "systemctl --user start $UNIT.service" \
       --output "$EVIDENCE/loaded-startup.json" >/dev/null; then
    record start ok "loaded-startup qualification passed" "$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print(json.dumps({k:d.get(k) for k in ("health_ms","first_read_ms","further","load_at_start","load_during","guard")}))' "$EVIDENCE/loaded-startup.json")"
  else
    fail_after_stop start "loaded-startup qualification failed; see $EVIDENCE/loaded-startup.json"
  fi
else
  [[ $LOAD_QUAL == 1 ]] && record start note "landing running: loaded qualification skipped (fail-closed admission); plain start"
  systemctl --user start "$UNIT.service"
  ms="$(wait_health 30)" || fail_after_stop start "Go health did not answer within 30 s"
  record start ok "Go health in ${ms} ms after start"
fi
record downtime info "service process was down for $(elapsed "$T_DOWN")s (socket listening throughout)"

# ---- 7 smoke ---------------------------------------------------------------
if [[ $DRY_RUN == 0 ]]; then
  exe="$(main_exe)"
  [[ "$exe" == "$RELEASE/codexpro-go" ]] || fail_after_stop smoke "MainPID runs $exe, not the Go release"
  pid="$(systemctl --user show "$UNIT.service" -p MainPID --value)"
  record smoke ok "MainPID $pid exe $exe nice $(ps -o ni= -p "$pid" | tr -d ' ') cgroup $(cut -d: -f3 "/proc/$pid/cgroup")"
  if python3 "$HERE/smoke.py" --base-url "$BASE_URL" --token-file "$TOKEN_FILE" --mode readonly \
       --project "$SMOKE_PROJECT" --read-path "$SMOKE_READ_PATH" --expect-tools "$EVIDENCE/tools-expected-go.json" \
       --save-tools "$EVIDENCE/tools-go.json" --output "$EVIDENCE/smoke-go.json" >/dev/null; then
    record smoke ok "smoke suite passed through $BASE_URL" "$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print(json.dumps({s["step"]:s["ms"] for s in d["steps"]}))' "$EVIDENCE/smoke-go.json")"
  else
    fail_after_stop smoke "smoke failed: $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["failed"])' "$EVIDENCE/smoke-go.json" 2>/dev/null || echo unknown)"
  fi
fi
systemctl --user cat "$UNIT.service" >"$EVIDENCE/unit-after.txt" 2>&1 || true
record result DONE "$UNIT.service now runs the Go release; rollback: $HERE/rollback.sh --instance $INSTANCE_FILE"
