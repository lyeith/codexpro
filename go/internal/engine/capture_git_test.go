package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
)

func fp5Git(t *testing.T, root string, args ...string) string {
	t.Helper()
	options := append([]string{"-C", root, "-c", "user.name=FP5 Fixture", "-c", "user.email=fp5-fixture@invalid"}, args...)
	out, err := exec.Command("git", options...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitCaptureUsesOneProcessAndPreservesBranchAndPaths(t *testing.T) {
	root := t.TempDir()
	fp5Git(t, root, "init", "-q")
	fp5Git(t, root, "symbolic-ref", "HEAD", "refs/heads/fp5-fixture")
	e := &Engine{Config: config.New(t.TempDir())}
	empty := e.captureGitEvidence(context.Background(), root)
	if empty == nil || empty["branch"] != "fp5-fixture" || empty["head"] != nil || empty["dirty"] != false {
		t.Fatalf("unborn capture: %#v", empty)
	}
	if err := os.WriteFile(filepath.Join(root, "before.txt"), []byte("tracked fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fp5Git(t, root, "add", "before.txt")
	fp5Git(t, root, "commit", "-q", "-m", "fixture")
	head := fp5Git(t, root, "rev-parse", "HEAD")
	if err := os.Rename(filepath.Join(root, "before.txt"), filepath.Join(root, "after.txt")); err != nil {
		t.Fatal(err)
	}
	fp5Git(t, root, "add", "-A")
	odd := "space \t\nname.txt"
	if err := os.WriteFile(filepath.Join(root, odd), []byte("untracked fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	script := "#!/bin/sh\nprintf 'git\\n' >> \"$FP5_GIT_COUNT\"\nexec \"$FP5_REAL_GIT\" \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FP5_REAL_GIT", realGit)
	t.Setenv("FP5_GIT_COUNT", count)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	got := e.captureGitEvidence(context.Background(), root)
	launches, err := os.ReadFile(count)
	if err != nil || string(launches) != "git\n" {
		t.Fatalf("capture launches: %q %v", launches, err)
	}
	if got == nil || got["head"] != head || got["branch"] != "fp5-fixture" || got["changed_path_count"] != 3 || got["dirty"] != true {
		t.Fatalf("capture: %#v", got)
	}
	paths := got["changed_paths"].([]string)
	if !slices.Contains(paths, "before.txt") || !slices.Contains(paths, "after.txt") || !slices.Contains(paths, safeEvidencePath(odd)) {
		t.Fatalf("missing paths: %q", paths)
	}
	before := got["status_fingerprint"]
	if err = os.WriteFile(filepath.Join(root, odd), []byte("different-sized fixture contents"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := e.captureGitEvidence(context.Background(), root)
	if changed == nil || changed["status_fingerprint"] == before {
		t.Fatal("same-status file modification disappeared from evidence")
	}
	fp5Git(t, root, "reset", "--hard", "HEAD")
	fp5Git(t, root, "checkout", "--detach", "-q", "HEAD")
	detached := e.captureGitEvidence(context.Background(), root)
	if detached == nil || detached["head"] != head || detached["branch"] != nil {
		t.Fatalf("detached capture: %#v", detached)
	}
}

func TestPorcelainEvidenceParsesRenameCopyConflictAndNULPaths(t *testing.T) {
	text := "# branch.oid abcdef\x00# branch.head topic\x00" +
		"1 .M N... 100644 100644 100644 h1 h2 ordinary path\x00" +
		"2 R. N... 100644 100644 100644 h1 h2 R100 renamed\x00original\x00" +
		"2 C. N... 100644 100644 100644 h1 h2 C100 copied\x00copy-source\x00" +
		"u UU N... 100644 100644 100644 100644 h1 h2 h3 conflict\x00" +
		"? odd\t\npath\x00"
	got, ok := parseEvidencePorcelain(text)
	if !ok || got.head != "abcdef" || got.branch != "topic" || len(got.paths) != 7 {
		t.Fatalf("parse: %+v %v", got, ok)
	}
	if got.paths[6].actual != "odd\t\npath" || got.paths[6].display != safeEvidencePath("odd\t\npath") {
		t.Fatal("path framing or display sanitization changed")
	}
	for _, broken := range []string{"", "# branch.head x", "? no-terminator", "2 R. N... 1 1 1 a b R100 destination\x00", "garbage\x00"} {
		if _, ok := parseEvidencePorcelain(broken); ok {
			t.Fatalf("accepted incomplete status %q", broken)
		}
	}
}

func TestGitCaptureOutsideRepositoryIsUnavailable(t *testing.T) {
	e := &Engine{Config: config.New(t.TempDir())}
	if got := e.captureGitEvidence(context.Background(), t.TempDir()); got != nil {
		t.Fatalf("non-repository evidence: %#v", got)
	}
}
