package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func privateCompletionInput(key string) RecordInput {
	started := time.Date(2026, 10, 31, 18, 0, 0, 0, time.UTC)
	return RecordInput{
		CompletionDedupKey: key, ToolName: "bash_job", Mutating: true,
		StartedAt: started, FinishedAt: started.Add(time.Second),
		Args: map[string]any{"workspace_id": "ws_completion", "project_id": "completion",
			"job_id": "j_12345678", "command": "printf done", "cwd": ".", "origin": "background"},
		Result: wire.Result{StructuredContent: map[string]any{
			"workspace_id": "ws_completion", "project_id": "completion", "job_id": "j_12345678",
			"job_status": "succeeded", "origin": "background", "exit_code": 0, "signal": nil,
			"duration_ms": int64(1000), "timed_out": false, "stdout_bytes": int64(4), "stderr_bytes": int64(0),
		}},
	}
}

func assertCompletionPublicContext(t *testing.T, action Action, key string) {
	t.Helper()
	if action["actor_ref"] != opaque("actor", "") || action["request_ref"] != opaque("request", "") {
		t.Fatalf("completion invented public caller identity: actor=%v request=%v", action["actor_ref"], action["request_ref"])
	}
	for _, field := range []string{"audit_scope", "request_fingerprint", "transport_session_ref", "parent_action_id", "git_before", "git_after", "path_evidence_before", "path_evidence_after", "completion_dedup_key"} {
		if _, ok := action[field]; ok {
			t.Fatalf("completion added public context field %q", field)
		}
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), "completion:") {
		t.Fatal("private completion receipt key escaped into the public action")
	}
	if action["status"] != "succeeded" || number(action["changed_path_count"]) != 0 {
		t.Fatalf("unexpected completion status or mutation evidence: %v", action)
	}
	paths, ok := action["changed_paths"].([]any)
	if !ok || paths == nil || len(paths) != 0 {
		t.Fatalf("completion must retain an empty changed_paths array: %#v", action["changed_paths"])
	}
}

func TestPrivateCompletionReceiptDoesNotInventPublicContext(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	ctx := context.Background()
	input := privateCompletionInput("private-incarnation-j_12345678:2026-10-31T18:00:00.000Z")
	first, err := j.Record(ctx, input)
	if err != nil || !first.Recorded || first.Duplicate {
		t.Fatalf("initial receipt: %+v %v", first, err)
	}
	action, ok, err := j.Get(ctx, first.ActionID)
	if err != nil || !ok {
		t.Fatalf("completion getter: %v %v", ok, err)
	}
	assertCompletionPublicContext(t, action, input.CompletionDedupKey)
	duplicate, err := j.Record(ctx, input)
	if err != nil || !duplicate.Duplicate || duplicate.Recorded || duplicate.ActionID != first.ActionID || duplicate.Sequence != first.Sequence {
		t.Fatalf("completion retry did not return the original receipt: %+v %v", duplicate, err)
	}
	input.CompletionDedupKey = "private-incarnation-j_12345678:2026-10-31T18:00:02.000Z"
	next, err := j.Record(ctx, input)
	if err != nil || !next.Recorded || next.Sequence != first.Sequence+1 || next.ActionID == first.ActionID {
		t.Fatalf("a reused compact job ID lost its new incarnation: %+v %v", next, err)
	}
}

func TestPrivateCompletionReceiptSurvivesCrashBeforeIndex(t *testing.T) {
	for _, stage := range []string{"after_sync", "before_index_commit", "after_index_commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			injected := errors.New("completion fault at " + stage)
			options := Options{Directory: dir, Enabled: true, MaxBytes: 1 << 20, RetainActions: 20,
				Fault: func(at string) error {
					if at == stage {
						return injected
					}
					return nil
				}}
			j, err := Open(options)
			if err != nil {
				t.Fatal(err)
			}
			input := privateCompletionInput("crash-window-" + stage)
			result, err := j.Record(context.Background(), input)
			if !errors.Is(err, injected) || result.Recorded {
				t.Fatalf("fault must not acknowledge recording: %+v %v", result, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			options.Fault = nil
			j, err = Open(options)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			recovered, err := j.Record(context.Background(), input)
			if err != nil || !recovered.Duplicate || recovered.Recorded || recovered.Sequence != 1 {
				t.Fatalf("synced completion was duplicated after recovery: %+v %v", recovered, err)
			}
			action, ok, err := j.Get(context.Background(), recovered.ActionID)
			if err != nil || !ok {
				t.Fatalf("recovered completion missing: %v %v", ok, err)
			}
			assertCompletionPublicContext(t, action, input.CompletionDedupKey)
		})
	}
}

func TestPrivateCompletionReceiptOutlivesRetention(t *testing.T) {
	options := Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 1}
	j, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	input := privateCompletionInput("old-completion-private-receipt")
	first, err := j.Record(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	nextInput := privateCompletionInput("new-completion-private-receipt")
	if _, err := j.Record(context.Background(), nextInput); err != nil {
		t.Fatal(err)
	}
	if _, retained, err := j.Get(context.Background(), first.ActionID); err != nil || retained {
		t.Fatalf("fixture did not prune the original action: retained=%v err=%v", retained, err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(options)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	duplicate, err := j.Record(context.Background(), input)
	if err != nil || !duplicate.Duplicate || duplicate.Recorded || duplicate.ActionID != first.ActionID || duplicate.Sequence != first.Sequence {
		t.Fatalf("pruned completion lost its durable receipt: %+v %v", duplicate, err)
	}
}

func TestConcurrentPrivateCompletionRetriesShareOneReceipt(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true, MaxBytes: 1 << 20, RetainActions: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	const count = 16
	results := make(chan response, count)
	var ready sync.WaitGroup
	ready.Add(count)
	start := make(chan struct{})
	for n := 0; n < count; n++ {
		go func() {
			ready.Done()
			<-start
			receipt, err := j.Record(context.Background(), privateCompletionInput("concurrent-private-incarnation"))
			results <- response{receipt: receipt, err: err}
		}()
	}
	ready.Wait()
	close(start)
	recorded, duplicated := 0, 0
	id := ""
	for n := 0; n < count; n++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.receipt.Recorded {
			recorded++
		}
		if result.receipt.Duplicate {
			duplicated++
		}
		if id == "" {
			id = result.receipt.ActionID
		}
		if result.receipt.ActionID != id || result.receipt.Sequence != 1 {
			t.Fatalf("inconsistent concurrent receipt: %+v", result.receipt)
		}
	}
	if recorded != 1 || duplicated != count-1 {
		t.Fatalf("recorded=%d duplicate=%d", recorded, duplicated)
	}
}

func TestPrivateCompletionKeyRejectsWrongDomain(t *testing.T) {
	j, err := Open(Options{Directory: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	input := privateCompletionInput("guarded-private-key")
	input.ToolName = "read"
	if result, err := j.Record(context.Background(), input); err == nil || result.Recorded {
		t.Fatal("completion key accepted for an ordinary tool")
	}
	input.ToolName = "bash_job"
	input.ActionID = "cpa_unrelated"
	if result, err := j.Record(context.Background(), input); err == nil || result.Recorded {
		t.Fatal("completion key overwrote a conflicting action identity")
	}
}
