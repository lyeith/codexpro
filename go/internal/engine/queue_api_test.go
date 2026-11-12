package engine

import (
	"context"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
	"testing"
)

func TestQueueToolEngineDispatchKeepsNamedSubmissionIdentity(t *testing.T) {
	f := newPersonalReadFixture(t)
	scope := wire.Scope{Agent: "sp5", Identity: wire.Identity{Source: "named", PrincipalType: "system", PrincipalID: "fleet", CredentialID: "fleet-device", AgentID: "sp5", AgentKind: "leaf"}}
	ctx := wire.WithIdentity(context.Background(), scope.Identity)
	args := map[string]any{"workspace_id": workspace.IDForRoot(f.alpha), "commands": []any{map[string]any{"command": ":"}}, "queue_key": "engine-submission"}
	first, err := f.engine.Call(ctx, scope, "queue_jobs", args)
	if err != nil || first.IsError {
		t.Fatalf("queue dispatcher refused ordinary fleet caller: %#v %v", first, err)
	}
	if first.StructuredContent["codexpro_tool"] != "queue_jobs" || !Mutating("queue_jobs", args) {
		t.Fatal("queued dispatch lost tool/effect classification")
	}
	records := f.engine.Jobs.Snapshot()
	if len(records) != 1 || records[0].Identity != scope.Identity {
		t.Fatal("queued dispatch lost authenticated owner")
	}
	second, err := f.engine.Call(ctx, scope, "queue_jobs", args)
	if err != nil || second.IsError || len(f.engine.Jobs.Snapshot()) != 1 {
		t.Fatalf("same key launched a second submission: %#v %v", second, err)
	}
	alias, err := f.engine.Call(ctx, scope, "start_jobs", args)
	if err != nil || !alias.IsError || len(f.engine.Jobs.Snapshot()) != 1 {
		t.Fatal("start_jobs accepted a queue-mode alias or created effects")
	}
	for _, r := range f.engine.Jobs.Snapshot() {
		_, _ = f.engine.Jobs.Stop(r.ID, r.WorkspaceID, "stopped")
	}
}
