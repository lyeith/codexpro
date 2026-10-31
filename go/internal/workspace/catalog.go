// Package workspace owns catalogs and lease identity, independently of tool sessions.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
	_ "github.com/mattn/go-sqlite3"
)

type CreationRoot struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Root  string `json:"root"`
}
type Catalog struct {
	Version        int            `json:"version"`
	DefaultProject string         `json:"defaultProject,omitempty"`
	Projects       []wire.Project `json:"projects"`
	CreationRoots  []CreationRoot `json:"creationRoots,omitempty"`
}
type session struct {
	selected string
	opened   []string
	times    map[string]string
}
type Manager struct {
	cfg          *config.Config
	mu           sync.RWMutex
	catalog      Catalog
	catalogBytes []byte
	sessions     map[string]*session
	createMu     sync.Mutex
	leases       map[string]*Lease
	leaseOrder   []string
	leaseLocks   leaseLockTable
	db           *sql.DB
	LastChange   func(string) any
	// Configure before serving. This immutable snapshot check runs under the
	// lease ownership lock; it must not block or reenter workspace operations.
	HasActiveJobs func(workspaceID string) bool
}

var projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var controls = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func Hash(text string) string      { s := sha256.Sum256([]byte(text)); return hex.EncodeToString(s[:]) }
func IDForRoot(root string) string { return "ws_" + Hash(root)[:24] }
func IsSubpath(child, parent string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}
func expandHome(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		h, _ := os.UserHomeDir()
		if s == "~" {
			return h
		}
		return filepath.Join(h, s[2:])
	}
	return s
}
func canonical(root, base, label string) (string, error) {
	root = expandHome(root)
	if !filepath.IsAbs(root) {
		root = filepath.Join(base, root)
	}
	root = filepath.Clean(root)
	s, e := os.Stat(root)
	if e != nil {
		return "", fmt.Errorf("%s does not exist: %s", label, root)
	}
	if !s.IsDir() {
		return "", fmt.Errorf("%s is not a directory: %s", label, root)
	}
	return filepath.EvalSymlinks(root)
}
func LoadCatalog(path string) (Catalog, []byte, error) {
	var c Catalog
	f, e := os.Open(path)
	if e != nil {
		return c, nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 1000001))
	if e != nil {
		return c, nil, e
	}
	if len(b) > 1000000 {
		return c, nil, fmt.Errorf("Projects file exceeds 1000000 bytes.")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, nil, fmt.Errorf("Could not parse projects file %s: %v", path, e)
	}
	if c.Version != 1 {
		return c, nil, fmt.Errorf("Projects file version must be 1.")
	}
	if len(c.Projects) < 1 || len(c.Projects) > 256 {
		return c, nil, fmt.Errorf("Projects file must contain 1-256 projects.")
	}
	if len(c.CreationRoots) > 256 {
		return c, nil, fmt.Errorf("Projects file must contain 0-256 creationRoots.")
	}
	ids, roots := map[string]bool{}, map[string]bool{}
	for i := range c.Projects {
		p := &c.Projects[i]
		label := fmt.Sprintf("projects[%d]", i)
		if !projectIDPattern.MatchString(p.ID) {
			return c, nil, fmt.Errorf("%s.id must be 1-64 lowercase letters, numbers, dots, underscores, or dashes.", label)
		}
		if p.Label == "" {
			p.Label = p.ID
		}
		p.Label = strings.TrimSpace(p.Label)
		if p.Label == "" || len([]rune(p.Label)) > 120 || controls.MatchString(p.Label) {
			return c, nil, fmt.Errorf("%s.label must be 1-120 characters without control characters.", label)
		}
		if p.Root == "" {
			return c, nil, fmt.Errorf("%s.root must be a non-empty path.", label)
		}
		p.Root, e = canonical(p.Root, filepath.Dir(path), label+".root")
		if e != nil {
			return c, nil, e
		}
		if p.BaseRef != "" && (strings.HasPrefix(p.BaseRef, "-") || regexp.MustCompile(`[\x00-\x20\x7f]`).MatchString(p.BaseRef) || len(p.BaseRef) > 256) {
			return c, nil, fmt.Errorf("%s.baseRef must be a Git ref without whitespace, control characters, or a leading dash.", label)
		}
		if p.MaxWorktrees < 0 || p.MaxWorktrees > 512 {
			return c, nil, fmt.Errorf("%s.maxWorktrees must be an integer from 1 to 512.", label)
		}
		if ids[p.ID] {
			return c, nil, fmt.Errorf("Duplicate project id: %s", p.ID)
		}
		if roots[p.Root] {
			return c, nil, fmt.Errorf("Duplicate canonical project root: %s", p.Root)
		}
		ids[p.ID], roots[p.Root] = true, true
	}
	for i := range c.CreationRoots {
		p := &c.CreationRoots[i]
		if !projectIDPattern.MatchString(p.ID) {
			return c, nil, fmt.Errorf("creationRoots[%d].id must be 1-64 lowercase letters, numbers, dots, underscores, or dashes.", i)
		}
		if p.Label == "" {
			p.Label = p.ID
		}
		p.Label = strings.TrimSpace(p.Label)
		p.Root, e = canonical(p.Root, filepath.Dir(path), fmt.Sprintf("creationRoots[%d].root", i))
		if e != nil {
			return c, nil, e
		}
		if ids[p.ID] {
			return c, nil, fmt.Errorf("Duplicate project or creation-root id: %s", p.ID)
		}
		if roots[p.Root] {
			return c, nil, fmt.Errorf("Duplicate canonical project or creation-root path: %s", p.Root)
		}
		ids[p.ID], roots[p.Root] = true, true
	}
	if c.DefaultProject == "" {
		c.DefaultProject = c.Projects[0].ID
	}
	found := false
	for _, p := range c.Projects {
		found = found || p.ID == c.DefaultProject
	}
	if !found {
		return c, nil, fmt.Errorf("defaultProject must name one of the configured project ids.")
	}
	return c, b, nil
}
func New(cfg *config.Config) (*Manager, error) {
	c, b, e := LoadCatalog(cfg.ProjectsFile)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(cfg.Home, 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite3", filepath.Join(cfg.Home, "workspaces.sqlite")+"?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(4)
	if _, e = db.Exec("CREATE TABLE IF NOT EXISTS leases(id TEXT PRIMARY KEY,revision INTEGER NOT NULL,body BLOB NOT NULL)"); e != nil {
		db.Close()
		return nil, e
	}
	m := &Manager{cfg: cfg, catalog: c, catalogBytes: b, sessions: map[string]*session{}, leases: map[string]*Lease{}, db: db}
	cfg.AllowedRoots = nil
	for _, p := range c.Projects {
		cfg.AllowedRoots = append(cfg.AllowedRoots, p.Root)
	}
	if e = m.loadLeases(); e != nil {
		db.Close()
		return nil, e
	}
	return m, nil
}
func (m *Manager) Close() error { return m.db.Close() }
func (m *Manager) Projects() []wire.Project {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]wire.Project(nil), m.catalog.Projects...)
}
func (m *Manager) Project(id string) (wire.Project, bool) {
	for _, p := range m.Projects() {
		if p.ID == id {
			return p, true
		}
	}
	return wire.Project{}, false
}
func (m *Manager) DefaultProject() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.catalog.DefaultProject
}
func (m *Manager) CreationRoots() []CreationRoot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]CreationRoot(nil), m.catalog.CreationRoots...)
}
func (m *Manager) Mode() string {
	if m.cfg.String("worktreeMode", "off") == "mcp" {
		return "mcp"
	}
	return "direct"
}
func (m *Manager) UnknownProject(ids []string) error {
	known := []string{}
	for _, p := range m.Projects() {
		known = append(known, p.ID)
	}
	plural := ""
	if len(ids) > 1 {
		plural = "s"
	}
	return &wire.Error{Code: "project_unknown", Message: fmt.Sprintf("Unknown project_id%s: %s. Configured project ids: %s. If the project you need is not in that list it is not configured on this server; ask the user to add it to the catalog (or use create_project when it is available). Do not guess other ids.", plural, strings.Join(ids, ", "), strings.Join(known, ", ")), Data: map[string]any{"retry_unchanged": false, "recovery": map[string]any{"tool": "list_projects", "message": "Pick one of the configured project ids."}, "known_project_ids": known, "unknown_project_ids": ids}}
}
func (m *Manager) sessionLocked(scope wire.Scope) *session {
	key := scope.PrincipalID + "\x00" + scope.SessionID
	s := m.sessions[key]
	if s == nil {
		s = &session{times: map[string]string{}}
		m.sessions[key] = s
	}
	return s
}
func (m *Manager) OpenProject(scope wire.Scope, id string, selectIt bool) (wire.Workspace, error) {
	p, ok := m.Project(id)
	if !ok {
		return wire.Workspace{}, m.UnknownProject([]string{id})
	}
	stat, e := os.Stat(p.Root)
	if e != nil {
		return wire.Workspace{}, &wire.Error{Code: "workspace_root_invalid", Message: "Workspace root does not exist: " + p.Root, Data: map[string]any{"retry_unchanged": false}}
	}
	if !stat.IsDir() {
		return wire.Workspace{}, &wire.Error{Code: "workspace_root_invalid", Message: "Workspace root is not a directory: " + p.Root, Data: map[string]any{"retry_unchanged": false}}
	}
	actual, e := filepath.EvalSymlinks(p.Root)
	if e != nil {
		return wire.Workspace{}, e
	}
	if actual != p.Root {
		return wire.Workspace{}, wire.Errorf("workspace_root_invalid", "Workspace root changed identity: %s", p.Root)
	}
	ws := wire.Workspace{ID: IDForRoot(p.Root), ProjectID: p.ID, Root: p.Root, Mode: "direct", BaseRef: p.BaseRef}
	m.mu.Lock()
	s := m.sessionLocked(scope)
	if _, ok = s.times[ws.ID]; !ok {
		s.times[ws.ID] = wire.Now()
		s.opened = append(s.opened, ws.ID)
	}
	if selectIt {
		s.selected = ws.ID
	}
	m.mu.Unlock()
	return ws, nil
}
func (m *Manager) Resolve(ctx context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	if e := ctx.Err(); e != nil {
		return wire.Workspace{}, e
	}
	m.mu.RLock()
	lease := m.leases[id]
	m.mu.RUnlock()
	if lease != nil {
		return m.resolveLease(ctx, scope, id)
	}
	if m.Mode() == "mcp" {
		if id != "" {
			return m.resolveLease(ctx, scope, id)
		}
		return wire.Workspace{}, wire.Errorf("", "workspace_id is required in MCP worktree mode. Call create_workspace once, or reuse the workspace_id returned earlier.")
	}
	if id == "" {
		m.mu.Lock()
		s := m.sessionLocked(scope)
		id = s.selected
		m.mu.Unlock()
		if id == "" {
			return m.OpenProject(scope, m.DefaultProject(), true)
		}
	}
	for _, p := range m.Projects() {
		if IDForRoot(p.Root) == id {
			return m.OpenProject(scope, p.ID, false)
		}
	}
	known := []string{}
	for _, p := range m.Projects() {
		known = append(known, IDForRoot(p.Root))
	}
	return wire.Workspace{}, &wire.Error{Code: "workspace_unknown", Message: fmt.Sprintf("Unknown workspace_id: %s. Known workspace ids: %s. Use the workspace_id from list_projects or open_workspace.", id, strings.Join(known, ", ")), Data: map[string]any{"retry_unchanged": false, "known_workspace_ids": known, "recovery": map[string]any{"tool": "list_projects", "message": "list_projects returns each project's workspace_id; open_workspace(project_id) also returns it."}}}
}
func (m *Manager) WasOpen(scope wire.Scope, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessionLocked(scope).times[id]
	return ok
}
func (m *Manager) DropSession(scope wire.Scope) {
	m.mu.Lock()
	delete(m.sessions, scope.PrincipalID+"\x00"+scope.SessionID)
	m.mu.Unlock()
}
func (m *Manager) summary(p wire.Project) map[string]any {
	base := p.BaseRef
	if base == "" {
		base = "HEAD"
	}
	limit := p.MaxWorktrees
	if limit == 0 {
		limit = m.cfg.Int("maxWorktrees", 64)
	}
	if limit > m.cfg.Int("maxWorktrees", 64) {
		limit = m.cfg.Int("maxWorktrees", 64)
	}
	return map[string]any{"id": p.ID, "label": p.Label, "default": p.ID == m.DefaultProject(), "baseRef": base, "maxWorktrees": limit}
}
func (m *Manager) ListProjects(scope wire.Scope) wire.Result {
	projects := []any{}
	lines := []string{}
	for _, p := range m.Projects() {
		d := m.summary(p)
		var last any
		if m.LastChange != nil {
			last = m.LastChange(p.ID)
		}
		d["last_recorded_project_change"] = last
		suffix := ""
		if p.ID == m.DefaultProject() {
			suffix = " (default)"
		}
		line := "- " + p.ID + " — " + p.Label + suffix
		if m.Mode() != "mcp" {
			id := IDForRoot(p.Root)
			d["workspace_id"] = id
			line += "; workspace_id=" + id
		}
		line += "; base=" + d["baseRef"].(string)
		if m.Mode() == "mcp" {
			line += fmt.Sprintf("; max_worktrees=%v", d["maxWorktrees"])
		}
		projects = append(projects, d)
		lines = append(lines, line)
	}
	roots := []any{}
	rootText := []string{}
	for _, r := range m.CreationRoots() {
		roots = append(roots, map[string]any{"id": r.ID, "label": r.Label})
		rootText = append(rootText, "- "+r.ID+" — "+r.Label)
	}
	if len(rootText) == 0 {
		rootText = []string{"- none configured"}
	}
	next := "Read-only work can use a project's workspace_id directly with tree/search/read. Before editing, open_workspace(project_id=...) once (or open_workspace(project_ids=[...]) for several) to load AGENTS.md guidance; reuse the returned workspace_ids."
	max := 12
	if m.Mode() == "mcp" {
		next = "Create one isolated workspace with create_workspace(project_id=...)."
		max = 0
	}
	return wire.Success("list_projects", "List Projects", map[string]any{"projects": projects, "count": len(projects), "creation_roots": roots, "creation_root_count": len(roots), "default_project_id": m.DefaultProject(), "multi_open_supported": m.Mode() != "mcp", "max_multi_open_projects": max}, "# Projects\n\n"+strings.Join(lines, "\n")+"\n\n# Creation Roots\n\n"+strings.Join(rootText, "\n")+"\n\n# Next\n\n"+next)
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	if d, e := os.Open(filepath.Dir(path)); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func sortedKeys(m map[string]*Lease) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
