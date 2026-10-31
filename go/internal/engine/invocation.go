package engine

import (
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// Mutating uses the full-permission source classification. The engine adds the
// retained connection-test restriction for optional batch persistence below.
func Mutating(name string, args map[string]any) bool {
	switch name {
	case "write", "edit", "apply_patch", "import_file", "bash", "start_jobs", "stop_jobs", "commit_changes", "create_project", "create_workspace", "release_workspace", "remove_workspace", "codexpro_self_test":
		return true
	case "batch":
		return batchInvocationMutating(args, false)
	}
	return false
}

func (e *Engine) invocationMutating(name string, args map[string]any) bool {
	if name == "batch" {
		return batchInvocationMutating(args, e.Config != nil && e.Config.Bool("connectionTest", false))
	}
	return Mutating(name, args)
}

// Source: tools/context.ts batchInvocationMutating and registry.ts derived
// BATCH_MUTATING_CHILD_TOOLS. A forbidden lifecycle child does not become an
// allowed batch mutation merely because its standalone tool can mutate.
func batchInvocationMutating(args map[string]any, connectionTest bool) bool {
	if value, ok := args["path"].(string); ok && strings.TrimFunc(value, evidenceTrimSpace) != "" {
		return true
	}
	for _, operation := range wire.Objects(args, "operations") {
		switch strings.TrimFunc(wire.String(operation, "tool", ""), evidenceTrimSpace) {
		case "write", "edit", "apply_patch", "bash":
			return true
		}
	}
	if checkpoint := args["checkpoint"]; checkpoint != nil {
		switch value := checkpoint.(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if value != "" {
				return true
			}
		case float64:
			if value != 0 {
				return true
			}
		case int:
			if value != 0 {
				return true
			}
		default:
			return true // Any valid checkpoint object, including {}, is truthy.
		}
	}
	return wire.Bool(args, "persist", false) && !connectionTest
}
