package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestNativeErrorResultPreservesExplicitSourceName(t *testing.T) {
	for _, test := range []struct {
		name, message string
		err           error
	}{
		{"typed", "TypeError: Invalid URL", NativeErrorf("TypeError", "Invalid %s", "URL")},
		{"wrapped", "TypeError: import failed: Invalid URL", fmt.Errorf("import failed: %w", NativeErrorf("TypeError", "Invalid URL"))},
		{"ordinary", "Error: ordinary native failure", errors.New("ordinary native failure")},
		{"wrapped-ordinary", "Error: import failed: ordinary native failure", fmt.Errorf("import failed: %w", errors.New("ordinary native failure"))},
		{"empty-name", "Error: unnamed native failure", NativeErrorf("", "unnamed native failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := ErrorResult(test.err)
			want := Result{
				Content:           []Content{{"type": "text", "text": test.message}},
				StructuredContent: map[string]any{"error": test.message},
				IsError:           true,
			}
			if !reflect.DeepEqual(result, want) {
				t.Fatalf("native error envelope changed: got %#v want %#v", result, want)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			if err := json.Unmarshal(encoded, &envelope); err != nil {
				t.Fatal(err)
			}
			wantEnvelope := map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": test.message}},
				"structuredContent": map[string]any{"error": test.message},
				"isError":           true,
			}
			if !reflect.DeepEqual(envelope, wantEnvelope) {
				t.Fatalf("native error acquired metadata/code or changed wire fields: %s", encoded)
			}
		})
	}
}

func TestNativeErrorResultKeepsCodexProErrorPrecedence(t *testing.T) {
	domain := &Error{Code: "SOURCE_CODE", Message: "source domain failure", Data: map[string]any{
		"details": map[string]any{"path": "fixture.txt"},
	}}
	// errors.As can find both classes in a joined error. The established domain
	// branch still wins, including when a native error appears first.
	err := fmt.Errorf("outer context: %w", errors.Join(NativeErrorf("TypeError", "Invalid URL"), domain))
	result := ErrorResult(err)
	want := Result{
		Content:           []Content{{"type": "text", "text": "CodexProError: source domain failure"}},
		StructuredContent: map[string]any{"error": "CodexProError: source domain failure", "error_code": "SOURCE_CODE", "path": "fixture.txt"},
		IsError:           true,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("typed native name displaced the existing domain envelope: got %#v want %#v", result, want)
	}
}
