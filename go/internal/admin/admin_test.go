package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/files"
	gitservice "github.com/lyeith/codexpro/go/internal/git"
	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/lyeith/codexpro/go/internal/workspace"
)

func fixture(t *testing.T) (*Service, wire.Scope, wire.Workspace) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	state := filepath.Join(base, "state")
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(state, 0700); e != nil {
		t.Fatal(e)
	}
	cfg := config.New(state)
	cfg.Set("skillHomeDir", filepath.Join(base, "empty-user"))
	cfg.Set("codexDir", filepath.Join(base, "empty-codex"))
	catalog := map[string]any{"version": 1, "defaultProject": "fixture", "projects": []any{map[string]any{"id": "fixture", "label": "Fixture", "root": root}}}
	data, _ := json.Marshal(catalog)
	if e := os.WriteFile(cfg.ProjectsFile, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(cfg.TokenFile, []byte(strings.Repeat("x", 48)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	manager, e := workspace.New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = manager.Close() })
	fs, e := files.New(cfg, manager)
	if e != nil {
		t.Fatal(e)
	}
	git, e := gitservice.New(cfg, manager, fs, nil)
	if e != nil {
		t.Fatal(e)
	}
	registry, e := contracts.New()
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(cfg, manager, fs, git, registry, nil)
	if e != nil {
		t.Fatal(e)
	}
	scope := wire.Scope{PrincipalID: "fixture", SessionID: "a"}
	w, e := manager.Resolve(context.Background(), scope, "")
	if e != nil {
		t.Fatal(e)
	}
	return s, scope, w
}
func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	argv := append([]string{"-n", "10", "git"}, args...)
	r := files.RunCommand(context.Background(), root, "nice", argv, "", 64000, 5*time.Second)
	if r.Err != nil || r.ExitCode != 0 {
		t.Fatalf("git failed: %v exit=%d %s", r.Err, r.ExitCode, r.Stderr)
	}
}
func decode(t *testing.T, r *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if e := json.Unmarshal(r.Body.Bytes(), &value); e != nil {
		t.Fatalf("invalid JSON response status=%d: %v", r.Code, e)
	}
	return value
}
func request(mux http.Handler, method, path, body, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:7210"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.RemoteAddr = "127.0.0.1:30000"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func errorCode(t *testing.T, r *httptest.ResponseRecorder) string {
	t.Helper()
	v := decode(t, r)
	e, _ := v["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}
func TestServerConfigShapeOrderAndIsolation(t *testing.T) {
	s, scope, _ := fixture(t)
	s.Config.Set("maxJobs", 7)
	s.Config.Set("maxJobsPerWorkspace", 3)
	s.Config.Set("bashMode", "safe")
	s.Config.Set("writeMode", "off")
	result, e := s.Call(context.Background(), scope, "server_config", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	v := result.StructuredContent
	if v["bashMode"] != "full" || v["writeMode"] != "workspace" || v["auditSchemaVersion"] != "1.0" {
		t.Fatalf("fixed compatibility fields: %#v", v)
	}
	if v["projectCreationEnabled"] != true {
		t.Fatal("project creation availability must not depend on creation-root count")
	}
	s.Config.Set("connectionTest", true)
	if s.ConfigView().Map()["projectCreationEnabled"] != false {
		t.Fatal("connection test must disable project creation")
	}
	s.Config.Set("connectionTest", false)
	if v["worktreeRoot"] != nil || v["cloudflareAccess"] != nil || v["bashSessionId"] != nil {
		t.Fatal("explicit null fields must survive")
	}
	b, _ := json.Marshal(v)
	if bytes.Contains(b, []byte(strings.Repeat("x", 48))) {
		t.Fatal("auth value escaped")
	}
	if v["maxActiveCaptures"].(json.Number) != "14" || v["maxCommandsPerCall"].(json.Number) != "3" {
		t.Fatal("derived live operational limits drifted")
	}
	text := result.Content[0]["text"].(string)
	keys := []string{"\"defaultRoot\":", "\"allowedRoots\":", "\"projectsFile\":", "\"projectCreationEnabled\":", "\"projectCreationRoots\":", "\"defaultProjectId\":", "\"projects\":", "\"host\":", "\"port\":", "\"authMode\":", "\"authEnabled\":"}
	pos := -1
	for _, key := range keys {
		next := strings.Index(text, key)
		if next <= pos {
			t.Fatalf("human JSON order changed at %s", key)
		}
		pos = next
	}
	if !strings.Contains(text, `"CODEXPRO_MAX_JOBS": 7`) || !strings.Contains(text, `"packet_bytes": 16384`) {
		t.Fatal("operational-limit naming/default drifted")
	}
}
func TestProfileSaveIsPrivateAndDoesNotReconfigureRuntime(t *testing.T) {
	s, _, _ := fixture(t)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux, nil)
	before := decode(t, request(mux, "GET", "/admin/profile", "", ""))
	if before["exists"] != false {
		t.Fatal("unexpected saved profile")
	}
	body := `{"port":"7215","tunnel":"ngrok","hostname":"https://EXAMPLE.com:443/mcp/","maxWorktrees":"5","worktreeMode":"mcp","bash":"full","bashSession":"private-1","requireBashSession":true}`
	response := request(mux, "POST", "/admin/profile", body, "https://127.0.0.1:7210")
	if response.Code != 200 {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	result := decode(t, response)
	if result["saved"] != true || result["profile_path"] != s.ProfilePath() {
		t.Fatal("save contract changed")
	}
	effective := result["effective"].(map[string]any)
	if effective["hostname"] != "example.com" || effective["port"] != "7215" || effective["maxWorktrees"] != "5" {
		t.Fatalf("normalization drift: %#v", effective)
	}
	runtime := result["runtime"].(map[string]any)
	if runtime["port"] != float64(7210) || s.Config.Listen != "127.0.0.1:7210" || s.Config.String("worktreeMode", "off") != "off" {
		t.Fatal("save changed live configuration")
	}
	st, e := os.Stat(s.ProfilePath())
	if e != nil {
		t.Fatal(e)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("profile mode: %o", st.Mode().Perm())
	}
	if !files.Within(s.ProfilePath(), s.Config.Home) {
		t.Fatal("profile escaped private state")
	}
	if strings.Contains(response.Body.String(), strings.Repeat("x", 48)) {
		t.Fatal("raw credential in HTTP")
	}
	var persisted map[string]any
	b, e := os.ReadFile(s.ProfilePath())
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &persisted); e != nil {
		t.Fatal(e)
	}
	if persisted["root"] != canonicalRoot(s.defaultRoot()) || persisted["version"] != float64(1) {
		t.Fatal("profile root identity lost")
	}
	if persisted["port"] != "7215" {
		t.Fatal("saved port must remain string")
	}
	health := decode(t, request(mux, "GET", "/healthz", "", ""))
	if health["authRequired"] != true || health["bashMode"] != "full" || health["worktreeRoot"] != nil {
		t.Fatal("health contract drift")
	}
}
func TestProfileErrorsAndBoundedRequests(t *testing.T) {
	s, _, _ := fixture(t)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux, nil)
	for _, tc := range []struct {
		method, body, origin string
		status               int
		code                 string
	}{
		{"POST", `{"port":7210}`, "https://evil.test", 403, "origin_denied"},
		{"POST", `{"port":`, "", 400, "invalid_json"},
		{"POST", "null", "", 400, "invalid_json"},
		{"POST", "[]", "", 400, "invalid_profile"},
		{"POST", `{"port":0}`, "", 400, "invalid_profile"},
		{"POST", `{"port":1.5}`, "", 400, "invalid_profile"},
		{"POST", `{"port":"bad"}`, "", 400, "invalid_profile"},
		{"POST", `{"bash":"safe"}`, "", 400, "invalid_profile"},
		{"POST", `{"handoffMode":"on"}`, "", 400, "invalid_profile"},
		{"POST", `{"codexSessions":"metadata"}`, "", 400, "invalid_profile"},
		{"POST", `{"work":"on"}`, "", 400, "invalid_profile"},
		{"POST", `{"requireBashSession":true}`, "", 400, "invalid_profile"},
		{"POST", `{"tunnel":"ngrok","hostname":""}`, "", 400, "invalid_profile"},
		{"POST", `{"hostname":"http://example.com"}`, "", 400, "invalid_profile"},
		{"POST", `{"hostname":"https://example.com/other"}`, "", 400, "invalid_profile"},
		{"POST", `{"widgetDomain":"https://example.com/path"}`, "", 400, "invalid_profile"},
		{"POST", `{"worktreeBase":"--bad"}`, "", 400, "invalid_profile"},
		{"POST", `{"bashSession":"bad session"}`, "", 400, "invalid_profile"},
		{"DELETE", "", "", 405, "method_not_allowed"},
	} {
		response := request(mux, tc.method, "/admin/profile", tc.body, tc.origin)
		if response.Code != tc.status || errorCode(t, response) != tc.code {
			t.Fatalf("%s %s: status=%d code=%s", tc.method, tc.body, response.Code, errorCode(t, response))
		}
	}
	unknown := request(mux, "POST", "/admin/profile", `{"z":true,"a":1}`, "")
	v := decode(t, unknown)
	issues := v["error"].(map[string]any)["issues"].(map[string]any)
	form := issues["formErrors"].([]any)
	if len(form) != 1 || form[0] != `Unrecognized keys: "z", "a"` {
		t.Fatalf("unknown-key insertion order: %#v", form)
	}
	large := request(mux, "POST", "/admin/profile", strings.Repeat(" ", 32769), "")
	if large.Code != 413 || errorCode(t, large) != "payload_too_large" {
		t.Fatal("content length cap missing")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:7210/admin/profile", strings.NewReader(strings.Repeat(" ", 32769)))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if rec.Code != 413 || !strings.Contains(rec.Body.String(), "Request body is too large.") {
		t.Fatal("stream cap missing")
	}
	if _, e := os.Stat(s.ProfilePath()); !os.IsNotExist(e) {
		t.Fatal("invalid requests created profile")
	}
}
func TestProfileRateLimitAndSameOriginOrder(t *testing.T) {
	s, _, _ := fixture(t)
	now := time.Unix(1000, 0)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux, &HTTPOptions{Clock: func() time.Time { return now }})
	for i := 0; i < 35; i++ {
		r := request(mux, "POST", "/admin/profile", "{}", "https://other.test")
		if r.Code != 403 {
			t.Fatal("same-origin should run before rate count")
		}
	}
	for i := 0; i < 30; i++ {
		r := request(mux, "POST", "/admin/profile", `{"port":0}`, "")
		if r.Code != 400 {
			t.Fatalf("request %d: %d", i, r.Code)
		}
	}
	r := request(mux, "POST", "/admin/profile", "{}", "")
	if r.Code != 429 || errorCode(t, r) != "rate_limited" {
		t.Fatal("31st request must be limited")
	}
	now = now.Add(time.Minute)
	r = request(mux, "POST", "/admin/profile", `{"port":0}`, "")
	if r.Code != 400 {
		t.Fatal("fixed window failed to expire")
	}
}
func TestProfileIdentityStaleRuntimeAndAtomicSymlinkBoundary(t *testing.T) {
	s, _, _ := fixture(t)
	if e := os.MkdirAll(filepath.Dir(s.ProfilePath()), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(s.ProfilePath(), []byte(`{"root":"/different-root","port":"1"}`), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := s.readProfile()
	if e != nil || len(p) != 0 {
		t.Fatal("profile identity mismatch accepted")
	}
	if e := os.MkdirAll(filepath.Dir(s.runtimePath()), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(s.runtimePath(), []byte(`{"pid":2147483646,"endpoint":"https://old.test/mcp"}`), 0600); e != nil {
		t.Fatal(e)
	}
	p, e = s.runtimeConnection()
	if e != nil || len(p) != 0 {
		t.Fatal("stale runtime leaked")
	}
	if _, e = os.Stat(s.runtimePath()); e != nil {
		t.Fatal("read unexpectedly mutated runtime state")
	}
	if e = os.RemoveAll(filepath.Dir(s.ProfilePath())); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Dir(s.ProfilePath())); e != nil {
		t.Fatal(e)
	}
	if e = s.saveProfile(map[string]any{"port": "7210"}); e == nil {
		t.Fatal("profile write followed escaped directory symlink")
	}
	entries, e := os.ReadDir(outside)
	if e != nil || len(entries) != 0 {
		t.Fatal("escaped profile write had side effects")
	}
}
func TestSetupFaviconAndDashboardReauth(t *testing.T) {
	s, _, _ := fixture(t)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux, nil)
	root := request(mux, "GET", "/", "", "")
	setup := request(mux, "GET", "/setup", "", "")
	if root.Code != 200 || root.Body.String() != setup.Body.String() {
		t.Fatal("root/setup equivalence drifted")
	}
	html := setup.Body.String()
	for _, part := range []string{`data-profile-form`, `name="worktreeMode"`, `name="projectsFile"`, `data-local-link`, `headers.Authorization="Bearer "+access`} {
		if !strings.Contains(html, part) {
			t.Fatalf("setup missing %s", part)
		}
	}
	for _, part := range []string{`name="handoffMode"`, `name="codexSessions"`, `<select name="bash">`, strings.Repeat("x", 48), "#ZgotmplZ"} {
		if strings.Contains(html, part) {
			t.Fatalf("retired/invalid/raw-secret markup: %s", part)
		}
	}
	favicon := request(mux, "GET", "/favicon.ico", "", "")
	if favicon.Code != 200 || favicon.Header().Get("Cache-Control") != "public, max-age=86400" || !strings.Contains(favicon.Body.String(), `viewBox="0 0 64 64"`) {
		t.Fatal("favicon contract")
	}
	req := httptest.NewRequest("GET", "http://local/activity?x=1", nil)
	req.Header.Set("Accept", "text/html")
	if !WantsDashboardReauth(req) {
		t.Fatal("dashboard reauth predicate")
	}
	out := httptest.NewRecorder()
	SendDashboardReauth(out, req)
	if out.Code != 401 || !strings.Contains(out.Body.String(), "codexpro.activity.credential") || !strings.Contains(out.Header().Get("Content-Security-Policy"), "form-action 'none'") {
		t.Fatal("reauth page contract")
	}
	req.URL.Path = "/setup"
	if WantsDashboardReauth(req) {
		t.Fatal("setup must not trigger activity-only reauth")
	}
}
func TestSelfTestReadOnlyAndOptionalProbes(t *testing.T) {
	s, scope, w := fixture(t)
	runGit(t, w.Root, "init", "--quiet")
	r, e := s.Call(context.Background(), scope, "codexpro_self_test", map[string]any{"workspace_id": w.ID, "include_global_skills": false})
	if e != nil {
		t.Fatal(e)
	}
	if r.StructuredContent["failed"] != 0 || r.StructuredContent["status"] != "warn" {
		t.Fatalf("unexpected self-test: %#v", r.StructuredContent)
	}
	if len(r.StructuredContent["files_touched"].([]string)) != 0 {
		t.Fatal("read-only self-test reported mutation")
	}
	if _, e = os.Stat(filepath.Join(w.Root, ".ai-bridge")); !os.IsNotExist(e) {
		t.Fatal("default diagnostic created context directory")
	}
	r, e = s.Call(context.Background(), scope, "codexpro_self_test", map[string]any{"workspace_id": w.ID, "include_global_skills": false, "write_probe": true, "pro_context_probe": true, "bash_probe": true})
	if e != nil {
		t.Fatal(e)
	}
	if r.StructuredContent["failed"] != 0 {
		t.Fatalf("probe failed: %#v", r.StructuredContent["checks"])
	}
	touched := r.StructuredContent["files_touched"].([]string)
	if !reflect.DeepEqual(touched, []string{".ai-bridge/codexpro-self-test.md"}) {
		t.Fatal(touched)
	}
	b, e := os.ReadFile(filepath.Join(w.Root, touched[0]))
	if e != nil || !bytes.Contains(b, []byte("marker: after")) {
		t.Fatal("write/edit marker failed")
	}
	entries, e := os.ReadDir(filepath.Join(w.Root, ".ai-bridge"))
	if e != nil || len(entries) != 1 {
		t.Fatal("diagnostic published an additional context file")
	}
}
func TestSelfTestChecksActualRegistryAndMcpNames(t *testing.T) {
	s, scope, w := fixture(t)
	runGit(t, w.Root, "init", "--quiet")
	s.RegisteredNames = func() []string { return []string{"missing-dispatch"} }
	home := s.Config.String("skillHomeDir", "")
	if e := os.MkdirAll(filepath.Join(home, ".codex"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(`[mcp_servers.alpha]
[mcpServers."beta"]
[mcp_servers.alpha]
`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(w.Root, ".mcp.json"), []byte(`{"mcpServers":{"alpha":{},"gamma":{}}}`), 0600); e != nil {
		t.Fatal(e)
	}
	names := s.discoverMcpServers(w)
	if len(names) != 4 {
		t.Fatalf("MCP source de-duplication: %#v", names)
	}
	r, e := s.Call(context.Background(), scope, "codexpro_self_test", map[string]any{"workspace_id": w.ID, "include_global_skills": false})
	if e != nil {
		t.Fatal(e)
	}
	if r.StructuredContent["status"] != "fail" {
		t.Fatal("registry mismatch hidden")
	}
	found := false
	for _, c := range r.StructuredContent["checks"].([]check) {
		if c.Name == "registered tool set" {
			found = c.Status == "fail" && strings.Contains(c.Detail, "missing:")
		}
		if c.Name == "inventory" && !strings.Contains(c.Detail, "4 MCP server names visible") {
			t.Fatal(c.Detail)
		}
	}
	if !found {
		t.Fatal("missing registry check")
	}
}
