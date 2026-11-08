package files

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/wire"
)

const wantSourceLimitRecovery = "The limit applies to the whole source before line selection. start_line, end_line and max_bytes cannot raise it. Use a smaller source or another authorized method that supports this source size."

func assertSourceLimitError(t *testing.T, err error, limit int) wire.Result {
	t.Helper()
	var domain *wire.Error
	if !errors.As(err, &domain) || domain.Code != "file_too_large" {
		t.Fatalf("error = %v, want file_too_large", err)
	}
	if !strings.Contains(domain.Message, fmt.Sprintf("Limit: %d bytes.", limit)) {
		t.Fatalf("error did not preserve the caller's source limit: %q", domain.Message)
	}
	wantRecovery := map[string]any{"message": wantSourceLimitRecovery}
	if domain.Data["retry_unchanged"] != false || !reflect.DeepEqual(domain.Data["recovery"], wantRecovery) {
		t.Fatalf("source-limit recovery = %#v", domain.Data)
	}
	result := wire.ErrorResult(err)
	if !result.IsError || result.StructuredContent["error_code"] != "file_too_large" ||
		result.StructuredContent["retry_unchanged"] != false ||
		!reflect.DeepEqual(result.StructuredContent["recovery"], wantRecovery) {
		t.Fatalf("source-limit envelope = %#v", result)
	}
	if len(result.Content) != 1 || !strings.Contains(result.Content[0]["text"].(string), wantSourceLimitRecovery) {
		t.Fatalf("human-readable recovery = %#v", result.Content)
	}
	for _, key := range []string{"text", "sha256", "edit_tag", "path", "root", "start_line", "end_line"} {
		if _, ok := result.StructuredContent[key]; ok {
			t.Fatalf("failed read returned %s: %#v", key, result.StructuredContent)
		}
	}
	return result
}

func TestReadWholeSourceLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"full", map[string]any{"path": "large.txt"}},
		{"already ranged", map[string]any{"path": "large.txt", "start_line": 2, "end_line": 2, "max_bytes": 32}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, scope := fixture(t)
			s.Config.Set("maxReadBytes", 64)
			source := strings.Repeat("private source line\n", 14)
			path := filepath.Join(w.Root, "large.txt")
			writeFixture(t, path, source)
			result, err := s.Call(context.Background(), scope, "read", tc.args)
			if !reflect.DeepEqual(result, wire.Result{}) {
				t.Fatalf("refused read returned a partial result: %#v", result)
			}
			envelope := assertSourceLimitError(t, err, 256)
			if strings.Contains(envelope.Content[0]["text"].(string), "private source line") {
				t.Fatal("source contents leaked through the error")
			}
			_, err = s.Snapshots.Resolve(scope, path, Tag(source), source, "large.txt")
			code(t, err, "edit_tag_unknown")
		})
	}
}

func TestReadSourceExplicitLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	writeFixture(t, path, strings.Repeat("line\n", 8))
	source, err := ReadSource(path, 16)
	if source != nil {
		t.Fatalf("oversized shared source returned bytes: %q", source)
	}
	assertSourceLimitError(t, err, 16)
}

func TestReadAtSourceLimitStillPaginates(t *testing.T) {
	s, w, scope := fixture(t)
	s.Config.Set("maxReadBytes", 8)
	source := strings.Repeat("row\n", 8)
	path := filepath.Join(w.Root, "boundary.txt")
	writeFixture(t, path, source)
	result := call(t, s, scope, "read", map[string]any{"path": "boundary.txt", "max_bytes": 8})
	data := result.StructuredContent
	if data["bytes"] != 32 || data["text"] != "1 | row" || data["returned_bytes"] != 7 ||
		data["start_line"] != 1 || data["end_line"] != 1 ||
		data["has_more"] != true || data["next_start_line"] != 2 {
		t.Fatalf("admitted source did not preserve bounded pagination: %#v", data)
	}
	tag, ok := data["edit_tag"].(string)
	if !ok || tag == "" {
		t.Fatalf("successful read has no edit tag: %#v", data)
	}
	snapshot, err := s.Snapshots.Resolve(scope, path, tag, source, "boundary.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Seen, []SeenRange{{Start: 1, End: 1}}) {
		t.Fatalf("read authorized undisplayed lines: %#v", snapshot.Seen)
	}
}
