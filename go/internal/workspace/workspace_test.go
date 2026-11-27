package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

func fixture(t *testing.T) (*Manager, *files.Service, wire.Scope) {
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
	f, e := files.New(cfg, m)
	if e != nil {
		t.Fatal(e)
	}
	return m, f, wire.Scope{PrincipalID: "principal-a", SessionID: "session-a", TransportID: "http", RequestID: "1"}
}
func TestCatalogAndInstructionPrecedenceAcrossSessions(t *testing.T) {
	m, f, scope := fixture(t)
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
	if !m.WasOpen(wire.Scope{PrincipalID: scope.PrincipalID, SessionID: "another"}, ws.ID) || !m.WasOpen(wire.Scope{PrincipalID: scope.PrincipalID}, ws.ID) {
		t.Fatal("open state is not shared by the principal's MCP requests")
	}
	if m.WasOpen(wire.Scope{PrincipalID: "another", SessionID: scope.SessionID}, ws.ID) {
		t.Fatal("session opening leaked to another principal")
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

func TestDirectManagerIgnoresRetiredLeaseState(t *testing.T) {
	m, _, scope := fixture(t)
	leasePath := filepath.Join(m.cfg.Home, "workspaces.sqlite")
	if _, err := os.Stat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("direct workspace manager created lease state: %v", err)
	}
	historical := []byte("archived lease data")
	if err := os.WriteFile(leasePath, historical, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	project, _ := m.Project("test")
	ws, err := reopened.Resolve(context.Background(), scope, "")
	if err != nil || ws.ID != IDForRoot(project.Root) || ws.Root != project.Root {
		t.Fatalf("direct workspace identity changed: %+v %v", ws, err)
	}
	if _, err := reopened.Resolve(context.Background(), scope, "wt_historical"); err == nil {
		t.Fatal("retired worktree ID resolved to a project")
	}
	if contents, err := os.ReadFile(leasePath); err != nil || !bytes.Equal(contents, historical) {
		t.Fatalf("direct workspace manager changed the archived DB: %v", err)
	}
}
func TestInstructionSymlinkCannotEscapeWorkspace(t *testing.T) {
	m, f, scope := fixture(t)
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
func TestExternalCatalogChangePreventsProjectSideEffects(t *testing.T) {
	m, _, scope := fixture(t)
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
