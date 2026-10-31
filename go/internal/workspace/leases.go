package workspace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type Lease struct {
	Version             int    `json:"version"`
	Revision            int    `json:"revision"`
	WorkspaceID         string `json:"workspaceId"`
	ProjectID           string `json:"projectId"`
	RepositoryID        string `json:"repositoryId"`
	RepositoryCommonDir string `json:"repositoryCommonDir"`
	CheckoutRoot        string `json:"checkoutRoot"`
	WorkspaceRoot       string `json:"workspaceRoot"`
	Branch              string `json:"branch"`
	BaseCommit          string `json:"baseCommit"`
	OwnerID             string `json:"ownerId"`
	State               string `json:"state"`
	CreationKeyHash     string `json:"creationKeyHash,omitempty"`
	Label               string `json:"label,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
	LastUsedAt          string `json:"lastUsedAt"`
	ReleasedAt          string `json:"releasedAt,omitempty"`
	Failure             string `json:"failure,omitempty"`
	Health              string `json:"health,omitempty"`
	ObservedAt          string `json:"observedAt,omitempty"`
}
type Repository struct{ Root, Top, Common, Relative, ID string }
type Handle struct {
	Workspace wire.Workspace
	Lease     Lease
	Created   bool
}

func (m *Manager) loadLeases() error {
	rows, e := m.db.Query("SELECT body FROM leases ORDER BY rowid")
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var l Lease
		if e = rows.Scan(&b); e != nil {
			return e
		}
		if e = json.Unmarshal(b, &l); e != nil {
			return fmt.Errorf("invalid persisted lease: %w", e)
		}
		if l.Version != 2 {
			return fmt.Errorf("unsupported private lease version %d", l.Version)
		}
		copy := l
		m.leases[l.WorkspaceID] = &copy
	}
	return rows.Err()
}
func (m *Manager) leaseLock(id string) *sync.RWMutex {
	v, _ := m.leaseLocks.LoadOrStore(id, &sync.RWMutex{})
	return v.(*sync.RWMutex)
}
func (m *Manager) storeLease(l Lease) error {
	b, e := json.Marshal(l)
	if e != nil {
		return e
	}
	_, e = m.db.Exec("INSERT INTO leases(id,revision,body)VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", l.WorkspaceID, l.Revision, b)
	if e != nil {
		return e
	}
	m.mu.Lock()
	copy := l
	m.leases[l.WorkspaceID] = &copy
	m.mu.Unlock()
	return nil
}
func (m *Manager) leaseSnapshot(id string) (Lease, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.leases[id]
	if !ok {
		return Lease{}, false
	}
	return *p, true
}
func (m *Manager) managedRoot() string { return filepath.Join(m.cfg.Home, "worktrees") }
func (m *Manager) inspectRepository(ctx context.Context, root string) (Repository, error) {
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil {
		return Repository{}, e
	}
	top, e := m.runGit(ctx, canonical, "rev-parse", "--show-toplevel")
	if e != nil {
		return Repository{}, e
	}
	top, e = filepath.EvalSymlinks(top)
	if e != nil {
		return Repository{}, e
	}
	if !IsSubpath(canonical, top) {
		return Repository{}, wire.Errorf("", "Configured workspace is not inside its Git top-level directory.")
	}
	common, e := m.runGit(ctx, top, "rev-parse", "--git-common-dir")
	if e != nil {
		return Repository{}, e
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(top, common)
	}
	common, e = filepath.EvalSymlinks(common)
	if e != nil {
		return Repository{}, e
	}
	rel, e := filepath.Rel(top, canonical)
	if e != nil {
		return Repository{}, e
	}
	if rel == "." {
		rel = ""
	}
	return Repository{Root: canonical, Top: top, Common: common, Relative: rel, ID: Hash(common + "\x00" + rel)[:24]}, nil
}
func (m *Manager) assertLeasePaths(l Lease) error {
	expected := filepath.Join(m.managedRoot(), "repositories", l.RepositoryID, "checkouts", Hash(l.WorkspaceID)[:32])
	if filepath.Clean(l.CheckoutRoot) != expected || !IsSubpath(l.WorkspaceRoot, expected) {
		return wire.Errorf("", "Worktree lease paths do not match its workspace_id and project scope.")
	}
	if root, e := filepath.EvalSymlinks(l.WorkspaceRoot); e == nil && !IsSubpath(root, expected) {
		return wire.Errorf("", "Workspace scope resolves outside its managed checkout.")
	}
	return nil
}
func leaseWorkspace(l Lease) wire.Workspace {
	return wire.Workspace{ID: l.WorkspaceID, ProjectID: l.ProjectID, Root: l.WorkspaceRoot, Mode: "worktree", Branch: l.Branch, BaseRef: l.BaseCommit}
}
func (m *Manager) resolveLease(ctx context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	l, ok := m.leaseSnapshot(id)
	if !ok {
		return wire.Workspace{}, wire.Errorf("", "Unknown workspace_id.")
	}
	if l.OwnerID != scope.PrincipalID {
		return wire.Workspace{}, wire.Errorf("", "Workspace access denied for this authenticated principal.")
	}
	if l.State != "ready" {
		return wire.Workspace{}, wire.Errorf("", "Workspace is not ready (state: %s).", l.State)
	}
	if l.Health == "identity_mismatch" {
		return wire.Workspace{}, wire.Errorf("", "Managed worktree belongs to a different repository.")
	}
	if e := m.assertLeasePaths(l); e != nil {
		return wire.Workspace{}, e
	}
	if _, e := os.Stat(l.WorkspaceRoot); e != nil {
		return wire.Workspace{}, wire.Errorf("", "Workspace checkout is missing; it will be reconciled on restart.")
	}
	return leaseWorkspace(l), ctx.Err()
}
func (m *Manager) Execute(ctx context.Context, scope wire.Scope, id string, mutating bool, fn func() (wire.Result, error)) (wire.Result, error) {
	if _, ok := m.leaseSnapshot(id); !ok {
		return fn()
	}
	lock := m.leaseLock(id)
	if mutating {
		lock.Lock()
		defer lock.Unlock()
	} else {
		lock.RLock()
		defer lock.RUnlock()
	}
	if _, e := m.resolveLease(ctx, scope, id); e != nil {
		return wire.Result{}, e
	}
	result, e := fn()
	if e == nil {
		m.mu.Lock()
		if l := m.leases[id]; l != nil {
			l.LastUsedAt = wire.Now()
		}
		m.mu.Unlock()
	}
	return result, e
}
func (m *Manager) CreateWorkspace(ctx context.Context, scope wire.Scope, a map[string]any) (Handle, error) {
	if m.Mode() != "mcp" {
		return Handle{}, wire.Errorf("", "MCP worktree mode is disabled.")
	}
	projectID := strings.TrimSpace(wire.String(a, "project_id", ""))
	if projectID == "" {
		ps := m.Projects()
		if len(ps) != 1 {
			return Handle{}, wire.Errorf("", "project_id is required when multiple projects are configured. Call list_projects first.")
		}
		projectID = ps[0].ID
	}
	p, ok := m.Project(projectID)
	if !ok {
		return Handle{}, wire.Errorf("", "Unknown project_id: %s. Call list_projects first.", projectID)
	}
	repo, e := m.inspectRepository(ctx, p.Root)
	if e != nil {
		return Handle{}, e
	}
	if IsSubpath(m.managedRoot(), repo.Top) || IsSubpath(repo.Top, m.managedRoot()) {
		return Handle{}, wire.Errorf("", "Managed worktree storage must not overlap project %s (%s).", p.ID, repo.Top)
	}
	ref := strings.TrimSpace(wire.String(a, "base_ref", p.BaseRef))
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n\t ") {
		return Handle{}, wire.Errorf("", "base_ref must be a non-empty Git ref without whitespace, control characters, or a leading dash.")
	}
	base, e := m.runGit(ctx, repo.Top, "rev-parse", "--verify", ref+"^{commit}")
	if e != nil {
		return Handle{}, e
	}
	key := strings.TrimSpace(wire.String(a, "idempotency_key", ""))
	if len(key) > 200 || strings.ContainsAny(key, "\x00\r\n") {
		return Handle{}, wire.Errorf("", "idempotency_key must be at most 200 characters without control newlines.")
	}
	hash := ""
	if key != "" {
		hash = Hash(p.ID + "\x00" + repo.ID + "\x00" + scope.PrincipalID + "\x00" + key)
	} else if scope.SessionID != "" {
		hash = Hash(p.ID + "\x00" + repo.ID + "\x00" + scope.PrincipalID + "\x00transport:" + scope.SessionID + "\x00request:" + scope.RequestID)
	}
	// Serialize catalog quota reservations and Git worktree changes; ordinary reads never take this lock.
	m.createMu.Lock()
	defer m.createMu.Unlock()
	m.mu.RLock()
	existing := Lease{}
	found := false
	retained, projectRetained := 0, 0
	for _, l := range m.leases {
		if hash != "" && l.CreationKeyHash == hash {
			existing = *l
			found = true
		}
		if l.State != "removing" {
			retained++
			if l.ProjectID == p.ID {
				projectRetained++
			}
		}
	}
	m.mu.RUnlock()
	if found {
		if existing.OwnerID != scope.PrincipalID || existing.State != "ready" {
			return Handle{}, wire.Errorf("", "A previous workspace creation with this idempotency key is in state %s.", existing.State)
		}
		if (wire.String(a, "base_ref", "") != "" && base != existing.BaseCommit) || existing.ProjectID != p.ID {
			return Handle{}, wire.Errorf("", "idempotency_key was already used with a different project or base_ref.")
		}
		return Handle{Workspace: leaseWorkspace(existing), Lease: existing}, nil
	}
	limit := m.cfg.Int("maxWorktrees", 64)
	if retained >= limit {
		return Handle{}, wire.Errorf("", "Global worktree limit reached (%d). Remove an existing clean managed workspace before creating another.", limit)
	}
	plimit := p.MaxWorktrees
	if plimit == 0 || plimit > limit {
		plimit = limit
	}
	if projectRetained >= plimit {
		return Handle{}, wire.Errorf("", "Worktree limit reached for project %s (%d).", p.ID, plimit)
	}
	raw := make([]byte, 24)
	if _, e = rand.Read(raw); e != nil {
		return Handle{}, e
	}
	id := "wt_" + base64.RawURLEncoding.EncodeToString(raw)
	internal := Hash(id)
	checkout := filepath.Join(m.managedRoot(), "repositories", repo.ID, "checkouts", internal[:32])
	root := filepath.Join(checkout, repo.Relative)
	now := wire.Now()
	label := strings.Join(strings.Fields(wire.String(a, "label", "")), " ")
	if len([]rune(label)) > 120 {
		label = string([]rune(label)[:120])
	}
	l := Lease{Version: 2, Revision: 1, WorkspaceID: id, ProjectID: p.ID, RepositoryID: repo.ID, RepositoryCommonDir: repo.Common, CheckoutRoot: checkout, WorkspaceRoot: root, Branch: "codexpro/mcp/" + internal[:24], BaseCommit: base, OwnerID: scope.PrincipalID, State: "provisioning", CreationKeyHash: hash, Label: label, CreatedAt: now, UpdatedAt: now, LastUsedAt: now, Health: "unknown"}
	if e = os.MkdirAll(filepath.Dir(checkout), 0700); e != nil {
		return Handle{}, e
	}
	if e = m.storeLease(l); e != nil {
		return Handle{}, e
	}
	_, e = m.runGit(ctx, repo.Top, "worktree", "add", "-b", l.Branch, checkout, base)
	if e == nil {
		var actual Repository
		actual, e = m.inspectRepository(ctx, root)
		if e == nil && actual.Common != repo.Common {
			e = wire.Errorf("", "Created worktree points at a different Git common directory.")
		}
	}
	if e == nil {
		var head string
		head, e = m.runGit(ctx, root, "rev-parse", "HEAD")
		if e == nil && head != base {
			e = wire.Errorf("", "Created worktree HEAD does not match the pinned base commit.")
		}
	}
	l.Revision++
	l.UpdatedAt = wire.Now()
	if e != nil {
		l.State = "failed"
		l.Failure = e.Error()
		_ = m.storeLease(l)
		return Handle{}, e
	}
	l.State = "ready"
	l.Health = "healthy"
	l.ObservedAt = wire.Now()
	if e = m.storeLease(l); e != nil {
		return Handle{}, e
	}
	return Handle{Workspace: leaseWorkspace(l), Lease: l, Created: true}, nil
}
func (m *Manager) ReleaseWorkspace(ctx context.Context, scope wire.Scope, id string) (wire.Workspace, error) {
	lock := m.leaseLock(id)
	lock.Lock()
	defer lock.Unlock()
	ws, e := m.resolveLease(ctx, scope, id)
	if e != nil {
		return ws, e
	}
	l, _ := m.leaseSnapshot(id)
	l.ReleasedAt = wire.Now()
	l.LastUsedAt = l.ReleasedAt
	l.UpdatedAt = l.ReleasedAt
	l.Revision++
	return ws, m.storeLease(l)
}
func (m *Manager) RemoveWorkspace(ctx context.Context, scope wire.Scope, id string) error {
	lock := m.leaseLock(id)
	if !lock.TryLock() {
		return wire.Errorf("", "Workspace still has active operations.")
	}
	defer lock.Unlock()
	ws, e := m.resolveLease(ctx, scope, id)
	if e != nil {
		return e
	}
	l, _ := m.leaseSnapshot(id)
	status, e := m.runGit(ctx, ws.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if e != nil {
		return e
	}
	if status != "" {
		return wire.Errorf("", "Refusing to remove a dirty worktree.")
	}
	p, ok := m.Project(l.ProjectID)
	if !ok {
		return wire.Errorf("", "Project is no longer configured: %s", l.ProjectID)
	}
	l.State = "removing"
	l.Revision++
	l.UpdatedAt = wire.Now()
	if e = m.storeLease(l); e != nil {
		return e
	}
	if _, e = m.runGit(ctx, p.Root, "worktree", "remove", l.CheckoutRoot); e != nil {
		l.State = "ready"
		l.Revision++
		l.Failure = e.Error()
		_ = m.storeLease(l)
		return e
	}
	if _, e = m.db.Exec("DELETE FROM leases WHERE id=?", id); e != nil {
		return e
	}
	m.mu.Lock()
	delete(m.leases, id)
	m.mu.Unlock()
	return nil
}

// Reconcile snapshots identity, probes outside the ownership lock, then applies
// only if the observed revision still matches. A failed probe never changes ownership.
func (m *Manager) Reconcile(ctx context.Context, probe func(context.Context, string) (Repository, error)) error {
	if probe == nil {
		probe = m.inspectRepository
	}
	m.mu.RLock()
	ids := sortedKeys(m.leases)
	m.mu.RUnlock()
	for _, id := range ids {
		if e := ctx.Err(); e != nil {
			return e
		}
		l, ok := m.leaseSnapshot(id)
		if !ok {
			continue
		}
		original := l.Revision
		remove := false
		_, statErr := os.Stat(l.WorkspaceRoot)
		if os.IsNotExist(statErr) {
			if l.State == "provisioning" {
				l.State = "failed"
				l.Failure = "Provisioning was interrupted before the worktree became available."
			} else if l.State == "removing" {
				remove = true
			} else if l.State == "ready" {
				l.State = "orphaned"
				l.Failure = "Managed worktree directory is missing."
			}
			l.Health = "missing"
		} else if statErr != nil {
			l.Health = "unavailable"
			l.Failure = statErr.Error()
		} else if e := m.assertLeasePaths(l); e != nil {
			l.Health = "identity_mismatch"
			l.Failure = e.Error()
		} else {
			info, e := probe(ctx, l.WorkspaceRoot)
			if e != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				l.Health = "unavailable"
				l.Failure = e.Error()
			} else if info.Common != l.RepositoryCommonDir {
				l.Health = "identity_mismatch"
				l.Failure = "Managed worktree belongs to a different repository."
			} else {
				l.Health = "healthy"
				l.Failure = ""
				if l.State == "provisioning" || l.State == "orphaned" || l.State == "removing" {
					l.State = "ready"
				}
			}
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		lock := m.leaseLock(id)
		if !lock.TryLock() {
			continue
		}
		current, ok := m.leaseSnapshot(id)
		if !ok || current.Revision != original {
			lock.Unlock()
			continue
		}
		// Read-only operations update last-use time without writing a journal event.
		// Preserve touches that happened while the health observation was in flight.
		l.LastUsedAt = current.LastUsedAt
		if remove {
			_, e := m.db.ExecContext(ctx, "DELETE FROM leases WHERE id=? AND revision=?", id, original)
			if e == nil {
				m.mu.Lock()
				delete(m.leases, id)
				m.mu.Unlock()
			}
			lock.Unlock()
			if e != nil {
				return e
			}
			continue
		}
		l.ObservedAt = wire.Now()
		l.UpdatedAt = l.ObservedAt
		l.Revision = original + 1
		e := m.storeLease(l)
		lock.Unlock()
		if e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = m.Reconcile(probeCtx, nil)
			cancel()
		}
	}
}
func (m *Manager) WorkspaceList(scope wire.Scope) []map[string]any {
	out := []map[string]any{}
	if m.Mode() == "mcp" {
		m.mu.RLock()
		defer m.mu.RUnlock()
		for _, id := range sortedKeys(m.leases) {
			l := m.leases[id]
			if l.OwnerID == scope.PrincipalID && l.State == "ready" {
				out = append(out, map[string]any{"id": l.WorkspaceID, "root": l.WorkspaceRoot, "openedAt": l.CreatedAt, "kind": "worktree", "branch": l.Branch, "baseCommit": l.BaseCommit, "projectId": l.ProjectID})
			}
		}
		return out
	}
	m.mu.Lock()
	s := m.sessionLocked(scope)
	ids := append([]string(nil), s.opened...)
	times := map[string]string{}
	for k, v := range s.times {
		times[k] = v
	}
	m.mu.Unlock()
	for _, id := range ids {
		for _, p := range m.Projects() {
			if IDForRoot(p.Root) == id {
				out = append(out, map[string]any{"id": id, "root": p.Root, "openedAt": times[id], "kind": "direct", "projectId": p.ID})
			}
		}
	}
	return out
}
