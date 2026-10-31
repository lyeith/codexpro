// Package engine connects domain services through a single audited dispatcher.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/files"
	gitops "github.com/lyeith/codexpro/go/internal/git"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/search"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

type Engine struct {
	Config                    *config.Config
	Registry                  *contracts.Registry
	Workspaces                *workspace.Manager
	WorkspaceTools            *workspace.Service
	Files                     *files.Service
	Search                    *search.Service
	Git                       *gitops.Service
	Jobs                      *jobs.Service
	Audit                     *audit.Journal
	Redactor                  Redactor
	Admin, Batch, WorkHistory wire.Caller
	maintenanceCancel         context.CancelFunc
	maintenanceDone           <-chan struct{}
}

func New(cfg *config.Config) (e *Engine, err error) {
	e = &Engine{Config: cfg}
	defer func() {
		if err != nil {
			_ = e.Close()
		}
	}()
	if e.Workspaces, err = workspace.New(cfg); err != nil {
		return e, err
	}
	if e.Registry, err = contracts.NewConfigured(contracts.ConfiguredOptions{WorktreeMode: e.Workspaces.Mode(), ToolMode: cfg.String("toolMode", "standard"), ProjectCount: len(e.Workspaces.Projects()), AnalysisEnabled: cfg.Bool("analysisEnabled", true), AuditMode: cfg.String("auditMode", "metadata"), ConnectionTest: cfg.Bool("connectionTest", false)}); err != nil {
		return e, err
	}
	e.Redactor = Redactor{cfg, e.Workspaces}
	if e.Files, err = files.New(cfg, e.Workspaces); err != nil {
		return e, err
	}
	e.Files.TransformText = func(scope wire.Scope, ws wire.Workspace, text string) string {
		return e.Redactor.Text(text, e.Redactor.Labels(map[string]any{"root": ws.Root, "workspace_id": ws.ID}))
	}
	if e.Search, err = search.New(cfg, e.Workspaces, e.Files); err != nil {
		return e, err
	}
	if e.Git, err = gitops.New(cfg, e.Workspaces, e.Files, e.Search); err != nil {
		return e, err
	}
	e.WorkspaceTools = workspace.NewService(e.Workspaces, e.Files)
	if e.Audit, err = audit.Open(audit.Options{Directory: filepath.Join(cfg.Home, "audit"), Enabled: cfg.Bool("auditEnabled", true), MaxBytes: cfg.Int64("auditMaxBytes", 8388608), RetainActions: cfg.Int("auditRetainActions", 350), MaxOutputBytes: cfg.Int("maxOutputBytes", 120000), Redact: wire.Redact}); err != nil {
		return e, err
	}
	e.Workspaces.LastChange = func(id string) any {
		change, err := e.Audit.LastRecordedProjectChange(context.Background(), id)
		if err != nil || change == nil {
			return nil
		}
		return change
	}
	if e.Jobs, err = jobs.New(cfg, e.Workspaces); err != nil {
		return e, err
	}
	e.Workspaces.HasActiveJobs = func(id string) bool {
		for _, job := range e.Jobs.Snapshot() {
			if job.WorkspaceID == id && !job.Quiescent {
				return true
			}
		}
		return false
	}
	e.Jobs.SetCompletionSink(e.recordCompletion)
	maintenanceCtx, cancelMaintenance := context.WithCancel(context.Background())
	e.maintenanceCancel = cancelMaintenance
	done := make(chan struct{})
	e.maintenanceDone = done
	go func() { defer close(done); e.Workspaces.RunMaintenance(maintenanceCtx) }()
	return e, nil
}
func (e *Engine) Close() error {
	var out error
	if e.maintenanceCancel != nil {
		e.maintenanceCancel()
		<-e.maintenanceDone
	}
	if e.Jobs != nil {
		out = errors.Join(out, e.Jobs.Close())
	}
	if e.Audit != nil {
		out = errors.Join(out, e.Audit.Close())
	}
	if e.Workspaces != nil {
		out = errors.Join(out, e.Workspaces.Close())
	}
	return out
}
func (e *Engine) DropSession(scope wire.Scope) {
	e.Workspaces.DropSession(scope)
	e.Git.ForgetSession(scope.PrincipalID, scope.SessionID)
}
func (e *Engine) PrincipalID() (string, error) {
	connectorID := ""
	if e.Workspaces.Mode() == "mcp" {
		path := filepath.Join(e.Config.Home, "worktrees", "connector-id")
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return "", err
			}
			raw := make([]byte, 24)
			if _, err = rand.Read(raw); err != nil {
				return "", err
			}
			b = []byte(hex.EncodeToString(raw))
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return "", err
			}
			_, err = file.Write(b)
			if err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err != nil {
				return "", err
			}
			if closeErr != nil {
				return "", closeErr
			}
		} else if err != nil {
			return "", err
		}
		connectorID = strings.TrimSpace(string(b))
	} else {
		project, ok := e.Workspaces.Project(e.Workspaces.DefaultProject())
		if !ok {
			return "", fmt.Errorf("default project is absent")
		}
		connectorID = workspace.Hash(project.Root)[:48]
	}
	return "connector_" + workspace.Hash(connectorID)[:32], nil
}
func isActivity(name string) bool {
	return name == "activity_list" || name == "activity_get" || name == "activity_status" || name == "activity_export"
}
func isGlobal(name string) bool {
	switch name {
	case "list_projects", "create_project", "server_config", "list_workspaces", "open_current_workspace", "open_workspace", "create_workspace", "release_workspace", "remove_workspace", "work_status":
		return true
	}
	return isActivity(name)
}
func identity(ws *wire.Workspace) map[string]any {
	out := map[string]any{"targets": []string{}, "paths": []any{}}
	if ws != nil {
		out["workspace_id"] = ws.ID
		if ws.ProjectID != "" {
			out["project_id"] = ws.ProjectID
		}
	}
	return out
}
func (e *Engine) Call(ctx context.Context, scope wire.Scope, name string, raw map[string]any) (result wire.Result, returnedError error) {
	started := time.Now()
	if scope.StartedAt.IsZero() {
		scope.StartedAt = started
	}
	args, validationErr := e.Registry.Validate(name, raw)
	if ve, ok := validationErr.(*contracts.ValidationError); ok && ve.SDK {
		return ve.SDKResult(), nil
	}
	if args == nil {
		args = raw
	}
	descriptor, known := e.Registry.Tool(name)
	if !known {
		return (&contracts.ValidationError{Tool: name, Unknown: true, SDK: true}).SDKResult(), nil
	}
	if ve, ok := validationErr.(*contracts.ValidationError); ok {
		validationErr = ve.BusinessError()
	}
	mutating := e.invocationMutating(name, args)
	skip := isActivity(name)
	ws, target := e.admissionTarget(ctx, scope, name, args)
	input := audit.RecordInput{ToolName: name, InvocationSurface: "direct", Args: args, Scope: scope, StartedAt: started, Mutating: mutating, Target: target, Before: e.admissionIdentity(name, args, ws)}
	actionID := ""
	if !skip {
		actionID = e.Audit.Begin(input)
		input.ActionID = actionID
	}
	childScope := scope
	childScope.ParentActionID = actionID
	var callErr error
	invoke := func() (wire.Result, error) {
		e.Audit.MarkRunning(actionID)
		if mutating && !skip && e.Audit.Enabled() {
			input.Before = e.capture(ctx, name, args, ws, nil)
		}
		if validationErr != nil {
			return wire.Result{}, validationErr
		}
		return e.dispatch(ctx, childScope, name, args)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			callErr = fmt.Errorf("internal tool failure: %v", recovered)
			result = wire.Result{}
		}
		afterWS, afterTarget := e.target(context.WithoutCancel(ctx), scope, name, args, result)
		input.Target = afterTarget
		input.After = identity(afterWS)
		if mutating && !skip && e.Audit.Enabled() {
			captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			input.After = e.capture(captureCtx, name, args, afterWS, &result)
			cancel()
		}
		if callErr == nil && !result.IsError && (name == "open_workspace" || name == "open_current_workspace" || name == "create_workspace") {
			projectID := wire.String(result.StructuredContent, "project_id", "")
			if projectID != "" {
				brief, err := e.Audit.RecentActivity(context.WithoutCancel(ctx), projectID, wire.String(result.StructuredContent, "workspace_id", ""))
				if err == nil {
					result.StructuredContent["recent_activity"] = brief
					change, _ := brief["last_recorded_project_change"].(map[string]any)
					at := wire.String(change, "at", "unavailable in retained activity")
					for _, item := range result.Content {
						if item["type"] == "text" {
							item["text"] = fmt.Sprint(item["text"]) + "\n\nLast recorded project change: " + at + ". " + wire.String(brief, "coverage", "")
							break
						}
					}
				}
			}
		}
		input.FinishedAt = time.Now()
		input.Error = callErr
		input.Result = result
		input.DashboardMetadata = result.DashboardMetadata
		if !skip {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_, err := e.Audit.Record(auditCtx, input)
			cancel()
			if err != nil {
				_, _ = fmt.Fprintln(os.Stderr, "CodexPro Go audit append failed:", wire.Redact(err.Error()))
			}
		}
		e.Audit.End(actionID)
		if callErr != nil {
			result = wire.ErrorResult(callErr)
		}
		if result.StructuredContent == nil {
			result.StructuredContent = map[string]any{}
		}
		if _, ok := result.StructuredContent["codexpro_tool"]; !ok {
			result.StructuredContent["codexpro_tool"] = name
		}
		if _, ok := result.StructuredContent["codexpro_title"]; !ok {
			result.StructuredContent["codexpro_title"] = descriptor.Title
		}
		if callErr == nil {
			if attacher, ok := any(e.Jobs).(interface {
				AttachStatus(string, map[string]any, wire.Result) wire.Result
			}); ok {
				result = attacher.AttachStatus(name, args, result)
			}
		}
		result = e.Redactor.Result(result)
		returnedError = nil
	}()
	if !isGlobal(name) {
		result, callErr = e.Workspaces.Execute(ctx, scope, wire.String(args, "workspace_id", ""), mutating, invoke)
	} else {
		result, callErr = invoke()
	}
	return result, nil
}
func (e *Engine) dispatch(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	switch name {
	case "list_projects", "create_project", "open_workspace", "open_current_workspace", "create_workspace", "release_workspace", "list_workspaces":
		return e.WorkspaceTools.Call(ctx, scope, name, args)
	case "remove_workspace":
		return e.WorkspaceTools.Call(ctx, scope, name, args)
	case "read", "write", "edit", "apply_patch", "tree", "view_image", "import_file", "load_skill":
		result, err := e.Files.Call(ctx, scope, name, args)
		if err == nil && !result.IsError && (name == "write" || name == "edit" || name == "apply_patch" || name == "import_file") {
			e.Search.Invalidate(wire.String(args, "workspace_id", ""))
		}
		return result, err
	case "search", "ast_grep", "inspect_workspace":
		return e.Search.Call(ctx, scope, name, args)
	case "show_changes", "commit_changes":
		return e.Git.Call(ctx, scope, name, args)
	case "bash", "start_jobs", "jobs", "stop_jobs":
		return e.Jobs.Call(ctx, scope, name, args)
	case "activity_list", "activity_get", "activity_status", "activity_export":
		return e.Audit.Call(ctx, scope, name, args)
	case "server_config", "codexpro_self_test":
		if e.Admin != nil {
			return e.Admin.Call(ctx, scope, name, args)
		}
	case "batch":
		if e.Batch != nil {
			return e.Batch.Call(ctx, scope, name, args)
		}
	case "work_status":
		if e.WorkHistory != nil {
			return e.WorkHistory.Call(ctx, scope, name, args)
		}
	}
	return wire.Result{}, wire.Errorf("", "Tool service is not configured: %s", name)
}
func (e *Engine) recordCompletion(job jobs.Record) error {
	finishedMS := job.FinishedAtMS
	if finishedMS == 0 {
		finishedMS = time.Now().UnixMilli()
	}
	var exitCode, signal any
	if job.ExitCode != nil {
		exitCode = *job.ExitCode
	}
	if job.Signal != "" {
		signal = job.Signal
	}
	args := map[string]any{
		"workspace_id": job.WorkspaceID, "command": job.Command, "cwd": job.Cwd,
		"job_id": job.ID, "origin": job.Origin,
	}
	data := map[string]any{
		"workspace_id": job.WorkspaceID, "job_id": job.ID, "job_status": job.Status,
		"origin": job.Origin, "exit_code": exitCode, "signal": signal,
		"duration_ms": finishedMS - job.StartedAtMS, "timed_out": job.Status == "timed_out",
		"stdout_bytes": completionFileSize(job.StdoutPath),
		"stderr_bytes": completionFileSize(job.StderrPath),
	}
	if job.ProjectID != "" {
		args["project_id"] = job.ProjectID
		data["project_id"] = job.ProjectID
	}
	if job.StopReason != "" {
		data["stop_reason"] = job.StopReason
	}
	result := wire.Result{IsError: job.Status != "succeeded", StructuredContent: data}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receipt, err := e.Audit.Record(ctx, audit.RecordInput{
		ToolName: "bash_job", Args: args, Result: result, Mutating: true,
		StartedAt: time.UnixMilli(job.StartedAtMS), FinishedAt: time.UnixMilli(finishedMS),
		CompletionDedupKey: jobs.CompletionDedupKey(job),
	})
	if err != nil {
		return err
	}
	if !receipt.Enabled || receipt.Recorded || receipt.Duplicate {
		return nil
	}
	return fmt.Errorf("job completion audit was not accepted")
}

func completionFileSize(path string) int64 {
	if stat, err := os.Stat(path); err == nil {
		return stat.Size()
	}
	return 0
}
