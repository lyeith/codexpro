package engine

import (
	"context"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
)

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
