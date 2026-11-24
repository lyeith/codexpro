package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestHTTPRawArgumentsPreserveValidation(t *testing.T) {
	var calls atomic.Int32
	received := make(chan map[string]any, 1)
	_, remote, credential, _ := newHTTPFixture(t, func(_ context.Context, _ wire.Scope, name string, args map[string]any) (wire.Result, error) {
		calls.Add(1)
		received <- args
		if name == "bash" {
			return wire.Result{}, wire.Errorf("fixture_business", "business validation still owns this argument")
		}
		return wire.Success(name, "Fixture", nil, "ok"), nil
	})
	session := initialize(t, remote, credential, "2025-11-25")
	cases := []struct {
		name      string
		params    string
		status    int
		domain    bool
		wantArgs  map[string]any
		errorText string
		rpcError  bool
	}{
		{"omitted", `{"name":"list_projects"}`, 200, true, nil, "", false},
		{"null", `{"name":"list_projects","arguments":null}`, 200, true, nil, "", false},
		{"empty-object", `{"name":"list_projects","arguments":{}}`, 200, true, map[string]any{}, "", false},
		{"original-map-and-numbers", `{"name":"list_projects","arguments":{"extra":1.5,"nested":{"n":2}}}`, 200, true, map[string]any{"extra": float64(1.5), "nested": map[string]any{"n": float64(2)}}, "", false},
		{"array", `{"name":"list_projects","arguments":[]}`, 200, false, nil, "arguments must be an object", false},
		{"string", `{"name":"list_projects","arguments":"input"}`, 200, false, nil, "arguments must be an object", false},
		{"number", `{"name":"list_projects","arguments":7}`, 200, false, nil, "arguments must be an object", false},
		{"boolean", `{"name":"list_projects","arguments":false}`, 200, false, nil, "arguments must be an object", false},
		{"unknown-tool", `{"name":"not_a_tool","arguments":{}}`, 200, false, nil, "not found", false},
		{"invalid-agent", `{"name":"list_projects","arguments":{"agent":"bad label"}}`, 200, false, nil, "agent must contain", false},
		{"missing-field", `{"name":"read","arguments":{"workspace_id":"fixture"}}`, 200, false, nil, "missing required fields", false},
		{"business-validation", `{"name":"bash","arguments":{"workspace_id":"fixture","command":"true","background":"wrong"}}`, 200, true, map[string]any{"workspace_id": "fixture", "command": "true", "background": "wrong"}, "business validation still owns", false},
		{"null-params", `null`, 200, false, nil, "", true},
		{"malformed", `{"name":"read","arguments":]}`, 400, false, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":` + tc.params + "}"
			status, _, data := request(t, remote, http.MethodPost, "/mcp", body, session, credential, nil)
			if status != tc.status {
				t.Fatalf("status=%d, want %d: %s", status, tc.status, data)
			}
			wantCalls := int32(0)
			if tc.domain {
				wantCalls = 1
				select {
				case args := <-received:
					if !reflect.DeepEqual(args, tc.wantArgs) {
						t.Fatalf("domain arguments=%#v, want %#v", args, tc.wantArgs)
					}
				default:
					t.Fatal("accepted input did not reach domain caller")
				}
			}
			if calls.Load()-before != wantCalls {
				t.Fatalf("domain calls=%d, want %d", calls.Load()-before, wantCalls)
			}
			reply := decodeRPC(t, data)
			if tc.rpcError {
				if reply["error"] == nil || reply["result"] != nil {
					t.Fatalf("malformed request changed RPC error shape: %#v", reply)
				}
				return
			}
			result, ok := reply["result"].(map[string]any)
			if !ok {
				t.Fatalf("missing tool result: %#v", reply)
			}
			if tc.errorText == "" {
				if _, present := result["isError"]; present {
					t.Fatal("successful result acquired isError")
				}
				return
			}
			if result["isError"] != true || !bytes.Contains(data, []byte(tc.errorText)) {
				t.Fatalf("wrong error result: %s", data)
			}
			if !tc.domain {
				if _, exists := result["_meta"]; exists {
					t.Fatal("SDK validation error acquired metadata")
				}
				if _, exists := result["structuredContent"]; exists {
					t.Fatal("SDK validation error acquired business structured content")
				}
			} else if result["structuredContent"] == nil {
				t.Fatal("business error lost structured content")
			}
		})
	}
}

func TestHTTPRawResultsPreserveEnvelopesAcrossProtocols(t *testing.T) {
	results := map[string]wire.Result{
		"nil-content":   {},
		"empty-content": {Meta: map[string]any{}, Content: []wire.Content{}, StructuredContent: map[string]any{}},
		"rich": {
			Meta: map[string]any{"extension": map[string]any{"enabled": false}},
			Content: []wire.Content{
				{"type": "text", "text": "<fixture>\ntext", "annotations": map[string]any{"priority": 0.5}, "extension": true},
				{"type": "image", "data": "AA==", "mimeType": "image/png"},
				{"type": "future-content", "extension": map[string]any{"value": 3}},
			},
			StructuredContent: map[string]any{"nested": map[string]any{"empty": []any{}, "value": 0}},
			DashboardMetadata: map[string]any{"private": "must-not-reach-the-wire"},
		},
		"tool-error": wire.ErrorResult(wire.Errorf("fixture_error", "fixture failure")),
	}
	_, remote, credential, _ := newHTTPFixture(t, func(_ context.Context, _ wire.Scope, _ string, args map[string]any) (wire.Result, error) {
		return results[args["path"].(string)], nil
	})
	for _, protocol := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"} {
		t.Run(protocol, func(t *testing.T) {
			session := initialize(t, remote, credential, protocol)
			for name, expected := range results {
				t.Run(name, func(t *testing.T) {
					params, _ := json.Marshal(map[string]any{"name": "read", "arguments": map[string]any{"workspace_id": "fixture", "path": name}})
					body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":` + string(params) + "}"
					status, _, data := request(t, remote, http.MethodPost, "/mcp", body, session, credential, map[string]string{"Mcp-Protocol-Version": protocol})
					if status != http.StatusOK || strings.Contains(string(data), "must-not-reach-the-wire") {
						t.Fatalf("result was refused or leaked private data: status=%d", status)
					}
					reply := decodeRPC(t, data)
					raw, err := json.Marshal(expected)
					if err != nil {
						t.Fatal(err)
					}
					var want any
					if err := json.Unmarshal(raw, &want); err != nil {
						t.Fatal(err)
					}
					if reply["error"] != nil || !reflect.DeepEqual(reply["result"], want) {
						t.Fatalf("raw result changed: got %#v, want %#v", reply, want)
					}
				})
			}
		})
	}
}
