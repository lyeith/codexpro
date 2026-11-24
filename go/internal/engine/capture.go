package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const evidencePaths = 24
const evidencePathChars = 320

func evidenceDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func evidenceTrimSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}
func evidenceString(value any, limit int) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.Map(func(r rune) rune {
		if r <= 0x1f || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimFunc(text, evidenceTrimSpace)
	units := utf16.Encode([]rune(text))
	if len(units) > limit {
		text = string(utf16.Decode(units[:limit]))
	}
	return text
}
func normalizedEvidencePath(value any) string {
	clean := evidenceString(value, 4096)
	driveAbsolute := len(clean) > 2 && ((clean[0] >= 'A' && clean[0] <= 'Z') || (clean[0] >= 'a' && clean[0] <= 'z')) && clean[1] == ':' && (clean[2] == '/' || clean[2] == '\\')
	if clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "\\") || driveAbsolute {
		return ""
	}
	slashed := strings.ReplaceAll(clean, "\\", "/")
	normalized := path.Clean(slashed)
	if strings.HasSuffix(slashed, "/") {
		normalized += "/"
	}
	if normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, "/../") {
		return ""
	}
	return normalized
}
func safeEvidencePath(value any) string {
	normalized := normalizedEvidencePath(value)
	if normalized == "" {
		return ""
	}
	units := utf16.Encode([]rune(normalized))
	if len(units) <= evidencePathChars {
		return normalized
	}
	return string(utf16.Decode(units[:evidencePathChars-17])) + "~" + evidenceDigest(normalized)[:16]
}
func uniqueEvidence(values []string, limit int) ([]string, bool) {
	seen := map[string]bool{}
	out := []string{}
	count := 0
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		count++
		if len(out) < limit {
			out = append(out, value)
		}
	}
	return out, count > limit
}
func evidenceArray(value any) []any {
	switch values := value.(type) {
	case []any:
		return values
	case []string:
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	case []map[string]any:
		out := make([]any, len(values))
		for i, v := range values {
			out[i] = v
		}
		return out
	}
	return nil
}
func evidenceResultPaths(result *wire.Result) []string {
	if result == nil {
		return nil
	}
	data := result.StructuredContent
	candidates := []string{safeEvidencePath(data["path"])}
	for _, key := range []string{"paths", "changed_paths", "changed_files"} {
		for _, item := range evidenceArray(data[key]) {
			if s, ok := item.(string); ok {
				if index := strings.LastIndexByte(s, '\t'); index >= 0 {
					s = s[index+1:]
				}
				candidates = append(candidates, safeEvidencePath(s))
			} else if m, ok := item.(map[string]any); ok {
				candidates = append(candidates, safeEvidencePath(m["path"]))
			}
		}
	}
	out, _ := uniqueEvidence(candidates, evidencePaths)
	return out
}
func (e *Engine) evidenceRequestPaths(name string, args map[string]any) []string {
	candidates := []string{}
	switch name {
	case "write", "edit", "read", "view_image":
		candidates = append(candidates, safeEvidencePath(args["path"]))
	case "batch":
		candidates = append(candidates, safeEvidencePath(args["path"]))
		for _, op := range wire.Objects(args, "operations") {
			child := wire.String(op, "tool", "")
			if child != "" && child != "batch" {
				candidates = append(candidates, e.evidenceRequestPaths(child, wire.Object(op, "args"))...)
			}
		}
	case "apply_patch":
		if paths, err := files.PatchPaths(wire.String(args, "patch", "")); err == nil {
			for _, candidate := range paths {
				candidates = append(candidates, safeEvidencePath(candidate))
			}
		}
	case "import_file":
		for _, key := range []string{"destination", "path", "target_path"} {
			if value := args[key]; value != nil {
				candidates = append(candidates, safeEvidencePath(value))
				break
			}
		}
	case "codexpro_self_test":
		if wire.Bool(args, "write_probe", false) {
			candidates = append(candidates, safeEvidencePath(path.Join(e.Config.String("contextDir", ".codexpro"), "codexpro-self-test.md")))
		}
	}
	out, _ := uniqueEvidence(candidates, evidencePaths)
	return out
}

// Match the legacy audit evidence guard. Ordinary file operations have their
// independent canonical path guard and do not rely on this metadata helper.
func evidenceWithin(root, absolute string) bool {
	relative, err := filepath.Rel(root, absolute)
	return err == nil && (relative == "." || (!strings.HasPrefix(relative, "..") && !filepath.IsAbs(relative)))
}
func capturePathEvidence(root string, paths []string) []any {
	out := []any{}
	for _, relative := range paths[:min(len(paths), evidencePaths)] {
		clean := safeEvidencePath(relative)
		if clean == "" {
			continue
		}
		absolute := filepath.Join(root, filepath.FromSlash(clean))
		if !evidenceWithin(root, absolute) {
			continue
		}
		info, err := os.Lstat(absolute)
		if err != nil {
			out = append(out, map[string]any{"path": clean, "exists": !errors.Is(err, syscall.ENOENT)})
			continue
		}
		kind := "other"
		switch {
		case info.Mode().IsRegular():
			kind = "file"
		case info.IsDir():
			kind = "directory"
		case info.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		}
		out = append(out, map[string]any{"path": clean, "exists": true, "kind": kind, "size": info.Size(), "mtime_ms": info.ModTime().UnixMilli()})
	}
	return out
}

type porcelainPath struct{ actual, display, status string }

type porcelainEvidence struct {
	head, branch string
	paths        []porcelainPath
}

// Porcelain v2 includes branch identity and all path states in one read-only Git
// process. NUL framing is required: newline/space/tab bytes may belong to paths.
func parseEvidencePorcelain(text string) (porcelainEvidence, bool) {
	out := porcelainEvidence{paths: []porcelainPath{}}
	if text == "" || !strings.HasSuffix(text, "\x00") {
		return out, false
	}
	fields := strings.Split(text, "\x00")
	indices := map[string]int{}
	add := func(actual, status string) bool {
		if actual == "" || filepath.IsAbs(actual) || strings.HasPrefix(actual, "\\") {
			return false
		}
		clean := path.Clean(actual)
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return false
		}
		value := porcelainPath{clean, safeEvidencePath(clean), strings.ReplaceAll(status, " ", ".")}
		if value.display == "" {
			return false
		}
		if i, ok := indices[clean]; ok {
			out.paths[i] = value
		} else {
			indices[clean] = len(out.paths)
			out.paths = append(out.paths, value)
		}
		return true
	}
	for i := 0; i < len(fields)-1; i++ {
		record := fields[i]
		if strings.HasPrefix(record, "# ") {
			if value, ok := strings.CutPrefix(record, "# branch.oid "); ok && value != "(initial)" {
				out.head = evidenceString(value, 128)
			}
			if value, ok := strings.CutPrefix(record, "# branch.head "); ok && value != "(detached)" {
				out.branch = evidenceString(value, 240)
			}
			continue
		}
		if strings.HasPrefix(record, "? ") {
			if !add(record[2:], "??") {
				return out, false
			}
			continue
		}
		count := 0
		switch {
		case strings.HasPrefix(record, "1 "):
			count = 9
		case strings.HasPrefix(record, "2 "):
			count = 10
		case strings.HasPrefix(record, "u "):
			count = 11
		default:
			return out, false
		}
		parts := strings.SplitN(record, " ", count)
		if len(parts) != count || len(parts[1]) != 2 || !add(parts[count-1], parts[1]) {
			return out, false
		}
		if record[0] == '2' {
			i++
			if i >= len(fields)-1 || !add(fields[i], parts[1]) {
				return out, false
			}
		}
	}
	return out, true
}
func pathStateFingerprint(root string, p porcelainPath) string {
	absolute := filepath.Join(root, filepath.FromSlash(p.actual))
	if !evidenceWithin(root, absolute) {
		return evidenceDigest(p.status + "\x00outside-root")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		code := "unknown"
		for candidate, name := range map[syscall.Errno]string{syscall.ENOENT: "ENOENT", syscall.ENOTDIR: "ENOTDIR", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ELOOP: "ELOOP", syscall.ENAMETOOLONG: "ENAMETOOLONG"} {
			if errors.Is(err, candidate) {
				code = name
				break
			}
		}
		return evidenceDigest(p.status + "\x00" + code)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return evidenceDigest(p.status + "\x00unknown")
	}
	fields := []string{p.status, fmt.Sprint(stat.Mode), fmt.Sprint(stat.Size), fmt.Sprint(stat.Mtim.Sec*1000 + stat.Mtim.Nsec/1e6), fmt.Sprint(stat.Ctim.Sec*1000 + stat.Ctim.Nsec/1e6), fmt.Sprint(stat.Ino)}
	return evidenceDigest(strings.Join(fields, "\x00"))
}
func (e *Engine) captureGitEvidence(ctx context.Context, root string) map[string]any {
	limit := max(64000, min(e.Config.Int("maxOutputBytes", 120000), 2000000))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status := files.RunCommandEnv(ctx, root, "git", []string{"status", "--porcelain=v2", "--branch", "--no-ahead-behind", "-z", "--untracked-files=all"}, "", limit, 3*time.Second, []string{"GIT_OPTIONAL_LOCKS=0"})
	if status.Err != nil || status.ExitCode != 0 || status.TimedOut || status.Truncated {
		return nil
	}
	parsed, complete := parseEvidencePorcelain(status.Stdout)
	if !complete {
		return nil
	}
	states := []any{}
	paths := []string{}
	fingerprint := []string{parsed.head, status.Stdout}
	for _, p := range parsed.paths {
		state := pathStateFingerprint(root, p)
		paths = append(paths, p.display)
		fingerprint = append(fingerprint, p.display+"\x00"+state)
		if len(states) < evidencePaths {
			states = append(states, map[string]any{"path": p.display, "status": p.status, "state_fingerprint": state})
		}
	}
	bounded, truncated := uniqueEvidence(paths, evidencePaths)
	out := map[string]any{"available": true, "dirty": len(parsed.paths) > 0, "changed_path_count": len(parsed.paths), "changed_paths": bounded, "changed_paths_truncated": truncated, "path_states": states, "status_fingerprint": evidenceDigest(strings.Join(fingerprint, "\x00"))}
	if parsed.head != "" {
		out["head"] = parsed.head
	}
	if parsed.branch != "" {
		out["branch"] = parsed.branch
	}
	return out
}
func (e *Engine) capture(ctx context.Context, name string, args map[string]any, ws *wire.Workspace, result *wire.Result) map[string]any {
	out := identity(ws)
	paths, _ := uniqueEvidence(append(e.evidenceRequestPaths(name, args), evidenceResultPaths(result)...), evidencePaths)
	// The metadata builder adds project/workspace/creation targets using the same
	// canonical validation, so the capture only carries paths and observations.
	out["targets"] = paths
	if ws == nil {
		return out
	}
	out["paths"] = capturePathEvidence(ws.Root, paths)
	if git := e.captureGitEvidence(ctx, ws.Root); git != nil {
		out["git"] = git
	}
	return out
}
