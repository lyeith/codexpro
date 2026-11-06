//go:build linux

package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Keep the same fixture files between processes. Session IDs and touch revisions
// may change; lease ownership and the original repository/branch must not.
func TestNamedPolicyWorkspaceStateTransfer(t *testing.T) {
	root, phase := namedPolicyWorkspaceFixture(t)
	repo := filepath.Join(root, "repo")
	cfg := config.New(filepath.Join(root, "go-state"))
	cfg.Set("worktreeMode", "mcp")
	cfg.Set("worktreeRoot", filepath.Join(root, "worktrees"))
	cfg.Set("codexDir", filepath.Join(root, "codex"))
	cfg.ProjectsFile = filepath.Join(root, "projects.json")
	idsPath := filepath.Join(root, "lease-ids.json")
	if phase == "seed" {
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal("seed requires a new fixture repository", err)
		}
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"),
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost",
				"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost"}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture git: %v: %s", err, output)
			}
		}
		git("init", "--quiet", "--initial-branch=main")
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("disposable fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "README.md")
		git("-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "fixture")
		catalog := Catalog{Version: 1, DefaultProject: "fixture",
			Projects: []wire.Project{{ID: "fixture", Label: "Fixture", Root: repo, BaseRef: "HEAD"}}}
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(cfg.ProjectsFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot save fixture catalog", writeErr, closeErr)
		}
	}
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owners := []string{"original-legacy-key", "principal:fleet"}
	type retainedLease struct {
		ID, RepositoryID, Branch, BaseCommit, CheckoutRoot, WorkspaceRoot string
	}
	var retained []retainedLease
	if phase == "seed" {
		for i, owner := range owners {
			scope := wire.Scope{PrincipalID: owner, SessionID: "seed"}
			handle, err := manager.CreateWorkspace(ctx, scope, map[string]any{
				"project_id": "fixture", "idempotency_key": "same-key",
			})
			if err != nil || !handle.Created {
				t.Fatalf("seed lease %d failed: %v", i, err)
			}
			lease := handle.Lease
			retained = append(retained, retainedLease{handle.Workspace.ID, lease.RepositoryID,
				lease.Branch, lease.BaseCommit, lease.CheckoutRoot, lease.WorkspaceRoot})
		}
		data, err := json.Marshal(retained)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(idsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot save fixture lease identities", writeErr, closeErr)
		}
	} else {
		data, err := os.ReadFile(idsPath)
		if err != nil || json.Unmarshal(data, &retained) != nil || len(retained) != 2 {
			t.Fatal("missing original fixture lease identities")
		}
	}
	if len(retained) != 2 || retained[0].ID == retained[1].ID {
		t.Fatal("legacy and named owners acquired the same lease")
	}
	for i, owner := range owners {
		expected := retained[i]
		scope := wire.Scope{PrincipalID: owner, SessionID: "after-restart"}
		lease, ok := manager.leaseSnapshot(expected.ID)
		if !ok || lease.Version != 2 || lease.OwnerID != owner || lease.ProjectID != "fixture" ||
			lease.RepositoryID != expected.RepositoryID || lease.Branch != expected.Branch ||
			lease.BaseCommit != expected.BaseCommit || lease.CheckoutRoot != expected.CheckoutRoot ||
			lease.WorkspaceRoot != expected.WorkspaceRoot ||
			!IsSubpath(lease.CheckoutRoot, filepath.Join(root, "worktrees")) {
			t.Fatalf("lease %d lost its original owner, repository, branch or configured root", i)
		}
		opened, err := manager.Resolve(ctx, scope, expected.ID)
		if err != nil || opened.Root != expected.WorkspaceRoot {
			t.Fatalf("original lease owner cannot resolve %d: %v", i, err)
		}
		again, err := manager.CreateWorkspace(ctx, scope, map[string]any{
			"project_id": "fixture", "idempotency_key": "same-key",
		})
		if err != nil || again.Created || again.Workspace.ID != expected.ID || again.Lease.Branch != expected.Branch {
			t.Fatalf("recovery changed lease or branch %d: %v", i, err)
		}
		if _, err := manager.Resolve(ctx, wire.Scope{PrincipalID: owners[1-i]}, expected.ID); err == nil {
			t.Fatal("legacy/named lease ownership was transferred")
		}
	}
}

func namedPolicyWorkspaceFixture(t *testing.T) (string, string) {
	t.Helper()
	root := os.Getenv("NAMED_POLICY_FIXTURE_ROOT")
	if root == "" {
		t.Skip("opt-in disposable named-policy fixture only")
	}
	phase := os.Getenv("NAMED_POLICY_FIXTURE_PHASE")
	if phase != "seed" && phase != "check" {
		t.Fatal("fixture phase must be seed or check")
	}
	real, err := filepath.EvalSymlinks(root)
	info, statErr := os.Lstat(root)
	if err != nil || statErr != nil || !filepath.IsAbs(root) || real != filepath.Clean(root) ||
		!info.IsDir() || info.Mode().Perm() != 0700 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		t.Fatal("fixture root must be an owned private real directory")
	}
	marker, err := os.ReadFile(filepath.Join(root, "DISPOSABLE-NAMED-POLICY-FIXTURE"))
	if err != nil || string(marker) != "named-policy-fixture-v1\n" {
		t.Fatal("missing disposable fixture marker")
	}
	return root, phase
}
