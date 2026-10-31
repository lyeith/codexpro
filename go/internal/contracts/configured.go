package contracts

import (
	"encoding/json"
	"fmt"
)

// ConfiguredOptions contains the startup capabilities that affect registration.
// Callers pass AnalysisEnabled explicitly from their effective configuration;
// NewVariant remains the legacy constructor with analysis and audit enabled.
// Empty AuditMode means metadata. Permission modes are intentionally absent:
// every configured inventory retains full Bash/workspace-write permissions.
// The persistent project catalog and historical work_status reader are fixed.
type ConfiguredOptions struct {
	WorktreeMode    string `json:"worktree_mode"`
	ToolMode        string `json:"tool_mode"`
	ProjectCount    int    `json:"project_count"`
	AnalysisEnabled bool   `json:"analysis_enabled"`
	AuditMode       string `json:"audit_mode"`
	ConnectionTest  bool   `json:"connection_test"`
}

// NewConfigured applies the exact reference capability gates to a frozen
// full-permission variant. It creates a fresh registry so tools/list, dispatch
// lookup and batch-child validation all see the same filtered inventory.
// Descriptors, registration order and initialization are never rewritten.
func NewConfigured(options ConfiguredOptions) (*Registry, error) {
	base, err := NewVariant(options.WorktreeMode, options.ToolMode, options.ProjectCount)
	if err != nil {
		return nil, err
	}
	if options.AuditMode == "" {
		options.AuditMode = "metadata"
	}
	if options.AuditMode != "metadata" && options.AuditMode != "off" {
		return nil, fmt.Errorf("unknown audit mode: %s", options.AuditMode)
	}
	raw := make([]json.RawMessage, 0, len(base.Tools))
	for _, tool := range base.Tools {
		if configuredAvailable(tool.Name, options) {
			raw = append(raw, tool.Raw)
		}
	}
	return newRegistry(raw)
}

// Source: src/tools/registry.ts TOOL_DESCRIPTORS/REQUIREMENTS/isToolAvailable at
// 44bb76b. Only supported variable capabilities are applied here; NewVariant
// handles inventory tiers/workspace selectors and every approved retirement.
func configuredAvailable(name string, options ConfiguredOptions) bool {
	if !options.AnalysisEnabled && name == "inspect_workspace" {
		return false
	}
	if options.AuditMode != "metadata" {
		switch name {
		case "activity_list", "activity_get", "activity_status", "activity_export":
			return false
		}
	}
	if options.ConnectionTest {
		switch name {
		case "create_project", "codexpro_self_test", "write", "edit", "apply_patch",
			"import_file", "start_jobs", "stop_jobs", "bash", "commit_changes":
			return false
		case "list_projects":
			// With projectCreation disabled, requiresAny is satisfied only by
			// managed worktrees or a multi-project catalog.
			return options.WorktreeMode == "mcp" || options.ProjectCount > 1
		}
	}
	return true
}
