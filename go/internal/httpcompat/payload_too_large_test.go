package httpcompat

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestPayloadTooLargePreservesStableExpressEnvelope(t *testing.T) {
	want := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<title>Error</title>\n</head>\n<body>\n<pre>PayloadTooLargeError: request entity too large</pre>\n</body>\n</html>\n"
	for _, method := range []string{http.MethodPost, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(method, "/usage/v1/pro/submissions?fixture=not-reflected", nil)
			PayloadTooLarge(recorder, request)
			if recorder.Code != http.StatusRequestEntityTooLarge ||
				recorder.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
				recorder.Header().Get("Content-Security-Policy") != "default-src 'none'" ||
				recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
				recorder.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
				t.Fatalf("stable HTTP error contract changed: status=%d headers=%v", recorder.Code, recorder.Header())
			}
			body := want
			if method == http.MethodHead {
				body = ""
			}
			if recorder.Body.String() != body {
				t.Fatalf("error envelope changed: %q", recorder.Body.String())
			}
		})
	}
}
