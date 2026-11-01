// Package config loads the Go service's configuration. The production service
// shares the operator configuration in ~/.config/codexpro (EnvironmentFile,
// project catalog, bearer token) with the TypeScript release it replaces, but
// keeps its own state directory: the TypeScript state tree ~/.codexpro stays
// untouched so that a rollback finds it exactly as it was left.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type Config struct {
	Home                string
	TokenFile           string
	ProjectsFile        string
	Listen              string
	EnvFile             string
	HostResourcesHelper string
	BlockedGlobs        []string
	AllowedRoots        []string
	mu                  sync.RWMutex
	values              map[string]any
}

func New(home string) *Config {
	return &Config{Home: home, Listen: "127.0.0.1:7210",
		TokenFile: filepath.Join(home, "http-token"), ProjectsFile: filepath.Join(home, "projects.json"),
		EnvFile: filepath.Join(home, "limits.env"), BlockedGlobs: []string{".git", ".git/**", "**/.git/**", "node_modules", "node_modules/**", "**/node_modules/**", ".env", ".env/**", ".env.*", ".env.*/**", "**/.env", "**/.env/**", "**/.env.*", "**/.env.*/**", "**/*.pem", "**/*.key", "**/id_rsa", "**/id_rsa.*", "**/id_ed25519", "**/id_ed25519.*", "**/.ssh/**", ".codexpro", ".codexpro/**", "**/.codexpro/**", "dist", "dist/**", "**/dist/**", "build", "build/**", "**/build/**", ".next", ".next/**", "**/.next/**", "coverage", "coverage/**", "**/coverage/**", ".cache", ".cache/**", "**/.cache/**"}, values: defaults()}
}
func defaults() map[string]any {
	return map[string]any{
		"maxReadBytes": 180000, "maxWriteBytes": 1000000, "maxImportBytes": 5000000,
		"maxOutputBytes": 120000, "maxSearchResults": 200, "maxCommandsPerCall": 6,
		"bashTimeoutMs": 120000, "maxBashTimeoutMs": 600000, "jobTimeoutMs": 2700000,
		"maxJobs": 12, "maxJobsPerWorkspace": 6, "maxActiveCaptures": 24,
		"maxJobOutputBytes": 8388608, "maxRetainedJobBytes": 536870912,
		"jobRetentionMs": 86400000, "maxJobHistoryPerWorkspace": 50,
		"jobOutputPageDefaultBytes": 16384, "jobOutputPageMaxBytes": 24576, "jobResponseMaxBytes": 196608,
		"auditMaxBytes": 8388608, "auditRetainActions": 350, "auditMode": "metadata", "auditEnabled": true,
		"bashMode": "full", "writeMode": "workspace", "toolMode": "standard", "worktreeMode": "off",
		"bashTranscript": "compact", "codexSessions": "off", "handoffMode": "off", "mode": "agent", "work": "off",
		"worktreeBaseRef": "HEAD", "maxWorktrees": 64, "contextDir": ".ai-bridge", "widgetDomain": "https://rebel0789.github.io",
		"exposeAbsolutePaths": false, "inheritEnv": false, "analysisEnabled": true,
		"maxInventoryFiles": 20000, "maxAnalyzedFiles": 5000, "maxScannedBytes": 67108864,
		"maxSymbols": 100000, "maxRelationships": 250000,
	}
}
func (c *Config) Int(k string, d int) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return wire.Int(c.values, k, d)
}
func (c *Config) Int64(k string, d int64) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return wire.Int64(c.values, k, d)
}
func (c *Config) Bool(k string, d bool) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return wire.Bool(c.values, k, d)
}
func (c *Config) String(k, d string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return wire.String(c.values, k, d)
}
func (c *Config) Snapshot() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := make(map[string]any, len(c.values))
	for k, v := range c.values {
		m[k] = v
	}
	return m
}
func (c *Config) Set(k string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = defaults()
	}
	c.values[k] = v
}

func canonicalFuture(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	var tail []string
	for {
		_, e = os.Lstat(p)
		if e == nil {
			break
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", e
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return "", e
	}
	for i := len(tail) - 1; i >= 0; i-- {
		p = filepath.Join(p, tail[i])
	}
	return p, nil
}
func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}

// The TypeScript state tree is checked canonically, including symlinked
// ancestors. HOME may be overridden for a private fixture; the account home
// remains protected.
func forbiddenStateRoots() []string {
	homes := []string{}
	if home, e := os.UserHomeDir(); e == nil {
		homes = append(homes, home)
	}
	if account, e := user.Current(); e == nil && account.HomeDir != "" {
		homes = append(homes, account.HomeDir)
	}
	roots := []string{}
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			roots = append(roots, path)
		}
	}
	for _, home := range homes {
		p := filepath.Join(home, ".codexpro")
		add(p)
		if resolved, e := canonicalFuture(p); e == nil {
			add(resolved)
		}
	}
	return roots
}
func protectedPath(path string) error {
	if path == "" {
		return nil
	}
	resolved, e := canonicalFuture(path)
	if e != nil {
		return e
	}
	for _, blocked := range forbiddenStateRoots() {
		if within(blocked, resolved) {
			return fmt.Errorf("path overlaps TypeScript CodexPro state: %s", path)
		}
	}
	return nil
}
func (c *Config) ValidateIsolation() error {
	_, port, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return e
	}
	number, e := strconv.Atoi(port)
	if e != nil || number < 1 || number > 65535 {
		return errors.New("listen port must be an integer from 1 to 65535")
	}
	for _, p := range []string{c.Home, c.TokenFile, c.ProjectsFile, c.EnvFile} {
		if e = protectedPath(p); e != nil {
			return e
		}
	}
	values := c.Snapshot()
	for _, key := range []string{"worktreeRoot", "jobsDir", "auditDirectory", "workDir", "workHistory", "profilePath", "codexDir", "astGrepPath"} {
		if p := wire.String(values, key, ""); p != "" {
			if e = protectedPath(p); e != nil {
				return e
			}
		}
	}
	if c.HostResourcesHelper != "" {
		if e = protectedPath(c.HostResourcesHelper); e != nil {
			return e
		}
	}
	return c.validateDerivedState()
}

// A safe Home alone does not make its descendants safe: an existing store
// directory, SQLite sidecar, lock or audit segment can be a symlink into live
// state. Check the paths actually opened by constructors before any store opens.
// Hardlinked state files are also rejected so a private write cannot change a
// second name for the same inode.
func stateFile(path string) error {
	if e := protectedPath(path); e != nil {
		return e
	}
	info, e := os.Stat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("private state file must be regular: %s", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return fmt.Errorf("private state file must not share a hard link: %s", path)
	}
	return nil
}
func stateDirectory(path string) error {
	if e := protectedPath(path); e != nil {
		return e
	}
	info, e := os.Stat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return fmt.Errorf("private state directory must be a directory: %s", path)
	}
	return nil
}
func sqliteState(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if e := stateFile(path + suffix); e != nil {
			return e
		}
	}
	return nil
}
func auditSegments(directory string) error {
	f, e := os.Open(directory)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	for {
		entries, readError := f.ReadDir(128)
		for _, entry := range entries {
			name := entry.Name()
			if (strings.HasPrefix(name, "segment-") || strings.HasPrefix(name, "compact-")) && strings.HasSuffix(name, ".jsonl") {
				if e = stateFile(filepath.Join(directory, name)); e != nil {
					return e
				}
			}
		}
		if readError == io.EOF {
			return nil
		}
		if readError != nil {
			return readError
		}
	}
}
func (c *Config) validateDerivedState() error {
	for _, name := range []string{"jobs", "usage", "inbox", "audit", "profiles", "runtime", "worktrees"} {
		if e := stateDirectory(filepath.Join(c.Home, name)); e != nil {
			return e
		}
	}
	for _, path := range []string{filepath.Join(c.Home, "workspaces.sqlite"), filepath.Join(c.Home, "jobs.sqlite"), filepath.Join(c.Home, "usage", "usage.sqlite"), filepath.Join(c.Home, "inbox", "inbox.sqlite"), filepath.Join(c.Home, "audit", "index.sqlite")} {
		if e := sqliteState(path); e != nil {
			return e
		}
	}
	if e := stateFile(filepath.Join(c.Home, "audit", "writer.lock")); e != nil {
		return e
	}
	if e := auditSegments(filepath.Join(c.Home, "audit")); e != nil {
		return e
	}
	values := c.Snapshot()
	managedRoot := wire.String(values, "worktreeRoot", filepath.Join(c.Home, "worktrees"))
	if managedRoot == "" {
		managedRoot = filepath.Join(c.Home, "worktrees")
	}
	if e := stateDirectory(managedRoot); e != nil {
		return e
	}
	if e := stateDirectory(filepath.Join(managedRoot, "repositories")); e != nil {
		return e
	}
	if e := stateFile(filepath.Join(managedRoot, "connector-id")); e != nil {
		return e
	}
	if directory := wire.String(values, "auditDirectory", ""); directory != "" && filepath.Clean(directory) != filepath.Join(c.Home, "audit") {
		if e := stateDirectory(directory); e != nil {
			return e
		}
		if e := sqliteState(filepath.Join(directory, "index.sqlite")); e != nil {
			return e
		}
		if e := stateFile(filepath.Join(directory, "writer.lock")); e != nil {
			return e
		}
		if e := auditSegments(directory); e != nil {
			return e
		}
	}
	if directory := wire.String(values, "jobsDir", ""); directory != "" {
		if e := stateDirectory(directory); e != nil {
			return e
		}
	}
	return nil
}
func (c *Config) Token() (string, error) {
	if e := c.ValidateIsolation(); e != nil {
		return "", e
	}
	if info, e := os.Stat(c.TokenFile); e == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("private token file must be a regular file")
		}
		if info.Mode().Perm()&0077 != 0 {
			return "", errors.New("private token file must have mode 0600")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			return "", errors.New("private token file must not share a hard link")
		}
	} else if !os.IsNotExist(e) {
		return "", e
	}
	b, e := readBounded(c.TokenFile, 16384)
	if os.IsNotExist(e) {
		raw := make([]byte, 48)
		if _, e = rand.Read(raw); e != nil {
			return "", e
		}
		b = []byte(base64.RawURLEncoding.EncodeToString(raw) + "\n")
		if e = os.MkdirAll(filepath.Dir(c.TokenFile), 0700); e != nil {
			return "", e
		}
		f, e := os.OpenFile(c.TokenFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", e
		}
		if _, e = f.Write(b); e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return "", e
		}
		if ce != nil {
			return "", ce
		}
	} else if e != nil {
		return "", e
	}
	info, e := os.Stat(c.TokenFile)
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("private token file must be a regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return "", errors.New("private token file must not share a hard link")
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("private token file must have mode 0600")
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 24 {
		return "", errors.New("private token is too short")
	}
	return t, nil
}
func (c *Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Snapshot()) }

func readBounded(path string, limit int64) ([]byte, error) {
	st, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file")
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return b, nil
}
