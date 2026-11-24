//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/contracts"
	"github.com/lyeith/codexpro/go/internal/plansaccess"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// BenchmarkHTTPTransport exercises the production Bootstrap -> Server -> SDK
// path. All policy, catalog and session setup is outside the timed loop. The
// domain caller returns immutable synthetic data; it performs no file or shell
// operation, and there is no audit, mail or background-job service in this fixture.
func BenchmarkHTTPTransport(b *testing.B) {
	smallArguments := map[string]any{"workspace_id": "fixture", "path": "benchmark.txt"}
	largeArguments := map[string]any{
		"workspace_id": "fixture", "path": "benchmark.txt",
		"content": strings.Repeat("x", 64*1024),
	}
	cases := []struct {
		name       string
		named      bool
		method     string
		tool       string
		arguments  map[string]any
		resultSize int
		parallel   bool
	}{
		{"NamedList", true, "tools/list", "", nil, 0, false},
		{"NamedSmallCall", true, "tools/call", "read", smallArguments, 1024, false},
		{"NamedLargeArguments64KiB", true, "tools/call", "write", largeArguments, 1024, false},
		{"NamedLargeResult64KiB", true, "tools/call", "read", smallArguments, 64 * 1024, false},
		{"LegacySmallCall", false, "tools/call", "read", smallArguments, 1024, false},
		{"NamedConcurrentSession", true, "tools/call", "read", smallArguments, 1024, true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			text := strings.Repeat("r", tc.resultSize)
			result := wire.Success(tc.tool, "Transport benchmark", map[string]any{"payload_bytes": tc.resultSize}, text)
			f := newHTTPBenchmarkFixture(b, tc.named, result)
			params := map[string]any{}
			if tc.method == "tools/call" {
				params["name"], params["arguments"] = tc.tool, tc.arguments
			}
			payload, err := json.Marshal(map[string]any{"method": tc.method, "params": params})
			if err != nil {
				b.Fatal(err)
			}
			// Prefix a fresh numeric ID without re-encoding the fixed arguments.
			// IDs are unique across concurrent requests on this one session.
			f.suffix = append([]byte(","), payload[1:]...)
			warm := f.call()
			var expected any = result
			if tc.method == "tools/list" {
				expected = f.server.Registry.List()
			}
			assertHTTPBenchmarkResult(b, warm, expected)
			responseBytes := warm.Body.Len()

			b.ReportAllocs()
			b.ResetTimer()
			if tc.parallel {
				var failed atomic.Bool
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						response := f.call()
						if !httpBenchmarkSucceeded(response) {
							failed.Store(true)
						}
					}
				})
				b.StopTimer()
				if failed.Load() {
					b.Fatal("concurrent HTTP benchmark received an unsuccessful RPC response")
				}
			} else {
				for i := 0; i < b.N; i++ {
					response := f.call()
					if !httpBenchmarkSucceeded(response) {
						b.Fatalf("HTTP benchmark received unsuccessful response: status=%d body=%q", response.Code, response.Body.Bytes())
					}
				}
				b.StopTimer()
			}
			b.ReportMetric(float64(len(payload)), "params_bytes")
			b.ReportMetric(float64(responseBytes), "response_bytes")
		})
	}
}

type httpBenchmarkFixture struct {
	bootstrap *Bootstrap
	server    *Server
	token     string
	session   string
	suffix    []byte
	sequence  atomic.Uint64
}

func newHTTPBenchmarkFixture(b *testing.B, named bool, result wire.Result) *httpBenchmarkFixture {
	b.Helper()
	cfg := config.New(b.TempDir())
	var token string
	if named {
		// This deterministic token exists only in the benchmark's private
		// temporary policy. It is unrelated to any serving credential.
		seed := sha256.Sum256([]byte("synthetic transport benchmark credential"))
		token = "pa1_" + base64.RawURLEncoding.EncodeToString(seed[:])
		digest := sha256.Sum256([]byte(token))
		policy := &plansaccess.Policy{
			Version: 1,
			Principals: []plansaccess.Principal{
				{ID: "benchmark", Type: "system"},
			},
			Agents: []plansaccess.Agent{
				{ID: "benchmark-worker", PrincipalID: "benchmark", Kind: "leaf", Address: "system/benchmark-worker"},
			},
			Credentials: []plansaccess.Credential{
				{ID: "benchmark-device", Device: "benchmark", PrincipalID: "benchmark", AgentID: "benchmark-worker", Audience: "codexpro", SHA256: hex.EncodeToString(digest[:])},
			},
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			b.Fatal(err)
		}
		path := filepath.Join(b.TempDir(), "benchmark-policy.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			b.Fatal(err)
		}
		cfg.Set("principalsFile", path)
		cfg.Set("legacyBridge", false)
	}
	reader := plansaccess.NewReader()
	bootstrap, err := NewBootstrap(cfg, http.NotFoundHandler(), reader)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = bootstrap.Close() })
	registry, err := contracts.New()
	if err != nil {
		b.Fatal(err)
	}
	caller := testCaller(func(context.Context, wire.Scope, string, map[string]any) (wire.Result, error) {
		return result, nil
	})
	server, err := New(cfg, registry, caller, "benchmark-principal", nil, reader)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = server.Close() })
	if err := bootstrap.Publish(server); err != nil {
		b.Fatal(err)
	}
	if !named {
		token = server.credential
	}
	f := &httpBenchmarkFixture{bootstrap: bootstrap, server: server, token: token}
	response := f.post([]byte(`{"jsonrpc":"2.0","id":"initialize","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"transport-benchmark","version":"1"}}}`))
	if response.Code != http.StatusOK || !sessionPattern.MatchString(response.Header().Get("Mcp-Session-Id")) {
		b.Fatalf("benchmark initialize failed: status=%d body=%q", response.Code, response.Body.Bytes())
	}
	f.session = response.Header().Get("Mcp-Session-Id")
	response = f.post([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if response.Code != http.StatusAccepted {
		b.Fatalf("benchmark initialized notification failed: status=%d", response.Code)
	}
	return f
}

func (f *httpBenchmarkFixture) call() *httptest.ResponseRecorder {
	body := make([]byte, 0, len(f.suffix)+48)
	body = append(body, `{"jsonrpc":"2.0","id":`...)
	body = strconv.AppendUint(body, f.sequence.Add(1), 10)
	body = append(body, f.suffix...)
	return f.post(body)
}

func (f *httpBenchmarkFixture) post(body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if f.session != "" {
		req.Header.Set("Mcp-Session-Id", f.session)
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	}
	response := httptest.NewRecorder()
	f.bootstrap.ServeHTTP(response, req)
	return response
}

func httpBenchmarkSucceeded(response *httptest.ResponseRecorder) bool {
	return response.Code == http.StatusOK &&
		bytes.Contains(response.Body.Bytes(), []byte(`"result":`)) &&
		!bytes.Contains(response.Body.Bytes(), []byte(`"isError":true`))
}

func assertHTTPBenchmarkResult(b *testing.B, response *httptest.ResponseRecorder, expected any) {
	b.Helper()
	if !httpBenchmarkSucceeded(response) {
		b.Fatalf("benchmark warm-up failed: status=%d body=%q", response.Code, response.Body.Bytes())
	}
	data := response.Body.Bytes()
	if !json.Valid(data) {
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				data = line[len("data: "):]
				break
			}
		}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		b.Fatal(err)
	}
	want, err := json.Marshal(expected)
	if err != nil {
		b.Fatal(err)
	}
	// The raw-result contract preserves the same JSON value. Compare its
	// canonical form once, outside timing, rather than benchmark response parsing.
	var gotValue, wantValue any
	if err := json.Unmarshal(envelope.Result, &gotValue); err != nil {
		b.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		b.Fatal(err)
	}
	got, _ := json.Marshal(gotValue)
	want, _ = json.Marshal(wantValue)
	if !bytes.Equal(got, want) {
		b.Fatalf("benchmark warm-up changed result value: got %d bytes, want %d", len(got), len(want))
	}
}
