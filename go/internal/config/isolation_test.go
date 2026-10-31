package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolationFixture(t *testing.T) (string, string, string) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CODEXPRO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".codexpro-go")
	live := filepath.Join(home, ".codexpro")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(live, 0700); err != nil {
		t.Fatal(err)
	}
	return home, state, live
}

func TestDerivedStoreDirectoryCannotFollowLiveState(t *testing.T) {
	for _, name := range []string{"jobs", "usage", "inbox", "audit", "profiles", "runtime", "worktrees"} {
		t.Run(name, func(t *testing.T) {
			_, state, live := isolationFixture(t)
			args := []string{"--state-dir", state, "--codex-dir", filepath.Join(state, "instructions")}
			if _, err := Load(args); err != nil {
				t.Fatalf("private control startup failed: %v", err)
			}
			target := filepath.Join(live, name)
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(state, name)); err != nil {
				t.Fatal(err)
			}
			cfg := New(state)
			if err := cfg.ValidateIsolation(); err == nil {
				t.Fatalf("%s directory escaped isolation", name)
			}
			if _, err := Load(args); err == nil || !strings.Contains(err.Error(), "overlaps live state") {
				t.Fatalf("startup accepted %s directory alias or rejected an unrelated setting: %v", name, err)
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("validation wrote through %s alias", name)
			}
		})
	}
}

func TestDerivedStoreLeafAndSidecarCannotFollowLiveState(t *testing.T) {
	databaseNames := []string{"workspaces.sqlite", "jobs.sqlite", "usage/usage.sqlite", "inbox/inbox.sqlite", "audit/index.sqlite"}
	leaves := []string{"audit/writer.lock", "audit/segment-00000000000001.jsonl", "audit/compact-00000000000001.jsonl"}
	for _, name := range databaseNames {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			leaves = append(leaves, name+suffix)
		}
	}
	for _, relative := range leaves {
		t.Run(relative, func(t *testing.T) {
			_, state, live := isolationFixture(t)
			target := filepath.Join(live, "must-stay-unmodified")
			const marker = "live fixture state\n"
			if err := os.WriteFile(target, []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(state, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, destination); err != nil {
				t.Fatal(err)
			}
			if err := New(state).ValidateIsolation(); err == nil {
				t.Fatalf("accepted derived leaf alias %s", relative)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != marker {
				t.Fatal("validation changed live fixture data")
			}
		})
	}
}

func TestDerivedStateRejectsHardlinkedWritableFiles(t *testing.T) {
	for _, relative := range []string{"workspaces.sqlite", "jobs.sqlite", "usage/usage.sqlite", "inbox/inbox.sqlite-shm", "audit/writer.lock", "audit/index.sqlite-wal", "audit/segment-01.jsonl"} {
		t.Run(relative, func(t *testing.T) {
			_, state, live := isolationFixture(t)
			target := filepath.Join(live, "original")
			if err := os.WriteFile(target, []byte("unchanged\n"), 0600); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(state, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, alias); err != nil {
				t.Fatal(err)
			}
			if err := New(state).ValidateIsolation(); err == nil || !strings.Contains(err.Error(), "hard link") {
				t.Fatalf("hardlinked %s accepted: %v", relative, err)
			}
		})
	}
}

func TestCanonicalLiveRootCannotBeReachedThroughItsRealPath(t *testing.T) {
	for _, relative := range []string{".codexpro", ".config/codexpro"} {
		t.Run(relative, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			real := filepath.Join(home, "relocated-live-state")
			if err := os.MkdirAll(real, 0700); err != nil {
				t.Fatal(err)
			}
			declared := filepath.Join(home, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(declared), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, declared); err != nil {
				t.Fatal(err)
			}
			if err := protectedPath(filepath.Join(real, "new-file")); err == nil {
				t.Fatal("real path behind live-state symlink was accepted")
			}
			cfg := New(filepath.Join(real, "new-go-state"))
			if err := cfg.ValidateIsolation(); err == nil {
				t.Fatal("canonical live Home accepted")
			}
		})
	}
}

func TestCanonicalAccountHomeAliasAndPrivateSibling(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "real-home")
	if err := os.MkdirAll(filepath.Join(realHome, ".codexpro"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "home-alias")
	if err := os.Symlink(realHome, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	if err := protectedPath(filepath.Join(realHome, ".codexpro", "new")); err == nil {
		t.Fatal("symlinked HOME hid live state")
	}
	private := New(filepath.Join(realHome, ".codexpro-go"))
	if err := private.ValidateIsolation(); err != nil {
		t.Fatalf("separate private sibling rejected: %v", err)
	}
	if _, err := os.Stat(private.Home); !os.IsNotExist(err) {
		t.Fatal("validation created private directories")
	}
}

func TestPrivateExistingStoreFilesRemainValid(t *testing.T) {
	_, state, _ := isolationFixture(t)
	for _, relative := range []string{"workspaces.sqlite", "jobs.sqlite", "jobs/job_fixture.spec.json", "usage/usage.sqlite", "inbox/inbox.sqlite", "audit/index.sqlite", "audit/writer.lock", "audit/segment-01.jsonl", "audit/compact-01.jsonl"} {
		path := filepath.Join(state, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := New(state).ValidateIsolation(); err != nil {
		t.Fatalf("independent ordinary state rejected: %v", err)
	}
}
