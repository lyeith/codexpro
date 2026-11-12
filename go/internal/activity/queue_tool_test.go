package activity

import (
	"context"
	"strings"
	"testing"
)

func TestQueueToolActionUsesRetainedCommandCountsAndJobLinks(t *testing.T) {
	h := testHandler(t, nil)
	for _, tool := range []string{"start_jobs", "queue_jobs"} {
		t.Run(tool, func(t *testing.T) {
			id := record(t, h, tool, "evidence-"+tool, map[string]any{"commands": []any{map[string]any{"command": "printf '<b>script</b>'"}}, "queue_key": "PRIVATE-QUEUE-KEY"}, map[string]any{"jobs": []any{map[string]any{"job_id": "job_deadbeef", "status": "queued", "output_complete": false}}, "job_ids": []any{"job_deadbeef"}, "queued_count": 1, "running_count": 0})
			action, found, err := h.options.Audit.GetDashboard(context.Background(), id)
			if err != nil || !found {
				t.Fatal("missing retained action", err)
			}
			view := h.actionView(action, "alpha", "Alpha", "recorded")
			facts := maps(view["facts"])
			seenQueued, seenJobs := false, false
			for _, fact := range facts {
				if fact["label"] == "Queued" && fact["value"] == "1" {
					seenQueued = true
				}
				if fact["label"] == "Jobs" && fact["value"] == "1" {
					seenJobs = true
				}
			}
			if !seenQueued || !seenJobs {
				t.Fatalf("job queue facts missing: %#v", facts)
			}
			html := renderAction(view)
			for _, want := range []string{"Tool call succeeded", "printf &#39;&lt;b&gt;script&lt;/b&gt;&#39;", "job_deadbeef", "/activity/job?job_id=job_deadbeef&amp;workspace_id=ws_alpha", `class="status warn">queued`} {
				if !strings.Contains(html, want) {
					t.Fatalf("missing retained queue evidence %q in %s", want, html)
				}
			}
			for _, bad := range []string{"PRIVATE-QUEUE-KEY", "<b>script</b>"} {
				if strings.Contains(html, bad) {
					t.Fatalf("render leaked raw/private input %q", bad)
				}
			}
		})
	}
}

func TestQueueToolHistoricalGapsRemainExplicit(t *testing.T) {
	action := map[string]any{"toolName": "queue_jobs"}
	if !strings.Contains(renderScripts(action), "predates exact-command capture") {
		t.Fatal("missing-script queue receipt became silent")
	}
	if !strings.Contains(renderJobEvidence(action), "Per-job details were not retained") {
		t.Fatal("missing per-job queue receipt became silent")
	}
	action["shellScripts"] = []any{map[string]any{"script": "printf retained", "truncated": true, "operationId": "job_deadbeef"}}
	if !strings.Contains(renderScripts(action), "truncated at journal limit") {
		t.Fatal("retained truncation explanation missing")
	}
}
