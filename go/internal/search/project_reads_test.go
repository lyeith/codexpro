package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestPersonalSearchMatchesOnlyVisibleSourceAndBounds(t *testing.T) {
	s, w, scope := testFixture(t)
	scope.Identity = wire.Identity{Source: "named", PrincipalType: "person", Role: "tester"}
	secret := "pa1_" + strings.Repeat("A", 43)
	put(t, w.Root, "sample.txt", "visible before\nkey "+secret+"\nvisible after\n")
	search := func(query string) wire.Result {
		t.Helper()
		r, e := s.Call(context.Background(), scope, "search", map[string]any{"query": query, "path": "sample.txt", "context_before": 1, "context_after": 1})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	matched := search("visible")
	if matched.StructuredContent["used"] != "literal" || len(matched.StructuredContent["matches"].([]map[string]any)) != 2 || matched.StructuredContent["root"] != nil {
		t.Fatal("ordinary project text not found")
	}
	data, e := json.Marshal(matched)
	if e != nil || strings.Contains(string(data), secret[:15]) || strings.Contains(string(data), "edit_tag") {
		t.Fatal("search exposed hidden source or edit authority")
	}
	if len(search(secret[:15]).StructuredContent["matches"].([]map[string]any)) != 0 {
		t.Fatal("partial credential query inferred the source secret")
	}
	put(t, w.Root, "binary", strings.Repeat("\x00", 32))
	s.Config.Set("maxScannedBytes", 16)
	if _, e := s.Call(context.Background(), scope, "search", map[string]any{"path": "binary", "query": "absent"}); e == nil {
		t.Fatal("binary bytes escaped search budget")
	}
}
