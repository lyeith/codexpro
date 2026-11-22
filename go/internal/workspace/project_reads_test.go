package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestGrantedProjectReadScope(t *testing.T) {
	m, f, scope := fixture(t)
	ctx := wire.WithProjects(context.Background(), []string{"test", "aggregate"})
	scope.Identity = wire.Identity{Source: "named", PrincipalType: "person", PrincipalID: "alex", Role: "tester"}
	p, _ := m.Project("test")
	id := IDForRoot(p.Root)
	// Even a repository marker on an enclosing aggregate cannot admit it.
	parent := filepath.Dir(p.Root)
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	m.catalog.Projects = append(m.catalog.Projects, wire.Project{ID: "aggregate", Root: parent, Label: "forbidden"})
	projects := m.ReadProjects(ctx)
	if len(projects) != 1 || projects[0].ID != "test" || projects[0].WorkspaceID != id {
		t.Fatalf("bad scoped catalog: %v", projects)
	}
	for _, bad := range []string{"", "unknown", IDForRoot(parent)} {
		_, err := m.Resolve(ctx, scope, bad)
		if err == nil || strings.Contains(err.Error(), parent) || strings.Contains(err.Error(), id) {
			t.Fatal("bad scope or leaked discovery", err)
		}
	}
	if _, err := m.Resolve(wire.WithProjects(ctx, nil), scope, id); err == nil {
		t.Fatal("withdrawn grant continued to work")
	}
	r, err := f.Call(ctx, scope, "read", map[string]any{"workspace_id": id, "path": "README.md"})
	if err != nil || !strings.Contains(r.StructuredContent["text"].(string), "fixture") || r.StructuredContent["root"] != nil || r.StructuredContent["edit_tag"] != nil {
		t.Fatalf("granted read: %v %v", r.StructuredContent, err)
	}
}
