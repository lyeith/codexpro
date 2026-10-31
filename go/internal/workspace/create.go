package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

var portableInvalid = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f\x7f]`)
var reservedName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\.|$)`)
var scpRepository = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)

func (m *Manager) repository(value, parent string) (string, error) {
	r := strings.TrimSpace(value)
	if r == "" || len(r) > 2048 || controls.MatchString(r) {
		return "", wire.Errorf("", "repository must be a non-empty Git HTTPS/SSH URL or allowed local path without control characters.")
	}
	if r == "~" || strings.HasPrefix(r, "~/") || strings.HasPrefix(r, "./") || strings.HasPrefix(r, "../") || filepath.IsAbs(r) {
		r = expandHome(r)
		if !filepath.IsAbs(r) {
			r = filepath.Join(parent, r)
		}
		stat, e := os.Stat(r)
		if e != nil {
			return "", wire.Errorf("", "Local Git repository does not exist: %s", r)
		}
		if !stat.IsDir() {
			return "", wire.Errorf("", "Local Git repository is not a directory: %s", r)
		}
		r, e = filepath.EvalSymlinks(r)
		if e != nil {
			return "", e
		}
		allowed := false
		for _, p := range m.Projects() {
			allowed = allowed || IsSubpath(r, p.Root)
		}
		for _, p := range m.CreationRoots() {
			allowed = allowed || IsSubpath(r, p.Root)
		}
		if !allowed {
			return "", wire.Errorf("", "Local Git repositories must stay inside an allowed project or creation root.")
		}
		return r, nil
	}
	if strings.HasPrefix(r, "-") || strings.ContainsAny(r, " \t\r\n") {
		return "", wire.Errorf("", "Remote repository URLs must not contain whitespace or begin with a dash.")
	}
	if scpRepository.MatchString(r) {
		return r, nil
	}
	u, e := url.Parse(r)
	if e != nil || u.Scheme == "" {
		return "", wire.Errorf("", "repository must use https://, ssh://, Git scp syntax, or an allowed local path.")
	}
	if u.Scheme != "https" && u.Scheme != "ssh" {
		return "", wire.Errorf("", "repository URLs must use HTTPS or SSH.")
	}
	if u.User != nil {
		_, hasPass := u.User.Password()
		if hasPass || (u.Scheme == "https" && u.User.Username() != "") {
			return "", wire.Errorf("", "repository URLs must not embed credentials.")
		}
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", wire.Errorf("", "repository URLs must not contain query parameters or fragments.")
	}
	return r, nil
}
func (m *Manager) CreateProject(ctx context.Context, scope wire.Scope, a map[string]any) (wire.Result, error) {
	m.createMu.Lock()
	defer m.createMu.Unlock()
	current, e := os.ReadFile(m.cfg.ProjectsFile)
	if e != nil {
		return wire.Result{}, e
	}
	m.mu.RLock()
	expected := append([]byte(nil), m.catalogBytes...)
	catalog := m.catalog
	catalog.Projects = append([]wire.Project(nil), m.catalog.Projects...)
	m.mu.RUnlock()
	if !bytes.Equal(current, expected) {
		return wire.Result{}, wire.Errorf("", "The projects file changed after this CodexPro server started. Restart CodexPro before creating another project so runtime permissions cannot overwrite external catalog edits.")
	}
	if len(catalog.Projects) >= 256 {
		return wire.Result{}, wire.Errorf("", "The projects file already contains the maximum of 256 projects.")
	}
	id := wire.String(a, "project_id", "")
	if !projectIDPattern.MatchString(id) {
		return wire.Result{}, wire.Errorf("", "project_id must be 1-64 lowercase letters, numbers, dots, underscores, or dashes.")
	}
	parentID := wire.String(a, "parent_id", "")
	parent := ""
	for _, p := range catalog.CreationRoots {
		if p.ID == parentID {
			parent = p.Root
		}
		if p.ID == id {
			return wire.Result{}, wire.Errorf("", "Project or creation-root id already exists: %s", id)
		}
	}
	for _, p := range catalog.Projects {
		if parent == "" && p.ID == parentID {
			parent = p.Root
		}
		if p.ID == id {
			return wire.Result{}, wire.Errorf("", "Project or creation-root id already exists: %s", id)
		}
	}
	if parent == "" {
		return wire.Result{}, wire.Errorf("", "Unknown parent_id: %s. Call list_projects first.", parentID)
	}
	source := wire.String(a, "source", "")
	if source != "empty" && source != "git" {
		return wire.Result{}, wire.Errorf("", "source must be empty or git.")
	}
	if m.Mode() == "mcp" && source == "empty" {
		return wire.Result{}, wire.Errorf("", "Raw empty projects cannot be added while MCP worktree mode is enabled. Use source=git so CodexPro can create isolated worktrees.")
	}
	if source == "empty" {
		for _, pair := range [][2]string{{"repository", "repository is valid only when source=git."}, {"initial_branch", "initial_branch is valid only when source=git and repository is omitted."}, {"base_ref", "base_ref is valid only when source=git."}} {
			if _, ok := a[pair[0]]; ok {
				return wire.Result{}, wire.Errorf("", "%s", pair[1])
			}
		}
	}
	directory := strings.TrimSpace(wire.String(a, "directory", id))
	if directory == "" || len([]rune(directory)) > 120 || directory == "." || directory == ".." || portableInvalid.MatchString(directory) || reservedName.MatchString(directory) || strings.HasSuffix(directory, ".") || strings.HasSuffix(directory, " ") {
		return wire.Result{}, wire.Errorf("", "directory must be one portable child-directory name of 1-120 characters without path separators, control characters, reserved device names, or a trailing dot/space.")
	}
	canonicalParent, e := filepath.EvalSymlinks(parent)
	if e != nil {
		return wire.Result{}, e
	}
	if canonicalParent != parent {
		return wire.Result{}, wire.Errorf("", "The selected parent root changed after server startup; restart CodexPro.")
	}
	target := filepath.Join(parent, directory)
	if filepath.Dir(target) != parent {
		return wire.Result{}, wire.Errorf("", "Project directory must be a direct child of the selected parent project.")
	}
	if _, e = os.Lstat(target); e == nil {
		return wire.Result{}, wire.Errorf("", "Project directory already exists: %s", target)
	} else if !os.IsNotExist(e) {
		return wire.Result{}, e
	}
	label := strings.TrimSpace(wire.String(a, "label", id))
	if label == "" || len([]rune(label)) > 120 || controls.MatchString(label) {
		return wire.Result{}, wire.Errorf("", "label must be 1-120 characters without control characters.")
	}
	base := strings.TrimSpace(wire.String(a, "base_ref", ""))
	if _, present := a["base_ref"]; present && (base == "" || len(base) > 256 || strings.HasPrefix(base, "-") || strings.ContainsAny(base, " \t\r\n\x00")) {
		return wire.Result{}, wire.Errorf("", "base_ref must be a Git ref without whitespace, control characters, or a leading dash.")
	}
	max := wire.Int(a, "max_worktrees", 0)
	if _, present := a["max_worktrees"]; present && (max < 1 || max > 512) {
		return wire.Result{}, wire.Errorf("", "max_worktrees must be an integer from 1 to 512.")
	}
	repository := ""
	if v, ok := a["repository"]; ok {
		value, ok := v.(string)
		if !ok {
			return wire.Result{}, wire.Errorf("", "repository must be a string.")
		}
		repository, e = m.repository(value, parent)
		if e != nil {
			return wire.Result{}, e
		}
	}
	branch := strings.TrimSpace(wire.String(a, "initial_branch", "main"))
	if repository != "" {
		if _, ok := a["initial_branch"]; ok {
			return wire.Result{}, wire.Errorf("", "initial_branch cannot be combined with repository; the cloned repository selects its own checkout.")
		}
	}
	if repository == "" && source == "git" && (branch == "" || len(branch) > 255 || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\r\n\x00")) {
		return wire.Result{}, wire.Errorf("", "initial_branch must be a non-empty Git branch name without whitespace, control characters, or a leading dash.")
	}
	if e = os.Mkdir(target, 0755); e != nil {
		return wire.Result{}, e
	}
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(target)
		}
	}()
	if source == "git" {
		template, e := os.MkdirTemp(m.cfg.Home, "git-template-")
		if e != nil {
			return wire.Result{}, e
		}
		defer os.RemoveAll(template)
		hooks := "core.hooksPath=" + template
		if repository != "" {
			_, e = m.runGit(ctx, parent, "-c", hooks, "clone", "--template="+template, "--no-hardlinks", "--", repository, target)
		} else {
			_, e = m.runGit(ctx, parent, "check-ref-format", "--branch", branch)
			if e == nil {
				_, e = m.runGit(ctx, parent, "-c", hooks, "init", "--template="+template, "--initial-branch="+branch, "--", target)
			}
			if e == nil {
				_, e = m.runGit(ctx, target, "-c", hooks, "-c", "commit.gpgSign=false", "-c", "user.name=CodexPro", "-c", "user.email=codexpro@localhost", "commit", "--allow-empty", "--no-verify", "-m", "Initial commit")
			}
		}
		if e != nil {
			return wire.Result{}, e
		}
		ref := base
		if ref == "" {
			ref = "HEAD"
		}
		if _, e = m.runGit(ctx, target, "rev-parse", "--verify", ref+"^{commit}"); e != nil {
			return wire.Result{}, e
		}
	}
	real, e := filepath.EvalSymlinks(target)
	if e != nil {
		return wire.Result{}, e
	}
	if real != target {
		return wire.Result{}, wire.Errorf("", "Created project directory changed identity during initialization.")
	}
	p := wire.Project{ID: id, Label: label, Root: target, BaseRef: base, MaxWorktrees: max}
	catalog.Projects = append(catalog.Projects, p)
	written, e := json.MarshalIndent(catalog, "", "  ")
	if e != nil {
		return wire.Result{}, e
	}
	written = append(written, '\n')
	if len(written) > 1000000 {
		return wire.Result{}, wire.Errorf("", "Projects file would exceed 1000000 bytes.")
	}
	latest, e := os.ReadFile(m.cfg.ProjectsFile)
	if e != nil {
		return wire.Result{}, e
	}
	if !bytes.Equal(latest, expected) {
		return wire.Result{}, wire.Errorf("", "The projects file changed concurrently; no project was registered.")
	}
	mode := os.FileMode(0600)
	if stat, e := os.Stat(m.cfg.ProjectsFile); e == nil {
		mode = stat.Mode().Perm()
	}
	if e = atomicWrite(m.cfg.ProjectsFile, written, mode); e != nil {
		return wire.Result{}, e
	}
	// Rename is the commit point. The catalog already names this directory.
	owned = false
	m.mu.Lock()
	m.catalog = catalog
	m.catalogBytes = written
	m.mu.Unlock()
	next := "open_workspace"
	if m.Mode() == "mcp" {
		next = "create_workspace"
	}
	gitDetail := "not initialized"
	if source == "git" {
		gitDetail = "initialized with an empty initial commit"
		if repository != "" {
			gitDetail = "cloned"
		}
	}
	text := fmt.Sprintf("# Project Created\n\nProject ID: %s\nLabel: %s\nSource: %s\nGit: %s\nCatalog: persisted and active in this server process\n\nNext: call %s with project_id=%s.", id, label, source, gitDetail, next, id)
	return wire.Success("create_project", "Create Project", map[string]any{"project_id": id, "parent_id": parentID, "project": m.summary(p), "root": target, "source": source, "cloned": repository != "", "git_initialized": source == "git", "initial_commit_created": source == "git" && repository == "", "next_tool": next}, text), nil
}
