package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestQueueToolRetainsSafeLaunchMetadataAndPrivateScripts(t *testing.T) {
	args := map[string]any{"commands": []any{map[string]any{"command": "printf PRIVATE-COMMAND"}}, "session_id": "PRIVATE-SESSION", "queue_key": "PRIVATE-QUEUE-KEY", "queue_timeout_ms": 1000}
	result := wire.Success("queue_jobs", "Queue Jobs", map[string]any{"queued_count": 1, "running_count": 0, "job_ids": []any{"job_deadbeef"}, "jobs": []any{map[string]any{"job_id": "job_deadbeef", "status": "queued", "stdout": "PRIVATE-STDOUT", "stderr": "PRIVATE-STDERR", "output_complete": false}}}, "")
	if !reflect.DeepEqual(summarizeArgs("queue_jobs", args), summarizeArgs("start_jobs", args)) {
		t.Fatal("queue command summary diverged from bounded launch summary")
	}
	if !reflect.DeepEqual(summarizeResult("queue_jobs", result), summarizeResult("start_jobs", result)) {
		t.Fatal("queue job metadata lost the ordinary bounded projection")
	}
	at := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	j := &Journal{options: Options{Clock: func() time.Time { return at }, Redact: func(s string) string { return strings.ReplaceAll(s, "PRIVATE-COMMAND", "[redacted]") }}, serverSessionRef: "srv_fixture"}
	action, err := j.build(RecordInput{ToolName: "queue_jobs", Args: args, Result: result, Target: &Target{Scope: "workspace", ProjectID: "alpha", WorkspaceID: "own"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if action["operation"] != "job.queue" || action["operation_class"] != "execute" || action["mutating"] != true || action["status"] != "succeeded" {
		t.Fatalf("queue call has wrong runtime classification: %#v", action)
	}
	metadata := object(action["result_metadata"])
	if number(metadata["queued_count"]) != 1 || number(metadata["running_count"]) != 0 || object(array(metadata["jobs"])[0])["status"] != "queued" {
		t.Fatalf("queue admission was not preserved separately from running: %#v", metadata)
	}
	scripts := array(object(action["dashboard_metadata"])["shell_scripts"])
	if len(scripts) != 1 || object(scripts[0])["operation_id"] != "job_deadbeef" || object(scripts[0])["script"] != "printf [redacted]" {
		t.Fatalf("existing exact-command redaction/link capture not applied: %#v", scripts)
	}
	body, err := json.Marshal(public(action))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE-COMMAND", "PRIVATE-SESSION", "PRIVATE-QUEUE-KEY", "PRIVATE-STDOUT", "PRIVATE-STDERR", "shell_scripts"} {
		if strings.Contains(string(body), private) {
			t.Fatalf("public metadata leaked %s", private)
		}
	}
}

func TestQueueToolUsesExistingScriptAndJobEvidenceBounds(t *testing.T) {
	commands, results := []any{}, []any{}
	for i := 0; i < 33; i++ {
		commands = append(commands, map[string]any{"command": "printf ordinary"})
		results = append(results, map[string]any{"job_id": fmt.Sprintf("job_%08x", i+1), "status": "queued"})
	}
	result := wire.Success("queue_jobs", "Queue Jobs", map[string]any{"jobs": results, "queued_count": 33}, "")
	summary := summarizeResult("queue_jobs", result)
	if number(summary["jobs_count"]) != 33 || len(array(summary["jobs"])) != 32 || summary["jobs_truncated"] != true {
		t.Fatalf("job evidence lost the existing 32-item bound: %#v", summary)
	}
	input := RecordInput{ToolName: "queue_jobs", Args: map[string]any{"commands": commands}, Result: result}
	scripts := dashboardScripts(input)
	if len(scripts) != 32 {
		t.Fatalf("command evidence is not capped at 32: %d", len(scripts))
	}
	commands[0] = map[string]any{"command": strings.Repeat("λ", 40000)}
	scripts = dashboardScripts(input)
	if len(scripts) != 1 {
		t.Fatalf("unexpected bounded-script tail: %d", len(scripts))
	}
	script := text(scripts[0]["script"])
	if len([]byte(script)) != 64*1024 || !utf8.ValidString(script) || scripts[0]["truncated"] != true {
		t.Fatal("script byte/UTF-8 truncation contract changed")
	}
	input.ToolName = "start_jobs"
	if !reflect.DeepEqual(scripts, dashboardScripts(input)) {
		t.Fatal("queue and ordinary launch use different command bounds")
	}
}

func TestQueueToolProjectReceiptsKeepWorkspaceAndProjectionFences(t *testing.T) {
	ids := map[string]bool{"own": true}
	for _, tool := range []string{"start_jobs", "queue_jobs"} {
		own := Action{"tool_name": tool, "project_id": "alpha", "workspace_id": "own"}
		if !ProjectReceiptVisible(own, ids) {
			t.Fatalf("single-workspace receipt missing for %s", tool)
		}
		for _, bad := range []Action{{"tool_name": tool, "project_id": "alpha", "workspace_id": "foreign"}, {"tool_name": tool, "workspace_id": "own"}, {"tool_name": tool, "project_id": "alpha", "workspace_id": "own", "audit_scope": "unattributed"}} {
			if ProjectReceiptVisible(bad, ids) {
				t.Fatalf("ambiguous/unowned receipt admitted: %#v", bad)
			}
		}
	}
	j := openTest(t, testOptions(t.TempDir()))
	input := recordInput("alpha", "queue-receipt")
	input.ToolName = "queue_jobs"
	input.Target = &Target{Scope: "workspace", ProjectID: "alpha", WorkspaceID: "own"}
	input.Args = map[string]any{"commands": []any{map[string]any{"command": "printf PRIVATE-COMMAND"}}, "queue_key": "PRIVATE-QUEUE-KEY"}
	receipt := writeTest(t, j, input)
	page, err := j.ProjectReceipts(context.Background(), "alpha", "", ids, 1)
	if err != nil || len(page.Actions) != 1 || page.Actions[0]["action_id"] != receipt.ActionID || len(page.Actions[0]) != 5 {
		t.Fatalf("queue receipt did not use minimal project projection: %#v %v", page, err)
	}
	body, _ := json.Marshal(page.Actions)
	if strings.Contains(string(body), "PRIVATE") {
		t.Fatal("project receipt disclosed command or queue-key metadata")
	}
}
