package engine

import (
	"context"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestProjectReadKeepsRegisteredIdentityWithoutCallerLabel(t *testing.T) {
	f := newPersonalReadFixture(t)
	ctx := f.ctx("alpha")
	f.scope.Agent = "SUPV"
	ws := workspace.IDForRoot(f.alpha)
	for _, caller := range []string{"SUPV", "SP5"} {
		args := map[string]any{"workspace_id": ws, "path": "public.txt", "agent": caller}
		if r := f.call(t, ctx, "read", args, true); r.StructuredContent["error_code"] != "access_denied" {
			t.Fatal("reader-supplied label did not receive the ordinary access refusal")
		}
	}
	f.call(t, ctx, "read", map[string]any{"workspace_id": ws, "path": "public.txt"}, false)
	reads, err := f.engine.Audit.List(ctx, audit.Filter{ToolName: "read", Limit: 3})
	if err != nil || len(reads.Actions) != 1 {
		t.Fatal("only the admitted read should have an audit receipt")
	}
	action := reads.Actions[0]
	if _, ok := action["agent"]; ok {
		t.Fatal("personal read acquired an operator display label")
	}
	id, ok := action["identity"].(map[string]any)
	if !ok || id["principal_id"] != f.scope.Identity.PrincipalID || id["agent_id"] != f.scope.Identity.AgentID {
		t.Fatal("admitted read lost its registered principal or agent")
	}
}

func TestBatchJobsInheritCallerAgentAndAllowExplicitChildLabel(t *testing.T) {
	e, scope, id, _ := batchWrapperFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := e.Call(ctx, scope, "batch", map[string]any{
		"workspace_id": id, "agent": "SP-3", "persist": false,
		"operations": []any{
			map[string]any{"id": "inherit", "tool": "bash", "args": map[string]any{"command": "printf inherited"}},
			map[string]any{"id": "override", "tool": "bash", "args": map[string]any{"command": "printf explicit", "agent": "SP-4"}},
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("batch failed: %v %+v", err, result)
	}
	records := e.Jobs.Snapshot()
	if len(records) != 2 {
		t.Fatalf("wrong job count: %v", records)
	}
	for _, record := range records {
		want := "SP-3"
		if record.Command == "printf explicit" {
			want = "SP-4"
		}
		if record.Agent != want {
			t.Fatalf("batch child lost label: %+v", record)
		}
	}
	retained, err := e.Audit.List(ctx, audit.Filter{ToolName: "batch", Limit: 1})
	if err != nil || len(retained.Actions) != 1 || retained.Actions[0]["agent"] != "SP-3" {
		t.Fatalf("outer caller label lost: %+v %v", retained, err)
	}
	metadata := retained.Actions[0]["result_metadata"].(map[string]any)
	children := metadata["child_results"].([]any)
	for i, label := range []string{"SP-3", "SP-4"} {
		if children[i].(map[string]any)["agent"] != label {
			t.Fatalf("child job audit evidence lost label: %v", children)
		}
	}
}
