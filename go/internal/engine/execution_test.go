package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/batch"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func executionFixture(t *testing.T) (*Engine, wire.Scope, string, string) {
	t.Helper()
	e, scope, id, root := batchWrapperFixture(t)
	e.Config.Set("jobTimeoutMs", 5000)
	e.Config.Set("bashTimeoutMs", 4000)
	e.Config.Set("maxBashTimeoutMs", 5000)
	service, err := admin.New(e.Config, e.Workspaces, e.Files, e.Git, e.Registry, e.Audit)
	if err != nil {
		t.Fatal(err)
	}
	e.Admin = service
	return e, scope, id, root
}

func executionResult(t *testing.T, e *Engine, ctx context.Context, scope wire.Scope, name string, args map[string]any) wire.Result {
	t.Helper()
	result, err := e.Call(ctx, scope, name, args)
	if err != nil {
		t.Fatalf("%s returned a transport error: %v", name, err)
	}
	return result
}

func requireExecutionHeld(t *testing.T, result wire.Result) {
	t.Helper()
	if !result.IsError || result.StructuredContent["error_code"] != "execution_held" {
		t.Fatalf("expected explicit execution hold: %#v", result)
	}
}

func executionHelperWitness(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	witness := filepath.Join(dir, "helper-called")
	quoted := "'" + strings.ReplaceAll(witness, "'", "'\\''") + "'"
	script := fmt.Sprintf("#!/bin/sh\nprintf 'unexpected helper\\n' >> %s\nexit 99\n", quoted)
	for _, name := range []string{"git", "nice", "bash"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return witness
}

func requireExecutionMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected effect at %s: %v", path, err)
	}
}

func TestExecutionHoldRefusesFreshToolsBeforeCapture(t *testing.T) {
	e, scope, id, root := executionFixture(t)
	witness := executionHelperWitness(t)
	hold, err := e.Jobs.BeginExecutionHold("engine-no-effects")
	if err != nil || !hold.AdmissionHeld {
		t.Fatalf("begin hold: %+v %v", hold, err)
	}
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"bash", "bash", map[string]any{"workspace_id": id, "command": "printf forbidden > payload"}},
		{"background", "start_jobs", map[string]any{"workspace_id": id, "commands": []any{map[string]any{"command": "printf forbidden > payload"}}}},
		{"queued", "queue_jobs", map[string]any{"workspace_id": id, "queue_key": "never-accepted", "commands": []any{map[string]any{"command": "printf forbidden > payload"}}}},
		{"write", "write", map[string]any{"workspace_id": id, "path": "payload", "content": "forbidden"}},
		{"batch", "batch", map[string]any{"workspace_id": id, "operations": []any{map[string]any{"tool": "write", "args": map[string]any{"path": "payload", "content": "forbidden"}}}}},
		{"stored-resume", "batch", map[string]any{"workspace_id": id, "path": "does-not-exist.json", "from": "verify"}},
		{"self-test", "codexpro_self_test", map[string]any{"workspace_id": id, "write_probe": true, "bash_probe": true}},
		{"commit", "commit_changes", map[string]any{"workspace_id": id, "message": "must not run"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			// This is the same established principal/session scope throughout.
			scope.RequestID = "held-" + item.name
			result := executionResult(t, e, context.Background(), scope, item.tool, item.args)
			requireExecutionHeld(t, result)
			if result.StructuredContent["codexpro_tool"] != item.tool {
				t.Fatal("hold result lost the ordinary tool presentation")
			}
		})
	}
	requireExecutionMissing(t, witness)
	requireExecutionMissing(t, filepath.Join(root, "payload"))
	requireExecutionMissing(t, filepath.Join(root, batch.StoreDir))
	requireExecutionMissing(t, filepath.Join(root, e.Config.String("contextDir", ".ai-bridge"), "codexpro-self-test.md"))
	if records := e.Jobs.Snapshot(); len(records) != 0 {
		t.Fatalf("held requests created jobs: %+v", records)
	}
	actions, err := e.Audit.List(context.Background(), audit.Filter{Limit: 20})
	if err != nil || len(actions.Actions) != len(cases) {
		t.Fatalf("refusal receipts were lost: count=%d error=%v", len(actions.Actions), err)
	}
	for _, action := range actions.Actions {
		if action["status"] != "failed" {
			t.Fatalf("held request was recorded as execution success: %#v", action)
		}
	}
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld {
		t.Fatalf("refusals left execution admitted: %+v %v", status, err)
	}
}

func executionIDs(t *testing.T, result wire.Result) []string {
	t.Helper()
	var ids []string
	switch values := result.StructuredContent["job_ids"].(type) {
	case []any:
		for _, value := range values {
			id, ok := value.(string)
			if !ok {
				t.Fatalf("non-string job ID: %#v", value)
			}
			ids = append(ids, id)
		}
	case []string:
		ids = append(ids, values...)
	}
	if len(ids) == 0 {
		t.Fatalf("missing accepted job IDs: %#v", result)
	}
	return ids
}

func TestExecutionHoldKeepsAcceptedQueueReceiptAndInspection(t *testing.T) {
	e, scope, id, root := executionFixture(t)
	scope.Identity = wire.Identity{Source: "named", PrincipalType: "system", PrincipalID: "fleet", CredentialID: "fixture-device", AgentID: "fp5", AgentKind: "leaf"}
	ctx := wire.WithIdentity(context.Background(), scope.Identity)
	cwd := filepath.Join(root, "original-cwd")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"workspace_id": id, "queue_key": "original-receipt",
		"commands": []any{map[string]any{"command": "printf original-output", "cwd": "original-cwd"}},
	}
	first := executionResult(t, e, ctx, scope, "queue_jobs", args)
	if first.IsError {
		t.Fatalf("original submission failed: %#v", first)
	}
	ids := executionIDs(t, first)
	hold, err := e.Jobs.BeginExecutionHold("engine-receipt")
	if err != nil {
		t.Fatal(err)
	}
	collected := executionResult(t, e, ctx, scope, "jobs", map[string]any{"workspace_id": id, "job_ids": ids, "output": "head", "wait_ms": 5000})
	if collected.IsError || collected.StructuredContent["all_succeeded"] != true {
		t.Fatalf("accepted job did not finish through the hold: %#v", collected)
	}
	rows, ok := collected.StructuredContent["jobs"].([]any)
	if !ok || len(rows) != 1 || rows[0].(map[string]any)["stdout"] != "original-output" {
		t.Fatalf("original output was not collectable: %#v", collected)
	}
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	witness := executionHelperWitness(t)
	retry := executionResult(t, e, ctx, scope, "queue_jobs", args)
	if retry.IsError || strings.Join(executionIDs(t, retry), ",") != strings.Join(ids, ",") {
		t.Fatalf("held receipt lookup lost the original IDs after CWD removal: %#v", retry)
	}
	conflict := map[string]any{"workspace_id": id, "queue_key": "original-receipt", "commands": []any{map[string]any{"command": "printf changed", "cwd": "original-cwd"}}}
	result := executionResult(t, e, ctx, scope, "queue_jobs", conflict)
	if !result.IsError || result.StructuredContent["error_code"] != "job_submission_conflict" {
		t.Fatalf("same-key payload conflict was not preserved: %#v", result)
	}
	missing := map[string]any{"workspace_id": id, "queue_key": "new-key", "commands": args["commands"]}
	requireExecutionHeld(t, executionResult(t, e, ctx, scope, "queue_jobs", missing))
	alias := executionResult(t, e, ctx, scope, "start_jobs", args)
	if !alias.IsError || !strings.Contains(fmt.Sprint(alias.Content), "queue_jobs") {
		t.Fatalf("unsupported start_jobs alias acquired submission semantics: %#v", alias)
	}
	requireExecutionMissing(t, witness)
	t.Setenv("PATH", oldPath)

	for _, item := range []struct {
		name string
		args map[string]any
	}{
		{"read", map[string]any{"workspace_id": id, "path": "a.txt"}},
		{"tree", map[string]any{"workspace_id": id, "max_depth": 1}},
		{"list_projects", map[string]any{}},
		{"server_config", map[string]any{}},
		{"activity_status", map[string]any{}},
		{"jobs", map[string]any{"workspace_id": id, "job_ids": ids, "output": "none", "wait_ms": 0}},
		{"stop_jobs", map[string]any{"workspace_id": id, "job_ids": ids}},
	} {
		if result := executionResult(t, e, ctx, scope, item.name, item.args); result.IsError {
			t.Fatalf("inspection/control %s was unavailable: %#v", item.name, result)
		}
	}
	foreign := scope
	foreign.Identity.PrincipalID, foreign.Identity.AgentID = "other-fleet", "other-leaf"
	foreignResult := executionResult(t, e, wire.WithIdentity(context.Background(), foreign.Identity), foreign, "jobs", map[string]any{"workspace_id": id, "job_ids": ids, "output": "none", "wait_ms": 0})
	if !foreignResult.IsError {
		t.Fatal("hold collection weakened job ownership")
	}
	if records := e.Jobs.Snapshot(); len(records) != 1 || records[0].ID != ids[0] || records[0].Command != "printf original-output" {
		t.Fatalf("receipt lookup changed original work: %+v", records)
	}
	// The original command may be published before its launcher has exited.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("original launcher did not settle: %+v %v", status, err)
		}
		if status.DispatchHeld {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecutionHoldPreservesPersonalReadAuthorization(t *testing.T) {
	f := newPersonalReadFixture(t)
	if _, err := f.engine.Jobs.BeginExecutionHold("personal-reads"); err != nil {
		t.Fatal(err)
	}
	ctx := f.ctx("alpha")
	alpha := workspace.IDForRoot(f.alpha)
	f.call(t, ctx, "read", map[string]any{"workspace_id": alpha, "path": "public.txt"}, false)
	f.call(t, ctx, "read", map[string]any{"workspace_id": workspace.IDForRoot(f.beta), "path": "public.txt"}, true)
	beforeDenial, err := f.engine.Audit.List(ctx, audit.Filter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	denied := f.call(t, ctx, "write", map[string]any{"workspace_id": alpha, "path": "blocked.txt", "content": "not authorized"}, true)
	if denied.StructuredContent["error_code"] != "access_denied" ||
		denied.StructuredContent["error"] != "CodexProError: This tool, project or resource is unavailable to this registration." {
		t.Fatalf("personal write did not receive the registration authorization refusal: %#v", denied)
	}
	afterDenial, err := f.engine.Audit.List(ctx, audit.Filter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if afterDenial.LatestSequence != beforeDenial.LatestSequence {
		t.Fatalf("registration refusal reached execution/audit: sequence before=%d after=%d", beforeDenial.LatestSequence, afterDenial.LatestSequence)
	}
	requireExecutionMissing(t, filepath.Join(f.alpha, "blocked.txt"))
}

type executionBatchBarrier struct {
	engine       *Engine
	blockTool    string
	entered      chan context.Context
	proceed      chan struct{}
	releaseDone  chan struct{}
	blockOnce    sync.Once
	releasedOnce sync.Once
}

func (b *executionBatchBarrier) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	return b.engine.Call(ctx, scope, name, args)
}

func (b *executionBatchBarrier) CallBatchChild(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if name == b.blockTool {
		b.blockOnce.Do(func() {
			b.entered <- ctx
			<-b.proceed
		})
	}
	return b.engine.CallBatchChild(ctx, scope, name, args)
}

func (b *executionBatchBarrier) RetainBatchChild(ctx context.Context) (context.Context, func(), error) {
	retained, release, err := b.engine.RetainBatchChild(ctx)
	if err != nil {
		return retained, release, err
	}
	return retained, func() {
		release()
		b.releasedOnce.Do(func() { close(b.releaseDone) })
	}, nil
}

func installExecutionBarrier(t *testing.T, e *Engine, tool string) *executionBatchBarrier {
	t.Helper()
	barrier := &executionBatchBarrier{
		engine: e, blockTool: tool, entered: make(chan context.Context, 1),
		proceed: make(chan struct{}), releaseDone: make(chan struct{}),
	}
	service, err := batch.New(e.Config, e.Workspaces, barrier, func(name string, args map[string]any) (map[string]any, error) {
		parsed, err := e.Registry.ValidateInner(name, args)
		if validation, ok := err.(*contracts.ValidationError); ok {
			err = validation.BusinessError()
		}
		return parsed, err
	})
	if err != nil {
		t.Fatal(err)
	}
	service.SetFiles(e.Files)
	e.Batch = service
	return barrier
}

func waitExecutionSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + label)
	}
}

func cleanupExecutionBatch(t *testing.T, release func(), done <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		release()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("batch fixture did not stop")
		}
	})
}

func TestExecutionHoldAdmittedBatchContinuesButStoredReplayIsFresh(t *testing.T) {
	e, scope, id, root := executionFixture(t)
	barrier := installExecutionBarrier(t, e, "write")
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(barrier.proceed) }) }
	parentDone := make(chan struct{})
	cleanupExecutionBatch(t, release, parentDone)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := make(chan wire.Result, 1)
	failure := make(chan error, 1)
	go func() {
		defer close(parentDone)
		value, err := e.Call(ctx, scope, "batch", map[string]any{
			"workspace_id": id,
			"persist":      true,
			"operations": []any{
				map[string]any{"id": "write", "tool": "write", "args": map[string]any{"path": "once.txt", "content": "original\n"}},
				map[string]any{"id": "read", "tool": "read", "args": map[string]any{"path": "once.txt"}},
			},
		})
		result <- value
		failure <- err
	}()
	select {
	case <-barrier.entered:
	case <-ctx.Done():
		t.Fatal("batch did not reach its admitted child")
	}
	hold, err := e.Jobs.BeginExecutionHold("admitted-batch")
	if err != nil {
		t.Fatal(err)
	}
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || status.DispatchHeld || len(status.PendingCalls) == 0 {
		t.Fatalf("hold ignored the original batch: %+v %v", status, err)
	}
	release()
	var original wire.Result
	select {
	case original = <-result:
	case <-ctx.Done():
		t.Fatal("admitted batch did not finish")
	}
	if err := <-failure; err != nil || original.IsError {
		t.Fatalf("admitted batch lost its continuation: %v %#v", err, original)
	}
	waitExecutionSignal(t, barrier.releaseDone, "read-child release")
	path := wire.String(original.StructuredContent, "batch_path", "")
	if path == "" {
		t.Fatal("original batch was not persisted")
	}
	written := filepath.Join(root, "once.txt")
	stamp := time.Unix(1234567890, 0)
	if err := os.Chtimes(written, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// Reusing the original request/session IDs and stored path is not a lease.
	replay := executionResult(t, e, context.Background(), scope, "batch", map[string]any{"workspace_id": id, "path": path, "from": "write"})
	requireExecutionHeld(t, replay)
	info, err := os.Stat(written)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("held stored replay repeated the write: %v %#v", err, info)
	}
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld {
		t.Fatalf("completed original batch retained admission: %+v %v", status, err)
	}
}

func TestExecutionHoldRetainsChildPastParentDeadlineBeforeDispatch(t *testing.T) {
	e, scope, id, _ := executionFixture(t)
	e.Config.Set("batchReadDeadlineMs", 30)
	barrier := installExecutionBarrier(t, e, "read")
	var unblock sync.Once
	release := func() { unblock.Do(func() { close(barrier.proceed) }) }
	parentDone := make(chan struct{})
	cleanupExecutionBatch(t, release, parentDone)
	done := make(chan wire.Result, 1)
	go func() {
		defer close(parentDone)
		value, err := e.Call(context.Background(), scope, "batch", map[string]any{
			"workspace_id": id, "persist": false,
			"operations": []any{map[string]any{"id": "read", "tool": "read", "args": map[string]any{"path": "a.txt"}}},
		})
		if err != nil {
			value = wire.ErrorResult(err)
		}
		done <- value
	}()
	var childCtx context.Context
	select {
	case childCtx = <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retained child did not reach pre-dispatch barrier")
	}
	hold, err := e.Jobs.BeginExecutionHold("late-child")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if !result.IsError || !strings.Contains(fmt.Sprint(result.StructuredContent), "Timed out:") {
			t.Fatalf("original read deadline changed: %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not return its existing child deadline")
	}
	if childCtx.Err() != context.DeadlineExceeded {
		t.Fatalf("child lost its original deadline: %v", childCtx.Err())
	}
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || status.DispatchHeld || len(status.PendingCalls) == 0 {
		t.Fatalf("parent return hid a child that had not entered engine dispatch: %+v %v", status, err)
	}
	release()
	waitExecutionSignal(t, barrier.releaseDone, "late-child release")
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld {
		t.Fatalf("finished late child retained admission: %+v %v", status, err)
	}
}

func TestExecutionHoldRejectsReleasedAndForeignLeases(t *testing.T) {
	e, scope, id, root := executionFixture(t)
	ctx, releaseParent, err := e.Jobs.AcquireExecution(context.Background(), scope, "batch", id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseParent)
	childCtx, releaseChild, err := e.RetainBatchChild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(releaseChild)
	hold, err := e.Jobs.BeginExecutionHold("lease-membership")
	if err != nil {
		t.Fatal(err)
	}
	releaseParent()
	if _, release, err := e.RetainBatchChild(ctx); err == nil {
		release()
		t.Fatal("released parent regained authority through a live sibling")
	}
	result, err := e.CallBatchChild(childCtx, scope, "write", map[string]any{"workspace_id": id, "path": "allowed.txt", "content": "original child"})
	if err != nil || result.IsError {
		t.Fatalf("live inherited child was refused: %v %#v", err, result)
	}
	other, otherScope, otherID, otherRoot := executionFixture(t)
	if _, err := other.CallBatchChild(childCtx, otherScope, "write", map[string]any{"workspace_id": otherID, "path": "foreign.txt", "content": "forbidden"}); err == nil {
		t.Fatal("another service adopted a foreign lease")
	}
	requireExecutionMissing(t, filepath.Join(otherRoot, "foreign.txt"))
	releaseChild()
	releaseChild()
	if _, err := e.CallBatchChild(childCtx, scope, "write", map[string]any{"workspace_id": id, "path": "released.txt", "content": "forbidden"}); err == nil {
		t.Fatal("released child reacquired admission")
	}
	requireExecutionMissing(t, filepath.Join(root, "released.txt"))
	if status, err := e.Jobs.HoldExecutionDispatch(hold.HoldID); err != nil || !status.DispatchHeld || len(status.PendingCalls) != 0 {
		t.Fatalf("idempotent release corrupted the gate: %+v %v", status, err)
	}
}
