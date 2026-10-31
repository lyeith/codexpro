package audit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTimedOutReceiptRetainsAdmissionSnapshot(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var intercepted atomic.Bool
	options := testOptions(t.TempDir())
	options.Fault = func(stage string) error {
		if stage == "before_append" && intercepted.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}
	j := openTest(t, options)
	input := recordInput("alpha", "cancel-admission")
	input.ActionID = "cpa_cancel_admission"
	input.ToolName = "bash"
	input.Mutating = true
	input.Args = map[string]any{"command": "echo original"}
	input.Before = map[string]any{"git": map[string]any{"head": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "paths": []any{map[string]any{"path": "original.txt", "exists": true}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := j.Record(ctx, input); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not reach admission barrier")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled caller did not return")
	}
	input.Args["command"] = "echo changed after cancellation"
	object(input.Before["git"])["head"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	object(array(input.Before["paths"])[0])["path"] = "changed.txt"
	input.Result.StructuredContent["path"] = "changed-result.txt"
	unblock()
	writeTest(t, j, recordInput("alpha", "barrier"))
	action, found, err := j.GetDashboard(context.Background(), input.ActionID)
	if err != nil || !found {
		t.Fatal("accepted append did not finish", err)
	}
	if object(action["git_before"])["head"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatal("mutable capture leaked across receipt cancellation")
	}
	evidence := array(action["path_evidence_before"])
	if len(evidence) != 1 || object(evidence[0])["path"] != "original.txt" {
		t.Fatalf("path evidence changed: %#v", evidence)
	}
	scripts := array(object(action["dashboard_metadata"])["shell_scripts"])
	if len(scripts) != 1 || object(scripts[0])["script"] != "echo original" {
		t.Fatalf("script changed: %#v", scripts)
	}
}
