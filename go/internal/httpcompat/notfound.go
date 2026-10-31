// Package httpcompat preserves observable HTTP fallback responses from the
// release TypeScript server without depending on its Express runtime.
package httpcompat

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

var htmlEscapes = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
	"'", "&#39;",
)

// NotFound matches Express finalhandler's route/method fallback. Query values
// are deliberately absent, including dashboard authentication parameters.
// Domain-specific 404 JSON responses should keep their existing handlers.
func NotFound(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	body := "<!DOCTYPE html>\n" +
		"<html lang=\"en\">\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>Error</title>\n" +
		"</head>\n" +
		"<body>\n" +
		"<pre>Cannot " + htmlEscapes.Replace(r.Method) + " " + htmlEscapes.Replace(path) + "</pre>\n" +
		"</body>\n" +
		"</html>\n"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}
