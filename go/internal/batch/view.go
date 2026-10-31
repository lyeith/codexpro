package batch

import (
	"context"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// ReadDefinition validates a saved definition without running preflight effects,
// retaining it, pruning another file, changing Git excludes, or executing a child.
func (s *Service) ReadDefinition(ctx context.Context, w wire.Workspace, path string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := s.load(w, path)
	if err != nil {
		return nil, err
	}
	if _, _, _, err = parseStored(loaded.Path, loaded.Raw); err != nil {
		return nil, err
	}
	return map[string]any{"path": loaded.Path, "autoStored": loaded.AutoStored, "definition": loaded.Raw}, nil
}
