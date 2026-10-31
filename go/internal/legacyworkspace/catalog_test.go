package legacyworkspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogShapeRetainsOfflineRootsAndSourceBounds(t *testing.T) {
	base := map[string]any{"version": 1, "projects": []any{map[string]any{"id": "fixture", "root": "/unavailable/recorded/repository"}}}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := sourceCatalog(raw)
	if err != nil || len(ids) != 1 || ids[0] != "fixture" {
		t.Fatalf("offline archive shape: %v %v", ids, err)
	}
	for _, test := range []struct {
		field string
		value any
		valid bool
	}{
		{"label", strings.Repeat("😀", 60), true}, {"label", strings.Repeat("😀", 61), false},
		{"label", nil, false}, {"label", "", false}, {"label", "\ufeffok\ufeff", true},
		{"label", "line\n", false}, {"baseRef", " main ", true}, {"baseRef", "-bad", false},
		{"baseRef", "has space", false}, {"baseRef", nil, false}, {"baseRef", "", false},
		{"maxWorktrees", 1, true}, {"maxWorktrees", 512, true}, {"maxWorktrees", 513, false},
		{"maxWorktrees", 1.5, false}, {"maxWorktrees", "1", false}, {"maxWorktrees", nil, false},
		{"root", nil, false}, {"root", "", false}, {"unknown", "value", false},
	} {
		var value map[string]any
		if err = json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		project := value["projects"].([]any)[0].(map[string]any)
		project[test.field] = test.value
		body, _ := json.Marshal(value)
		_, err = sourceCatalog(body)
		if (err == nil) != test.valid {
			t.Errorf("%s=%v: valid=%v error=%v", test.field, test.value, test.valid, err)
		}
	}
	for _, test := range []struct {
		field string
		value any
		valid bool
	}{
		{"creationRoots", nil, true}, {"creationRoots", []any{}, true}, {"creationRoots", map[string]any{}, false},
		{"defaultProject", "fixture", true}, {"defaultProject", nil, false}, {"defaultProject", "", false},
		{"version", "1", false}, {"unknown", "value", false},
	} {
		var value map[string]any
		_ = json.Unmarshal(raw, &value)
		value[test.field] = test.value
		body, _ := json.Marshal(value)
		_, err = sourceCatalog(body)
		if (err == nil) != test.valid {
			t.Errorf("%s=%v: valid=%v error=%v", test.field, test.value, test.valid, err)
		}
	}
}
