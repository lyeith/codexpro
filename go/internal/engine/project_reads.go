package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func readRefused() wire.Result {
	return wire.ErrorResult(wire.Errorf("access_denied", "This tool, project or resource is unavailable to this registration."))
}

// Validate the read surface before capturing evidence or dispatching a call.
// Unknown inputs cannot select another execution mode.
func readArguments(name string, args map[string]any) bool {
	for key := range args {
		if !wire.ReadArgumentAllowed(name, key) {
			return false
		}
	}
	if name == "search" && (wire.String(args, "kind", "text") != "text" || wire.String(args, "scope", "workspace") != "workspace") {
		return false
	}
	p := wire.String(args, "path", "")
	return !filepath.IsAbs(p) && !strings.HasPrefix(p, "~") && !slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

func (e *Engine) projectRead(ctx context.Context, scope wire.Scope, name string, raw map[string]any) wire.Result {
	if !wire.ReadToolAllowed(name) || !readArguments(name, raw) {
		return readRefused()
	}
	args, err := e.Registry.Validate(name, raw)
	if err != nil {
		return readRefused()
	}
	scope.Agent = "" // Personal reads use registered identity for attribution.
	switch name {
	case "list_projects":
		projects := e.Workspaces.ReadProjects(ctx)
		list := make([]map[string]any, 0, len(projects))
		for _, p := range projects {
			list = append(list, map[string]any{"id": p.ID, "label": p.Label, "workspace_ids": []string{p.WorkspaceID}, "workspace_id": p.WorkspaceID})
		}
		return wire.Success(name, "Granted projects", map[string]any{"projects": list, "count": len(list)}, fmt.Sprintf("%d granted project(s). Use an explicit workspace ID for reads.", len(list)))
	case "activity_list", "activity_get":
		return e.projectActivity(ctx, scope, name, args)
	}

	w, err := e.Workspaces.ResolveRead(ctx, scope, wire.String(args, "workspace_id", ""))
	if err != nil {
		return readRefused()
	}
	start := time.Now()
	result, err := e.dispatch(ctx, scope, name, args)
	// Use the verified pair for the receipt. Never infer authority from request
	// arguments or attach the general operator job and activity summaries.
	input := audit.RecordInput{ToolName: name, InvocationSurface: "direct", Args: args, Scope: scope,
		StartedAt: start, FinishedAt: time.Now(), Error: err, Result: result,
		Target: &audit.Target{Scope: "workspace", ProjectID: w.ProjectID, WorkspaceID: w.ID}}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, recordErr := e.Audit.Record(logCtx, input); recordErr != nil {
		return readRefused()
	}
	if err != nil || result.IsError {
		return readRefused()
	}
	return e.Redactor.resultWithLabels(result, []pathLabel{{w.Root, "[workspace:" + w.ID + "]"}})
}

func projectWorkspaces(projects []workspace.ReadProject, id string) (map[string]bool, bool) {
	for _, p := range projects {
		if p.ID == id {
			return map[string]bool{p.WorkspaceID: true}, true
		}
	}
	return nil, false
}

func (e *Engine) projectActivity(ctx context.Context, scope wire.Scope, name string, args map[string]any) wire.Result {
	projects := e.Workspaces.ReadProjects(ctx)
	if name == "activity_get" {
		action, found, err := e.Audit.Get(ctx, wire.String(args, "action_id", ""))
		if err != nil || !found {
			return readRefused()
		}
		project, _ := action["project_id"].(string)
		ids, ok := projectWorkspaces(projects, project)
		if !ok || !audit.ProjectReceiptVisible(action, ids) {
			return readRefused()
		}
		return wire.Success(name, "Project activity", map[string]any{"action": audit.ProjectReceipt(action)}, "One authorized project activity receipt.")
	}

	project := wire.String(args, "project_id", "")
	ids, ok := projectWorkspaces(projects, project)
	if !ok {
		return readRefused()
	}
	ws := wire.String(args, "workspace_id", "")
	if ws != "" && !ids[ws] {
		return readRefused()
	}
	page, err := e.Audit.ProjectReceipts(ctx, project, ws, ids, wire.Int(args, "limit", 100))
	if err != nil {
		return readRefused()
	}
	return wire.Success(name, "Project activity", map[string]any{
		"project_id": project, "actions": page.Actions, "has_more": page.HasMore, "truncated": page.HasMore == nil,
	}, fmt.Sprintf("%d project activity receipt(s).", len(page.Actions)))
}
