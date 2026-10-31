package files

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type nativeHunk struct {
	anchor    string
	hasAnchor bool
	lines     []string
	eof       bool
}
type nativeFile struct {
	op, path, move string
	hunks          []nativeHunk
	added          []string
}

func patchError(message string) error {
	return failure("patch_format_invalid", message, map[string]any{"message": "Read the current target files and regenerate the patch. Do not retry unchanged."})
}
func stalePatch(message string) error {
	return failure("patch_context_stale", message, map[string]any{"message": "Read the current target files and regenerate the patch. Do not retry unchanged."})
}
func isNative(patch string) bool {
	return strings.HasPrefix(strings.TrimLeft(patch, " \t\r\n"), "*** Begin Patch")
}

var nativeHeader = regexp.MustCompile("^\\*\\*\\* (Add|Update|Delete) File: (.+)$")
var hunkHeader = regexp.MustCompile("^@@ -(\\d+)(?:,(\\d+))? \\+(\\d+)(?:,(\\d+))? @@")
var symlinkMode = regexp.MustCompile("(?m)^(?:(?:new|old|deleted) file mode|new mode|old mode) 120000\\s*$")

func parseNative(patch string) ([]nativeFile, error) {
	lines := strings.Split(strings.ReplaceAll(TrimJS(patch), "\r\n", "\n"), "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" || lines[len(lines)-1] != "*** End Patch" {
		return nil, patchError("Expected complete *** Begin Patch / *** End Patch markers.")
	}
	files := []nativeFile{}
	fi, hi := -1, -1
	for _, line := range lines[1 : len(lines)-1] {
		if m := nativeHeader.FindStringSubmatch(line); m != nil {
			files = append(files, nativeFile{op: m[1], path: m[2], hunks: []nativeHunk{}, added: []string{}})
			fi = len(files) - 1
			hi = -1
			continue
		}
		if fi < 0 {
			return nil, patchError("Patch content must follow a file header.")
		}
		f := &files[fi]
		if strings.HasPrefix(line, "*** Move to: ") && f.op == "Update" && f.move == "" && len(f.hunks) == 0 {
			f.move = strings.TrimPrefix(line, "*** Move to: ")
			continue
		}
		if f.op == "Add" && strings.HasPrefix(line, "+") {
			f.added = append(f.added, line[1:])
			continue
		}
		if f.op == "Update" && (line == "@@" || strings.HasPrefix(line, "@@ ")) {
			h := nativeHunk{lines: []string{}}
			if line != "@@" {
				h.hasAnchor = true
				h.anchor = line[3:]
			}
			f.hunks = append(f.hunks, h)
			hi = len(f.hunks) - 1
			continue
		}
		if f.op == "Update" && hi >= 0 && line == "*** End of File" && !f.hunks[hi].eof {
			f.hunks[hi].eof = true
			continue
		}
		if f.op == "Update" && hi >= 0 && !f.hunks[hi].eof && len(line) > 0 && strings.ContainsRune(" +-", rune(line[0])) {
			f.hunks[hi].lines = append(f.hunks[hi].lines, line)
			continue
		}
		return nil, patchError(fmt.Sprintf("Invalid %s patch content for %s.", f.op, f.path))
	}
	if len(files) == 0 {
		return nil, patchError("Patch has no file operations.")
	}
	seen := map[string]bool{}
	for _, f := range files {
		if f.op == "Update" && len(f.hunks) == 0 && f.move == "" {
			return nil, patchError("No update hunks for " + f.path + ".")
		}
		targets := []string{f.path}
		if f.move != "" {
			targets = append(targets, f.move)
		}
		for _, p := range targets {
			if TrimJS(p) == "" || strings.ContainsRune(p, 0) || seen[p] {
				return nil, patchError("Duplicate or invalid patch target.")
			}
			seen[p] = true
		}
	}
	return files, nil
}
func DecodeGitPath(value string) string {
	if strings.HasPrefix(value, "\"") {
		if s, e := strconv.Unquote(value); e == nil {
			return s
		}
	}
	return value
}
func gitPatchPath(value string, strip bool) string {
	raw := TrimJS(strings.Split(value, "\t")[0])
	if raw == "/dev/null" {
		return ""
	}
	decoded := DecodeGitPath(raw)
	if !strip || filepath.IsAbs(decoded) || len(decoded) > 2 && decoded[1] == ':' {
		return decoded
	}
	if i := strings.IndexByte(decoded, '/'); i >= 0 {
		return decoded[i+1:]
	}
	return decoded
}
func PatchPaths(patch string) ([]string, error) {
	if isNative(patch) {
		files, e := parseNative(patch)
		if e != nil {
			return nil, e
		}
		out := []string{}
		for _, f := range files {
			out = append(out, f.path)
			if f.move != "" {
				out = append(out, f.move)
			}
		}
		return out, nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	old, next := 0, 0
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		if old != 0 || next != 0 {
			if len(line) > 0 {
				switch line[0] {
				case ' ':
					old--
					next--
				case '-':
					old--
				case '+':
					next--
				}
			}
			if old < 0 || next < 0 {
				return nil, patchError("Invalid unified hunk length.")
			}
			continue
		}
		if strings.HasPrefix(line, "diff --git ") {
			header := line[11:]
			pairs := [][2]string{}
			quoted, escaped := false, false
			for i := 0; i < len(header); i++ {
				c := header[i]
				if escaped {
					escaped = false
					continue
				}
				if quoted && c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					quoted = !quoted
					continue
				}
				if c != ' ' || quoted {
					continue
				}
				a, b := header[:i], header[i+1:]
				valid := func(p string) bool {
					p = strings.TrimPrefix(p, "\"")
					slash := strings.IndexByte(p, '/')
					return slash >= 0 && !strings.ContainsAny(p[:slash], " \t\r\n\"")
				}
				if valid(a) && valid(b) {
					pairs = append(pairs, [2]string{a, b})
				}
			}
			if quoted || len(pairs) != 1 {
				return nil, patchError("Ambiguous or invalid diff --git paths.")
			}
			add(gitPatchPath(pairs[0][0], true))
			add(gitPatchPath(pairs[0][1], true))
			continue
		}
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			old, next = 1, 1
			if m[2] != "" {
				old, _ = strconv.Atoi(m[2])
			}
			if m[4] != "" {
				next, _ = strconv.Atoi(m[4])
			}
			continue
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			add(gitPatchPath(line[4:], true))
		}
		for _, prefix := range []string{"rename from ", "rename to ", "copy from ", "copy to "} {
			if strings.HasPrefix(line, prefix) {
				add(gitPatchPath(strings.TrimPrefix(line, prefix), false))
				break
			}
		}
		if strings.HasPrefix(line, "*** ") {
			return nil, patchError("Cannot mix native patch markers and Git diffs.")
		}
	}
	if old != 0 || next != 0 {
		return nil, patchError("Incomplete unified hunk.")
	}
	return out, nil
}
func JSONString(value any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(value)
	return strings.TrimSuffix(b.String(), "\n")
}
func fullDiff(name string, before, after *string, mode string) string {
	if before != nil && after != nil && *before == *after {
		return ""
	}
	a, b := JSONString("a/"+name), JSONString("b/"+name)
	split := func(s *string) []string {
		if s == nil || *s == "" {
			return []string{}
		}
		ls := strings.Split(*s, "\n")
		if strings.HasSuffix(*s, "\n") {
			ls = ls[:len(ls)-1]
		}
		return ls
	}
	old, next := split(before), split(after)
	out := []string{"diff --git " + a + " " + b}
	if before == nil {
		out = append(out, "new file mode "+mode)
	} else if after == nil {
		out = append(out, "deleted file mode "+mode)
	}
	oldName, newName := a, b
	if before == nil {
		oldName = "/dev/null"
	}
	if after == nil {
		newName = "/dev/null"
	}
	out = append(out, "--- "+oldName, "+++ "+newName)
	if len(old) > 0 || len(next) > 0 {
		oldStart, newStart := 0, 0
		if len(old) > 0 {
			oldStart = 1
		}
		if len(next) > 0 {
			newStart = 1
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, len(old), newStart, len(next)))
		body := func(lines []string, text *string, prefix string) {
			for i, line := range lines {
				out = append(out, prefix+line)
				if i == len(lines)-1 && text != nil && !strings.HasSuffix(*text, "\n") {
					out = append(out, "\\ No newline at end of file")
				}
			}
		}
		body(old, before, "-")
		body(next, after, "+")
	}
	return strings.Join(append(out, ""), "\n")
}
func updateNative(file nativeFile, text string) (string, error) {
	crlf := strings.Contains(text, "\r\n")
	remainder := strings.ReplaceAll(text, "\r\n", "")
	if strings.Contains(remainder, "\r") || crlf && strings.Contains(remainder, "\n") {
		return "", patchError("Use a Git diff for mixed line endings.")
	}
	ending := "\n"
	if crlf {
		ending = "\r\n"
	}
	lines := strings.Split(text, ending)
	final := strings.HasSuffix(text, ending)
	if final || text == "" {
		lines = lines[:len(lines)-1]
	}
	position := 0
	for _, h := range file.hunks {
		if h.hasAnchor {
			matches := []int{}
			for i, line := range lines {
				if i >= position && line == h.anchor {
					matches = append(matches, i)
				}
			}
			if len(matches) != 1 {
				return "", stalePatch("Missing or ambiguous anchor in " + file.path + ".")
			}
			position = matches[0] + 1
		}
		old, next := []string{}, []string{}
		for _, line := range h.lines {
			if line[0] != '+' {
				old = append(old, line[1:])
			}
			if line[0] != '-' {
				next = append(next, line[1:])
			}
		}
		if len(h.lines) == 0 {
			return "", patchError("Empty update hunk.")
		}
		matches := []int{}
		for i := position; i <= len(lines)-len(old); i++ {
			if h.eof && i+len(old) != len(lines) {
				continue
			}
			match := true
			for j, s := range old {
				if lines[i+j] != s {
					match = false
					break
				}
			}
			if match {
				matches = append(matches, i)
			}
		}
		if len(matches) != 1 {
			return "", stalePatch("Missing or ambiguous hunk context in " + file.path + ".")
		}
		i := matches[0]
		merged := append([]string{}, lines[:i]...)
		merged = append(merged, next...)
		merged = append(merged, lines[i+len(old):]...)
		lines = merged
		position = i + len(next)
	}
	result := strings.Join(lines, ending)
	if len(lines) > 0 && final {
		result += ending
	}
	return result, nil
}
func (s *Service) patch(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	input := wire.String(a, "patch", "")
	limit := s.Config.Int("maxWriteBytes", 1000000)
	if TrimJS(input) == "" || len(input) > limit {
		return wire.Result{}, patchError("Patch is empty or exceeds the write byte limit.")
	}
	if wire.HasSecretValue(input) {
		return wire.Result{}, wire.SecretContentBlockedError("apply_patch", input)
	}
	if symlinkMode.MatchString(input) {
		return wire.Result{}, patchError("Symlink patches are blocked from apply_patch.")
	}
	paths, e := PatchPaths(input)
	if e != nil {
		return wire.Result{}, e
	}
	if len(paths) == 0 {
		return wire.Result{}, patchError("Patch must contain file headers.")
	}
	targets := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		abs, _, e := s.ResolvePath(w, p, true)
		if e != nil {
			return wire.Result{}, e
		}
		if seen[abs] {
			return wire.Result{}, patchError("Duplicate canonical patch targets.")
		}
		seen[abs] = true
		targets = append(targets, abs)
	}
	release := s.LockPaths(targets...)
	defer release()
	for _, p := range paths {
		if _, _, e = s.ResolvePath(w, p, true); e != nil {
			return wire.Result{}, e
		}
	}
	patch := input
	if isNative(input) {
		definitions, e := parseNative(input)
		if e != nil {
			return wire.Result{}, e
		}
		parts := []string{}
		normalized := 0
		appendDiff := func(diff string) error {
			normalized += len(diff)
			if normalized > limit*8 {
				return patchError("Expanded native diff exceeds its bounded normalization budget. Use a compact Git diff.")
			}
			parts = append(parts, diff)
			return nil
		}
		for _, f := range definitions {
			abs, _, e := s.ResolvePath(w, f.path, true)
			if e != nil {
				return wire.Result{}, e
			}
			st, statErr := os.Lstat(abs)
			if f.op == "Add" {
				if statErr == nil {
					return wire.Result{}, stalePatch("Add target already exists: " + f.path + ".")
				}
				text := ""
				if len(f.added) > 0 {
					text = strings.Join(f.added, "\n") + "\n"
				}
				if e = appendDiff(fullDiff(f.path, nil, &text, "100644")); e != nil {
					return wire.Result{}, e
				}
				continue
			}
			if os.IsNotExist(statErr) {
				return wire.Result{}, stalePatch("Target does not exist: " + f.path + ".")
			}
			if statErr != nil {
				return wire.Result{}, statErr
			}
			if !st.Mode().IsRegular() || st.Size() > int64(limit) {
				return wire.Result{}, patchError("Patch target is not a bounded regular text file.")
			}
			b, e := os.ReadFile(abs)
			if e != nil {
				return wire.Result{}, e
			}
			if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
				return wire.Result{}, patchError("Native patches require UTF-8 text.")
			}
			before := string(b)
			mode := "100644"
			if st.Mode().Perm()&0111 != 0 {
				mode = "100755"
			}
			if f.op == "Delete" {
				if e = appendDiff(fullDiff(f.path, &before, nil, mode)); e != nil {
					return wire.Result{}, e
				}
				continue
			}
			after, e := updateNative(f, before)
			if e != nil {
				return wire.Result{}, e
			}
			if len(after) > limit {
				return wire.Result{}, patchError("Updated file exceeds the write byte limit.")
			}
			if f.move != "" {
				dest, _, e := s.ResolvePath(w, f.move, true)
				if e != nil {
					return wire.Result{}, e
				}
				if _, e = os.Stat(dest); e == nil {
					return wire.Result{}, stalePatch("Move destination already exists.")
				}
				if e = appendDiff(fullDiff(f.path, &before, nil, mode)); e != nil {
					return wire.Result{}, e
				}
				if e = appendDiff(fullDiff(f.move, nil, &after, mode)); e != nil {
					return wire.Result{}, e
				}
			} else {
				if e = appendDiff(fullDiff(f.path, &before, &after, "100644")); e != nil {
					return wire.Result{}, e
				}
			}
		}
		patch = strings.Join(parts, "")
	}
	changed := TrimJS(patch) != ""
	additions, deletions := 0, 0
	diff := ""
	if changed {
		for _, check := range []bool{true, false} {
			args := []string{"apply"}
			if check {
				args = append(args, "--check")
			}
			args = append(args, "--whitespace=nowarn")
			r := RunCommand(ctx, w.Root, "git", args, patch, s.Config.Int("maxOutputBytes", 120000), 30*time.Second)
			if r.Err != nil || r.ExitCode != 0 {
				message := strings.TrimSpace(r.Stderr)
				if message == "" && r.Err != nil {
					message = r.Err.Error()
				}
				if message == "" {
					message = "git apply failed"
				}
				message = wire.Redact(message)
				lower := strings.ToLower(message)
				code := "patch_apply_failed"
				for _, p := range []string{"patch failed", "does not apply", "while searching", "does not exist"} {
					if strings.Contains(lower, p) {
						code = "patch_context_stale"
						break
					}
				}
				if code == "patch_apply_failed" {
					for _, p := range []string{"corrupt", "malformed", "no valid patches", "unrecognized"} {
						if strings.Contains(lower, p) {
							code = "patch_format_invalid"
							break
						}
					}
				}
				rec := map[string]any{"message": "Read current targets and regenerate the patch or use tagged edit."}
				if len(paths) == 1 {
					rec["tool"] = "read"
					rec["args"] = map[string]any{"path": paths[0]}
				}
				return wire.Result{}, failure(code, message, rec)
			}
		}
		inHunk := false
		for _, line := range strings.Split(patch, "\n") {
			if strings.HasPrefix(line, "diff --git") {
				inHunk = false
			}
			if strings.HasPrefix(line, "@@") {
				inHunk = true
			}
			if inHunk && strings.HasPrefix(line, "+") {
				additions++
			}
			if inHunk && strings.HasPrefix(line, "-") {
				deletions++
			}
		}
		diff = wire.Redact(strings.TrimRight(patch, " \t\r\n"))
	}
	data := Base(w)
	data["paths"] = paths
	data["stdout"] = ""
	data["stderr"] = ""
	data["additions"] = additions
	data["deletions"] = deletions
	data["changed"] = changed
	data["diff"] = diff
	diffText := "No diff output."
	if diff != "" {
		diffText = DiffBlock(diff)
	}
	text := fmt.Sprintf("# Apply Patch\nPaths: %s\nDiff stats: +%d -%d\n%s", strings.Join(paths, ", "), additions, deletions, diffText)
	return wire.Success("apply_patch", "Apply Patch", data, text), nil
}
