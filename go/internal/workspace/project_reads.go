package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// ReadProject contains only the identifiers a personal reader can actually use.
type ReadProject struct {
	ID, Label, WorkspaceID string
}

func (m *Manager) safeReadProject(p wire.Project) bool {
	home, _ := os.UserHomeDir()
	if p.ID == "projects" || p.Root == home || p.Root == filepath.Join(home, "Projects") || filepath.Dir(p.Root) == p.Root ||
		IsSubpath(m.cfg.Home, p.Root) || IsSubpath(p.Root, m.cfg.Home) {
		return false
	}
	actual, err := filepath.EvalSymlinks(p.Root)
	if err != nil || actual != p.Root {
		return false
	}
	// A grant cannot turn a parent of other catalogued repositories or
	// creation roots into a shortcut around their own grants.
	for _, other := range m.Projects() {
		if other.ID != p.ID && IsSubpath(other.Root, p.Root) {
			return false
		}
	}
	for _, other := range m.CreationRoots() {
		if IsSubpath(other.Root, p.Root) {
			return false
		}
	}
	marker, err := os.Lstat(filepath.Join(p.Root, ".git"))
	return err == nil && (marker.IsDir() || marker.Mode().IsRegular())
}

func (m *Manager) ReadProjects(ctx context.Context) []ReadProject {
	out := []ReadProject{}
	grants := wire.ProjectsFrom(ctx)
	for _, p := range m.Projects() {
		if ctx.Err() != nil || !slices.Contains(grants, p.ID) || !m.safeReadProject(p) {
			continue
		}
		out = append(out, ReadProject{ID: p.ID, Label: p.Label, WorkspaceID: IDForRoot(p.Root)})
	}
	return out
}

func (m *Manager) ResolveRead(ctx context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	if id != "" {
		for _, p := range m.ReadProjects(ctx) {
			if p.WorkspaceID == id {
				if w, err := m.OpenProject(scope, p.ID, false); err == nil {
					return w, nil
				}
			}
		}
	}
	return wire.Workspace{}, wire.Errorf("permission_denied", "An available granted workspace_id is required.")
}
