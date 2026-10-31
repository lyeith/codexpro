package batch

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type wrappedAndRawCaller struct{ wrapped, raw atomic.Int32 }

func (c *wrappedAndRawCaller) Call(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
	c.wrapped.Add(1)
	return wire.Result{}, wire.Errorf("wrong_wrapper", "MCP wrapper must not run inside batch")
}
func (c *wrappedAndRawCaller) CallBatchChild(_ context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	c.raw.Add(1)
	if scope.ParentActionID != "parent" || scope.RequestID != "request/batch/"+wire.String(args, "id", "") {
		return wire.Result{}, wire.Errorf("wrong_scope", "child lost enclosing action/request identity")
	}
	return wire.Result{Content: []wire.Content{{"type": "text", "text": "raw"}}, StructuredContent: map[string]any{"path": args["path"]}}, nil
}

func TestBatchUsesRegisteredRawHandlerInsteadOfOuterMCPWrapper(t *testing.T) {
	s, _, _ := fixture(t, nil)
	caller := &wrappedAndRawCaller{}
	s.caller = caller
	r := invoke(t, s, map[string]any{"mode": "parallel", "operations": []any{
		op("a", "read", map[string]any{"id": "a", "path": "a"}), op("b", "read", map[string]any{"id": "b", "path": "b"}), op("c", "read", map[string]any{"id": "c", "path": "c"}),
	}})
	if r.IsError || caller.raw.Load() != 3 || caller.wrapped.Load() != 0 {
		t.Fatalf("batch did not use raw handlers: raw=%d wrapped=%d result=%#v", caller.raw.Load(), caller.wrapped.Load(), r)
	}
	for _, child := range rows(r) {
		data := child["structured"].(map[string]any)
		if _, ok := data["codexpro_tool"]; ok {
			t.Fatal("raw child acquired an MCP presentation tag")
		}
	}
}
