package files

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"time"
)

const pixelBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6ioAAAAAASUVORK5CYII="

func TestImportImageBytesHashMetadataAndMode(t *testing.T) {
	s, w, scope := fixture(t)
	s.Config.Set("importAllowLoopback", true)
	t.Setenv("TMPDIR", t.TempDir())
	png, e := base64.StdEncoding.DecodeString(pixelBase64)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = response.Write(png)
	}))
	defer server.Close()
	r := call(t, s, scope, "import_file", map[string]any{"file": map[string]any{"download_url": server.URL, "file_id": "fixture-file", "file_name": "original.png"}, "destination": "nested/pixel.png", "expected_sha256": SHABytes(png)})
	if r.StructuredContent["bytes"] != len(png) || r.StructuredContent["sha256"] != SHABytes(png) || r.StructuredContent["verified"] != true || r.StructuredContent["declared_mime_type"] != "image/png" || r.StructuredContent["detected_mime_type"] != "image/png" || r.StructuredContent["mime_type_status"] != "matched" || r.StructuredContent["overwritten"] != false {
		t.Fatal(r.StructuredContent)
	}
	got, e := os.ReadFile(filepath.Join(w.Root, "nested/pixel.png"))
	if e != nil || !bytes.Equal(got, png) {
		t.Fatal(e)
	}
	normal := filepath.Join(w.Root, "normal-mode")
	f, e := os.OpenFile(normal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	wantInfo, _ := os.Stat(normal)
	gotInfo, _ := os.Stat(filepath.Join(w.Root, "nested/pixel.png"))
	if wantInfo.Mode().Perm() != gotInfo.Mode().Perm() {
		t.Fatalf("import mode %o, normal mode %o", gotInfo.Mode().Perm(), wantInfo.Mode().Perm())
	}
	viewed := call(t, s, scope, "view_image", map[string]any{"path": "nested/pixel.png"})
	if viewed.Meta != nil || len(viewed.Content) != 2 || viewed.Content[1]["type"] != "image" || viewed.Content[1]["data"] != pixelBase64 {
		t.Fatal(viewed)
	}
}
func TestImportHashMismatchAndExclusiveDestinationLeaveBytesIntact(t *testing.T) {
	s, w, scope := fixture(t)
	s.Config.Set("importAllowLoopback", true)
	t.Setenv("TMPDIR", t.TempDir())
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		downloads.Add(1)
		_, _ = response.Write([]byte("downloaded"))
	}))
	defer server.Close()
	ref := map[string]any{"download_url": server.URL, "file_id": "fixture-file"}
	_, e := s.Call(context.Background(), scope, "import_file", map[string]any{"file": ref, "destination": "bad", "expected_sha256": strings.Repeat("0", 64)})
	if e == nil || e.Error() != "Attachment SHA-256 mismatch for bad." {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(w.Root, "bad")); !os.IsNotExist(e) {
		t.Fatal("hash mismatch wrote destination")
	}
	writeFixture(t, filepath.Join(w.Root, "existing"), "old")
	_, e = s.Call(context.Background(), scope, "import_file", map[string]any{"file": ref, "destination": "existing"})
	if e == nil || e.Error() != "Destination already exists and overwrite=false: existing" {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(w.Root, "existing"))
	if string(got) != "old" {
		t.Fatal("exclusive import overwrote old bytes")
	}
	r := call(t, s, scope, "import_file", map[string]any{"file": ref, "destination": "existing", "overwrite": true})
	if r.StructuredContent["overwritten"] != true || r.StructuredContent["verified"] != false || r.StructuredContent["file_name"] != nil {
		t.Fatal(r.StructuredContent)
	}
	if downloads.Load() != 3 {
		t.Fatal("import skipped required download/hash phase")
	}
	temps, _ := filepath.Glob(filepath.Join(os.TempDir(), "codexpro-import-*.bin"))
	if len(temps) != 0 {
		t.Fatal("temporary import files leaked", temps)
	}
}
func TestImportRedirectBoundsAndSizeCaps(t *testing.T) {
	s, _, scope := fixture(t)
	s.Config.Set("importAllowLoopback", true)
	s.Config.Set("maxImportBytes", 3)
	t.Setenv("TMPDIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			response.Header().Set("Location", "/ok")
			response.WriteHeader(302)
		case "/loop":
			response.Header().Set("Location", "/loop")
			response.WriteHeader(302)
		case "/missing-location":
			response.WriteHeader(302)
		case "/large-header":
			response.Header().Set("Content-Length", "100")
			response.WriteHeader(200)
		case "/large-stream":
			response.Header().Set("Trailer", "X-Fixture")
			response.WriteHeader(200)
			_, _ = response.Write([]byte("ab"))
			response.(http.Flusher).Flush()
			_, _ = response.Write([]byte("cde"))
		default:
			response.Header().Set("Content-Type", "application/octet-stream")
			_, _ = response.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	args := func(path, destination string) map[string]any {
		return map[string]any{"file": map[string]any{"download_url": server.URL + path, "file_id": "fixture"}, "destination": destination}
	}
	r := call(t, s, scope, "import_file", args("/redirect", "ok"))
	if r.StructuredContent["bytes"] != 2 {
		t.Fatal(r.StructuredContent)
	}
	for _, tc := range []struct{ path, message string }{{"/loop", "Attachment download exceeded redirect limit."}, {"/missing-location", "Attachment download redirected without a Location header."}, {"/large-header", "Attachment is too large. Limit: 3 bytes."}, {"/large-stream", "Attachment exceeded import size limit (3 bytes)."}} {
		_, e := s.Call(context.Background(), scope, "import_file", args(tc.path, "rejected"))
		if e == nil || e.Error() != tc.message {
			t.Fatalf("%s: %v", tc.path, e)
		}
	}
}
func TestImportMimeMismatchIsReported(t *testing.T) {
	s, _, scope := fixture(t)
	s.Config.Set("importAllowLoopback", true)
	t.Setenv("TMPDIR", t.TempDir())
	png, _ := base64.StdEncoding.DecodeString(pixelBase64)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) { _, _ = response.Write(png) }))
	defer server.Close()
	r := call(t, s, scope, "import_file", map[string]any{"file": map[string]any{"download_url": server.URL, "file_id": "fixture", "mime_type": "application/pdf"}, "destination": "declared.pdf"})
	if r.StructuredContent["mime_type_status"] != "mismatched" || r.StructuredContent["declared_mime_type"] != "application/pdf" || r.StructuredContent["detected_mime_type"] != "image/png" {
		t.Fatal(r.StructuredContent)
	}
}
func TestGlobalWriteLocksAcrossServiceInstances(t *testing.T) {
	s, w, scope := fixture(t)
	other, e := New(s.Config, s.Workspaces)
	if e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(w.Root, "a"), "old")
	release := s.LockPaths(filepath.Join(w.Root, "a"))
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, e := other.Call(context.Background(), scope, "write", map[string]any{"path": "a", "content": "new", "expected_sha256": SHA("old")})
		done <- e
	}()
	<-started
	select {
	case e := <-done:
		release()
		t.Fatalf("second service completed while first held lock: %v", e)
	case <-time.After(25 * time.Millisecond):
	}
	// A second service must acquire the same registry entry before modifying bytes.
	before, _ := os.ReadFile(filepath.Join(w.Root, "a"))
	if string(before) != "old" {
		release()
		t.Fatal("second service bypassed global lock")
	}
	release()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("write stayed blocked after lock release")
	}
	after, _ := os.ReadFile(filepath.Join(w.Root, "a"))
	if string(after) != "new" {
		t.Fatal(string(after))
	}
}
