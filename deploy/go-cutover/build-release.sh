#!/usr/bin/env bash
# Build an immutable CodexPro Go release from a clean, committed checkout.
#
#   deploy/go-cutover/build-release.sh [--dry-run]
#
# Output: ~/apps/codexpro-go/releases/<shortsha>/ containing the binary, the
# native ast-grep helper copied from the TypeScript release (hash-verified, with
# its licence), go.mod/go.sum, SOURCE_REVISION, the state-migration tools
# (tools/import-jobs, tools/audit-import.test), deploy/go-cutover and its two
# Python helpers, and manifest.json with SHA-256s of every file.
# The directory is made read-only once complete. An existing release directory
# is never overwritten.
set -euo pipefail

DRY_RUN=0
[[ "${1:-}" == "--dry-run" ]] && DRY_RUN=1

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RELEASES="${CODEXPRO_GO_RELEASES:-$HOME/apps/codexpro-go/releases}"
AST_GREP_SRC="${AST_GREP_SRC:-$HOME/apps/codexpro/releases/job45-dashboard-20261030-44bb76b/node_modules/@ast-grep/cli}"
AST_GREP_SHA256="258240cfb99fce31a80157da4d3189e38e3c0ffe49580655256a7e3c69d77222"
HELPER="${CODEXPRO_HOST_RESOURCES_HELPER:-$HOME/.local/bin/ssd-dev}"

cd "$REPO"
commit="$(git rev-parse HEAD)"
short="$(git rev-parse --short=10 HEAD)"
if [[ -n "$(git status --porcelain -- go deploy/go-cutover docs/go-rewrite/archive_work.py docs/go-rewrite/legacy_sqlite_migration.py)" ]]; then
  echo "refusing: release inputs have uncommitted changes" >&2
  exit 1
fi
dest="$RELEASES/$short"
if [[ -e "$dest" ]]; then
  echo "release already exists: $dest" >&2
  exit 1
fi
if compgen -G "$HOME/Projects/plans-land-*" >/dev/null; then
  echo "refusing: a Plans landing is running (~/Projects/plans-land-*); retry later" >&2
  exit 1
fi
echo "load: $(cut -d' ' -f1-3 /proc/loadavg)"
echo "commit $commit -> $dest"
actual_ast="$(sha256sum "$AST_GREP_SRC/ast-grep" | cut -d' ' -f1)"
if [[ "$actual_ast" != "$AST_GREP_SHA256" ]]; then
  echo "ast-grep hash mismatch: $actual_ast" >&2
  exit 1
fi
if [[ $DRY_RUN == 1 ]]; then
  echo "dry run: would build ./cmd/codexpro with nice -n 10 go build -p 2 and copy ast-grep"
  exit 0
fi

tmp="$RELEASES/.building-$short-$$"
trap 'chmod -R u+w "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT
mkdir -p "$tmp"
started="$(date -u +%FT%TZ)"
mkdir -p "$tmp/tools" "$tmp/deploy" "$tmp/docs/go-rewrite"
(cd go && nice -n 10 timeout 300s go build -p 2 -o "$tmp/codexpro-go" ./cmd/codexpro)
# State migration tools used inside the cutover window (built now, not then).
(cd go && nice -n 10 timeout 300s go build -p 2 -o "$tmp/tools/import-jobs" ./cmd/import-jobs)
(cd go && nice -n 10 timeout 300s go test -p 2 -c -o "$tmp/tools/audit-import.test" ./internal/audit)
cp -R deploy/go-cutover "$tmp/deploy/"
install -m 0644 docs/go-rewrite/archive_work.py docs/go-rewrite/legacy_sqlite_migration.py "$tmp/docs/go-rewrite/"
install -m 0755 "$AST_GREP_SRC/ast-grep" "$tmp/ast-grep"
for notice in LICENSE LICENSE.md README.md; do
  [[ -f "$AST_GREP_SRC/$notice" ]] && install -m 0644 "$AST_GREP_SRC/$notice" "$tmp/ast-grep.$notice"
done
install -m 0644 go/go.mod go/go.sum "$tmp/"
echo "$commit" >"$tmp/SOURCE_REVISION"

go_version_m="$(go version -m "$tmp/codexpro-go")"
helper_real="$(readlink -f "$HELPER")"
python3 - "$tmp" "$commit" "$started" "$helper_real" "$go_version_m" <<'PY'
import hashlib, json, os, subprocess, sys
tmp, commit, started, helper, version_m = sys.argv[1:6]
def sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()
files = {}
for root, _, names in os.walk(tmp):
    for name in names:
        path = os.path.join(root, name)
        rel = os.path.relpath(path, tmp)
        if "__pycache__" in rel:
            continue
        files[rel] = {"sha256": sha(path), "bytes": os.path.getsize(path)}
files = dict(sorted(files.items()))
ast_version = subprocess.run([os.path.join(tmp, "ast-grep"), "--version"], capture_output=True, text=True).stdout.strip()
manifest = {
    "schema_version": 1,
    "source_commit": commit,
    "go_tree": subprocess.run(["git", "rev-parse", f"{commit}:go"], capture_output=True, text=True).stdout.strip(),
    "build_started_at": started,
    "build_command": "nice -n 10 timeout 300s go build -p 2 -o codexpro-go ./cmd/codexpro (in go/)",
    "go_version_m": version_m,
    "files": files,
    "ast_grep": {"version": ast_version, "copied_from_sha256": files["ast-grep"]["sha256"]},
    "host_resource_helper": {"resolved_path": helper, "sha256": sha(helper)},
}
with open(os.path.join(tmp, "manifest.json"), "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
print(json.dumps({"binary_sha256": files["codexpro-go"]["sha256"], "ast_grep": ast_version}, indent=2))
PY
chmod -R a-w "$tmp"
chmod u+w "$tmp"
mv "$tmp" "$dest"
chmod a-w "$dest"
trap - EXIT
echo "release ready: $dest"
