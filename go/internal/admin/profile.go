package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const maxProfileBytes = 1000000

func canonicalRoot(root string) string {
	p, e := filepath.Abs(root)
	if e != nil {
		p = root
	}
	if real, e := filepath.EvalSymlinks(p); e == nil {
		return real
	}
	return p
}
func (s *Service) ProfilePath() string {
	sum := sha256.Sum256([]byte(canonicalRoot(s.defaultRoot())))
	return filepath.Join(s.Config.Home, "profiles", hex.EncodeToString(sum[:])[:24]+".json")
}
func (s *Service) runtimePath() string {
	return filepath.Join(s.Config.Home, "runtime", filepath.Base(s.ProfilePath()))
}
func readObject(path string) (map[string]any, error) {
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, maxProfileBytes+1))
	if e != nil {
		return nil, e
	}
	if len(b) > maxProfileBytes {
		return nil, fmt.Errorf("Profile state exceeds %d bytes.", maxProfileBytes)
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(&value); e != nil {
		return nil, e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return nil, fmt.Errorf("Profile state must contain one JSON value.")
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, nil
	}
	return m, nil
}
func (s *Service) readProfile() (map[string]any, error) {
	p, e := readObject(s.ProfilePath())
	if e != nil {
		return nil, e
	}
	if p == nil {
		return map[string]any{}, nil
	}
	if root, ok := p["root"].(string); ok && root != "" && canonicalRoot(root) != canonicalRoot(s.defaultRoot()) {
		return map[string]any{}, nil
	}
	p["profilePath"] = s.ProfilePath()
	return p, nil
}
func (s *Service) runtimeConnection() (map[string]any, error) {
	p, e := readObject(s.runtimePath())
	if e != nil {
		return nil, e
	}
	if p == nil {
		return map[string]any{}, nil
	}
	if root, ok := p["root"].(string); ok && root != "" && canonicalRoot(root) != canonicalRoot(s.defaultRoot()) {
		return map[string]any{}, nil
	}
	if raw, ok := p["pid"]; ok {
		n, valid := numeric(raw)
		if valid {
			pid := int(n)
			if n != float64(pid) || pid <= 0 {
				return map[string]any{}, nil
			}
			e := syscall.Kill(pid, 0)
			if e != nil && e != syscall.EPERM {
				return map[string]any{}, nil
			}
		}
	}
	// Reading the dashboard never unlinks stale records; startup owns recovery.
	return p, nil
}
func (s *Service) effectiveProfile(p map[string]any) map[string]any {
	_, port := s.hostPort()
	c := s.Config
	str := func(k, d string) string {
		if v, ok := p[k]; ok && v != nil {
			return jsString(v)
		}
		return d
	}
	boolean := func(k string, d bool) bool {
		if v, ok := p[k]; ok && v != nil {
			return truthy(v)
		}
		return d
	}
	choose := func(k string, values []string, d string) string {
		value := str(k, d)
		for _, x := range values {
			if value == x {
				return value
			}
		}
		return d
	}
	hostname := firstEnv("CODEXPRO_PUBLIC_HOSTNAME", "CODEXPRO_HOSTNAME", "NGROK_DOMAIN")
	tunnel := firstEnv("CODEXPRO_TUNNEL")
	if !contains(tunnels, tunnel) {
		if os.Getenv("CODEXPRO_TUNNEL_MODE") == "0" {
			tunnel = "none"
		} else {
			tunnel = "cloudflare"
		}
	}
	// Compatibility fields are present but retired operating modes are inert.
	return map[string]any{
		"port": str("port", strconv.Itoa(port)), "mode": "agent", "tunnel": choose("tunnel", tunnels, tunnel), "hostname": str("hostname", hostname),
		"tunnelName": str("tunnelName", ""), "ngrokConfig": str("ngrokConfig", ""), "cloudflareConfig": str("cloudflareConfig", ""), "cloudflareTokenFile": str("cloudflareTokenFile", ""),
		"bash": "full", "bashTranscript": choose("bashTranscript", []string{"compact", "full"}, c.String("bashTranscript", "compact")), "codexSessions": "off", "codexDir": str("codexDir", c.String("codexDir", filepath.Join(c.Home, "empty-codex"))),
		"bashSession": str("bashSession", c.String("bashSessionId", "")), "requireBashSession": boolean("requireBashSession", c.Bool("requireBashSession", false)),
		"write": "workspace", "handoffMode": "off", "toolMode": choose("toolMode", []string{"minimal", "standard", "full"}, c.String("toolMode", "standard")),
		"worktreeMode": choose("worktreeMode", []string{"off", "mcp"}, c.String("worktreeMode", "off")), "work": "off", "workDir": str("workDir", filepath.Join(c.Home, "work")),
		"worktreeBase": str("worktreeBase", c.String("worktreeBaseRef", "HEAD")), "worktreeRoot": str("worktreeRoot", c.String("worktreeRoot", filepath.Join(c.Home, "worktrees"))),
		"maxWorktrees": str("maxWorktrees", strconv.Itoa(c.Int("maxWorktrees", 64))), "projectsFile": str("projectsFile", c.ProjectsFile),
		"toolCards": false, "widgetDomain": str("widgetDomain", c.String("widgetDomain", "https://rebel0789.github.io")), "noInstallCloudflared": boolean("noInstallCloudflared", false),
	}
}
func (s *Service) ProfileResponse() (map[string]any, error) {
	p, e := s.readProfile()
	if e != nil {
		return nil, e
	}
	runtime, e := s.runtimeConnection()
	if e != nil {
		return nil, e
	}
	public := map[string]any{}
	for k, v := range p {
		switch k {
		case "token", "cloudflareToken":
			if truthy(v) {
				public[k] = "<saved>"
			}
		default:
			public[k] = v
		}
	}
	_, port := s.hostPort()
	c := s.Config
	result := map[string]any{"ok": true, "profile_path": s.ProfilePath(), "exists": wire.String(p, "profilePath", "") != "", "profile": public, "effective": s.effectiveProfile(p), "runtime_connection": runtime,
		"runtime": map[string]any{"defaultRoot": s.defaultRoot(), "projectsFile": nullable(c.ProjectsFile), "defaultProjectId": s.Workspaces.DefaultProject(), "projects": s.projectLabels(), "port": port, "bashMode": "full", "bashTranscript": c.String("bashTranscript", "compact"), "codexSessions": "off", "writeMode": "workspace", "handoffMode": "off", "toolMode": c.String("toolMode", "standard"), "worktreeMode": c.String("worktreeMode", "off"), "worktreeBaseRef": c.String("worktreeBaseRef", "HEAD"), "worktreeRoot": c.String("worktreeRoot", filepath.Join(c.Home, "worktrees")), "maxWorktrees": c.Int("maxWorktrees", 64), "toolCards": false, "widgetDomain": c.String("widgetDomain", "https://rebel0789.github.io"), "authMode": "static-token", "authMethods": []string{"static-token"}, "authEnabled": true}}
	return result, nil
}

var tunnels = []string{"cloudflare", "ngrok", "cloudflare-named", "tailscale", "none"}
var enumFields = map[string][]string{
	"tunnel": tunnels, "mode": {"agent", "handoff", "pro"}, "bash": {"safe", "off", "full"}, "bashTranscript": {"compact", "full"}, "codexSessions": {"off", "metadata", "read"}, "write": {"workspace", "handoff", "off"}, "handoffMode": {"off", "on"}, "toolMode": {"standard", "minimal", "full"}, "worktreeMode": {"off", "mcp"}, "work": {"on", "off"},
}
var textLengths = map[string]int{"hostname": 253, "codexDir": 4096, "bashSession": 64, "workDir": 4096, "worktreeBase": 256, "worktreeRoot": 4096, "projectsFile": 4096, "widgetDomain": 2048, "tunnelName": 128, "ngrokConfig": 4096, "cloudflareConfig": 4096, "cloudflareTokenFile": 4096}
var booleanFields = map[string]bool{"requireBashSession": true, "toolCards": true, "noInstallCloudflared": true}
var profileFieldOrder = []string{"tunnel", "hostname", "port", "mode", "bash", "bashTranscript", "codexSessions", "codexDir", "bashSession", "requireBashSession", "write", "handoffMode", "toolMode", "worktreeMode", "work", "workDir", "worktreeBase", "worktreeRoot", "maxWorktrees", "projectsFile", "toolCards", "widgetDomain", "tunnelName", "ngrokConfig", "cloudflareConfig", "cloudflareTokenFile", "noInstallCloudflared"}
var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var refControls = regexp.MustCompile(`[\x00-\x20\x7f]`)

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
	}
	return ""
}
func jsString(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.Number:
		return string(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case []any:
		parts := []string{}
		for _, x := range v {
			if x == nil {
				parts = append(parts, "")
			} else {
				parts = append(parts, jsString(x))
			}
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case int:
		return v != 0
	case float64:
		return v != 0 && !math.IsNaN(v)
	case json.Number:
		n, _ := v.Float64()
		return n != 0
	}
	return true
}
func numeric(v any) (float64, bool) {
	switch v := v.(type) {
	case json.Number:
		n, e := v.Float64()
		return n, e == nil
	case float64:
		return v, !math.IsNaN(v)
	case int:
		return float64(v), true
	}
	return 0, false
}
func coerceNumber(v any) (float64, bool) {
	if n, ok := numeric(v); ok {
		return n, true
	}
	switch x := v.(type) {
	case nil:
		return 0, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		t := files.TrimJS(x)
		if t == "" {
			return 0, true
		}
		if len(t) > 2 && t[0] == '0' {
			base := 0
			switch t[1] {
			case 'x', 'X':
				base = 16
			case 'b', 'B':
				base = 2
			case 'o', 'O':
				base = 8
			}
			if base != 0 {
				n, e := strconv.ParseUint(t[2:], base, 64)
				return float64(n), e == nil
			}
		}
		n, e := strconv.ParseFloat(t, 64)
		return n, e == nil && !math.IsNaN(n)
	case []any:
		return coerceNumber(jsString(x))
	default:
		return 0, false
	}
}
func typeName(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, int:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return reflect.TypeOf(v).String()
}

type profileIssue struct {
	FormErrors  []string            `json:"formErrors"`
	FieldErrors map[string][]string `json:"fieldErrors"`
}

func validateProfile(value any, keyOrder []string) (map[string]any, *profileIssue) {
	input, ok := value.(map[string]any)
	issue := &profileIssue{FormErrors: []string{}, FieldErrors: map[string][]string{}}
	if !ok {
		issue.FormErrors = append(issue.FormErrors, "Invalid input: expected object, received "+typeName(value))
		return nil, issue
	}
	out := map[string]any{}
	known := map[string]bool{}
	add := func(key, msg string) { issue.FieldErrors[key] = append(issue.FieldErrors[key], msg) }
	for _, key := range profileFieldOrder {
		known[key] = true
		v, present := input[key]
		if !present {
			continue
		}
		if values, ok := enumFields[key]; ok {
			str, isString := v.(string)
			if !isString || !contains(values, str) {
				quoted := []string{}
				for _, x := range values {
					quoted = append(quoted, strconv.Quote(x))
				}
				add(key, "Invalid option: expected one of "+strings.Join(quoted, "|"))
			} else {
				out[key] = str
			}
			continue
		}
		if max, ok := textLengths[key]; ok {
			str, isString := v.(string)
			if !isString {
				add(key, "Invalid input: expected string, received "+typeName(v))
				continue
			}
			str = files.TrimJS(str)
			out[key] = str
			if files.JSLength(str) > max {
				add(key, fmt.Sprintf("Too big: expected string to have <=%d characters", max))
			}
			if key == "bashSession" && str != "" && !sessionPattern.MatchString(str) {
				add(key, "bashSession must be 1-64 characters using letters, numbers, dot, underscore, or dash, and must start with a letter or number.")
			}
			if key == "worktreeBase" && str != "" && (strings.HasPrefix(str, "-") || refControls.MatchString(str)) {
				add(key, "worktreeBase must be a Git ref without whitespace, control characters, or a leading dash.")
			}
			continue
		}
		if booleanFields[key] {
			if b, ok := v.(bool); ok {
				out[key] = b
			} else {
				add(key, "Invalid input: expected boolean, received "+typeName(v))
			}
			continue
		}
		n, ok := coerceNumber(v)
		if !ok || math.IsInf(n, 0) {
			received := "NaN"
			if math.IsInf(n, 0) {
				received = "number"
			}
			add(key, "Invalid input: expected number, received "+received)
			continue
		}
		if math.Trunc(n) != n {
			add(key, "Invalid input: expected int, received number")
			continue
		}
		maximum := 65535
		if key == "maxWorktrees" {
			maximum = 512
		}
		if n < 1 {
			add(key, "Too small: expected number to be >=1")
		}
		if n > float64(maximum) {
			add(key, fmt.Sprintf("Too big: expected number to be <=%d", maximum))
		}
		out[key] = int(n)
	}
	unknown := []string{}
	seen := map[string]bool{}
	for _, k := range keyOrder {
		if !known[k] && !seen[k] {
			unknown = append(unknown, strconv.Quote(k))
			seen[k] = true
		}
	}
	for k := range input {
		if !known[k] && !seen[k] {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	if len(unknown) > 0 {
		prefix := "Unrecognized key: "
		if len(unknown) > 1 {
			prefix = "Unrecognized keys: "
		}
		issue.FormErrors = append(issue.FormErrors, prefix+strings.Join(unknown, ", "))
	}
	if len(issue.FormErrors) > 0 || len(issue.FieldErrors) > 0 {
		return nil, issue
	}
	return out, nil
}
func normalizeHostname(raw string) (string, error) {
	raw = strings.TrimRight(files.TrimJS(raw), "/")
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return "", fmt.Errorf("Invalid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("hostname must use https when a scheme is provided.")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("hostname must not include query strings or fragments.")
	}
	if u.Path != "" && u.Path != "/" && u.Path != "/mcp" {
		return "", fmt.Errorf("hostname must be a host, URL root, or /mcp URL.")
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	if strings.ContainsAny(u.Hostname(), " \t\r\n\\") {
		return "", fmt.Errorf("Invalid URL")
	}
	return host, nil
}
func normalizeWidget(raw string) (string, error) {
	raw = files.TrimJS(raw)
	if raw == "" {
		return "", nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" {
		return "", fmt.Errorf("Invalid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("widgetDomain must use https.")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("widgetDomain must be an origin only, for example https://widgets.example.com.")
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	return "https://" + host, nil
}
func normalizePath(root, raw string) string {
	raw = files.TrimJS(raw)
	if raw == "" {
		return ""
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		h, _ := os.UserHomeDir()
		if raw == "~" {
			raw = h
		} else {
			raw = filepath.Join(h, raw[2:])
		}
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(root, raw)
	}
	return filepath.Clean(raw)
}
func (s *Service) buildProfile(existing, input map[string]any) (map[string]any, error) {
	next := s.effectiveProfile(existing)
	for k, v := range input {
		next[k] = v
	}
	next["port"] = jsString(next["port"])
	next["maxWorktrees"] = jsString(next["maxWorktrees"])
	for key, want := range map[string]string{"mode": "agent", "bash": "full", "write": "workspace", "handoffMode": "off", "codexSessions": "off", "work": "off"} {
		if wire.String(next, key, "") != want {
			return nil, fmt.Errorf("%s must be %s in the Go build; retired operating modes are not available.", key, want)
		}
	}
	hostname, e := normalizeHostname(wire.String(next, "hostname", ""))
	if e != nil {
		return nil, e
	}
	tunnel := wire.String(next, "tunnel", "none")
	stable := tunnel == "ngrok" || tunnel == "cloudflare-named" || tunnel == "tailscale"
	if !stable {
		hostname = ""
	}
	if stable && hostname == "" {
		return nil, fmt.Errorf("hostname is required for ngrok, cloudflare-named, and tailscale profiles.")
	}
	widget, e := normalizeWidget(wire.String(next, "widgetDomain", ""))
	if e != nil {
		return nil, e
	}
	session := wire.String(next, "bashSession", "")
	if wire.Bool(next, "requireBashSession", false) && session == "" {
		return nil, fmt.Errorf("requireBashSession requires a bashSession value.")
	}
	// Full-permission managed worktrees are allowed by the approved Go design.
	root := s.defaultRoot()
	projects := normalizePath(root, wire.String(next, "projectsFile", ""))
	if projects != "" {
		st, e := os.Stat(projects)
		if e != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("projectsFile does not exist or is not a file: %s", projects)
		}
	}
	out := map[string]any{"port": next["port"], "mode": "agent", "tunnel": tunnel, "bash": "full", "write": "workspace", "handoffMode": "off", "toolMode": next["toolMode"], "worktreeMode": next["worktreeMode"], "work": "off", "worktreeBase": next["worktreeBase"], "maxWorktrees": next["maxWorktrees"], "toolCards": false}
	if out["worktreeBase"] == "" {
		out["worktreeBase"] = "HEAD"
	}
	set := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	set("hostname", hostname)
	if tunnel == "cloudflare-named" {
		set("tunnelName", wire.String(next, "tunnelName", ""))
		set("cloudflareConfig", normalizePath(root, wire.String(next, "cloudflareConfig", "")))
		set("cloudflareTokenFile", normalizePath(root, wire.String(next, "cloudflareTokenFile", "")))
		if token, ok := existing["cloudflareToken"].(string); ok && token != "" {
			out["cloudflareToken"] = token
		}
	}
	if tunnel == "ngrok" {
		set("ngrokConfig", normalizePath(root, wire.String(next, "ngrokConfig", "")))
	}
	if token, ok := existing["token"].(string); ok && token != "" {
		out["token"] = token
	} else if f, e := os.Open(s.Config.TokenFile); e == nil {
		b, e := io.ReadAll(io.LimitReader(f, 16385))
		_ = f.Close()
		if e == nil && len(b) <= 16384 {
			set("token", strings.TrimSpace(string(b)))
		}
	}
	if wire.String(next, "bashTranscript", "compact") != "compact" {
		out["bashTranscript"] = next["bashTranscript"]
	}
	set("codexDir", wire.String(next, "codexDir", ""))
	set("bashSession", session)
	if wire.Bool(next, "requireBashSession", false) {
		out["requireBashSession"] = true
	}
	set("workDir", wire.String(next, "workDir", ""))
	set("worktreeRoot", normalizePath(root, wire.String(next, "worktreeRoot", "")))
	set("projectsFile", projects)
	set("widgetDomain", widget)
	if a, ok := existing["allowedRoots"].([]any); ok && len(a) > 0 {
		out["allowedRoots"] = a
	}
	if wire.Bool(next, "noInstallCloudflared", false) {
		out["noInstallCloudflared"] = true
	}
	return out, nil
}
func (s *Service) saveProfile(profile map[string]any) error {
	directory := filepath.Dir(s.ProfilePath())
	if e := os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	home, e := filepath.EvalSymlinks(s.Config.Home)
	if e != nil {
		return e
	}
	dir, e := filepath.EvalSymlinks(directory)
	if e != nil {
		return e
	}
	if !files.Within(dir, home) {
		return fmt.Errorf("Profile directory escapes the private state directory.")
	}
	payload := map[string]any{"version": 1, "updatedAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
	for k, v := range profile {
		if k != "profilePath" {
			payload[k] = v
		}
	}
	payload["root"] = canonicalRoot(s.defaultRoot())
	data, e := encode(payload, true)
	if e != nil {
		return e
	}
	data = append(data, '\n')
	f, e := os.CreateTemp(directory, ".profile-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeError := f.Close()
	if e != nil {
		return e
	}
	if closeError != nil {
		return closeError
	}
	if e = os.Rename(name, s.ProfilePath()); e != nil {
		return e
	}
	d, e := os.Open(directory)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func ipKey(remote string) string {
	host, _, e := net.SplitHostPort(remote)
	if e == nil {
		return host
	}
	if remote != "" {
		return remote
	}
	return "local"
}
