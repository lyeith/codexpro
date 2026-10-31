package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
)

// Explicit opt-in: these tests create only fresh private scopes and host resource
// claims. They never inspect, signal, or write the production CodexPro job store.
func TestHostScopeIntegration(t *testing.T) {
	helper := os.Getenv("CODEXPRO_GO_TEST_HOST_RESOURCES")
	if helper == "" {
		t.Skip("set CODEXPRO_GO_TEST_HOST_RESOURCES to the absolute installed helper to exercise user scopes")
	}
	initial, cfg, resolver := newFixture(t)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEXPRO_JOB_SCOPES", "1")
	cfg.HostResourcesHelper = helper
	cfg.Set("jobTimeoutMs", 10000)
	s, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s == nil {
			return
		}
		for _, r := range s.Snapshot() {
			if r.Status == "running" {
				_, _ = s.Stop(r.ID, "", "stopped")
			}
		}
		_ = s.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	waitQuiet := func(t *testing.T, id string) Record {
		t.Helper()
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			r, ok := s.Get(id, "")
			if ok && r.Status != "running" && r.Quiescent {
				return r
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		r, _ := s.Get(id, "")
		t.Fatalf("owner did not settle: status=%s quiet=%v scope=%s", r.Status, r.Quiescent, r.ScopeUnit)
		return Record{}
	}

	t.Run("drains_setsid_descendant_after_success", func(t *testing.T) {
		o := opts(resolver, "setsid sh -c 'echo $$ > escaped.pid; exec sleep 30' </dev/null >/dev/null 2>&1 & while [ ! -s escaped.pid ]; do sleep .01; done; sleep .4; printf done")
		o.TimeoutMS = 10000
		r, err := s.start(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(5 * time.Second)
		pid := 0
		identity := ""
		for time.Now().Before(until) {
			if b, e := os.ReadFile(filepath.Join(resolver.workspace.Root, "escaped.pid")); e == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				identity = process.ProcessIdentity(pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pid <= 0 || identity == "" {
			t.Fatal("escaped child did not establish its identity")
		}
		r = waitQuiet(t, r.ID)
		if r.Status != "succeeded" || r.QuiescenceScope != "systemd_scope" {
			t.Fatalf("wrong completion: status=%s proof=%s", r.Status, r.QuiescenceScope)
		}
		if process.ProcessAlive(pid, identity) {
			t.Fatal("setsid descendant survived scope settlement")
		}
		if r.ResourceClaim == nil || r.ScopeIdentity == nil {
			t.Fatal("exact resource/process owner identity not retained")
		}
	})

	t.Run("resource_environment_is_private_per_owner", func(t *testing.T) {
		o := opts(resolver, "printf '%s\\n' \"$TMPDIR\" \"$GOTMPDIR\" \"$GOCACHE\"; sleep 1.5")
		o.TimeoutMS = 10000
		a, err := s.start(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		o.Command += "; : second"
		b, err := s.start(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		a = waitQuiet(t, a.ID)
		b = waitQuiet(t, b.ID)
		left, err := os.ReadFile(a.StdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(b.StdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		l, r := strings.Fields(string(left)), strings.Fields(string(right))
		if len(l) != 3 || len(r) != 3 || l[0] == r[0] || l[1] == r[1] || l[2] != r[2] {
			t.Fatal("scratch/cache environment ownership mismatch")
		}
		if a.ResourceClaim.RunID == b.ResourceClaim.RunID || a.ResourceClaim.RegistrationID == b.ResourceClaim.RegistrationID {
			t.Fatal("host allocation identity reused")
		}
	})

	t.Run("restart_preserves_active_scope", func(t *testing.T) {
		o := opts(resolver, "sleep 1; printf recovered")
		o.TimeoutMS = 10000
		r, err := s.start(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = New(cfg, resolver)
		if err != nil {
			t.Fatal(err)
		}
		r = waitQuiet(t, r.ID)
		if r.Status != "succeeded" {
			t.Fatalf("scope did not survive manager restart: %s", r.Status)
		}
		out, err := s.ReadTail(ctx, r.ID, r.WorkspaceID, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if out.Stdout != "recovered" {
			t.Fatal("restart lost scope output")
		}
	})

	t.Run("cancel_before_begin_revokes_late_activation", func(t *testing.T) {
		run, err := process.RandomHex(16)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := process.RandomHex(16)
		if err != nil {
			t.Fatal(err)
		}
		claim := process.ResourceClaim{Version: 1, Helper: helper, RunID: run, RegistrationID: nonce}
		if _, err = process.CancelResources(ctx, claim); err != nil {
			t.Fatal(err)
		}
		incarnation, err := process.RandomHex(16)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = process.BeginResources(ctx, claim, resolver.workspace.Root, "codexpro-job_deadbeef-"+incarnation+".scope"); err == nil {
			t.Fatal("revoked allocation accepted delayed begin")
		}
	})
}
