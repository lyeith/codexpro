package search

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// The cached path must produce exactly what a fresh service computes, and a
// changed file must be re-read.
func TestAnalysisFactCacheMatchesFreshAnalysisAndSeesChanges(t *testing.T) {
	s, w, _ := testFixture(t)
	put(t, w.Root, "src/a.ts", "import { b } from './b';\nexport function alpha() {}\n")
	put(t, w.Root, "src/b.ts", "export const b = 2;\n")
	put(t, w.Root, "README.md", "# fixture\n")
	first, e := s.Analyze(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	fresh, _, _ := testFixture(t)
	fresh.Config = s.Config
	fresh.Files = s.Files
	want, e := fresh.analyze(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	normalize := func(a *Analysis) string {
		c := *a
		c.Cache = AnalysisCache{}
		b, _ := json.Marshal(c)
		return string(b)
	}
	if normalize(first) != normalize(want) {
		t.Fatalf("cached analysis differs from fresh analysis:\n%s\n%s", normalize(first), normalize(want))
	}
	// Change content and size: the new declaration must appear.
	put(t, w.Root, "src/b.ts", "export const b = 2;\nexport function beta() {}\n")
	future := time.Now().Add(2 * time.Second)
	if e := os.Chtimes(filepath.Join(w.Root, "src/b.ts"), future, future); e != nil {
		t.Fatal(e)
	}
	second, e := s.Analyze(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, sym := range second.Symbols {
		found = found || sym.Name == "beta"
	}
	if !found || second.Fingerprint == first.Fingerprint {
		t.Fatalf("changed file was not re-read: %#v", second.Symbols)
	}
	// A file that becomes binary leaves the inventory, as before caching.
	put(t, w.Root, "README.md", "bin\x00ary")
	third, e := s.Analyze(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range third.Files {
		if f.Path == "README.md" {
			t.Fatal("binary file stayed in the inventory")
		}
	}
}

func TestAnalysisIsSharedAcrossConcurrentCallers(t *testing.T) {
	s, w, _ := testFixture(t)
	for i := 0; i < 50; i++ {
		put(t, w.Root, filepath.Join("src", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".ts"), "export const v = 1;\n")
	}
	var wg sync.WaitGroup
	results := make(chan *Analysis, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, e := s.Analyze(context.Background(), w)
			if e != nil {
				t.Error(e)
				return
			}
			results <- a
		}()
	}
	wg.Wait()
	close(results)
	keys := map[string]bool{}
	for a := range results {
		keys[a.Cache.Key] = true
	}
	if len(keys) != 1 {
		t.Fatalf("concurrent callers saw different analyses: %v", keys)
	}
	s.mu.Lock()
	inflight := len(s.flights)
	s.mu.Unlock()
	if inflight != 0 {
		t.Fatal("finished analysis left a flight registered")
	}
}

func TestAnalysisBudgetReturnsUnavailableWarning(t *testing.T) {
	s, w, scope := testFixture(t)
	for i := 0; i < 400; i++ {
		put(t, w.Root, filepath.Join("src", "d"+string(rune('a'+i%26)), "f"+string(rune('a'+i/26))+".ts"), strings.Repeat("export const v = 1;\n", 200))
	}
	s.Config.Set("analysisBudgetMs", 1)
	_, e := s.Analyze(context.Background(), w)
	if e == nil || !strings.Contains(e.Error(), "time budget") {
		t.Fatalf("budget did not bound the analysis: %v", e)
	}
	r := invoke(t, s, scope, "search", map[string]any{"query": "export", "intent": "text", "max_results": 5})
	analysis, _ := r.StructuredContent["analysis"].(*StructuredResult)
	if analysis == nil {
		b, _ := json.Marshal(r.StructuredContent["analysis"])
		if !strings.Contains(string(b), "Repository analysis unavailable") {
			t.Fatalf("search did not report the existing unavailable warning: %s", b)
		}
	} else if len(analysis.Warnings) == 0 || !strings.HasPrefix(analysis.Warnings[0], "Repository analysis unavailable") {
		t.Fatalf("unexpected analysis warnings: %#v", analysis.Warnings)
	}
	// Progress made before the budget ran out is kept: with a normal budget the
	// next analysis completes.
	s.Config.Set("analysisBudgetMs", 10000)
	if _, e = s.Analyze(context.Background(), w); e != nil {
		t.Fatal(e)
	}
}

func TestAnalysisCallerCancellationDoesNotFailOtherCallers(t *testing.T) {
	s, w, _ := testFixture(t)
	for i := 0; i < 200; i++ {
		put(t, w.Root, filepath.Join("src", "g"+string(rune('a'+i%26))+string(rune('a'+i/26))+".ts"), strings.Repeat("export const v = 1;\n", 100))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Analyze(ctx, w); e == nil {
		t.Fatal("cancelled caller was not released")
	}
	if _, e := s.Analyze(context.Background(), w); e != nil {
		t.Fatalf("a cancelled caller failed the shared analysis: %v", e)
	}
}

// A ripgrep that never finishes is bounded and reported as a truncated page.
func TestRipgrepTimeoutReturnsTruncatedPartialPage(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 20\n"
	if e := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s, w, _ := testFixture(t)
	put(t, w.Root, "a.txt", "needle\n")
	s.Config.Set("searchTimeoutMs", 300)
	o := options{Query: "needle", Root: ".", Max: 10}
	started := time.Now()
	p, e := s.ripgrep(context.Background(), w, o, "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("ripgrep timeout not enforced: %v", elapsed)
	}
	if !p.More || len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], "timed out") {
		t.Fatalf("timeout not reported as a truncated page: %#v", p)
	}
	_ = wire.Workspace{}
}
