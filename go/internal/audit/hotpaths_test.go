package audit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func collectSettled(t *testing.T, j *Journal, observed time.Time) CollectorEventTimes {
	t.Helper()
	j.mu.RLock()
	j.mu.RUnlock()
	got := j.CollectEventTimes(context.Background(), observed)
	if !got.Known || len(got.Labels) != 9 {
		t.Fatalf("settled journal unavailable: %+v", got)
	}
	return got
}

func TestCollectorWarmSnapshotTracksRetentionAndCompaction(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions, options.SegmentBytes = 2, 4096
	j := openTest(t, options)
	at := options.Clock()
	old := at.Add(-time.Minute)
	newer := at.Add(-time.Second)
	writeTest(t, j, collectorInput("quiet", "quiet", "SP1", "read", old, old))
	writeTest(t, j, collectorInput("busy", "newer", "SP1", "read", newer, newer))
	first := collectSettled(t, j, at)
	if first.Labels[0].Last == nil || !first.Labels[0].Last.Equal(newer) {
		t.Fatal("initial retained maximum missing")
	}
	// A returned pointer is caller-owned.
	*first.Labels[0].Last = at.Add(time.Hour)
	if got := collectSettled(t, j, at); got.Labels[0].Last == nil || !got.Labels[0].Last.Equal(newer) {
		t.Fatal("caller changed the retained maximum")
	}
	writeTest(t, j, collectorInput("busy", "replacement-1", "", "read", at, at))
	writeTest(t, j, collectorInput("busy", "replacement-2", "", "read", at, at))
	if got := collectSettled(t, j, at); got.Labels[0].Last == nil || !got.Labels[0].Last.Equal(old) {
		t.Fatal("expired maximum survived retention or quiet evidence was lost")
	}
	writeTest(t, j, collectorInput("delayed", "older-completion", "SP1", "bash_job", old.Add(-time.Minute), old.Add(-time.Second)))
	if got := collectSettled(t, j, at); got.Labels[0].Last == nil || !got.Labels[0].Last.Equal(old) {
		t.Fatal("later sequence replaced the event-time maximum")
	}
	for i := 0; i < 20; i++ {
		writeTest(t, j, collectorInput("busy", fmt.Sprintf("compact-%d", i), "", "read", at, at))
		collectSettled(t, j, at)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openTest(t, options)
	if got := collectSettled(t, j, at); got.Labels[0].Last == nil || !got.Labels[0].Last.Equal(old) {
		t.Fatal("compaction/restart changed the retained maximum")
	}
}

func TestCollectorWarmSnapshotChecksObservationEachTime(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	at := options.Clock()
	future := at.Add(time.Minute)
	writeTest(t, j, collectorInput("a", "future", "SP1", "read", at, future))
	j.mu.RLock()
	j.mu.RUnlock()
	if got := j.CollectEventTimes(context.Background(), at); got.Known {
		t.Fatal("future completion became a present observation")
	}
	collectSettled(t, j, future)
	if got := j.CollectEventTimes(context.Background(), at); got.Known {
		t.Fatal("cached future completion bypassed observation time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := j.CollectEventTimes(ctx, future); got.Known {
		t.Fatal("cached observation bypassed cancellation")
	}
}

func TestCollectorWarmSnapshotDetectsSegmentChanges(t *testing.T) {
	for _, change := range []string{"corrupt", "missing-time", "replace", "delete", "restore-mtime"} {
		t.Run(change, func(t *testing.T) {
			if change == "restore-mtime" && runtime.GOOS != "linux" {
				t.Skip("Linux change-time invalidation")
			}
			options := testOptions(t.TempDir())
			j := openTest(t, options)
			at := options.Clock()
			writeTest(t, j, collectorInput("a", "warm", "SP1", "read", at, at))
			collectSettled(t, j, at)
			path := filepath.Join(options.Directory, j.currentName)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if change == "missing-time" {
				body = []byte(strings.Replace(string(body), "\"occurred_at\":", "\"missing__at\":", 1))
			} else {
				body[0] = '!'
			}
			switch change {
			case "delete":
				err = os.Remove(path)
			case "replace":
				replacement := path + ".replacement"
				if err = os.WriteFile(replacement, body, 0600); err == nil {
					if err = os.Chtimes(replacement, info.ModTime(), info.ModTime()); err == nil {
						err = os.Rename(replacement, path)
					}
				}
			default:
				err = os.WriteFile(path, body, 0600)
				if err == nil && change == "restore-mtime" {
					err = os.Chtimes(path, info.ModTime(), info.ModTime())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := j.CollectEventTimes(context.Background(), at); got.Known || len(got.Labels) != 0 {
				t.Fatal("warm observation hid damaged retained evidence")
			}
		})
	}
}

func TestCollectorConcurrentSnapshotsAndAppends(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 8
	j := openTest(t, options)
	at := options.Clock()
	writeTest(t, j, collectorInput("a", "initial", "SP1", "read", at, at))
	collectSettled(t, j, at)
	var readers sync.WaitGroup
	readers.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer readers.Done()
			for n := 0; n < 100; n++ {
				got := j.CollectEventTimes(context.Background(), at)
				if got.Known && (len(got.Labels) != 9 || got.Labels[0].Last == nil || got.Labels[0].Last.After(at)) {
					t.Error("inconsistent concurrent retained observation")
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		writeTest(t, j, collectorInput("a", fmt.Sprintf("concurrent-%d", i), "SP1", "read", at, at))
	}
	readers.Wait()
	collectSettled(t, j, at)
}

func TestAuditReducedLimitsPruneOtherProjectIndexes(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 8
	options.SegmentBytes = 512
	j := openTest(t, options)
	var alpha []Receipt
	for i := 0; i < 4; i++ {
		input := recordInput("alpha", fmt.Sprintf("change-%d", i))
		input.ToolName, input.Mutating = "write", true
		input.Args = map[string]any{"path": fmt.Sprintf("a%d.txt", i), "content": "x"}
		alpha = append(alpha, writeTest(t, j, input))
	}
	j.SetLimits(options.MaxBytes, 1, options.MaxOutputBytes)
	writeTest(t, j, recordInput("beta", "apply-reduced-limits"))
	for i, receipt := range alpha {
		_, exists, err := j.Get(context.Background(), receipt.ActionID)
		if err != nil || exists != (i == len(alpha)-1) {
			t.Fatalf("retention lookup %d: exists=%t err=%v", i, exists, err)
		}
	}
	change, err := j.LastRecordedProjectChange(context.Background(), "alpha")
	if err != nil || change == nil || change["action_id"] != alpha[len(alpha)-1].ActionID {
		t.Fatalf("unrelated project projection after limit reduction: %+v %v", change, err)
	}
	for i := 0; i < 20; i++ {
		writeTest(t, j, recordInput("beta", fmt.Sprintf("maintenance-%d", i)))
	}
	for _, project := range []string{"alpha", "beta"} {
		list, err := j.List(context.Background(), Filter{ProjectID: project})
		if err != nil || len(list.Actions) != 1 {
			t.Fatalf("%s retained list: %+v %v", project, list, err)
		}
	}
}

func TestCollectorWarmReadersShareKnownSnapshot(t *testing.T) {
	options := testOptions(t.TempDir())
	j := openTest(t, options)
	at := options.Clock()
	writeTest(t, j, collectorInput("a", "shared", "SP1", "read", at, at))
	collectSettled(t, j, at)
	var readers sync.WaitGroup
	readers.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer readers.Done()
			for n := 0; n < 100; n++ {
				got := j.CollectEventTimes(context.Background(), at)
				if !got.Known || len(got.Labels) != 9 || got.Labels[0].Last == nil || !got.Labels[0].Last.Equal(at) {
					t.Error("concurrent readers lost unchanged retained evidence")
					return
				}
			}
		}()
	}
	readers.Wait()
}

func TestCollectorAppendDoesNotHidePriorFileMutation(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt-%t", corrupt), func(t *testing.T) {
			options := testOptions(t.TempDir())
			j := openTest(t, options)
			at := options.Clock()
			writeTest(t, j, collectorInput("a", "old", "SP1", "read", at, at))
			collectSettled(t, j, at)
			path := filepath.Join(options.Directory, j.currentName)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				body[0] = '!'
			} else {
				body = []byte(strings.Replace(string(body), "\"SP1\"", "\"SP2\"", 1))
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			// The legitimate append also changes mtime/ctime. The old byte
			// prefix must still be checked before its parsed facts are reused.
			writeTest(t, j, collectorInput("a", "append", "SP3", "read", at, at))
			j.mu.RLock()
			j.mu.RUnlock()
			got := j.CollectEventTimes(context.Background(), at)
			if corrupt {
				if got.Known {
					t.Fatal("append concealed corruption in an older retained record")
				}
			} else if !got.Known || got.Labels[0].Last != nil ||
				got.Labels[1].Last == nil || !got.Labels[1].Last.Equal(at) ||
				got.Labels[2].Last == nil || !got.Labels[2].Last.Equal(at) {
				t.Fatalf("append concealed valid older record change: %+v", got)
			}
		})
	}
}

func TestCollectorParsedFactsFollowRetainedEntries(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions, options.SegmentBytes = 3, 1024
	j := openTest(t, options)
	at := options.Clock()
	for i := 0; i < 40; i++ {
		writeTest(t, j, collectorInput("a", fmt.Sprintf("bounded-%d", i), "SP1", "read", at, at))
		collectSettled(t, j, at)
		snapshot := j.collectorCache.Load()
		count := 0
		for name, segment := range snapshot.segments {
			for _, record := range segment.records {
				count++
				if record.entry.Segment != name {
					t.Fatal("parsed fact survived a segment replacement")
				}
				if _, exists := j.byID[record.entry.ActionID]; !exists {
					t.Fatal("expired parsed fact retained after pruning")
				}
			}
		}
		if count != len(j.entries) || count > options.RetainActions {
			t.Fatalf("parsed fact count %d differs from retained count %d", count, len(j.entries))
		}
	}
}
