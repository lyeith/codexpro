package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lyeith/codexpro/go/internal/workspace"
)

func TestProjectReadSourceLimitPreservesUniformRefusal(t *testing.T) {
	f := newPersonalReadFixture(t)
	f.engine.Config.Set("maxReadBytes", 1024)
	limit := f.engine.Files.ScanLimit()
	const marker = "oversized-source-marker\n"
	oversized := "ok\n" + strings.Repeat(marker, limit/len(marker)+1)
	if len(oversized) <= limit {
		t.Fatal("fixture must exceed the whole-source limit")
	}
	for name, body := range map[string]string{
		"small.txt":     "ok\n",
		"oversized.txt": oversized,
	} {
		if err := os.WriteFile(filepath.Join(f.alpha, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	ctx := f.ctx("alpha")
	ws := workspace.IDForRoot(f.alpha)
	const message = "CodexProError: This tool, project or resource is unavailable to this registration."
	for _, tc := range []struct {
		name   string
		ranged bool
	}{
		{"full", false},
		{"already-ranged", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"workspace_id": ws, "path": "small.txt"}
			if tc.ranged {
				args["start_line"] = 1
				args["end_line"] = 1
				args["max_bytes"] = 1000
			}

			// Prove the same grant and page arguments reach an allowed read.
			read := f.call(t, ctx, "read", args, false)
			text, ok := read.StructuredContent["text"].(string)
			if !ok || !strings.Contains(text, "ok") {
				t.Fatal("small-file control did not return its source")
			}

			args["path"] = "missing.txt"
			missing := f.call(t, ctx, "read", args, true)
			if len(missing.StructuredContent) != 2 ||
				missing.StructuredContent["error_code"] != "access_denied" ||
				missing.StructuredContent["error"] != message ||
				len(missing.Content) != 1 || len(missing.Content[0]) != 2 ||
				missing.Content[0]["type"] != "text" || missing.Content[0]["text"] != message {
				t.Fatal("missing-path control did not return the generic access refusal")
			}

			args["path"] = "oversized.txt"
			refused := f.call(t, ctx, "read", args, true)
			// Exact envelope equality excludes size, limit, source, edit provenance
			// and recovery details. Ordinary scoped audit receipts remain allowed.
			if renderPersonalResult(t, refused) != renderPersonalResult(t, missing) {
				t.Fatal("oversized source was distinguishable from an unavailable project path")
			}
		})
	}
}
