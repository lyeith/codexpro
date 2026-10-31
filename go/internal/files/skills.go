package files

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type skillRecord struct {
	Name, Description, Source, Path, Abs string
	Precedence                           int
}

func sourceRank(s string) int {
	switch s {
	case "workspace":
		return 0
	case "user":
		return 1
	case "plugin":
		return 2
	}
	return 3
}
func skillLess(a, b skillRecord) bool {
	if a.Precedence != b.Precedence {
		return a.Precedence < b.Precedence
	}
	if sourceRank(a.Source) != sourceRank(b.Source) {
		return sourceRank(a.Source) < sourceRank(b.Source)
	}
	if c := Compare(a.Name, b.Name); c != 0 {
		return c < 0
	}
	return Compare(a.Path, b.Path) < 0
}
func activeSkills(records []skillRecord) []skillRecord {
	records = append([]skillRecord(nil), records...)
	sort.SliceStable(records, func(i, j int) bool { return skillLess(records[i], records[j]) })
	out := []skillRecord{}
	seen := map[string]bool{}
	for _, r := range records {
		if !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if sourceRank(a.Source) != sourceRank(b.Source) {
			return sourceRank(a.Source) < sourceRank(b.Source)
		}
		if c := Compare(a.Name, b.Name); c != 0 {
			return c < 0
		}
		return Compare(a.Path, b.Path) < 0
	})
	return out
}
func publicSkill(r skillRecord) map[string]any {
	m := map[string]any{"name": r.Name, "source": r.Source, "path": r.Path}
	if r.Description != "" {
		m["description"] = r.Description
	}
	return m
}
func boundedSkillRead(path string, limit int) (string, int, int, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, 0, FileError(e, "open", path)
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", 0, 0, e
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(max(1, min(limit, int(st.Size()))))))
	if e != nil {
		return "", 0, 0, e
	}
	return DecodeText(b), len(b), int(st.Size()), nil
}

var skillName = regexp.MustCompile("(?m)^name:\\s*(.+)$")
var skillDescription = regexp.MustCompile("(?m)^description:\\s*(.+)$")

func frontmatter(text string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	value := TrimJS(m[1])
	if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
		value = value[1:]
	}
	if len(value) > 0 && (value[len(value)-1] == '"' || value[len(value)-1] == '\'') {
		value = value[:len(value)-1]
	}
	return value
}
func skillDisplay(abs, root, home string) string {
	if Within(abs, root) {
		if abs == root {
			return "$WORKSPACE"
		}
		return "$WORKSPACE/" + Rel(abs, root)
	}
	if Within(abs, home) {
		if abs == home {
			return "~"
		}
		return "~/" + Rel(abs, home)
	}
	real := Canonical(abs)
	realHome := Canonical(home)
	if Within(real, realHome) {
		if real == realHome {
			return "~"
		}
		return "~/" + Rel(real, realHome)
	}
	return abs
}
func (s *Service) discoverSkills(ctx context.Context, w wire.Workspace, includeGlobal bool, maxSkills int) ([]skillRecord, error) {
	maxSkills = max(1, min(maxSkills, 500))
	root := Canonical(w.Root)
	home, _ := os.UserHomeDir()
	home = Canonical(s.Config.String("skillHomeDir", home))
	type scanRoot struct{ dir, source string }
	roots := []scanRoot{}
	for _, rel := range []string{".codex/skills", ".agents/skills", "skills"} {
		dir, e := filepath.EvalSymlinks(filepath.Join(root, rel))
		if e == nil && Within(dir, root) {
			roots = append(roots, scanRoot{dir, "workspace"})
		}
	}
	if includeGlobal {
		for _, r := range []scanRoot{{filepath.Join(home, ".codex", "skills"), "user"}, {filepath.Join(home, ".agents", "skills"), "user"}, {filepath.Join(home, ".codex", "plugins", "cache"), "plugin"}} {
			if _, e := os.Stat(r.dir); e == nil {
				roots = append(roots, r)
			}
		}
	}
	type found struct {
		path, source string
		precedence   int
	}
	foundFiles := []found{}
	var walk func(string, int, int, string) error
	walk = func(dir string, depth, precedence int, source string) error {
		if depth < 0 || len(foundFiles) >= maxSkills {
			return nil
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return nil
		}
		sort.SliceStable(entries, func(i, j int) bool { return Compare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, entry := range entries {
			if len(foundFiles) >= maxSkills {
				break
			}
			if entry.Name() == "node_modules" || entry.Name() == ".git" {
				continue
			}
			p := filepath.Join(dir, entry.Name())
			if entry.Type().IsRegular() && entry.Name() == "SKILL.md" {
				foundFiles = append(foundFiles, found{p, source, precedence})
			} else if entry.IsDir() {
				if e = walk(p, depth-1, precedence, source); e != nil {
					return e
				}
			} else if entry.Type()&os.ModeSymlink != 0 {
				if target, e := filepath.EvalSymlinks(p); e == nil {
					if st, e := os.Stat(target); e == nil && st.IsDir() {
						if e = walk(target, depth-1, precedence, source); e != nil {
							return e
						}
					}
				}
			}
		}
		return nil
	}
	for precedence, r := range roots {
		depth := 3
		if strings.Contains(filepath.ToSlash(r.dir), "/plugins/cache") {
			depth = 9
		}
		if e := walk(r.dir, depth, precedence, r.source); e != nil {
			return nil, e
		}
		if len(foundFiles) >= maxSkills {
			break
		}
	}
	out := []skillRecord{}
	seen := map[string]bool{}
	for _, f := range foundFiles {
		real := Canonical(f.path)
		if Within(f.path, root) && !Within(real, root) {
			continue
		}
		text, _, _, _ := boundedSkillRead(real, 16000)
		name := frontmatter(text, skillName)
		if name == "" {
			name = filepath.Base(filepath.Dir(real))
		}
		r := skillRecord{name, frontmatter(text, skillDescription), f.source, skillDisplay(real, root, home), real, f.precedence}
		key := r.Source + ":" + r.Name + ":" + r.Path
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return skillLess(out[i], out[j]) })
	return out, nil
}
func (s *Service) DiscoverSkills(ctx context.Context, w wire.Workspace, global bool, limit int) ([]map[string]any, error) {
	records, e := s.discoverSkills(ctx, w, global, limit)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, r := range activeSkills(records) {
		out = append(out, publicSkill(r))
	}
	return out, nil
}
func (s *Service) loadSkill(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	name := TrimJS(wire.String(a, "name", ""))
	if name == "" {
		return wire.Result{}, fmt.Errorf("Skill name is required.")
	}
	source := wire.String(a, "source", "")
	requestedPath := TrimJS(wire.String(a, "path", ""))
	globalDefault := source == "" || source != "workspace" || requestedPath != "" && !strings.HasPrefix(requestedPath, "$WORKSPACE/")
	records, e := s.discoverSkills(ctx, w, wire.Bool(a, "include_global_skills", globalDefault), max(1, min(500, wire.Int(a, "max_skills", 500))))
	if e != nil {
		return wire.Result{}, e
	}
	active := activeSkills(records)
	candidates := records
	if requestedPath == "" {
		if source != "" {
			candidates = []skillRecord{}
			for _, r := range records {
				if r.Source == source {
					candidates = append(candidates, r)
				}
			}
		}
		candidates = activeSkills(candidates)
	}
	matches := []skillRecord{}
	for _, r := range candidates {
		if r.Name == name && (source == "" || r.Source == source) && (requestedPath == "" || r.Path == requestedPath) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		near := []string{}
		for _, r := range active {
			if strings.Contains(strings.ToLower(r.Name), strings.ToLower(name)) {
				near = append(near, r.Name+" ["+r.Source+"]")
				if len(near) >= 8 {
					break
				}
			}
		}
		suffix := ""
		if requestedPath != "" {
			suffix = " at " + requestedPath
		}
		if len(near) > 0 {
			suffix += ". Similar skills: " + strings.Join(near, ", ")
		}
		return wire.Result{}, fmt.Errorf("Skill not found: %s%s", name, suffix)
	}
	if len(matches) > 1 {
		choices := []string{}
		for _, r := range matches {
			choices = append(choices, r.Name+" ["+r.Source+"] at "+r.Path)
		}
		return wire.Result{}, fmt.Errorf("Multiple exact skill matches remained for %s: %s", name, strings.Join(choices, "; "))
	}
	skill := matches[0]
	if filepath.Base(skill.Abs) != "SKILL.md" {
		return wire.Result{}, fmt.Errorf("Refusing to load non-skill file: %s", skill.Path)
	}
	if skill.Source == "workspace" {
		real, e := filepath.EvalSymlinks(skill.Abs)
		if e != nil || !Within(real, Canonical(w.Root)) {
			return wire.Result{}, fmt.Errorf("Refusing to load workspace skill outside workspace: %s", skill.Path)
		}
	}
	text, n, total, e := boundedSkillRead(skill.Abs, max(1000, min(100000, wire.Int(a, "max_bytes", 40000))))
	if e != nil {
		return wire.Result{}, e
	}
	truncated := total > n
	data := Base(w)
	data["skill"] = publicSkill(skill)
	data["bytes"] = n
	data["total_bytes"] = total
	data["truncated"] = truncated
	data["text"] = text
	marker := ""
	if truncated {
		marker = "\n\n[truncated: increase max_bytes if more context is required]"
	}
	out := fmt.Sprintf("# Load Skill\n\nName: %s\nSource: %s\nPath: %s\nBytes: %d/%d\n\n```markdown\n%s%s\n```", skill.Name, skill.Source, skill.Path, n, total, text, marker)
	return wire.Success("load_skill", "Load Skill", data, out), nil
}
