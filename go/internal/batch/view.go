package batch

import (
	"context"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// ReadDefinition preserves the source viewer contract: it checks the path,
// text bounds and JSON syntax, then displays any parsed JSON value. Execution
// retains its separate strict parseStored gate. Reading never maintains or
// prunes recipes, changes Git excludes, or calls a child.
func (s *Service) ReadDefinition(ctx context.Context, w wire.Workspace, path string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded, err := s.load(w, path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": loaded.Path, "autoStored": loaded.AutoStored, "definition": loaded.Raw}, nil
}
