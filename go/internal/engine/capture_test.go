package engine

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// These values were produced by the original audit.ts pure functions under
// Node, rather than by reproducing the Go implementation in the test.
func TestEvidencePathsMatchSourceGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/capture-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit string `json:"source_commit"`
		Cases        []struct {
			Input      any    `json:"input"`
			Normalized string `json:"normalized"`
			Safe       string `json:"safe"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || len(fixture.Cases) < 20 {
		t.Fatal("incomplete source fixture")
	}
	for _, item := range fixture.Cases {
		if got := normalizedEvidencePath(item.Input); got != item.Normalized {
			t.Errorf("normalized %q: got %q, want %q", item.Input, got, item.Normalized)
		}
		if got := safeEvidencePath(item.Input); got != item.Safe {
			t.Errorf("safe %q: got %q, want %q", item.Input, got, item.Safe)
		}
	}
}

// Audit evidence must accept only paths from the same parsed patch that the
// write service executes. Header-looking text inside a malformed patch is not
// evidence that a file was targeted.
func TestPatchEvidenceUsesValidatedPatchPaths(t *testing.T) {
	e := &Engine{}
	cases := []struct {
		name, patch string
		want        []string
	}{
		{"add", "*** Begin Patch\n*** Add File: docs/example.txt\n+hello\n*** End Patch\n", []string{"docs/example.txt"}},
		{"move", "*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n@@\n-old\n+new\n*** End Patch\n", []string{"a.txt", "b.txt"}},
		{"missing terminator", "*** Begin Patch\n*** Add File: docs/ghost.txt\n+hello\n", []string{}},
		{"invalid body", "*** Begin Patch\n*** Add File: docs/ghost.txt\nhello\n*** End Patch\n", []string{}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := e.evidenceRequestPaths("apply_patch", map[string]any{"patch": item.patch})
			if !reflect.DeepEqual(got, item.want) {
				t.Fatalf("got %#v, want %#v", got, item.want)
			}
		})
	}
}
