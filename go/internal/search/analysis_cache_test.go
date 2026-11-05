package search

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	const fileCount = 400
	const fileBytes = 4000 // 200 fixture lines of 20 bytes each.
	const budgetError = "repository analysis did not finish within its 1 ms time budget; partial progress is kept for the next call"
	body := strings.Repeat("// ordinary source.\n", 199)
	wantSymbols := make([]Symbol, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		path := filepath.Join("src", "d"+string(rune('a'+i%26)), "f"+string(rune('a'+i/26))+".ts")
		name := fmt.Sprintf("v%03d", i)
		put(t, w.Root, path, "export let "+name+" = 1\n"+body)
		wantSymbols = append(wantSymbols, Symbol{Name: name, Kind: "variable", Path: filepath.ToSlash(path), Line: 1, Exported: true, Confidence: "strong"})
	}
	slices.SortFunc(wantSymbols, func(a, b Symbol) int { return strings.Compare(a.Path, b.Path) })
	s.Config.Set("analysisBudgetMs", 1)
	if _, err := s.Analyze(context.Background(), w); err == nil || err.Error() != budgetError {
		t.Fatalf("short-budget Analyze error = %v, want %q", err, budgetError)
	}
	r := invoke(t, s, scope, "search", map[string]any{"query": "export", "intent": "text", "max_results": 5})
	warning, ok := r.StructuredContent["analysis"].(*StructuredResult)
	if !ok || warning == nil {
		t.Fatalf("search analysis has type %T, want *StructuredResult", r.StructuredContent["analysis"])
	}
	if want := []string{"Repository analysis unavailable: " + budgetError}; !slices.Equal(warning.Warnings, want) {
		t.Fatalf("search analysis warnings = %q, want %q", warning.Warnings, want)
	}

	s.Config.Set("analysisBudgetMs", 10000)
	got, err := s.Analyze(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	wantCoverage := Coverage{InventoryFiles: fileCount, AnalyzedFiles: fileCount, ScannedBytes: fileCount * fileBytes, SymbolCount: fileCount, Warnings: []string{}}
	if !reflect.DeepEqual(got.Coverage, wantCoverage) {
		t.Fatalf("recovery coverage = %+v, want %+v", got.Coverage, wantCoverage)
	}
	if len(got.Files) != fileCount || len(got.Symbols) != fileCount {
		t.Fatalf("recovery returned %d files and %d symbols, want %d each", len(got.Files), len(got.Symbols), fileCount)
	}
	for i, want := range wantSymbols {
		file := got.Files[i]
		if file.Path != want.Path || file.Bytes != fileBytes || file.Language != "typescript" || file.Role != "source" || file.Generated || file.Entrypoint {
			t.Fatalf("recovery file %d = %+v, want %s (%d bytes of TypeScript source)", i, file, want.Path, fileBytes)
		}
		if got.Symbols[i] != want {
			t.Fatalf("recovery symbol %d = %+v, want %+v", i, got.Symbols[i], want)
		}
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
