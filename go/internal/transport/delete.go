package transport

import "net/http"

// The release-0.31 TypeScript transport replies 200 with an empty body to a
// successful session DELETE. The pinned Go SDK replies 204 for that operation.
type legacyDeleteResponse struct{ http.ResponseWriter }

func (w *legacyDeleteResponse) WriteHeader(status int) {
	if status == http.StatusNoContent {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *legacyDeleteResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
