package audit

import (
	"context"
	"fmt"
	"testing"
)

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
