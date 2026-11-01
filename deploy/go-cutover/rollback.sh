#!/usr/bin/env bash
# Return codexpro.service to the TypeScript release.
#
#   deploy/go-cutover/rollback.sh [--instance FILE] [--dry-run] [--reason TEXT]
#
# 1 record   Go job census (records still running in Go's jobs.sqlite and their
#            scopes); they are left running and listed for reconciliation
# 2 switch   move the Go drop-in into the evidence directory, daemon-reload,
#            restart the service (TypeScript; socket keeps listening)
# 3 smoke    TypeScript read-only smoke through the socket with the same token
# 4 evidence copy of the Go state (now stopped) for reconciliation of writes
#            made during the Go window. TypeScript state was never written by Go.
# Idempotent: with no drop-in installed it only verifies and smokes TypeScript.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

INSTANCE_FILE="$HERE/production.env"
DRY_RUN=0
REASON="operator request"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --instance) INSTANCE_FILE="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --reason) REASON="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
load_instance "$INSTANCE_FILE"
start_evidence rollback
record reason info "$REASON"
[[ $DRY_RUN == 1 ]] && record mode info "DRY RUN: no state-changing command is executed"

go_census() {
  python3 - "$GO_STATE/jobs.sqlite" <<'PY'
import json, sqlite3, sys, os
path = sys.argv[1]
if not os.path.exists(path):
    print(json.dumps({"running": [], "note": "no Go jobs store"})); sys.exit()
db = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=5)
try:
    rows = db.execute("SELECT id, status, payload FROM jobs").fetchall()
except sqlite3.Error as e:
    print(json.dumps({"running": [], "error": str(e)})); sys.exit()
running = []
for job_id, status, payload in rows:
    try:
        r = json.loads(payload)
    except (TypeError, ValueError):
        r = {}
    if status == "running" or (r.get("scope_unit") and r.get("quiescent") is False):
        running.append({"id": job_id, "status": status, "scope": r.get("scope_unit")})
print(json.dumps({"running": running}))
PY
}

if [[ -f "$GATE" ]]; then
  run mv "$GATE" "$EVIDENCE/removed-gate.conf"
  record gate ok "removed cutover start gate"
fi
if [[ -f "$DROPIN" ]]; then
  c="$(go_census 2>&1 || echo '{"error":"census failed"}')"
  record census info "Go jobs not settled (left running, reconcile later)" "$c"
  t="$(date +%s.%N)"
  run mv "$DROPIN" "$EVIDENCE/removed-dropin.conf"
  run systemctl --user daemon-reload
  run systemctl --user restart "$UNIT.service"
  record switch ok "drop-in removed, daemon-reload, restart in $(elapsed "$t")s; $(unit_state)"
else
  record switch skip "no Go drop-in installed; verifying the TypeScript service only"
  if [[ "$(systemctl --user is-active "$UNIT.service" || true)" != active ]]; then
    run systemctl --user start "$UNIT.service"
  fi
fi

if [[ $DRY_RUN == 0 ]]; then
  ms="$(wait_health 60)" || { record health FAIL "TypeScript health did not answer within 60 s"; exit 1; }
  record health ok "TypeScript health in ${ms} ms; exe $(main_exe)"
  if python3 "$HERE/smoke.py" --base-url "$BASE_URL" --token-file "$TOKEN_FILE" --mode readonly --ready-budget 60 \
       --project "$SMOKE_PROJECT" --read-path "$SMOKE_READ_PATH" --output "$EVIDENCE/smoke-typescript.json" >/dev/null; then
    record smoke ok "TypeScript smoke passed"
  else
    record smoke FAIL "TypeScript smoke failed: $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["failed"])' "$EVIDENCE/smoke-typescript.json" 2>/dev/null || echo unknown)"
  fi
  if [[ -d "$GO_STATE" ]]; then
    tar -C "$(dirname "$GO_STATE")" -cf "$EVIDENCE/go-state-at-rollback.tar" "$(basename "$GO_STATE")"
    record evidence ok "Go state copied to $EVIDENCE/go-state-at-rollback.tar ($(du -h "$EVIDENCE/go-state-at-rollback.tar" | cut -f1)); Go state left in place, read-only to TypeScript"
  fi
fi
record result DONE "$UNIT.service runs $(main_exe). Reconcile writes made during the Go window before resuming writers."
