package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// AttachStatus is the legacy non-job-tool digest. Activity routes deliberately
// use Snapshot instead: a dashboard GET must never acknowledge a completion.
// A session can acknowledge only jobs attributed to the same caller.
func (s *Service) AttachStatus(identity wire.Identity, name string, args map[string]any, result wire.Result) wire.Result {
	switch name {
	case "bash", "start_jobs", "queue_jobs", "jobs", "stop_jobs", "codexpro":
		return result
	}
	// Serialize selection plus acknowledgement so concurrent ordinary tool
	// completions do not both publish the same unacknowledged finished job.
	s.admission.Lock()
	defer s.admission.Unlock()
	workspace := wire.String(args, "workspace_id", "")
	selected, count := s.statusCandidates(identity, workspace)
	entries := selected[:count]
	if len(entries) == 0 {
		return result
	}
	now := time.Now().UnixMilli()
	descriptions := make([]string, len(entries))
	rows := make([]map[string]any, len(entries))
	finished := []string{}
	for i, published := range entries {
		// Detach exposed pointers (notably exit_code) from immutable state.
		r := copyRecord(*published)
		end := r.FinishedAtMS
		if end == 0 {
			end = now
		}
		elapsed := end - r.StartedAtMS
		if unfinished(r) {
			descriptions[i] = fmt.Sprintf("%s (%s) %s %s", r.ID, r.CommandLabel, r.Status, elapsedLabel(elapsed))
		} else {
			exit := ""
			if r.ExitCode != nil {
				exit = fmt.Sprintf(" exit %d", *r.ExitCode)
			}
			descriptions[i] = fmt.Sprintf("%s (%s) %s%s after %s", r.ID, r.CommandLabel, r.Status, exit, elapsedLabel(elapsed))
			finished = append(finished, r.ID)
		}
		rows[i] = map[string]any{"job_id": r.ID, "status": r.Status, "command": r.CommandLabel, "elapsed_ms": elapsed, "exit_code": r.ExitCode, "workspace_id": r.WorkspaceID}
		if r.Agent != "" {
			rows[i]["agent"] = r.Agent
		}
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

// statusCandidates reads immutable published records without cloning or sorting
// retained history. The caller holds admission through rendering and durable
// acknowledgement; mu only protects map membership during this bounded selection.
func (s *Service) statusCandidates(identity wire.Identity, workspace string) ([8]*Record, int) {
	var selected [8]*Record
	count := 0
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.records {
		r := entry.published.Load()
		if r == nil || !r.sameCaller(identity) || r.Origin == "foreground" ||
			(workspace != "" && r.WorkspaceID != workspace) || (!unfinished(*r) && r.Acknowledged) {
			continue
		}
		at := count
		for at > 0 && statusBefore(r, selected[at-1]) {
			at--
		}
		if at == len(selected) {
			continue
		}
		if count < len(selected) {
			count++
		}
		copy(selected[at+1:count], selected[at:count-1])
		selected[at] = r
	}
	return selected, count
}

// Preserve the digest's running, queued, then terminal priority, newest first
// within each group. Equal start timestamps have no guaranteed ordering.
func statusBefore(a, b *Record) bool {
	if (a.Status == "running") != (b.Status == "running") {
		return a.Status == "running"
	}
	if (a.Status == "queued") != (b.Status == "queued") {
		return a.Status == "queued"
	}
	return a.StartedAtMS > b.StartedAtMS
}
