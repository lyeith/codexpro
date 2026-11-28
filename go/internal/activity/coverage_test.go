package activity

import (
	"context"
	"testing"
)

// activity.json carries the journal's time-scoped coverage so a consumer can
// check that its window starts at or after complete_since.
func TestActivityCoverageReportsCompleteSince(t *testing.T) {
	h := testHandler(t, nil)
	record(t, h, "read", "coverage", map[string]any{"path": "a.txt"}, map[string]any{"path": "a.txt"})
	out, e := h.CollectJSON(context.Background(), JSONOptions{Limit: 1, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	coverage := object(out["coverage"])
	if coverage["audit_enabled"] != true || coverage["appends_healthy"] != true || coverage["history_incomplete"] != false ||
		coverage["complete_since"] != timestamp(h.options.Clock()) || len(array(coverage["incomplete_ranges"])) != 0 {
		t.Fatalf("coverage: %v", coverage)
	}
	if _, ok := out["agent_activity"]; ok {
		t.Fatal("agent activity is only reported for an agent filter")
	}
	record(t, h, "read", "agent-call", map[string]any{"path": "a.txt", "agent": "SP16"}, map[string]any{"path": "a.txt"})
	out, e = h.CollectJSON(context.Background(), JSONOptions{Agent: "SP16", Limit: 1, QuietAfterMS: 30000})
	if e != nil {
		t.Fatal(e)
	}
	agent := object(out["agent_activity"])
	if agent["complete_since"] != timestamp(h.options.Clock()) || len(array(agent["actions"])) != 1 {
		t.Fatalf("agent activity: %v", agent)
	}
}
