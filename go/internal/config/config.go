// Package config loads only the private Go instance's configuration.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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
		EnvFile: filepath.Join(home, "limits.env"), BlockedGlobs: []string{".git", ".git/**", "**/.git/**", "node_modules", "node_modules/**", "**/node_modules/**", ".env", ".env/**", ".env.*", ".env.*/**", "**/.env", "**/.env/**", "**/.env.*", "**/.env.*/**", "**/*.pem", "**/*.key", "**/id_rsa", "**/id_rsa.*", "**/id_ed25519", "**/id_ed25519.*", "**/.ssh/**", ".codexpro", ".codexpro/**", "**/.codexpro/**", "dist", "dist/**", "**/dist/**", "build", "build/**", "**/build/**", ".next", ".next/**", "**/.next/**", "coverage", "coverage/**", "**/coverage/**", ".cache", ".cache/**", "**/.cache/**", "**/secrets/**", "**/*.sqlite", "**/*.sqlite3", "**/*.db", "**/.venv/**", "**/__pycache__/**"}, values: defaults()}
}
func defaults() map[string]any {
	return map[string]any{
		"maxReadBytes": 180000, "maxWriteBytes": 1000000, "maxImportBytes": 5000000,
		"maxOutputBytes": 120000, "maxSearchResults": 200, "maxCommandsPerCall": 6,
		"bashTimeoutMs": 120000, "maxBashTimeoutMs": 600000, "jobTimeoutMs": 5400000,
		"maxJobs": 12, "maxJobsPerWorkspace": 6, "maxActiveCaptures": 24,
		"maxJobOutputBytes": 8388608, "maxRetainedJobBytes": 536870912,
		"jobRetentionMs": 86400000, "maxJobHistoryPerWorkspace": 50,
		"jobOutputPageDefaultBytes": 16384, "jobOutputPageMaxBytes": 24576, "jobResponseMaxBytes": 196608,
		"auditMaxBytes": 8388608, "auditRetainActions": 350, "auditMode": "metadata", "auditEnabled": true,
		"bashMode": "full", "writeMode": "workspace", "toolMode": "standard", "worktreeMode": "off",
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
func (c *Config) ValidateIsolation() error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	_, port, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return e
	}
	if port == "7200" {
		return errors.New("port 7200 belongs to the live TypeScript service")
	}
	for _, p := range []string{c.Home, c.TokenFile, c.ProjectsFile, c.EnvFile} {
		resolved, e := canonicalFuture(p)
		if e != nil {
			return e
		}
		for _, blocked := range []string{filepath.Join(home, ".codexpro"), filepath.Join(home, ".config", "codexpro")} {
			if within(blocked, resolved) {
				return fmt.Errorf("private writable path overlaps live state: %s", p)
			}
		}
	}
	return nil
}
func Load(args []string) (*Config, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	state := os.Getenv("CODEXPRO_GO_HOME")
	if state == "" {
		state = filepath.Join(home, ".codexpro-go")
	}
	c := New(state)
	f := flag.NewFlagSet("codexpro-go", flag.ContinueOnError)
	f.StringVar(&c.Home, "state-dir", state, "private state directory")
	f.StringVar(&c.Listen, "listen", "127.0.0.1:7210", "private HTTP listen address")
	var token, projects, limits string
	f.StringVar(&token, "token-file", "", "private bearer-token file")
	f.StringVar(&projects, "projects-file", "", "private project catalog")
	f.StringVar(&limits, "limits-file", "", "private reloadable limits file")
	f.StringVar(&c.HostResourcesHelper, "host-resources-helper", filepath.Join(home, ".local", "bin", "ssd-dev"), "existing host resource helper")
	var worktreeMode, codexDir, astPath, workHistory string
	f.StringVar(&worktreeMode, "worktree-mode", "off", "off or mcp; isolated worktrees stay in private state")
	f.StringVar(&codexDir, "codex-dir", filepath.Join(home, ".codex"), "read-only global instructions and skills")
	f.StringVar(&astPath, "ast-grep-path", "", "pinned ast-grep executable")
	f.StringVar(&workHistory, "work-history", "", "private copied archive database for read-only work_status")
	if e = f.Parse(args); e != nil {
		return nil, e
	}
	if f.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	if worktreeMode != "off" && worktreeMode != "mcp" {
		return nil, errors.New("worktree-mode must be off or mcp")
	}
	c.Set("worktreeMode", worktreeMode)
	c.Set("codexDir", codexDir)
	c.Set("astGrepPath", astPath)
	c.Set("workHistory", workHistory)
	c.Home, e = filepath.Abs(c.Home)
	if e != nil {
		return nil, e
	}
	if token == "" {
		token = filepath.Join(c.Home, "http-token")
	}
	c.TokenFile = token
	if projects == "" {
		projects = filepath.Join(c.Home, "projects.json")
	}
	c.ProjectsFile = projects
	if limits == "" {
		limits = filepath.Join(c.Home, "limits.env")
	}
	c.EnvFile = limits
	if e = c.ValidateIsolation(); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(c.Home, 0700); e != nil {
		return nil, e
	}
	if e = c.Reload(); e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	return c, nil
}
func (c *Config) Token() (string, error) {
	if e := c.ValidateIsolation(); e != nil {
		return "", e
	}
	b, e := os.ReadFile(c.TokenFile)
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
	if info.Mode().Perm()&0077 != 0 {
		return "", errors.New("private token file must have mode 0600")
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 24 {
		return "", errors.New("private token is too short")
	}
	return t, nil
}
func (c *Config) Reload() error {
	b, e := os.ReadFile(c.EnvFile)
	if e != nil {
		return e
	}
	next := c.Snapshot()
	for no, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("limits line %d lacks =", no+1)
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		if strings.HasPrefix(k, "CODEXPRO_") {
			parts := strings.Split(strings.ToLower(strings.TrimPrefix(k, "CODEXPRO_")), "_")
			k = parts[0]
			for _, p := range parts[1:] {
				if p != "" {
					k += strings.ToUpper(p[:1]) + p[1:]
				}
			}
		}
		if _, ok = next[k]; !ok {
			return fmt.Errorf("unknown reloadable limit %q", k)
		}
		n, e := strconv.Atoi(v)
		if e != nil || n <= 0 {
			return fmt.Errorf("invalid positive integer for %s", k)
		}
		next[k] = n
	}
	if wire.Int(next, "bashTimeoutMs", 30000) > wire.Int(next, "maxBashTimeoutMs", 900000) {
		return errors.New("bashTimeoutMs exceeds maxBashTimeoutMs")
	}
	c.mu.Lock()
	c.values = next
	c.mu.Unlock()
	return nil
}
func (c *Config) MarshalJSON() ([]byte, error) { return json.Marshal(c.Snapshot()) }
