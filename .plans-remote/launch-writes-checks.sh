#!/usr/bin/env bash
# Run only on plans-worker through the existing finite FP5 external owner.
set -euo pipefail
umask 077
[[ $# == 2 ]] || exit 2
inputs=$1
evidence=$2
[[ $(hostname -s) == plans-worker && -d "$inputs/fleet" && -d "$evidence" ]] || exit 125
export GIT_OPTIONAL_LOCKS=0 GOWORK=off GOMAXPROCS=2
overall=0
run_check() {
    local label=$1
    shift
    printf '%q ' "$@" > "$evidence/$label.command"
    printf '\n' >> "$evidence/$label.command"
    date -u +'%FT%T.%NZ' > "$evidence/$label.started"
    set +e
    "$@" > "$evidence/$label.stdout" 2> "$evidence/$label.stderr"
    local rc=$?
    set -e
    printf '%s\n' "$rc" > "$evidence/$label.exit"
    date -u +'%FT%T.%NZ' > "$evidence/$label.finished"
    printf '%s exit %s\n' "$label" "$rc"
    if (( rc != 0 )); then
        overall=1
    fi
    return "$rc"
}
git rev-parse HEAD > "$evidence/base-commit.txt"
git diff --binary HEAD > "$evidence/source.patch"
git ls-files --others --exclude-standard > "$evidence/untracked-source.txt"
cp -p .plans-remote/launch-admission-proof.py "$evidence/launch-admission-proof.py"
sha256sum go/go.mod "$inputs/scope.sh" "$inputs/runtime.json" "$evidence/launch-admission-proof.py" > "$evidence/input-sha256.txt"
run_check go-version go -C go version
normal_ok=0
if run_check normal go -C go test -json -mod=readonly -p 2 -count=1 -timeout=300s ./internal/process ./internal/jobs ./internal/engine; then
    normal_ok=1
fi
if run_check build go -C go build -mod=readonly -p 2 -o "$evidence/codexpro" ./cmd/codexpro; then
    run_check admission uv run --no-project --offline --python /usr/bin/python3.14 python -I -B "$evidence/launch-admission-proof.py" --fleet-root "$inputs/fleet" --policy-source "$inputs/runtime.json" --evidence-dir "$evidence/fleet-proof" --launcher-executable "$evidence/codexpro" || true
fi
if (( normal_ok )); then
    run_check race go -C go test -json -race -mod=readonly -p 2 -count=1 -timeout=300s ./internal/process ./internal/jobs ./internal/engine || true
    run_check vet go -C go vet -mod=readonly -p 2 ./internal/process ./internal/jobs ./internal/engine || true
fi
printf '%s\n' "$overall" > "$evidence/checks.exit"
exit "$overall"
