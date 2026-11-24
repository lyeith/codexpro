package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueDispatcherExpiresWithoutUnrelatedNotifications(t *testing.T) {
	s, _, resolver := queueFixture(t)
	occupy(t, s, queueOption(resolver, queueOwner("held"), "never-launched"))
	queued := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("queued"), "touch forbidden")}, "deadline")[0]
	_, err := s.update(queued.ID, func(r *Record) bool {
		r.QueueDeadlineMS = time.Now().Add(60 * time.Millisecond).UnixMilli()
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.wg.Add(1)
	go s.queueDispatcher()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err = s.wait(ctx, []string{queued.ID}, "all", 3*time.Second, false); err != nil {
		t.Fatal(err)
	}
	result, ok := s.Get(queued.ID, "")
	if !ok || result.Status != "timed_out" || result.PID != 0 {
		t.Fatalf("queue deadline did not settle without launch: %+v", result)
	}
	if _, err = os.Stat(filepath.Join(resolver.workspace.Root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("expired job executed")
	}
}

func TestQueueRetryHasNoIdleTimerAndHonorsNearestDeadline(t *testing.T) {
	s, _, resolver := queueFixture(t)
	if _, pending := s.queueRetryDelay(time.Now(), false); pending {
		t.Fatal("idle queue requests periodic polling")
	}
	now := time.Now().Truncate(time.Millisecond)
	queued := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("queued"), "unused")}, "near")[0]
	_, err := s.update(queued.ID, func(r *Record) bool { r.QueueDeadlineMS = now.Add(75 * time.Millisecond).UnixMilli(); return true })
	if err != nil {
		t.Fatal(err)
	}
	if delay, pending := s.queueRetryDelay(now, false); !pending || delay != 75*time.Millisecond {
		t.Fatalf("nearest deadline: %v %v", delay, pending)
	}
	if delay, pending := s.queueRetryDelay(now.Add(75*time.Millisecond), false); !pending || delay != 0 {
		t.Fatalf("deadline crossed after dispatch was delayed: %v %v", delay, pending)
	}
}

func TestPublicJobSnapshotsRemainDetached(t *testing.T) {
	s, _, resolver := queueFixture(t)
	r := occupy(t, s, queueOption(resolver, queueOwner("held"), "unused"))
	_, err := s.update(r.ID, func(r *Record) bool { r.ExitCode = intPointer(7); return true })
	if err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	*snapshot[0].ExitCode = 99
	one, _ := s.Get(r.ID, "")
	*one.ExitCode = 42
	again, _ := s.Get(r.ID, "")
	if *again.ExitCode != 7 {
		t.Fatal("public snapshot aliases published state")
	}
}

func TestMaintenanceSchedulesRetentionDeadline(t *testing.T) {
	s, cfg, _ := queueFixture(t)
	now := time.Now().Truncate(time.Millisecond)
	cfg.Set("jobRetentionMs", 1000)
	rows := fp5History(1)
	rows[0].FinishedAtMS = now.Add(-750 * time.Millisecond).UnixMilli()
	if err := s.insertRecords(rows); err != nil {
		t.Fatal(err)
	}
	if got := s.nextMaintenance(now, now); !got.Equal(now.Add(251 * time.Millisecond)) {
		t.Fatalf("retention deadline = %s", got.Sub(now))
	}
}

func TestQueueDispatcherWakesWhenExecutionCapacityReturns(t *testing.T) {
	s, cfg, resolver := newFixture(t)
	cfg.Set("maxJobs", 1)
	cfg.Set("maxJobsPerWorkspace", 1)
	held := occupy(t, s, queueOption(resolver, queueOwner("held"), "admission-only"))
	queued := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("queued"), "printf released")}, "release")[0]
	completeAdmission(t, s, held)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if _, err := s.wait(ctx, []string{queued.ID}, "all", 8*time.Second, false); err != nil {
		t.Fatal(err)
	}
	result, ok := s.Get(queued.ID, "")
	if !ok || result.Status != "succeeded" {
		t.Fatalf("capacity release did not execute queued job: %+v", result)
	}
	output, err := os.ReadFile(result.StdoutPath)
	if err != nil || string(output) != "released" {
		t.Fatalf("queued command output: %q %v", output, err)
	}
}

func TestMaintenanceCatchesExpiryDuringPruneWithoutSpinningOverdue(t *testing.T) {
	s, cfg, _ := queueFixture(t)
	cutoff := time.Now().Truncate(time.Millisecond)
	completed := cutoff.Add(20 * time.Millisecond)
	cfg.Set("jobRetentionMs", 1000)
	rows := fp5History(2)
	rows[0].FinishedAtMS = cutoff.Add(-1001 * time.Millisecond).UnixMilli()
	rows[1].FinishedAtMS = cutoff.Add(-995 * time.Millisecond).UnixMilli()
	if err := s.insertRecords(rows); err != nil {
		t.Fatal(err)
	}
	due := cutoff.Add(6 * time.Millisecond)
	if got := s.nextMaintenance(cutoff, completed); !got.Equal(due) {
		t.Fatalf("deadline crossed during prune was lost: %s, want %s", got, due)
	}
	// If these rows remain protected, the next pass must not schedule them again.
	if got := s.nextMaintenance(completed, completed); !got.Equal(completed.Add(idleMaintenanceInterval)) {
		t.Fatalf("already-overdue rows spin maintenance: %s", got)
	}
}

func TestQueueExpiryFailuresUseBoundedRetry(t *testing.T) {
	for _, draining := range []bool{false, true} {
		name := "closed-database"
		if draining {
			name = "draining"
		}
		t.Run(name, func(t *testing.T) {
			s, _, resolver := queueFixture(t)
			queued := enqueueTest(t, s, []startOptions{queueOption(resolver, queueOwner("queued"), "never-launched")}, "failed-expiry")[0]
			_, err := s.update(queued.ID, func(r *Record) bool { r.QueueDeadlineMS = time.Now().Add(-time.Second).UnixMilli(); return true })
			if err != nil {
				t.Fatal(err)
			}
			if draining {
				s.InterruptWaits()
			} else if err = s.db.Close(); err != nil {
				t.Fatal(err)
			}
			blocked := s.dispatchQueued()
			if !blocked {
				t.Fatal("failed or drained expiration did not request bounded retry")
			}
			if delay, pending := s.queueRetryDelay(time.Now(), blocked); !pending || delay != time.Second {
				t.Fatalf("failed expiration would spin: %v %v", delay, pending)
			}
			result, _ := s.Get(queued.ID, "")
			if result.Status != "queued" || result.PID != 0 {
				t.Fatalf("failed expiration changed or launched work: %+v", result)
			}
		})
	}
}
