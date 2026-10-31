package workspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type InstructionSource struct {
	Scope     string `json:"scope"`
	Path      string `json:"path"`
	Loaded    bool   `json:"loaded"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}
type Instructions struct {
	Text    string              `json:"text"`
	Sources []InstructionSource `json:"sources"`
}
type InstructionOptions struct {
	Target            string
	Global, Workspace bool
	MaxBytes          int
}

func (i Instructions) Metadata() map[string]any {
	loaded, complete := false, true
	paths := []string{}
	out := map[string]any{}
	for _, s := range i.Sources {
		if s.Loaded {
			loaded = true
			paths = append(paths, s.Path)
			if s.Scope == "workspace" {
				if _, ok := out["agents_path"]; !ok {
					out["agents_path"] = s.Path
				}
			}
		}
		if !s.Loaded || s.Truncated {
			complete = false
		}
	}
	out["agents_loaded"], out["agents_files"], out["agents_sources"], out["agents_complete"] = loaded, paths, i.Sources, complete
	return out
}
func (m *Manager) InstructionBudget() int {
	return min(60000, m.cfg.Int("maxOutputBytes", 120000)*6/10)
}
func instructionName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "agents.md", ".agents.md"} {
		for _, entry := range entries {
			if entry.Name() == name {
				return entry.Name(), nil
			}
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), name) {
				return entry.Name(), nil
			}
		}
	}
	return "", nil
}
func decodeInstruction(b []byte, truncated bool) string {
	if truncated {
		// StringDecoder retains an incomplete trailing UTF-8 sequence until the next page.
		for n := len(b); n > 0 && n > len(b)-utf8.UTFMax; n-- {
			if utf8.RuneStart(b[n-1]) {
				if !utf8.FullRune(b[n-1:]) {
					b = b[:n-1]
				}
				break
			}
		}
	}
	return strings.ToValidUTF8(string(b), "�")
}
func (m *Manager) ReadInstructions(ctx context.Context, f *files.Service, ws wire.Workspace, opts InstructionOptions) (Instructions, error) {
	type scopeDir struct{ scope, dir, rel string }
	dirs := []scopeDir{}
	home, _ := os.UserHomeDir()
	codexDir := m.cfg.String("codexDir", filepath.Join(home, ".codex"))
	if opts.Global {
		dirs = append(dirs, scopeDir{"global", codexDir, ""})
	}
	if opts.Workspace {
		abs, rel, e := f.ResolvePath(ws, opts.Target, false)
		if e != nil {
			return Instructions{}, e
		}
		st, e := os.Stat(abs)
		if e != nil && !os.IsNotExist(e) {
			return Instructions{}, files.FileError(e, "stat", abs)
		}
		if e != nil || !st.IsDir() {
			rel = filepath.Dir(rel)
		}
		dirs = append(dirs, scopeDir{"workspace", ws.Root, "."})
		if rel != "." {
			parts := strings.Split(rel, string(filepath.Separator))
			for i := 1; i <= len(parts); i++ {
				r := filepath.Join(parts[:i]...)
				if _, _, e = f.ResolvePath(ws, r, false); e != nil {
					return Instructions{}, e
				}
				dirs = append(dirs, scopeDir{"workspace", filepath.Join(ws.Root, r), r})
			}
		}
	}
	type selectedFile struct {
		source InstructionSource
		abs    string
	}
	selected := []selectedFile{}
	for _, d := range dirs {
		if e := ctx.Err(); e != nil {
			return Instructions{}, e
		}
		name, e := instructionName(d.dir)
		if e != nil {
			p := d.rel
			if p == "" {
				p = d.dir
			}
			selected = append(selected, selectedFile{source: InstructionSource{Scope: d.scope, Path: p, Error: "Cannot discover instruction files in this directory."}})
			continue
		}
		if name == "" {
			continue
		}
		p := filepath.Join(d.rel, name)
		if d.scope == "global" {
			p = filepath.Join(d.dir, name)
		}
		selected = append(selected, selectedFile{source: InstructionSource{Scope: d.scope, Path: filepath.ToSlash(p)}, abs: filepath.Join(d.dir, name)})
	}
	budget := opts.MaxBytes
	if budget < 0 {
		budget = m.InstructionBudget()
	}
	budget = max(0, min(budget, m.cfg.Int("maxReadBytes", 180000)))
	perFile := budget / max(1, len(selected))
	chunks := []string{}
	sources := []InstructionSource{}
	seen := map[string]bool{}
	for _, s := range selected {
		if e := ctx.Err(); e != nil {
			return Instructions{}, e
		}
		source := s.source
		var readErr error
		if source.Error != "" {
			readErr = fmt.Errorf("%s", source.Error)
		}
		resolved := s.abs
		if readErr == nil && source.Scope == "workspace" {
			resolved, _, readErr = f.ResolvePath(ws, source.Path, false)
		}
		var real string
		if readErr == nil {
			real, readErr = filepath.EvalSymlinks(resolved)
		}
		if readErr == nil && seen[real] {
			continue
		}
		if readErr == nil {
			seen[real] = true
		}
		var data []byte
		if readErr == nil {
			var handle *os.File
			handle, readErr = os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if readErr == nil {
				var st os.FileInfo
				st, readErr = handle.Stat()
				if readErr == nil && !st.Mode().IsRegular() {
					readErr = fmt.Errorf("Instructions must be a regular text file.")
				}
				if readErr == nil {
					data, readErr = io.ReadAll(io.LimitReader(handle, int64(perFile+1)))
				}
				handle.Close()
			}
		}
		if readErr == nil && bytes.IndexByte(data, 0) >= 0 {
			readErr = fmt.Errorf("Instructions must be a text file.")
		}
		title := "### " + source.Scope + " instructions: " + source.Path + "\n\n"
		if readErr != nil {
			if source.Error == "" {
				source.Error = readErr.Error()
			}
			chunks = append(chunks, title+"[Instructions unreadable: "+source.Error+"]")
		} else {
			source.Truncated = len(data) > perFile
			source.Loaded = perFile > 0
			text := decodeInstruction(data[:min(len(data), perFile)], source.Truncated)
			if source.Truncated {
				text += "\n[Instructions truncated by the response budget; this scope is incomplete.]"
			}
			chunks = append(chunks, title+wire.Redact(text))
		}
		sources = append(sources, source)
	}
	global, local := false, false
	for _, s := range sources {
		global = global || s.Scope == "global"
		local = local || s.Scope == "workspace"
	}
	if opts.Global && !global {
		chunks = append([]string{"Global instructions: none found in " + codexDir + "."}, chunks...)
	}
	if opts.Workspace && !local {
		chunks = append(chunks, "Workspace instructions: none found for this target.")
	}
	return Instructions{Text: strings.Join(chunks, "\n\n"), Sources: sources}, nil
}

type SummaryOptions struct {
	Tree                                     bool
	Depth, Entries                           int
	Skills, GlobalSkills, GlobalInstructions bool
	InstructionBytes                         int
}
type Summary struct {
	Text         string
	Workspace    wire.Workspace
	Instructions Instructions
	Skills       []string
	Inventory    []map[string]any
	Counts       map[string]int
	Tree         *string
	GitStatus    string
}

func (m *Manager) Summary(ctx context.Context, scope wire.Scope, f *files.Service, ws wire.Workspace, opts SummaryOptions) (Summary, error) {
	s := Summary{Workspace: ws, Skills: []string{}, Inventory: []map[string]any{}, Counts: map[string]int{"total": 0, "workspace": 0, "user": 0, "plugin": 0, "other": 0}}
	if opts.Skills {
		inventory, e := f.DiscoverSkills(ctx, ws, opts.GlobalSkills, 120)
		if e != nil {
			return s, e
		}
		s.Inventory = inventory
		for _, skill := range inventory {
			s.Skills = append(s.Skills, wire.String(skill, "name", ""))
			source := wire.String(skill, "source", "other")
			s.Counts[source]++
		}
		s.Counts["total"] = len(inventory)
	}
	var e error
	s.Instructions, e = m.ReadInstructions(ctx, f, ws, InstructionOptions{Target: ".", Global: opts.GlobalInstructions, Workspace: true, MaxBytes: opts.InstructionBytes})
	if e != nil {
		return s, e
	}
	if opts.Tree {
		result, e := f.Call(ctx, scope, "tree", map[string]any{"workspace_id": ws.ID, "path": ".", "max_depth": max(1, min(opts.Depth, 8)), "max_entries": max(1, min(opts.Entries, 3000)), "include_hidden": false})
		if e != nil {
			return s, e
		}
		text := wire.String(result.StructuredContent, "tree", "")
		if text == "" {
			text = wire.String(result.StructuredContent, "text", "")
		}
		s.Tree = &text
	}
	status := m.gitText(ctx, ws.Root, "status", "--short", "--branch")
	log := m.gitText(ctx, ws.Root, "log", "--max-count=5", "--oneline", "--decorate")
	s.GitStatus = status
	projectText := ""
	if ws.ProjectID != "" {
		projectText = "\nProject: " + ws.ProjectID
	}
	worktreeText := ""
	if ws.Mode == "worktree" {
		worktreeText = "\nWorkspace kind: managed Git worktree\nBranch: " + ws.Branch + "\nBase commit: " + ws.BaseRef
	}
	skillText := "Skills: skipped. Pass include_skills=true if skill discovery is needed."
	if opts.Skills {
		skillText = fmt.Sprintf("Skills: %d total (%d workspace, %d user, %d plugin, %d other).", s.Counts["total"], s.Counts["workspace"], s.Counts["user"], s.Counts["plugin"], s.Counts["other"])
	}
	treeText := ""
	if s.Tree != nil && *s.Tree != "" {
		treeText = "\n## Files\n\n" + *s.Tree
	}
	s.Text = fmt.Sprintf("# Workspace\n\nWorkspace: %s\nRoot: %s%s%s\nBash mode: full\nWrite mode: workspace\nHandoff tools: off\nDebug activity: %s\nTool mode: %s\n\n## AGENTS Instructions\n\nApply global instructions first, then workspace instructions; more specific instructions take precedence.\n\n%s\n%s\n\n## Git status\n\n%s\n\n## Recent commits\n\n%s\n%s", ws.ID, ws.Root, projectText, worktreeText, m.cfg.String("auditMode", "metadata"), m.cfg.String("toolMode", "standard"), s.Instructions.Text, skillText, status, log, treeText)
	return s, nil
}
func (m *Manager) gitText(ctx context.Context, root string, args ...string) string {
	out, e := m.runGit(ctx, root, args...)
	if e != nil {
		return wire.Redact(e.Error())
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "(no output)"
	}
	return wire.Redact(out)
}
func (s Summary) Fields() map[string]any {
	out := s.Instructions.Metadata()
	out["workspace_id"], out["root"], out["skills"], out["skill_inventory"], out["skill_counts"], out["git_status"] = s.Workspace.ID, s.Workspace.Root, s.Skills, s.Inventory, s.Counts, s.GitStatus
	if s.Tree != nil {
		out["tree"] = *s.Tree
	}
	if s.Workspace.Branch != "" {
		out["branch"] = s.Workspace.Branch
	}
	if s.Workspace.Mode == "worktree" {
		out["base_commit"] = s.Workspace.BaseRef
	}
	return out
}
