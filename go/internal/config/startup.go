package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type startupOption struct {
	Flag, Env, Profile, Field, Kind string
	Fallback                        any
	Min, Max                        int
	Choices                         []string
}

var startupOptions = []startupOption{
	{"worktree-mode", "CODEXPRO_WORKTREE_MODE", "worktreeMode", "worktreeMode", "enum", "off", 0, 0, []string{"off", "mcp"}},
	{"tool-mode", "CODEXPRO_TOOL_MODE", "toolMode", "toolMode", "enum", "standard", 0, 0, []string{"minimal", "standard", "full"}},
	{"bash-transcript", "CODEXPRO_BASH_TRANSCRIPT", "bashTranscript", "bashTranscript", "enum", "compact", 0, 0, []string{"compact", "full"}},
	{"codex-dir", "CODEXPRO_CODEX_DIR", "codexDir", "codexDir", "path", "", 0, 0, nil},
	{"bash-session", "CODEXPRO_BASH_SESSION_ID", "bashSession", "bashSessionId", "session", "", 0, 0, nil},
	{"require-bash-session", "CODEXPRO_REQUIRE_BASH_SESSION", "requireBashSession", "requireBashSession", "bool", false, 0, 0, nil},
	{"context-dir", "CODEXPRO_CONTEXT_DIR", "", "contextDir", "context", ".ai-bridge", 0, 0, nil},
	{"worktree-root", "CODEXPRO_WORKTREE_ROOT", "worktreeRoot", "worktreeRoot", "path", "", 0, 0, nil},
	{"worktree-base", "CODEXPRO_WORKTREE_BASE", "worktreeBase", "worktreeBaseRef", "ref", "HEAD", 0, 0, nil},
	{"max-worktrees", "CODEXPRO_MAX_WORKTREES", "maxWorktrees", "maxWorktrees", "number", 64, 1, 512, nil},
	{"analysis", "CODEXPRO_ANALYSIS", "", "analysisEnabled", "bool", true, 0, 0, nil},
	{"audit", "CODEXPRO_AUDIT_MODE", "", "auditMode", "enum", "metadata", 0, 0, []string{"metadata", "off"}},
	{"widget-domain", "CODEXPRO_WIDGET_DOMAIN", "widgetDomain", "widgetDomain", "origin", "https://rebel0789.github.io", 0, 0, nil},
	{"expose-absolute-paths", "CODEXPRO_EXPOSE_ABSOLUTE_PATHS", "", "exposeAbsolutePaths", "bool", false, 0, 0, nil},
	{"inherit-env", "CODEXPRO_INHERIT_ENV", "", "inheritEnv", "bool", false, 0, 0, nil},
	{"connection-test", "CODEXPRO_CONNECTION_TEST", "", "connectionTest", "bool", false, 0, 0, nil},
	{"bash", "CODEXPRO_BASH_MODE", "bash", "bashMode", "fixed", "full", 0, 0, nil},
	{"write", "CODEXPRO_WRITE_MODE", "write", "writeMode", "fixed", "workspace", 0, 0, nil},
	{"mode", "CODEXPRO_MODE", "mode", "mode", "fixed", "agent", 0, 0, nil},
	{"handoff-mode", "CODEXPRO_HANDOFF_MODE", "handoffMode", "handoffMode", "fixed", "off", 0, 0, nil},
	{"codex-sessions", "CODEXPRO_CODEX_SESSIONS", "codexSessions", "codexSessions", "fixed", "off", 0, 0, nil},
	{"work", "CODEXPRO_WORK_MODE", "work", "work", "fixed", "off", 0, 0, nil},
	{"work-dir", "CODEXPRO_WORK_DIR", "workDir", "workDir", "path", "", 0, 0, nil},
	{"ast-grep-path", "CODEXPRO_AST_GREP_PATH", "", "astGrepPath", "optionalPath", "", 0, 0, nil},
	{"work-history", "CODEXPRO_GO_WORK_HISTORY", "", "workHistory", "optionalPath", "", 0, 0, nil},
}

type optionFlag struct {
	Value            string
	Present, Boolean bool
}

func (v *optionFlag) String() string     { return v.Value }
func (v *optionFlag) Set(s string) error { v.Value = s; v.Present = true; return nil }
func (v *optionFlag) IsBoolFlag() bool   { return v.Boolean }
func expandedPath(path, base string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}
func envLookup(file environmentValues, key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	value, ok := file.Values[key]
	return value, ok
}
func selectedValue(flags map[string]*optionFlag, file environmentValues, flagName string, envKeys []string, profile map[string]any, profileKey string, fallback any) (string, bool, bool) {
	if v := flags[flagName]; v != nil && v.Present {
		return v.Value, true, false
	}
	for _, key := range envKeys {
		if value, ok := envLookup(file, key); ok {
			return value, true, false
		}
	}
	if profileKey != "" {
		if value, ok := profile[profileKey]; ok && value != nil {
			return settingString(value), true, true
		}
	}
	return settingString(fallback), false, false
}
func settingString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Load uses CLI > process environment > private EnvironmentFile > private
// saved profile > defaults. Saved credentials never replace the own token file.
func Load(args []string) (*Config, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	cwd, e := os.Getwd()
	if e != nil {
		return nil, e
	}
	flags := map[string]*optionFlag{}
	f := flag.NewFlagSet("codexpro-go", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	add := func(name string, isBool bool) {
		v := &optionFlag{Boolean: isBool}
		flags[name] = v
		f.Var(v, name, "private instance "+name)
	}
	for _, name := range []string{"state-dir", "listen", "host", "port", "token-file", "projects-file", "limits-file", "host-resources-helper"} {
		add(name, false)
	}
	for _, spec := range startupOptions {
		add(spec.Flag, spec.Kind == "bool")
	}
	// CLI compatibility spellings share the same value and preserve last argument.
	f.Var(flags["bash"], "bash-mode", "full permission mode")
	f.Var(flags["write"], "write-mode", "workspace write mode")
	if e = f.Parse(args); e != nil {
		return nil, e
	}
	if f.NArg() != 0 {
		return nil, errors.New("unexpected positional arguments")
	}
	state := filepath.Join(home, ".codexpro-go")
	if v, ok := os.LookupEnv("CODEXPRO_GO_HOME"); ok && v != "" {
		state = v
	}
	if flags["state-dir"].Present {
		state = flags["state-dir"].Value
	}
	if trimJS(state) == "" {
		return nil, errors.New("state-dir must not be empty")
	}
	state = expandedPath(state, cwd)
	if e = protectedPath(state); e != nil {
		return nil, e
	}
	c := New(state)
	empty := environmentValues{Values: map[string]string{}}
	limits, _, _ := selectedValue(flags, empty, "limits-file", []string{"CODEXPRO_GO_LIMITS_FILE", "CODEXPRO_ENV_FILE"}, nil, "", filepath.Join(state, "limits.env"))
	c.EnvFile = expandedPath(limits, cwd)
	if e = protectedPath(c.EnvFile); e != nil {
		return nil, e
	}
	file, e := readEnvironment(c.EnvFile)
	if os.IsNotExist(e) {
		file = environmentValues{Values: map[string]string{}}
	} else if e != nil {
		return nil, e
	}
	projects, _, _ := selectedValue(flags, file, "projects-file", []string{"CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_PROJECTS_FILE"}, nil, "", filepath.Join(state, "projects.json"))
	c.ProjectsFile = expandedPath(projects, cwd)
	if e = protectedPath(c.ProjectsFile); e != nil {
		return nil, e
	}
	profile, profilePath, profileRoot, e := readStartupProfile(state, c.ProjectsFile)
	if e != nil {
		return nil, e
	}
	projects, _, fromProfile := selectedValue(flags, file, "projects-file", []string{"CODEXPRO_GO_PROJECTS_FILE", "CODEXPRO_PROJECTS_FILE"}, profile, "projectsFile", c.ProjectsFile)
	base := cwd
	if fromProfile {
		base = profileRoot
	}
	c.ProjectsFile = expandedPath(projects, base)
	token, _, _ := selectedValue(flags, file, "token-file", []string{"CODEXPRO_GO_TOKEN_FILE"}, nil, "", filepath.Join(state, "http-token"))
	c.TokenFile = expandedPath(token, cwd)
	helper, _, _ := selectedValue(flags, file, "host-resources-helper", []string{"CODEXPRO_HOST_RESOURCES_HELPER"}, nil, "", filepath.Join(home, ".local", "bin", "ssd-dev"))
	if helper != "" {
		c.HostResourcesHelper = expandedPath(helper, cwd)
	}
	host, _, _ := selectedValue(flags, file, "host", []string{"CODEXPRO_HOST"}, nil, "", "127.0.0.1")
	portText, portPresent, _ := selectedValue(flags, file, "port", []string{"CODEXPRO_PORT"}, profile, "port", 7210)
	port := startupNumber(portText, portPresent, 7210, 1, 65535)
	c.Listen = net.JoinHostPort(host, strconv.Itoa(port))
	if flags["listen"].Present {
		if flags["host"].Present || flags["port"].Present {
			return nil, errors.New("use listen or host/port, not both")
		}
		c.Listen = flags["listen"].Value
	}
	for _, spec := range startupOptions {
		fallback := spec.Fallback
		switch spec.Field {
		case "codexDir":
			fallback = filepath.Join(home, ".codex")
		case "worktreeRoot":
			fallback = filepath.Join(state, "worktrees")
		case "workDir":
			fallback = filepath.Join(state, "work")
		}
		envKeys := []string{spec.Env}
		if spec.Field == "codexDir" {
			envKeys = append(envKeys, "CODEX_HOME")
		}
		if spec.Field == "astGrepPath" {
			envKeys = append(envKeys, "AST_GREP_PATH")
		}
		value, present, fromProfile := selectedValue(flags, file, spec.Flag, envKeys, profile, spec.Profile, fallback)
		pathBase := cwd
		if fromProfile {
			pathBase = profileRoot
		}
		var parsed any
		switch spec.Kind {
		case "fixed":
			if value != settingString(fallback) {
				return nil, fmt.Errorf("%s must be %s in the Go build; retired operating modes are not available", spec.Flag, settingString(fallback))
			}
			parsed = value
		case "enum":
			found := false
			for _, option := range spec.Choices {
				found = found || value == option
			}
			if !found {
				return nil, fmt.Errorf("%s must be one of %s", spec.Flag, strings.Join(spec.Choices, ", "))
			}
			parsed = value
		case "bool":
			if spec.Field == "inheritEnv" && !flags[spec.Flag].Present {
				parsed = present && value == "1"
			} else {
				parsed = booleanValue(value, present, fallback.(bool))
			}
		case "number":
			parsed = startupNumber(value, present, fallback.(int), spec.Min, spec.Max)
		case "path", "optionalPath":
			if spec.Kind == "optionalPath" && value == "" {
				parsed = ""
			} else {
				parsed = expandedPath(value, pathBase)
			}
		case "session":
			value = trimJS(value)
			if value != "" && !bashSessionPattern.MatchString(value) {
				return nil, errors.New("bash-session must be 1-64 letters, numbers, dot, underscore or dash and start with a letter or number")
			}
			parsed = value
		case "ref":
			value = trimJS(value)
			if value == "" {
				value = "HEAD"
			}
			if strings.HasPrefix(value, "-") || refControlPattern.MatchString(value) {
				return nil, errors.New("worktree-base must be a Git ref without whitespace, control characters, or a leading dash")
			}
			parsed = value
		case "context":
			var err error
			parsed, err = normalizeContext(value)
			if err != nil {
				return nil, err
			}
		case "origin":
			var err error
			parsed, err = normalizeOrigin(value)
			if err != nil {
				return nil, err
			}
		}
		c.Set(spec.Field, parsed)
	}
	if c.Bool("requireBashSession", false) && c.String("bashSessionId", "") == "" {
		return nil, errors.New("require-bash-session requires a bash-session value")
	}
	if value, present := envLookup(file, "CODEXPRO_AUTH_MODE"); present && value != "static-token" {
		return nil, errors.New("Go HTTP auth requires static-token mode")
	}
	if value, present := envLookup(file, "CODEXPRO_ALLOW_NO_HTTP_TOKEN"); booleanValue(value, present, false) {
		return nil, errors.New("Go HTTP auth requires its own bearer-token file")
	}
	for _, spec := range operationalLimits {
		value, present := envLookup(file, spec.Key)
		c.Set(spec.Field, startupNumber(value, present, spec.Fallback, spec.Min, spec.Max))
	}
	extras := []operationalSpec{
		{"CODEXPRO_MAX_READ_BYTES", "maxReadBytes", 180000, 4000, 2000000}, {"CODEXPRO_MAX_WRITE_BYTES", "maxWriteBytes", 1000000, 1000, 10000000}, {"CODEXPRO_MAX_IMPORT_BYTES", "maxImportBytes", 5000000, 1000, 50000000}, {"CODEXPRO_MAX_SEARCH_RESULTS", "maxSearchResults", 200, 5, 2000},
		{"CODEXPRO_MAX_HTTP_SESSIONS", "maxHttpSessions", 64, 1, 512}, {"CODEXPRO_HTTP_SESSION_TTL_MS", "httpSessionTtlMs", 1800000, 60000, 86400000},
		{"CODEXPRO_ANALYSIS_MAX_INVENTORY_FILES", "maxInventoryFiles", 20000, 100, 100000}, {"CODEXPRO_ANALYSIS_MAX_ANALYZED_FILES", "maxAnalyzedFiles", 5000, 10, 50000}, {"CODEXPRO_ANALYSIS_MAX_SCANNED_BYTES", "maxScannedBytes", 67108864, 1000000, 536870912}, {"CODEXPRO_ANALYSIS_MAX_SYMBOLS", "maxSymbols", 100000, 100, 1000000}, {"CODEXPRO_ANALYSIS_MAX_RELATIONSHIPS", "maxRelationships", 250000, 100, 2000000},
	}
	for _, spec := range extras {
		value, present := envLookup(file, spec.Key)
		c.Set(spec.Field, startupNumber(value, present, spec.Fallback, spec.Min, spec.Max))
	}
	drain, drainPresent := envLookup(file, "CODEXPRO_DRAIN_TIMEOUT_MS")
	c.Set("drainTimeoutMs", drainTimeoutMs(drain, drainPresent))
	c.Set("auditEnabled", c.String("auditMode", "metadata") == "metadata")
	if profilePath != "" {
		c.Set("profilePath", profilePath)
	}
	if value, present := envLookup(file, "CODEXPRO_BLOCKED_GLOBS"); present {
		for _, part := range strings.Split(value, ",") {
			if part = trimJS(part); part != "" {
				c.BlockedGlobs = append(c.BlockedGlobs, part)
			}
		}
	}
	// Nothing creates directories until every effective setting has passed isolation.
	if e = c.ValidateIsolation(); e != nil {
		return nil, e
	}
	if e = os.MkdirAll(c.Home, 0700); e != nil {
		return nil, e
	}
	return c, nil
}

var bashSessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var refControlPattern = regexp.MustCompile(`[\x00-\x20\x7f]`)

func normalizeContext(raw string) (string, error) {
	raw = strings.ReplaceAll(trimJS(raw), "\\", "/")
	if raw == "" {
		raw = ".ai-bridge"
	}
	if filepath.IsAbs(raw) || len(raw) > 1 && raw[1] == ':' {
		return "", errors.New("context-dir must be a workspace-relative hidden directory")
	}
	normalized := filepath.ToSlash(filepath.Clean(raw))
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", errors.New("context-dir must stay inside the workspace")
	}
	parts := strings.Split(normalized, "/")
	if !strings.HasPrefix(parts[0], ".") {
		return "", errors.New("context-dir must start with a hidden directory")
	}
	for _, part := range parts {
		for _, blocked := range []string{".git", ".ssh", ".gnupg", ".cache", "node_modules", "src", "dist", "build", ".next", "coverage"} {
			if part == blocked {
				return "", errors.New("context-dir uses a protected directory")
			}
		}
	}
	return normalized, nil
}
func normalizeOrigin(raw string) (string, error) {
	raw = trimJS(raw)
	if raw == "" {
		raw = "https://rebel0789.github.io"
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return "", errors.New("widget-domain must be a valid origin URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("widget-domain must use https")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("widget-domain must be an origin only")
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host, nil
}
