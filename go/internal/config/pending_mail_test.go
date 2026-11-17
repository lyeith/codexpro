package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pendingBindingsFixture = `[{"label":"SP9","principal":"fixture-fleet","agent":"fixture-worker","mailbox":"fixture/fleet","logical_recipient":"SP9"},{"label":"AP1","principal":"fixture-app","agent":"fixture-mobile","mailbox":"fixture/mobile","logical_recipient":""}]`

func TestPendingMailBindingConfigurationIsExplicit(t *testing.T) {
	got, err := ParsePendingMailBindings(pendingBindingsFixture)
	if err != nil || got["SP9"].LogicalRecipient != "SP9" || got["AP1"].Mailbox != "fixture/mobile" || got["AP1"].LogicalRecipient != "" {
		t.Fatal("explicit bindings were not retained", err)
	}
	for _, raw := range []string{
		"", "null", "[]", pendingBindingsFixture + "{}",
		strings.Replace(pendingBindingsFixture, `"logical_recipient":""`, `"logical_recipient":null`, 1),
		strings.Replace(pendingBindingsFixture, `,"logical_recipient":""`, "", 1),
		strings.Replace(pendingBindingsFixture, `"label":"SP9"`, `"label":"SP9","label":"SP8"`, 1),
		strings.Replace(pendingBindingsFixture, `"label":"SP9"`, `"Label":"SP9"`, 1),
		strings.Replace(pendingBindingsFixture, `"AP1"`, `"SP9"`, 1),
		strings.Replace(pendingBindingsFixture, `"SP9"`, `"ROOT"`, 1),
		strings.Replace(pendingBindingsFixture, "fixture/fleet", "fixture/fleet\\ninjected", 1),
		strings.Replace(pendingBindingsFixture, `"fixture-worker"`, `"fixture-worker","token":"forbidden"`, 1),
		strings.Repeat(" ", 16<<10) + pendingBindingsFixture,
	} {
		if _, err := ParsePendingMailBindings(raw); err == nil {
			t.Fatalf("ambiguous or invalid routing accepted: %q", raw[:min(len(raw), 180)])
		}
	}
}

func pendingMailStartupArgs(state string) []string {
	return append(startupArgs(state),
		"--principals-file", filepath.Join(state, "private-policy.json"),
		"--pending-mail-socket", filepath.Join(state, "agentmail.sock"),
		"--pending-mail-bindings", pendingBindingsFixture)
}

func TestPendingMailTrustedStartupAndReloadIsolation(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{
		"pendingMailSocket":   filepath.Join(state, "untrusted.sock"),
		"pendingMailBindings": pendingBindingsFixture,
	})
	cfg, err := Load(startupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != "" || cfg.String("pendingMailBindings", "") != "" {
		t.Fatal("saved profile enabled mail notices", err)
	}
	t.Setenv("CODEXPRO_PRINCIPALS_FILE", filepath.Join(state, "private-policy.json"))
	t.Setenv("CODEXPRO_PENDING_MAIL_SOCKET", filepath.Join(state, "env.sock"))
	t.Setenv("CODEXPRO_PENDING_MAIL_BINDINGS", pendingBindingsFixture)
	cfg, err = Load(startupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != filepath.Join(state, "env.sock") {
		t.Fatal("trusted environment was not applied", err)
	}
	cfg, err = Load(pendingMailStartupArgs(state))
	if err != nil || cfg.String("pendingMailSocket", "") != filepath.Join(state, "agentmail.sock") {
		t.Fatal("explicit startup did not override environment", err)
	}
	before := cfg.Snapshot()
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte("CODEXPRO_PENDING_MAIL_SOCKET=/different.sock\nCODEXPRO_PENDING_MAIL_BINDINGS=[]\nCODEXPRO_MAX_JOBS=7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Reload(); err != nil || cfg.Int("maxJobs", 0) != 7 {
		t.Fatal("ordinary reload failed", err)
	}
	for _, key := range []string{"pendingMailSocket", "pendingMailBindings"} {
		if cfg.String(key, "") != before[key] {
			t.Fatalf("reload changed startup-only %s", key)
		}
	}
}

func TestPendingMailRequiresCompleteNamedLocalSettings(t *testing.T) {
	for _, missing := range []string{"principals-file", "pending-mail-socket", "pending-mail-bindings"} {
		t.Run(missing, func(t *testing.T) {
			state, _ := startupFixture(t)
			args := pendingMailStartupArgs(state)
			for i := range args {
				if args[i] == "--"+missing {
					args = append(args[:i], args[i+2:]...)
					break
				}
			}
			if _, err := Load(args); err == nil {
				t.Fatal("incomplete routing was accepted")
			}
		})
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
