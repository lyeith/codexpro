package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func (s *Service) SetTransform(f func(wire.Scope, wire.Workspace, string) string) {
	s.TransformText = f
}
func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	w, e := s.Workspaces.Resolve(ctx, scope, wire.String(args, "workspace_id", ""))
	if e != nil {
		return wire.Result{}, e
	}
	switch name {
	case "read":
		return s.read(ctx, scope, w, args)
	case "write":
		return s.write(ctx, scope, w, args)
	case "edit":
		return s.edit(ctx, scope, w, args)
	case "tree":
		return s.tree(ctx, scope, w, args)
	case "view_image":
		return s.image(ctx, scope, w, args)
	case "apply_patch":
		return s.patch(ctx, scope, w, args)
	case "import_file":
		return s.importFile(ctx, scope, w, args)
	case "load_skill":
		return s.loadSkill(ctx, scope, w, args)
	default:
		return wire.Result{}, wire.Errorf("tool_unknown", "Unknown file tool: %s", name)
	}
}
func Base(w wire.Workspace) map[string]any {
	return map[string]any{"workspace_id": w.ID, "root": w.Root}
}
func (s *Service) read(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	abs, rel, e := s.ResolvePath(w, wire.String(a, "path", ""), false)
	if e != nil {
		return wire.Result{}, e
	}
	b, e := ReadSource(abs, s.ScanLimit())
	if e != nil {
		return wire.Result{}, e
	}
	text := DecodeText(b)
	all := Lines(text)
	start := max(1, wire.Int(a, "start_line", 1))
	end := min(len(all), wire.Int(a, "end_line", len(all)))
	if end < start {
		return wire.Result{}, fmt.Errorf("end_line (%d) must be >= start_line (%d).", end, start)
	}
	limit := min(wire.Int(a, "max_bytes", s.Config.Int("maxReadBytes", 180000)), s.Config.Int("maxReadBytes", 180000))
	selected := all[start-1 : end]
	render := func(lines []string) string { return s.Display(scope, w, Numbered(lines, start)) }
	numbered := render(selected)
	if len(numbered) > limit {
		lo, hi := 0, len(selected)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(render(selected[:mid])) <= limit {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo == 0 {
			return wire.Result{}, failure("read_line_too_large", fmt.Sprintf("Line %d exceeds the %d-byte read page.", start, limit), map[string]any{"tool": "search", "message": fmt.Sprintf("Search %s for a targeted excerpt, or increase read max_bytes within the configured limit.", rel)})
		}
		selected = selected[:lo]
		end = start + lo - 1
		numbered = render(selected)
	}
	tag := s.Snapshots.Record(scope, abs, text, start, end)
	var next any
	continuation := "End of file."
	if end < len(all) {
		next = end + 1
		continuation = fmt.Sprintf("More lines remain. Continue read with start_line=%d; only the displayed lines were read for editing.", end+1)
	}
	data := Base(w)
	data["path"] = rel
	data["text"] = numbered
	data["start_line"] = start
	data["end_line"] = end
	data["total_lines"] = len(all)
	data["bytes"] = len(b)
	data["sha256"] = SHA(text)
	data["truncated"] = start > 1 || end < len(all)
	data["has_more"] = end < len(all)
	data["next_start_line"] = next
	data["returned_bytes"] = len(numbered)
	data["edit_tag"] = tag
	out := fmt.Sprintf("# Read File\n\nPath: %s\nLines: %d-%d of %d\nBytes: %d\nReturned bytes: %d\nSHA-256: %s\nEdit tag: %s\n%s\n\nEvery displayed line number belongs to this four-character edit tag. Pass it as edit_tag to edit; all hunks in that call are resolved against these original line numbers.\n\n```text\n%s\n```", rel, start, end, len(all), len(b), len(numbered), SHA(text), tag, continuation, numbered)
	return wire.Success("read", "Read File", data, out), ctx.Err()
}
func expectedSHA(expected, actual, rel string) error {
	if expected != "" && !strings.EqualFold(expected, SHA(actual)) {
		return fmt.Errorf("File changed since it was read: %s. Expected SHA-256 %s, found %s. Read the file again before writing.", rel, expected, SHA(actual))
	}
	return nil
}
func writeText(abs, rel, content string, old *string) error {
	if old != nil {
		f, e := os.OpenFile(abs, os.O_RDWR, 0)
		if e != nil {
			return FileError(e, "open", abs)
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			return e
		}
		current := make([]byte, st.Size())
		n, e := f.ReadAt(current, 0)
		if e != nil && n != len(current) {
			return e
		}
		if DecodeText(current) != *old {
			return failure("file_changed", "File changed during write: "+rel+". Read the file again before writing.", map[string]any{"tool": "read", "message": "Re-read the file, then resend the write with the new sha256."})
		}
		if e = f.Truncate(0); e != nil {
			return e
		}
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		if _, e = f.WriteString(content); e != nil {
			return e
		}
		return f.Sync()
	}
	raw := make([]byte, 6)
	if _, e := rand.Read(raw); e != nil {
		return e
	}
	temp := filepath.Join(filepath.Dir(abs), fmt.Sprintf(".%s.codexpro-%d-%s.tmp", filepath.Base(abs), os.Getpid(), hex.EncodeToString(raw)))
	f, e := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if e != nil {
		return FileError(e, "open", temp)
	}
	defer os.Remove(temp)
	if _, e = f.WriteString(content); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return FileError(os.Rename(temp, abs), "rename", temp)
}
func (s *Service) checkWrite(tool, before, after string) error {
	if s.SecretWriteCheck != nil {
		return s.SecretWriteCheck(tool, before, after)
	}
	blocked := wire.HasSecretValue(after)
	if tool == "edit" {
		blocked = wire.IntroducesSecretValue(before, after)
	}
	if blocked {
		return wire.SecretContentBlockedError(tool, after)
	}
	return nil
}
func (s *Service) write(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	abs, rel, e := s.ResolvePath(w, wire.String(a, "path", ""), true)
	if e != nil {
		return wire.Result{}, e
	}
	content := wire.String(a, "content", "")
	limit := s.Config.Int("maxWriteBytes", 1000000)
	if len(content) > limit {
		return wire.Result{}, failure("file_too_large", fmt.Sprintf("Write content is too large (%d bytes). Limit: %d bytes.", len(content), limit), nil)
	}
	if e = s.checkWrite("write", "", content); e != nil {
		return wire.Result{}, e
	}
	release := s.LockPaths(abs)
	defer release()
	old := ""
	exists := false
	if _, statErr := os.Stat(abs); statErr == nil {
		b, err := ReadSource(abs, max(limit, s.Config.Int("maxReadBytes", 180000)))
		if err != nil {
			return wire.Result{}, err
		}
		old = DecodeText(b)
		exists = true
	} else if !os.IsNotExist(statErr) {
		return wire.Result{}, FileError(statErr, "stat", abs)
	}
	if exists && !wire.Bool(a, "overwrite", true) {
		return wire.Result{}, failure("file_exists", "File already exists and overwrite=false: "+rel, nil)
	}
	expected := wire.String(a, "expected_sha256", "")
	if expected != "" && !exists {
		return wire.Result{}, fmt.Errorf("File does not exist, so expected_sha256 cannot be verified: %s", rel)
	}
	if exists {
		if e = expectedSHA(expected, old, rel); e != nil {
			return wire.Result{}, e
		}
	}
	if wire.Bool(a, "create_dirs", true) {
		if e = os.MkdirAll(filepath.Dir(abs), 0777); e != nil {
			return wire.Result{}, FileError(e, "mkdir", filepath.Dir(abs))
		}
	}
	if e = ctx.Err(); e != nil {
		return wire.Result{}, e
	}
	d := UnifiedDiff(old, content, rel)
	var previous *string
	if exists {
		previous = &old
	}
	if e = writeText(abs, rel, content, previous); e != nil {
		return wire.Result{}, e
	}
	data := Base(w)
	data["path"] = rel
	data["existed"] = exists
	data["bytes"] = len(content)
	data["sha256"] = SHA(content)
	data["additions"] = d.Additions
	data["deletions"] = d.Deletions
	data["diff"] = wire.Redact(d.Text)
	out := fmt.Sprintf("# Write File\n\nPath: %s\nExisted before: %t\nBytes: %d\nSHA-256: %s\nDiff stats: +%d -%d%s", rel, exists, len(content), SHA(content), d.Additions, d.Deletions, DiffBlock(d.Text))
	return wire.Success("write", "Write File", data, out), nil
}

type preparedRange struct {
	start, end, operation int
	lines                 []string
}
type preparedInsertion struct {
	operation int
	lines     []string
}

func editContentLines(s string) []string {
	ls := Lines(s)
	if len(ls) > 1 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}
func operationMaps(a map[string]any) []map[string]any {
	if ops, ok := a["edits"].([]map[string]any); ok {
		return ops
	}
	return wire.Objects(a, "edits")
}
func (s *Service) edit(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	tag := wire.String(a, "edit_tag", "")
	if tag == "" {
		return wire.Result{}, fmt.Errorf("edit_tag is required. Read the file first and copy its four-character edit tag.")
	}
	ops := operationMaps(a)
	if len(ops) == 0 {
		return wire.Result{}, fmt.Errorf("edits must contain at least one operation.")
	}
	if len(ops) > 100 {
		return wire.Result{}, fmt.Errorf("A single edit call supports at most 100 tagged line operations.")
	}
	abs, rel, e := s.ResolvePath(w, wire.String(a, "path", ""), true)
	if e != nil {
		return wire.Result{}, e
	}
	release := s.LockPaths(abs)
	defer release()
	b, e := ReadSource(abs, max(s.Config.Int("maxWriteBytes", 1000000), s.Config.Int("maxReadBytes", 180000)))
	if e != nil {
		return wire.Result{}, e
	}
	before := DecodeText(b)
	snapshot, e := s.Snapshots.Resolve(scope, abs, tag, before, rel)
	if e != nil {
		return wire.Result{}, e
	}
	bom := ""
	body := before
	if strings.HasPrefix(body, "\ufeff") {
		bom = "\ufeff"
		body = strings.TrimPrefix(body, bom)
	}
	endings := map[string]bool{}
	ending := ""
	for i := 0; i < len(body); i++ {
		v := ""
		if body[i] == '\r' {
			v = "\r"
			if i+1 < len(body) && body[i+1] == '\n' {
				v = "\r\n"
				i++
			}
		} else if body[i] == '\n' {
			v = "\n"
		}
		if v != "" {
			endings[v] = true
			if ending == "" {
				ending = v
			}
		}
	}
	if len(endings) > 1 {
		return wire.Result{}, fmt.Errorf("Tagged line edit does not normalize mixed line endings in %s. Use apply_patch for this file.", rel)
	}
	if ending == "" {
		ending = "\n"
	}
	original := Lines(body)
	count := len(original)
	ranges := []preparedRange{}
	insertions := map[int]preparedInsertion{}
	assertLine := func(value int, field string, operation int) error {
		if value < 1 || value > count {
			return fmt.Errorf("edits[%d].%s must be an original line between 1 and %d; received %d.", operation, field, count, value)
		}
		return nil
	}
	for operation, op := range ops {
		kind := wire.String(op, "op", "")
		switch kind {
		case "replace", "delete":
			start := wire.Int(op, "start_line", 0)
			end := wire.Int(op, "end_line", start)
			if e = assertLine(start, "start_line", operation); e != nil {
				return wire.Result{}, e
			}
			if e = assertLine(end, "end_line", operation); e != nil {
				return wire.Result{}, e
			}
			if end < start {
				return wire.Result{}, fmt.Errorf("edits[%d].end_line (%d) must be greater than or equal to start_line (%d).", operation, end, start)
			}
			if e = snapshot.AssertSeen(start, end, operation, rel); e != nil {
				return wire.Result{}, e
			}
			lines := []string{}
			if kind == "replace" {
				lines = editContentLines(wire.String(op, "content", ""))
			}
			ranges = append(ranges, preparedRange{start - 1, end, operation, lines})
		case "insert_before", "insert_after":
			line := wire.Int(op, "line", 0)
			if e = assertLine(line, "line", operation); e != nil {
				return wire.Result{}, e
			}
			if e = snapshot.AssertSeen(line, line, operation, rel); e != nil {
				return wire.Result{}, e
			}
			gap := line
			if kind == "insert_before" {
				gap--
			}
			if previous, ok := insertions[gap]; ok {
				return wire.Result{}, fmt.Errorf("edits[%d] and edits[%d] both insert at the same original gap. Combine their content into one operation.", operation, previous.operation)
			}
			insertions[gap] = preparedInsertion{operation, editContentLines(wire.String(op, "content", ""))}
		default:
			return wire.Result{}, fmt.Errorf("Unknown edit operation: %s", kind)
		}
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start || ranges[i].start == ranges[j].start && ranges[i].end < ranges[j].end
	})
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			return wire.Result{}, fmt.Errorf("edits[%d] overlaps edits[%d]. All ranges must name disjoint lines in the original edit-tag snapshot.", ranges[i].operation, ranges[i-1].operation)
		}
	}
	// Report insertion conflicts in original operation order, like the Map in TS.
	for operation := range ops {
		for gap, ins := range insertions {
			if ins.operation != operation {
				continue
			}
			for _, r := range ranges {
				if gap > r.start && gap < r.end {
					return wire.Result{}, fmt.Errorf("edits[%d] inserts inside the original range owned by edits[%d]. Insert at a range boundary or combine the change.", ins.operation, r.operation)
				}
			}
		}
	}
	rangeAt := map[int]preparedRange{}
	for _, r := range ranges {
		rangeAt[r.start] = r
	}
	out := []string{}
	for cursor := 0; cursor <= count; {
		if ins, ok := insertions[cursor]; ok {
			out = append(out, ins.lines...)
		}
		if cursor == count {
			break
		}
		if r, ok := rangeAt[cursor]; ok {
			out = append(out, r.lines...)
			cursor = r.end
		} else {
			out = append(out, original[cursor])
			cursor++
		}
	}
	after := bom + strings.Join(out, ending)
	limit := s.Config.Int("maxWriteBytes", 1000000)
	if len(after) > limit {
		return wire.Result{}, failure("file_too_large", fmt.Sprintf("Edited file would be too large (%d bytes). Limit: %d bytes.", len(after), limit), nil)
	}
	if e = s.checkWrite("edit", before, after); e != nil {
		return wire.Result{}, e
	}
	d := UnifiedDiff(before, after, rel)
	d.Additions = 0
	d.Deletions = 0
	for _, r := range ranges {
		old, next := original[r.start:r.end], r.lines
		pre, suffix := 0, 0
		for pre < len(old) && pre < len(next) && old[pre] == next[pre] {
			pre++
		}
		for suffix < len(old)-pre && suffix < len(next)-pre && old[len(old)-1-suffix] == next[len(next)-1-suffix] {
			suffix++
		}
		d.Additions += len(next) - pre - suffix
		d.Deletions += len(old) - pre - suffix
	}
	for _, ins := range insertions {
		d.Additions += len(ins.lines)
	}
	if !d.Changed {
		return wire.Result{}, fmt.Errorf("Tagged edit would not change %s. Re-read the file and remove no-op operations.", rel)
	}
	if e = ctx.Err(); e != nil {
		return wire.Result{}, e
	}
	if e = writeText(abs, rel, after, &before); e != nil {
		return wire.Result{}, e
	}
	newTag := s.Snapshots.Record(scope, abs, after, 0, 0)
	data := Base(w)
	data["path"] = rel
	data["mode"] = "tagged_lines"
	data["edits_applied"] = len(ops)
	data["changed"] = true
	data["bytes"] = len(after)
	data["base_edit_tag"] = snapshot.Tag
	data["edit_tag"] = newTag
	data["additions"] = d.Additions
	data["deletions"] = d.Deletions
	data["diff"] = wire.Redact(d.Text)
	text := fmt.Sprintf("# Edit File\n\nPath: %s\nOperations applied: %d\nBase edit tag: %s\nNew edit tag: %s\nBytes: %d\nDiff stats: +%d -%d%s", rel, len(ops), snapshot.Tag, newTag, len(after), d.Additions, d.Deletions, DiffBlock(d.Text))
	return wire.Success("edit", "Edit File", data, text), nil
}
func (s *Service) tree(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	abs, rel, e := s.ResolvePath(w, wire.String(a, "path", "."), false)
	if e != nil {
		return wire.Result{}, e
	}
	st, e := os.Stat(abs)
	if e != nil {
		return wire.Result{}, FileError(e, "stat", abs)
	}
	if !st.IsDir() {
		return wire.Result{}, failure("path_not_directory", "Not a directory: "+rel, nil)
	}
	maxDepth := max(1, min(12, wire.Int(a, "max_depth", 4)))
	maxEntries := max(1, min(3000, wire.Int(a, "max_entries", 800)))
	hidden := wire.Bool(a, "include_hidden", false)
	root := rel
	if root != "." {
		root += "/"
	}
	lines := []string{root}
	entries := 0
	truncated := false
	var walk func(string, int, string) error
	walk = func(dir string, depth int, prefix string) error {
		if depth >= maxDepth || truncated {
			return nil
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		all, e := os.ReadDir(dir)
		if e != nil {
			return FileError(e, "scandir", dir)
		}
		list := []os.DirEntry{}
		for _, entry := range all {
			if !hidden && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if s.IsBlocked(Rel(filepath.Join(dir, entry.Name()), w.Root)) {
				continue
			}
			list = append(list, entry)
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].IsDir() != list[j].IsDir() {
				return list[i].IsDir()
			}
			return Compare(list[i].Name(), list[j].Name()) < 0
		})
		for i, entry := range list {
			if entries >= maxEntries {
				truncated = true
				return nil
			}
			branch, child := "├── ", "│   "
			if i == len(list)-1 {
				branch, child = "└── ", "    "
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			lines = append(lines, prefix+branch+name)
			entries++
			if entry.IsDir() {
				if e = walk(filepath.Join(dir, entry.Name()), depth+1, prefix+child); e != nil {
					return e
				}
			}
			if truncated {
				return nil
			}
		}
		return nil
	}
	if e = walk(abs, 0, ""); e != nil {
		return wire.Result{}, e
	}
	if truncated {
		lines = append(lines, fmt.Sprintf("...[tree truncated after %d entries]", entries))
	}
	text := strings.Join(lines, "\n")
	data := Base(w)
	data["text"] = text
	data["entries"] = entries
	data["truncated"] = truncated
	return wire.Success("tree", "File Tree", data, text), nil
}
