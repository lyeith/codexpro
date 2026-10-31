package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type fixtureResolver struct{ w wire.Workspace }

func (r fixtureResolver) Resolve(context.Context, wire.Scope, string) (wire.Workspace, error) {
	return r.w, nil
}
func (r fixtureResolver) Projects() []wire.Project {
	return []wire.Project{{ID: r.w.ProjectID, Root: r.w.Root}}
}
func (r fixtureResolver) Project(id string) (wire.Project, bool) {
	return wire.Project{ID: r.w.ProjectID, Root: r.w.Root}, id == r.w.ProjectID
}
func fixture(t *testing.T) (*Service, wire.Workspace, wire.Scope) {
	t.Helper()
	root := t.TempDir()
	cfg := config.New(t.TempDir())
	cfg.BlockedGlobs = []string{".env", "**/.git/**", "**/node_modules/**"}
	w := wire.Workspace{ID: "ws_test", ProjectID: "test", Root: root}
	s, e := New(cfg, fixtureResolver{w})
	if e != nil {
		t.Fatal(e)
	}
	return s, w, wire.Scope{PrincipalID: "owner", SessionID: "first"}
}
func call(t *testing.T, s *Service, scope wire.Scope, name string, args map[string]any) wire.Result {
	t.Helper()
	r, e := s.Call(context.Background(), scope, name, args)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func writeFixture(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0640); e != nil {
		t.Fatal(e)
	}
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *wire.Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error=%v, want code %s", err, want)
	}
}

func TestDecodeTextMaximalSubpart(t *testing.T) {
	for _, tc := range []struct {
		b    []byte
		want string
	}{
		{[]byte{0xf0, 0x9f}, "�"}, {[]byte{0xe1, 0x80, 'x'}, "�x"}, {[]byte{0xff, 0xff}, "��"}, {[]byte{0xed, 0xa0, 0x80}, "���"}, {[]byte("a😀b"), "a😀b"},
	} {
		if got := DecodeText(tc.b); got != tc.want {
			t.Fatalf("%x => %q, want %q", tc.b, got, tc.want)
		}
	}
}
func TestReadDecodedHashAndPagination(t *testing.T) {
	s, w, scope := fixture(t)
	b := []byte{'a', 0xf0, 0x9f, '\n', 'b', '\n'}
	if e := os.WriteFile(filepath.Join(w.Root, "bad.txt"), b, 0600); e != nil {
		t.Fatal(e)
	}
	r := call(t, s, scope, "read", map[string]any{"path": "bad.txt", "max_bytes": 10})
	if r.StructuredContent["sha256"] != SHA(DecodeText(b)) || r.StructuredContent["bytes"] != len(b) {
		t.Fatal(r.StructuredContent)
	}
	if r.StructuredContent["has_more"] != true || r.StructuredContent["next_start_line"] == nil {
		t.Fatal(r.StructuredContent)
	}
	next := r.StructuredContent["next_start_line"].(int)
	r = call(t, s, scope, "read", map[string]any{"path": "bad.txt", "start_line": next, "max_bytes": 100})
	if r.StructuredContent["has_more"] != false || r.StructuredContent["next_start_line"] != nil || r.StructuredContent["truncated"] != true {
		t.Fatal(r.StructuredContent)
	}
}
func TestReadBudgetAfterTransform(t *testing.T) {
	s, w, scope := fixture(t)
	writeFixture(t, filepath.Join(w.Root, "a"), "x\ny\n")
	s.SetTransform(func(_ wire.Scope, _ wire.Workspace, v string) string {
		return strings.ReplaceAll(v, "x", strings.Repeat("X", 40))
	})
	_, e := s.Call(context.Background(), scope, "read", map[string]any{"path": "a", "max_bytes": 20})
	code(t, e, "read_line_too_large")
}
func TestProvenanceSessionsPrincipalsAndRestart(t *testing.T) {
	s, w, a := fixture(t)
	writeFixture(t, filepath.Join(w.Root, "a"), "one\ntwo\nthree\n")
	read := call(t, s, a, "read", map[string]any{"path": "a", "start_line": 1, "end_line": 1})
	args := map[string]any{"path": "a", "edit_tag": read.StructuredContent["edit_tag"], "edits": []any{map[string]any{"op": "replace", "start_line": 2, "content": "changed"}}}
	b := a
	b.SessionID = "second"
	_, e := s.Call(context.Background(), b, "edit", args)
	code(t, e, "edit_range_unseen")
	call(t, s, b, "read", map[string]any{"path": "a", "start_line": 2, "end_line": 2})
	foreign := b
	foreign.PrincipalID = "other"
	_, e = s.Call(context.Background(), foreign, "edit", args)
	code(t, e, "edit_tag_unknown")
	fresh, _ := New(s.Config, s.Workspaces)
	_, e = fresh.Call(context.Background(), b, "edit", args)
	code(t, e, "edit_tag_unknown")
	r := call(t, s, b, "edit", args)
	if r.StructuredContent["additions"] != 1 || r.StructuredContent["deletions"] != 1 {
		t.Fatal(r.StructuredContent)
	}
	actual, _ := os.ReadFile(filepath.Join(w.Root, "a"))
	if string(actual) != "one\nchanged\nthree\n" {
		t.Fatalf("%q", actual)
	}
}
func TestEditOriginalCoordinatesAndNoOp(t *testing.T) {
	s, w, scope := fixture(t)
	writeFixture(t, filepath.Join(w.Root, "a"), "\ufeffa\r\nb\r\nc\r\nd\r\n")
	r := call(t, s, scope, "read", map[string]any{"path": "a"})
	call(t, s, scope, "edit", map[string]any{"path": "a", "edit_tag": r.StructuredContent["edit_tag"], "edits": []any{
		map[string]any{"op": "replace", "start_line": 1, "content": "A\nB"},
		map[string]any{"op": "delete", "start_line": 3},
		map[string]any{"op": "insert_before", "line": 4, "content": "X"},
	}})
	b, _ := os.ReadFile(filepath.Join(w.Root, "a"))
	if string(b) != "\ufeffA\r\nB\r\nb\r\nX\r\nd\r\n" {
		t.Fatalf("%q", b)
	}
	r = call(t, s, scope, "read", map[string]any{"path": "a"})
	_, e := s.Call(context.Background(), scope, "edit", map[string]any{"path": "a", "edit_tag": r.StructuredContent["edit_tag"], "edits": []any{map[string]any{"op": "replace", "start_line": 2, "content": "B"}}})
	if e == nil || !strings.Contains(e.Error(), "would not change") {
		t.Fatalf("%v", e)
	}
}
func TestWritePreservesInodeLinksAndCAS(t *testing.T) {
	s, w, scope := fixture(t)
	path := filepath.Join(w.Root, "a")
	alias := filepath.Join(w.Root, "b")
	writeFixture(t, path, "old\n")
	if e := os.Link(path, alias); e != nil {
		t.Fatal(e)
	}
	oldInfo, _ := os.Stat(path)
	oldInode := oldInfo.Sys().(*syscall.Stat_t).Ino
	call(t, s, scope, "write", map[string]any{"path": "a", "content": "new\n", "expected_sha256": SHA("old\n")})
	newInfo, _ := os.Stat(path)
	b, _ := os.ReadFile(alias)
	if oldInode != newInfo.Sys().(*syscall.Stat_t).Ino || string(b) != "new\n" || newInfo.Mode().Perm() != 0640 {
		t.Fatal("inode/link/mode changed")
	}
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for _, value := range []string{"first\n", "second\n"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, e := s.Call(context.Background(), scope, "write", map[string]any{"path": "a", "content": value, "expected_sha256": SHA("new\n")})
			errorsOut <- e
		}(value)
	}
	wg.Wait()
	close(errorsOut)
	success := 0
	for e := range errorsOut {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful CAS writes=%d", success)
	}
}
func TestCanonicalGuardsAndGlob(t *testing.T) {
	s, w, _ := fixture(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, filepath.Join(w.Root, "escape")); e != nil {
		t.Fatal(e)
	}
	_, _, e := s.ResolvePath(w, "escape/new", true)
	code(t, e, "path_outside_workspace")
	writeFixture(t, filepath.Join(w.Root, ".env"), "innocent fixture")
	_, _, e = s.ResolvePath(w, ".env", false)
	code(t, e, "path_blocked")
	for _, tc := range []struct {
		p, n string
		want bool
	}{{"src/**/*.ts", "src/a.ts", true}, {"src/**/*.{ts,go}", "src/deep/a.go", true}, {"**/node_modules/**", "x/node_modules/a.js", true}, {"*.go", "nested/a.go", false}, {"@(a|b).go", "a.go", true}} {
		if got := MatchGlob(tc.p, tc.n); got != tc.want {
			t.Errorf("%s %s=%t", tc.p, tc.n, got)
		}
	}
}

func TestDotPrefixContainment(t *testing.T) {
	s, w, _ := fixture(t)
	for _, name := range []string{"..notes", "..notes/child", ".normal"} {
		abs, rel, e := s.ResolvePath(w, name, true)
		if e != nil || !Within(abs, w.Root) || rel != name {
			t.Fatalf("valid child %q: abs=%q rel=%q error=%v", name, abs, rel, e)
		}
	}
	for _, name := range []string{"..", "../escape", "../..notes"} {
		_, _, e := s.ResolvePath(w, name, true)
		code(t, e, "path_outside_workspace")
	}
	if !MatchGlob("résumé/**/*.go", "résumé/東京.go") {
		t.Fatal("unicode glob lost literal characters")
	}
	if got := expandHome("~"); got == "" || filepath.Base(got) == "~" {
		t.Fatalf("home expansion %q", got)
	}
}
func TestSnapshotCollisionNeverAuthorizesDifferentText(t *testing.T) {
	s := NewSnapshots()
	scope := wire.Scope{PrincipalID: "p"}
	path := filepath.Join(t.TempDir(), "a")
	seen := map[string]string{}
	first, second := "", ""
	for i := 0; i < 10000; i++ {
		text := strings.Repeat("a", i%5) + string(rune(i+100))
		tag := Tag(text)
		if prior, ok := seen[tag]; ok && prior != text {
			first, second = prior, text
			break
		}
		seen[tag] = text
	}
	if first == "" {
		t.Fatal("fixture did not find a birthday collision")
	}
	tag := s.Record(scope, path, first, 1, 1)
	s.Record(scope, path, second, 1, 1)
	_, e := s.Resolve(scope, path, tag, first, "a")
	code(t, e, "edit_tag_stale")
}
func TestNativePatchWholePlanPreflight(t *testing.T) {
	s, w, scope := fixture(t)
	writeFixture(t, filepath.Join(w.Root, "a"), "old\n")
	patch := "*** Begin Patch\n*** Update File: a\n@@\n-old\n+new\n*** Delete File: missing\n*** End Patch"
	_, e := s.Call(context.Background(), scope, "apply_patch", map[string]any{"patch": patch})
	code(t, e, "patch_context_stale")
	b, _ := os.ReadFile(filepath.Join(w.Root, "a"))
	if string(b) != "old\n" {
		t.Fatal("preflight changed first file")
	}
}
func TestPatchHeadersCannotBeForgedInsideHunk(t *testing.T) {
	p := "--- a/one\n+++ b/one\n@@ -1,1 +1,2 @@\n-x\n+--- a/escape\n++++ b/escape\n"
	paths, e := PatchPaths(p)
	if e != nil {
		t.Fatal(e)
	}
	if len(paths) != 1 || paths[0] != "one" {
		t.Fatal(paths)
	}
}
