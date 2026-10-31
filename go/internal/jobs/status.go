package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// AttachStatus is the legacy non-job-tool digest. Activity routes deliberately
// use Snapshot instead: a dashboard GET must never acknowledge a completion.
// This method shares acknowledgement state across all MCP sessions/principals.
func (s *Service) AttachStatus(name string, args map[string]any, result wire.Result) wire.Result {
	switch name {
	case "bash", "start_jobs", "jobs", "stop_jobs", "codexpro":
		return result
	}
	// Serialize selection plus acknowledgement so concurrent ordinary tool
	// completions do not both publish the same unacknowledged finished job.
	s.admission.Lock()
	defer s.admission.Unlock()
	workspace := wire.String(args, "workspace_id", "")
	snapshot := s.Snapshot()
	entries := make([]Record, 0, 8)
	for _, r := range snapshot {
		if r.Origin == "foreground" || (workspace != "" && r.WorkspaceID != workspace) {
			continue
		}
		if r.Status == "running" {
			entries = append(entries, r)
			if len(entries) == 8 {
				break
			}
		}
	}
	if len(entries) < 8 {
		for _, r := range snapshot {
			if r.Origin == "foreground" || r.Status == "running" || r.Acknowledged || (workspace != "" && r.WorkspaceID != workspace) {
				continue
			}
			entries = append(entries, r)
			if len(entries) == 8 {
				break
			}
		}
	}
	if len(entries) == 0 {
		return result
	}
	now := time.Now().UnixMilli()
	descriptions := make([]string, len(entries))
	rows := make([]map[string]any, len(entries))
	finished := []string{}
	for i, r := range entries {
		end := r.FinishedAtMS
		if end == 0 {
			end = now
		}
		elapsed := end - r.StartedAtMS
		if r.Status == "running" {
			descriptions[i] = fmt.Sprintf("%s (%s) running %s", r.ID, r.CommandLabel, elapsedLabel(elapsed))
		} else {
			exit := ""
			if r.ExitCode != nil {
				exit = fmt.Sprintf(" exit %d", *r.ExitCode)
			}
			descriptions[i] = fmt.Sprintf("%s (%s) %s%s after %s", r.ID, r.CommandLabel, r.Status, exit, elapsedLabel(elapsed))
			finished = append(finished, r.ID)
		}
		rows[i] = map[string]any{"job_id": r.ID, "status": r.Status, "command": r.CommandLabel, "elapsed_ms": elapsed, "exit_code": r.ExitCode, "workspace_id": r.WorkspaceID}
	}
	line := "Background jobs: " + strings.Join(descriptions, "; ") + ". Collect with jobs(job_ids=[...], wait_ms)."
	data := make(map[string]any, len(result.StructuredContent)+1)
	for key, value := range result.StructuredContent {
		data[key] = value
	}
	data["background_jobs"] = rows
	result.StructuredContent = data
	if result.Content != nil {
		content := make([]wire.Content, len(result.Content))
		found := false
		for i, block := range result.Content {
			copy := make(wire.Content, len(block))
			for key, value := range block {
				copy[key] = value
			}
			content[i] = copy
			if !found && block["type"] == "text" {
				if text, ok := block["text"].(string); ok {
					copy["text"] = text + "\n\n" + line
					found = true
				}
			}
		}
		if !found {
			content = append(content, wire.Content{"type": "text", "text": line})
		}
		result.Content = content
	}
	// A storage failure leaves the event eligible for a later digest. Reporting a
	// duplicate is safer than claiming an acknowledgement that was not durable.
	_ = s.acknowledge(finished)
	return result
}
