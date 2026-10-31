package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfiguredManagedRootOwnsCheckoutAcrossRecovery(t *testing.T) {
	manager, _, scope := fixture(t, true)
	custom := filepath.Join(filepath.Dir(manager.cfg.Home), "separate-managed-root")
	manager.cfg.Set("worktreeRoot", custom)
	if err := manager.cfg.ValidateIsolation(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	handle, err := manager.CreateWorkspace(ctx, scope, map[string]any{
		"project_id": "test", "idempotency_key": "configured-root-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !IsSubpath(handle.Workspace.Root, custom) || handle.Workspace.Root == custom {
		t.Fatalf("configured storage was ignored: checkout=%s root=%s", handle.Workspace.Root, custom)
	}
	if _, err := os.Stat(filepath.Join(manager.cfg.Home, "worktrees")); !os.IsNotExist(err) {
		t.Fatalf("default storage was touched despite an explicit root: %v", err)
	}
	if err := manager.assertLeasePaths(handle.Lease); err != nil {
		t.Fatal(err)
	}
	cfg := manager.cfg
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	scope.SessionID = "session-after-restart"
	opened, err := recovered.Resolve(ctx, scope, handle.Workspace.ID)
	if err != nil || opened.Root != handle.Workspace.Root {
		t.Fatalf("persisted custom-root workspace could not be recovered: %+v %v", opened, err)
	}
	repeated, err := recovered.CreateWorkspace(ctx, scope, map[string]any{
		"project_id": "test", "idempotency_key": "configured-root-recovery",
	})
	if err != nil || repeated.Created || repeated.Workspace.ID != handle.Workspace.ID {
		t.Fatalf("retry after recovery changed the custom-root lease: %+v %v", repeated, err)
	}
	if err := recovered.RemoveWorkspace(ctx, scope, handle.Workspace.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(handle.Workspace.Root); !os.IsNotExist(err) {
		t.Fatalf("clean custom-root checkout was not removed: %v", err)
	}
	project, _ := recovered.Project("test")
	if _, err := recovered.runGit(ctx, project.Root, "show-ref", "--verify", "refs/heads/"+handle.Lease.Branch); err != nil {
		t.Fatalf("removal discarded the managed branch: %v", err)
	}
}
