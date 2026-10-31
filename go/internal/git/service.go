package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/search"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Service struct {
	Config     *config.Config
	Workspaces wire.Resolver
	Files      *files.Service
	Analysis   *search.Service
	mu         sync.Mutex
	reviews    map[string]string
}

func New(cfg *config.Config, resolver wire.Resolver, fileService *files.Service, analysis *search.Service) (*Service, error) {
	return &Service{Config: cfg, Workspaces: resolver, Files: fileService, Analysis: analysis, reviews: map[string]string{}}, nil
}
func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, a map[string]any) (wire.Result, error) {
	w, e := s.Workspaces.Resolve(ctx, scope, wire.String(a, "workspace_id", ""))
	if e != nil {
		return wire.Result{}, e
	}
	switch name {
	case "show_changes":
		return s.show(ctx, scope, w, a)
	case "commit_changes":
		return s.commit(ctx, scope, w, a)
	}
	return wire.Result{}, wire.Errorf("tool_unknown", "Unknown Git tool: %s", name)
}
func failure(code, message string, recovery map[string]any) error {
	data := map[string]any{"retry_unchanged": false}
	if recovery != nil {
		data["recovery"] = recovery
	}
	return &wire.Error{Code: code, Message: message, Data: data}
}
func run(ctx context.Context, w wire.Workspace, args []string, limit int) string {
	r := files.RunCommand(ctx, w.Root, "git", args, "", limit, 0)
	if r.Err != nil {
		return "git unavailable or failed: " + r.Err.Error()
	}
	if r.ExitCode != 0 {
		detail := files.TrimJS(r.Stderr)
		if detail == "" {
			detail = files.TrimJS(r.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("git exited with status %d", r.ExitCode)
		}
		return detail
	}
	result := files.TrimJS(r.Stdout)
	if result == "" {
		result = "(no output)"
	}
	return wire.Redact(result)
}
func IsFailure(value string) bool {
	lower := strings.ToLower(files.TrimJS(value))
	return strings.HasPrefix(lower, "fatal:") || strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "git unavailable or failed:") || strings.HasPrefix(lower, "git exited with status") || strings.HasPrefix(lower, "usage: git ") || strings.Contains(lower, "not a git repository")
}
func looksLikeError(value string) bool {
	text := files.TrimJS(value)
	return strings.HasPrefix(text, "fatal:") || strings.HasPrefix(text, "error:") || strings.HasPrefix(text, "git unavailable or failed:") || strings.HasPrefix(text, "git exited with status") || strings.HasPrefix(text, "usage: git ") || strings.Contains(strings.ToLower(text), "not a git repository")
}
func normalized(value string) string {
	if files.TrimJS(value) == "(no output)" {
		return ""
	}
	return value
}
func outputLines(value string) []string {
	out := []string{}
	if files.TrimJS(value) == "(no output)" {
		return out
	}
	for _, line := range strings.Split(value, "\n") {
		if v := files.TrimJS(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (s *Service) Status(ctx context.Context, w wire.Workspace, path string, staged bool) (string, error) {
	args := []string{"status", "--short", "--branch"}
	if staged {
		args = []string{"diff", "--cached", "--name-status"}
	}
	if files.TrimJS(path) != "" {
		_, rel, e := s.Files.ResolvePath(w, path, false)
		if e != nil {
			return "", e
		}
		args = append(args, "--", rel)
	}
	return run(ctx, w, args, s.Config.Int("maxOutputBytes", 120000)), nil
}
func (s *Service) Log(ctx context.Context, w wire.Workspace, count int) string {
	return run(ctx, w, []string{"log", fmt.Sprintf("--max-count=%d", max(1, min(30, count))), "--oneline", "--decorate"}, s.Config.Int("maxOutputBytes", 120000))
}
func (s *Service) Diff(ctx context.Context, w wire.Workspace, path string, staged bool) (string, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv"}
	if staged {
		args = append(args, "--staged")
	}
	if files.TrimJS(path) != "" {
		_, rel, e := s.Files.ResolvePath(w, path, false)
		if e != nil {
			return "", e
		}
		args = append(args, "--", rel)
	}
	return run(ctx, w, args, s.Config.Int("maxOutputBytes", 120000)), nil
}
func (s *Service) DiffStatus(ctx context.Context, w wire.Workspace, path string, staged bool) (string, error) {
	args := []string{"diff", "--name-status"}
	if staged {
		args = append(args, "--staged")
	}
	untracked := []string{"ls-files", "--others", "--exclude-standard"}
	if files.TrimJS(path) != "" {
		_, rel, e := s.Files.ResolvePath(w, path, false)
		if e != nil {
			return "", e
		}
		args = append(args, "--", rel)
		untracked = append(untracked, "--", rel)
	}
	diff := run(ctx, w, args, s.Config.Int("maxOutputBytes", 120000))
	if staged || IsFailure(diff) {
		return diff, nil
	}
	extra := run(ctx, w, untracked, s.Config.Int("maxOutputBytes", 120000))
	if IsFailure(extra) {
		return diff, nil
	}
	lines := outputLines(diff)
	for _, p := range outputLines(extra) {
		lines = append(lines, "?? "+p)
	}
	if len(lines) == 0 {
		return "(no output)", nil
	}
	return strings.Join(lines, "\n"), nil
}
func diffStats(diff string) (additions, deletions int, changed bool) {
	for _, l := range strings.Split(diff, "\n") {
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			additions++
		}
		if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			deletions++
		}
	}
	return additions, deletions, files.TrimJS(diff) != ""
}
func changedLines(status string) []string {
	out := []string{}
	for _, v := range outputLines(status) {
		if !strings.HasPrefix(v, "##") {
			out = append(out, v)
		}
	}
	return out
}

var twoStatus = regexp.MustCompile(`^.{2}\s`)

func ChangedPaths(lines []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, line := range lines {
		raw := ""
		switch {
		case strings.HasPrefix(line, "?? "):
			raw = files.TrimJS(files.JSSlice(line, 3, files.JSLength(line)))
		case strings.Contains(line, "\t"):
			parts := strings.Split(line, "\t")
			raw = files.TrimJS(parts[len(parts)-1])
		case twoStatus.MatchString(line):
			raw = files.TrimJS(files.JSSlice(line, 3, files.JSLength(line)))
		default:
			continue
		}
		if strings.Contains(raw, " -> ") {
			parts := strings.Split(raw, " -> ")
			raw = parts[len(parts)-1]
		}
		decoded := files.DecodeGitPath(raw)
		if decoded != "" && !seen[decoded] {
			seen[decoded] = true
			out = append(out, decoded)
		}
	}
	return out
}

var untrackedLine = regexp.MustCompile(`^\?\?\s+(.+)$`)

func (s *Service) untrackedFingerprint(w wire.Workspace, lines []string) string {
	h := sha256.New()
	for _, line := range lines {
		m := untrackedLine.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		rel := m[1]
		_, _ = h.Write([]byte(rel + "\x00"))
		abs, _, e := s.Files.ResolvePath(w, rel, false)
		if e == nil {
			var st os.FileInfo
			st, e = os.Stat(abs)
			if e == nil {
				_, _ = h.Write([]byte(strconv.FormatInt(st.Size(), 10) + "\x00" + strconv.FormatInt(st.ModTime().UnixMilli(), 10) + "\x00"))
				if st.Mode().IsRegular() && st.Size() <= int64(s.Config.Int("maxReadBytes", 180000)) {
					var b []byte
					b, e = os.ReadFile(abs)
					if e == nil {
						_, _ = h.Write(b)
					} else {
						e = files.FileError(e, "open", abs)
					}
				}
			} else {
				e = files.FileError(e, "stat", abs)
			}
		}
		if e != nil {
			_, _ = h.Write([]byte(e.Error()))
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) ForgetSession(principal, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := principal + "\x00" + session + "\x00"
	for key := range s.reviews {
		if strings.HasPrefix(key, prefix) {
			delete(s.reviews, key)
		}
	}
}
func (s *Service) show(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	rawPath := wire.String(a, "path", "")
	path := ""
	if files.TrimJS(rawPath) != "" {
		_, rel, e := s.Files.ResolvePath(w, rawPath, false)
		if e != nil {
			return wire.Result{}, e
		}
		path = rel
	}
	staged, includeDiff := wire.Bool(a, "staged", false), wire.Bool(a, "include_diff", true)
	status, e := s.DiffStatus(ctx, w, path, staged)
	if e != nil {
		return wire.Result{}, e
	}
	status = normalized(status)
	diff, e := s.Diff(ctx, w, path, staged)
	if e != nil {
		return wire.Result{}, e
	}
	diff = normalized(diff)
	statusError, diffError := "", ""
	if looksLikeError(status) {
		statusError = status
	}
	if diff != "" && looksLikeError(diff) {
		diffError = diff
		diff = ""
	}
	additions, deletions, diffChanged := diffStats(diff)
	changed := []string{}
	untracked := ""
	if statusError == "" {
		changed = changedLines(status)
		untracked = s.untrackedFingerprint(w, changed)
	}
	since := "workspace"
	if wire.String(a, "since", "") == "last_shown" {
		since = "last_shown"
	}
	mark := wire.Bool(a, "mark_reviewed", true) && includeDiff
	stage := "unstaged"
	if staged {
		stage = "staged"
	}
	key := scope.PrincipalID + "\x00" + scope.SessionID + "\x00" + w.ID + "\x00" + path + "\x00" + stage
	fingerprint := files.SHA(status + "\x00" + diff + "\x00" + untracked)
	s.mu.Lock()
	hit := includeDiff && since == "last_shown" && s.reviews[key] == fingerprint
	if mark {
		s.reviews[key] = fingerprint
	}
	s.mu.Unlock()
	responseDiff := diff
	if hit || !includeDiff {
		responseDiff = ""
	}
	if hit {
		additions, deletions, diffChanged = 0, 0, false
	}
	paths := []string{}
	if statusError == "" {
		paths = ChangedPaths(changed)
	}
	var analysis map[string]any
	if s.Config.Bool("analysisEnabled", true) && len(paths) > 0 && !hit {
		if s.Analysis != nil {
			impact, e := s.Analysis.Review(ctx, w, paths)
			if e == nil {
				analysis = impact.Wire()
			} else {
				analysis = map[string]any{"schema_version": 1, "changed_paths": paths, "affected_areas": []string{}, "dependent_files": []any{}, "related_tests": []any{}, "risk_signals": []any{}, "recommended_commands": []any{}, "warnings": []string{"Change analysis unavailable: " + e.Error()}}
			}
		}
	}
	changedText := "- No changed files."
	if statusError != "" {
		changedText = "- Git status unavailable: " + statusError
	} else if hit {
		changedText = "- No changes since last shown review."
	} else if len(changed) > 0 {
		changedText = "- " + strings.Join(changed, "\n- ")
	}
	diffText := "\n\nDiff omitted by request."
	if hit {
		diffText = "\n\nNo new diff since last shown review."
	} else if includeDiff {
		if diffError != "" {
			diffText = "\n\nGit diff unavailable: " + diffError
		} else if diff != "" {
			diffText = files.DiffBlock(diff)
		} else {
			diffText = "\n\nNo diff output."
		}
	}
	analysisText := ""
	if analysis != nil {
		areas, _ := analysis["affected_areas"].([]string)
		risks := []string{}
		tests := []string{}
		if rs, ok := analysis["risk_signals"].([]search.Risk); ok {
			for _, r := range rs {
				if r.Label != "" {
					risks = append(risks, r.Label)
				}
			}
		}
		if ts, ok := analysis["related_tests"].([]search.ImpactFile); ok {
			for _, t := range ts {
				if t.Path != "" {
					tests = append(tests, t.Path)
				}
			}
		}
		join := func(v []string) string {
			if len(v) == 0 {
				return "none"
			}
			return strings.Join(v, ", ")
		}
		analysisText = "\n\n## Analysis\n\nAffected areas: " + join(areas) + "\nRisks: " + join(risks) + "\nRelated tests: " + join(tests)
	}
	text := fmt.Sprintf("# Show Changes\n\nWorkspace: %s\n\n## Changed\n\n%s\n\n## Diff stats\n\n+%d -%d%s%s", w.Root, changedText, additions, deletions, diffText, analysisText)
	data := files.Base(w)
	displayPath := "workspace changes"
	if _, ok := a["path"]; ok {
		displayPath = rawPath
	}
	data["path"] = displayPath
	data["status"] = status
	if statusError != "" {
		data["status_error"] = statusError
	}
	if diffError != "" {
		data["diff_error"] = diffError
	}
	if hit {
		data["changed_files"] = []string{}
	} else {
		data["changed_files"] = changed
	}
	data["staged"] = staged
	data["include_diff"] = includeDiff
	data["additions"] = additions
	data["deletions"] = deletions
	data["changed"] = statusError == "" && !hit && (len(changed) > 0 || diffChanged)
	data["diff"] = responseDiff
	data["review_since"] = since
	data["review_marked"] = mark
	data["review_checkpoint_hit"] = hit
	if analysis != nil {
		data["analysis"] = analysis
	}
	return wire.Success("show_changes", "Show Changes", data, text), nil
}
func (s *Service) commit(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	message := files.TrimJS(wire.String(a, "message", ""))
	if message == "" {
		return wire.Result{}, failure("args_invalid", "message is required.", nil)
	}
	release := s.Files.LockPaths(w.Root + "/.git")
	defer release()
	inside := run(ctx, w, []string{"rev-parse", "--is-inside-work-tree"}, 8*1024)
	if IsFailure(inside) || files.TrimJS(inside) != "true" {
		return wire.Result{}, failure("git_unavailable", "Not a Git working tree: "+inside, nil)
	}
	candidates := []string{}
	explicit := wire.Strings(a, "paths")
	if len(explicit) > 0 {
		for _, p := range explicit {
			_, rel, e := s.Files.ResolvePath(w, p, false)
			if e != nil {
				return wire.Result{}, e
			}
			candidates = append(candidates, rel)
		}
	} else {
		status := run(ctx, w, []string{"status", "--porcelain=v1", "-uall", "-z"}, s.Config.Int("maxOutputBytes", 120000))
		if IsFailure(status) {
			return wire.Result{}, failure("git_unavailable", status, nil)
		}
		// Preserve the deployed parser until its leading-space/rename defect has an approved contract correction.
		if status != "(no output)" {
			for _, record := range strings.Split(status, "\x00") {
				if files.JSLength(record) > 3 {
					candidates = append(candidates, files.JSSlice(record, 3, files.JSLength(record)))
				}
			}
		}
	}
	selected, blocked := []string{}, []string{}
	for _, p := range candidates {
		if s.Files.IsBlocked(p) {
			blocked = append(blocked, p)
		} else {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 {
		message := "Nothing to commit: the working tree has no changes."
		if len(blocked) > 0 {
			message = "Nothing to commit: every changed path is blocked by safety rules (" + strings.Join(blocked, ", ") + ")."
		}
		return wire.Result{}, failure("nothing_to_commit", message, map[string]any{"tool": "show_changes", "message": "Review the working tree first."})
	}
	staged := run(ctx, w, append([]string{"add", "--"}, selected...), s.Config.Int("maxOutputBytes", 120000))
	if IsFailure(staged) {
		return wire.Result{}, failure("git_command_failed", staged, nil)
	}
	result := files.RunCommand(ctx, w.Root, "git", append([]string{"commit", "--quiet", "--only", "-F", "-", "--"}, selected...), message, s.Config.Int("maxOutputBytes", 120000), 0)
	if result.Err != nil || result.ExitCode != 0 {
		detail := ""
		if result.Err != nil {
			detail = result.Err.Error()
		}
		if detail == "" {
			detail = files.TrimJS(result.Stderr)
		}
		if detail == "" {
			detail = files.TrimJS(result.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("git exited with status %d", result.ExitCode)
		}
		return wire.Result{}, failure("git_command_failed", "git commit failed: "+wire.Redact(detail), nil)
	}
	sha := files.TrimJS(run(ctx, w, []string{"rev-parse", "HEAD"}, 8*1024))
	branch := files.TrimJS(run(ctx, w, []string{"branch", "--show-current"}, 8*1024))
	if branch == "(no output)" {
		branch = "detached"
	}
	summary := run(ctx, w, []string{"show", "--stat", "--oneline", "--no-color", "-1", "HEAD"}, s.Config.Int("maxOutputBytes", 120000))
	post := files.RunCommand(ctx, w.Root, "git", []string{"status", "--porcelain=v1", "-uall"}, "", s.Config.Int("maxOutputBytes", 120000), 0)
	status := ""
	var statusError any
	var clean any
	if post.Err != nil || post.ExitCode != 0 {
		detail := ""
		if post.Err != nil {
			detail = post.Err.Error()
		}
		if detail == "" {
			detail = files.TrimJS(post.Stderr)
		}
		if detail == "" {
			detail = fmt.Sprintf("git status exited with status %d", post.ExitCode)
		}
		statusError = wire.Redact(detail)
	} else {
		status = wire.Redact(strings.TrimRightFunc(post.Stdout, func(r rune) bool { return files.TrimJS(string(r)) == "" }))
		clean = status == ""
	}
	skipped := ""
	if len(blocked) > 0 {
		suffix := "s"
		if len(blocked) == 1 {
			suffix = ""
		}
		skipped = fmt.Sprintf("\n\nSkipped %d blocked path%s (not staged): %s", len(blocked), suffix, strings.Join(blocked, ", "))
	}
	verification := ""
	if statusError != nil {
		verification = "Working-tree status unavailable: " + statusError.(string)
	} else if clean == true {
		verification = "Working tree: clean."
	} else {
		verification = "Working tree: changes remain.\n\n" + status
	}
	text := fmt.Sprintf("# Commit\n\nCommit: %s\nBranch: %s\n\n%s%s\n\n%s", sha, branch, summary, skipped, verification)
	data := files.Base(w)
	data["commit"] = sha
	data["commit_short"] = files.JSSlice(sha, 0, 12)
	data["working_tree_clean"] = clean
	data["status"] = status
	data["status_error"] = statusError
	data["branch"] = branch
	data["files"] = selected
	data["file_count"] = len(selected)
	data["skipped_blocked_paths"] = blocked
	return wire.Success("commit_changes", "Commit Changes", data, text), nil
}
