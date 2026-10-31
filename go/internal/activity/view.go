package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lyeith/codexpro/go/internal/usage"
)

func maps(v any) []map[string]any {
	out := []map[string]any{}
	for _, v := range array(v) {
		out = append(out, object(v))
	}
	return out
}
func stringsOf(v any) []string {
	out := []string{}
	for _, v := range array(v) {
		out = append(out, stringOf(v))
	}
	return out
}
func statusTone(status string) string {
	if status == "succeeded" {
		return "good"
	}
	if status == "cancelled" || status == "blocked" {
		return "warn"
	}
	return "bad"
}

var longRunID = regexp.MustCompile("^run_[A-Za-z0-9_-]{13,}$")

func renderRunID(value string) string {
	if !longRunID.MatchString(value) {
		return escape(value)
	}
	short := value[:12] + "…" + value[len(value)-4:]
	return `<span class="run-reference"><button type="button" class="copy-run-id" data-copy-run-id="` + escape(value) + `" title="` + escape(value) + `" aria-label="Copy full run ID ` + escape(value) + `"><code>` + escape(short) + `</code><span aria-hidden="true">⧉</span></button><span class="copy-run-status" role="status"></span></span>`
}
func renderFields(title string, fields []map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	var body strings.Builder
	for _, field := range fields {
		class := ""
		if boolean(field["mono"]) {
			class = "mono-value"
		}
		class += " " + stringValue(field["tone"])
		value := escape(field["value"])
		if field["key"] == "run_id" {
			value = renderRunID(stringValue(field["value"]))
		}
		fmt.Fprintf(&body, `<div><dt>%s</dt><dd class="%s">%s</dd></div>`, escape(field["label"]), escape(class), value)
	}
	return `<section class="action-section"><h4>` + escape(title) + `</h4><dl class="field-grid">` + body.String() + `</dl></section>`
}
func renderFacts(facts []map[string]any) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		value := escape(f["value"])
		if f["label"] == "Run" {
			value = renderRunID(stringValue(f["value"]))
		}
		fmt.Fprintf(&b, `<div><dt>%s</dt><dd class="%s">%s</dd></div>`, escape(f["label"]), escape(f["tone"]), value)
	}
	return `<dl class="fact-row">` + b.String() + `</dl>`
}
func renderEvidence(title string, items []map[string]any) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, `<div><dt>%s</dt><dd>%s</dd></div>`, escape(item["label"]), escape(item["value"]))
	}
	return `<section class="action-section"><h4>` + escape(title) + `</h4><dl class="evidence-list">` + b.String() + `</dl></section>`
}
func gitEvidenceText(v map[string]any) string {
	parts := []string{}
	if branch := stringValue(v["branch"]); branch != "" {
		parts = append(parts, branch)
	}
	if head := stringValue(v["head"]); head != "" {
		if len(head) > 12 {
			head = head[:12]
		}
		parts = append(parts, head)
	}
	identity := strings.Join(parts, " @ ")
	if identity == "" {
		identity = "No Git identity"
	}
	state := "clean"
	if boolean(v["dirty"]) {
		state = plural(number(v["changedPathCount"]), "changed path")
	}
	return identity + " · " + state
}
func renderActionGit(a map[string]any) string {
	before, after := object(a["gitBefore"]), object(a["gitAfter"])
	if len(before) == 0 && len(after) == 0 {
		return ""
	}
	body := ""
	if len(before) > 0 {
		body += `<div><span>Before</span><code>` + escape(gitEvidenceText(before)) + `</code></div>`
	}
	if len(after) > 0 {
		body += `<div><span>After</span><code>` + escape(gitEvidenceText(after)) + `</code></div>`
	}
	return `<section class="action-section"><h4>Git evidence</h4><div class="git-transition">` + body + `</div></section>`
}
func renderPaths(title string, paths []string, tone, note string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, `<code class="path %s">%s</code>`, escape(tone), escape(p))
	}
	extra := ""
	if note != "" {
		extra = " <span>" + escape(note) + "</span>"
	}
	return `<section class="action-section"><h4>` + escape(title) + extra + `</h4><div class="path-list">` + b.String() + `</div></section>`
}
func renderScripts(a map[string]any) string {
	scripts := maps(a["shellScripts"])
	if len(scripts) == 0 {
		if a["toolName"] == "bash" || a["toolName"] == "bash_job" || a["toolName"] == "start_jobs" {
			return `<p class="privacy-note"><strong>Shell script unavailable:</strong> this retained action predates exact-command capture.</p>`
		}
		return ""
	}
	var b strings.Builder
	for i, s := range scripts {
		title := "Shell script"
		if id := stringValue(s["operationId"]); id != "" {
			title += " · " + id
		} else if len(scripts) > 1 {
			title += fmt.Sprintf(" %d", i+1)
		}
		note := "recorded command"
		if boolean(s["truncated"]) {
			note = "truncated at journal limit"
		}
		fmt.Fprintf(&b, `<section class="shell-script"><div class="shell-script-head"><h4>%s</h4><span>%s</span></div><pre>%s</pre></section>`, escape(title), note, escape(s["script"]))
	}
	return b.String()
}
func renderJobEvidence(a map[string]any) string {
	jobs := maps(a["jobs"])
	if len(jobs) == 0 {
		if a["toolName"] == "start_jobs" || a["toolName"] == "jobs" || a["toolName"] == "stop_jobs" {
			return `<p class="privacy-note">Per-job details were not retained for this historical action.</p>`
		}
		return ""
	}
	heading := "Job outcomes at snapshot time"
	if boolean(a["jobDetailsTruncated"]) {
		heading += " (list truncated)"
	}
	var b strings.Builder
	for _, job := range jobs {
		id := stringValue(job["job_id"])
		params := url.Values{"job_id": {id}, "workspace_id": {stringValue(a["workspaceId"])}}
		state, tone := stringValue(job["status"]), ""
		if state == "succeeded" {
			tone = "good"
		} else if state == "running" {
			tone = "warn"
		} else if state != "" {
			tone = "bad"
		} else {
			state = "not recorded"
		}
		detail := ""
		if has(job, "exit_code") {
			detail += " · exit " + escape(job["exit_code"])
		}
		if reason := stringValue(job["stop_reason"]); reason != "" {
			detail += " · " + escape(reason)
		}
		fields := []map[string]any{}
		keys := []string{}
		for key, value := range job {
			if key != "job_id" && value != nil {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			fields = append(fields, map[string]any{"key": key, "label": strings.ReplaceAll(key, "_", " "), "value": stringOf(job[key])})
		}
		fmt.Fprintf(&b, `<div class="job-evidence" data-refresh-preserve data-job-href="/activity/job?%s"><p><code>%s</code> · <span class="status %s">%s</span>%s</p>%s<button type="button" class="button" data-job-refresh>Refresh current state and output</button><div class="job-live"><p class="privacy-note">Current output is fetched on expansion. It is separate from this historical receipt.</p></div></div>`, escape(params.Encode()), escape(id), tone, escape(state), detail, renderFields("Recorded job details", fields))
	}
	return `<section class="action-section"><h4>` + heading + `</h4>` + b.String() + `</section>`
}
func renderChildResults(a map[string]any) string {
	if a["toolName"] != "batch" {
		return ""
	}
	children := maps(a["childResults"])
	if len(children) == 0 {
		return `<p class="privacy-note">Historical per-operation results were not retained. A saved definition is not an execution result.</p>`
	}
	heading := "Historical operation outcomes"
	if boolean(a["childResultsTruncated"]) {
		heading += " (list truncated)"
	}
	var b strings.Builder
	for _, child := range children {
		state := "tool call failed"
		if boolean(child["skipped"]) {
			state = "skipped"
		} else if boolean(child["ok"]) {
			state = "tool call succeeded"
		}
		detail := ""
		if has(child, "exit_code") {
			detail += " · exit " + escape(child["exit_code"])
		}
		if has(child, "error_code") {
			detail += " · " + escape(child["error_code"])
		}
		if boolean(child["text_truncated"]) || boolean(child["structured_truncated"]) {
			detail += " · result truncated"
		}
		if has(child, "commit") {
			detail += " · commit " + escape(child["commit"])
		}
		if has(child, "path") {
			detail += " · " + escape(child["path"])
		}
		if has(child, "bytes") {
			detail += " · " + escape(child["bytes"]) + " bytes"
		}
		fmt.Fprintf(&b, `<p><code>%s · %s</code> · %s%s</p>`, escape(child["id"]), escape(child["tool"]), state, detail)
		if id := stringValue(child["job_id"]); id != "" {
			copy := map[string]any{}
			for k, v := range a {
				copy[k] = v
			}
			copy["toolName"] = "bash"
			copy["jobs"] = []map[string]any{{"job_id": id, "status": child["job_status"], "exit_code": child["exit_code"]}}
			b.WriteString(renderJobEvidence(copy))
		}
	}
	return `<section class="action-section"><h4>` + heading + `</h4>` + b.String() + `</section>`
}
func renderAction(a map[string]any) string {
	notes := []string{}
	if number(a["hiddenPathCount"]) > 0 {
		notes = append(notes, plural(number(a["hiddenPathCount"]), "blocked path")+" hidden")
	}
	if boolean(a["changedPathsTruncated"]) {
		notes = append(notes, "list truncated")
	}
	changed := stringsOf(a["changedPaths"])
	changedHTML := renderPaths("Changed", changed, "tracked", strings.Join(notes, " · "))
	readHTML := ""
	if len(changed) == 0 {
		title := "Read"
		if a["operationClass"] == "analysis" {
			title = "Analysed"
		}
		readHTML = renderPaths(title, stringsOf(a["readPaths"]), "read", "")
	}
	batch := ""
	if href, p := stringValue(a["batchHref"]), stringValue(a["batchPath"]); href != "" && p != "" {
		batch = `<section class="action-section batch-inline" data-refresh-preserve data-batch-href="` + escape(href) + `" data-batch-state="idle"><h4>Saved batch definition <code>` + escape(p) + `</code> <a href="` + escape(href) + `" data-local-link target="_blank" rel="noopener">open in new tab ↗</a></h4><div class="batch-inline-body"><p class="empty">Loading batch…</p></div></section>`
	}
	diagnostic := ""
	if stringValue(a["errorCode"]) != "" || stringValue(a["errorMessage"]) != "" {
		diagnostic = `<p class="error-note"><strong>Error:</strong> <code>` + escape(a["errorCode"]) + `</code>`
		if has(a, "errorMessage") {
			diagnostic += " · " + escape(a["errorMessage"])
		}
		diagnostic += "</p>"
		if has(a, "recoveryMessage") {
			diagnostic += `<p class="privacy-note">Recovery: ` + escape(a["recoveryMessage"]) + `</p>`
		}
	}
	attribution := ""
	if a["attribution"] == "recovered" {
		attribution = `<span class="attribution" title="Project inferred from the workspace id">recovered</span>`
	} else if a["attribution"] == "unknown" {
		attribution = `<span class="attribution" title="Project id is not in the catalog">unknown project</span>`
	}
	mutation := ""
	if boolean(a["mutating"]) {
		mutation = `<span class="mutating">may modify files</span>`
	}
	status := stringValue(a["status"])
	if a["toolName"] == "jobs" || a["toolName"] == "start_jobs" || a["toolName"] == "stop_jobs" {
		status = "Tool call " + status
	}
	started := ""
	if has(a, "startedAt") {
		started = `Started: <time datetime="` + escape(a["startedAt"]) + `" data-local-time>` + escape(a["startedAt"]) + `</time> · `
	}
	withoutFingerprints := func(fields []map[string]any) []map[string]any {
		out := []map[string]any{}
		for _, f := range fields {
			key := stringValue(f["key"])
			if !strings.Contains(key, "digest") && !strings.Contains(key, "fingerprint") {
				out = append(out, f)
			}
		}
		return out
	}
	workspace := ""
	if has(a, "workspaceId") {
		workspace = "<code>" + escape(a["workspaceId"]) + "</code>"
	}
	return fmt.Sprintf(`<details class="action-card" data-action-id="%s" data-class="%s"><summary title="%s"><div class="action-time"><time datetime="%s" data-local-time>%s</time><span>#%s</span></div><div class="action-summary-main"><div class="action-title"><code class="tool-badge">%s</code><strong>%s</strong></div><div class="action-subtitle"><span class="class-tag">%s</span><span>%s</span>%s%s</div></div><span class="status %s">%s</span></summary><div class="action-body">%s<p class="privacy-note">%sHistorical snapshot: <time datetime="%s" data-local-time>%s</time></p>%s%s%s%s%s%s<details class="recorded-metadata"><summary>More recorded details</summary>%s%s</details>%s%s%s<p class="action-identity"><code>%s</code>%s</p></div></details>`,
		escape(a["actionId"]), escape(a["operationClass"]), escape(stringValue(a["operation"])+" · action "+stringValue(a["actionId"])), escape(a["finishedAt"]), escape(a["finishedAt"]), escape(a["sequence"]), escape(a["toolName"]), escape(a["headline"]), escape(a["operationClass"]), escape(humanDuration(number(a["durationMs"]))), mutation, attribution, statusTone(stringValue(a["status"])), escape(status), renderFacts(maps(a["facts"])), started, escape(a["finishedAt"]), escape(a["finishedAt"]), renderScripts(a), diagnostic, renderJobEvidence(a), renderChildResults(a), renderActionGit(a), renderEvidence("File evidence", maps(a["pathEvidence"])), renderFields("Recorded request details", withoutFingerprints(maps(a["requestFields"]))), renderFields("Recorded result details", withoutFingerprints(maps(a["resultFields"]))), changedHTML, readHTML, batch, escape(a["actionId"]), workspace)
}
func renderPathList(title string, paths []string, tone string) string {
	if len(paths) == 0 {
		return ""
	}
	body := ""
	for _, p := range paths {
		body += `<code class="path ` + escape(tone) + `">` + escape(p) + `</code>`
	}
	return `<div class="path-group"><strong>` + escape(title) + `</strong><div class="path-list">` + body + `</div></div>`
}
func renderGit(project map[string]any) string {
	git := object(project["git"])
	if !boolean(git["available"]) {
		message := stringValue(git["message"])
		if message == "" {
			message = "Unavailable"
		}
		return `<section class="git-panel unavailable"><div><strong>Git</strong><span>` + escape(message) + `</span></div></section>`
	}
	if !boolean(git["dirty"]) {
		head := stringValue(git["head"])
		if head == "" {
			head = "no commit"
		}
		at := ""
		if has(git, "committedAt") {
			at = `<time datetime="` + escape(git["committedAt"]) + `" data-local-time>` + escape(git["committedAt"]) + `</time>`
		}
		return `<section class="git-panel clean"><div><strong>Working tree</strong><span>Clean at <code>` + escape(head) + `</code></span></div>` + at + `</section>`
	}
	tracked, untracked := stringsOf(git["trackedChangedPaths"]), stringsOf(git["untrackedPaths"])
	visible := len(tracked) + len(untracked)
	notes := []string{}
	if number(git["hiddenPathCount"]) > 0 {
		notes = append(notes, plural(number(git["hiddenPathCount"]), "safety-blocked path")+" hidden")
	}
	if number(git["omittedPathCount"]) > 0 {
		notes = append(notes, plural(number(git["omittedPathCount"]), "additional path")+" omitted")
	}
	note := ""
	if len(notes) > 0 {
		note = `<p class="safety-note">` + escape(strings.Join(notes, " · ")) + `</p>`
	}
	diff := `<p class="empty">No tracked diff. The working tree contains only untracked or safety-filtered paths.</p>`
	if boolean(git["diffAvailable"]) {
		body, _ := json.Marshal(git)
		hash := sha256.Sum256(body)
		revision := hex.EncodeToString(hash[:])[:16]
		diff = `<div class="git-diff" data-refresh-preserve data-refresh-revision="` + revision + `" data-diff-project="` + escape(project["id"]) + `" data-diff-state="idle"><p class="empty">Loading diff…</p></div>`
	}
	return fmt.Sprintf(`<details class="git-details"><summary><span><strong>Diff from HEAD</strong><small>%s</small></span><span class="delta"><b>+%v</b><b>−%v</b></span></summary><div class="git-body">%s%s%s%s</div></details>`, escape(plural(float64(visible), "visible path")), number(git["additions"]), number(git["deletions"]), renderPathList("Tracked changes", tracked, "tracked"), renderPathList("Untracked files (contents not rendered)", untracked, "untracked"), note, diff)
}
func renderProject(project map[string]any) string {
	git := object(project["git"])
	branch := ""
	if boolean(git["available"]) {
		label := stringValue(git["branch"])
		if label == "" {
			label = "detached"
		}
		branch = `<span class="branch">` + escape(label) + `</span>`
	}
	latest := `<span>No retained activity for this project.</span>`
	if has(project, "latestActivityAt") {
		latest = `<time datetime="` + escape(project["latestActivityAt"]) + `" data-local-time>` + escape(project["latestActivityAt"]) + `</time>`
	}
	actions := maps(project["actions"])
	count := "none retained"
	list := `<p class="empty">No retained activity for this project.</p>`
	if len(actions) > 0 {
		count = fmt.Sprintf("newest %d", len(actions))
		list = `<div class="action-list">`
		for _, a := range actions {
			list += renderAction(a)
		}
		list += "</div>"
	}
	return fmt.Sprintf(`<article class="project-card" data-project="%s"><header class="project-head"><div><span class="project-id">%s</span><h2 title="%s">%s</h2></div><div class="project-meta">%s%s</div></header>%s<section class="activity-block"><div class="section-title"><h3>Latest actions</h3><span>%s</span></div>%s</section></article>`, escape(project["id"]), escape(project["id"]), escape(project["label"]), escape(project["label"]), branch, latest, renderGit(project), count, list)
}
func metricNumber(v any) string {
	if v == nil {
		return "—"
	}
	s := strconv.FormatFloat(number(v), 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	parts := strings.SplitN(s, ".", 2)
	whole := parts[0]
	sign := ""
	if strings.HasPrefix(whole, "-") {
		sign = "-"
		whole = whole[1:]
	}
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	s = sign + whole
	if len(parts) > 1 {
		s += "." + parts[1]
	}
	return s
}
func renderMetrics(snapshot Snapshot) string {
	metrics := snapshot.Metrics
	project := "All projects"
	if id := stringValue(metrics["projectId"]); id != "" {
		project = id
		for _, p := range snapshot.Projects {
			if p["id"] == id {
				project = stringValue(p["label"])
				break
			}
		}
	}
	latency := func(v any) string {
		if v == nil {
			return "—"
		}
		return metricNumber(v) + " ms"
	}
	note := metricNumber(metrics["count"]) + " retained completed calls · " + metricNumber(metrics["failedCount"]) + " unsuccessful (failed, timed out, cancelled or blocked) · latency n=" + metricNumber(metrics["latencySampleCount"]) + ". Includes queue time; excludes background-job completion events. Retention may reduce this sample."
	return fmt.Sprintf(`<section class="tool-metrics" data-dashboard-section="metrics" aria-label="Tool call metrics"><div class="tool-metrics-heading"><h2>Tool calls</h2><p>Last 60 min · retained journal · all outcomes · %s</p></div><div class="tool-metrics-grid"><div class="metric" data-refresh-key="call-rate"><span>Tool calls / hour</span><strong>%s</strong></div><div class="metric" data-refresh-key="call-mean"><span>Average tool-call latency</span><strong>%s</strong></div><div class="metric" data-refresh-key="call-median"><span>Median tool-call latency</span><strong>%s</strong></div></div><p class="tool-metrics-note">%s</p></section>`, escape(project), escape(metricNumber(metrics["callsPerHour"])), escape(latency(metrics["meanLatencyMs"])), escape(latency(metrics["medianLatencyMs"])), escape(note))
}
func renderRecent(actions []map[string]any, controls, navigation string) string {
	var rows strings.Builder
	for _, a := range actions {
		project := stringValue(a["projectId"])
		if project == "" {
			project = "global"
		}
		attribution := ""
		if a["attribution"] == "recovered" {
			attribution = `<span class="recovered">recovered from workspace (best effort)</span>`
		} else if a["attribution"] == "unknown" {
			attribution = `<span class="recovered">id not in catalog</span>`
		}
		fmt.Fprintf(&rows, `<tr class="command-record" data-command-id="%s"><td class="command-when"><time datetime="%s" data-local-time>%s</time><small>#%s</small></td><td class="command-project"><strong title="%s">%s</strong><code title="%s">%s</code>%s</td><td class="command-cell">%s</td></tr>`, escape(a["actionId"]), escape(a["finishedAt"]), escape(a["finishedAt"]), escape(a["sequence"]), escape(a["projectLabel"]), escape(a["projectLabel"]), escape(project), escape(project), attribution, renderAction(a))
	}
	table := `<p class="empty">No retained commands.</p>`
	if rows.Len() > 0 {
		table = `<div class="command-table-scroll"><table class="command-table"><caption class="visually-hidden">Retained CodexPro command history</caption><thead><tr><th scope="col">Time</th><th scope="col">Project</th><th scope="col">Command</th></tr></thead><tbody>` + rows.String() + `</tbody></table></div>`
	}
	return `<section class="dashboard-section recent-panel"><div class="section-heading history-heading"><div><span class="eyebrow">Newest first</span><h2>Command history</h2></div>` + controls + `</div>` + table + navigation + `</section>`
}

type DashboardSection struct {
	Key  string `json:"key"`
	HTML string `json:"html"`
}
type DashboardData struct {
	GeneratedAt string             `json:"generatedAt"`
	Sections    []DashboardSection `json:"sections"`
}

func RenderDashboardData(s Snapshot) DashboardData {
	warning := ""
	if boolean(s.Audit["enabled"]) {
		if boolean(s.Audit["gap_detected"]) || number(s.Audit["malformed_records"]) > 0 {
			gap := ""
			if boolean(s.Audit["gap_detected"]) {
				gap = " and a sequence gap"
			}
			warning = `<div class="banner bad">The action journal reports ` + escape(s.Audit["malformed_records"]) + ` malformed record(s)` + gap + `. Activity may be incomplete.</div>`
		}
	} else {
		warning = `<div class="banner warn">Debug activity is disabled. Git state is available, but recent CodexPro actions require <code>--audit metadata</code>.</div>`
	}
	projectCards := ""
	for _, p := range s.Projects {
		projectCards += renderProject(p)
	}
	if projectCards == "" {
		projectCards = `<div class="banner warn">No runnable projects are configured.</div>`
	}
	history := s.History
	older, newest := url.Values{}, url.Values{}
	if id := stringValue(history["projectId"]); id != "" {
		older.Set("project_id", id)
		newest.Set("project_id", id)
	}
	if has(history, "nextBeforeSequence") {
		older.Set("before_sequence", stringOf(history["nextBeforeSequence"]))
	}
	navigation := fmt.Sprintf(`<nav class="history-navigation" aria-label="Command history pages"><span>%d shown · %v matching retained actions</span><div>`, len(s.RecentActions), number(history["matchedCount"]))
	if has(history, "beforeSequence") {
		navigation += `<a href="/activity?` + escape(newest.Encode()) + `" data-local-link>Newest</a>`
	}
	if has(history, "nextBeforeSequence") {
		navigation += `<a href="/activity?` + escape(older.Encode()) + `" data-local-link>Older actions →</a>`
	}
	navigation += "</div></nav>"
	controls := `<label class="project-filter">Project <select data-project-filter aria-label="Filter command history by project"><option value="">All projects</option>`
	for _, p := range s.Projects {
		selected := ""
		if p["id"] == history["projectId"] {
			selected = " selected"
		}
		controls += `<option value="` + escape(p["id"]) + `"` + selected + `>` + escape(p["label"]) + `</option>`
	}
	controls += "</select></label>"
	summary := fmt.Sprintf(`<section class="summary" aria-label="Activity status" data-dashboard-section="summary"><div class="metric"><span>Projects</span><strong>%d</strong></div><div class="metric"><span>Retained actions</span><strong>%s</strong></div><div class="metric"><span>Latest sequence</span><strong>%s</strong></div><div class="metric"><span>Updated</span><strong><time datetime="%s" data-local-time>%s</time></strong></div></section>`, len(s.Projects), escape(s.Audit["action_count"]), escape(s.Audit["latest_sequence"]), escape(s.GeneratedAt), escape(s.GeneratedAt))
	live := s.Live
	if live == nil {
		live = map[string]any{"generatedAt": s.GeneratedAt, "calls": []any{}, "jobs": []any{}}
	}
	return DashboardData{GeneratedAt: s.GeneratedAt, Sections: []DashboardSection{
		{"summary", summary}, {"metrics", renderMetrics(s)}, {"warnings", `<div data-dashboard-section="warnings">` + warning + `</div>`},
		{"live", `<section class="dashboard-section live-panel" data-live-panel data-dashboard-section="live">` + renderLive(live) + `</section>`},
		{"timeline", `<div data-dashboard-section="timeline">` + renderTimeline(s.Timeline, s.TimelineNote) + `</div>`},
		{"history", `<div data-dashboard-section="history">` + renderRecent(s.RecentActions, controls, navigation) + `</div>`},
		{"projects", fmt.Sprintf(`<section class="dashboard-section project-section" data-dashboard-section="projects"><div class="section-heading"><div><span class="eyebrow">Current repository state</span><h2>Project working trees</h2></div><span>%d configured</span></div><div class="project-grid">%s</div></section>`, len(s.Projects), projectCards)},
	}}
}
func RenderDashboardPage(s Snapshot) string {
	data := RenderDashboardData(s)
	sections := map[string]string{}
	for _, section := range data.Sections {
		sections[section.Key] = section.HTML
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="icon" href="/favicon.ico"><title>CodexPro Activity & Changes</title><style>` + dashboardStyle + `</style></head><body><main><header class="topbar"><div class="brand"><img src="/favicon.ico" alt=""><div><span class="eyebrow">Authenticated local control</span><h1>Activity & changes</h1><p class="subtitle">A cross-project timeline, the latest commands, and every configured checkout’s current working-tree state.</p></div></div><div class="actions"><a class="button" href="/activity/inbox" data-local-link>Decision inbox <span data-inbox-count></span></a><a class="button" href="/setup" data-local-link>Setup</a><button class="button primary" type="button" data-refresh>Refresh</button></div></header>` + sections["summary"] + sections["metrics"] + `<p class="privacy-note" data-refresh-status role="status">Updated at <time datetime="` + escape(s.GeneratedAt) + `" data-local-time>` + escape(s.GeneratedAt) + `</time></p>` + usage.HTML + sections["warnings"] + sections["live"] + sections["timeline"] + sections["history"] + sections["projects"] + `<footer class="foot">In-flight calls and jobs update every 5 seconds, including while details are open. History updates in place every 15 seconds, including while details are open. Exact Bash scripts are retained within journal limits; output tails are bounded. Calls are tracked for this server process; jobs persist across restarts.</footer></main><script>` + dashboardScript + `</script></body></html>`
}
func prettyJSON(v any) string {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if encoder.Encode(v) != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}
func RenderBatchFragment(view map[string]any) string {
	definition := object(view["definition"])
	operations := array(definition["operations"])
	mode := stringValue(definition["mode"])
	if mode == "" {
		mode = "unknown"
	}
	stored := ""
	if boolean(view["autoStored"]) {
		stored = `<span class="badge">auto-stored</span>`
	}
	var cards strings.Builder
	if len(operations) == 0 {
		cards.WriteString(`<p class="empty">This JSON has no operation list.</p>`)
	}
	for i, raw := range operations {
		op := object(raw)
		id := stringValue(op["id"])
		if id == "" {
			id = fmt.Sprintf("op_%d", i+1)
		}
		tool := stringValue(op["tool"])
		if tool == "" {
			tool = "unknown"
		}
		fmt.Fprintf(&cards, `<details class="batch-operation" open><summary><span><b>%d</b><code>%s</code></span><strong>%s</strong></summary><pre>%s</pre></details>`, i, escape(id), escape(tool), escape(prettyJSON(object(op["args"]))))
	}
	return `<div class="badges"><span class="badge">` + escape(mode) + `</span><span class="badge">` + escape(plural(float64(len(operations)), "operation")) + `</span>` + stored + `</div><div class="operation-list">` + cards.String() + `</div><details class="raw"><summary><strong>Raw JSON</strong></summary><pre>` + escape(prettyJSON(view["definition"])) + `</pre></details>`
}
func RenderBatchPage(view map[string]any) string {
	definition := object(view["definition"])
	meta := escape(view["projectLabel"])
	if has(view, "workspaceId") {
		meta += " · " + escape(view["workspaceId"])
	}
	continueOnError := "no"
	if boolean(definition["continue_on_error"]) {
		continueOnError = "yes"
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="icon" href="/favicon.ico"><title>` + escape(view["path"]) + ` · CodexPro Batch</title><style>` + dashboardStyle + `main{max-width:1100px}.batch-operation{margin:12px 0;border:1px solid var(--rule);border-radius:8px;overflow:hidden}.batch-operation summary{display:flex;justify-content:space-between;gap:12px;padding:12px;cursor:pointer}.batch-operation summary span{display:flex;gap:12px}.batch-operation pre,.raw pre{margin:0;padding:14px;overflow:auto;background:#111827;color:#e5e7eb;font:12px/1.5 var(--mono);max-height:560px}.badges{display:flex;gap:8px;margin:12px 0}.badge{border:1px solid var(--rule);border-radius:999px;padding:4px 8px}.back{display:inline-block;margin:12px 0;color:var(--accent)}</style></head><body><main><a class="back" href="/activity" data-local-link>← Activity & changes</a><header class="dashboard-section"><span class="eyebrow">Saved batch definition</span><h1>` + escape(view["path"]) + `</h1><p>` + meta + ` · Continue on error: ` + continueOnError + `</p><p class="privacy-note">This is a saved definition. Its operations are displayed for review; opening this page does not execute them.</p></header><section class="dashboard-section batch-inline">` + RenderBatchFragment(view) + `</section></main><script>(function(){const u=new URL(location.href);const c=u.searchParams.get("codexpro_token")||u.searchParams.get("token")||sessionStorage.getItem("codexpro.activity.credential")||"";if(c)sessionStorage.setItem("codexpro.activity.credential",c);u.searchParams.delete("codexpro_token");u.searchParams.delete("token");history.replaceState(null,"",u.pathname+u.search);document.querySelectorAll("[data-local-link]").forEach(a=>{const v=new URL(a.href);if(c)v.searchParams.set("codexpro_token",c);a.href=v.pathname+v.search;});})();</script></body></html>`
}
