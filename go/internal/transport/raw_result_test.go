package transport

import (
	"encoding/json"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRawResultSinglePassMarshalJSON(t *testing.T) {
	t.Run("wire.Result with complete and merged meta", func(t *testing.T) {
		res := &rawResult{
			ResultBase: mcp.ResultBase{
				Meta: mcp.Meta{
					"serverInfo": map[string]any{"name": "CodexPro"},
					"existing":   "should-not-overwrite",
				},
			},
			Value: wire.Result{
				Meta:              map[string]any{"existing": "from-tool"},
				Content:           []wire.Content{{"type": "text", "text": "hello"}},
				StructuredContent: map[string]any{"ok": true},
				IsError:           true,
			},
			complete: true,
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if decoded["resultType"] != "complete" || decoded["isError"] != true {
			t.Fatalf("unexpected envelope: %s", raw)
		}
		meta, _ := decoded["_meta"].(map[string]any)
		if meta["existing"] != "from-tool" || meta["serverInfo"] == nil {
			t.Fatalf("unexpected _meta: %v", meta)
		}
		if sc, _ := decoded["structuredContent"].(map[string]any); sc["ok"] != true {
			t.Fatalf("unexpected structuredContent: %v", sc)
		}
	})

	t.Run("map[string]any with complete and meta does not mutate input", func(t *testing.T) {
		input := map[string]any{"tools": []any{"a", "b"}}
		res := &rawResult{
			ResultBase: mcp.ResultBase{Meta: mcp.Meta{"serverInfo": "v1"}},
			Value:      input,
			complete:   true,
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		if _, mutated := input["resultType"]; mutated {
			t.Fatal("MarshalJSON mutated input map")
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if decoded["resultType"] != "complete" {
			t.Fatalf("missing resultType: %s", raw)
		}
		meta, _ := decoded["_meta"].(map[string]any)
		if meta["serverInfo"] != "v1" {
			t.Fatalf("unexpected _meta: %v", meta)
		}
	})
}
