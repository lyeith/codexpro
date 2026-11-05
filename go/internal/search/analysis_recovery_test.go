package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Interrupt the computation at its next context check after the first complete
// file. The public Analyze call intentionally detaches from caller cancellation.
type cancelAfterSourceFact struct {
	context.Context
	cancel context.CancelFunc
	cache  *factCache
	first  fileIdentity
}

func (c cancelAfterSourceFact) Err() error {
	if c.cache.sourceFact(c.first) != nil {
		c.cancel()
	}
	return c.Context.Err()
}

func TestAnalysisRecoveryReusesCompletedSourceFactAndSeesChangedFile(t *testing.T) {
	s, w, _ := testFixture(t)
	put(t, w.Root, "a.ts", "export const alpha = 1;\n")
	put(t, w.Root, "b.ts", "export const bravo = 2;\n")
	identity := func(name string) fileIdentity {
		t.Helper()
		path := filepath.Join(w.Root, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return identityOf(path, info)
	}
	a, b := identity("a.ts"), identity("b.ts")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	partial, err := s.analyze(cancelAfterSourceFact{ctx, cancel, &s.facts, a}, w)
	if partial != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted analysis: result present=%t, error=%v", partial != nil, err)
	}
	for _, id := range []fileIdentity{a, b} {
		if readable, known := s.facts.readableFact(readableKey{id, s.Files.ScanLimit()}); !known || !readable {
			t.Fatalf("missing readable inventory fact for %s", filepath.Base(id.Abs))
		}
	}
	retained := s.facts.sourceFact(a)
	if retained == nil || s.facts.sourceFact(b) != nil {
		t.Fatal("interrupted extraction should have retained only a.ts")
	}

	recovered, err := s.Analyze(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	want := []Symbol{
		{Name: "alpha", Kind: "variable", Path: "a.ts", Line: 1, Exported: true, Confidence: "strong"},
		{Name: "bravo", Kind: "variable", Path: "b.ts", Line: 1, Exported: true, Confidence: "strong"},
	}
	if !slices.Equal(recovered.Symbols, want) {
		t.Fatalf("recovered symbols = %#v, want %#v", recovered.Symbols, want)
	}
	completedB := s.facts.sourceFact(b)
	if s.facts.sourceFact(a) != retained || completedB == nil {
		t.Fatal("recovery should reuse a.ts and complete b.ts")
	}

	put(t, w.Root, "a.ts", "export const gamma = 1;\n")
	later := time.Unix(0, a.ModifiedNS).Add(time.Second)
	if err := os.Chtimes(a.Abs, later, later); err != nil {
		t.Fatal(err)
	}
	edited := identity("a.ts")
	if edited.Size != a.Size || edited.ModifiedNS == a.ModifiedNS {
		t.Fatal("fixture must change a.ts at the same size with a distinct modification time")
	}
	changed, err := s.Analyze(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	want[0].Name = "gamma"
	if !slices.Equal(changed.Symbols, want) {
		t.Fatalf("changed symbols = %#v, want %#v", changed.Symbols, want)
	}
	reparsed := s.facts.sourceFact(edited)
	if reparsed == nil || reparsed == retained || s.facts.sourceFact(b) != completedB {
		t.Fatal("the edited identity should have a new fact and unchanged b.ts should reuse its fact")
	}
}
