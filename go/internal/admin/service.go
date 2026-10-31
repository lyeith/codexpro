// Package admin preserves the retained diagnostics and local HTTP control surface.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/files"
	gitservice "github.com/lyeith/codexpro/go/internal/git"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

type Service struct {
	Config     *config.Config
	Workspaces *workspace.Manager
	Files      *files.Service
	Git        *gitservice.Service
	Registry   *contracts.Registry
	// RegisteredNames is set once before serving; it lets diagnostics compare the
	// active dispatcher with the frozen expected registry.
	RegisteredNames func() []string
	Audit           *audit.Journal
	profileMu       sync.Mutex
}

func New(cfg *config.Config, workspaces *workspace.Manager, fileService *files.Service, git *gitservice.Service, registry *contracts.Registry, journal *audit.Journal) (*Service, error) {
	if cfg == nil || workspaces == nil || fileService == nil || git == nil || registry == nil {
		return nil, fmt.Errorf("admin requires config, workspace, files, git, and tool registry")
	}
	return &Service{Config: cfg, Workspaces: workspaces, Files: fileService, Git: git, Registry: registry, Audit: journal}, nil
}
func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	switch name {
	case "server_config":
		view := s.ConfigView()
		text, e := encode(view, true)
		if e != nil {
			return wire.Result{}, e
		}
		return wire.Success(name, "Server Config", view.Map(), "# CodexPro Server Config\n\n"+string(text)), nil
	case "codexpro_self_test":
		return s.selfTest(ctx, scope, args)
	}
	return wire.Result{}, wire.Errorf("tool_unknown", "Unknown admin tool: %s", name)
}

// ordered retains JavaScript insertion order in human-readable JSON blocks.
// Structured JSON objects are decoded separately; object order is not a wire contract.
type field struct {
	Key   string
	Value any
}
type ordered []field

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, e := encode(f.Key, false)
		if e != nil {
			return nil, e
		}
		v, e := encode(f.Value, false)
		if e != nil {
			return nil, e
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
func (o ordered) Map() map[string]any {
	b, e := encode(o, false)
	if e != nil {
		return map[string]any{}
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	_ = d.Decode(&m)
	return m
}
func encode(v any, pretty bool) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if pretty {
		e.SetIndent("", "  ")
	}
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (s *Service) defaultRoot() string {
	if p, ok := s.Workspaces.Project(s.Workspaces.DefaultProject()); ok {
		return p.Root
	}
	return ""
}
func (s *Service) hostPort() (string, int) {
	host, port, e := net.SplitHostPort(s.Config.Listen)
	if e != nil {
		return "127.0.0.1", 7210
	}
	n, e := strconv.Atoi(port)
	if e != nil {
		n = 7210
	}
	return host, n
}
func (s *Service) projectLabels() []ordered {
	out := []ordered{}
	for _, p := range s.Workspaces.Projects() {
		out = append(out, ordered{{"id", p.ID}, {"label", p.Label}})
	}
	return out
}
func (s *Service) projectSummaries(values map[string]any) []ordered {
	out := []ordered{}
	limit := wire.Int(values, "maxWorktrees", 64)
	base := wire.String(values, "worktreeBaseRef", "HEAD")
	for _, p := range s.Workspaces.Projects() {
		ref := p.BaseRef
		if ref == "" {
			ref = base
		}
		maximum := p.MaxWorktrees
		if maximum == 0 {
			maximum = limit
		}
		out = append(out, ordered{{"id", p.ID}, {"label", p.Label}, {"default", p.ID == s.Workspaces.DefaultProject()}, {"baseRef", ref}, {"maxWorktrees", min(maximum, limit)}})
	}
	return out
}
func (s *Service) allowedRoots() []string {
	out := []string{}
	for _, p := range s.Workspaces.Projects() {
		out = append(out, p.Root)
	}
	return out
}
func (s *Service) creationRoots() []ordered {
	out := []ordered{}
	for _, r := range s.Workspaces.CreationRoots() {
		out = append(out, ordered{{"id", r.ID}, {"label", r.Label}, {"root", r.Root}})
	}
	return out
}
func (s *Service) auditEnabled() bool {
	if s.Audit != nil {
		return s.Audit.Enabled()
	}
	return s.Config.Bool("auditEnabled", true)
}

// ConfigView includes inert compatibility fields for retired subsystems, without
// creating their state, loops, approval modes, or runtime implementations.
func (s *Service) ConfigView() ordered {
	c := s.Config
	v := c.Snapshot()
	host, port := s.hostPort()
	roots := s.creationRoots()
	names := s.Registry.Names()
	if s.RegisteredNames != nil {
		names = append([]string{}, s.RegisteredNames()...)
	}
	i := func(k string, d int) int { return wire.Int(v, k, d) }
	b := func(k string, d bool) bool { return wire.Bool(v, k, d) }
	st := func(k, d string) string { return wire.String(v, k, d) }
	worktreeRoot := any(nil)
	if st("worktreeMode", "off") == "mcp" {
		worktreeRoot = st("worktreeRoot", filepath.Join(c.Home, "worktrees"))
	}
	limits := ordered{{"CODEXPRO_MAX_OUTPUT_BYTES", i("maxOutputBytes", 120000)}, {"CODEXPRO_MAX_BASH_TIMEOUT_MS", i("maxBashTimeoutMs", 600000)}, {"CODEXPRO_BASH_TIMEOUT_MS", i("bashTimeoutMs", 120000)}, {"CODEXPRO_JOB_TIMEOUT_MS", i("jobTimeoutMs", 5400000)}, {"CODEXPRO_MAX_JOBS", i("maxJobs", 12)}, {"CODEXPRO_MAX_JOBS_PER_WORKSPACE", i("maxJobsPerWorkspace", 6)}, {"CODEXPRO_MAX_JOB_OUTPUT_BYTES", i("maxJobOutputBytes", 8388608)}, {"CODEXPRO_JOB_RETENTION_MS", i("jobRetentionMs", 86400000)}, {"CODEXPRO_MAX_RETAINED_JOB_BYTES", i("maxRetainedJobBytes", 536870912)}, {"CODEXPRO_MAX_JOB_HISTORY_PER_WORKSPACE", i("maxJobHistoryPerWorkspace", 50)}, {"CODEXPRO_AUDIT_MAX_BYTES", i("auditMaxBytes", 8388608)}, {"CODEXPRO_AUDIT_RETAIN_ACTIONS", i("auditRetainActions", 350)}}
	return ordered{
		{"defaultRoot", s.defaultRoot()}, {"allowedRoots", s.allowedRoots()}, {"projectsFile", nullable(c.ProjectsFile)}, {"projectCreationEnabled", c.ProjectsFile != "" && !b("connectionTest", false)}, {"projectCreationRoots", roots}, {"defaultProjectId", s.Workspaces.DefaultProject()}, {"projects", s.projectSummaries(v)},
		{"host", host}, {"port", port}, {"authMode", "static-token"}, {"authEnabled", true}, {"cloudflareAccess", nil},
		{"bashMode", "full"}, {"bashTranscript", st("bashTranscript", "compact")}, {"bashSessionId", nullable(st("bashSessionId", ""))}, {"requireBashSession", b("requireBashSession", false)},
		{"codexSessions", "off"}, {"codexDir", st("codexDir", filepath.Join(c.Home, "empty-codex"))}, {"writeMode", "workspace"}, {"handoffMode", "off"}, {"toolMode", st("toolMode", "standard")}, {"exposeAbsolutePaths", b("exposeAbsolutePaths", false)}, {"toolCards", false},
		{"auditMode", st("auditMode", "metadata")}, {"debugActivityMode", st("auditMode", "metadata")}, {"auditEnabled", s.auditEnabled()}, {"auditSchemaVersion", "1.0"}, {"auditLogConfigured", true}, {"auditMaxBytes", i("auditMaxBytes", 8388608)}, {"auditRetainActions", i("auditRetainActions", 350)},
		{"connectionTest", b("connectionTest", false)}, {"analysisEnabled", b("analysisEnabled", true)}, {"analysisLimits", ordered{{"maxInventoryFiles", i("maxInventoryFiles", 20000)}, {"maxAnalyzedFiles", i("maxAnalyzedFiles", 5000)}, {"maxScannedBytes", i("maxScannedBytes", 67108864)}, {"maxSymbols", i("maxSymbols", 100000)}, {"maxRelationships", i("maxRelationships", 250000)}}},
		{"inheritEnv", b("inheritEnv", false)}, {"contextDir", st("contextDir", ".ai-bridge")}, {"worktreeMode", st("worktreeMode", "off")}, {"worktreeRoot", worktreeRoot}, {"worktreeBaseRef", st("worktreeBaseRef", "HEAD")}, {"maxWorktrees", i("maxWorktrees", 64)},
		{"maxReadBytes", i("maxReadBytes", 180000)}, {"maxWriteBytes", i("maxWriteBytes", 1000000)}, {"maxImportBytes", i("maxImportBytes", 5000000)}, {"maxOutputBytes", i("maxOutputBytes", 120000)}, {"maxBashTimeoutMs", i("maxBashTimeoutMs", 600000)}, {"bashTimeoutMs", i("bashTimeoutMs", 120000)},
		{"maxCommandsPerCall", i("maxJobsPerWorkspace", 6)}, {"maxActiveCaptures", i("maxJobs", 12) * 2}, {"reloadableLimits", limits}, {"limitsEnvFile", c.EnvFile},
		{"jobTimeoutMs", i("jobTimeoutMs", 5400000)}, {"maxJobOutputBytes", i("maxJobOutputBytes", 8388608)}, {"jobOutputPageDefaultBytes", 16384}, {"jobOutputPageMaxBytes", 24576}, {"jobResponseMaxBytes", 196608},
		{"maxJobs", i("maxJobs", 12)}, {"maxJobsPerWorkspace", i("maxJobsPerWorkspace", 6)}, {"jobRetentionMs", i("jobRetentionMs", 86400000)},
		{"work", ordered{{"enabled", false}, {"management", false}, {"idle_ms", 600000}, {"attempt_ms", nil}, {"max_attempts", nil}, {"max_active_ms", nil}, {"max_runs", nil}, {"no_progress_policy", "advisory"}, {"packet_bytes", 16384}}},
		{"maxRetainedJobBytes", i("maxRetainedJobBytes", 536870912)}, {"maxJobHistoryPerWorkspace", i("maxJobHistoryPerWorkspace", 50)}, {"maxSearchResults", i("maxSearchResults", 200)}, {"blockedGlobs", append([]string{}, c.BlockedGlobs...)}, {"registeredTools", names}, {"registeredToolCount", len(names)},
	}
}
func cleanOneLine(value string, limit int) string {
	v := strings.Join(strings.Fields(value), " ")
	if v == "" {
		v = value
	}
	return files.JSSlice(v, 0, min(files.JSLength(v), limit))
}
