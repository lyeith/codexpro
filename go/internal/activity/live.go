package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func asMap(value any) map[string]any {
	body, _ := json.Marshal(value)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return out
}
func (h *Handler) CollectLive(projectID string) map[string]any {
	now := h.options.Clock()
	calls, jobs := []map[string]any{}, []map[string]any{}
	for _, call := range h.options.Audit.InFlight() {
		if projectID != "" && call.ProjectID != projectID {
			continue
		}
		view := asMap(call)
		label := "Server / unattributed"
		if p, ok := h.project(call.ProjectID); ok {
			label = p.Label
		}
		view["projectLabel"] = label
		view["elapsedMs"] = age(now, call.StartedAt)
		calls = append(calls, view)
	}
	if h.options.Jobs != nil {
		for _, job := range h.options.Jobs.Snapshot() {
			if job.Status != "running" {
				continue
			}
			project, ok := h.jobProject(job)
			if !ok || projectID != "" && project.ID != projectID {
				continue
			}
			jobs = append(jobs, map[string]any{"jobId": job.ID, "workspaceId": job.WorkspaceID, "projectId": project.ID, "projectLabel": project.Label, "startedAt": job.StartedAt, "elapsedMs": max(int64(0), now.UnixMilli()-job.StartedAtMS), "origin": job.Origin, "command": wire.Redact(job.Command), "cwd": job.Cwd, "deadlineAt": timestamp(time.UnixMilli(job.DeadlineMS))})
		}
	}
	return map[string]any{"generatedAt": timestamp(now), "calls": calls, "jobs": jobs}
}
func renderLive(live map[string]any) string {
	calls, jobs := array(live["calls"]), array(live["jobs"])
	var callHTML, jobHTML strings.Builder
	for _, raw := range calls {
		c := object(raw)
		state := "In flight"
		if c["state"] == "queued" {
			state = "Waiting for workspace"
		}
		workspace := stringValue(c["workspaceId"])
		if workspace == "" {
			workspace = "server"
		}
		fmt.Fprintf(&callHTML, `<details class="action-card" data-live-id="%s"><summary><div class="action-summary-main"><div class="action-title"><code class="tool-badge">%s</code><strong>%s</strong></div><div class="action-subtitle">%s elapsed · %s</div></div><span class="status warn">%s</span></summary><div class="action-body"><p class="privacy-note">Issued %s. Awaiting tool result.</p>`, escape(c["actionId"]), escape(c["toolName"]), escape(c["projectLabel"]), escape(humanDuration(number(c["elapsedMs"]))), escape(workspace), state, escape(c["startedAt"]))
		for _, raw := range array(c["shellScripts"]) {
			s := object(raw)
			title := "Shell script"
			if id := stringValue(s["operation_id"]); id != "" {
				title += " · " + id
			}
			note := "recorded command"
			if boolean(s["truncated"]) {
				note = "truncated at journal limit"
			}
			fmt.Fprintf(&callHTML, `<section class="shell-script"><div class="shell-script-head"><h4>%s</h4><span>%s</span></div><pre>%s</pre></section>`, escape(title), note, escape(s["script"]))
		}
		fields := []map[string]any{}
		for key, value := range object(c["requestMetadata"]) {
			if strings.Contains(key, "digest") || strings.Contains(key, "fingerprint") {
				continue
			}
			v := stringOf(value)
			if a := array(value); a != nil {
				parts := []string{}
				for _, item := range a {
					parts = append(parts, stringOf(item))
				}
				v = strings.Join(parts, ", ")
			}
			fields = append(fields, map[string]any{"key": key, "label": strings.ReplaceAll(key, "_", " "), "value": v})
		}
		callHTML.WriteString(renderFields("Request details", fields))
		callHTML.WriteString("</div></details>")
	}
	for _, raw := range jobs {
		job := object(raw)
		params := url.Values{"job_id": {stringValue(job["jobId"])}, "workspace_id": {stringValue(job["workspaceId"])}}
		fmt.Fprintf(&jobHTML, `<details class="action-card" data-live-id="%s"><summary><div class="action-summary-main"><div class="action-title"><code class="tool-badge">%s</code><strong>%s</strong></div><div class="action-subtitle">%s · %s · %s elapsed</div></div><span class="status warn">Running</span></summary><div class="action-body"><p class="privacy-note">Started %s · deadline %s · working directory %s</p><section class="shell-script"><div class="shell-script-head"><h4>Shell script</h4></div><pre>%s</pre></section><div data-refresh-preserve data-job-href="/activity/job?%s"><button type="button" class="button" data-job-refresh>Refresh output</button><div class="job-live">Expand to load current output.</div></div></div></details>`, escape(job["jobId"]), escape(job["origin"]), escape(strings.Split(stringValue(job["command"]), "\n")[0]), escape(job["projectId"]), escape(job["jobId"]), escape(humanDuration(number(job["elapsedMs"]))), escape(job["startedAt"]), escape(job["deadlineAt"]), escape(job["cwd"]), escape(job["command"]), escape(params.Encode()))
	}
	result := fmt.Sprintf(`<div class="section-heading" data-live-generated-at="%s"><h2>In flight</h2><span>%d tool calls · %d running jobs</span></div><p class="privacy-note">Calls are awaiting a result; jobs are running processes. A Bash call and its process may appear in both. Updated every 5 seconds.</p><div class="live-columns">`, escape(live["generatedAt"]), len(calls), len(jobs))
	if callHTML.Len() > 0 {
		result += `<section data-refresh-key="live-calls"><h3 class="live-heading">Tool calls</h3><div class="live-list">` + callHTML.String() + `</div></section>`
	}
	if jobHTML.Len() > 0 {
		result += `<section data-refresh-key="live-jobs"><h3 class="live-heading">Jobs</h3><div class="live-list">` + jobHTML.String() + `</div></section>`
	}
	result += "</div>"
	if len(calls) == 0 && len(jobs) == 0 {
		result += `<p class="empty">No calls or jobs in flight.</p>`
	}
	return result
}

var jobIDPattern = regexp.MustCompile("^job_[a-f0-9]{8}$")

func (h *Handler) JobFragment(ctx context.Context, id, workspace string) (string, error) {
	if !jobIDPattern.MatchString(id) || workspace == "" || len(workspace) > 160 || h.options.Jobs == nil {
		return "", fmt.Errorf("Invalid job reference")
	}
	job, ok := h.options.Jobs.Get(id, workspace)
	if !ok {
		return "", fmt.Errorf("Job not found")
	}
	if _, ok = h.jobProject(job); !ok {
		return "", fmt.Errorf("Job project is no longer in the catalog")
	}
	output, err := h.options.Jobs.ReadTail(ctx, id, workspace, 16384)
	if err != nil {
		return "", err
	}
	metadata := h.options.Jobs.OutputMetadata(job)
	state := escape(job.ID) + " · " + escape(job.Status)
	if job.ExitCode != nil {
		state += " · exit " + escape(*job.ExitCode)
	}
	if job.Signal != "" {
		state += " · " + escape(job.Signal)
	}
	if job.StopReason != "" {
		state += " · " + escape(job.StopReason)
	}
	availability := "Output expired under retention"
	if boolean(metadata["output_available"]) {
		availability = "Retained output available"
	}
	truncation := ""
	if output.Truncated {
		truncation = " · truncated to the last 16,384 bytes per stream"
	}
	stdout, stderr := output.Stdout, output.Stderr
	if stdout == "" {
		stdout = "(no stdout)"
	}
	if stderr == "" {
		stderr = "(no stderr)"
	}
	return fmt.Sprintf(`<section data-job-status="%s"><h4>Current job state · %s</h4><p>%s</p><p>%s · %v rendered bytes.</p><p>Output tail: %d stdout bytes · %d stderr bytes%s. Reading does not acknowledge completion.</p><h4>stdout</h4><pre>%s</pre><h4>stderr</h4><pre>%s</pre></section>`, escape(job.Status), escape(timestamp(h.options.Clock())), state, availability, number(metadata["available_stdout_bytes"])+number(metadata["available_stderr_bytes"]), output.StdoutBytes, output.StderrBytes, truncation, escape(stdout), escape(stderr)), nil
}
