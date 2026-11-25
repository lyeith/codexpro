package jobs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

var listHistoryBenchmarkResult wire.Result

// BenchmarkJobsListHistory measures the public jobs route, including metadata,
// redaction and result encoding. It does not run workers, read output bodies or
// acknowledge records. Active metadata stats are confined to temporary paths.
func BenchmarkJobsListHistory(b *testing.B) {
	for _, history := range []int{500, 5000} {
		for _, scenario := range []string{"noeligible", "active_over20", "defaultfinished", "filterednewerrecords"} {
			b.Run(fmt.Sprintf("%s/history_%d", scenario, history), func(b *testing.B) {
				s, w, identity := newListHistoryService(b)
				now := time.Now().Truncate(time.Second)
				historyIDs := make([]string, 0, history)
				for i := 0; i < history; i++ {
					r := attachStatusHistoryRecord(i, identity, now.Add(-time.Hour))
					// Expired, acknowledged terminal metadata is still eligible
					// for the default listing; no output filesystem is needed.
					r.OutputExpired = true
					putListHistoryRecord(s, w, r)
					historyIDs = append(historyIDs, r.ID)
				}

				args := map[string]any{"workspace_id": w.ID, "output": "none", "wait_ms": 0}
				var want []string
				switch scenario {
				case "noeligible":
					args["include_finished"] = false
				case "defaultfinished":
					for i := history - 1; i >= history-20; i-- {
						want = append(want, historyIDs[i])
					}
				case "active_over20", "filterednewerrecords":
					if scenario == "active_over20" {
						args["include_finished"] = false
					}
					for i := 0; i < 28; i++ {
						status := "queued"
						if i < 4 {
							status = "running"
						}
						r := listHistoryRecord(history+i, identity, now.Add(-time.Minute+time.Duration(i)*time.Second), status)
						r.ID = fmt.Sprintf("list-active-%02d", i)
						r.Acknowledged = false
						putListHistoryRecord(s, w, r)
					}
					// Running records are older than the queued candidates,
					// but occupy the first four positions.
					for i := 3; i >= 0; i-- {
						want = append(want, fmt.Sprintf("list-active-%02d", i))
					}
					for i := 27; i >= 12; i-- {
						want = append(want, fmt.Sprintf("list-active-%02d", i))
					}
				}
				if scenario == "filterednewerrecords" {
					for i := 0; i < 48; i++ {
						r := listHistoryRecord(history+28+i, identity, now.Add(-10*time.Second+time.Duration(i)*time.Millisecond), "running")
						r.ID = fmt.Sprintf("list-filtered-%02d", i)
						r.Acknowledged = false
						switch i % 8 {
						case 0:
							r.Origin = "foreground"
						case 1:
							r.WorkspaceID = "other-workspace"
						case 2:
							r.Identity.CredentialID = "other-credential"
						case 3:
							r.Identity.PrincipalID = "other-principal"
						case 4:
							r.Identity.AgentID = "other-agent"
						case 5:
							r.Identity.AgentKind = "other-kind"
						case 6:
							r.Identity = wire.Identity{}
						case 7:
							r.Identity.Source = "legacy"
						}
						putListHistoryRecord(s, w, r)
					}
				}

				ctx := context.Background()
				scope := wire.Scope{Agent: "FP5", Identity: identity}
				got, err := s.Call(ctx, scope, "jobs", args)
				if err != nil || got.IsError {
					b.Fatalf("fixture jobs call failed: result=%#v error=%v", got, err)
				}
				requireListHistoryIDs(b, got, want)
				if number(got.StructuredContent["requested_wait_ms"]) != 0 ||
					number(got.StructuredContent["returned_output_bytes"]) != 0 {
					b.Fatalf("fixture waited or returned output bodies: %#v", got.StructuredContent)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					listHistoryBenchmarkResult, err = s.Call(ctx, scope, "jobs", args)
					if err != nil || listHistoryBenchmarkResult.IsError {
						b.Fatalf("jobs call failed: result=%#v error=%v", listHistoryBenchmarkResult, err)
					}
				}
			})
		}
	}
}
