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
	"sync"
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
func evidenceString(value any, limit int) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(strings.Trim(text, "\ufeff"))
	units := utf16.Encode([]rune(text))
	if len(units) > limit {
		text = string(utf16.Decode(units[:limit]))
	}
	return text
}
func normalizedEvidencePath(value any) string {
	clean := evidenceString(value, 4096)
	if clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, "\\") || (len(clean) > 2 && clean[1] == ':' && (clean[2] == '/' || clean[2] == '\\')) {
		return ""
	}
	normalized := path.Clean(strings.ReplaceAll(clean, "\\", "/"))
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
		for _, line := range strings.Split(wire.String(args, "patch", ""), "\n") {
			for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
				if strings.HasPrefix(line, prefix) {
					candidates = append(candidates, safeEvidencePath(strings.TrimPrefix(line, prefix)))
					break
				}
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

func parseEvidencePorcelain(text string) []porcelainPath {
	fields := strings.Split(text, "\x00")
	out := []porcelainPath{}
	indices := map[string]int{}
	add := func(actual, status string) {
		clean := normalizedEvidencePath(actual)
		if clean == "" {
			return
		}
		value := porcelainPath{clean, safeEvidencePath(clean), strings.ReplaceAll(status, " ", ".")}
		if i, ok := indices[clean]; ok {
			out[i] = value
		} else {
			indices[clean] = len(out)
			out = append(out, value)
		}
	}
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if len(record) < 4 {
			continue
		}
		status := record[:2]
		add(record[3:], status)
		if strings.ContainsAny(status, "RC") && i+1 < len(fields) {
			i++
			add(fields[i], status)
		}
	}
	return out
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
	// Git inspection is bounded and read-only. It never holds the lease ownership
	// lock or the audit writer, and an unavailable probe cannot revoke a lease.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	run := func(args ...string) files.ProcessResult {
		return files.RunCommandEnv(ctx, root, "git", args, "", limit, 3*time.Second, []string{"GIT_OPTIONAL_LOCKS=0"})
	}
	inside := run("rev-parse", "--is-inside-work-tree")
	if inside.Err != nil || inside.ExitCode != 0 || strings.TrimSpace(inside.Stdout) != "true" {
		return nil
	}
	var head, branch, status files.ProcessResult
	var group sync.WaitGroup
	group.Add(3)
	go func() { defer group.Done(); head = run("rev-parse", "--verify", "HEAD") }()
	go func() { defer group.Done(); branch = run("symbolic-ref", "--quiet", "--short", "HEAD") }()
	go func() { defer group.Done(); status = run("status", "--porcelain=v1", "-z", "--untracked-files=all") }()
	group.Wait()
	headText, branchText := "", ""
	if head.Err == nil && head.ExitCode == 0 {
		headText = evidenceString(head.Stdout, 128)
	}
	if branch.Err == nil && branch.ExitCode == 0 {
		branchText = evidenceString(branch.Stdout, 240)
	}
	states := []any{}
	paths := []string{}
	fingerprint := []string{headText, "status-unavailable"}
	count := 0
	if status.Err == nil && status.ExitCode == 0 {
		fingerprint[1] = status.Stdout
		for _, p := range parseEvidencePorcelain(status.Stdout) {
			count++
			state := pathStateFingerprint(root, p)
			paths = append(paths, p.display)
			fingerprint = append(fingerprint, p.display+"\x00"+state)
			if len(states) < evidencePaths {
				states = append(states, map[string]any{"path": p.display, "status": p.status, "state_fingerprint": state})
			}
		}
	}
	bounded, truncated := uniqueEvidence(paths, evidencePaths)
	out := map[string]any{"available": true, "dirty": count > 0, "changed_path_count": count, "changed_paths": bounded, "changed_paths_truncated": truncated, "path_states": states, "status_fingerprint": evidenceDigest(strings.Join(fingerprint, "\x00"))}
	if headText != "" {
		out["head"] = headText
	}
	if branchText != "" {
		out["branch"] = branchText
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
