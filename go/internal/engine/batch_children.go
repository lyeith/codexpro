package engine

import (
	"context"

	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// CallBatchChild validates and dispatches one operation. The enclosing batch
// owns the audit action, background digest acknowledgement, and output redaction.
func (e *Engine) CallBatchChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if scope.Identity.Restricted() {
		return wire.Result{}, wire.Errorf("access_denied", "Batch operations are unavailable to this registration.")
	}
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
	scope.Agent = wire.String(validated, "agent", scope.Agent)
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
