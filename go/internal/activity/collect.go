package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Snapshot struct {
	GeneratedAt             string
	Audit                   map[string]any
	Projects, RecentActions []map[string]any
	Live, Metrics, History  map[string]any
	Timeline                *Timeline
	TimelineNote            string
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
func escape(v any) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;").Replace(stringOf(v))
}
func has(m map[string]any, key string) bool { v, ok := m[key]; return ok && v != nil }
func humanBytes(v float64) string {
	absolute := math.Abs(v)
	if absolute < 1024 {
		return stringOf(v) + " B"
	}
	if absolute < 1048576 {
		digits := 0
		if absolute < 10240 {
			digits = 1
		}
		return strconv.FormatFloat(v/1024, 'f', digits, 64) + " KiB"
	}
	digits := 0
	if absolute < 10485760 {
		digits = 1
	}
	return strconv.FormatFloat(v/1048576, 'f', digits, 64) + " MiB"
}
func humanDuration(v float64) string {
	if v < 1000 {
		return stringOf(v) + " ms"
	}
	if v < 60000 {
		digits := 0
		if v < 10000 {
			digits = 1
		}
		return strconv.FormatFloat(v/1000, 'f', digits, 64) + " s"
	}
	return strconv.FormatFloat(v/60000, 'f', 1, 64) + " min"
}
func plural(v float64, singular string, alternate ...string) string {
	word := singular
	if v != 1 {
		if len(alternate) > 0 {
			word = alternate[0]
		} else {
			word += "s"
		}
	}
	return stringOf(v) + " " + word
}
func join(values ...string) string {
	out := []string{}
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return strings.Join(out, " · ")
}
func normalizePath(s string) string {
	return strings.TrimPrefix(path.Clean(strings.ReplaceAll(s, "\\", "/")), "./")
}
func (h *Handler) safePath(s string) bool {
	normalized := normalizePath(s)
	if s == "" || normalized == "" || normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "../") {
		return false
	}
	if h.options.SafePath != nil {
		return h.options.SafePath(normalized)
	}
	// Production supplies the shared path guard. Conservative lexical fallback is
	// useful for an empty/test dashboard and does not access the filesystem.
	for _, part := range strings.Split(normalized, "/") {
		if part == ".env" || strings.HasPrefix(part, ".env.") || part == ".ssh" || part == ".aws" || part == ".git" {
			return false
		}
	}
	return true
}
func unique(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func metadataLabel(key string) string {
	if s, ok := metadataLabels[key]; ok {
		return s
	}
	words := strings.Split(key, "_")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
func metadataValue(key string, v any) (string, bool) {
	switch v := v.(type) {
	case bool:
		if v {
			return "yes", true
		}
		return "no", true
	case float64, int, int64, json.Number:
		n := number(v)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return "", false
		}
		if key == "bytes" || strings.HasSuffix(key, "_bytes") {
			return humanBytes(n), true
		}
		if strings.HasSuffix(key, "_ms") {
			return humanDuration(n), true
		}
		return stringOf(n), true
	case string:
		clean := wire.Redact(strings.TrimSpace(v))
		if clean == "" {
			return "", false
		}
		if strings.HasSuffix(key, "_digest") || strings.HasSuffix(key, "_fingerprint") {
			short := clean
			if len(short) > 12 {
				short = short[:12] + "…"
			}
			return "sha256:" + short, true
		}
		return clean, true
	}
	if list := array(v); list != nil {
		return fmt.Sprintf("%d entries", len(list)), true
	}
	return "", false
}
func (h *Handler) metadataFields(metadata map[string]any) []map[string]any {
	out := []map[string]any{}
	pathKeys := map[string]bool{"path": true, "batch_path": true, "cwd": true, "directory": true}
	for key, v := range metadata {
		if key == "command_name" && strings.TrimSpace(stringValue(metadata["command_label"])) != "" {
			continue
		}
		if pathKeys[key] {
			if s, ok := v.(string); ok && !h.safePath(s) {
				continue
			}
		}
		formatted, ok := metadataValue(key, v)
		if !ok {
			continue
		}
		item := map[string]any{"key": key, "label": metadataLabel(key), "value": formatted, "mono": pathKeys[key] || strings.HasSuffix(key, "_digest") || strings.HasSuffix(key, "_fingerprint") || strings.HasSuffix(key, "_id") || key == "command_label"}
		if key == "additions" {
			item["tone"] = "positive"
		} else if key == "deletions" || key == "stderr_bytes" {
			item["tone"] = "negative"
		}
		out = append(out, item)
	}
	ranks := map[string]int{}
	for i, key := range metadataOrder {
		ranks[key] = i
	}
	sort.Slice(out, func(i, k int) bool {
		a, aok := ranks[stringValue(out[i]["key"])]
		b, bok := ranks[stringValue(out[k]["key"])]
		if aok || bok {
			if !aok {
				return false
			}
			if !bok {
				return true
			}
			return a < b
		}
		return stringValue(out[i]["label"]) < stringValue(out[k]["label"])
	})
	return out
}
func (h *Handler) headline(a audit.Action, paths []string) string {
	request, result := object(a["request_metadata"]), object(a["result_metadata"])
	target := ""
	if len(paths) > 0 {
		target = paths[0]
	} else {
		for _, key := range []string{"path", "cwd", "directory"} {
			candidate := stringValue(request[key])
			if h.safePath(candidate) {
				target = wire.Redact(normalizePath(candidate))
				break
			}
		}
	}
	delta := ""
	if has(result, "additions") || has(result, "deletions") {
		delta = fmt.Sprintf("+%v −%v", number(result["additions"]), number(result["deletions"]))
	}
	fallback := func(s string) string {
		if target != "" {
			return target
		}
		return s
	}
	countText := func(key, word string) string {
		if has(result, key) {
			return plural(number(result[key]), word)
		}
		return ""
	}
	flag := func(b bool, s string) string {
		if b {
			return s
		}
		return ""
	}
	switch stringValue(a["tool_name"]) {
	case "bash", "bash_job":
		label := stringValue(first(request["command_label"], request["command_name"]))
		if label == "" {
			label = "Command"
		}
		outcome := ""
		if boolean(result["timed_out"]) {
			outcome = "timed out"
		} else if has(result, "exit_code") {
			outcome = "exit " + stringOf(result["exit_code"])
		}
		return join(label, outcome)
	case "edit":
		work := ""
		edits := first(result["edits_applied"], request["edit_operations"])
		replacements := first(result["replacements"], request["expected_replacements"])
		if edits != nil {
			work = plural(number(edits), "operation")
		} else if replacements != nil {
			work = plural(number(replacements), "replacement")
		}
		return join(fallback("File edit"), delta, work)
	case "batch":
		ops, total := first(result["operation_count"], request["operation_count"]), result["total_operation_count"]
		label := "Batch"
		if ops != nil {
			label = plural(number(ops), "operation")
			if total != nil && number(total) != number(ops) {
				label = fmt.Sprintf("%v of %v operations", number(ops), number(total))
			}
		}
		outcome := ""
		if number(result["failed_count"]) > 0 {
			outcome = plural(number(result["failed_count"]), "failure")
		} else if number(result["skipped_count"]) > 0 {
			outcome = plural(number(result["skipped_count"]), "skipped operation")
		} else if boolean(result["succeeded"]) {
			outcome = "completed"
		}
		stored := ""
		if number(first(result["start_index"], request["from_index"])) > 0 {
			from := stringValue(first(result["start_operation_id"], request["from_operation"]))
			if from == "" {
				from = "#" + stringOf(number(first(result["start_index"], request["from_index"])))
			}
			stored = "from " + from
		} else if stringValue(result["batch_path"]) != "" {
			stored = "stored"
			if boolean(result["auto_stored"]) {
				stored = "saved"
			}
		}
		changed := ""
		if number(a["changed_path_count"]) > 0 {
			changed = plural(number(a["changed_path_count"]), "changed path")
		}
		checkpoint := ""
		if s := stringValue(result["checkpoint_status"]); s != "" {
			checkpoint = "checkpoint " + s
		}
		return join(label, stored, outcome, changed, checkpoint)
	case "write":
		size := ""
		if b := first(result["bytes"], request["content_bytes"]); b != nil {
			size = humanBytes(number(b))
		}
		return join(fallback("File write"), delta, size, flag(boolean(result["created"]), "created"))
	case "apply_patch":
		n := number(a["changed_path_count"])
		if n == 0 {
			n = number(request["target_path_count"])
		}
		if n == 0 {
			n = float64(len(paths))
		}
		return join(plural(n, "path"), delta)
	case "import_file":
		size := ""
		if has(result, "bytes") {
			size = humanBytes(number(result["bytes"]))
		}
		return join(fallback("Imported file"), size)
	case "read":
		rangeText, size := "", ""
		if has(request, "start_line") || has(request, "end_line") {
			start := first(request["start_line"], 1)
			rangeText = "lines " + stringOf(start)
			if has(request, "end_line") {
				rangeText += "–" + stringOf(request["end_line"])
			} else {
				rangeText += "+"
			}
		}
		if has(result, "bytes") {
			size = humanBytes(number(result["bytes"]))
		}
		return join(fallback("File read"), rangeText, size, flag(boolean(result["truncated"]), "truncated"))
	case "search", "ast_grep":
		matches := first(result["matches_count"], result["count"])
		count := ""
		if matches != nil {
			count = plural(number(matches), "match", "matches")
		}
		if a["tool_name"] == "ast_grep" {
			mode := stringValue(result["ast_mode"])
			if mode == "kind" && stringValue(request["ast_kind"]) != "" {
				mode = "kind " + stringValue(request["ast_kind"])
			}
			version := ""
			if s := stringValue(result["ast_provider_version"]); s != "" {
				version = "ast-grep " + s
			}
			return join(fallback("Structural search"), count, stringValue(request["ast_language"]), mode, version, flag(boolean(result["has_more"]), "more available"))
		}
		kind := stringValue(first(request["search_kind"], result["search_kind"]))
		scope := stringValue(first(request["search_scope"], result["search_scope"]))
		label, query := "Repository search", ""
		if kind == "config" {
			label, query = "Configuration query", "config"
		} else if boolean(request["regex"]) {
			query = "regex"
		}
		if scope == "workspace" {
			scope = ""
		}
		return join(fallback(label), count, query, strings.ReplaceAll(scope, "_", " "), flag(boolean(result["has_more"]), "more available"))
	case "tree":
		n := first(result["files_count"], result["paths_count"], result["count"])
		entries := ""
		if n != nil {
			entries = plural(number(n), "entry", "entries")
		}
		return join(fallback("Workspace tree"), entries)
	case "show_changes", "git_diff", "git_status":
		n := first(result["changed_files_count"], result["changed_paths_count"])
		files := ""
		if n != nil {
			files = plural(number(n), "changed file")
		}
		return join(fallback("Working tree"), files, delta)
	case "open_workspace":
		n := first(result["workspaces_count"], result["count"], request["project_ids_count"])
		label := stringValue(request["project_id"])
		if label == "" {
			label = "Workspace"
			if n != nil {
				label = plural(number(n), "workspace")
			}
		}
		reuse := ""
		if number(result["already_open_count"]) > 0 {
			reuse = plural(number(result["already_open_count"]), "workspace") + " reused"
		} else if boolean(result["already_open"]) {
			reuse = "already open"
		}
		return join(label, reuse, flag(boolean(request["include_tree"]), "with tree"))
	case "open_current_workspace":
		return join("Workspace", flag(boolean(request["include_tree"]), "with tree"))
	case "inspect_workspace":
		return join(fallback("Workspace inspection"), countText("files_count", "file"))
	case "work_status", "work_manage", "work_claim", "work_update":
		action := stringValue(request["action"])
		labels := map[string]string{"list": "List runs", "get": "Inspect run", "read_document": "Read document", "search_memory": "Search memory", "history": "Run history", "operation": "Inspect operation", "create": "Create run", "activate": "Activate run", "resume": "Resume run", "pause": "Pause run", "cancel": "Cancel run", "recover": "Recover run", "revise_limits": "Revise limits", "finish_run": "Verify run completion", "claim": "Claim iteration", "heartbeat": "Heartbeat", "checkpoint": "Checkpoint", "revise_plan": "Revise plan", "put_document": "Update document", "resolve_operation": "Reconcile operation", "finish_iteration": "Finish iteration"}
		label := labels[action]
		if label == "" {
			label = "Inspect work"
			if a["tool_name"] != "work_status" {
				label = "Work update"
			}
		}
		revision := ""
		if has(result, "revision") {
			revision = "revision " + stringOf(result["revision"])
		}
		return join(label, stringValue(request["section"]), revision)
	default:
		return join(fallback(stringValue(a["operation"])), countText("count", "result"), delta)
	}
}
func actionFacts(a audit.Action) []map[string]any {
	request, result := object(a["request_metadata"]), object(a["result_metadata"])
	facts := []map[string]any{}
	add := func(label string, value any, tone string) {
		if value == nil || stringOf(value) == "" {
			return
		}
		f := map[string]any{"label": label, "value": wire.Redact(stringOf(value))}
		if tone != "" {
			f["tone"] = tone
		}
		facts = append(facts, f)
	}
	delta := func() {
		if !has(result, "additions") && !has(result, "deletions") {
			return
		}
		toneA, toneD := "muted", "muted"
		if number(result["additions"]) > 0 {
			toneA = "positive"
		}
		if number(result["deletions"]) > 0 {
			toneD = "negative"
		}
		add("Added", "+"+stringOf(number(result["additions"])), toneA)
		add("Removed", "−"+stringOf(number(result["deletions"])), toneD)
	}
	size := func() {
		if has(result, "bytes") {
			add("Size", humanBytes(number(result["bytes"])), "")
		}
	}
	duration := func(label, key string) {
		if has(result, key) {
			add(label, humanDuration(number(result[key])), "")
		}
	}
	switch stringValue(a["tool_name"]) {
	case "read":
		start, end := request["start_line"], first(request["end_line"], result["end_line"])
		if start != nil || end != nil {
			add("Lines", stringOf(first(start, 1))+"–"+stringOf(first(end, "end")), "")
		}
		size()
		if boolean(result["truncated"]) {
			add("Truncated", "yes", "negative")
		}
	case "search", "ast_grep":
		add("Matches", result["matches_count"], "")
		add("Scope", strings.TrimSpace(stringValue(request["path"])+" "+stringValue(request["glob"])), "")
		if boolean(request["regex"]) {
			add("Regex", "yes", "")
		}
		if boolean(result["has_more"]) {
			add("More", "yes", "muted")
		}
		add("Engine", first(result["search_used"], result["ast_provider"]), "")
	case "tree":
		add("Path", first(request["path"], "."), "")
		add("Entries", first(result["count"], result["entries_count"]), "")
		add("Depth", request["max_depth"], "")
	case "write":
		delta()
		if value, ok := result["existed"].(bool); ok {
			outcome := "created"
			if value {
				outcome = "overwrote"
			}
			add("Outcome", outcome, "")
		}
		size()
	case "edit":
		delta()
		add("Operations", first(result["edits_applied"], request["edit_operations"]), "")
		size()
	case "apply_patch", "import_file":
		delta()
		add("Files", first(result["files_count"], result["changed_paths_count"]), "")
	case "bash", "bash_job":
		tone := ""
		if has(result, "exit_code") {
			tone = "positive"
			if number(result["exit_code"]) != 0 {
				tone = "negative"
			}
		}
		add("Exit", result["exit_code"], tone)
		duration("Duration", "duration_ms")
		if has(result, "stdout_bytes") {
			add("Output", humanBytes(number(result["stdout_bytes"]))+" out · "+humanBytes(number(result["stderr_bytes"]))+" err", "")
		}
		if boolean(result["timed_out"]) {
			add("Timed out", "yes", "negative")
		}
		if id := stringValue(result["job_id"]); id != "" {
			add("Job", strings.TrimSuffix(id+" · "+stringValue(result["job_origin"])+" · "+stringValue(result["job_status"]), " · "), "")
		}
		if cwd := stringValue(request["cwd"]); cwd != "." {
			add("Working dir", cwd, "")
		}
	case "batch":
		executed, total := first(result["executed_operation_count"], result["operation_count"]), first(result["total_operation_count"], request["operation_count"])
		operations := first(executed, total)
		if executed != nil && total != nil && number(total) != number(executed) {
			operations = stringOf(executed) + " of " + stringOf(total)
		}
		add("Operations", operations, "")
		add("Succeeded", result["succeeded_count"], "positive")
		if number(result["failed_count"]) != 0 {
			add("Failed", result["failed_count"], "negative")
		}
		if number(result["skipped_count"]) != 0 {
			add("Skipped", result["skipped_count"], "muted")
		}
		add("Mode", request["mode"], "")
		if number(request["file_mutation_count"]) != 0 {
			add("File mutations", request["file_mutation_count"], "")
		}
		if number(request["verification_command_count"]) != 0 {
			add("Verification", plural(number(request["verification_command_count"]), "command"), "")
		}
		add("Saved", first(result["batch_path"], request["batch_path"]), "")
	case "show_changes":
		delta()
		add("Changed files", result["changed_files_count"], "")
		add("Scope", request["path"], "")
		if boolean(request["staged"]) {
			add("Staged", "yes", "")
		}
	case "commit_changes":
		add("Commit", first(result["commit"], object(a["git_after"])["head"]), "")
		add("Branch", first(result["branch"], object(a["git_after"])["branch"]), "")
		add("Files", first(result["file_count"], result["files_count"]), "")
		state := "not recorded"
		if v, ok := result["status_available"].(bool); ok && !v {
			state = "unavailable"
		} else if v, ok := result["working_tree_clean"].(bool); ok {
			state = "changes remain"
			if v {
				state = "clean"
			}
		}
		add("Post-commit tree", state, "")
		add("Skipped blocked paths", result["skipped_blocked_count"], "")
		add("Status error", result["status_error"], "")
	case "open_workspace", "open_current_workspace", "create_workspace":
		add("Project", first(result["project_id"], request["project_id"]), "")
		if number(result["workspaces_count"]) > 1 {
			add("Projects", result["workspaces_count"], "")
		}
		if boolean(result["already_open"]) {
			add("Already open", "yes", "muted")
		}
	case "start_jobs", "jobs", "stop_jobs", "stop_job":
		add("Jobs", first(result["jobs_count"], request["commands_count"], request["job_ids_count"]), "")
		add("Running", result["running_count"], "")
		duration("Actual wait", "waited_ms")
		if wait := first(result["requested_wait_ms"], request["wait_ms"]); wait != nil {
			add("Wait limit", humanDuration(number(wait)), "")
		}
		add("Wait mode", request["wait_for"], "")
		add("All finished", result["all_finished"], "")
		add("All succeeded", result["all_succeeded"], "")
		if boolean(result["server_restarting"]) {
			add("Server restarting", "yes", "")
		}
	case "work_status", "work_manage", "work_claim", "work_update":
		add("Run", first(a["run_id"], result["run_id"], request["run_id"]), "")
		for _, pair := range [][2]string{{"Revision", "revision"}, {"State", "state"}, {"Documents saved", "documents_count"}, {"Checkpoint", "checkpoint_id"}, {"Document", "document_id"}} {
			add(pair[0], result[pair[1]], "")
		}
		add("Outcome", request["outcome"], "")
	default:
		for _, key := range []string{"count", "matches_count", "files_count", "bytes", "project_id"} {
			v := first(result[key], request[key])
			if key == "bytes" && v != nil {
				v = humanBytes(number(v))
			}
			add(metadataLabel(key), v, "")
		}
	}
	if len(facts) > 8 {
		facts = facts[:8]
	}
	return facts
}
func (h *Handler) actionView(a audit.Action, projectID, label, attribution string) map[string]any {
	request, result := object(a["request_metadata"]), object(a["result_metadata"])
	paths := []string{}
	hidden := 0
	rawPaths := []string{}
	for _, v := range array(a["changed_paths"]) {
		rawPaths = append(rawPaths, normalizePath(stringValue(v)))
	}
	for _, p := range unique(rawPaths) {
		if h.safePath(p) {
			paths = append(paths, wire.Redact(p))
		} else {
			hidden++
		}
	}
	readPaths := []string{}
	if a["operation_class"] == "read" || a["operation_class"] == "analysis" {
		for _, v := range array(a["targets"]) {
			s := stringValue(v)
			if strings.HasPrefix(s, "project:") || s == "workspace changes" || strings.HasPrefix(s, "batch:") {
				continue
			}
			s = normalizePath(s)
			if h.safePath(s) {
				readPaths = append(readPaths, wire.Redact(s))
			}
		}
		readPaths = unique(readPaths)
		if len(readPaths) > 12 {
			readPaths = readPaths[:12]
		}
	}
	before, after := map[string]map[string]any{}, map[string]map[string]any{}
	evidenceOrder := []string{}
	for _, part := range []struct {
		key   string
		index map[string]map[string]any
	}{{"path_evidence_before", before}, {"path_evidence_after", after}} {
		for _, v := range array(a[part.key]) {
			e := object(v)
			p := stringValue(e["path"])
			if h.safePath(p) {
				p = normalizePath(p)
				evidenceOrder = append(evidenceOrder, p)
				part.index[p] = e
			}
		}
	}
	evidence := []map[string]any{}
	state := func(v map[string]any) string {
		if !boolean(v["exists"]) {
			return "missing"
		}
		kind := stringValue(v["kind"])
		if kind == "" {
			kind = "item"
		}
		if has(v, "size") {
			kind += " · " + humanBytes(number(v["size"]))
		}
		return kind
	}
	for i, p := range unique(evidenceOrder) {
		if i >= 6 {
			break
		}
		evidence = append(evidence, map[string]any{"label": wire.Redact(p), "value": state(before[p]) + " → " + state(after[p])})
	}
	jobViews := []map[string]any{}
	if list := array(result["jobs"]); list != nil {
		for _, v := range list {
			jobViews = append(jobViews, object(v))
		}
	} else if id := stringValue(result["job_id"]); id != "" {
		jobViews = append(jobViews, map[string]any{"job_id": id, "status": result["job_status"], "exit_code": result["exit_code"], "stop_reason": result["stop_reason"]})
	} else {
		for _, id := range array(request["job_ids"]) {
			jobViews = append(jobViews, map[string]any{"job_id": id})
		}
	}
	children := []map[string]any{}
	for _, v := range array(result["child_results"]) {
		children = append(children, object(v))
	}
	scripts := []map[string]any{}
	dashboard := object(a["dashboard_metadata"])
	for _, v := range array(dashboard["shell_scripts"]) {
		s := object(v)
		scripts = append(scripts, map[string]any{"operationId": s["operation_id"], "script": wire.Redact(stringValue(s["script"])), "truncated": boolean(s["truncated"])})
	}
	out := map[string]any{"actionId": a["action_id"], "sequence": a["sequence"], "finishedAt": a["finished_at"], "startedAt": a["occurred_at"], "projectId": projectID, "projectLabel": label, "workspaceId": a["workspace_id"], "attribution": attribution, "toolName": a["tool_name"], "operation": a["operation"], "operationClass": a["operation_class"], "status": a["status"], "durationMs": a["duration_ms"], "mutating": a["mutating"], "headline": h.headline(a, paths), "facts": actionFacts(a), "jobs": jobViews, "childResults": children, "jobDetailsTruncated": boolean(result["jobs_truncated"]), "childResultsTruncated": boolean(result["child_results_truncated"]), "readPaths": readPaths, "changedPaths": paths, "hiddenPathCount": hidden, "changedPathsTruncated": boolean(a["changed_paths_truncated"]), "requestFields": h.metadataFields(request), "resultFields": h.metadataFields(result), "pathEvidence": evidence, "errorCode": a["error_code"], "errorMessage": dashboard["error_message"], "recoveryMessage": dashboard["recovery_message"], "shellScripts": scripts}
	for _, side := range [][2]string{{"git_before", "gitBefore"}, {"git_after", "gitAfter"}} {
		if g := object(a[side[0]]); len(g) > 0 {
			out[side[1]] = map[string]any{"branch": wire.Redact(stringValue(g["branch"])), "head": g["head"], "dirty": g["dirty"], "changedPathCount": g["changed_path_count"]}
		}
	}
	if a["tool_name"] == "batch" {
		candidate := stringValue(first(result["batch_path"], request["batch_path"]))
		if h.safePath(candidate) && projectID != "" {
			candidate = normalizePath(candidate)
			params := url.Values{"project_id": {projectID}, "path": {candidate}}
			if id := stringValue(a["workspace_id"]); id != "" {
				params.Set("workspace_id", id)
			}
			out["batchPath"] = candidate
			out["batchHref"] = "/activity/batch?" + params.Encode()
		}
	}
	return out
}
func (h *Handler) cachedGit(ctx context.Context, project wire.Project) (map[string]any, error) {
	unavailable := func(message string) map[string]any {
		return map[string]any{"available": false, "message": message, "dirty": false, "trackedChangedPaths": []string{}, "untrackedPaths": []string{}, "hiddenPathCount": 0, "omittedPathCount": 0, "additions": 0, "deletions": 0, "diffAvailable": false}
	}
	if h.options.Git == nil {
		return unavailable("Git working-tree status could not be read."), nil
	}
	key := project.ID + "\x00" + project.Root
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := h.ctx.Err(); err != nil {
			return nil, err
		}
		h.gitMu.Lock()
		cached := h.gitCache[key]
		if cached != nil && cached.ready == nil && h.options.Clock().Sub(cached.at) < 10*time.Second {
			value := cached.value
			h.gitMu.Unlock()
			return value, nil
		}
		if cached != nil && cached.ready != nil {
			ready := cached.ready
			h.gitMu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-h.ctx.Done():
				return nil, h.ctx.Err()
			}
		}
		ready := make(chan struct{})
		h.gitCache[key] = &gitCacheEntry{ready: ready}
		h.gitMu.Unlock()
		select {
		case h.gitSlots <- struct{}{}:
		case <-ctx.Done():
			h.gitMu.Lock()
			delete(h.gitCache, key)
			close(ready)
			h.gitMu.Unlock()
			return nil, ctx.Err()
		case <-h.ctx.Done():
			h.gitMu.Lock()
			delete(h.gitCache, key)
			close(ready)
			h.gitMu.Unlock()
			return nil, h.ctx.Err()
		}
		operationCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(h.ctx, cancel)
		value, err := h.options.Git(operationCtx, project, false)
		stop()
		canceled := operationCtx.Err()
		cancel()
		<-h.gitSlots
		if canceled != nil {
			h.gitMu.Lock()
			delete(h.gitCache, key)
			close(ready)
			h.gitMu.Unlock()
			return nil, canceled
		}
		if err != nil {
			value = unavailable("Git working-tree status could not be read.")
		}
		h.gitMu.Lock()
		h.gitCache[key] = &gitCacheEntry{at: h.options.Clock(), value: value}
		close(ready)
		h.gitMu.Unlock()
		return value, nil
	}
}
func collectMetrics(actions []map[string]any, now time.Time, projectID string) map[string]any {
	start := now.Add(-time.Hour)
	count, failed := 0, 0
	durations := []float64{}
	for _, a := range actions {
		if a["toolName"] == "bash_job" || projectID != "" && a["projectId"] != projectID {
			continue
		}
		status := stringValue(a["status"])
		if status != "succeeded" && status != "failed" && status != "timed_out" && status != "cancelled" && status != "blocked" {
			continue
		}
		finished, ok := parseTime(a["finishedAt"])
		if !ok || finished.Before(start) || finished.After(now) {
			continue
		}
		count++
		if status != "succeeded" {
			failed++
		}
		d := number(a["durationMs"])
		if has(a, "durationMs") && !math.IsInf(d, 0) && !math.IsNaN(d) && d >= 0 {
			durations = append(durations, d)
		}
	}
	sort.Float64s(durations)
	var mean, median any
	if len(durations) > 0 {
		avg := 0.0
		for _, d := range durations {
			avg += d / float64(len(durations))
		}
		mean = avg
		mid := len(durations) / 2
		median = durations[mid]
		if len(durations)%2 == 0 {
			median = durations[mid-1]/2 + durations[mid]/2
		}
	}
	out := map[string]any{"source": "retained journal", "windowMs": 3600000, "windowStartedAt": timestamp(start), "windowEndedAt": timestamp(now), "count": count, "failedCount": failed, "callsPerHour": count, "latencySampleCount": len(durations), "meanLatencyMs": mean, "medianLatencyMs": median}
	if projectID != "" {
		out["projectId"] = projectID
	}
	return out
}
func (h *Handler) CollectDashboard(ctx context.Context, projectID string, beforeSequence int64) (Snapshot, error) {
	now := h.options.Clock()
	snapshot := Snapshot{GeneratedAt: timestamp(now)}
	var err error
	snapshot.Audit, err = h.options.Audit.Status(ctx)
	if err != nil {
		return snapshot, err
	}
	retained, err := h.options.Audit.ListDashboard(ctx, audit.Filter{Limit: 100000})
	if err != nil {
		return snapshot, err
	}
	projects := h.projects()
	labels := map[string]string{}
	for _, p := range projects {
		labels[p.ID] = p.Label
	}
	workspaceProjects, ambiguous := map[string]string{}, map[string]bool{}
	for _, a := range retained.Actions {
		if strings.HasPrefix(stringValue(a["tool_name"]), "work_") && stringValue(a["audit_scope"]) == "" {
			continue
		}
		workspace, project := stringValue(a["workspace_id"]), stringValue(a["project_id"])
		if workspace == "" || project == "" || ambiguous[workspace] {
			continue
		}
		if previous := workspaceProjects[workspace]; previous != "" && previous != project {
			delete(workspaceProjects, workspace)
			ambiguous[workspace] = true
		} else {
			workspaceProjects[workspace] = project
		}
	}
	all := []map[string]any{}
	byProject := map[string][]map[string]any{}
	for i := len(retained.Actions) - 1; i >= 0; i-- {
		a := retained.Actions[i]
		project, label, attribution := "", "Unattributed", "unattributed"
		scope, tool := stringValue(a["audit_scope"]), stringValue(a["tool_name"])
		if scope == "server" {
			label, attribution = "Server", "server"
		} else if scope == "unattributed" || strings.HasPrefix(tool, "work_") && scope == "" {
		} else if p := stringValue(a["project_id"]); p != "" {
			project = p
			if known, ok := labels[p]; ok {
				label, attribution = known, "recorded"
			} else {
				label, attribution = "Unknown project id (not in catalog)", "unknown"
			}
		} else if recovered := workspaceProjects[stringValue(a["workspace_id"])]; recovered != "" {
			project, attribution = recovered, "recovered"
			label = labels[recovered]
			if label == "" {
				label = "Unknown project id (not in catalog)"
			}
		}
		view := h.actionView(a, project, label, attribution)
		all = append(all, view)
		if len(byProject[project]) < 5 {
			byProject[project] = append(byProject[project], view)
		}
	}
	snapshot.Projects = make([]map[string]any, len(projects))
	tasks := make(chan int, len(projects))
	var wg sync.WaitGroup
	var errorMu sync.Mutex
	var collectErr error
	workers := min(2, len(projects))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range tasks {
				project := projects[index]
				git, gitErr := h.cachedGit(ctx, project)
				if gitErr != nil {
					errorMu.Lock()
					collectErr = gitErr
					errorMu.Unlock()
					continue
				}
				actions := byProject[project.ID]
				if actions == nil {
					actions = []map[string]any{}
				}
				p := map[string]any{"id": project.ID, "label": project.Label, "actions": actions, "git": git}
				if len(actions) > 0 {
					p["latestActivityAt"] = actions[0]["finishedAt"]
				}
				snapshot.Projects[index] = p
			}
		}()
	}
	for i := range projects {
		tasks <- i
	}
	close(tasks)
	wg.Wait()
	if collectErr != nil {
		return snapshot, collectErr
	}
	sort.SliceStable(snapshot.Projects, func(i, k int) bool {
		a, b := stringValue(snapshot.Projects[i]["latestActivityAt"]), stringValue(snapshot.Projects[k]["latestActivityAt"])
		if a != "" || b != "" {
			return a > b
		}
		return stringValue(snapshot.Projects[i]["label"]) < stringValue(snapshot.Projects[k]["label"])
	})
	matching, page := []map[string]any{}, []map[string]any{}
	timelineActions := []map[string]any{}
	for _, a := range all {
		if projectID == "" || a["projectId"] == projectID {
			matching = append(matching, a)
			if beforeSequence == 0 || int64(number(a["sequence"])) < beforeSequence {
				page = append(page, a)
			}
		}
		if at, ok := parseTime(a["finishedAt"]); ok && now.Sub(at) <= 14*24*time.Hour {
			timelineActions = append(timelineActions, a)
		}
	}
	snapshot.RecentActions = page
	if len(snapshot.RecentActions) > 30 {
		snapshot.RecentActions = snapshot.RecentActions[:30]
	}
	snapshot.History = map[string]any{"matchedCount": len(matching), "retainedCount": len(all)}
	if projectID != "" {
		snapshot.History["projectId"] = projectID
	}
	if beforeSequence > 0 {
		snapshot.History["beforeSequence"] = beforeSequence
	}
	if len(page) > 30 {
		snapshot.History["nextBeforeSequence"] = snapshot.RecentActions[len(snapshot.RecentActions)-1]["sequence"]
	}
	snapshot.Metrics = collectMetrics(all, now, projectID)
	retain := 350
	if h.options.Config != nil {
		retain = h.options.Config.Int("auditRetainActions", 350)
	}
	snapshot.Timeline = buildTimeline(timelineActions, now.UnixMilli(), retain)
	snapshot.TimelineNote = "No retained actions"
	if snapshot.Timeline != nil {
		snapshot.TimelineNote = fmt.Sprintf("Last %d actions · %d lanes · %s per cell", snapshot.Timeline.ActionCount, len(snapshot.Timeline.Lanes), timelineBinLabel(snapshot.Timeline.BinMS))
	}
	snapshot.Live = h.CollectLive(projectID)
	return snapshot, nil
}
