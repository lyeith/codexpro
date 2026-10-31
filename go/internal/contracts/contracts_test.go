package contracts

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFrozenDescriptorsOnlyApprovedRemovals(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../docs/go-rewrite/capture-contracts/tools-list-live.json")
	if e != nil {
		t.Fatal(e)
	}
	var source struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if e = json.Unmarshal(b, &source); e != nil {
		t.Fatal(e)
	}
	index := 0
	for _, raw := range source.Result.Tools {
		var tool map[string]any
		_ = json.Unmarshal(raw, &tool)
		if tool["name"] == "work_manage" || tool["name"] == "work_update" {
			continue
		}
		var actual map[string]any
		_ = json.Unmarshal(r.Tools[index].Raw, &actual)
		if !reflect.DeepEqual(tool, actual) {
			t.Errorf("descriptor drift for %s", tool["name"])
		}
		index++
	}
	if index != 28 || len(r.Tools) != 28 {
		t.Fatal("unexpected removal/addition")
	}
}
func TestValidationMatchesCapturedPrivateTypeScript(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("../../../docs/go-rewrite/evidence/private-validation.json")
	if e != nil {
		t.Fatal(e)
	}
	var evidence struct {
		Cases []struct {
			Case      string `json:"case"`
			Tool      string `json:"tool"`
			Arguments any    `json:"arguments"`
			Reference struct {
				Body struct {
					Result map[string]any `json:"result"`
				} `json:"body"`
			} `json:"reference"`
		} `json:"cases"`
	}
	if e = json.Unmarshal(b, &evidence); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, c := range evidence.Cases {
		if _, ok := c.Reference.Body.Result["structuredContent"]; ok {
			continue
		}
		t.Run(c.Case, func(t *testing.T) {
			_, e := r.Validate(c.Tool, c.Arguments)
			ve, ok := e.(*ValidationError)
			if !ok {
				t.Fatalf("expected validation failure, got %v", e)
			}
			encoded, _ := json.Marshal(ve.SDKResult())
			var actual map[string]any
			_ = json.Unmarshal(encoded, &actual)
			if !reflect.DeepEqual(actual, c.Reference.Body.Result) {
				t.Fatalf("actual %s; reference %v", encoded, c.Reference.Body.Result)
			}
		})
		count++
	}
	if count < 9 {
		t.Fatal("missing validation captures")
	}
}
func TestHiddenBashFieldAndUnknownStripping(t *testing.T) {
	r, e := New()
	if e != nil {
		t.Fatal(e)
	}
	args, e := r.Validate("bash", map[string]any{"workspace_id": "ws", "command": "true", "background": true, "unadvertised_unknown": "strip"})
	if e != nil {
		t.Fatal(e)
	}
	if args["background"] != true {
		t.Fatal("legacy hidden background lost")
	}
	if _, ok := args["unadvertised_unknown"]; ok {
		t.Fatal("unknown input retained")
	}
	_, e = r.Validate("bash", map[string]any{"workspace_id": "ws", "command": "true", "background": "yes"})
	ve, ok := e.(*ValidationError)
	if !ok || ve.SDK || !strings.Contains(ve.Error(), "background") {
		t.Fatalf("wrong hidden validation layer %v", e)
	}
}
