package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func collectorInput(project, request, label, tool string, start, finish time.Time) RecordInput {
	input := recordInput(project, request)
	input.Scope.Agent, input.ToolName = label, tool
	input.StartedAt, input.FinishedAt = start, finish
	return input
}

func TestCollectorEventMaximumSurvivesDelayedCompletionAndRecovery(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	at := options.Clock()
	writeTest(t, j, collectorInput("private-a", "recent-call", "SP1", "read", at.Add(-2*time.Second), at.Add(-time.Second)))
	writeTest(t, j, collectorInput("private-b", "older-call", "SP2", "bash", at.Add(-10*time.Second), at.Add(-3*time.Second)))
	writeTest(t, j, collectorInput("private-c", "newer-job", "SP2", "bash_job", at.Add(-5*time.Second), at))
	writeTest(t, j, collectorInput("private-outside", "unrelated", "SP10", "read", at.Add(time.Hour), at.Add(2*time.Hour)))
	writeTest(t, j, collectorInput("private-unlabelled", "none", "", "read", at, at))

	// A durable completion with an older event time is replayed after a crash.
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	options.Fault = func(stage string) error {
		if stage == "after_sync" {
			return errors.New("disposable crash before index commit")
		}
		return nil
	}
	j = openTest(t, options)
	input := collectorInput("private-delayed", "delayed-job", "SP1", "bash_job", at.Add(-time.Minute), at.Add(-30*time.Second))
	input.CompletionDedupKey = "fixture-delayed-completion"
	if receipt, err := j.Record(context.Background(), input); err == nil || receipt.Recorded {
		t.Fatal("fixture did not stop before a durable index receipt")
	}
	if got := j.CollectEventTimes(context.Background(), at); got.Known || len(got.Labels) != 0 {
		t.Fatal("failed journal owner supplied a complete projection")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	options.Fault = nil
	j = openTest(t, options)
	receipt, err := j.Record(context.Background(), input)
	if err != nil || !receipt.Duplicate || receipt.Recorded {
		t.Fatal("fixture did not recover the delayed completion")
	}
	got := j.CollectEventTimes(context.Background(), at)
	if !got.Known || len(got.Labels) != 9 {
		t.Fatal("retained fixed-label observation missing")
	}
	for i, expected := range []string{"2026-10-31T15:59:59Z", "2026-10-31T16:00:00Z"} {
		if got.Labels[i].Label != []string{"SP1", "SP2"}[i] || got.Labels[i].Last == nil ||
			got.Labels[i].Last.Format(time.RFC3339) != expected {
			t.Fatalf("event maximum for label %d was not %s", i+1, expected)
		}
	}
	for _, label := range got.Labels[2:] {
		if label.Last != nil {
			t.Fatal("unrelated labels became SP observations")
		}
	}
}

func TestCollectorMissingRetainedTimeDoesNotMeanNeverActive(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	at := options.Clock()
	old := writeTest(t, j, collectorInput("busy", "expired", "SP1", "read", at.Add(-time.Minute), at.Add(-time.Minute)))
	writeTest(t, j, collectorInput("quiet", "retained", "SP2", "read", at.Add(-20*time.Second), at.Add(-10*time.Second)))
	writeTest(t, j, collectorInput("busy", "replacement-one", "", "read", at, at))
	writeTest(t, j, collectorInput("busy", "replacement-two", "", "read", at, at))
	if _, exists, err := j.Get(context.Background(), old.ActionID); err != nil || exists {
		t.Fatal("fixture did not expire the earlier observation")
	}
	got := j.CollectEventTimes(context.Background(), at)
	if !got.Known || len(got.Labels) != 9 || got.Labels[0].Last != nil ||
		got.Labels[1].Last == nil || got.Labels[1].Last.Format(time.RFC3339) != "2026-10-31T15:59:50Z" {
		t.Fatal("planned retention invented history or lost retained quiet-project evidence")
	}
}

type collectorCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *collectorCancellation) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCollectorCancellationClosedAndBusyOwnersAreUnknown(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	at := options.Clock()
	writeTest(t, j, collectorInput("a", "one", "SP1", "read", at, at))
	writeTest(t, j, collectorInput("b", "two", "SP2", "read", at, at))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := j.CollectEventTimes(&collectorCancellation{Context: ctx, cancel: cancel}, at)
	if got.Known || len(got.Labels) != 0 {
		t.Fatal("cancellation exposed a partial maximum")
	}
	j.mu.Lock()
	got = j.CollectEventTimes(context.Background(), at)
	j.mu.Unlock()
	if got.Known {
		t.Fatal("busy writer was treated as a complete observation")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if got := j.CollectEventTimes(context.Background(), at); got.Known {
		t.Fatal("closed owner was reported as available")
	}
	disabled := openTest(t, Options{})
	if disabled.CollectEventTimes(context.Background(), at).Known {
		t.Fatal("disabled owner was reported as empty")
	}
}

func TestCollectorDamagedOrInconsistentRetainedEvidenceIsUnknown(t *testing.T) {
	for _, shape := range []string{"torn", "gap", "read-error", "missing-time", "future", "inverted"} {
		t.Run(shape, func(t *testing.T) {
			options := testOptions(t.TempDir())
			j := openTest(t, options)
			at := options.Clock()
			start, finish := at.Add(-time.Second), at
			if shape == "future" {
				start, finish = at.Add(time.Second), at.Add(2*time.Second)
			} else if shape == "inverted" {
				start, finish = at, at.Add(-time.Second)
			}
			writeTest(t, j, collectorInput("private", "fixture", "SP1", "read", start, finish))
			path := filepath.Join(options.Directory, j.currentName)
			if shape == "torn" || shape == "gap" {
				if err := j.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "torn" {
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString("{\"sequence\":2")
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else if shape == "gap" || shape == "read-error" {
				file, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteAt([]byte("!"), 0)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else if shape == "missing-time" {
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				changed := strings.Replace(string(body), "\"occurred_at\":", "\"missing__at\":", 1)
				if len(changed) != len(body) || changed == string(body) {
					t.Fatal("invalid fixture mutation")
				}
				if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "torn" || shape == "gap" {
				j = openTest(t, options)
			}
			got := j.CollectEventTimes(context.Background(), at)
			if got.Known || len(got.Labels) != 0 {
				t.Fatal("unavailable maximum became a complete observation")
			}
		})
	}
}
