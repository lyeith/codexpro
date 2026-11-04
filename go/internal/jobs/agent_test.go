package jobs

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestJobAgentPersistsAcrossRestartAndLaterReaders(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.Call(ctx, wire.Scope{Agent: "SP-3"}, "bash", map[string]any{"workspace_id": resolver.workspace.ID, "command": "printf labeled"})
	if err != nil || result.IsError {
		t.Fatalf("Bash failed: %v %+v", err, result)
	}
	data := resultData(t, result)
	id := data["job_id"].(string)
	if data["agent"] != "SP-3" {
		t.Fatalf("Bash response lost label: %v", data)
	}
	if record, ok := s.Get(id, ""); !ok || record.Agent != "SP-3" {
		t.Fatalf("job record lost label: %+v", record)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if record, ok := s.Get(id, ""); !ok || record.Agent != "SP-3" {
		t.Fatalf("restart lost label: %+v", record)
	}
	// A later caller's label must not replace the label of the original job.
	result, err = s.Call(ctx, wire.Scope{Agent: "SP-7"}, "jobs", map[string]any{"workspace_id": resolver.workspace.ID, "job_ids": []string{id}, "output": "none", "wait_ms": 0})
	if err != nil {
		t.Fatal(err)
	}
	views := resultData(t, result)["jobs"].([]any)
	if len(views) != 1 || views[0].(map[string]any)["agent"] != "SP-3" {
		t.Fatalf("later reader changed original label: %v", views)
	}
}

func TestUnlabeledJobOmitsAgent(t *testing.T) {
	s, _, _ := newFixture(t)
	data := invoke(t, s, "bash", map[string]any{"command": "printf unlabeled"})
	if _, present := data["agent"]; present {
		t.Fatal("unlabeled job changed Bash response")
	}
	record, ok := s.Get(data["job_id"].(string), "")
	if !ok || record.Agent != "" {
		t.Fatalf("unlabeled job acquired label: %+v", record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err = json.Unmarshal(encoded, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, present := persisted["agent"]; present {
		t.Fatal("unlabeled job changed persisted representation")
	}
}
