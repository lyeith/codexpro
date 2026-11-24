package search

import (
	"errors"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestLiteralFallbackSearchPagesWithoutExternalTools(t *testing.T) {
	// Neither ripgrep nor Node is available: both paths must search in Go.
	t.Setenv("PATH", "")
	for _, restricted := range []bool{false, true} {
		name := "ordinary"
		if restricted {
			name = "restricted"
		}
		t.Run(name, func(t *testing.T) {
			s, w, scope := testFixture(t)
			if restricted {
				scope.Identity = wire.Identity{Source: "named", PrincipalType: "person", Role: "tester"}
			}
			const query = "needle[1].*"
			put(t, w.Root, "a.txt", query+" first\nneedle1 decoy\n")
			put(t, w.Root, "b.txt", query+" second\n")
			put(t, w.Root, "node_modules/blocked.txt", query+" blocked\n")
			args := map[string]any{"query": query, "max_results": 1, "context_before": 0, "context_after": 0}
			for page, wantPath := range []string{"a.txt", "b.txt"} {
				result, err := s.Call(t.Context(), scope, "search", args)
				if err != nil {
					t.Fatal(err)
				}
				data := result.StructuredContent
				matches, ok := data["matches"].([]map[string]any)
				if !ok || len(matches) != 1 || matches[0]["path"] != wantPath || matches[0]["line"] != 1 {
					t.Fatalf("page %d lost literal matching or cursor order: %+v", page, data)
				}
				if data["used"] != "literal" {
					t.Fatalf("Go fallback reported another engine: %+v", data)
				}
				if page == 0 {
					cursor, ok := data["next_cursor"].(string)
					if data["has_more"] != true || !ok || cursor == "" {
						t.Fatalf("first page lost continuation: %+v", data)
					}
					args["cursor"] = cursor
				} else if data["has_more"] != false || data["next_cursor"] != nil {
					// There are exactly two allowed matches. The regex decoy
					// and blocked dependency file must not create another page.
					t.Fatalf("fallback included a nonliteral or blocked match: %+v", data)
				}
				if restricted && (data["root"] != nil || matches[0]["edit_tag"] != nil) {
					t.Fatalf("restricted search exposed workspace or edit authority: %+v", data)
				}
			}
		})
	}
}

func TestLiteralFallbackPreservesRegexErrorClassification(t *testing.T) {
	t.Setenv("PATH", "")
	s, _, scope := testFixture(t)
	_, err := s.Call(t.Context(), scope, "search", map[string]any{"query": "[", "regex": true})
	var domain *wire.Error
	const message = "Regex search requires ripgrep; the bounded literal fallback supports fixed strings only."
	if err == nil || errors.As(err, &domain) || err.Error() != message {
		t.Fatalf("fallback regex refusal changed its ordinary error classification: %T %v", err, err)
	}
	result := wire.ErrorResult(err)
	expected := "Error: " + message
	if !result.IsError || len(result.Content) != 1 || len(result.StructuredContent) != 1 || result.Content[0]["text"] != expected || result.StructuredContent["error"] != expected {
		t.Fatalf("fallback regex refusal lost its error envelope: %+v", result)
	}
}
