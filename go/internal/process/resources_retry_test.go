package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// fakeHelper fails its first `failures` begin calls, as ssd-dev does while gc
// holds its cache lock, then allocates. It echoes the requested identifiers.
func fakeHelper(t *testing.T, failures int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	script := `#!/bin/sh
n=$(cat "` + counter + `" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "` + counter + `"
if [ $n -le ` + strconv.Itoa(failures) + ` ]; then echo "ssd-dev: busy" >&2; exit 125; fi
run=""; reg=""; while [ $# -gt 0 ]; do case "$1" in --run-id) run=$2; shift;; --registration-id) reg=$2; shift;; esac; shift; done
printf '{"run_id":"%s","registration_id":"%s","scratch":"/tmp/x","environment":{},"owner":{}}' "$run" "$reg"
`
	helper := filepath.Join(dir, "ssd-dev")
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return helper, counter
}

func calls(t *testing.T, counter string) int {
	b, _ := os.ReadFile(counter)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func testClaim(helper string) ResourceClaim {
	return ResourceClaim{Version: 1, Helper: helper, RunID: strings.Repeat("a", 32), RegistrationID: strings.Repeat("b", 32)}
}

func TestBeginResourcesRetriesWhileHelperIsBusy(t *testing.T) {
	helper, counter := fakeHelper(t, 2)
	started := time.Now()
	a, err := BeginResourcesWithRetry(context.Background(), testClaim(helper), t.TempDir(), "codexpro-job_x.scope", 20*time.Second)
	if err != nil {
		t.Fatalf("allocation was not retried: %v", err)
	}
	if a.RunID != strings.Repeat("a", 32) || calls(t, counter) != 3 {
		t.Fatalf("unexpected allocation %+v after %d calls", a, calls(t, counter))
	}
	if elapsed := time.Since(started); elapsed < 2*time.Second || elapsed > 10*time.Second {
		t.Fatalf("backoff not applied as expected: %v", elapsed)
	}
}

func TestBeginResourcesReportsRetryableErrorAfterBudget(t *testing.T) {
	helper, _ := fakeHelper(t, 1000)
	_, err := BeginResourcesWithRetry(context.Background(), testClaim(helper), t.TempDir(), "codexpro-job_x.scope", 1500*time.Millisecond)
	var typed *wire.Error
	if !errors.As(err, &typed) || typed.Code != "host_resources_unavailable" || typed.Data["retry_unchanged"] != true {
		t.Fatalf("exhausted retry is not a retryable error: %#v", err)
	}
}

func TestBeginResourcesDoesNotRetryInvalidClaims(t *testing.T) {
	helper, counter := fakeHelper(t, 0)
	claim := testClaim(helper)
	claim.RunID = "not-hex"
	if _, err := BeginResourcesWithRetry(context.Background(), claim, t.TempDir(), "u.scope", 10*time.Second); err == nil || errors.Is(err, ErrHelperFailed) {
		t.Fatalf("invalid claim not rejected directly: %v", err)
	}
	if calls(t, counter) != 0 {
		t.Fatal("helper invoked for an invalid claim")
	}
}
