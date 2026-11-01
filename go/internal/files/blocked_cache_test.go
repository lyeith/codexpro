package files

import "testing"

// Remembered verdicts must equal the uncached glob evaluation and follow a
// change of the configured glob list.
func TestBlockedVerdictCacheMatchesGlobsAndFollowsConfigChanges(t *testing.T) {
	s, _, _ := fixture(t)
	paths := []string{"src/a.ts", "node_modules/x/index.js", "deep/.env", "a/b/secrets/k.txt", "docs/readme.md", ".git/config", "build/out.js"}
	evaluate := func(path string) string {
		for _, g := range s.Config.BlockedGlobs {
			if MatchGlob(g, path) || MatchGlob(g, baseName(path)) {
				return g
			}
		}
		return ""
	}
	for round := 0; round < 2; round++ {
		for _, p := range paths {
			if got, want := s.BlockedGlob(p), evaluate(p); got != want {
				t.Fatalf("round %d %s: cached %q, glob evaluation %q", round, p, got, want)
			}
		}
	}
	if s.BlockedGlob("docs/readme.md") != "" {
		t.Fatal("unblocked path reported blocked")
	}
	original := s.Config.BlockedGlobs
	s.Config.BlockedGlobs = append(append([]string{}, original...), "docs/**")
	if s.BlockedGlob("docs/readme.md") != "docs/**" {
		t.Fatal("verdict cache ignored a changed glob list")
	}
	s.Config.BlockedGlobs = original
	if s.BlockedGlob("docs/readme.md") != "" {
		t.Fatal("verdict cache kept a stale verdict after the glob list changed back")
	}
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == 47 {
			return p[i+1:]
		}
	}
	return p
}
