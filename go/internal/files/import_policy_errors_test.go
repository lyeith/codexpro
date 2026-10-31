package files

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func TestImportHTTPFailuresPreserveSourceDomainClass(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	for _, test := range []struct {
		name, path, message string
		requests            int32
	}{
		{"not-found", "/not-found", "Attachment download failed with HTTP 404.", 1},
		{"redirect-without-location", "/no-location", "Attachment download redirected without a Location header.", 1},
		{"redirect-loop", "/loop", "Attachment download exceeded redirect limit.", 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scope, root := replayErrorService(t)
			service.Config.Set("importAllowLoopback", true)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != test.path || r.Header.Get("Authorization") != "" {
					t.Errorf("HTTP failure fixture received unexpected path/credentials")
				}
				switch test.path {
				case "/not-found":
					w.WriteHeader(http.StatusNotFound)
				case "/no-location":
					w.WriteHeader(http.StatusFound)
				case "/loop":
					w.Header().Set("Location", "/loop")
					w.WriteHeader(http.StatusFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := service.Call(ctx, scope, "import_file", map[string]any{
				"workspace_id": "ws_error_contract", "destination": "rejected.bin",
				"file": map[string]any{"download_url": server.URL + test.path, "file_id": "http-failure-fixture"},
			})
			assertDomainError(t, err, test.message)
			if requests.Load() != test.requests {
				t.Fatalf("source redirect/fetch bound changed: got %d want %d", requests.Load(), test.requests)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("HTTP failure created a destination: %v %v", entries, readErr)
			}
			leftovers, globErr := filepath.Glob(filepath.Join(temporary, "codexpro-import-*"))
			if globErr != nil || len(leftovers) != 0 {
				t.Fatalf("HTTP failure leaked a temporary download: %v %v", leftovers, globErr)
			}
		})
	}
}

func TestImportReferenceFailuresPreserveSourceDomainClass(t *testing.T) {
	valid := map[string]any{"download_url": "https://files.oaiusercontent.com/never-fetched", "file_id": "fixture"}
	for _, test := range []struct {
		name, destination, message string
		file                       any
	}{
		{"string", "never.bin", "Unsupported attachment reference. ChatGPT must pass an Apps SDK file object with download_url and file_id via openai/fileParams. A bare string file id or URL is not enough.", "file-fixture"},
		{"null", "never.bin", "Unsupported attachment reference. Expected { download_url, file_id, mime_type?, file_name? }.", nil},
		{"array", "never.bin", "Unsupported attachment reference. Expected { download_url, file_id, mime_type?, file_name? }.", []any{}},
		{"number", "never.bin", "Unsupported attachment reference. Expected { download_url, file_id, mime_type?, file_name? }.", 7},
		{"missing-destination", " \t\r\n", "destination is required.", valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, scope, root := replayErrorService(t)
			_, err := service.Call(context.Background(), scope, "import_file", map[string]any{
				"workspace_id": "ws_error_contract", "destination": test.destination, "file": test.file,
			})
			assertDomainError(t, err, test.message)
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid reference created a file: %v %v", entries, readErr)
			}
		})
	}
}

func TestImportURLPolicyFailuresPreserveSourceDomainClass(t *testing.T) {
	for _, test := range []struct{ name, raw, message string }{
		{"credential", "https://fixture-user:fixture-pass@files.oaiusercontent.com/file", "Attachment download_url must not include credentials."},
		{"loopback", "https://127.0.0.1/file", "Attachment download_url points to a blocked host."},
		{"private-address", "https://127.0.0.2/file", "Attachment download_url resolves to a blocked address."},
		{"scheme", "http://127.0.0.1/file", "Attachment download_url must use HTTPS."},
		{"missing-host", "https:", "Attachment download_url is missing a hostname."},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, root := replayErrorService(t)
			service.Config.Set("importAllowedHosts", "127.0.0.2")
			parsed, err := url.Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			// All cases fail before DNS or a socket. The helper-level missing-host
			// case classifies that construction; raw invalid URL calls below still
			// preserve their earlier native parsing failure.
			err = service.safeImportURL(context.Background(), parsed, false)
			assertDomainError(t, err, test.message)
			entries, readErr := os.ReadDir(root)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("URL policy check created a file: %v %v", entries, readErr)
			}
		})
	}
}

func TestImportInvalidURLParsingRemainsNative(t *testing.T) {
	service, scope, root := replayErrorService(t)
	_, err := service.Call(context.Background(), scope, "import_file", map[string]any{
		"workspace_id": "ws_error_contract", "destination": "invalid.bin",
		"file": map[string]any{"download_url": "not a URL", "file_id": "fixture"},
	})
	var domain *wire.Error
	if err == nil || errors.As(err, &domain) {
		t.Fatalf("native URL parse error changed class: %T %v", err, err)
	}
	result := wire.ErrorResult(err)
	if !result.IsError || len(result.Content) != 1 || len(result.StructuredContent) != 1 ||
		result.Content[0]["type"] != "text" || result.Content[0]["text"] != "TypeError: Invalid URL" ||
		result.StructuredContent["error"] != "TypeError: Invalid URL" {
		t.Fatalf("native parse error lost its exact source name/envelope: %+v", result)
	}
	if _, exists := result.StructuredContent["error_code"]; exists {
		t.Fatalf("native parse error acquired a domain code: %+v", result)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("invalid URL created a file: %v %v", entries, readErr)
	}
}
