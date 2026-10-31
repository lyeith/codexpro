package search

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type testResolver struct{ w wire.Workspace }

func (r testResolver) Resolve(context.Context, wire.Scope, string) (wire.Workspace, error) {
	return r.w, nil
}
func (r testResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.w.ProjectID, Root: r.w.Root}}
}
func (r testResolver) Project(id string) (wire.Project, bool) {
	return wire.Project{ID: r.w.ProjectID, Root: r.w.Root}, id == r.w.ProjectID
}
func testFixture(t *testing.T) (*Service, wire.Workspace, wire.Scope) {
	t.Helper()
	cfg := config.New(t.TempDir())
	cfg.BlockedGlobs = []string{"**/.git/**", "**/node_modules/**", ".env"}
	cfg.Set("maxSearchResults", 200)
	w := wire.Workspace{ID: "ws_contract", ProjectID: "contract", Root: t.TempDir()}
	f, e := files.New(cfg, testResolver{w})
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(cfg, testResolver{w}, f)
	if e != nil {
		t.Fatal(e)
	}
	return s, w, wire.Scope{PrincipalID: "owner", SessionID: "one"}
}
func put(t *testing.T, root, path, text string) {
	t.Helper()
	abs := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(abs), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(abs, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func invoke(t *testing.T, s *Service, scope wire.Scope, tool string, a map[string]any) wire.Result {
	t.Helper()
	r, e := s.Call(context.Background(), scope, tool, a)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func assertCode(t *testing.T, e error, expected string) {
	t.Helper()
	var problem *wire.Error
	if !errors.As(e, &problem) || problem.Code != expected {
		t.Fatalf("error %v; want %s", e, expected)
	}
}
func TestConfigQueryLocationsAndFormats(t *testing.T) {
	for _, tc := range []struct {
		format, text, query, address string
		line                         int
	}{
		{"json", "{\n \"routes\": [{\"path\": \"/a\"}, {\"path\": \"/b\"}]\n}\n", "routes[1].path", "$.routes[1].path", 2},
		{"json", "{\n // note\n \"a.b\": {\"x/y\": true,},\n}\n", `["a.b"]["x/y"]`, `$["a.b"]["x/y"]`, 3},
		{"yaml", "server:\n  host: localhost\n  port: 8080\n", "server.port", "$.server.port", 3},
		{"toml", "[server]\nhost = \"localhost\"\nport = 8080\n", "server.port", "$.server.port", 3},
	} {
		matches, _, e := queryConfig(tc.text, tc.format, tc.query)
		if e != nil {
			t.Fatalf("%s: %v", tc.format, e)
		}
		if len(matches) != 1 || matches[0].Address != tc.address || matches[0].Line != tc.line {
			t.Fatalf("%s: %#v", tc.format, matches)
		}
	}
	values, _, e := queryConfig("{\"routes\":[{\"path\":\"/a\"},{\"path\":\"/b\"}]}", "json", "/routes/*/path")
	if e != nil || len(values) != 2 {
		t.Fatalf("wildcard: %#v %v", values, e)
	}
	if _, _, e := queryConfig("{\"0\":\"string\",\"items\":[\"zero\"]}", "json", "items["); e == nil {
		t.Fatal("unclosed query accepted")
	}
}
func TestSearchCursorBindingAndPageContinuity(t *testing.T) {
	s, w, scope := testFixture(t)
	put(t, w.Root, "a.txt", "needle one\nneedle two\nneedle three\n")
	if _, e := exec.LookPath("rg"); e != nil {
		t.Skip("real ripgrep unavailable")
	}
	args := map[string]any{"query": "needle", "max_results": 1, "context_before": 0, "context_after": 0}
	first := invoke(t, s, scope, "search", args)
	if first.StructuredContent["has_more"] != true {
		t.Fatal(first.StructuredContent)
	}
	cursor := first.StructuredContent["next_cursor"].(string)
	args["cursor"] = cursor
	second := invoke(t, s, scope, "search", args)
	matches := second.StructuredContent["matches"].([]map[string]any)
	if len(matches) != 1 || matches[0]["line"] != 2 {
		t.Fatal(matches)
	}
	args["query"] = "other"
	_, e := s.Call(context.Background(), scope, "search", args)
	assertCode(t, e, "search_cursor_mismatch")
}
func TestSearchContextGrantsOnlyShownLines(t *testing.T) {
	s, w, scope := testFixture(t)
	put(t, w.Root, "a.txt", "one\nneedle\nthree\n")
	o := s.options(map[string]any{"query": "needle", "context_before": 0, "context_after": 0})
	raw := []hit{{Path: "a.txt", Line: 2, End: 2, Text: "needle", Source: "text", Editable: true}}
	states, _ := s.loadContexts(w, raw)
	_, matches, contexts, _, _, e := s.contexts(scope, o, raw, states)
	if e != nil {
		t.Fatal(e)
	}
	if len(contexts) != 1 || contexts[0]["text"] != "2 | needle" {
		t.Fatal(contexts)
	}
	scope.SessionID = "second"
	tag := matches[0]["edit_tag"].(string)
	_, e = s.Files.Call(context.Background(), scope, "edit", map[string]any{"path": "a.txt", "edit_tag": tag, "edits": []any{map[string]any{"op": "replace", "start_line": 1, "content": "ONE"}}})
	assertCode(t, e, "edit_range_unseen")
	_, e = s.Files.Call(context.Background(), scope, "edit", map[string]any{"path": "a.txt", "edit_tag": tag, "edits": []any{map[string]any{"op": "replace", "start_line": 2, "content": "NEEDLE"}}})
	if e != nil {
		t.Fatal(e)
	}
}
func TestAnalysisExtractsImportsRolesAndImpact(t *testing.T) {
	s, w, _ := testFixture(t)
	put(t, w.Root, "src/auth.ts", "export function login() {}\n")
	put(t, w.Root, "src/main.ts", "import { login } from './auth.js';\nlogin();\n")
	put(t, w.Root, "tests/auth.test.ts", "import { login } from '../src/auth.js';\nlogin();\n")
	put(t, w.Root, "README.md", "# App\n")
	put(t, w.Root, "package.json", "{\"scripts\":{\"test\":\"node test.js\",\"lint\":\"eslint .\"},\"packageManager\":\"pnpm@9\"}")
	a, e := s.Analyze(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Symbols) != 1 || len(a.Relationships) != 2 || a.Coverage.AnalyzedFiles != 3 {
		t.Fatalf("%#v", a)
	}
	a2, e := s.Analyze(context.Background(), w)
	if e != nil || !a2.Cache.Hit {
		t.Fatalf("second cache: %#v %v", a2, e)
	}
	impact, e := s.Review(context.Background(), w, []string{"src/auth.ts"})
	if e != nil {
		t.Fatal(e)
	}
	if len(impact.DependentFiles) != 1 || impact.DependentFiles[0].Path != "src/main.ts" || len(impact.RelatedTests) != 1 || impact.RelatedTests[0].Path != "tests/auth.test.ts" {
		t.Fatalf("%#v", impact)
	}
	if len(impact.RiskSignals) != 1 || impact.RiskSignals[0].ID != "authentication" || len(impact.RecommendedCommands) != 2 || impact.RecommendedCommands[0].Command != "pnpm test" {
		t.Fatalf("%#v", impact)
	}
	s.Invalidate(w.ID)
	a3, e := s.Analyze(context.Background(), w)
	if e != nil || a3.Cache.Hit {
		t.Fatal("explicit invalidation not applied")
	}
}
func TestAnalysisLimitsScopesAndConcurrentCache(t *testing.T) {
	s, w, scope := testFixture(t)
	put(t, w.Root, "src/a.ts", "export const a = 1;\nexport const b = 2;\n")
	put(t, w.Root, "tests/a.test.ts", "const a = 3;\n")
	s.Config.Set("maxSymbols", 1)
	r := invoke(t, s, scope, "inspect_workspace", map[string]any{"path": "src", "include_relationships": false})
	if len(r.StructuredContent["files"].([]InventoryFile)) != 1 || len(r.StructuredContent["symbols"].([]Symbol)) != 1 || len(r.StructuredContent["relationships"].([]Relationship)) != 0 {
		t.Fatal(r.StructuredContent)
	}
	if r.StructuredContent["coverage"].(Coverage).Truncated != true {
		t.Fatal("symbol budget is not reported")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Analyze(context.Background(), w); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
}
func TestStructuredDefinitionsAndDependentTests(t *testing.T) {
	s, w, scope := testFixture(t)
	put(t, w.Root, "src/a.ts", "export function greet() {}\n")
	put(t, w.Root, "tests/a.test.ts", "import { greet } from '../src/a.js';\ngreet();\n")
	o := s.options(map[string]any{"query": "greet", "intent": "references", "include_tests": true, "path": "src"})
	grouped, e := s.structuredSearch(context.Background(), w, o)
	if e != nil {
		t.Fatal(e)
	}
	if len(grouped.Groups["definitions"]) != 1 || len(grouped.Groups["tests"]) != 2 {
		t.Fatalf("%#v", grouped)
	}
	s.Config.Set("analysisEnabled", false)
	r := s.structured(context.Background(), w, o)
	if r.Cache.Key != "disabled" || len(r.Warnings) != 1 {
		t.Fatal(r)
	}
	_ = scope
}
func fakeAst(t *testing.T, s *Service, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ast-grep")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'ast-grep 0.42.0\\n'; exit 0; fi\n" + body + "\n"
	if e := os.WriteFile(path, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	s.Config.Set("astGrepPath", path)
}
func TestAstByteRangeCapturesAndProvenance(t *testing.T) {
	s, w, scope := testFixture(t)
	text := "const message = \"✓\";\n"
	put(t, w.Root, "unicode.ts", text)
	raw := `{"file":"unicode.ts","language":"TypeScript","text":"const message = \"✓\";","range":{"byteOffset":{"start":0,"end":22},"start":{"line":0,"column":0},"end":{"line":0,"column":22}},"metaVariables":{"single":{"VALUE":{"text":"\"✓\"","range":{"byteOffset":{"start":16,"end":21},"start":{"line":0,"column":16},"end":{"line":0,"column":21}}}}}}`
	var record map[string]any
	if e := json.Unmarshal([]byte(raw), &record); e != nil {
		t.Fatal(e)
	}
	record["range"].(map[string]any)["byteOffset"].(map[string]any)["end"] = len([]byte(strings.TrimSuffix(text, "\n")))
	encoded, _ := json.Marshal(record)
	fakeAst(t, s, "printf '%s\\n' '"+string(encoded)+"'")
	r := invoke(t, s, scope, "ast_grep", map[string]any{"pattern": "const $NAME = $VALUE", "language": "typescript"})
	m := r.StructuredContent["matches"].([]map[string]any)
	if len(m) != 1 || m[0]["editable"] != true || m[0]["end_byte"] != len([]byte(strings.TrimSuffix(text, "\n"))) || len(m[0]["captures"].([]map[string]any)) != 1 {
		t.Fatal(m)
	}
	tag := m[0]["edit_tag"]
	scope.SessionID = "second"
	_, e := s.Files.Call(context.Background(), scope, "edit", map[string]any{"path": "unicode.ts", "edit_tag": tag, "edits": []any{map[string]any{"op": "replace", "start_line": 1, "content": "const message = \"done\";"}}})
	if e != nil {
		t.Fatal(e)
	}
}
func TestAstStaleBytesNeverGrantEditTag(t *testing.T) {
	s, w, scope := testFixture(t)
	put(t, w.Root, "a.ts", "const current = 1;\n")
	hit := astHit{Path: "a.ts", Language: "TypeScript", Preview: "old", Digest: files.SHA("old"), StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 3, MatchBytes: 3, Captures: []map[string]any{}}
	states, _ := s.astStates(w, []astHit{hit})
	_, matches, contexts, _, warn := s.astContexts(scope, s.astOptions(map[string]any{}), []astHit{hit}, states)
	if matches[0]["editable"] != false || contexts[0]["truncated"] != true || len(warn) != 1 {
		t.Fatal(matches, contexts, warn)
	}
	if _, ok := matches[0]["edit_tag"]; ok {
		t.Fatal("stale hit received tag")
	}
}
func TestAstMalformedOutputAndCancellation(t *testing.T) {
	s, _, scope := testFixture(t)
	fakeAst(t, s, "printf 'invalid-json\\n'")
	_, e := s.Call(context.Background(), scope, "ast_grep", map[string]any{"pattern": "x"})
	assertCode(t, e, "ast_grep_invalid_output")
	fakeAst(t, s, "sleep 30")
	executable, _ := s.astExecutable()
	if _, e = s.astVersion(context.Background(), executable); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e = s.Call(ctx, scope, "ast_grep", map[string]any{"pattern": "x"})
	assertCode(t, e, "ast_grep_cancelled")
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancellation failed to bound process group")
	}
}
