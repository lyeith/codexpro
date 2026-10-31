package audit

import (
	"context"
	"sync"
	"testing"
)

func TestProjectChangeCacheTracksRetentionAndRestart(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	changed := recordInput("alpha", "changed")
	changed.ToolName = "write"
	changed.Mutating = true
	changed.Args = map[string]any{"path": "a.txt", "content": "x"}
	first := writeTest(t, j, changed)
	value, err := j.LastRecordedProjectChange(context.Background(), "alpha")
	if err != nil || value == nil || value["action_id"] != first.ActionID {
		t.Fatalf("missing change cache: %#v %v", value, err)
	}
	value["action_id"] = "tampered"
	value, err = j.LastRecordedProjectChange(context.Background(), "alpha")
	if err != nil || value["action_id"] != first.ActionID {
		t.Fatal("caller mutated cached evidence")
	}
	// Calls with no recorded path or HEAD change eventually expire the evidence.
	for _, id := range []string{"no-change-1", "no-change-2"} {
		input := recordInput("alpha", id)
		input.ToolName = "bash"
		input.Mutating = true
		input.Args = map[string]any{"command": "true"}
		input.Result.StructuredContent = map[string]any{"exit_code": 0}
		writeTest(t, j, input)
	}
	if value, err = j.LastRecordedProjectChange(context.Background(), "alpha"); err != nil || value != nil {
		t.Fatalf("expired change retained: %#v %v", value, err)
	}
	changed.Scope.RequestID = "new-change"
	last := writeTest(t, j, changed)
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j = openTest(t, options)
	defer j.Close()
	if value, err = j.LastRecordedProjectChange(context.Background(), "alpha"); err != nil || value["action_id"] != last.ActionID {
		t.Fatalf("restart cache mismatch: %#v %v", value, err)
	}
}

func TestRuntimeAuditLimitsAreAtomicDuringConcurrentWork(t *testing.T) {
	j := openTest(t, testOptions(t.TempDir()))
	defer j.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%2 == 0 {
				j.SetLimits(1000000, 2, 4096)
			} else {
				j.SetLimits(2000000, 4, 8192)
			}
		}
	}()
	for i := 0; i < 10; i++ {
		input := recordInput("alpha", "")
		input.Scope.RequestID = randomID("reload_")
		writeTest(t, j, input)
		status, err := j.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r := object(status["retention"])
		a, b := int64(number(r["max_bytes"])), int(number(r["retain_actions"]))
		if !((a == 1000000 && b == 2) || (a == 2000000 && b == 4) || (a == j.options.MaxBytes && b == j.options.RetainActions)) {
			t.Fatalf("torn limits: %#v", r)
		}
	}
	wg.Wait()
	j.SetLimits(1000000, 1, 4096)
	writeTest(t, j, recordInput("alpha", "final"))
	list, err := j.List(context.Background(), Filter{ProjectID: "alpha", Limit: 500})
	if err != nil || len(list.Actions) != 1 {
		t.Fatalf("new retention ignored: %d %v", len(list.Actions), err)
	}
}
