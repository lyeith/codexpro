package jobs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// NewPaused leaves recovery, dispatch, observation and completion workers stopped.
// These fixtures publish synthetic records directly and never start a process.
func newSubmissionProjectionService(tb testing.TB) *Service {
	tb.Helper()
	tb.Setenv("CODEXPRO_JOB_SCOPES", "0")
	root := tb.TempDir()
	cfg := config.New(filepath.Join(root, "state"))
	cfg.Set("maxJobHistoryPerWorkspace", 10000)
	cfg.Set("jobRetentionMs", 3600000)
	cfg.Set("maxRetainedJobBytes", 1<<30)
	s, err := NewPaused(cfg, testResolver{wire.Workspace{ID: "fp5-private", Root: root}})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("close private jobs store: %v", err)
		}
	})
	return s
}

func insertSubmissionProjectionRecords(tb testing.TB, s *Service, records []Record) {
	tb.Helper()
	s.admission.Lock()
	err := s.insertRecords(records)
	s.admission.Unlock()
	if err != nil {
		tb.Fatal(err)
	}
}

// Use the normal persisted terminal transition to create a real outbox payload.
// Scope and claim values are inert metadata; no owner or helper is invoked.
func completeSubmissionProjectionRecord(tb testing.TB, s *Service, id, owner string) Record {
	tb.Helper()
	r, err := s.update(id, func(r *Record) bool {
		r.Status = "succeeded"
		r.ExitCode = intPointer(0)
		r.FinishedAtMS = r.StartedAtMS + 1
		r.Quiescent = owner == ""
		switch owner {
		case "scope":
			r.ScopeUnit = "codexpro-fp5-projection-fixture"
		case "claim":
			r.ResourceClaim = &process.ResourceClaim{
				Version:        1,
				Helper:         "/fixture/fp5-no-execution",
				RunID:          "fp5-projection-run",
				RegistrationID: "fp5-projection-registration",
			}
		}
		return true
	})
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// Read the stored BLOBs themselves, independently of the target's SQL length
// expression and in-memory selection. The caller supplies the known fixture ID.
func submissionProjectionStoredBytes(tb testing.TB, s *Service, id string) (int64, int64) {
	tb.Helper()
	var payload, completion []byte
	err := s.db.QueryRow(
		"SELECT CAST(j.payload AS BLOB), CAST(c.payload AS BLOB) FROM jobs j LEFT JOIN job_completions c ON c.job_id=j.id WHERE j.id=?",
		id,
	).Scan(&payload, &completion)
	if err != nil {
		tb.Fatal(err)
	}
	if len(payload) == 0 {
		tb.Fatal("fixture has an empty stored job payload")
	}
	return int64(len(payload)), int64(len(completion))
}

func submissionProjectionReservedBytes(tb testing.TB, s *Service, payloadBytes int64) int64 {
	tb.Helper()
	helper, err := json.Marshal(s.cfg.HostResourcesHelper)
	if err != nil {
		tb.Fatal(err)
	}
	// Existing admission contract: two 16-KiB lifecycle fields at worst-case
	// JSON expansion, 4096 other bytes and the helper, plus a full outbox copy.
	return 2 * (payloadBytes + 2*16384*6 + 4096 + int64(len(helper)))
}

func TestSubmissionPayloadBytesMissingKeyedRowFailsClosed(t *testing.T) {
	s := newSubmissionProjectionService(t)
	records := fp5History(2)
	records[0].SubmissionKey = "present-key"
	records[1].SubmissionKey = "missing-key"
	insertSubmissionProjectionRecords(t, s, records)

	result, err := s.db.Exec("DELETE FROM jobs WHERE id=?", records[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := result.RowsAffected(); err != nil || deleted != 1 {
		t.Fatalf("delete fixture row: count=%d err=%v", deleted, err)
	}
	if retained, ok := s.Get(records[1].ID, records[1].WorkspaceID); !ok || retained.SubmissionKey != "missing-key" {
		t.Fatal("missing SQL row was not retained in the keyed memory inventory")
	}
	payloadBytes, completionBytes := submissionProjectionStoredBytes(t, s, records[0].ID)
	if payloadBytes+completionBytes <= 0 {
		t.Fatal("surviving row cannot distinguish a partial total from zero")
	}

	s.admission.Lock()
	total, err := s.submissionPayloadBytes()
	s.admission.Unlock()
	if err == nil || err.Error() != "queued submission payload inventory is incomplete" || total != 0 {
		t.Fatalf("incomplete keyed inventory: total=%d err=%v", total, err)
	}
}

func TestSubmissionPayloadBytesTerminalOwnerReservation(t *testing.T) {
	for _, owner := range []string{"scope", "claim"} {
		t.Run(owner, func(t *testing.T) {
			s := newSubmissionProjectionService(t)
			records := fp5History(1)
			records[0].SubmissionKey = "terminal-owner"
			records[0].Status = "queued"
			records[0].ExitCode = nil
			records[0].FinishedAtMS = 0
			records[0].Quiescent = false
			insertSubmissionProjectionRecords(t, s, records)
			terminal := completeSubmissionProjectionRecord(t, s, records[0].ID, owner)
			if terminal.Status != "succeeded" || terminal.Quiescent {
				t.Fatal("fixture did not retain a terminal, unquiescent owner")
			}

			payloadBytes, completionBytes := submissionProjectionStoredBytes(t, s, terminal.ID)
			reserved := submissionProjectionReservedBytes(t, s, payloadBytes)
			if completionBytes == 0 || reserved <= payloadBytes+completionBytes {
				t.Fatal("fixture does not separate future reservation from stored jobs and outbox bytes")
			}
			s.admission.Lock()
			total, err := s.submissionPayloadBytes()
			s.admission.Unlock()
			if err != nil || total != reserved {
				t.Fatalf("terminal owner lost future growth reservation: got=%d want=%d err=%v", total, reserved, err)
			}

			if _, err = s.update(terminal.ID, func(r *Record) bool {
				r.Quiescent = true
				return true
			}); err != nil {
				t.Fatal(err)
			}
			quietPayload, quietCompletion := submissionProjectionStoredBytes(t, s, terminal.ID)
			if quietCompletion != completionBytes || quietCompletion == 0 {
				t.Fatal("quiescence lost or rewrote the retained outbox size")
			}
			// A false reservation flag still denotes a selected keyed record.
			expected := quietPayload + quietCompletion
			s.admission.Lock()
			total, err = s.submissionPayloadBytes()
			s.admission.Unlock()
			if err != nil || total != expected || total >= reserved {
				t.Fatalf("quiet keyed receipt must count actual jobs plus outbox: got=%d want=%d prior=%d err=%v", total, expected, reserved, err)
			}
		})
	}
}

func BenchmarkSubmissionPayloadBytes(b *testing.B) {
	cohort := []struct {
		name, status, owner string
		reserve             bool
	}{
		{"queued", "queued", "", true},
		{"running", "running", "", true},
		{"scope_pending", "succeeded", "scope", true},
		{"claim_pending", "succeeded", "claim", true},
		{"quiet", "succeeded", "", false},
	}
	for _, n := range []int{500, 5000} {
		for _, scenario := range []struct {
			name  string
			mixed bool
		}{
			{"no_keyed", false},
			{"mixed_keyed", true},
		} {
			b.Run(fmt.Sprintf("history_%d/%s", n, scenario.name), func(b *testing.B) {
				b.StopTimer()
				s := newSubmissionProjectionService(b)
				records := fp5History(n)
				// Fix persisted history bytes across baseline and candidate runs.
				for i := range records {
					records[i].StartedAtMS = 1800000000000 - int64(i)
					records[i].FinishedAtMS = records[i].StartedAtMS
				}
				if scenario.mixed {
					for i, member := range cohort {
						records[i].SubmissionKey = "projection-" + member.name
						records[i].Command = ": " + member.name
						records[i].Status = "queued"
						if member.status == "running" {
							records[i].Status = "running"
						}
						records[i].ExitCode = nil
						records[i].FinishedAtMS = 0
						records[i].Quiescent = false
					}
				}
				insertSubmissionProjectionRecords(b, s, records)
				if scenario.mixed {
					for i, member := range cohort {
						if member.status == "succeeded" {
							completeSubmissionProjectionRecord(b, s, records[i].ID, member.owner)
						}
					}
				}

				var jobsCount, outboxCount int
				if err := s.db.QueryRow("SELECT (SELECT count(*) FROM jobs), (SELECT count(*) FROM job_completions)").Scan(&jobsCount, &outboxCount); err != nil {
					b.Fatal(err)
				}
				expectedOutbox := 0
				if scenario.mixed {
					expectedOutbox = 3
				}
				if jobsCount != n || outboxCount != expectedOutbox {
					b.Fatalf("fixture SQL inventory: jobs=%d outbox=%d", jobsCount, outboxCount)
				}
				var expected int64
				if scenario.mixed {
					for i, member := range cohort {
						payloadBytes, completionBytes := submissionProjectionStoredBytes(b, s, records[i].ID)
						if (completionBytes > 0) != (member.status == "succeeded") {
							b.Fatalf("unexpected outbox membership for %s", member.name)
						}
						reservation := payloadBytes + completionBytes
						if member.reserve {
							reservation = max(reservation, submissionProjectionReservedBytes(b, s, payloadBytes))
						}
						expected += reservation
					}
				}
				s.admission.Lock()
				total, err := s.submissionPayloadBytes()
				s.admission.Unlock()
				if err != nil || total != expected {
					b.Fatalf("fixture byte accounting: got=%d want=%d err=%v", total, expected, err)
				}

				b.ReportAllocs()
				b.ResetTimer()
				b.StartTimer()
				for i := 0; i < b.N; i++ {
					s.admission.Lock()
					total, err := s.submissionPayloadBytes()
					s.admission.Unlock()
					if err != nil || total != expected {
						b.Fatalf("byte accounting: got=%d want=%d err=%v", total, expected, err)
					}
				}
				b.StopTimer()
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
			})
		}
	}
}
