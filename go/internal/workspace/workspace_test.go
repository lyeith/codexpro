package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func fixture(t *testing.T, managed bool) (*Manager, *files.Service, wire.Scope) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "project")
	created := filepath.Join(root, "created")
	for _, p := range []string{repo, created} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Contract", "GIT_AUTHOR_EMAIL=contract@localhost", "GIT_COMMITTER_NAME=Contract", "GIT_COMMITTER_EMAIL=contract@localhost")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v: %s", args, e, b)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	if e := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "README.md")
	run("-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "fixture")
	cfg := config.New(filepath.Join(root, "state"))
	if managed {
		cfg.Set("worktreeMode", "mcp")
	}
	cfg.Set("codexDir", filepath.Join(root, "global"))
	if e := os.MkdirAll(cfg.Home, 0700); e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(Catalog{Version: 1, DefaultProject: "test", Projects: []wire.Project{{ID: "test", Label: "Test", Root: repo, BaseRef: "HEAD"}}, CreationRoots: []CreationRoot{{ID: "new", Label: "New", Root: created}}})
	if e := os.WriteFile(cfg.ProjectsFile, data, 0600); e != nil {
		t.Fatal(e)
	}
	m, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	f, e := files.New(cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	return m, f, wire.Scope{PrincipalID: "principal-a", SessionID: "session-a", TransportID: "http", RequestID: "1"}
}
func TestCatalogAndInstructionPrecedenceAcrossSessions(t *testing.T) {
	m, f, scope := fixture(t, false)
	ctx := context.Background()
	ws, e := m.OpenProject(scope, "test", true)
	if e != nil {
		t.Fatal(e)
	}
	global := m.cfg.String("codexDir", "")
	if e = os.MkdirAll(global, 0700); e != nil {
		t.Fatal(e)
	}
	for path, body := range map[string]string{filepath.Join(global, "AGENTS.md"): "Global rules", filepath.Join(ws.Root, "AGENTS.md"): "Regular rules", filepath.Join(ws.Root, "AGENTS.override.md"): "Override rules"} {
		if e = os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	instructions, e := m.ReadInstructions(ctx, f, ws, InstructionOptions{Global: true, Workspace: true, MaxBytes: -1})
	if e != nil {
		t.Fatal(e)
	}
	if len(instructions.Sources) != 2 || !strings.Contains(instructions.Text, "Override rules") || strings.Contains(instructions.Text, "Regular rules") || !instructions.Metadata()["agents_complete"].(bool) {
		t.Fatalf("incorrect precedence: %+v", instructions)
	}
	if m.WasOpen(wire.Scope{PrincipalID: scope.PrincipalID, SessionID: "another"}, ws.ID) {
		t.Fatal("session opening leaked")
	}
	if !m.WasOpen(scope, ws.ID) {
		t.Fatal("open state lost")
	}
	same, e := m.Resolve(ctx, wire.Scope{PrincipalID: scope.PrincipalID, SessionID: "another"}, ws.ID)
	if e != nil || same.ID != ws.ID {
		t.Fatalf("catalog identity is not stable: %+v %v", same, e)
	}
	truncated, e := m.ReadInstructions(ctx, f, ws, InstructionOptions{Global: true, Workspace: true, MaxBytes: 3})
	if e != nil {
		t.Fatal(e)
	}
	if truncated.Metadata()["agents_complete"].(bool) {
		t.Fatal("truncated instructions called complete")
	}
}
func TestInstructionSymlinkCannotEscapeWorkspace(t *testing.T) {
	m, f, scope := fixture(t, false)
	ws, e := m.OpenProject(scope, "test", true)
	if e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "rules.md")
	if e = os.WriteFile(outside, []byte("outside-only-content"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(ws.Root, "AGENTS.md")); e != nil {
		t.Fatal(e)
	}
	result, e := m.ReadInstructions(context.Background(), f, ws, InstructionOptions{Workspace: true, MaxBytes: -1})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(result.Text, "outside-only-content") || result.Metadata()["agents_complete"].(bool) || len(result.Sources) != 1 {
		t.Fatalf("unsafe instruction read: %+v", result)
	}
}
func TestLeaseProbeDoesNotHoldOwnershipAndRevisionWins(t *testing.T) {
	m, _, scope := fixture(t, true)
	ctx := context.Background()
	h, e := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test", "idempotency_key": "stable"})
	if e != nil {
		t.Fatal(e)
	}
	repeated, e := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test", "idempotency_key": "stable"})
	if e != nil || repeated.Created || repeated.Workspace.ID != h.Workspace.ID {
		t.Fatalf("retry created another checkout: %+v %v", repeated, e)
	}
	if _, e = m.Resolve(ctx, wire.Scope{PrincipalID: "different"}, h.Workspace.ID); e == nil {
		t.Fatal("cross-principal workspace access accepted")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.Reconcile(ctx, func(context.Context, string) (Repository, error) {
			close(entered)
			<-release
			return Repository{}, errors.New("transient resource exhaustion")
		})
	}()
	<-entered
	readDone := make(chan error, 1)
	go func() {
		_, err := m.Execute(ctx, scope, h.Workspace.ID, false, func() (wire.Result, error) { return wire.Result{}, nil })
		readDone <- err
	}()
	select {
	case e = <-readDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Git probe blocked tool ownership")
	}
	// This durable revision must invalidate the older observation.
	if _, e = m.ReleaseWorkspace(ctx, scope, h.Workspace.ID); e != nil {
		t.Fatal(e)
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	l, _ := m.leaseSnapshot(h.Workspace.ID)
	if l.ReleasedAt == "" || l.Health != "healthy" || l.State != "ready" {
		t.Fatalf("stale health probe overwrote newer lease: %+v", l)
	}
	if e = m.Reconcile(ctx, func(context.Context, string) (Repository, error) {
		return Repository{}, errors.New("spawn temporarily unavailable")
	}); e != nil {
		t.Fatal(e)
	}
	l, _ = m.leaseSnapshot(h.Workspace.ID)
	if l.State != "ready" || l.Health != "unavailable" {
		t.Fatalf("transient failure orphaned lease: %+v", l)
	}
	if _, e = m.Resolve(ctx, scope, h.Workspace.ID); e != nil {
		t.Fatal("transient health failure blocked valid ownership", e)
	}
	if e = m.Reconcile(ctx, func(context.Context, string) (Repository, error) { return Repository{Common: "foreign"}, nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Resolve(ctx, scope, h.Workspace.ID); e == nil {
		t.Fatal("verified foreign repository accepted")
	}
}
func TestLeaseMissingRecoveryAndDirtyRemoval(t *testing.T) {
	m, _, scope := fixture(t, true)
	ctx := context.Background()
	h, e := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test", "idempotency_key": "recovery"})
	if e != nil {
		t.Fatal(e)
	}
	moved := h.Lease.CheckoutRoot + ".temporarily-missing"
	if e = os.Rename(h.Lease.CheckoutRoot, moved); e != nil {
		t.Fatal(e)
	}
	if e = m.Reconcile(ctx, nil); e != nil {
		t.Fatal(e)
	}
	l, _ := m.leaseSnapshot(h.Workspace.ID)
	if l.State != "orphaned" {
		t.Fatal("genuine missing checkout was not classified")
	}
	if e = os.Rename(moved, h.Lease.CheckoutRoot); e != nil {
		t.Fatal(e)
	}
	if e = m.Reconcile(ctx, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Resolve(ctx, scope, h.Workspace.ID); e != nil {
		t.Fatal("same verified repository did not recover", e)
	}
	file := filepath.Join(h.Workspace.Root, "untracked.txt")
	if e = os.WriteFile(file, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = m.RemoveWorkspace(ctx, scope, h.Workspace.ID); e == nil {
		t.Fatal("dirty checkout removed")
	}
	if b, e := os.ReadFile(file); e != nil || string(b) != "keep" {
		t.Fatal("dirty file was touched")
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if e = m.RemoveWorkspace(ctx, scope, h.Workspace.ID); e != nil {
		t.Fatal(e)
	}
	if _, ok := m.leaseSnapshot(h.Workspace.ID); ok {
		t.Fatal("removed lease persisted")
	}
	p, _ := m.Project("test")
	if _, e = m.runGit(ctx, p.Root, "rev-parse", "--verify", h.Lease.Branch); e != nil {
		t.Fatal("removal deleted retained branch", e)
	}
}
func TestExternalCatalogChangePreventsProjectSideEffects(t *testing.T) {
	m, _, scope := fixture(t, false)
	if e := os.WriteFile(m.cfg.ProjectsFile, []byte("{}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e := m.CreateProject(context.Background(), scope, map[string]any{"project_id": "new-project", "parent_id": "new", "directory": "new-project", "mode": "empty"})
	if e == nil {
		t.Fatal("external catalog overwrite accepted")
	}
	if _, e = os.Stat(filepath.Join(m.CreationRoots()[0].Root, "new-project")); !os.IsNotExist(e) {
		t.Fatal("project side effect happened before catalog CAS")
	}
}
