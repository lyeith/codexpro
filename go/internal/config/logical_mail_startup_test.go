package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func logicalMailStartupArgs(state string) []string {
	return append(startupArgs(state),
		"--principals-file", filepath.Join(state, "private-policy.json"),
		"--logical-mail-socket", filepath.Join(state, "agentmail.sock"),
		"--logical-mail-principal", "fixture-fleet",
		"--logical-mail-agent", "fixture-worker",
		"--logical-mail-mailbox", "fixture/fleet",
	)
}

func TestLogicalMailIsDisabledByDefaultAndIgnoresSavedProfiles(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{
		"logicalMailSocket":    filepath.Join(state, "untrusted.sock"),
		"logicalMailPrincipal": "untrusted-principal",
		"logicalMailAgent":     "untrusted-agent",
		"logicalMailMailbox":   "untrusted/mailbox",
	})
	cfg, err := Load(startupArgs(state))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"logicalMailSocket", "logicalMailPrincipal", "logicalMailAgent", "logicalMailMailbox"} {
		if cfg.String(key, "") != "" {
			t.Fatalf("saved profile activated %s", key)
		}
	}
}

func TestLogicalMailTrustedStartupPrecedenceAndReloadIsolation(t *testing.T) {
	state, _ := startupFixture(t)
	env := map[string]string{
		"CODEXPRO_PRINCIPALS_FILE":        filepath.Join(state, "private-policy.json"),
		"CODEXPRO_LOGICAL_MAIL_SOCKET":    filepath.Join(state, "environment.sock"),
		"CODEXPRO_LOGICAL_MAIL_PRINCIPAL": "environment-principal",
		"CODEXPRO_LOGICAL_MAIL_AGENT":     "environment-agent",
		"CODEXPRO_LOGICAL_MAIL_MAILBOX":   "environment/mailbox",
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
	cfg, err := Load(startupArgs(state))
	if err != nil || cfg.String("logicalMailSocket", "") != env["CODEXPRO_LOGICAL_MAIL_SOCKET"] || cfg.String("logicalMailMailbox", "") != "environment/mailbox" {
		t.Fatal("trusted environment was not applied", err)
	}
	cfg, err = Load(logicalMailStartupArgs(state))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"logicalMailSocket":    filepath.Join(state, "agentmail.sock"),
		"logicalMailPrincipal": "fixture-fleet",
		"logicalMailAgent":     "fixture-worker",
		"logicalMailMailbox":   "fixture/fleet",
	} {
		if cfg.String(key, "") != want {
			t.Fatalf("explicit startup %s did not override environment", key)
		}
	}
	before := cfg.Snapshot()
	text := "CODEXPRO_LOGICAL_MAIL_SOCKET=/different.sock\nCODEXPRO_LOGICAL_MAIL_PRINCIPAL=different\nCODEXPRO_LOGICAL_MAIL_AGENT=different\nCODEXPRO_LOGICAL_MAIL_MAILBOX=different/mailbox\nCODEXPRO_MAX_JOBS=7\n"
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if cfg.Int("maxJobs", 0) != 7 {
		t.Fatal("ordinary operational reload stopped working")
	}
	for key, value := range before {
		if strings.HasPrefix(key, "logicalMail") && cfg.String(key, "") != value {
			t.Fatalf("operational reload changed startup-only %s", key)
		}
	}
}

func TestLogicalMailRequiresCompleteNamedLocalBinding(t *testing.T) {
	for _, missing := range []string{"principals-file", "logical-mail-socket", "logical-mail-principal", "logical-mail-agent", "logical-mail-mailbox"} {
		t.Run(missing, func(t *testing.T) {
			state, _ := startupFixture(t)
			args := logicalMailStartupArgs(state)
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "--"+missing {
					args = append(args[:i], args[i+2:]...)
					break
				}
			}
			if _, err := Load(args); err == nil {
				t.Fatalf("partial logical-mail configuration without %s was accepted", missing)
			}
		})
	}
	for name, bad := range map[string]string{
		"relative socket": "relative.sock",
		"remote URL":      "https://example.invalid/mail",
		"abstract socket": "\x00abstract",
		"newline socket":  "/private/agentmail\n.sock",
	} {
		t.Run(name, func(t *testing.T) {
			state, _ := startupFixture(t)
			args := append(logicalMailStartupArgs(state), "--logical-mail-socket", bad)
			if _, err := Load(args); err == nil {
				t.Fatal("non-local or malformed logical-mail socket was accepted")
			}
		})
	}
	for _, bad := range []string{"different principal", "line\nfeed", "\x00hidden", strings.Repeat("x", 257)} {
		t.Run("bad identity "+strings.ReplaceAll(bad[:min(len(bad), 12)], "\x00", "NUL"), func(t *testing.T) {
			state, _ := startupFixture(t)
			if _, err := Load(append(logicalMailStartupArgs(state), "--logical-mail-principal", bad)); err == nil {
				t.Fatal("malformed configured principal was accepted")
			}
		})
	}
	state, _ := startupFixture(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(append(logicalMailStartupArgs(state), "--logical-mail-socket", filepath.Join(home, ".codexpro", "retained.sock"))); err == nil {
		t.Fatal("logical mail could open retained TypeScript state")
	}
}
