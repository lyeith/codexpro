package jobs

import (
	"context"
	"github.com/lyeith/codexpro/go/internal/wire"
	"testing"
)

// This public-path regression requires a durable queued receipt even when the
// ordinary per-workspace execution capacity is full.
func TestCapacityQueueOptInAcceptsPublicRequestAtFullWorkspace(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobsPerWorkspace", 1)
	held, _, err := s.admit(startOptions{Workspace: resolver.workspace, CwdAbs: resolver.workspace.Root, Cwd: ".", Command: "held-original", Origin: "background", TimeoutMS: 5000, OutputLimit: 8192})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.finalize(held.ID, nil, "lost") }()
	result, err := s.Call(context.Background(), wire.Scope{}, "queue_jobs", map[string]any{"workspace_id": resolver.workspace.ID, "commands": []map[string]any{{"command": "printf waiting-owner", "label": "ready proof"}}, "queue_key": "regression-waiting-owner"})
	if err != nil {
		t.Fatalf("explicitly waiting command was rejected instead of durably queued: %v", err)
	}
	data := resultData(t, result)
	if data["queued_count"] != float64(1) {
		t.Fatalf("missing queued receipt: %#v", data)
	}
	if data["jobs"].([]any)[0].(map[string]any)["label"] != "ready proof" {
		t.Fatal("queued label lost")
	}
	ids := data["job_ids"].([]any)
	for _, id := range ids {
		_, _ = s.Stop(id.(string), resolver.workspace.ID, "stopped")
	}
}
