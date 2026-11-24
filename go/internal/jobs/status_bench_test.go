package jobs

import (
	"fmt"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

var attachStatusBenchmarkResult wire.Result

// BenchmarkAttachStatusHistory exercises the real ordinary-tool digest with
// retained history that has the nested fields present in durable job records.
// Only running/queued jobs can be selected, so repeated iterations never change
// acknowledgements or measure SQLite writes instead of digest construction.
func BenchmarkAttachStatusHistory(b *testing.B) {
	for _, history := range []int{500, 5000} {
		for _, scenario := range []string{"no_eligible", "active", "workspace_owner_filters"} {
			b.Run(fmt.Sprintf("%s/history_%d", scenario, history), func(b *testing.B) {
				identity := wire.Identity{
					Source: "named", PrincipalType: "human", PrincipalID: "bench-principal",
					CredentialID: "bench-credential", AgentID: "bench-agent", AgentKind: "fleet",
				}
				s := &Service{records: make(map[string]*jobEntry, history+64)}
				now := time.Now().Truncate(time.Second)
				put := func(r Record) { s.records[r.ID] = newEntry(r, false) }
				for i := 0; i < history; i++ {
					r := attachStatusHistoryRecord(i, identity, now.Add(-time.Hour))
					if i%3 == 0 {
						r.WorkspaceID = "bench-other-workspace"
					}
					if i%5 == 0 {
						r.Identity.CredentialID = "bench-other-credential"
					}
					put(r)
				}

				wantRows := 0
				if scenario != "no_eligible" {
					// Four running jobs and eight queued jobs exercise the cap
					// and priority even when queued jobs have newer timestamps.
					for i := 0; i < 12; i++ {
						r := attachStatusHistoryRecord(history+i, identity, now.Add(-time.Minute))
						r.Status = "queued"
						if i < 4 {
							r.Status = "running"
						}
						r.Acknowledged = false
						r.Quiescent = false
						r.ExitCode = nil
						r.FinishedAt = ""
						r.FinishedAtMS = 0
						put(r)
					}
					wantRows = 8
				}

				var args map[string]any
				if scenario == "workspace_owner_filters" {
					args = map[string]any{"workspace_id": "bench-workspace"}
					// Newer excluded records must not displace owned jobs or
					// trigger a durable acknowledgement. These include another
					// credential for the same principal and display label.
					for i := 0; i < 48; i++ {
						r := attachStatusHistoryRecord(history+12+i, identity, now.Add(-30*time.Second))
						r.Acknowledged = false
						switch i % 6 {
						case 0:
							r.Origin = "foreground"
						case 1:
							r.WorkspaceID = "bench-other-workspace"
						case 2:
							r.Identity.CredentialID = "bench-other-credential"
						case 3:
							r.Identity.PrincipalID = "bench-other-principal"
						case 4:
							r.Identity.AgentID = "bench-other-agent"
						case 5:
							r.Identity = wire.Identity{}
						}
						put(r)
					}
				}

				base := wire.Success("read", "Read File", map[string]any{
					"path": "go/internal/jobs/status.go", "text": "package jobs",
				}, "package jobs")
				got := s.AttachStatus(identity, "read", args, base)
				rows, _ := got.StructuredContent["background_jobs"].([]map[string]any)
				if len(rows) != wantRows {
					b.Fatalf("fixture returned %d rows, want %d", len(rows), wantRows)
				}
				for _, row := range rows {
					if row["status"] != "running" && row["status"] != "queued" {
						b.Fatalf("fixture selected a mutable completion: %#v", row)
					}
				}
				if base.StructuredContent["background_jobs"] != nil {
					b.Fatal("fixture mutated the reusable result")
				}

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					attachStatusBenchmarkResult = s.AttachStatus(identity, "read", args, base)
				}
			})
		}
	}
}

func attachStatusHistoryRecord(i int, identity wire.Identity, at time.Time) Record {
	id := fmt.Sprintf("job_attach_bench_%06d", i)
	start := at.Add(time.Duration(i) * time.Millisecond)
	exit := 0
	return Record{
		ID: id, Agent: "FP5", WorkspaceID: "bench-workspace", ProjectID: "bench-project",
		Root: "/bench/workspace", Cwd: "go", Command: "go test ./internal/jobs",
		CommandLabel: "go test ./internal/jobs", PID: 1000 + i,
		ProcessIdentity: "bench-boot:12345", ScopeUnit: "codexpro-" + id + ".scope",
		ScopeIdentity: &process.ScopeIdentity{
			Version: 1, Unit: "codexpro-" + id + ".scope", BootID: "bench-boot",
			InvocationID: id, Cgroup: "/user.slice/bench/" + id,
		},
		ResourceClaim: &process.ResourceClaim{
			Version: 1, Helper: "/bench/bin/host-resources", RunID: id,
			RegistrationID: "registration-" + id,
		},
		Origin: "background", Status: "succeeded", Acknowledged: true, Quiescent: true,
		StartedAt: start.UTC().Format(time.RFC3339Nano), StartedAtMS: start.UnixMilli(),
		FinishedAt:   start.Add(time.Second).UTC().Format(time.RFC3339Nano),
		FinishedAtMS: start.Add(time.Second).UnixMilli(), ExitCode: &exit,
		TimeoutMS: 300000, DeadlineMS: start.Add(5 * time.Minute).UnixMilli(),
		OutputLimitBytes: 1048576, CapturedStdoutBytes: 4096, StoredBytes: 8192,
		StdoutPath:  "/bench/jobs/" + id + "/stdout.log",
		StderrPath:  "/bench/jobs/" + id + "/stderr.log",
		ResultPath:  "/bench/jobs/" + id + "/result.json",
		InputJobIDs: []string{"job_input_a", "job_input_b", "job_input_c"},
		LaunchState: "settled", QuiescenceScope: "process-group", Identity: identity,
		OwnerPrincipalID: identity.PrincipalID, OwnerSessionID: "bench-session",
		RequestID: "request-" + id, ParentActionID: "action-" + id,
	}
}
