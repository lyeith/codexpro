package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type streamResult struct {
	stderr                                              string
	exit                                                int
	stopped, limited, eventLimited, cancelled, timedOut bool
	err                                                 error
}

func streamJSON(ctx context.Context, root, command string, args []string, timeout time.Duration, eventMax, totalMax int, consume func([]byte) (bool, error)) streamResult {
	parent := ctx
	var deadline context.CancelFunc
	if timeout > 0 {
		ctx, deadline = context.WithTimeout(ctx, timeout)
		defer deadline()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return streamResult{err: e}
	}
	stderr, e := cmd.StderrPipe()
	if e != nil {
		return streamResult{err: e}
	}
	done := make(chan struct{})
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGTERM)
		go func() {
			select {
			case <-done:
			case <-time.After(time.Second):
				select {
				case <-done:
					return
				default:
				}
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
		return err
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	if e = cmd.Start(); e != nil {
		close(done)
		return streamResult{err: e, exit: -1}
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
	var stderrText string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		b, _ := io.ReadAll(io.LimitReader(stderr, 64*1024))
		stderrText = files.DecodeText(b)
		_, _ = io.Copy(io.Discard, stderr)
	}()
	result := streamResult{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), eventMax+1)
	observed := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		observed += len(line) + 1
		if totalMax > 0 && observed > totalMax {
			result.limited = true
			cancel()
			break
		}
		if len(line) > eventMax {
			result.limited = true
			result.eventLimited = true
			cancel()
			break
		}
		if len(line) == 0 {
			continue
		}
		stop, err := consume(line)
		if err != nil {
			result.err = err
			cancel()
			break
		}
		if stop {
			result.stopped = true
			cancel()
			break
		}
	}
	if e = scanner.Err(); e != nil && result.err == nil {
		if strings.Contains(e.Error(), "token too long") {
			result.limited = true
			result.eventLimited = true
		} else if !result.stopped {
			result.err = e
		}
		cancel()
	}
	e = cmd.Wait()
	close(done)
	wg.Wait()
	result.stderr = stderrText
	result.exit = -1
	if cmd.ProcessState != nil {
		result.exit = cmd.ProcessState.ExitCode()
	}
	result.cancelled = parent.Err() != nil
	result.timedOut = ctx.Err() == context.DeadlineExceeded && !result.cancelled
	if result.err == nil && e != nil && !result.stopped && !result.limited {
		result.err = e
	}
	return result
}
func (s *Service) ripgrep(ctx context.Context, w wire.Workspace, o options, after string, allowed map[string]bool) (page, error) {
	p := page{Hits: []hit{}, Used: "ripgrep", Warnings: []string{}}
	if allowed != nil && len(allowed) == 0 {
		return p, nil
	}
	abs, _, e := s.Files.ResolvePath(w, o.Root, false)
	if e != nil {
		return p, e
	}
	args := []string{"--json", "--line-number", "--with-filename", "--no-heading", "--color=never", "--sort", "path", "--max-columns", "4000", "--max-filesize", strconv.Itoa(s.Files.ScanLimit())}
	if !o.Regex {
		args = append(args, "--fixed-strings")
	}
	if o.Hidden {
		args = append(args, "--hidden")
	}
	for _, g := range s.Config.BlockedGlobs {
		args = append(args, "-g", "!"+g)
	}
	if o.Glob != "" {
		args = append(args, "-g", o.Glob)
	}
	args = append(args, "-e", o.Query, "--", abs)
	r := streamJSON(ctx, w.Root, "rg", args, 0, max(2000000, s.Config.Int("maxOutputBytes", 120000)), 0, func(line []byte) (bool, error) {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				Line int `json:"line_number"`
			} `json:"data"`
		}
		if e := json.Unmarshal(line, &event); e != nil {
			return false, fmt.Errorf("ripgrep returned malformed JSON: %s", e)
		}
		if event.Type != "match" {
			return false, nil
		}
		rel := relativePath(w, event.Data.Path.Text)
		if rel == "" || s.Files.IsBlocked(rel) || allowed != nil && !allowed[rel] || event.Data.Line < 1 {
			return false, nil
		}
		text := strings.TrimSuffix(event.Data.Lines.Text, "\n")
		text = strings.TrimSuffix(text, "\r")
		h := hit{Path: rel, Line: event.Data.Line, End: event.Data.Line, Text: text, Source: "text", Editable: true}
		if after != "" && files.Compare(hitKey(h), after) <= 0 {
			return false, nil
		}
		p.Hits = append(p.Hits, h)
		return len(p.Hits) >= o.Max+1, nil
	})
	if r.cancelled {
		return p, ctx.Err()
	}
	if r.limited {
		p.Warnings = append(p.Warnings, "Search stopped because one ripgrep event exceeded the output safety budget.")
	}
	if r.err != nil && !r.stopped && !r.limited && r.exit != 1 {
		if r.exit > 1 {
			message := strings.TrimSpace(r.stderr)
			if message == "" {
				message = fmt.Sprintf("ripgrep failed with exit code %d", r.exit)
			}
			return p, wire.Errorf("", "%s", message)
		}
		return p, r.err
	}
	p.More = len(p.Hits) > o.Max || r.stopped || r.limited
	return p, nil
}
func (s *Service) fallback(ctx context.Context, w wire.Workspace, o options, after string, allowed map[string]bool) (page, error) {
	p := page{Hits: []hit{}, Used: "node", Warnings: []string{}}
	if o.Regex {
		return p, fmt.Errorf("Regex search requires ripgrep; the bounded JavaScript fallback supports fixed strings only.")
	}
	if allowed != nil && len(allowed) == 0 {
		return p, nil
	}
	paths, e := s.Files.ListFiles(ctx, w, o.Root, o.Glob, o.Hidden, 20000)
	if e != nil {
		return p, e
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if allowed != nil && !allowed[rel] {
			continue
		}
		abs, _, e := s.Files.ResolvePath(w, rel, false)
		if e != nil {
			continue
		}
		b, e := files.ReadSource(abs, s.Files.ScanLimit())
		if e != nil {
			continue
		}
		for index, line := range files.Lines(files.DecodeText(b)) {
			if !strings.Contains(line, o.Query) {
				continue
			}
			h := hit{Path: rel, Line: index + 1, End: index + 1, Text: line, Source: "text", Editable: true}
			if after != "" && files.Compare(hitKey(h), after) <= 0 {
				continue
			}
			p.Hits = append(p.Hits, h)
			if len(p.Hits) >= o.Max+1 {
				p.More = true
				return p, nil
			}
		}
	}
	return p, nil
}
func inferFormat(path, requested string) string {
	if requested != "auto" {
		return requested
	}
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".jsonc"):
		return "json"
	case strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		return "yaml"
	case strings.HasSuffix(lower, ".toml"):
		return "toml"
	}
	return ""
}
func (s *Service) configSearch(ctx context.Context, w wire.Workspace, o options, after string, allowed map[string]bool) (page, error) {
	p := page{Hits: []hit{}, Used: "config", Warnings: []string{}}
	if allowed != nil && len(allowed) == 0 {
		return p, nil
	}
	paths, e := s.Files.ListFiles(ctx, w, o.Root, o.Glob, o.Hidden, 20000)
	if e != nil {
		return p, e
	}
	sort.Strings(paths)
	filtered := []string{}
	for _, path := range paths {
		if allowed != nil && !allowed[path] {
			continue
		}
		if o.ConfigFormat == "auto" && inferFormat(path, "auto") == "" {
			continue
		}
		filtered = append(filtered, path)
	}
	if len(filtered) > 2000 {
		p.Warnings = append(p.Warnings, "Configuration query examined only the first 2000 matching files.")
		filtered = filtered[:2000]
	}
	for _, rel := range filtered {
		format := inferFormat(rel, o.ConfigFormat)
		if format == "" {
			continue
		}
		abs, _, e := s.Files.ResolvePath(w, rel, false)
		if e != nil {
			return p, e
		}
		b, e := files.ReadSource(abs, s.Files.ScanLimit())
		if e != nil {
			continue
		}
		nodes, warn, e := queryConfig(files.DecodeText(b), format, o.Query)
		if e != nil {
			p.Warnings = append(p.Warnings, rel+": "+e.Error())
			continue
		}
		for _, v := range warn {
			p.Warnings = append(p.Warnings, rel+": "+v)
		}
		for _, n := range nodes {
			h := hit{Path: rel, Line: n.Line, End: n.End, Text: n.Text, Source: "config", Editable: true, Address: n.Address, ConfigFormat: format}
			if after != "" && files.Compare(hitKey(h), after) <= 0 {
				continue
			}
			p.Hits = append(p.Hits, h)
			if len(p.Hits) >= o.Max+1 {
				p.More = true
				break
			}
		}
		if p.More {
			break
		}
	}
	sort.SliceStable(p.Hits, func(i, j int) bool { return files.Compare(hitKey(p.Hits[i]), hitKey(p.Hits[j])) < 0 })
	p.Warnings = warnings(p.Warnings)
	return p, nil
}
func gitRun(ctx context.Context, w wire.Workspace, args []string, budget int) files.ProcessResult {
	args = append([]string{"-c", "core.quotePath=false"}, args...)
	return files.RunCommandEnv(ctx, w.Root, "git", args, "", budget, 10*time.Second, []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"})
}
func gitMessage(r files.ProcessResult) string {
	if v := strings.TrimSpace(r.Stderr); v != "" {
		return v
	}
	if r.Err != nil {
		return r.Err.Error()
	}
	return ""
}
func (s *Service) assertDiff(ctx context.Context, w wire.Workspace, base string) error {
	invalid := strings.TrimSpace(base) == "" || strings.HasPrefix(strings.TrimSpace(base), "-")
	for _, r := range base {
		if r <= 32 || r == 127 {
			invalid = true
		}
	}
	if invalid {
		return fmt.Errorf("base_ref must be a non-empty Git ref without whitespace, control characters, or a leading dash.")
	}
	r := gitRun(ctx, w, []string{"rev-parse", "--is-inside-work-tree"}, 16*1024)
	if r.Err != nil || r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "true" {
		return fmt.Errorf("Diff-aware search requires a Git working tree.")
	}
	r = gitRun(ctx, w, []string{"rev-parse", "--verify", base + "^{commit}"}, 16*1024)
	if r.Err != nil || r.ExitCode != 0 {
		return fmt.Errorf("base_ref does not resolve to a commit: %s", base)
	}
	return nil
}
func selector(o options) []string {
	if o.DiffTarget == "staged" {
		return []string{"diff", "--cached", o.BaseRef}
	}
	if o.DiffTarget == "head" {
		return []string{"diff", o.BaseRef + "...HEAD"}
	}
	return []string{"diff", o.BaseRef}
}
func (s *Service) allowedPath(o options, path, root string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	if path == "" || path == "." || path == ".." || strings.HasPrefix(path, "../") || s.Files.IsBlocked(path) {
		return false
	}
	if !o.Hidden {
		for _, part := range strings.Split(path, "/") {
			if strings.HasPrefix(part, ".") {
				return false
			}
		}
	}
	if root != "." && path != root && !strings.HasPrefix(path, root+"/") {
		return false
	}
	return o.Glob == "" || files.MatchGlob(o.Glob, path)
}
func nulPaths(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, "\x00") {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (s *Service) changedPaths(ctx context.Context, w wire.Workspace, o options) (map[string]bool, []string, error) {
	o.BaseRef = strings.TrimSpace(o.BaseRef)
	if e := s.assertDiff(ctx, w, o.BaseRef); e != nil {
		return nil, nil, e
	}
	_, root, e := s.Files.ResolvePath(w, o.Root, false)
	if e != nil {
		return nil, nil, e
	}
	budget := max(4*1024*1024, s.Config.Int("maxOutputBytes", 120000)*4)
	r := gitRun(ctx, w, append(selector(o), "--name-only", "-z", "--", root), budget)
	if (r.Err != nil || r.ExitCode != 0) && !r.Truncated {
		message := gitMessage(r)
		if message == "" {
			message = "Git changed-file search failed."
		}
		return nil, nil, fmt.Errorf("%s", message)
	}
	warn := []string{}
	if r.Truncated {
		warn = append(warn, "Changed-file list was truncated by the Git output budget.")
	}
	paths := nulPaths(r.Stdout)
	if o.DiffTarget == "worktree" && o.Untracked {
		r = gitRun(ctx, w, []string{"ls-files", "--others", "--exclude-standard", "-z", "--", root}, budget)
		if r.ExitCode == 0 || r.Truncated {
			paths = append(paths, nulPaths(r.Stdout)...)
		}
		if r.Truncated {
			warn = append(warn, "Untracked-file list was truncated by the Git output budget.")
		}
	}
	allowed := map[string]bool{}
	for _, p := range paths {
		if s.allowedPath(o, p, root) {
			allowed[p] = true
		}
	}
	return allowed, warn, nil
}

var diffHeader = regexp.MustCompile("^@@ -(\\d+)(?:,\\d+)? \\+(\\d+)(?:,\\d+)? @@")

func parseDiffPath(value string) string {
	v := strings.TrimSpace(value)
	if v == "/dev/null" {
		return v
	}
	v = files.DecodeGitPath(v)
	if strings.HasPrefix(v, "a/") || strings.HasPrefix(v, "b/") {
		v = v[2:]
	}
	return v
}
func (s *Service) diffSearch(ctx context.Context, w wire.Workspace, o options, after string) (page, error) {
	p := page{Hits: []hit{}, Used: "git-diff", Warnings: []string{}}
	o.BaseRef = strings.TrimSpace(o.BaseRef)
	if e := s.assertDiff(ctx, w, o.BaseRef); e != nil {
		return p, e
	}
	_, root, e := s.Files.ResolvePath(w, o.Root, false)
	if e != nil {
		return p, e
	}
	match, e := makeMatcher(o.Query, o.Regex)
	if e != nil {
		return p, e
	}
	budget := max(8*1024*1024, min(64*1024*1024, s.Config.Int("maxOutputBytes", 120000)*16))
	r := gitRun(ctx, w, append(selector(o), "--no-color", "--no-ext-diff", "--unified=0", "--", root), budget)
	if (r.Err != nil || r.ExitCode != 0) && !r.Truncated {
		message := gitMessage(r)
		if message == "" {
			message = "Git diff search failed."
		}
		return p, fmt.Errorf("%s", message)
	}
	if r.Truncated {
		p.Warnings = append(p.Warnings, "Git diff was truncated by the diff-search byte budget.")
	}
	oldPath, newPath := "", ""
	oldLine, newLine := 0, 0
	inHunk, capped := false, false
	add := func(h hit) error {
		if !s.allowedPath(o, h.Path, root) {
			return nil
		}
		ok, e := match(h.Text)
		if e != nil {
			return e
		}
		if ok {
			p.Hits = append(p.Hits, h)
			if len(p.Hits) >= 100000 {
				capped = true
			}
		}
		return nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(r.Stdout, "\r\n", "\n"), "\n") {
		if capped {
			break
		}
		if strings.HasPrefix(line, "--- ") {
			oldPath = parseDiffPath(line[4:])
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			newPath = parseDiffPath(line[4:])
			inHunk = false
			continue
		}
		if m := diffHeader.FindStringSubmatch(line); m != nil {
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		if line[0] == '-' && !strings.HasPrefix(line, "---") {
			if o.Scope == "diff_removed" && oldPath != "" && oldPath != "/dev/null" {
				e = add(hit{Path: oldPath, Line: oldLine, End: oldLine, Text: line[1:], Source: "diff_removed", Editable: false})
			}
			oldLine++
		} else if line[0] == '+' && !strings.HasPrefix(line, "+++") {
			if o.Scope == "diff_added" && newPath != "" && newPath != "/dev/null" {
				e = add(hit{Path: newPath, Line: newLine, End: newLine, Text: line[1:], Source: "diff_added", Editable: true})
			}
			newLine++
		} else if line[0] == ' ' {
			oldLine++
			newLine++
		}
		if e != nil {
			return p, e
		}
	}
	if capped {
		p.Warnings = append(p.Warnings, "Diff search stopped after 100000 matching lines.")
	}
	if o.Scope == "diff_added" && o.DiffTarget == "worktree" && o.Untracked && !capped {
		untracked := gitRun(ctx, w, []string{"ls-files", "--others", "--exclude-standard", "-z", "--", root}, budget)
		if untracked.ExitCode == 0 || untracked.Truncated {
			paths := nulPaths(untracked.Stdout)
			sort.Strings(paths)
			for _, rel := range paths {
				if capped || !s.allowedPath(o, rel, root) {
					continue
				}
				abs, _, err := s.Files.ResolvePath(w, rel, false)
				if err != nil {
					continue
				}
				b, err := files.ReadSource(abs, s.Files.ScanLimit())
				if err != nil {
					continue
				}
				for i, line := range files.Lines(files.DecodeText(b)) {
					if e = add(hit{Path: rel, Line: i + 1, End: i + 1, Text: line, Source: "diff_added", Editable: true}); e != nil {
						return p, e
					}
					if capped {
						break
					}
				}
			}
		}
		if untracked.Truncated {
			p.Warnings = append(p.Warnings, "Untracked diff search was truncated by the Git output budget.")
		}
	}
	sort.SliceStable(p.Hits, func(i, j int) bool { return files.Compare(hitKey(p.Hits[i]), hitKey(p.Hits[j])) < 0 })
	filtered := []hit{}
	for _, h := range p.Hits {
		if after == "" || files.Compare(hitKey(h), after) > 0 {
			filtered = append(filtered, h)
		}
	}
	p.More = len(filtered) > o.Max || r.Truncated || capped
	p.Hits = filtered[:min(len(filtered), o.Max+1)]
	p.Warnings = warnings(p.Warnings)
	return p, nil
}
func (s *Service) primary(ctx context.Context, w wire.Workspace, o options, after string) (page, error) {
	if o.Scope == "diff_added" || o.Scope == "diff_removed" {
		if o.Kind != "text" {
			return page{}, fmt.Errorf("diff_added and diff_removed scopes support text search only. Use changed_files for configuration queries.")
		}
		return s.diffSearch(ctx, w, o, after)
	}
	var allowed map[string]bool
	warn := []string{}
	if o.Scope == "changed_files" {
		var e error
		allowed, warn, e = s.changedPaths(ctx, w, o)
		if e != nil {
			return page{}, e
		}
	}
	var p page
	var e error
	if o.Kind == "config" {
		p, e = s.configSearch(ctx, w, o, after, allowed)
	} else if _, err := exec.LookPath("rg"); err == nil {
		p, e = s.ripgrep(ctx, w, o, after, allowed)
	} else {
		p, e = s.fallback(ctx, w, o, after, allowed)
	}
	p.Warnings = warnings(warn, p.Warnings)
	return p, e
}

// Keep a byte buffer import available to stream fixtures without exposing it in
// service state.
var _ = bytes.MinRead
