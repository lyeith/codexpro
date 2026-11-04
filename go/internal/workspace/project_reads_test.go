package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestGrantedProjectReadOwnerAndMode(t *testing.T) {
	for _, managed := range []bool{false, true} {
		m, f, scope := fixture(t, managed)
		ctx := wire.WithProjects(context.Background(), []string{"test", "aggregate"})
		scope.Identity = wire.Identity{Source: "named", PrincipalType: "person", PrincipalID: "alex", Role: "tester"}
		p, _ := m.Project("test")
		direct := IDForRoot(p.Root)
		id := direct
		if managed {
			h, err := m.CreateWorkspace(ctx, scope, map[string]any{"project_id": "test"})
			if err != nil {
				t.Fatal(err)
			}
			id = h.Workspace.ID
			if _, err := m.ResolveRead(ctx, scope, direct); err == nil {
				t.Fatal("direct checkout exposed in managed mode")
			}
			foreign := scope
			foreign.PrincipalID = "other"
			if _, err := m.ResolveRead(ctx, foreign, id); err == nil {
				t.Fatal("foreign managed checkout")
			}
		}
		// Even a repository marker on an enclosing aggregate cannot admit it.
		parent := filepath.Dir(p.Root)
		if err := os.Mkdir(filepath.Join(parent, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		m.catalog.Projects = append(m.catalog.Projects, wire.Project{ID: "aggregate", Root: parent, Label: "forbidden"})
		projects := m.ReadProjects(ctx, scope)
		if len(projects) != 1 || projects[0].ID != "test" || !slices.Equal(projects[0].WorkspaceIDs, []string{id}) {
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
}
