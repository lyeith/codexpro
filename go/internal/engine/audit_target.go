package engine

import (
	"context"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Source: tools/context.ts resolveAuditTarget. Ordinary workspace operations
// carry identity evidence but do not emit an explicit audit_scope field.
func explicitAuditTarget(name string) bool {
	switch name {
	case "create_project", "create_workspace", "release_workspace", "remove_workspace",
		"server_config", "list_projects", "list_workspaces":
		return true
	}
	return false
}

type historicalAuditTarget interface {
	AuditTarget(context.Context, wire.Scope, map[string]any, wire.Result) *audit.Target
}

func (e *Engine) target(ctx context.Context, scope wire.Scope, name string, args map[string]any, result wire.Result) (*wire.Workspace, *audit.Target) {
	// Request cancellation must not erase an otherwise verified audit identity.
	// Observation remains bounded and cannot authorize or execute a new mutation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if name == "work_status" {
		if history, ok := e.WorkHistory.(historicalAuditTarget); ok {
			if target := history.AuditTarget(ctx, scope, args, result); target != nil {
				return nil, target
			}
		}
		// An unconfigured or inaccessible historical run must not borrow source
		// workspace identity or expose an unverified run ID.
		reference := result.StructuredContent["run_id"]
		if wire.String(args, "action", "list") != "list" {
			if requested := args["run_id"]; requested != nil {
				reference = requested
			}
		}
		if _, selected := reference.(string); selected {
			return nil, &audit.Target{Scope: "unattributed"}
		}
		if value, exists := args["project_id"]; exists {
			if project, ok := value.(string); ok {
				if _, exists = e.Workspaces.Project(project); exists {
					return nil, &audit.Target{Scope: "project", ProjectID: project}
				}
			}
			return nil, &audit.Target{Scope: "unattributed"}
		}
		return nil, &audit.Target{Scope: "server"}
	}
	var ws *wire.Workspace
	{
		type candidate struct {
			value   any
			present bool
		}
		first, firstPresent := args["workspace_id"]
		second, secondPresent := result.StructuredContent["workspace_id"]
		third, thirdPresent := result.StructuredContent["selected_workspace_id"]
		candidates := []candidate{{first, firstPresent}, {second, secondPresent}, {third, thirdPresent}}
		provided := false
		for _, item := range candidates {
			provided = provided || item.present
			id, ok := item.value.(string)
			if !ok || strings.TrimSpace(id) == "" {
				continue
			}
			if value, err := e.Workspaces.Resolve(ctx, scope, id); err == nil {
				ws = &value
				break
			}
		}
		if ws == nil && !provided && !explicitAuditTarget(name) && e.Workspaces.Mode() == "direct" {
			if value, err := e.Workspaces.Resolve(ctx, scope, ""); err == nil {
				ws = &value
			}
		}
	}
	if !explicitAuditTarget(name) {
		return ws, nil
	}
	if ws != nil {
		return ws, &audit.Target{Scope: "workspace", ProjectID: ws.ProjectID, WorkspaceID: ws.ID}
	}
	project := result.StructuredContent["project_id"]
	if project == nil {
		project = args["project_id"]
	}
	if id, ok := project.(string); ok {
		if _, exists := e.Workspaces.Project(id); exists {
			return nil, &audit.Target{Scope: "project", ProjectID: id}
		}
	}
	return nil, &audit.Target{Scope: "server"}
}

func (e *Engine) admissionTarget(ctx context.Context, scope wire.Scope, name string, args map[string]any) (*wire.Workspace, *audit.Target) {
	// The source deliberately skips workspace lookup only at admission of open.
	// Its completed/error attribution uses the ordinary resolver afterward.
	if name == "open_workspace" || name == "open_current_workspace" {
		return nil, nil
	}
	return e.target(ctx, scope, name, args, wire.Result{})
}

func (e *Engine) admissionIdentity(name string, args map[string]any, ws *wire.Workspace) map[string]any {
	before := identity(ws)
	if name != "open_workspace" && name != "open_current_workspace" {
		return before
	}
	requested, exists := args["project_id"]
	if !exists || requested == nil {
		switch projects := args["project_ids"].(type) {
		case []any:
			if len(projects) > 0 {
				requested = projects[0]
			}
		case []string:
			if len(projects) > 0 {
				requested = projects[0]
			}
		default:
			requested = e.Workspaces.DefaultProject()
		}
	}
	if id, ok := requested.(string); ok {
		if project, exists := e.Workspaces.Project(id); exists {
			before["project_id"] = project.ID
		}
	}
	return before
}
