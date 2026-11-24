package jobs

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func statusSelectionRecord(id, workspace, status string, started time.Time) Record {
	r := Record{
		ID: id, WorkspaceID: workspace, Origin: "background", Status: status,
		CommandLabel: "printf status", StartedAt: started.UTC().Format(time.RFC3339Nano),
		StartedAtMS: started.UnixMilli(),
	}
	if !unfinished(r) {
		r.FinishedAtMS = r.StartedAtMS + 1000
		r.FinishedAt = started.Add(time.Second).UTC().Format(time.RFC3339Nano)
		r.ExitCode = intPointer(0)
		r.Quiescent = true
	}
	return r
}

func insertStatusSelectionRecords(t *testing.T, s *Service, records []Record) {
	t.Helper()
	s.admission.Lock()
	err := s.insertRecords(records)
	s.admission.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func statusSelectionRows(t *testing.T, result wire.Result) []map[string]any {
	t.Helper()
	value, exists := result.StructuredContent["background_jobs"]
	if !exists {
		return nil
	}
	rows, ok := value.([]map[string]any)
	if !ok {
		t.Fatalf("unexpected digest rows: %#v", value)
	}
	return rows
}

func requireStatusSelectionIDs(t *testing.T, rows []map[string]any, want []string) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("got %d digest rows, want %d: %#v", len(rows), len(want), rows)
	}
	for i, id := range want {
		if rows[i]["job_id"] != id {
			t.Fatalf("digest row %d = %v, want %s", i, rows[i]["job_id"], id)
		}
	}
}

func TestAttachStatusPriorityNewestAndHiddenCompletions(t *testing.T) {
	s, _, resolver := queueFixture(t)
	at := time.Now().Add(-time.Minute)
	makeRecord := func(id, status string, offset time.Duration) Record {
		return statusSelectionRecord(id, resolver.workspace.ID, status, at.Add(offset))
	}
	records := []Record{
		makeRecord("done-hidden-old", "succeeded", 41*time.Second),
		makeRecord("running-old", "running", time.Second),
		makeRecord("queued-new", "queued", 25*time.Second),
		makeRecord("done-new", "succeeded", 45*time.Second),
		makeRecord("running-new", "running", 3*time.Second),
		makeRecord("done-expired", "failed", 44*time.Second),
		makeRecord("queued-old", "queued", 20*time.Second),
		makeRecord("done-promoted", "stopped", 43*time.Second),
		makeRecord("running-middle", "running", 2*time.Second),
		makeRecord("done-hidden-new", "succeeded", 42*time.Second),
	}
	records[1].Acknowledged = true  // Unfinished jobs remain visible even if acknowledged.
	records[5].OutputExpired = true // A tombstone still carries its unacknowledged completion.
	records[6].Acknowledged = true
	records[7].Origin = "promoted"
	insertStatusSelectionRecords(t, s, records)

	base := wire.Success("read", "Read File", nil, "ok")
	rows := statusSelectionRows(t, s.AttachStatus(wire.Identity{}, "read", nil, base))
	want := []string{
		"running-new", "running-middle", "running-old", "queued-new", "queued-old",
		"done-new", "done-expired", "done-promoted",
	}
	requireStatusSelectionIDs(t, rows, want)
	visibleTerminal := map[string]bool{"done-new": true, "done-expired": true, "done-promoted": true}
	for _, original := range records {
		current, ok := s.Get(original.ID, "")
		if !ok || current.Acknowledged != (original.Acknowledged || visibleTerminal[original.ID]) {
			t.Fatalf("acknowledgement escaped displayed terminal rows: %+v", current)
		}
	}
	rows = statusSelectionRows(t, s.AttachStatus(wire.Identity{}, "read", nil, base))
	requireStatusSelectionIDs(t, rows, []string{
		"running-new", "running-middle", "running-old", "queued-new", "queued-old",
		"done-hidden-new", "done-hidden-old",
	})
}

func TestAttachStatusFiltersBeforeSelectingAndAcknowledging(t *testing.T) {
	s, _, resolver := queueFixture(t)
	identity := queueOwner("FP5").Identity
	at := time.Now().Add(-time.Minute)
	visible := statusSelectionRecord("owned", resolver.workspace.ID, "succeeded", at)
	visible.Identity = identity
	visible.Identity.Role = "changed-role" // Role and display label do not own receipts.
	visible.Agent = "SP99"
	records := []Record{visible}
	for i := 0; i < 80; i++ {
		r := statusSelectionRecord(fmt.Sprintf("excluded-%02d", i), resolver.workspace.ID, "succeeded", at.Add(time.Duration(i+1)*time.Millisecond))
		r.Identity = identity
		r.Agent = "FP5"
		switch i % 8 {
		case 0:
			r.Acknowledged = true
		case 1:
			r.Origin = "foreground"
		case 2:
			r.WorkspaceID = "other-workspace"
		case 3:
			r.Identity.CredentialID = "another-credential"
		case 4:
			r.Identity.PrincipalID = "another-principal"
		case 5:
			r.Identity.AgentID = "another-agent"
		case 6:
			r.Identity.AgentKind = "another-kind"
		case 7:
			r.Identity = wire.Identity{}
		}
		records = append(records, r)
	}
	insertStatusSelectionRecords(t, s, records)
	base := wire.Success("read", "Read File", nil, "ok")
	result := s.AttachStatus(identity, "read", map[string]any{"workspace_id": resolver.workspace.ID}, base)
	requireStatusSelectionIDs(t, statusSelectionRows(t, result), []string{"owned"})
	for _, original := range records {
		current, ok := s.Get(original.ID, "")
		wantAcknowledged := original.Acknowledged || original.ID == visible.ID
		if !ok || current.Acknowledged != wantAcknowledged {
			t.Fatalf("filtered job acknowledged: %+v", current)
		}
	}
	// Without a workspace filter, the same credential can collect other workspaces.
	rows := statusSelectionRows(t, s.AttachStatus(identity, "list_projects", nil, base))
	requireStatusSelectionIDs(t, rows, []string{
		"excluded-74", "excluded-66", "excluded-58", "excluded-50",
		"excluded-42", "excluded-34", "excluded-26", "excluded-18",
	})
}

func TestAttachStatusUnnamedCallerKeepsLegacyOwnership(t *testing.T) {
	s, _, resolver := queueFixture(t)
	at := time.Now().Add(-time.Minute)
	unnamed := statusSelectionRecord("unnamed", resolver.workspace.ID, "succeeded", at)
	legacy := statusSelectionRecord("legacy", resolver.workspace.ID, "succeeded", at.Add(time.Second))
	legacy.Identity.Source = "legacy"
	named := statusSelectionRecord("named", resolver.workspace.ID, "succeeded", at.Add(2*time.Second))
	named.Identity = queueOwner("FP5").Identity
	insertStatusSelectionRecords(t, s, []Record{named, unnamed, legacy})

	result := s.AttachStatus(wire.Identity{}, "read", nil, wire.Success("read", "Read File", nil, "ok"))
	requireStatusSelectionIDs(t, statusSelectionRows(t, result), []string{"legacy", "unnamed"})
	if current, ok := s.Get(named.ID, ""); !ok || current.Acknowledged {
		t.Fatal("unnamed caller acknowledged a named credential's completion")
	}
}

func TestAttachStatusFailedAcknowledgementStaysVisibleAndDetached(t *testing.T) {
	s, cfg, resolver := queueFixture(t)
	r := statusSelectionRecord("retry-ack", resolver.workspace.ID, "failed", time.Now().Add(-time.Minute))
	r.Identity = queueOwner("FP5").Identity
	r.ExitCode = intPointer(7)
	insertStatusSelectionRecords(t, s, []Record{r})
	if _, err := s.db.Exec("CREATE TRIGGER refuse_status_ack BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'fixture'); END;"); err != nil {
		t.Fatal(err)
	}
	base := wire.Success("read", "Read File", nil, "ok")
	rows := statusSelectionRows(t, s.AttachStatus(r.Identity, "read", nil, base))
	requireStatusSelectionIDs(t, rows, []string{r.ID})
	*rows[0]["exit_code"].(*int) = 99
	current, ok := s.Get(r.ID, "")
	if !ok || current.Acknowledged || current.ExitCode == nil || *current.ExitCode != 7 {
		t.Fatalf("failed acknowledgement or returned pointer changed published state: %+v", current)
	}
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM jobs WHERE id=?", r.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var durable Record
	if err := json.Unmarshal(payload, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Acknowledged || durable.ExitCode == nil || *durable.ExitCode != 7 {
		t.Fatalf("failed acknowledgement changed durable state: %+v", durable)
	}
	if _, err := s.db.Exec("DROP TRIGGER refuse_status_ack"); err != nil {
		t.Fatal(err)
	}
	rows = statusSelectionRows(t, s.AttachStatus(r.Identity, "read", nil, base))
	requireStatusSelectionIDs(t, rows, []string{r.ID})
	if code := rows[0]["exit_code"].(*int); *code != 7 {
		t.Fatalf("retry inherited a mutation from the previous response: %d", *code)
	}
	current, ok = s.Get(r.ID, "")
	if !ok || !current.Acknowledged {
		t.Fatal("successful retry did not acknowledge the completion")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewPaused(cfg, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	result := recovered.AttachStatus(r.Identity, "read", nil, base)
	if rows := statusSelectionRows(t, result); len(rows) != 0 {
		t.Fatalf("durably acknowledged completion reappeared after recovery: %#v", rows)
	}
}
