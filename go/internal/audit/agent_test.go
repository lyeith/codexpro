package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestAgentRoundTripsInFlightAndJournalAfterRestart(t *testing.T) {
	options := testOptions(t.TempDir())
	journal := openTest(t, options)
	labeled := recordInput("test", "labeled")
	labeled.Scope.Agent = "SP-3"
	labeled.ActionID = journal.Begin(labeled)
	active := journal.InFlight()
	if len(active) != 1 || active[0].Agent != "SP-3" {
		t.Fatalf("in-flight action lost caller label: %v", active)
	}
	journal.MarkRunning(labeled.ActionID)
	if journal.InFlight()[0].Agent != "SP-3" {
		t.Fatal("running transition lost caller label")
	}
	labeledReceipt := writeTest(t, journal, labeled)
	journal.End(labeled.ActionID)
	if len(journal.InFlight()) != 0 {
		t.Fatal("completed action remained in-flight")
	}
	// A newer unlabeled action proves filtering happens before the limit.
	unlabeled := recordInput("test", "unlabeled")
	unlabeled.ActionID = journal.Begin(unlabeled)
	encoded, err := json.Marshal(journal.InFlight()[0])
	if err != nil {
		t.Fatal(err)
	}
	var activeFields map[string]any
	if err = json.Unmarshal(encoded, &activeFields); err != nil {
		t.Fatal(err)
	}
	if _, present := activeFields["agent"]; present {
		t.Fatal("omitted caller label changed in-flight representation")
	}
	unlabeledReceipt := writeTest(t, journal, unlabeled)
	journal.End(unlabeled.ActionID)
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openTest(t, options)
	for _, list := range []func(context.Context, Filter) (ListResult, error){journal.List, journal.ListDashboard} {
		result, err := list(context.Background(), Filter{Agent: "SP-3", Limit: 1})
		if err != nil || len(result.Actions) != 1 || result.Actions[0]["action_id"] != labeledReceipt.ActionID || result.Actions[0]["agent"] != "SP-3" {
			t.Fatalf("retained label/filter lost: %+v %v", result, err)
		}
		all, err := list(context.Background(), Filter{Limit: 10})
		if err != nil || len(all.Actions) != 2 {
			t.Fatalf("unfiltered actions changed: %+v %v", all, err)
		}
		if all.Actions[1]["action_id"] != unlabeledReceipt.ActionID {
			t.Fatal("unexpected action order")
		}
		if _, present := all.Actions[1]["agent"]; present {
			t.Fatal("omitted caller label changed persisted representation")
		}
	}
	zero := int64(0)
	forward, err := journal.List(context.Background(), Filter{Agent: "SP-3", AfterSequence: &zero, Limit: 1})
	if err != nil || len(forward.Actions) != 1 || forward.Actions[0]["agent"] != "SP-3" {
		t.Fatalf("forward label filter failed: %+v %v", forward, err)
	}
}

func TestJobEvidencePreservesOriginalAgent(t *testing.T) {
	for _, scenario := range []struct {
		tool  string
		data  map[string]any
		field string
	}{
		{"bash", map[string]any{"job_id": "job_12345678", "agent": "SP-3"}, "job_agent"},
		{"bash_job", map[string]any{"job_id": "job_12345678", "agent": "SP-3"}, "job_agent"},
		{"jobs", map[string]any{"jobs": []any{map[string]any{"job_id": "job_12345678", "agent": "SP-3"}}}, "jobs"},
		{"batch", map[string]any{"results": []any{map[string]any{"id": "run", "tool": "bash", "structured": map[string]any{"job_id": "job_12345678", "agent": "SP-3"}}}}, "child_results"},
	} {
		t.Run(scenario.tool, func(t *testing.T) {
			journal := openTest(t, testOptions(t.TempDir()))
			input := recordInput("test", scenario.tool)
			input.ToolName = scenario.tool
			input.Scope.Agent = "SP-7"
			input.Result = wire.Result{StructuredContent: scenario.data}
			receipt := writeTest(t, journal, input)
			action, found, err := journal.Get(context.Background(), receipt.ActionID)
			if err != nil || !found || action["agent"] != "SP-7" {
				t.Fatalf("call label changed: %v %v", action, err)
			}
			metadata := object(action["result_metadata"])
			if scenario.field == "job_agent" {
				if metadata["job_agent"] != "SP-3" {
					t.Fatalf("original job label lost: %v", metadata)
				}
			} else if items := array(metadata[scenario.field]); len(items) != 1 || object(items[0])["agent"] != "SP-3" {
				t.Fatalf("original job label lost: %v", metadata)
			}
		})
	}
}
