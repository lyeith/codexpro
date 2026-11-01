#!/usr/bin/env bash
# Snapshot the TypeScript CodexPro stores read-only and import them into a
# fresh Go state directory.
#
#   migrate-state.sh [options] --tools DIR SNAPSHOT_DIR DEST_STATE_DIR
#   migrate-state.sh [--source-home DIR] --prestage-work OUT_DIR
#
# Options:
#   --tools DIR           prebuilt importers: import-jobs (go build ./cmd/import-jobs)
#                         and audit-import.test (go test -c -o audit-import.test ./internal/audit)
#   --source-home DIR     TypeScript state home (default ~/.codexpro)
#   --catalog FILE        projects catalog to record (default ~/.config/codexpro/projects.json)
#   --original-jobs-dir DIR  directory the jobs.json records name (default SOURCE_HOME/jobs;
#                         set it when SOURCE_HOME is a copy of another home)
#   --prestaged-work DIR  work-history archive captured earlier with --prestage-work; reused
#                         when the live DB/WAL identity and SHA-256 still match, else recaptured
#   --plan                print the steps and source inventory, write nothing
#
# SNAPSHOT_DIR and DEST_STATE_DIR must not exist. Nothing is written below the
# source home or ~/.config/codexpro: files there are only read; SQLite never
# connects to a live database (DB+WAL are copied with a stable second hash pass
# and backed up from the private copy); nothing is checkpointed or locked.
# The job importer requires a never-used Go Home, so it runs first and creates
# DEST; the other stores are added beside it. The last stdout line is a JSON
# summary; the exit status is non-zero on any failure or count mismatch.
set -euo pipefail

plan=0 tools="" prestaged="" prestage_out="" orig_jobs=""
src_home=$HOME/.codexpro
src_catalog=$HOME/.config/codexpro/projects.json
while [[ $# -gt 0 ]]; do
  case "$1" in
    --plan) plan=1; shift ;;
    --tools) tools="$2"; shift 2 ;;
    --source-home) src_home="$2"; shift 2 ;;
    --catalog) src_catalog="$2"; shift 2 ;;
    --prestaged-work) prestaged="$2"; shift 2 ;;
    --original-jobs-dir) orig_jobs="$2"; shift 2 ;;
    --prestage-work) prestage_out="$2"; shift 2 ;;
    -h|--help) sed -n 2,26p "$0"; exit 0 ;;
    --*) echo "unknown option $1" >&2; exit 2 ;;
    *) break ;;
  esac
done

script_dir=$(cd "$(dirname "$0")" && pwd)
helpers=${CODEXPRO_MIGRATION_HELPERS:-$script_dir/../../docs/go-rewrite}
src_home=$(realpath "$src_home")
src_jobs=$src_home/jobs
orig_jobs=${orig_jobs:-$src_jobs}
src_audit=$src_home/audit/tool-calls.jsonl
src_usage=$src_home/audit/usage/usage.sqlite
src_inbox=$src_home/audit/inbox/inbox.sqlite
src_work=$src_home/work/work.sqlite

die() { echo "migrate-state: $*" >&2; printf '{"ok":false,"error":"%s"}\n' "${*//\"/\'}"; exit 1; }
now() { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
step() { printf '%s %s\n' "$(now)" "$*"; }
private_target() {
  local p=$1
  for root in "$src_home" "$HOME/.codexpro" "$HOME/.config/codexpro"; do
    case "$p/" in "$(realpath -m "$root")"/*) die "refusing a path below live TypeScript state/config: $p" ;; esac
  done
  [[ ! -e "$p" && ! -L "$p" ]] || die "already exists: $p"
}

if [[ -n "$prestage_out" ]]; then
  out=$(realpath -m "$prestage_out"); private_target "$out"
  mkdir -p "$(dirname "$out")"
  t0=$(date +%s.%N)
  nice -n 10 python3 "$helpers/archive_work.py" --source "$src_work" --destination "$out" --attempts 5 >"$out.capture.json"
  printf '{"ok":true,"prestaged_work":"%s","seconds":%.3f}\n' "$out" "$(echo "$(date +%s.%N) - $t0" | bc)"
  exit 0
fi

[[ $# -eq 2 && -n "$tools" ]] || { echo "usage: $0 [options] --tools DIR SNAPSHOT_DIR DEST_STATE_DIR" >&2; exit 2; }
snap=$(realpath -m "$1"); dest=$(realpath -m "$2"); tools=$(realpath "$tools")
private_target "$snap"; private_target "$dest"
for f in "$src_jobs/jobs.json" "$src_audit" "$src_audit.index.json" "$src_usage" "$src_inbox" "$src_work" "$src_catalog"; do
  [[ -f "$f" ]] || die "missing source: $f"
done
for t in import-jobs audit-import.test; do [[ -x "$tools/$t" ]] || die "missing tool: $tools/$t"; done
for h in archive_work.py legacy_sqlite_migration.py; do [[ -f "$helpers/$h" ]] || die "missing helper: $helpers/$h"; done
[[ -z "$prestaged" || -f "$prestaged/manifest.json" ]] || die "prestaged work archive has no manifest: $prestaged"

if [[ $plan -eq 1 ]]; then
  echo "plan: source $src_home ; snapshot -> $snap ; import -> $dest ; tools $tools ; helpers $helpers"
  echo "  catalog   $src_catalog (copied for evidence; Go reads the live catalog in place)"
  echo "  jobs      $src_jobs ($(ls "$src_jobs" | wc -l) entries) -> import-jobs (creates $dest)"
  echo "  audit     $src_audit ($(wc -l <"$src_audit") lines) -> $dest/audit"
  echo "  usage     $src_usage -> $dest/usage/usage.sqlite"
  echo "  inbox     $src_inbox -> $dest/inbox/inbox.sqlite"
  echo "  work      $src_work -> $dest/work-history/work.sqlite ${prestaged:+(prestaged $prestaged if unchanged)}"
  echo "  not moved: $src_home/work/checkouts, $src_home/runtime (evidence copy only)"
  printf '{"ok":true,"plan":true}\n'
  exit 0
fi

umask 077
mkdir -p "$(dirname "$snap")" "$(dirname "$dest")"
mkdir "$snap"
timings=$snap/timings.tsv
: >"$timings"
timed() { # timed NAME CMD...   records wall seconds per step
  local name=$1; shift
  local t0; t0=$(date +%s.%N)
  step "$name"
  "$@"
  printf '%s\t%s\n' "$name" "$(echo "$(date +%s.%N) - $t0" | bc)" >>"$timings"
}

# ---- snapshot (read-only on the live stores) --------------------------------
snap_catalog() {
  mkdir "$snap/catalog" "$snap/runtime"
  cp --preserve=mode,timestamps "$src_catalog" "$snap/catalog/projects.json"
  [[ "$(sha "$src_catalog")" == "$(sha "$snap/catalog/projects.json")" ]] || die "catalog changed during copy"
  cp --preserve=mode,timestamps "$src_home"/runtime/*.json "$snap/runtime/" 2>/dev/null || true
}
snap_jobs() {
  local before after
  for attempt in 1 2 3 4 5; do
    rm -rf "$snap/jobs.tmp"
    before=$(sha "$src_jobs/jobs.json")
    cp -R --preserve=mode,timestamps "$src_jobs" "$snap/jobs.tmp"
    find "$snap/jobs.tmp" -name '*.tmp-*' -delete
    after=$(sha "$src_jobs/jobs.json")
    if [[ "$before" == "$after" && "$(sha "$snap/jobs.tmp/jobs.json")" == "$before" ]]; then
      mv "$snap/jobs.tmp" "$snap/jobs"; jobs_sha=$before; return 0
    fi
    sleep 0.5
  done
  die "no stable jobs.json snapshot in 5 attempts"
}
snap_audit() {
  local b1 b2
  mkdir "$snap/audit"
  for attempt in 1 2 3 4 5; do
    b1=$(sha "$src_audit"); b2=$(sha "$src_audit.index.json")
    cp --preserve=mode,timestamps "$src_audit" "$snap/audit/tool-calls.jsonl"
    cp --preserve=mode,timestamps "$src_audit.index.json" "$snap/audit/tool-calls.jsonl.index.json"
    if [[ "$b1" == "$(sha "$src_audit")" && "$b2" == "$(sha "$src_audit.index.json")" &&
          "$b1" == "$(sha "$snap/audit/tool-calls.jsonl")" && "$b2" == "$(sha "$snap/audit/tool-calls.jsonl.index.json")" ]]; then return 0; fi
    sleep 0.5
  done
  die "no stable audit journal snapshot in 5 attempts"
}
snap_sqlite() { # kind source
  python3 "$helpers/legacy_sqlite_migration.py" --kind "$1" --source "$2" --destination "$snap/$1" --attempts 5 >"$snap/$1.capture.json"
}
work_reused=false
snap_work() {
  if [[ -n "$prestaged" ]] && python3 - "$prestaged/manifest.json" "$src_work" >"$snap/work.prestage-check.json" <<'PY'
import hashlib, json, os, sys
manifest, source = json.load(open(sys.argv[1])), sys.argv[2]
want = manifest["source_files"]
def ident(p):
    try: s = os.stat(p, follow_symlinks=False)
    except FileNotFoundError: return None
    return {"device": s.st_dev, "inode": s.st_ino, "bytes": s.st_size, "mtime_ns": s.st_mtime_ns}
def sha(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""): h.update(b)
    return h.hexdigest()
result = {"source": source, "match": True, "reasons": []}
for suffix in ("", "-wal", "-journal"):
    path = source + suffix; name = os.path.basename(path); have = ident(path); rec = want.get(name)
    if suffix == "-journal":
        if have and have["bytes"]: result["match"] = False; result["reasons"].append("rollback journal present")
        continue
    if rec is None and have is None: continue
    if rec is None or have is None or any(have[k] != rec[k] for k in have) or sha(path) != rec["sha256"]:
        result["match"] = False; result["reasons"].append(name + " differs from prestaged capture")
json.dump(result, sys.stdout); print()
sys.exit(0 if result["match"] else 1)
PY
  then
    cp -R --preserve=mode,timestamps "$prestaged" "$snap/work"; work_reused=true
  else
    [[ -z "$prestaged" ]] || step "prestaged work archive is stale; recapturing ($(cat "$snap/work.prestage-check.json" 2>/dev/null))"
    python3 "$helpers/archive_work.py" --source "$src_work" --destination "$snap/work" --attempts 5 >"$snap/work.capture.json"
  fi
}

timed snapshot_catalog snap_catalog
timed snapshot_jobs snap_jobs
timed snapshot_audit snap_audit
timed snapshot_usage snap_sqlite usage "$src_usage"
timed snapshot_inbox snap_sqlite inbox "$src_inbox"
timed snapshot_work snap_work

# ---- import into the fresh Go Home ------------------------------------------
import_jobs() {
  "$tools/import-jobs" --state "$dest" --source-jobs "$snap/jobs" --original-jobs-dir "$orig_jobs" \
    --metadata-sha256 "$jobs_sha" >"$snap/jobs.import.json"
}
import_audit() {
  SP6_AUDIT_SOURCE="$snap/audit/tool-calls.jsonl" SP6_AUDIT_INDEX="$snap/audit/tool-calls.jsonl.index.json" \
  SP6_AUDIT_DEST="$dest/audit" \
    "$tools/audit-import.test" -test.run '^TestLegacyAuditImportProbe$' -test.count=1 -test.v >"$snap/audit.import.log" 2>&1 \
    || { tail -20 "$snap/audit.import.log" >&2; die "audit import failed"; }
  grep -q '^--- PASS: TestLegacyAuditImportProbe' "$snap/audit.import.log" || die "audit import probe did not pass (skipped?)"
}
install_sqlite() { mkdir "$dest/$1"; cp "$snap/$1/$1.sqlite" "$dest/$1/$1.sqlite"; }
install_work() {
  mkdir "$dest/work-history"
  cp "$snap/work/work.sqlite" "$snap/work/manifest.json" "$snap/work/principal-map.json" "$dest/work-history/"
}
timed import_jobs import_jobs
timed import_audit import_audit
timed import_usage install_sqlite usage
timed import_inbox install_sqlite inbox
timed import_work install_work

# ---- verify and write the manifest -------------------------------------------
step "verify"
python3 - "$snap" "$dest" "$src_catalog" "$work_reused" <<'PY'
import hashlib, json, sqlite3, sys
from pathlib import Path
snap, dest, catalog = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def rows(db):
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as c:
        return {n: c.execute(f'SELECT count(*) FROM "{n}"').fetchone()[0]
                for (n,) in c.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")}
t = {k: float(v) for k, v in (l.split("\t") for l in (snap / "timings.tsv").read_text().split("\n") if l)}
out = {"ok": True, "snapshot_dir": str(snap), "destination": str(dest),
       "snapshot_seconds": round(sum(v for k, v in t.items() if k.startswith("snapshot_")), 3),
       "import_seconds": round(sum(v for k, v in t.items() if k.startswith("import_")), 3),
       "step_seconds": {k: round(v, 3) for k, v in t.items()}, "work_prestaged_reused": sys.argv[4] == "true",
       "stores": {}, "failures": []}
fail = out["failures"]
cat = json.loads((snap / "catalog/projects.json").read_text())
out["stores"]["catalog"] = {"source": catalog, "sha256": sha(snap / "catalog/projects.json"),
                            "projects": len(cat.get("projects", [])), "disposition": "read in place via CODEXPRO_PROJECTS_FILE"}
jr = json.loads((snap / "jobs.import.json").read_text())
src = json.loads((snap / "jobs/jobs.json").read_text())["jobs"]
archive_only = [{"id": j["id"], "status": j["status"], "reason": j.get("reason", "")} for j in jr["jobs"] if j["disposition"] != "imported_history"]
out["stores"]["jobs"] = {"source_records": len(src), "imported": jr["imported_records"], "archive_only": jr["archive_only_records"],
                         "expired": jr["expired_records"], "raw_bytes": jr["raw_bytes"], "rendered_bytes": jr["rendered_bytes"],
                         "metadata_sha256": jr["metadata_sha256"], "archive_only_records": archive_only}
if jr["source_records"] != len(src) or jr["imported_records"] + jr["archive_only_records"] != len(src): fail.append("jobs counts")
with sqlite3.connect(f"file:{dest}/jobs.sqlite?mode=ro", uri=True) as c:
    n = c.execute("SELECT count(*) FROM jobs").fetchone()[0]
out["stores"]["jobs"]["destination_rows"] = n
if n != jr["imported_records"]: fail.append("jobs destination rows")
proof = json.loads((dest / "audit/import-proof.json").read_text())
lines = sum(1 for l in (snap / "audit/tool-calls.jsonl").read_text().splitlines() if l.strip())
out["stores"]["audit"] = {"source_lines": lines, "source_sha256": sha(snap / "audit/tool-calls.jsonl"), "proof": proof}
for kind in ("usage", "inbox"):
    m = json.loads((snap / kind / "manifest.json").read_text())
    want = {n: v["rows"] for n, v in m["inventory"]["tables"].items()}
    got = rows(dest / kind / f"{kind}.sqlite")
    out["stores"][kind] = {"source": m["source_path"], "source_files": {k: {"bytes": v["bytes"], "sha256": v["sha256"]} for k, v in m["source_files"].items()},
                           "archive_sha256": m["normalized_archive"]["sha256"], "rows": got}
    if got != want: fail.append(kind + " rows")
    if sha(dest / kind / f"{kind}.sqlite") != m["normalized_archive"]["sha256"]: fail.append(kind + " hash")
wm = json.loads((snap / "work/manifest.json").read_text())
got = rows(dest / "work-history/work.sqlite")
out["stores"]["work_history"] = {"source_files": {k: {"bytes": v["bytes"], "sha256": v["sha256"]} for k, v in wm["source_files"].items()},
                                 "archive_sha256": sha(dest / "work-history/work.sqlite"), "rows": got,
                                 "principal_map": json.loads((dest / "work-history/principal-map.json").read_text())}
if sha(dest / "work-history/work.sqlite") != sha(snap / "work/work.sqlite"): fail.append("work hash")
out["ok"] = not fail
(snap / "migration-manifest.json").write_text(json.dumps(out, indent=2) + "\n")
summary = {k: out[k] for k in ("ok", "failures", "snapshot_seconds", "import_seconds", "step_seconds", "work_prestaged_reused", "destination")}
summary["counts"] = {"jobs_imported": out["stores"]["jobs"]["imported"], "jobs_archive_only": out["stores"]["jobs"]["archive_only"],
                     "audit_lines": lines, "usage": out["stores"]["usage"]["rows"], "inbox": out["stores"]["inbox"]["rows"],
                     "work": out["stores"]["work_history"]["rows"]}
print(json.dumps(summary, separators=(",", ":")))
sys.exit(0 if out["ok"] else 1)
PY
