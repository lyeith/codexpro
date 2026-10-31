package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

// An absent allocation can truthfully be quiescent at the instant finish is
// called while an older begin request is still in flight. Only revocation of
// the caller's exact registration closes that future-admission race. This
// fixture models that protocol distinction without touching the host helper.
func TestPreparedRecoveryRevokesLateBeginAfterAbsentFinish(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	r, _, err := s.admit(opts(resolver, "ungranted command must never execute"))
	if err != nil {
		t.Fatal(err)
	}
	run, registration := strings.Repeat("a", 32), strings.Repeat("b", 32)
	marker := filepath.Join(resolver.workspace.Root, "registration-revoked")
	helper := filepath.Join(resolver.workspace.Root, "absent-allocation-helper")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
case "$2" in
  finish)
    printf '%%s' '{"run_id":"%s","status":"absent","quiescent":true,"cancelled":false}'
    ;;
  cancel)
    printf '%%s' "$3:$5" > %s
    printf '%%s' '{"run_id":"%s","status":"cancelled","quiescent":true,"cancelled":true}'
    ;;
  begin)
    if [ -f %s ]; then exit 125; fi
    printf '%%s' '{"run_id":"%s","registration_id":"%s","scratch":"/unused","environment":{},"owner":{}}'
    ;;
  *) exit 126 ;;
esac
`, run, quote(marker), run, quote(marker), run, registration)
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	claim := process.ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: registration}
	_, err = s.update(r.ID, func(next *Record) bool {
		next.ResourceClaim = &claim
		next.ScopeUnit = "codexpro-" + r.ID + "-" + strings.Repeat("c", 32)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	until := time.Now().Add(4 * time.Second)
	for time.Now().Before(until) {
		r, _ = recovered.Get(r.ID, "")
		if r.Status != "running" && r.Quiescent {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if r.Status != "failed" || !r.Quiescent {
		t.Fatalf("prepared owner did not settle: status=%s quiescent=%v", r.Status, r.Quiescent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = process.BeginResources(ctx, claim, resolver.workspace.Root, r.ScopeUnit+".scope")
	if err == nil {
		t.Fatal("recovery released ownership while the delayed begin could still activate")
	}
	revoked, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(revoked) != run+":"+registration {
		t.Fatal("recovery did not revoke the persisted caller registration identity")
	}
	if _, err := os.Stat(r.GrantPath); !os.IsNotExist(err) {
		t.Fatal("recovery published an execution grant for the unaccepted command")
	}
}
