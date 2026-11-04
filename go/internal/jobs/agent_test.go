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

func TestNamedJobOwnershipSurvivesRecovery(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	id := wire.Identity{Source: "named", PrincipalID: "alpha", CredentialID: "lab", AgentID: "crew-a", AgentKind: "crew", Role: "tester"}
	owner := wire.Scope{Identity: id, PrincipalID: "principal:alpha", Agent: "SP-3"}
	other := owner
	other.Identity.CredentialID = "other-device"
	res, err := s.Call(context.Background(), owner, "start_jobs", map[string]any{"workspace_id": resolver.workspace.ID, "commands": []any{map[string]any{"command": "printf completed"}}})
	if err != nil || res.IsError {
		t.Fatal("start failed", err)
	}
	jobID := resultData(t, res)["job_ids"].([]any)[0].(string)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	seen := make(chan Record, 1)
	recovered.SetCompletionSink(func(job Record) error { seen <- job; return nil })
	select {
	case job := <-seen:
		if job.ID != jobID || job.Agent != "SP-3" || job.Identity != id || job.OwnerPrincipalID != owner.PrincipalID {
			t.Fatalf("recovered completion lost owner: %+v", job.Identity)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing recovered completion")
	}
	for _, name := range []string{"jobs", "stop_jobs"} {
		_, err = recovered.Call(context.Background(), other, name, map[string]any{"workspace_id": resolver.workspace.ID, "job_ids": []string{jobID}})
		reason, ok := err.(*wire.Error)
		if !ok || reason.Code != "job_not_found" || len(reason.Data["known_job_ids"].([]string)) != 0 {
			t.Fatalf("crossed %s: %v", name, err)
		}
	}
	bad := recovered.AttachStatus(other.Identity, "list_projects", nil, wire.Result{})
	if bad.StructuredContent["background_jobs"] != nil {
		t.Fatal("foreign receipt returned in digest")
	}
	r, _ := recovered.Get(jobID, "")
	if r.Acknowledged {
		t.Fatal("foreign caller acknowledged job")
	}
	good := recovered.AttachStatus(owner.Identity, "list_projects", nil, wire.Result{})
	if good.StructuredContent["background_jobs"] == nil {
		t.Fatal("own digest missing")
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
