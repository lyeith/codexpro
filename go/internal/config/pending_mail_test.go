package config

import (
	"os"
	"path/filepath"
	"testing"
)

func pendingMailStartupArgs(state string) []string {
	return append(startupArgs(state),
		"--principals-file", filepath.Join(state, "private-policy.json"),
		"--pending-mail-socket", filepath.Join(state, "agentmail.sock"))
}

func TestPendingMailHasNoLabelRoutingConfiguration(t *testing.T) {
	state, _ := startupFixture(t)
	// A retained old environment variable is neither parsed nor kept.
	t.Setenv("CODEXPRO_PENDING_MAIL_BINDINGS", "obsolete routing must not be read")
	cfg, err := Load(pendingMailStartupArgs(state))
	if err != nil {
		t.Fatal(err)
	}
	if _, retained := cfg.Snapshot()["pendingMailBindings"]; retained {
		t.Fatal("CodexPro retained a label routing list")
	}
	if _, err := Load(append(pendingMailStartupArgs(state), "--pending-mail-bindings", "[]")); err == nil {
		t.Fatal("retired label routing flag was accepted")
	}
}

func TestPendingMailTrustedStartupAndReloadIsolation(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{
		"pendingMailSocket": filepath.Join(state, "untrusted.sock"),
	})
	cfg, err := Load(startupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != "" {
		t.Fatal("saved profile enabled mail notices", err)
	}
	t.Setenv("CODEXPRO_PRINCIPALS_FILE", filepath.Join(state, "private-policy.json"))
	t.Setenv("CODEXPRO_PENDING_MAIL_SOCKET", filepath.Join(state, "env.sock"))
	cfg, err = Load(startupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != filepath.Join(state, "env.sock") {
		t.Fatal("trusted environment was not applied", err)
	}
	cfg, err = Load(pendingMailStartupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != filepath.Join(state, "agentmail.sock") {
		t.Fatal("explicit startup did not override environment", err)
	}
	before := cfg.String("pendingMailSocket", "")
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte("CODEXPRO_PENDING_MAIL_SOCKET=/different.sock\nCODEXPRO_MAX_JOBS=7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Reload(); err != nil || cfg.Int("maxJobs", 0) != 7 {
		t.Fatal("ordinary reload failed", err)
	}
	if cfg.String("pendingMailSocket", "") != before {
		t.Fatal("reload changed the trusted startup socket")
	}
}

func TestPendingMailRequiresNamedLocalSettings(t *testing.T) {
	state, _ := startupFixture(t)
	if _, err := Load(append(startupArgs(state), "--pending-mail-socket", filepath.Join(state, "agentmail.sock"))); err == nil {
		t.Fatal("pending summary enabled without named authentication")
	}
	if _, err := Load(append(startupArgs(state), "--principals-file", filepath.Join(state, "private-policy.json"))); err != nil {
		t.Fatal("disabled pending summary required a socket", err)
	}
	for _, bad := range []string{"relative.sock", "https://example.invalid/mail", "/tmp/mail\nsocket"} {
		t.Run("bad socket", func(t *testing.T) {
			state, _ := startupFixture(t)
			if _, err := Load(append(pendingMailStartupArgs(state), "--pending-mail-socket", bad)); err == nil {
				t.Fatal("non-local socket accepted")
			}
		})
	}
}
