package engine

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// CallBatchChild is the equivalent of tools/context.ts's registeredToolHandler,
// which stores the validated domain handler, not registerToolCompat's outer MCP
// wrapper. The enclosing batch already owns its lease and audit action. Running
// Engine.Call again would add child audit rows, acknowledge background digests,
// redact errors before the batch's workspace is available and retake the lease.
func (e *Engine) CallBatchChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	switch name {
	case "tree", "search", "ast_grep", "read", "inspect_workspace", "show_changes", "write", "edit", "apply_patch", "bash":
	default:
		return wire.Result{}, wire.Errorf("batch_child_not_allowed", "Tool is not available as a batch child: %s", name)
	}
	validated, err := e.Registry.ValidateInner(name, args)
	if err != nil {
		if validation, ok := err.(*contracts.ValidationError); ok {
			err = validation.BusinessError()
		}
		return wire.Result{}, err
	}
	result, err := e.dispatch(ctx, scope, name, validated)
	if err != nil {
		return result, err
	}
	// Go's shared domain-result helper supplies outer presentation tags. The
	// source raw child handlers do not; remove only these two manufactured keys,
	// before batch structured budgets and output receipts are calculated.
	if result.StructuredContent != nil {
		data := make(map[string]any, len(result.StructuredContent))
		for key, value := range result.StructuredContent {
			if key != "codexpro_tool" && key != "codexpro_title" {
				data[key] = value
			}
		}
		result.StructuredContent = data
	}
	return result, nil
}
