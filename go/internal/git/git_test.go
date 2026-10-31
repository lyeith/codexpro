package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/search"
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
func gitCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "NO_COLOR=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, out)
	}
	return string(out)
}
func put(t *testing.T, root, path, text string) {
	t.Helper()
	p := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) (*Service, wire.Workspace, wire.Scope) {
	t.Helper()
	if _, e := exec.LookPath("git"); e != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	gitCommand(t, root, "init", "-q", "-b", "main")
	gitCommand(t, root, "config", "user.name", "Contract Fixture")
	gitCommand(t, root, "config", "user.email", "fixture@example.invalid")
	gitCommand(t, root, "config", "commit.gpgsign", "false")
	put(t, root, "a.txt", "old a\n")
	put(t, root, "b.txt", "old b\n")
	gitCommand(t, root, "add", "--", "a.txt", "b.txt")
	gitCommand(t, root, "commit", "-q", "-m", "fixture")
	cfg := config.New(t.TempDir())
	cfg.BlockedGlobs = []string{".env", "**/.git/**", "**/node_modules/**"}
	cfg.Set("analysisEnabled", false)
	w := wire.Workspace{ID: "ws_git", ProjectID: "git", Root: root}
	f, e := files.New(cfg, testResolver{w})
	if e != nil {
		t.Fatal(e)
	}
	a, e := search.New(cfg, testResolver{w}, f)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(cfg, testResolver{w}, f, a)
	if e != nil {
		t.Fatal(e)
	}
	return s, w, wire.Scope{PrincipalID: "owner", SessionID: "one"}
}
func call(t *testing.T, s *Service, scope wire.Scope, name string, a map[string]any) wire.Result {
	t.Helper()
	r, e := s.Call(context.Background(), scope, name, a)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func errorCode(t *testing.T, e error, code string) {
	t.Helper()
	var problem *wire.Error
	if !errors.As(e, &problem) || problem.Code != code {
		t.Fatalf("%v, want %s", e, code)
	}
}
func TestReviewCheckpointIsSessionScopedAndOptional(t *testing.T) {
	s, w, scope := fixture(t)
	put(t, w.Root, "a.txt", "new a\n")
	args := map[string]any{"since": "last_shown"}
	first := call(t, s, scope, "show_changes", args)
	if first.StructuredContent["review_checkpoint_hit"] != false || first.StructuredContent["changed"] != true || first.StructuredContent["diff"] == "" {
		t.Fatal(first.StructuredContent)
	}
	second := call(t, s, scope, "show_changes", args)
	if second.StructuredContent["review_checkpoint_hit"] != true || second.StructuredContent["changed"] != false || second.StructuredContent["diff"] != "" {
		t.Fatal(second.StructuredContent)
	}
	other := scope
	other.SessionID = "two"
	third := call(t, s, other, "show_changes", args)
	if third.StructuredContent["review_checkpoint_hit"] != false {
		t.Fatal("session leaked")
	}
	s.ForgetSession(scope.PrincipalID, scope.SessionID)
	again := call(t, s, scope, "show_changes", args)
	if again.StructuredContent["review_checkpoint_hit"] != false {
		t.Fatal("session state survived disposal")
	}
	newer, _ := New(s.Config, s.Workspaces, s.Files, s.Analysis)
	again = call(t, newer, scope, "show_changes", args)
	if again.StructuredContent["review_checkpoint_hit"] != false {
		t.Fatal("checkpoint persisted across restart")
	}
}
func TestReviewNoDiffDoesNotAcknowledgeAndUntrackedContentCounts(t *testing.T) {
	s, w, scope := fixture(t)
	put(t, w.Root, "new.txt", "one\n")
	r := call(t, s, scope, "show_changes", map[string]any{"since": "last_shown", "include_diff": false})
	if r.StructuredContent["review_marked"] != false {
		t.Fatal(r.StructuredContent)
	}
	r = call(t, s, scope, "show_changes", map[string]any{"since": "last_shown"})
	if r.StructuredContent["review_checkpoint_hit"] != false {
		t.Fatal(r.StructuredContent)
	}
	r = call(t, s, scope, "show_changes", map[string]any{"since": "last_shown"})
	if r.StructuredContent["review_checkpoint_hit"] != true {
		t.Fatal(r.StructuredContent)
	}
	put(t, w.Root, "new.txt", "two\n")
	r = call(t, s, scope, "show_changes", map[string]any{"since": "last_shown"})
	if r.StructuredContent["review_checkpoint_hit"] != false {
		t.Fatal("untracked bytes were omitted from fingerprint")
	}
}
func TestStagedAndPathScopedReview(t *testing.T) {
	s, w, scope := fixture(t)
	put(t, w.Root, "a.txt", "new a\n")
	put(t, w.Root, "b.txt", "new b\n")
	gitCommand(t, w.Root, "add", "--", "a.txt")
	staged := call(t, s, scope, "show_changes", map[string]any{"staged": true})
	paths := ChangedPaths(staged.StructuredContent["changed_files"].([]string))
	if len(paths) != 1 || paths[0] != "a.txt" {
		t.Fatal(paths)
	}
	scoped := call(t, s, scope, "show_changes", map[string]any{"path": "b.txt"})
	paths = ChangedPaths(scoped.StructuredContent["changed_files"].([]string))
	if len(paths) != 1 || paths[0] != "b.txt" || strings.Contains(scoped.StructuredContent["diff"].(string), "a.txt") {
		t.Fatal(scoped.StructuredContent)
	}
}
func TestExplicitCommitPreservesUnrelatedIndexAndStatus(t *testing.T) {
	s, w, scope := fixture(t)
	put(t, w.Root, "a.txt", "new a\n")
	put(t, w.Root, "b.txt", "new b\n")
	gitCommand(t, w.Root, "add", "--", "b.txt")
	r := call(t, s, scope, "commit_changes", map[string]any{"message": "fix: update a", "paths": []any{"a.txt"}})
	sha := r.StructuredContent["commit"].(string)
	if len(sha) != 40 || r.StructuredContent["commit_short"] != sha[:12] || r.StructuredContent["working_tree_clean"] != false || r.StructuredContent["status_error"] != nil {
		t.Fatal(r.StructuredContent)
	}
	if got := strings.TrimSpace(gitCommand(t, w.Root, "diff", "--cached", "--name-only")); got != "b.txt" {
		t.Fatal(got)
	}
	if got := gitCommand(t, w.Root, "show", "HEAD:b.txt"); got != "old b\n" {
		t.Fatal("unrelated staged change committed")
	}
	if got := gitCommand(t, w.Root, "show", "HEAD:a.txt"); got != "new a\n" {
		t.Fatal(got)
	}
}
func TestDefaultCommitFiltersUntrackedSecrets(t *testing.T) {
	s, w, scope := fixture(t)
	put(t, w.Root, "new.txt", "safe\n")
	put(t, w.Root, ".env", "innocent secret fixture\n")
	r := call(t, s, scope, "commit_changes", map[string]any{"message": "feat: add safe file"})
	paths := r.StructuredContent["files"].([]string)
	blocked := r.StructuredContent["skipped_blocked_paths"].([]string)
	if len(paths) != 1 || paths[0] != "new.txt" || len(blocked) != 1 || blocked[0] != ".env" || r.StructuredContent["working_tree_clean"] != false {
		t.Fatal(r.StructuredContent)
	}
	if got := strings.TrimSpace(gitCommand(t, w.Root, "ls-files", "--", ".env")); got != "" {
		t.Fatal("blocked file staged")
	}
	_, e := s.Call(context.Background(), scope, "commit_changes", map[string]any{"message": "blocked"})
	errorCode(t, e, "nothing_to_commit")
}
func TestShowOutsideGitReturnsStructuredAvailability(t *testing.T) {
	s, _, scope := fixture(t)
	w := wire.Workspace{ID: "ws_empty", ProjectID: "empty", Root: t.TempDir()}
	s.Workspaces = testResolver{w}
	r := call(t, s, scope, "show_changes", map[string]any{})
	if r.StructuredContent["changed"] != false || r.StructuredContent["status_error"] == nil || r.StructuredContent["diff_error"] == nil || r.StructuredContent["diff"] != "" {
		t.Fatal(r.StructuredContent)
	}
}
func TestDashboardBoundedPathsAndNoReviewMutation(t *testing.T) {
	s, w, _ := fixture(t)
	put(t, w.Root, "a.txt", "new a\n")
	put(t, w.Root, "visible.txt", "safe\n")
	put(t, w.Root, ".env", "private fixture\n")
	status, e := s.SnapshotGit(context.Background(), wire.Project{ID: w.ProjectID, Root: w.Root}, false)
	if e != nil {
		t.Fatal(e)
	}
	if status["available"] != true || status["dirty"] != true || status["diffAvailable"] != true || status["hiddenPathCount"] != 1 || status["additions"] != 1 || status["deletions"] != 1 {
		t.Fatal(status)
	}
	diff, e := s.SnapshotGit(context.Background(), wire.Project{ID: w.ProjectID, Root: w.Root}, true)
	if e != nil || !strings.Contains(diff["diff"].(string), "+new a") || diff["truncated"] != false {
		t.Fatal(diff, e)
	}
	if len(s.reviews) != 0 {
		t.Fatal("dashboard acknowledged tool review")
	}
	for _, p := range []string{"../outside", "..", "/tmp/outside", ".env", "."} {
		if s.SafeDashboardPath(p) {
			t.Fatalf("unsafe dashboard path %q", p)
		}
	}
	if !s.SafeDashboardPath("..notes") {
		t.Fatal("valid dot-prefixed path refused")
	}
}
