package contracts

import "fmt"

// ConfiguredOptions are startup capabilities that determine which tools are
// advertised and callable.
type ConfiguredOptions struct {
	ToolMode        string
	ProjectCount    int
	AnalysisEnabled bool
	AuditMode       string
	ConnectionTest  bool
}

// NewConfigured uses the same inventory for discovery and direct or batch calls.
func NewConfigured(options ConfiguredOptions) (*Registry, error) {
	switch options.ToolMode {
	case "":
		options.ToolMode = "standard"
	case "minimal", "standard", "full":
	default:
		return nil, fmt.Errorf("unknown tool mode: %s", options.ToolMode)
	}
	if options.ProjectCount < 0 {
		return nil, fmt.Errorf("project count cannot be negative")
	}
	switch options.AuditMode {
	case "":
		options.AuditMode = "metadata"
	case "metadata", "off":
	default:
		return nil, fmt.Errorf("unknown audit mode: %s", options.AuditMode)
	}
	return newRegistry(options)
}

func variantAvailable(name string, options ConfiguredOptions) bool {
	switch name {
	case "load_skill", "inspect_workspace", "tree", "search", "ast_grep", "view_image":
		return options.ToolMode != "minimal"
	case "list_workspaces":
		return options.ToolMode == "full"
	case "open_current_workspace":
		return options.ProjectCount <= 1
	default:
		return true
	}
}

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
			"import_file", "start_jobs", "queue_jobs", "stop_jobs", "bash", "commit_changes":
			return false
		case "list_projects":
			return options.ProjectCount > 1
		}
	}
	return true
}
