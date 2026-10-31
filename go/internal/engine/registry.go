package engine

// RegisteredNames reports the advertised tools which actually have a configured
// dispatcher. The self-test compares this independently with the schema catalog.
func (e *Engine) RegisteredNames() []string {
	out := []string{}
	for _, name := range e.Registry.Names() {
		ready := false
		switch name {
		case "list_projects", "create_project", "open_workspace", "open_current_workspace", "create_workspace", "release_workspace", "list_workspaces", "remove_workspace":
			ready = e.WorkspaceTools != nil
		case "read", "write", "edit", "apply_patch", "tree", "view_image", "import_file", "load_skill":
			ready = e.Files != nil
		case "search", "ast_grep", "inspect_workspace":
			ready = e.Search != nil
		case "show_changes", "commit_changes":
			ready = e.Git != nil
		case "bash", "start_jobs", "jobs", "stop_jobs":
			ready = e.Jobs != nil
		case "activity_list", "activity_get", "activity_status", "activity_export":
			ready = e.Audit != nil
		case "server_config", "codexpro_self_test":
			ready = e.Admin != nil
		case "batch":
			ready = e.Batch != nil
		case "work_status":
			ready = e.WorkHistory != nil
		}
		if ready {
			out = append(out, name)
		}
	}
	return out
}
