package httpcompat

import (
	"io"
	"net/http"
	"strconv"
)

// PayloadTooLarge preserves Express's stable body-parser error envelope.
// The release development response additionally includes Node stack frames;
// omitting those runtime diagnostics requires the recorded coordinator ruling.
// It does not replace validation/domain errors from usage or inbox handlers.
func PayloadTooLarge(w http.ResponseWriter, r *http.Request) {
	body := "<!DOCTYPE html>\n" +
		"<html lang=\"en\">\n" +
		"<head>\n" +
		"<meta charset=\"utf-8\">\n" +
		"<title>Error</title>\n" +
		"</head>\n" +
		"<body>\n" +
		"<pre>PayloadTooLargeError: request entity too large</pre>\n" +
		"</body>\n" +
		"</html>\n"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
}
