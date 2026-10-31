package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

// These failures arise after a real bounded download, so rejection cannot be
// mistaken for the existing URL/input validation coverage. The source throws
// uncoded CodexProError at importOps.ts240,257,372,380 and300/310.
func TestDownloadedImportDomainErrorsPreserveWireClassAndDestination(t *testing.T) {
	const limit = 1024
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	for _, test := range []struct {
		name, route, existing, message string
		wrongHash                      bool
	}{
		{"existing-destination", "/body", "keep current bytes\n", "Destination already exists and overwrite=false: downloaded.bin", false},
		{"hash-mismatch", "/body", "", "Attachment SHA-256 mismatch for downloaded.bin.", true},
		{"advertised-overflow", "/length", "", "Attachment is too large. Limit: 1024 bytes.", false},
		{"streamed-overflow", "/stream", "", "Attachment exceeded import size limit (1024 bytes).", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scope, root := replayErrorService(t)
			service.Config.Set("importAllowLoopback", true)
			service.Config.Set("maxImportBytes", limit)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != test.route || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected import request method/path/credential")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				switch r.URL.Path {
				case "/length":
					w.Header().Set("Content-Length", fmt.Sprint(limit+1))
					_, _ = w.Write(bytes.Repeat([]byte{'x'}, limit+1))
				case "/stream":
					// Flushing headers prevents net/http from deriving a length;
					// the reader must enforce its streaming limit independently.
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					_, _ = w.Write(bytes.Repeat([]byte{'y'}, limit+1))
				default:
					_, _ = w.Write([]byte("downloaded body\n"))
				}
			}))
			defer server.Close()
			path := filepath.Join(root, "downloaded.bin")
			if test.existing != "" {
				if err := os.WriteFile(path, []byte(test.existing), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := map[string]any{"workspace_id": "ws_error_contract", "destination": "downloaded.bin",
				"file": map[string]any{"download_url": server.URL + test.route, "file_id": "owned-native-fixture"}}
			if test.wrongHash {
				args["expected_sha256"] = strings.Repeat("0", 64)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := service.Call(ctx, scope, "import_file", args)
			assertDomainError(t, err, test.message)
			if requests.Load() != 1 {
				t.Fatalf("failure must follow exactly one actual fixture fetch: %d", requests.Load())
			}
			current, readErr := os.ReadFile(path)
			if test.existing == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("rejected import created a destination: %q %v", current, readErr)
				}
			} else if readErr != nil || string(current) != test.existing {
				t.Fatalf("rejected import changed current bytes: %q %v", current, readErr)
			}
			leftovers, globErr := filepath.Glob(filepath.Join(temporary, "codexpro-import-*"))
			if globErr != nil || len(leftovers) != 0 {
				t.Fatalf("rejected import left temporary downloads: %v %v", leftovers, globErr)
			}
		})
	}
}

func TestDownloadedImportNativeConnectionFailureKeepsNativeErrorClass(t *testing.T) {
	service, scope, root := replayErrorService(t)
	service.Config.Set("importAllowLoopback", true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("private disconnect fixture failed: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := service.Call(ctx, scope, "import_file", map[string]any{
		"workspace_id": "ws_error_contract", "destination": "missing.bin",
		"file": map[string]any{"download_url": server.URL, "file_id": "connection-failure-fixture"},
	})
	var domain *wire.Error
	if err == nil || errors.As(err, &domain) {
		t.Fatalf("native network failure was converted to a domain error: %T %v", err, err)
	}
	result := wire.ErrorResult(err)
	if !result.IsError || !strings.HasPrefix(result.Content[0]["text"].(string), "Error: ") {
		t.Fatalf("native network failure changed wire class: %+v", result)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed connection changed the fixture: %v %v", entries, readErr)
	}
}
