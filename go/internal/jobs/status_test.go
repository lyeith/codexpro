package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func digestRecord(t *testing.T, s *Service, resolver testResolver, origin, workspace, status string) Record {
	t.Helper()
	o := opts(resolver, "printf digest")
	o.Origin = origin
	o.Workspace.ID = workspace
	r, _, err := s.admit(o)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.update(r.ID, func(r *Record) bool {
		r.CommandLabel = "printf digest"
		r.StartedAtMS = time.Now().Add(-2500 * time.Millisecond).UnixMilli()
		r.Status = status
		if status != "running" {
			r.FinishedAtMS = r.StartedAtMS + 2500
			r.Quiescent = true
			code := 0
			r.ExitCode = &code
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAttachStatusFiltersAcknowledgesAndPreservesPayload(t *testing.T) {
	s, _, resolver := newFixture(t)
	foreground := digestRecord(t, s, resolver, "foreground", resolver.workspace.ID, "succeeded")
	other := digestRecord(t, s, resolver, "background", "other", "succeeded")
	finished := digestRecord(t, s, resolver, "background", resolver.workspace.ID, "succeeded")
	base := wire.Success("read", "Read File", map[string]any{"text": "original"}, "original")
	exempt := s.AttachStatus("jobs", map[string]any{}, base)
	if _, ok := exempt.StructuredContent["background_jobs"]; ok {
		t.Fatal("job tool got recursive digest")
	}
	r := s.AttachStatus("read", map[string]any{"workspace_id": resolver.workspace.ID}, base)
	rows := r.StructuredContent["background_jobs"].([]map[string]any)
	if len(rows) != 1 || rows[0]["job_id"] != finished.ID {
		t.Fatalf("digest scope/origin mismatch: %#v", rows)
	}
	if !strings.Contains(r.Content[0]["text"].(string), "succeeded exit 0 after 2.5 s") {
		t.Fatal("digest text lost status/elapsed")
	}
	if base.Content[0]["text"] != "original" || base.StructuredContent["background_jobs"] != nil {
		t.Fatal("digest mutated a shared result template")
	}
	for _, id := range []string{foreground.ID, other.ID} {
		record, _ := s.Get(id, "")
		if record.Acknowledged {
			t.Fatal("digest acknowledged an invisible job")
		}
	}
	record, _ := s.Get(finished.ID, "")
	if !record.Acknowledged {
		t.Fatal("visible finished job was not acknowledged")
	}
	r = s.AttachStatus("read", map[string]any{"workspace_id": resolver.workspace.ID}, base)
	if _, ok := r.StructuredContent["background_jobs"]; ok {
		t.Fatal("finished job appeared twice")
	}
}

func TestAttachStatusConcurrentCompletionIsPublishedOnce(t *testing.T) {
	s, _, resolver := newFixture(t)
	record := digestRecord(t, s, resolver, "background", resolver.workspace.ID, "succeeded")
	base := wire.Success("read", "Read File", nil, "ok")
	var wg sync.WaitGroup
	out := make(chan wire.Result, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); out <- s.AttachStatus("read", nil, base) }()
	}
	wg.Wait()
	close(out)
	count := 0
	for result := range out {
		if rows, ok := result.StructuredContent["background_jobs"].([]map[string]any); ok {
			for _, row := range rows {
				if row["job_id"] == record.ID {
					count++
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("finished job published %d times concurrently", count)
	}
}

func TestAttachStatusRunningFirstAndMaximumEight(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobs", 20)
	cfg.Set("maxJobsPerWorkspace", 20)
	for i := 0; i < 9; i++ {
		o := opts(resolver, fmt.Sprintf("printf digest%d", i))
		if _, _, err := s.admit(o); err != nil {
			t.Fatal(err)
		}
	}
	finished := digestRecord(t, s, resolver, "background", resolver.workspace.ID, "succeeded")
	r := s.AttachStatus("read", nil, wire.Success("read", "Read File", nil, "ok"))
	rows := r.StructuredContent["background_jobs"].([]map[string]any)
	if len(rows) != 8 {
		t.Fatal("digest cap differs from eight")
	}
	for _, row := range rows {
		if row["status"] != "running" {
			t.Fatal("finished row displaced running job")
		}
	}
	if record, _ := s.Get(finished.ID, ""); record.Acknowledged {
		t.Fatal("capacity-hidden completion was acknowledged")
	}
	// These admission-only fixtures never launch a child. Settle them explicitly
	// so normal fixture cleanup does not spend its bounded wait on prepared jobs.
	for _, record := range s.Snapshot() {
		if record.Status == "running" {
			_, _ = s.update(record.ID, func(r *Record) bool { r.Status = "stopped"; r.Quiescent = true; return true })
		}
	}
	_, _ = s.Call(context.Background(), wire.Scope{}, "jobs", map[string]any{"workspace_id": resolver.workspace.ID, "wait_ms": 0, "output": "none"})
}
