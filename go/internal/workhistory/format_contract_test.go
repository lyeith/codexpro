package workhistory

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func normalizedWorkEnvelope(t *testing.T, value any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(jsonData(value), &out); err != nil {
		t.Fatal(err)
	}
	blocks, ok := out["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatal("work result lost its single text block")
	}
	block := blocks[0].(map[string]any)
	var textValue any
	if err := json.Unmarshal([]byte(block["text"].(string)), &textValue); err != nil {
		t.Fatal(err)
	}
	block["text"] = textValue
	return out
}

func workFormatDifference(expected, actual any, path string) string {
	if reflect.DeepEqual(expected, actual) {
		return ""
	}
	switch left := expected.(type) {
	case map[string]any:
		right, ok := actual.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: map versus %T", path, actual)
		}
		keys := make([]string, 0, len(left)+len(right))
		for key := range left {
			keys = append(keys, key)
		}
		for key := range right {
			if _, exists := left[key]; !exists {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			a, presentA := left[key]
			b, presentB := right[key]
			if presentA != presentB {
				return fmt.Sprintf("%s.%s: reference presence=%v candidate presence=%v", path, key, presentA, presentB)
			}
			if difference := workFormatDifference(a, b, path+"."+key); difference != "" {
				return difference
			}
		}
	case []any:
		right, ok := actual.([]any)
		if !ok || len(left) != len(right) {
			return fmt.Sprintf("%s: array shape differs", path)
		}
		for index := range left {
			if difference := workFormatDifference(left[index], right[index], fmt.Sprintf("%s[%d]", path, index)); difference != "" {
				return difference
			}
		}
	default:
		if _, ok := expected.(string); ok {
			return path + ": string value differs"
		}
		return fmt.Sprintf("%s: reference=%v candidate=%v", path, expected, actual)
	}
	return path + ": value differs"
}

func TestWorkFormatterMatchesPinnedPureSourceGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/work-format-goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceCommit string `json:"source_commit"`
		Cases        []struct {
			Name     string         `json:"name"`
			Value    map[string]any `json:"value"`
			MaxBytes int            `json:"max_bytes"`
			Offset   int            `json:"offset"`
			Limit    int            `json:"limit"`
			Expected map[string]any `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SourceCommit != "44bb76bfa671e4aad540a8976f0fa6e6d5c2507a" || len(fixture.Cases) != 5 {
		t.Fatal("unexpected formatter source capture")
	}
	for _, row := range fixture.Cases {
		t.Run(row.Name, func(t *testing.T) {
			before := string(jsonData(row.Value))
			service := &Service{options: Options{PacketBytes: row.MaxBytes}}
			result := service.result(row.Value, row.Offset, row.Limit)
			expected := normalizedWorkEnvelope(t, row.Expected)
			actual := normalizedWorkEnvelope(t, result)
			if difference := workFormatDifference(expected, actual, "$"); difference != "" {
				t.Fatal(difference)
			}
			if before != string(jsonData(row.Value)) {
				t.Fatal("formatting mutated the caller's stored DTO")
			}
		})
	}
}

func TestWorkUndefinedCountsKeysButNeverSerializesAsNullObject(t *testing.T) {
	value := map[string]any{"present": nil, "omitted": workUndefined, "array": []any{workUndefined, nil, "<&>"}}
	if got := string(jsonData(value)); got != `{"array":[null,null,"<&>"],"present":null}` {
		t.Fatalf("JavaScript JSON omission/array/HTML semantics changed: %s", got)
	}
	copy := workCopy(value)
	if !isWorkUndefined(copy["omitted"]) {
		t.Fatal("intermediate copy erased an undefined key before trimming")
	}
	result := wire.Result{Meta: map[string]any{}, Content: []wire.Content{{"type": "text", "text": "<&>"}}, StructuredContent: map[string]any{"text": "<&>"}}
	if got := string(jsonData(result)); got != `{"_meta":{},"content":[{"text":"<&>","type":"text"}],"structuredContent":{"text":"<&>"}}` {
		t.Fatalf("result size used an HTML-escaping custom marshaler: %s", got)
	}
}
