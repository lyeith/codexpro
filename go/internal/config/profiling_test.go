package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilingStartupIsExplicitAndPrivate(t *testing.T) {
	state, root := startupFixture(t)
	writeStartupProfile(t, state, root, map[string]any{"pprofListen": "0.0.0.0:6060"})
	cfg, err := Load(startupArgs(state))
	if err != nil || cfg.String("pprofListen", "") != "" {
		t.Fatalf("saved profile enabled diagnostics: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "limits.env"), []byte("CODEXPRO_PPROF_LISTEN=127.0.0.1:6060\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(startupArgs(state))
	if err != nil || cfg.String("pprofListen", "") != "127.0.0.1:6060" {
		t.Fatalf("private environment file setting missing: %v", err)
	}
	t.Setenv("CODEXPRO_PPROF_LISTEN", "[::1]:6061")
	cfg, err = Load(startupArgs(state))
	if err != nil || cfg.String("pprofListen", "") != "[::1]:6061" {
		t.Fatalf("process setting did not take precedence: %v", err)
	}
	cfg, err = Load(append(startupArgs(state), "--pprof-listen", "127.0.0.1:0"))
	if err != nil || cfg.String("pprofListen", "") != "127.0.0.1:0" {
		t.Fatalf("explicit CLI setting did not take precedence: %v", err)
	}
	if err := cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if cfg.String("pprofListen", "") != "127.0.0.1:0" {
		t.Fatal("operational reload altered the startup-only diagnostics setting")
	}
	cfg, err = Load(append(startupArgs(state), "--pprof-listen="))
	if err != nil || cfg.String("pprofListen", "") != "" {
		t.Fatalf("explicit empty CLI setting did not disable diagnostics: %v", err)
	}
}

func TestProfilingStartupRejectsUnsafeAddressBeforeCreatingState(t *testing.T) {
	state, _ := startupFixture(t)
	for _, address := range []string{"0.0.0.0:6060", "[::]:6060", ":6060", "localhost:6060", "192.0.2.1:6060", "127.0.0.1:65536"} {
		t.Run(address, func(t *testing.T) {
			if _, err := Load(append(startupArgs(state), "--pprof-listen", address)); err == nil || !strings.Contains(err.Error(), "pprof-listen") {
				t.Fatalf("invalid profiling setting accepted: %v", err)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "must-not-be-created")
	_, err := Load(append(startupArgs(missing), "--projects-file", filepath.Join(state, "projects.json"), "--pprof-listen", "0.0.0.0:6060"))
	if err == nil {
		t.Fatal("unsafe address accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("invalid startup created state: %v", err)
	}
}
