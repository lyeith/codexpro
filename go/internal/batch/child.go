package batch

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Real engines expose the raw validated handler used by the reference batch
// registry. A simple injected domain caller remains supported for embedding and
// focused tests; it must not be an independently wrapped MCP invocation.
type rawBatchCaller interface {
	CallBatchChild(context.Context, wire.Scope, string, map[string]any) (wire.Result, error)
}

func (s *Service) callChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if raw, ok := s.caller.(rawBatchCaller); ok {
		return raw.CallBatchChild(ctx, scope, name, args)
	}
	return s.caller.Call(ctx, scope, name, args)
}
