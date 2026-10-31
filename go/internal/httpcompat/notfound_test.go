package httpcompat

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func errorPage(message string) string {
	return "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>" + message + "</pre>\n</body>\n</html>\n"
}

func TestNotFoundMatchesCapturedExpressMethodsAndHeaders(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"GET", "/sp6-missing-route"},
		{"POST", "/activity/live"},
		{"POST", "/healthz"},
		{"HEAD", "/healthz"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, "http://127.0.0.1:7210"+tc.path+"?codexpro_token=[REDACTED_SECRET]", nil)
			response := httptest.NewRecorder()
			NotFound(response, request)
			expected := errorPage("Cannot " + tc.method + " " + tc.path)
			if response.Code != 404 {
				t.Fatalf("status = %d", response.Code)
			}
			for key, value := range map[string]string{
				"Content-Type":            "text/html; charset=utf-8",
				"Content-Security-Policy": "default-src 'none'",
				"X-Content-Type-Options":  "nosniff",
				"Content-Length":          strconv.Itoa(len(expected)),
			} {
				if got := response.Header().Get(key); got != value {
					t.Fatalf("%s = %q; want %q", key, got, value)
				}
			}
			body := response.Body.String()
			if tc.method == "HEAD" {
				if body != "" {
					t.Fatal("HEAD emitted an entity body")
				}
			} else if body != expected {
				t.Fatalf("body = %q; want %q", body, expected)
			}
			if strings.Contains(body, "[REDACTED_SECRET]") || strings.Contains(body, "codexpro_token") {
				t.Fatal("fallback reflected authenticated query data")
			}
		})
	}
}

func TestNotFoundEscapesMethodAndPreservesEscapedPath(t *testing.T) {
	request := &http.Request{
		Method: "GET&<\"'",
		URL:    &url.URL{Path: "/a<b>\"'&/already/encoded", RawPath: "/a%3Cb%3E%22'&/already%2fencoded", RawQuery: "q=must-not-appear"},
	}
	response := httptest.NewRecorder()
	NotFound(response, request)
	expected := errorPage("Cannot GET&amp;&lt;&quot;&#39; /a%3Cb%3E%22&#39;&amp;/already%2fencoded")
	if got := response.Body.String(); got != expected {
		t.Fatalf("body = %q; want %q", got, expected)
	}
}
