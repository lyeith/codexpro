#!/usr/bin/env bash
# Jobs-only successor for the preserved gate02 race-fixture failures.
set -euo pipefail
umask 077
[[ $# == 2 ]] || exit 2
evidence=$1
expected_head=$2
[[ $(hostname -s) == plans-worker && -d "$evidence" ]] || exit 125
export GIT_OPTIONAL_LOCKS=0 GOWORK=off GOMAXPROCS=2
[[ $(git rev-parse HEAD) == "$expected_head" ]] || exit 125
[[ -z $(git status --porcelain=v1 --untracked-files=all) ]] || exit 125
git rev-parse HEAD 'HEAD^{tree}' > "$evidence/source-before.txt"
git ls-files -z | LC_ALL=C sort -z | xargs -0 sha256sum > "$evidence/source-files.sha256"
overall=0
run_check() {
    local label=$1
    shift
    printf '%q ' "$@" > "$evidence/$label.command"
    printf '\n' >> "$evidence/$label.command"
    date -u +'%FT%T.%NZ' > "$evidence/$label.started"
    "$@" > "$evidence/$label.stdout" 2> "$evidence/$label.stderr"
    local rc=$?
    printf '%s\n' "$rc" > "$evidence/$label.exit"
    date -u +'%FT%T.%NZ' > "$evidence/$label.finished"
    printf '%s exit %s\n' "$label" "$rc"
    return "$rc"
}
if run_check normal-focused go -C go test -json -mod=readonly -p 2 -count=1 -timeout=120s -run '^(TestBashWireOutcomeAndReadAcknowledgement|TestInterruptWaitsReturnsPendingJobWaitsAsRestarting|TestLauncherFinalizationWaitsForDiagnosticWriterBeforePrune)$' ./internal/jobs; then
    if run_check race-jobs go -C go test -json -race -mod=readonly -p 2 -count=1 -timeout=300s ./internal/jobs; then
        if ! run_check build go -C go build -mod=readonly -p 2 -o "$evidence/codexpro" ./cmd/codexpro; then overall=1; fi
    else
        overall=1
    fi
else
    overall=1
fi
if [[ -f $evidence/codexpro ]]; then
    sha256sum "$evidence/codexpro" > "$evidence/codexpro.sha256"
    go -C go version -m "$evidence/codexpro" > "$evidence/codexpro-build-info.txt"
fi
git rev-parse HEAD 'HEAD^{tree}' > "$evidence/source-after.txt"
if ! cmp "$evidence/source-before.txt" "$evidence/source-after.txt" ||
   ! sha256sum -c --quiet "$evidence/source-files.sha256" ||
   [[ -n $(git status --porcelain=v1 --untracked-files=all) ]]; then
    overall=125
fi
printf '%s\n' "$overall" > "$evidence/checks.exit"
exit "$overall"
