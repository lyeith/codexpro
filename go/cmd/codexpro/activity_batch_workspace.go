package main

import "github.com/lyeith/codexpro/go/internal/wire"

// Batch viewing reads the selected project root and uses the workspace ID for
// display attribution.
func activityBatchWorkspace(project wire.Project, id string) wire.Workspace {
	if id == "" {
		id = "activity_" + project.ID
	}
	return wire.Workspace{ID: id, ProjectID: project.ID, Root: project.Root}
}
