package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Service struct {
	Manager *Manager
	Files   *files.Service
}

func NewService(manager *Manager, f *files.Service) *Service {
	return &Service{Manager: manager, Files: f}
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, a map[string]any) (wire.Result, error) {
	m := s.Manager
	switch name {
	case "list_projects":
		return m.ListProjects(scope), nil
	case "create_project":
		return m.CreateProject(ctx, scope, a)
	case "open_workspace":
		return s.open(ctx, scope, a)
	case "open_current_workspace":
		ws, e := m.OpenProject(scope, m.DefaultProject(), true)
		if e != nil {
			return wire.Result{}, e
		}
		summary, e := m.Summary(ctx, scope, s.Files, ws, SummaryOptions{Tree: wire.Bool(a, "include_tree", false), Depth: wire.Int(a, "max_depth", 2), Entries: 500, Skills: wire.Bool(a, "include_skills", false), GlobalSkills: wire.Bool(a, "include_global_skills", false), GlobalInstructions: true, InstructionBytes: -1})
		if e != nil {
			return wire.Result{}, e
		}
		d := summary.Fields()
		d["selected_workspace_id"], d["project_id"], d["bash_mode"], d["write_mode"], d["tool_mode"] = ws.ID, ws.ProjectID, "full", "workspace", m.cfg.String("toolMode", "standard")
		return wire.Success(name, "Open Current Workspace", d, summary.Text), nil
	case "list_workspaces":
		ws, e := m.Resolve(ctx, scope, "")
		if e != nil {
			return wire.Result{}, e
		}
		current := m.WorkspaceList(scope)
		lines := []string{}
		for _, w := range current {
			line := "- " + wire.String(w, "id", "") + " — " + wire.String(w, "root", "")
			if p := wire.String(w, "projectId", ""); p != "" {
				line += " project=" + p
			}
			if b := wire.String(w, "branch", ""); b != "" {
				line += " [" + b + "]"
			}
			if wire.String(w, "id", "") == ws.ID {
				line += " (selected)"
			}
			line += " (opened " + wire.String(w, "openedAt", "") + ")"
			lines = append(lines, line)
		}
		return wire.Success(name, "List Workspaces", map[string]any{"workspaces": current, "count": len(current), "selected_workspace_id": ws.ID}, strings.Join(lines, "\n")), nil
	case "create_workspace":
		h, e := m.CreateWorkspace(ctx, scope, a)
		if e != nil {
			return wire.Result{}, e
		}
		summary, e := m.Summary(ctx, scope, s.Files, h.Workspace, SummaryOptions{Tree: wire.Bool(a, "include_tree", false), Depth: wire.Int(a, "max_depth", 2), Entries: 500, Skills: wire.Bool(a, "include_skills", true), GlobalSkills: wire.Bool(a, "include_global_skills", true), GlobalInstructions: true, InstructionBytes: -1})
		if e != nil {
			return wire.Result{}, e
		}
		d := summary.Fields()
		d["project_id"], d["branch"], d["base_commit"], d["created"], d["worktree_mode"] = h.Lease.ProjectID, h.Lease.Branch, h.Lease.BaseCommit, h.Created, m.cfg.String("worktreeMode", "off")
		action := "Reused"
		if h.Created {
			action = "Created"
		}
		text := fmt.Sprintf("# %s Isolated Workspace\n\nWorkspace ID: %s\nProject ID: %s\nBranch: %s\nBase commit: %s\n\nPass this workspace_id unchanged to every later repository tool call.\n\n%s", action, h.Workspace.ID, h.Lease.ProjectID, h.Lease.Branch, h.Lease.BaseCommit, summary.Text)
		return wire.Success(name, "Create Isolated Workspace", d, text), nil
	case "release_workspace":
		ws, e := m.ReleaseWorkspace(ctx, scope, wire.String(a, "workspace_id", ""))
		if e != nil {
			return wire.Result{}, e
		}
		return wire.Success(name, "Release Workspace", map[string]any{"workspace_id": ws.ID, "project_id": nullable(ws.ProjectID), "root": ws.Root, "branch": ws.Branch, "released": true}, "# Workspace Released\n\nWorkspace: "+ws.ID+"\nBranch: "+ws.Branch+"\n\nThe worktree and all changes were preserved."), nil
	case "remove_workspace":
		id := wire.String(a, "workspace_id", "")
		if e := m.RemoveWorkspace(ctx, scope, id); e != nil {
			return wire.Result{}, e
		}
		return wire.Success(name, "Remove Clean Workspace", map[string]any{"workspace_id": id, "removed": true, "branch_preserved": true}, "# Workspace Removed\n\nWorkspace: "+id+"\n\nThe clean worktree was removed. Its Git branch was preserved."), nil
	default:
		return wire.Result{}, wire.Errorf("", "Unknown tool: %s", name)
	}
}
func (s *Service) open(ctx context.Context, scope wire.Scope, a map[string]any) (wire.Result, error) {
	m := s.Manager
	if m.Mode() != "mcp" && wire.String(a, "workspace_id", "") != "" {
		for _, k := range []string{"root", "path", "project_id", "project_ids"} {
			if v, ok := a[k]; ok && v != nil && v != "" {
				return wire.Result{}, wire.Errorf("", "Use workspace_id alone when opening a retained run workspace.")
			}
		}
		ws, e := m.Resolve(ctx, scope, wire.String(a, "workspace_id", ""))
		if e != nil {
			return wire.Result{}, e
		}
		summary, e := m.Summary(ctx, scope, s.Files, ws, SummaryOptions{Depth: 3, Entries: 500, Skills: wire.Bool(a, "include_skills", false), GlobalInstructions: true, InstructionBytes: -1})
		if e != nil {
			return wire.Result{}, e
		}
		d := summary.Instructions.Metadata()
		d["workspace_id"], d["project_id"], d["root"], d["skills"], d["git_status"] = ws.ID, ws.ProjectID, ws.Root, summary.Skills, summary.GitStatus
		return wire.Success("open_workspace", "Open Workspace", d, summary.Text), nil
	}
	requested := []string{}
	seen := map[string]bool{}
	if m.Mode() != "mcp" {
		for _, id := range wire.Strings(a, "project_ids") {
			id = strings.TrimSpace(id)
			if id == "" {
				return wire.Result{}, wire.Errorf("", "project_ids must contain non-empty project ids from list_projects.")
			}
			if !seen[id] {
				requested = append(requested, id)
				seen[id] = true
			}
		}
	}
	arrayMode := len(requested) > 0
	if arrayMode && (wire.String(a, "project_id", "") != "" || wire.String(a, "root", "") != "" || wire.String(a, "path", "") != "") {
		return wire.Result{}, wire.Errorf("", "open_workspace accepts project_ids or one project_id/root/path target, not both.")
	}
	previous := map[string]bool{}
	for _, ws := range m.WorkspaceList(scope) {
		previous[wire.String(ws, "id", "")] = true
	}
	workspaces := []wire.Workspace{}
	if arrayMode {
		unknown := []string{}
		for _, id := range requested {
			if _, ok := m.Project(id); !ok {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			return wire.Result{}, m.UnknownProject(unknown)
		}
		for i, id := range requested {
			ws, e := m.OpenProject(scope, id, i == 0)
			if e != nil {
				return wire.Result{}, e
			}
			workspaces = append(workspaces, ws)
		}
	} else {
		var ws wire.Workspace
		var e error
		if m.Mode() == "mcp" {
			ws, e = m.Resolve(ctx, scope, wire.String(a, "workspace_id", ""))
		} else {
			id := wire.String(a, "project_id", m.DefaultProject())
			ws, e = m.OpenProject(scope, id, true)
		}
		if e != nil {
			return wire.Result{}, e
		}
		workspaces = append(workspaces, ws)
	}
	primary := workspaces[0]
	defaultTree := !arrayMode && (m.Mode() == "mcp" || !previous[primary.ID])
	tree := wire.Bool(a, "include_tree", defaultTree)
	entriesPer := max(1, min(wire.Int(a, "max_entries", 500), 3000))
	if arrayMode && tree {
		entriesPer = max(1, entriesPer/len(workspaces))
	}
	shared := Instructions{Sources: []InstructionSource{}}
	if arrayMode {
		var e error
		shared, e = m.ReadInstructions(ctx, s.Files, primary, InstructionOptions{Global: true, MaxBytes: m.InstructionBudget() / 2})
		if e != nil {
			return wire.Result{}, e
		}
	}
	summaries := make([]Summary, len(workspaces))
	entries := make([]map[string]any, len(workspaces))
	// Each summary runs independently, but the caller's requested order remains stable.
	type response struct {
		index   int
		summary Summary
		err     error
	}
	ch := make(chan response, len(workspaces))
	for i, ws := range workspaces {
		go func(i int, ws wire.Workspace) {
			budget := -1
			if arrayMode {
				budget = (m.InstructionBudget() - min(m.InstructionBudget()/2, len(shared.Text))) / len(workspaces)
			}
			summary, e := m.Summary(ctx, scope, s.Files, ws, SummaryOptions{Tree: tree, Depth: wire.Int(a, "max_depth", 3), Entries: entriesPer, Skills: wire.Bool(a, "include_skills", false), GlobalSkills: wire.Bool(a, "include_global_skills", false), GlobalInstructions: !arrayMode, InstructionBytes: budget})
			ch <- response{i, summary, e}
		}(i, ws)
	}
	var firstErr error
	for range workspaces {
		r := <-ch
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		summaries[r.index] = r.summary
	}
	if firstErr != nil {
		return wire.Result{}, firstErr
	}
	for i, summary := range summaries {
		d := summary.Fields()
		all := Instructions{Sources: append(append([]InstructionSource{}, shared.Sources...), summary.Instructions.Sources...)}
		for k, v := range all.Metadata() {
			d[k] = v
		}
		d["project_id"], d["already_open"] = nullable(workspaces[i].ProjectID), previous[workspaces[i].ID]
		entries[i] = d
	}
	if !arrayMode {
		summary := summaries[0]
		d := summary.Fields()
		d["primary_workspace_id"], d["selected_workspace_id"], d["project_id"], d["already_open"] = primary.ID, primary.ID, nullable(primary.ProjectID), previous[primary.ID]
		d["workspaces"], d["count"], d["include_tree"], d["bash_mode"], d["write_mode"], d["tool_mode"], d["worktree_mode"] = entries, 1, tree, "full", "workspace", m.cfg.String("toolMode", "standard"), m.cfg.String("worktreeMode", "off")
		text := summary.Text
		if _, specified := a["include_tree"]; !specified && previous[primary.ID] {
			text += "\n\nThis workspace was already open. Reuse workspace_id=" + primary.ID + "; another open_workspace call is unnecessary."
		}
		return wire.Success("open_workspace", "Open Workspace", d, text), nil
	}
	label := func(ws wire.Workspace) string {
		if ws.ProjectID != "" {
			return ws.ProjectID
		}
		return ws.ID
	}
	lines := []string{"# Workspaces Opened", "", fmt.Sprintf("Count: %d", len(workspaces)), "Selected primary: " + label(primary) + " (" + primary.ID + ")", "Reuse the returned workspace_ids for later calls; reopening them is unnecessary.", ""}
	already := 0
	complete := true
	ids := []string{}
	projects := []string{}
	trees := []string{}
	boundedEntries := []any{}
	truncatedEntries := 0
	for i, ws := range workspaces {
		entry := entries[i]
		summary := summaries[i]
		state := " (opened)"
		if previous[ws.ID] {
			state = " (already open)"
			already++
		}
		instructionFiles := wire.Strings(entry, "agents_files")
		filesText := strings.Join(instructionFiles, ", ")
		if filesText == "" {
			filesText = "none"
		}
		if !wire.Bool(entry, "agents_complete", false) {
			filesText += " (incomplete)"
			complete = false
		}
		headline := "Git status unavailable"
		for _, line := range strings.Split(summary.GitStatus, "\n") {
			if strings.TrimSpace(line) != "" {
				headline = line
				break
			}
		}
		lines = append(lines, "- "+label(ws)+" — "+ws.ID+state+"\n  Root: "+ws.Root+"\n  Instructions: "+filesText+"\n  Git: "+headline)
		ids = append(ids, ws.ID)
		if ws.ProjectID != "" {
			projects = append(projects, ws.ProjectID)
		}
		if summary.Tree != nil && *summary.Tree != "" {
			trees = append(trees, "## "+label(ws)+" files\n\n"+*summary.Tree)
		}
		b, truncated := wire.BoundedStructured(entry, max(2000, m.cfg.Int("maxOutputBytes", 120000)/len(entries)))
		boundedEntries = append(boundedEntries, b)
		if truncated {
			truncatedEntries++
		}
	}
	lines = append(lines, "", "## AGENTS Instructions", "Apply the shared global instructions first, then the instructions for the project being edited. More specific instructions take precedence.", shared.Text)
	for i, summary := range summaries {
		lines = append(lines, "## "+label(workspaces[i])+" instructions\n\n"+summary.Instructions.Text)
	}
	if len(trees) > 0 {
		lines = append(lines, "")
		lines = append(lines, trees...)
	}
	text, truncated := wire.TruncateUTF8(strings.Join(lines, "\n"), m.cfg.Int("maxOutputBytes", 120000), "\n...[multi-workspace output truncated]")
	treeEntries := 0
	if tree {
		treeEntries = entriesPer
	}
	d := map[string]any{"workspaces": boundedEntries, "workspace_ids": ids, "project_ids": projects, "count": len(entries), "already_open_count": already, "primary_workspace_id": primary.ID, "selected_workspace_id": primary.ID, "selected_project_id": nullable(primary.ProjectID), "include_tree": tree, "tree_max_entries_per_workspace": treeEntries, "agents_complete": !truncated && complete, "output_truncated": truncated, "workspace_results_truncated_count": truncatedEntries, "bash_mode": "full", "write_mode": "workspace", "tool_mode": m.cfg.String("toolMode", "standard"), "worktree_mode": m.cfg.String("worktreeMode", "off")}
	return wire.Success("open_workspace", "Open Workspace", d, text), nil
}
