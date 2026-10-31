package git

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type dashboardRun struct {
	ok        bool
	stdout    string
	truncated bool
}

func dashboardGit(ctx context.Context, root string, args []string, limit int) dashboardRun {
	r := files.RunCommandEnv(ctx, root, "git", args, "", limit, 4*time.Second, []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"})
	truncated := r.Truncated || len(r.Stdout) >= limit
	if truncated && r.Stdout != "" {
		return dashboardRun{true, files.JSSlice(r.Stdout, 0, limit) + "\n… diff output truncated by CodexPro …\n", true}
	}
	return dashboardRun{r.Err == nil && r.ExitCode == 0, r.Stdout, false}
}
func dashboardUnavailable(message string) map[string]any {
	return map[string]any{"available": false, "message": message, "dirty": false, "trackedChangedPaths": []string{}, "untrackedPaths": []string{}, "hiddenPathCount": 0, "omittedPathCount": 0, "additions": 0, "deletions": 0, "diffAvailable": false}
}
func normalizeDashboardPath(value string) string {
	if value == "" {
		return "."
	}
	return strings.TrimPrefix(filepath.ToSlash(value), "./")
}
func (s *Service) SafeDashboardPath(value string) bool {
	path := normalizeDashboardPath(value)
	return path != "" && path != "." && path != ".." && !filepath.IsAbs(path) && !strings.HasPrefix(path, "../") && !s.Files.IsBlocked(path)
}
func dashboardPaths(raw string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range strings.Split(raw, "\x00") {
		if v == "" {
			continue
		}
		v = normalizeDashboardPath(v)
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var insertionsPattern = regexp.MustCompile(`(?:^|,)\s*(\d+) insertions?\(\+\)`)
var deletionsPattern = regexp.MustCompile(`(?:^|,)\s*(\d+) deletions?\(-\)`)

func shortStat(text string) (int, int) {
	a, d := 0, 0
	if m := insertionsPattern.FindStringSubmatch(text); len(m) > 1 {
		a, _ = strconv.Atoi(m[1])
	}
	if m := deletionsPattern.FindStringSubmatch(text); len(m) > 1 {
		d, _ = strconv.Atoi(m[1])
	}
	return a, d
}
func (s *Service) dashboardStatus(ctx context.Context, p wire.Project) map[string]any {
	if st, e := os.Stat(p.Root); e != nil || !st.IsDir() {
		return dashboardUnavailable("Project root is unavailable.")
	}
	inside := dashboardGit(ctx, p.Root, []string{"rev-parse", "--is-inside-work-tree"}, 8*1024)
	if !inside.ok || files.TrimJS(inside.stdout) != "true" {
		return dashboardUnavailable("Not a Git working tree.")
	}
	verified := dashboardGit(ctx, p.Root, []string{"rev-parse", "--verify", "HEAD"}, 8*1024)
	hasHead := verified.ok
	branch := dashboardGit(ctx, p.Root, []string{"branch", "--show-current"}, 8*1024)
	head, committed := dashboardRun{}, dashboardRun{}
	if hasHead {
		head = dashboardGit(ctx, p.Root, []string{"rev-parse", "--short=12", "HEAD"}, 8*1024)
		committed = dashboardGit(ctx, p.Root, []string{"log", "-1", "--format=%cI"}, 8*1024)
	}
	tracked := dashboardRun{ok: true}
	if hasHead {
		tracked = dashboardGit(ctx, p.Root, []string{"diff", "--relative", "--name-only", "-z", "HEAD", "--", "."}, 256*1024)
	}
	untracked := dashboardGit(ctx, p.Root, []string{"ls-files", "--others", "--exclude-standard", "-z", "--", "."}, 256*1024)
	if !tracked.ok || !untracked.ok {
		return dashboardUnavailable("Git working-tree status could not be read.")
	}
	allTracked, allUntracked := dashboardPaths(tracked.stdout), dashboardPaths(untracked.stdout)
	safeTracked, safeUntracked := []string{}, []string{}
	for _, v := range allTracked {
		if s.SafeDashboardPath(v) {
			safeTracked = append(safeTracked, v)
		}
	}
	for _, v := range allUntracked {
		if s.SafeDashboardPath(v) {
			safeUntracked = append(safeUntracked, v)
		}
	}
	hidden := len(allTracked) + len(allUntracked) - len(safeTracked) - len(safeUntracked)
	renderedTracked := append([]string{}, safeTracked[:min(120, len(safeTracked))]...)
	slots := max(0, 120-len(renderedTracked))
	renderedUntracked := append([]string{}, safeUntracked[:min(slots, len(safeUntracked))]...)
	omitted := len(safeTracked) + len(safeUntracked) - len(renderedTracked) - len(renderedUntracked)
	additions, deletions := 0, 0
	if hasHead && len(renderedTracked) > 0 {
		stat := dashboardGit(ctx, p.Root, append([]string{"diff", "--relative", "--shortstat", "HEAD", "--"}, renderedTracked...), 256*1024)
		if stat.ok {
			additions, deletions = shortStat(stat.stdout)
		}
	}
	for i := range renderedTracked {
		renderedTracked[i] = wire.Redact(renderedTracked[i])
	}
	for i := range renderedUntracked {
		renderedUntracked[i] = wire.Redact(renderedUntracked[i])
	}
	branchText := "detached"
	if branch.ok && files.TrimJS(branch.stdout) != "" {
		branchText = wire.Redact(files.TrimJS(branch.stdout))
	}
	result := map[string]any{"available": true, "branch": branchText, "dirty": len(allTracked) > 0 || len(allUntracked) > 0, "trackedChangedPaths": renderedTracked, "untrackedPaths": renderedUntracked, "hiddenPathCount": hidden, "omittedPathCount": omitted, "additions": additions, "deletions": deletions, "diffAvailable": hasHead && len(renderedTracked) > 0}
	if head.ok {
		result["head"] = files.TrimJS(head.stdout)
	}
	if committed.ok {
		result["committedAt"] = files.TrimJS(committed.stdout)
	}
	return result
}

// SnapshotGit has no workspace resolution, review checkpoint, lease, or persistent state side effects.
// The activity owner supplies its ten-second metadata cache; diff requests always recompute.
func (s *Service) SnapshotGit(ctx context.Context, p wire.Project, includeDiff bool) (map[string]any, error) {
	status := s.dashboardStatus(ctx, p)
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !includeDiff {
		return status, nil
	}
	result := map[string]any{"diff": "", "truncated": false}
	if status["available"] != true {
		message, _ := status["message"].(string)
		if message == "" {
			message = "Git is unavailable for this project."
		}
		result["diff"] = message
		return result, nil
	}
	if head, _ := status["head"].(string); head == "" {
		result["diff"] = "This Git working tree has no commit yet."
		return result, nil
	}
	paths, _ := status["trackedChangedPaths"].([]string)
	if len(paths) == 0 {
		result["diff"] = "No tracked diff. The working tree contains only untracked or safety-filtered paths."
		return result, nil
	}
	rendered := dashboardGit(ctx, p.Root, append([]string{"diff", "--relative", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, paths...), 512*1024)
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !rendered.ok {
		result["diff"] = "Tracked diff is too large or could not be rendered; the changed-path summary remains available."
		result["truncated"] = true
		return result, nil
	}
	result["diff"] = wire.Redact(files.TrimJS(rendered.stdout))
	result["truncated"] = rendered.truncated
	return result, nil
}
