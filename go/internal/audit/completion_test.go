package audit

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestCompletionReceiptOutlivesVisibleRetention(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 2
	j := openTest(t, options)
	input := recordInput("alpha", "job_abcd1234:2026-10-31T16:00:00.000Z")
	input.ToolName = "bash_job"
	input.Scope.TransportID = "codexpro-job-completion"
	original := writeTest(t, j, input)
	for i := 0; i < 5; i++ {
		writeTest(t, j, recordInput("alpha", fmt.Sprintf("later-%d", i)))
	}
	if _, ok, e := j.Get(context.Background(), original.ActionID); e != nil || ok {
		t.Fatalf("completion should have expired from public retention: %t %v", ok, e)
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	duplicate, e := j.Record(context.Background(), input)
	if e != nil || !duplicate.Duplicate || duplicate.Recorded || duplicate.ActionID != original.ActionID || duplicate.Sequence != original.Sequence {
		t.Fatalf("completion retry lost its durable receipt: %+v %v", duplicate, e)
	}
	input.Scope.RequestID = "job_abcd1234:2026-11-01T16:00:00.000Z"
	fresh := writeTest(t, j, input)
	if fresh.ActionID == original.ActionID || fresh.Sequence != 7 {
		t.Fatalf("compact job-id reuse was suppressed: %+v", fresh)
	}
}
func TestRecoveredCompletionPersistsIndependentReceipt(t *testing.T) {
	options := testOptions(t.TempDir())
	options.RetainActions = 1
	options.Fault = func(stage string) error {
		if stage == "after_sync" {
			return errors.New("crash before index receipt")
		}
		return nil
	}
	j := openTest(t, options)
	input := recordInput("alpha", "job_ffff1234:2026-10-31T16:01:00.000Z")
	input.ToolName = "bash_job"
	input.Scope.TransportID = "codexpro-job-completion"
	if receipt, e := j.Record(context.Background(), input); e == nil || receipt.Recorded {
		t.Fatal("premature completion acknowledgement")
	}
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	options.Fault = nil
	j = openTest(t, options)
	writeTest(t, j, recordInput("alpha", "replacement"))
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
	j = openTest(t, options)
	receipt, e := j.Record(context.Background(), input)
	if e != nil || !receipt.Duplicate || receipt.Sequence != 1 {
		t.Fatalf("replayed receipt lost: %+v %v", receipt, e)
	}
}
