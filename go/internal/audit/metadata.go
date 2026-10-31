package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func timeNow() time.Time     { return time.Now() }
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func opaque(prefix, value string) string {
	if value == "" {
		value = "unknown"
	}
	return prefix + "_" + digest(value)[:32]
}
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
func isNumber(v any) bool {
	switch v.(type) {
	case float64, int, int64, int32, json.Number:
		f := number(v)
		return !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return false
}
func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(Action); ok {
		return map[string]any(m)
	}
	return map[string]any{}
}
func array(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	switch a := v.(type) {
	case []string:
		out := make([]any, len(a))
		for i, v := range a {
			out[i] = v
		}
		return out
	case []map[string]any:
		out := make([]any, len(a))
		for i, v := range a {
			out[i] = v
		}
		return out
	}
	return nil
}
func text(v any) string    { s, _ := v.(string); return s }
func boolValue(v any) bool { b, _ := v.(bool); return b }
func first(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

var identifierRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$")
var controlRE = regexp.MustCompile("[\x00-\x1f\x7f]")

// JavaScript String.trim differs from unicode.IsSpace: FEFF is whitespace,
// while U+0085 is not. Keep the audit display path equal to capture evidence.
func auditTrimSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func bounded(v any, max int) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimFunc(controlRE.ReplaceAllString(s, " "), auditTrimSpace)
	units := utf16.Encode([]rune(s))
	if len(units) > max {
		s = string(utf16.Decode(units[:max]))
	}
	return s
}
func identifier(v any) string {
	s := bounded(v, 160)
	if identifierRE.MatchString(s) {
		return s
	}
	return ""
}
func relative(v any) string {
	s := bounded(v, 4096)
	driveAbsolute := len(s) > 2 && ((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z')) && s[1] == ':' && (s[2] == '/' || s[2] == '\\')
	if s == "" || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") || driveAbsolute {
		return ""
	}
	slashed := strings.ReplaceAll(s, "\\", "/")
	s = path.Clean(slashed)
	if strings.HasSuffix(slashed, "/") {
		s += "/"
	}
	if s == ".." || strings.HasPrefix(s, "../") || strings.Contains(s, "/../") {
		return ""
	}
	if len(utf16.Encode([]rune(s))) > 320 {
		units := utf16.Encode([]rune(s))
		s = string(utf16.Decode(units[:303])) + "~" + digest(s)[:16]
	}
	return s
}
func addString(out map[string]any, key string, v any, max int) {
	if s := bounded(v, max); s != "" {
		out[key] = s
	}
}
func addID(out map[string]any, key string, v any) {
	if s := identifier(v); s != "" {
		out[key] = s
	}
}
func addPath(out map[string]any, key string, v any) {
	if s := relative(v); s != "" {
		out[key] = s
	}
}
func addNumber(out map[string]any, key string, v any) {
	if isNumber(v) {
		out[key] = number(v)
	}
}
func addBool(out map[string]any, key string, v any) {
	if b, ok := v.(bool); ok {
		out[key] = b
	}
}
func addBytes(out map[string]any, key string, v any) {
	if s, ok := v.(string); ok {
		out[key] = len([]byte(s))
	}
}
func copyNumbers(out, in map[string]any, keys string) {
	for _, k := range strings.Fields(keys) {
		addNumber(out, k, in[k])
	}
}
func copyBools(out, in map[string]any, keys string) {
	for _, k := range strings.Fields(keys) {
		addBool(out, k, in[k])
	}
}
func copyStrings(out, in map[string]any, keys string, max int) {
	for _, k := range strings.Fields(keys) {
		addString(out, k, in[k], max)
	}
}
func unique(values []string, limit int) ([]string, bool) {
	out := []string{}
	seen := map[string]bool{}
	truncated := false
	for _, s := range values {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		if len(out) < limit {
			out = append(out, s)
		} else {
			truncated = true
		}
	}
	return out, truncated
}

var safeLabels = strings.Fields("bash biome bun bundle cargo cat clang clang++ cmake cmd composer deno docker dotnet eslint find git go gradle gradlew grep head java javac ls make mvn ninja node npm npx perl php pnpm powershell pwd pwsh pytest rg ruby rustc sed sh swift tail tsc uv wc xcodebuild yarn zsh")
var safeSubcommands = map[string]string{
	"bun": "add build install lint remove run test x", "cargo": "build check clean clippy doc fetch fmt run test update",
	"docker": "build compose exec images info inspect logs ps pull run stop version", "dotnet": "build clean format pack publish restore run test",
	"git": "add branch checkout clean commit diff fetch log merge pull push rebase reset restore rev-parse show status switch tag",
	"go":  "build clean env fmt generate install list mod run test tool version vet work", "gradle": "build check clean test", "gradlew": "build check clean test", "mvn": "clean compile install package test verify",
	"npm": "audit build ci install lint pack publish run start test update", "pnpm": "add audit build install lint pack publish remove run start test update",
	"swift": "build package run test", "uv": "add build lock pip remove run sync tool venv", "yarn": "add audit build install lint pack remove run start test upgrade",
}

func contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

var commandTokenRE = regexp.MustCompile("^[A-Za-z0-9_./:-]+$")
var pythonRE = regexp.MustCompile("^python([0-9]+(\\.[0-9]+)*)?$")
var scriptRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._:@-]{0,79}$")
var sensitiveLabelRE = regexp.MustCompile("(?i)(api[_-]?key|auth|credential|password|passwd|private[_-]?key|secret|token)")

func safeSegment(s string) bool { return scriptRE.MatchString(s) && !sensitiveLabelRE.MatchString(s) }
func commandName(command string) string {
	for _, token := range strings.Fields(command) {
		if strings.Contains(token, "=") {
			continue
		}
		if !commandTokenRE.MatchString(token) {
			return "complex"
		}
		base := strings.TrimSuffix(strings.ToLower(path.Base(strings.ReplaceAll(token, "\\", "/"))), ".exe")
		if pythonRE.MatchString(base) {
			return "python"
		}
		if contains(safeLabels, base) {
			return base
		}
		return "other"
	}
	return ""
}
func commandLabel(command string) string {
	name := commandName(command)
	if name == "" || name == "complex" || name == "other" {
		return name
	}
	if strings.ContainsAny(command, "\r\n;&|<>"+string(rune(96))) || strings.Contains(command, "$(") {
		return name
	}
	tokens := strings.Fields(command)
	idx := -1
	for i, t := range tokens {
		if !strings.Contains(t, "=") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return name
	}
	args := tokens[idx+1:]
	if name == "make" || name == "npx" {
		for _, v := range args {
			if !strings.HasPrefix(v, "-") && safeSegment(v) {
				return name + " " + v
			}
		}
		return name
	}
	if name == "python" && len(args) > 1 && args[0] == "-m" && safeSegment(args[1]) {
		return name + " -m " + args[1]
	}
	sub := ""
	subindex := 0
	for i, v := range args {
		if !strings.HasPrefix(v, "-") {
			sub = v
			subindex = i
			break
		}
	}
	if !contains(strings.Fields(safeSubcommands[name]), sub) || sub == "" {
		return name
	}
	next := ""
	if subindex+1 < len(args) {
		next = args[subindex+1]
	}
	if contains([]string{"npm", "pnpm", "yarn", "bun"}, name) && sub == "run" {
		if safeSegment(next) {
			return name + " run " + next
		}
		return name + " run"
	}
	if name == "docker" && sub == "compose" {
		if contains(strings.Fields("build config down exec logs ps pull restart run start stop up"), next) {
			return name + " compose " + next
		}
		return name + " compose"
	}
	if name == "uv" && sub == "run" {
		if safeSegment(next) {
			return name + " run " + next
		}
		return name + " run"
	}
	return name + " " + sub
}
func patchPaths(v any) []string {
	patch, ok := v.(string)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, line := range strings.Split(patch, "\n") {
		for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: ", "--- ", "+++ "} {
			if strings.HasPrefix(line, prefix) {
				p := strings.TrimPrefix(line, prefix)
				p = strings.SplitN(p, "\t", 2)[0]
				p = strings.Trim(p, "\"")
				p = strings.TrimPrefix(strings.TrimPrefix(p, "a/"), "b/")
				if p != "/dev/null" {
					out = append(out, relative(p))
				}
			}
		}
	}
	values, _ := unique(out, 24)
	return values
}
func summarizeArgs(tool string, args map[string]any) map[string]any {
	out := map[string]any{}
	addID(out, "workspace_id", args["workspace_id"])
	addID(out, "project_id", args["project_id"])
	switch tool {
	case "work_status", "work_manage", "work_claim", "work_update":
		for _, k := range strings.Fields("action section run_id phase document_id operation_id outcome") {
			addID(out, k, args[k])
		}
		copyNumbers(out, args, "expected_revision document_revision")
		for _, k := range []string{"documents", "todos"} {
			if a := array(args[k]); a != nil {
				out[k+"_count"] = len(a)
			}
		}
	case "open_workspace":
		if a := array(args["project_ids"]); a != nil {
			ids := []string{}
			for _, v := range a {
				ids = append(ids, identifier(v))
			}
			ids, _ = unique(ids, len(ids))
			if len(ids) > 0 {
				out["project_ids_count"] = len(ids)
			}
		}
		if s, ok := args["root"].(string); ok {
			out["requested_root_digest"] = digest(s)
		}
		copyBools(out, args, "include_tree")
		copyNumbers(out, args, "max_depth max_files")
	case "open_current_workspace":
		copyBools(out, args, "include_tree")
		copyNumbers(out, args, "max_depth")
	case "create_project":
		addID(out, "parent_id", args["parent_id"])
		addPath(out, "directory", args["directory"])
		addString(out, "source", args["source"], 24)
		_, supplied := args["repository"].(string)
		out["repository_supplied"] = supplied
		if supplied {
			out["repository_digest"] = digest(text(args["repository"]))
		}
		addString(out, "initial_branch", args["initial_branch"], 255)
		addString(out, "base_ref", args["base_ref"], 256)
		copyNumbers(out, args, "max_worktrees")
	case "create_workspace":
		addString(out, "base_ref", args["base_ref"], 256)
		_, supplied := args["idempotency_key"].(string)
		out["idempotency_key_supplied"] = supplied
	case "read":
		addPath(out, "path", args["path"])
		copyNumbers(out, args, "start_line end_line max_bytes")
	case "write":
		addPath(out, "path", args["path"])
		addBytes(out, "content_bytes", args["content"])
		copyBools(out, args, "create_dirs overwrite")
		_, ok := args["expected_sha256"].(string)
		out["expected_sha256_supplied"] = ok
	case "edit":
		addPath(out, "path", args["path"])
		out["edit_mode"] = "tagged_lines"
		_, ok := args["edit_tag"].(string)
		out["edit_tag_supplied"] = ok
		edits := array(args["edits"])
		if len(edits) > 0 {
			out["edit_operations"] = len(edits)
			size := 0
			for _, v := range edits {
				size += len([]byte(text(object(v)["content"])))
			}
			out["edit_content_bytes"] = size
		}
	case "batch":
		p := relative(args["path"])
		out["batch_source"] = "inline"
		if p != "" {
			out["batch_source"] = "file"
			out["batch_path"] = p
		}
		operations := array(args["operations"])
		if len(operations) > 0 {
			out["operation_count"] = len(operations)
		}
		mutations, verification := 0, 0
		for _, v := range operations {
			t := text(object(v)["tool"])
			if contains([]string{"write", "edit", "apply_patch"}, t) {
				mutations++
			}
			if t == "bash" {
				verification++
			}
		}
		out["file_mutation_count"] = mutations
		out["verification_command_count"] = verification
		addString(out, "from_operation", args["from"], 80)
		copyNumbers(out, args, "from_index")
		copyBools(out, args, "persist continue_on_error")
		addString(out, "mode", args["mode"], 16)
	case "apply_patch":
		addBytes(out, "patch_bytes", args["patch"])
		out["target_path_count"] = len(patchPaths(args["patch"]))
	case "import_file":
		addPath(out, "path", first(args["destination"], args["path"], args["target_path"]))
		out["source_supplied"] = first(args["file"], args["source"], args["url"]) != nil
		copyBools(out, args, "overwrite")
		_, ok := args["expected_sha256"].(string)
		out["expected_sha256_supplied"] = ok
	case "bash", "bash_job":
		addPath(out, "cwd", args["cwd"])
		command, ok := args["command"].(string)
		if ok {
			addString(out, "command_name", commandName(command), 80)
			addString(out, "command_label", commandLabel(command), 256)
			if command != "" {
				out["command_digest"] = digest(command)
			}
			out["command_bytes"] = len([]byte(command))
		}
		copyNumbers(out, args, "timeout_ms")
		_, ok = args["session_id"].(string)
		out["session_id_supplied"] = ok
		copyBools(out, args, "background")
		addString(out, "on_timeout", args["on_timeout"], 16)
		addString(out, "job_id", args["job_id"], 40)
		addString(out, "origin", args["origin"], 16)
		if ids := array(args["input_job_ids"]); ids != nil {
			if len(ids) > 16 {
				ids = ids[:16]
			}
			mapped := []any{}
			for _, v := range ids {
				if s := identifier(v); s != "" {
					mapped = append(mapped, s)
				} else {
					mapped = append(mapped, nil)
				}
			}
			out["input_job_ids"] = mapped
		}
	case "jobs", "stop_jobs":
		if ids := array(args["job_ids"]); ids != nil {
			out["job_ids_count"] = len(ids)
			out["job_ids"] = idsSafe(ids, 32)
		}
		copyNumbers(out, args, "wait_ms max_bytes")
		copyBools(out, args, "full_output")
		addString(out, "wait_for", args["wait_for"], 8)
		addString(out, "output", args["output"], 16)
		_, ok := args["cursor"].(string)
		out["cursor_supplied"] = ok
	case "start_jobs":
		if a := array(args["commands"]); a != nil {
			out["commands_count"] = len(a)
		}
		_, ok := args["session_id"].(string)
		out["session_id_supplied"] = ok
	case "search", "ast_grep":
		addPath(out, "path", args["path"])
		copyBools(out, args, "include_hidden group_by_file")
		copyNumbers(out, args, "max_results context_before context_after")
		_, ok := args["cursor"].(string)
		out["cursor_supplied"] = ok
		if tool == "search" {
			addString(out, "glob", args["glob"], 512)
			if q, ok := args["query"].(string); ok {
				if q != "" {
					out["query_digest"] = digest(q)
				}
				out["query_bytes"] = len([]byte(q))
			}
			addString(out, "search_kind", args["kind"], 16)
			addString(out, "search_scope", args["scope"], 32)
			addString(out, "config_format", args["config_format"], 16)
			copyBools(out, args, "regex include_untracked")
			addString(out, "base_ref", args["base_ref"], 256)
			addString(out, "diff_target", args["diff_target"], 16)
			addString(out, "intent", args["intent"], 32)
			if sym, ok := args["symbol"].(string); ok {
				out["symbol"] = opaque("symbol", sym)
			}
		} else {
			if p, ok := args["pattern"].(string); ok {
				if p != "" {
					out["pattern_digest"] = digest(p)
				}
				out["pattern_bytes"] = len([]byte(p))
			}
			addString(out, "ast_kind", args["kind"], 256)
			addString(out, "ast_language", args["language"], 80)
			addString(out, "ast_selector", args["selector"], 256)
			addString(out, "ast_strictness", args["strictness"], 32)
			if a := array(args["globs"]); a != nil {
				out["globs_count"] = len(a)
			}
			copyNumbers(out, args, "timeout_ms")
		}
	case "tree":
		addPath(out, "path", args["path"])
		copyNumbers(out, args, "max_depth max_entries")
		copyBools(out, args, "include_hidden")
	case "inspect_workspace":
		addPath(out, "path", args["path"])
		copyNumbers(out, args, "max_files")
		copyBools(out, args, "include_symbols include_relationships")
	case "show_changes":
		addPath(out, "path", args["path"])
		copyBools(out, args, "staged include_diff")
	case "commit_changes":
		if s, ok := args["message"].(string); ok {
			out["message_digest"] = digest(s)
			out["message_bytes"] = len([]byte(s))
		}
		if a := array(args["paths"]); a != nil {
			out["paths_count"] = len(a)
		}
	case "codexpro_self_test":
		copyBools(out, args, "write_probe bash_probe pro_context_probe")
	case "load_skill":
		if s, ok := args["name"].(string); ok {
			out["skill_ref"] = opaque("skill", s)
		}
		addString(out, "source", args["source"], 32)
		copyNumbers(out, args, "max_bytes")
	case "release_workspace", "remove_workspace":
	default:
		addPath(out, "path", args["path"])
		addPath(out, "cwd", args["cwd"])
		copyNumbers(out, args, "limit")
	}
	return out
}
func idsSafe(values []any, max int) []string {
	out := []string{}
	for i, v := range values {
		if i >= max {
			break
		}
		if s := identifier(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func summarizeResult(tool string, r wire.Result) map[string]any {
	in := r.StructuredContent
	if in == nil {
		in = map[string]any{}
	}
	out := map[string]any{}
	if strings.HasPrefix(tool, "work_") || tool == "batch" {
		checkpoint := object(in["checkpoint"])
		for _, k := range []string{"run_id", "checkpoint_id"} {
			addID(out, k, first(in[k], checkpoint[k]))
		}
		for _, k := range []string{"iteration_id", "document_id"} {
			addID(out, k, in[k])
		}
		addNumber(out, "revision", first(in["revision"], checkpoint["revision"]))
		copyNumbers(out, in, "document_revision")
		addID(out, "checkpoint_status", checkpoint["status"])
		if a := array(first(in["documents"], checkpoint["documents"])); a != nil {
			out["documents_count"] = len(a)
		}
	}
	if tool == "bash" || tool == "bash_job" {
		addString(out, "job_id", in["job_id"], 40)
		addString(out, "job_status", in["job_status"], 16)
		addString(out, "job_origin", first(in["job_origin"], in["origin"]), 16)
	}
	if contains([]string{"start_jobs", "jobs", "stop_jobs"}, tool) {
		jobs := array(in["jobs"])
		if jobs != nil {
			out["jobs_count"] = len(jobs)
		}
		copyNumbers(out, in, "running_count waited_ms requested_wait_ms")
		copyBools(out, in, "all_finished all_succeeded server_restarting")
		for _, k := range []string{"job_ids", "stopped_ids", "already_finished_ids"} {
			if a := array(in[k]); a != nil {
				out[k] = idsSafe(a, 32)
			}
		}
		out["jobs_truncated"] = len(jobs) > 32
		if jobs != nil {
			items := []map[string]any{}
			for i, v := range jobs {
				if i >= 32 {
					break
				}
				job := object(v)
				item := map[string]any{}
				addID(item, "job_id", job["job_id"])
				addString(item, "status", job["status"], 24)
				copyNumbers(item, job, "exit_code elapsed_ms deadline_in_ms stdout_bytes stderr_bytes returned_bytes available_stdout_bytes available_stderr_bytes")
				copyStrings(item, job, "signal stop_reason", 40)
				addString(item, "origin", job["origin"], 24)
				addString(item, "output_mode", job["output_mode"], 16)
				addString(item, "output_expires_at", job["output_expires_at"], 40)
				copyBools(item, job, "output_truncated output_available output_growing output_complete has_more")
				items = append(items, item)
			}
			out["jobs"] = items
		}
	}
	if tool == "commit_changes" {
		if validCommit(text(in["commit"])) {
			out["commit"] = in["commit"]
		}
		addString(out, "branch", in["branch"], 256)
		copyNumbers(out, in, "file_count")
		copyBools(out, in, "working_tree_clean")
		out["status_available"] = in["working_tree_clean"] != nil && in["status_error"] == nil
		addString(out, "status_error", in["status_error"], 320)
		if a := array(in["skipped_blocked_paths"]); a != nil {
			out["skipped_blocked_count"] = len(a)
		}
	}
	if tool == "batch" {
		if children := array(in["results"]); children != nil {
			out["child_results_truncated"] = len(children) > 32
			items := []map[string]any{}
			for i, v := range children {
				if i >= 32 {
					break
				}
				child := object(v)
				data := object(child["structured"])
				item := map[string]any{}
				addID(item, "id", child["id"])
				addString(item, "tool", child["tool"], 80)
				copyBools(item, child, "ok skipped text_truncated structured_truncated")
				addString(item, "error_code", data["error_code"], 80)
				copyNumbers(item, data, "exit_code bytes")
				addID(item, "job_id", data["job_id"])
				addString(item, "job_status", data["job_status"], 24)
				addPath(item, "path", data["path"])
				if validCommit(text(data["commit"])) {
					item["commit"] = data["commit"]
				}
				items = append(items, item)
			}
			out["child_results"] = items
		}
	}
	if r.IsError {
		out["is_error"] = true
	}
	addID(out, "workspace_id", first(in["workspace_id"], in["selected_workspace_id"]))
	addID(out, "project_id", in["project_id"])
	addPath(out, "path", in["path"])
	addPath(out, "batch_path", in["batch_path"])
	copyBools(out, in, "persisted persistence_default persistence_requested auto_stored retry_unchanged git_excluded pruned_batch_paths_truncated changed created existed already_open succeeded truncated has_more output_limited output_truncated")
	copyNumbers(out, in, "retention_limit pruned_batch_count already_open_count bytes additions deletions replacements edits_applied operation_count total_operation_count start_index executed_operation_count succeeded_count failed_count skipped_count failed_index child_text_truncated_count child_structured_truncated_count workspace_results_truncated_count count")
	addString(out, "batch_source", in["batch_source"], 16)
	addString(out, "batch_tag", in["batch_tag"], 4)
	addString(out, "efficiency_hint", in["efficiency_hint"], 320)
	copyStrings(out, in, "error_code state status start_operation_id failed_operation_id resumable_from", 80)
	addString(out, "query_fingerprint", in["query_fingerprint"], 64)
	addString(out, "search_kind", in["kind"], 16)
	addString(out, "search_scope", in["scope"], 32)
	addString(out, "search_used", in["used"], 32)
	addString(out, "ast_provider", in["provider"], 32)
	addString(out, "ast_provider_version", in["provider_version"], 80)
	addString(out, "ast_mode", in["mode"], 16)
	addString(out, "mode", in["mode"], 32)
	addNumber(out, "exit_code", first(in["exit_code"], in["exitCode"]))
	addNumber(out, "duration_ms", first(in["duration_ms"], in["durationMs"]))
	for _, k := range []string{"changed_files", "changed_paths", "paths", "files", "matches", "contexts", "warnings", "projects", "workspaces"} {
		if a := array(in[k]); a != nil {
			out[k+"_count"] = len(a)
		}
	}
	if (tool == "search" || tool == "ast_grep") && array(in["matches"]) != nil {
		count := 0
		for _, v := range array(in["matches"]) {
			if boolValue(object(v)["editable"]) {
				count++
			}
		}
		out["editable_matches_count"] = count
	}
	if tool == "bash" || tool == "bash_job" {
		copyStrings(out, in, "stop_reason signal", 40)
		addBool(out, "timed_out", first(in["timed_out"], in["timedOut"]))
		for _, stream := range []string{"stdout", "stderr"} {
			if isNumber(in[stream+"_bytes"]) {
				out[stream+"_bytes"] = in[stream+"_bytes"]
			} else {
				addBytes(out, stream+"_bytes", in[stream])
			}
		}
	}
	return out
}

var commitRE = regexp.MustCompile("^[a-f0-9]{40,64}$")

func validCommit(v string) bool { return commitRE.MatchString(v) }
func validStatus(s string) bool {
	return contains([]string{"succeeded", "failed", "timed_out", "cancelled", "blocked"}, s)
}

var normalizeErrorRE = regexp.MustCompile("[^a-z0-9]+")

func normalizeError(s string) string {
	s = strings.Trim(normalizeErrorRE.ReplaceAllString(strings.ToLower(s), "_"), "_")
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		s = "error"
	}
	return s
}
func classify(input RecordInput) (string, string) {
	result := input.Result.StructuredContent
	reported := input.Error != nil || input.Result.IsError
	code := ""
	var wireErr *wire.Error
	if errors.As(input.Error, &wireErr) && wireErr.Code != "" {
		code = normalizeError(wireErr.Code)
	} else if c := text(result["error_code"]); c != "" {
		code = normalizeError(c)
	}
	errorText := text(result["error"]) + "\n" + text(result["stderr"])
	if input.Error != nil {
		errorText = input.Error.Error()
	} else {
		for _, c := range input.Result.Content {
			if c["type"] == "text" {
				errorText += "\n" + text(c["text"])
			}
		}
	}
	lower := strings.ToLower(errorText)
	timed := (input.ToolName == "bash" || input.ToolName == "bash_job") && (boolValue(first(result["timed_out"], result["timedOut"])) || strings.Contains(strings.ToLower(text(result["stderr"])), "[codexpro] command timed out after "))
	if timed || (reported && code == "" && strings.Contains(lower, "timed out")) {
		return "timed_out", "timeout"
	}
	if errors.Is(input.Error, context.Canceled) {
		return "cancelled", "cancelled"
	}
	if input.ToolName == "bash" || input.ToolName == "bash_job" {
		if result["job_status"] == "running" {
			return "succeeded", ""
		}
		exit := first(result["exit_code"], result["exitCode"])
		if isNumber(exit) && number(exit) != 0 {
			return "failed", fmt.Sprintf("command_exit_%v", number(exit))
		}
		if signal := bounded(result["signal"], 40); signal != "" {
			return "failed", "command_signal_" + normalizeError(signal)
		}
		if (result["exit_code"] == nil && hasKey(result, "exit_code")) || (!isNumber(exit) && input.Result.IsError) {
			return "failed", "command_failed"
		}
	}
	if reported {
		if code != "" {
			if strings.HasPrefix(code, "policy_") || strings.HasSuffix(code, "_blocked") {
				return "blocked", code
			}
			return "failed", code
		}
		for _, needle := range []string{"blocked", "disabled", "forbidden", "unauthorized", "unauthorised", "outside allowed", "not in the safe", "safe bash allowlist", "not available in the current mode", "permission denied", "approval"} {
			if strings.Contains(lower, needle) {
				return "blocked", "policy_blocked"
			}
		}
		if strings.Contains(lower, "invalid argument") {
			return "failed", "invalid_arguments"
		}
		for _, n := range []string{"changed since", "stale", "conflict", "already exists"} {
			if strings.Contains(lower, n) {
				return "failed", "conflict"
			}
		}
		for _, n := range []string{"not found", "does not exist", "unknown project", "unknown workspace"} {
			if strings.Contains(lower, n) {
				return "failed", "not_found"
			}
		}
		return "failed", "tool_error"
	}
	return "succeeded", ""
}
func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

type descriptor struct {
	operation, class string
	mutating         bool
}

var descriptors = map[string]descriptor{
	"server_config": {"server.config", "administrative", false}, "list_projects": {"project.list", "lifecycle", false}, "create_project": {"project.create", "lifecycle", true},
	"codexpro_self_test": {"server.self_test", "administrative", true}, "load_skill": {"capability.read", "read", false}, "list_workspaces": {"workspace.list", "lifecycle", false},
	"create_workspace": {"workspace.create", "lifecycle", true}, "release_workspace": {"workspace.release", "lifecycle", true}, "remove_workspace": {"workspace.remove", "lifecycle", true},
	"open_workspace": {"workspace.open", "lifecycle", false}, "open_current_workspace": {"workspace.open", "lifecycle", false}, "inspect_workspace": {"workspace.inspect", "analysis", false},
	"tree": {"file.list", "read", false}, "search": {"file.search", "read", false}, "ast_grep": {"file.ast_search", "read", false}, "read": {"file.read", "read", false}, "view_image": {"file.image", "read", false},
	"write": {"file.write", "write", true}, "edit": {"file.edit", "write", true}, "apply_patch": {"file.patch", "write", true}, "import_file": {"file.import", "write", true},
	"bash": {"command.run", "execute", true}, "bash_job": {"command.background", "execute", true}, "start_jobs": {"job.start", "execute", true}, "jobs": {"job.collect", "read", false}, "stop_jobs": {"job.stop", "execute", true},
	"show_changes": {"git.review", "git", false}, "commit_changes": {"git.commit", "git", true},
}

func describe(input RecordInput) descriptor {
	if input.ToolName == "work_status" {
		action := text(input.Args["action"])
		if !contains(strings.Fields("list get read_document search_memory history operation"), action) {
			action = "list"
		}
		return descriptor{"work." + action, "read", false}
	}
	if input.ToolName == "batch" {
		class := "read"
		if input.Mutating {
			class = "write"
		}
		return descriptor{"batch.execute", class, input.Mutating}
	}
	if d, ok := descriptors[input.ToolName]; ok {
		return d
	}
	if input.Mutating {
		return descriptor{"tool.mutate", "write", true}
	}
	return descriptor{"tool.read", "read", false}
}
func requestPaths(tool string, args map[string]any) []string {
	paths := []string{}
	switch tool {
	case "read", "write", "edit", "view_image":
		paths = append(paths, relative(args["path"]))
	case "import_file":
		paths = append(paths, relative(first(args["destination"], args["path"], args["target_path"])))
	case "apply_patch":
		paths = append(paths, patchPaths(args["patch"])...)
	case "batch":
		paths = append(paths, relative(args["path"]))
		for _, v := range array(args["operations"]) {
			op := object(v)
			name := text(op["tool"])
			if name != "" && name != "batch" {
				paths = append(paths, requestPaths(name, object(op["args"]))...)
			}
		}
	case "codexpro_self_test":
		if boolValue(args["write_probe"]) {
			paths = append(paths, ".ai-bridge/codexpro-self-test.md")
		}
	}
	paths, _ = unique(paths, 24)
	return paths
}
func resultPaths(result map[string]any) []string {
	paths := []string{relative(result["path"])}
	for _, k := range []string{"paths", "changed_paths", "changed_files"} {
		for _, v := range array(result[k]) {
			if s, ok := v.(string); ok {
				if idx := strings.LastIndex(s, "\t"); idx >= 0 {
					s = s[idx+1:]
				}
				paths = append(paths, relative(s))
			} else {
				paths = append(paths, relative(object(v)["path"]))
			}
		}
	}
	paths, _ = unique(paths, 24)
	return paths
}
func indexedEvidence(values any, key string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, v := range array(values) {
		m := object(v)
		if s := text(m[key]); s != "" {
			out[s] = m
		}
	}
	return out
}
func changedPaths(input RecordInput, status string) ([]string, int, bool) {
	candidates := []string{}
	if input.Mutating && status == "succeeded" {
		candidates = append(candidates, resultPaths(input.Result.StructuredContent)...)
	}
	beforeGit, afterGit := object(input.Before["git"]), object(input.After["git"])
	truncated := false
	if len(beforeGit) > 0 && len(afterGit) > 0 && beforeGit["status_fingerprint"] != afterGit["status_fingerprint"] {
		before := indexedEvidence(beforeGit["path_states"], "path")
		after := indexedEvidence(afterGit["path_states"], "path")
		keys := []string{}
		for k := range before {
			keys = append(keys, k)
		}
		for k := range after {
			keys = append(keys, k)
		}
		keys, _ = unique(keys, len(keys))
		sort.Strings(keys)
		for _, k := range keys {
			if before[k]["state_fingerprint"] != after[k]["state_fingerprint"] {
				candidates = append(candidates, k)
			}
		}
		truncated = boolValue(beforeGit["changed_paths_truncated"]) || boolValue(afterGit["changed_paths_truncated"])
	} else if len(beforeGit) == 0 && boolValue(afterGit["dirty"]) {
		for _, v := range array(afterGit["changed_paths"]) {
			candidates = append(candidates, text(v))
		}
		truncated = boolValue(afterGit["changed_paths_truncated"])
	}
	before, after := indexedEvidence(input.Before["paths"], "path"), indexedEvidence(input.After["paths"], "path")
	keys := []string{}
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		keys = append(keys, k)
	}
	keys, _ = unique(keys, len(keys))
	sort.Strings(keys)
	for _, k := range keys {
		a, _ := json.Marshal(before[k])
		b, _ := json.Marshal(after[k])
		if !bytes.Equal(a, b) {
			candidates = append(candidates, k)
		}
	}
	all, _ := unique(candidates, len(candidates))
	bounded, trimmed := unique(all, 24)
	return bounded, len(all), truncated || trimmed
}
func dashboardScripts(input RecordInput) []map[string]any {
	scripts := []map[string]any{}
	switch input.ToolName {
	case "bash", "bash_job":
		if command, ok := input.Args["command"].(string); ok {
			scripts = append(scripts, map[string]any{"script": command})
		}
	case "start_jobs":
		jobs := array(input.Result.StructuredContent["jobs"])
		for i, v := range array(input.Args["commands"]) {
			if i >= 32 {
				break
			}
			id := fmt.Sprintf("job_%d", i+1)
			if i < len(jobs) && text(object(jobs[i])["job_id"]) != "" {
				id = text(object(jobs[i])["job_id"])
			}
			scripts = append(scripts, map[string]any{"operation_id": id, "script": text(object(v)["command"])})
		}
	case "batch":
		for _, v := range array(input.DashboardMetadata["shell_scripts"]) {
			scripts = append(scripts, object(v))
		}
	}
	return boundScripts(scripts, 64*1024)
}
func truncateUTF8(s string, max int) (string, bool) {
	if len([]byte(s)) <= max {
		return s, false
	}
	if max < 0 {
		max = 0
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max], true
}
func boundScripts(scripts []map[string]any, max int) []map[string]any {
	out := []map[string]any{}
	for _, s := range scripts {
		if max <= 0 {
			break
		}
		value, ok := s["script"].(string)
		if !ok {
			continue
		}
		v, trimmed := truncateUTF8(value, max)
		item := map[string]any{"script": v}
		addString(item, "operation_id", s["operation_id"], 80)
		if boolValue(s["truncated"]) || trimmed {
			item["truncated"] = true
		}
		out = append(out, item)
		max -= len([]byte(v))
	}
	return out
}
func (j *Journal) build(input RecordInput, sequence int64) (Action, error) {
	started, finished := input.StartedAt, input.FinishedAt
	if started.IsZero() {
		started = input.Scope.StartedAt
	}
	if started.IsZero() {
		started = j.options.Clock()
	}
	if finished.IsZero() {
		finished = j.options.Clock()
	}
	d := describe(input)
	status, errorCode := classify(input)
	actionID := input.ActionID
	if actionID == "" {
		actionID = randomID("cpa_")
	}
	actor, request := opaque("actor", input.Scope.PrincipalID), opaque("request", input.Scope.RequestID)
	changed, count, truncated := changedPaths(input, status)
	targets := append(requestPaths(input.ToolName, input.Args), resultPaths(input.Result.StructuredContent)...)
	result := input.Result.StructuredContent
	projectID := identifier(first(input.Args["project_id"], result["project_id"], result["selected_project_id"], input.After["project_id"], input.Before["project_id"]))
	workspaceID := identifier(first(input.Args["workspace_id"], result["workspace_id"], result["selected_workspace_id"], input.After["workspace_id"], input.Before["workspace_id"]))
	targetProject := identifier(first(input.Args["project_id"], result["project_id"], result["selected_project_id"]))
	if targetProject != "" {
		targets = append(targets, "project:"+targetProject)
	}
	if workspaceID != "" && contains([]string{"create_workspace", "release_workspace", "remove_workspace"}, input.ToolName) {
		targets = append(targets, "workspace:"+workspaceID)
	}
	if input.ToolName == "create_project" {
		if id := identifier(input.Args["parent_id"]); id != "" {
			targets = append(targets, "parent:"+id)
		}
		if p := relative(input.Args["directory"]); p != "" {
			targets = append(targets, "directory:"+p)
		}
	}
	for _, e := range []map[string]any{input.Before, input.After} {
		for _, v := range array(e["targets"]) {
			targets = append(targets, text(v))
		}
	}
	targets, _ = unique(targets, 40)
	if input.Target != nil {
		projectID, workspaceID = identifier(input.Target.ProjectID), identifier(input.Target.WorkspaceID)
	}
	requestMeta, resultMeta := summarizeArgs(input.ToolName, input.Args), summarizeResult(input.ToolName, input.Result)
	var wireErr *wire.Error
	if errors.As(input.Error, &wireErr) {
		addString(resultMeta, "error_code", wireErr.Code, 80)
		if wireErr.Data != nil {
			copyBools(resultMeta, wireErr.Data, "retry_unchanged")
			addString(resultMeta, "recovery_tool", object(wireErr.Data["recovery"])["tool"], 32)
		}
	}
	duration := finished.UnixMilli() - started.UnixMilli()
	if duration < 0 {
		duration = 0
	}
	action := Action{"schema_version": "1.0", "namespace": "debug", "sequence": sequence, "action_id": actionID, "occurred_at": timestamp(started), "finished_at": timestamp(finished), "tool_name": input.ToolName, "operation": d.operation, "operation_class": d.class, "mutating": input.Mutating || d.mutating, "invocation_surface": "direct", "actor_ref": actor, "request_ref": request, "server_session_ref": j.serverSessionRef, "status": status, "duration_ms": duration, "targets": targets, "changed_paths": changed, "changed_path_count": count, "changed_paths_truncated": truncated, "request_metadata": requestMeta, "result_metadata": resultMeta, "result_ref": "codexpro://actions/" + actionID}
	if input.InvocationSurface != "" {
		action["invocation_surface"] = input.InvocationSurface
	}
	if projectID != "" {
		action["project_id"] = projectID
	}
	if workspaceID != "" {
		action["workspace_id"] = workspaceID
	}
	if input.Target != nil {
		action["audit_scope"] = input.Target.Scope
		if input.Target.RunID != "" {
			action["run_id"] = input.Target.RunID
		}
	}
	transport := j.serverSessionRef
	if input.Scope.TransportID != "" {
		transport = opaque("transport", input.Scope.TransportID)
		action["transport_session_ref"] = transport
	}
	if input.Scope.RequestID != "" {
		action["request_fingerprint"] = digest(strings.Join([]string{actor, transport, request, input.ToolName}, "\x00"))
	}
	for key, v := range map[string]any{"git_before": input.Before["git"], "git_after": input.After["git"]} {
		if len(object(v)) > 0 {
			action[key] = v
		}
	}
	if a := array(input.Before["paths"]); len(a) > 0 {
		action["path_evidence_before"] = a
	}
	if a := array(input.After["paths"]); len(a) > 0 {
		action["path_evidence_after"] = a
	}
	if receipt := object(result["work_receipt"]); receipt["run_id"] != nil && receipt["operation_id"] != nil {
		action["work"] = map[string]any{"run_id": receipt["run_id"], "iteration_id": receipt["iteration_id"], "generation": receipt["generation"], "operation_id": receipt["operation_id"]}
	}
	dashboard := map[string]any{}
	scripts := dashboardScripts(input)
	for _, s := range scripts {
		s["script"] = j.options.Redact(text(s["script"]))
	}
	if len(scripts) > 0 {
		dashboard["shell_scripts"] = scripts
	}
	errorMessage := text(result["error"])
	if errorMessage == "" && wireErr != nil {
		errorMessage = map[string]string{"ENOENT": "The requested file or directory was not found. Check the recorded target and refresh its path.", "EACCES": "The requested file or directory could not be accessed.", "edit_tag_stale": "The file changed after it was read. Read it again before editing.", "edit_tag_unknown": "The edit snapshot is unavailable. Read the file again before editing.", "nothing_to_commit": "No eligible changes were available to commit. Review the working tree.", "job_not_found": "The requested job is unavailable or expired.", "policy_blocked": "This operation was blocked by the configured workspace policy."}[wireErr.Code]
	}
	if errorMessage != "" {
		value, trimmed := truncateUTF8(j.options.Redact(errorMessage), 2048)
		if trimmed {
			value += "… [truncated]"
		}
		dashboard["error_message"] = value
	}
	if recovery := text(object(result["recovery"])["message"]); recovery != "" {
		value, trimmed := truncateUTF8(j.options.Redact(recovery), 1024)
		if trimmed {
			value += "… [truncated]"
		}
		dashboard["recovery_message"] = value
	}
	if len(dashboard) > 0 {
		action["dashboard_metadata"] = dashboard
	}
	if errorCode != "" {
		action["error_code"] = errorCode
	}
	summary := input.ToolName + " " + status
	if input.ToolName == "bash" && isNumber(resultMeta["exit_code"]) {
		summary += fmt.Sprintf("; exit %v", number(resultMeta["exit_code"]))
	}
	if count > 0 {
		summary += fmt.Sprintf("; %d changed path", count)
		if count != 1 {
			summary += "s"
		}
	}
	action["summary"] = summary
	return action, nil
}
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func serializeAction(action Action) ([]byte, error) {
	data, err := encode(action)
	if err != nil {
		return nil, err
	}
	if len(data) <= 131072 {
		return data, nil
	}
	dashboard := object(action["dashboard_metadata"])
	scripts := []map[string]any{}
	for _, v := range array(dashboard["shell_scripts"]) {
		scripts = append(scripts, object(v))
	}
	if len(scripts) == 0 {
		return nil, errors.New("Action record exceeded 131072 bytes")
	}
	low, high := 0, 64*1024
	var best []byte
	for low <= high {
		mid := (low + high) / 2
		candidate := clone(action)
		meta := object(candidate["dashboard_metadata"])
		trimmed := boundScripts(scripts, mid)
		if len(trimmed) > 0 {
			meta["shell_scripts"] = trimmed
		} else {
			delete(meta, "shell_scripts")
		}
		if len(meta) == 0 {
			delete(candidate, "dashboard_metadata")
		}
		b, e := encode(candidate)
		if e != nil {
			return nil, e
		}
		if len(b) <= 131072 {
			best = b
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == nil {
		return nil, errors.New("Action record exceeded 131072 bytes")
	}
	return best, nil
}
