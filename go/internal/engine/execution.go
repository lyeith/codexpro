package engine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Inspection keeps the existing typed tool contracts and authorization. Audit
// mutation hints are not an execution policy: stop_jobs controls accepted work,
// and several inspection tools run bounded read helpers.
func executionInspection(name string) bool {
	if wire.ReadToolAllowed(name) {
		return true
	}
	switch name {
	case "open_workspace", "open_current_workspace", "list_workspaces",
		"load_skill", "ast_grep", "inspect_workspace", "show_changes",
		"server_config", "work_status", "activity_status", "activity_export",
		"jobs", "stop_jobs":
		return true
	}
	return false
}

func (e *Engine) acquireExecution(ctx context.Context, scope wire.Scope, name string, args map[string]any) (context.Context, func(), error) {
	if e.Jobs == nil {
		return ctx, nil, wire.Errorf("execution_unavailable", "Execution admission is not configured.")
	}
	return e.Jobs.AcquireExecution(ctx, scope, name, wire.String(args, "workspace_id", ""))
}

// RetainBatchChild reserves an inherited admission synchronously, before a
// deadline-bound child goroutine can be delayed past its enclosing call.
func (e *Engine) RetainBatchChild(ctx context.Context) (context.Context, func(), error) {
	if e.Jobs == nil {
		return ctx, nil, wire.Errorf("execution_unavailable", "Execution admission is not configured.")
	}
	return e.Jobs.RetainExecution(ctx)
}

// undispatchedResult records the verified caller and the actual refusal or
// existing receipt without resolving a target or invoking Git capture helpers.
func (e *Engine) undispatchedResult(ctx context.Context, scope wire.Scope, name, title string, args map[string]any, started time.Time, mutating bool, result wire.Result, callErr error) wire.Result {
	input := audit.RecordInput{
		ToolName: name, InvocationSurface: "direct", Args: args, Scope: scope,
		StartedAt: started, FinishedAt: time.Now(), Mutating: mutating,
		Target: &audit.Target{Scope: "unattributed"}, Before: identity(nil), After: identity(nil),
		Error: callErr, Result: result, DashboardMetadata: result.DashboardMetadata,
	}
	if e.Audit != nil {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if _, err := e.Audit.Record(auditCtx, input); err != nil && e.auditFailureLog.Allow() {
			fmt.Fprintln(os.Stderr, "CodexPro Go audit append failed:", wire.Redact(err.Error()))
		}
		cancel()
	}
	if callErr != nil {
		result = wire.ErrorResult(callErr)
	}
	return e.presentResult(scope, name, title, args, result, callErr)
}

func (e *Engine) presentResult(scope wire.Scope, name, title string, args map[string]any, result wire.Result, callErr error) wire.Result {
	if result.StructuredContent == nil {
		result.StructuredContent = map[string]any{}
	}
	if _, ok := result.StructuredContent["codexpro_tool"]; !ok {
		result.StructuredContent["codexpro_tool"] = name
	}
	if _, ok := result.StructuredContent["codexpro_title"]; !ok {
		result.StructuredContent["codexpro_title"] = title
	}
	if callErr == nil && e.Jobs != nil {
		result = e.Jobs.AttachStatus(scope.Identity, name, args, result)
	}
	return e.Redactor.Result(result)
}
