package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type HTTPOptions struct {
	Directory     string
	Clock         Clock
	ProjectExists func(string) bool
	Principal     func(*http.Request) string
	SameOrigin    func(http.ResponseWriter, *http.Request) bool
	Projects      func() []wire.Project
}
type HTTPHandler struct {
	options HTTPOptions
	mu      sync.Mutex
	store   *Store
}

func RegisterRoutes(mux *http.ServeMux, options HTTPOptions) *HTTPHandler {
	h := &HTTPHandler{options: options}
	mux.Handle("/usage/v1/pro", h)
	mux.Handle("/usage/v1/pro/", h)
	return h
}
func (h *HTTPHandler) database() (*Store, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store != nil {
		return h.store, nil
	}
	s, err := Open(h.options.Directory, h.options.Clock)
	if err != nil {
		return nil, err
	}
	h.store = s
	return s, nil
}
func (h *HTTPHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.store == nil {
		return nil
	}
	return h.store.Close()
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		http.Error(w, "Usage storage unavailable.", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}
func responseError(w http.ResponseWriter, err error) {
	status, message := 500, "Usage storage unavailable."
	var e *Error
	if errors.As(err, &e) {
		status, message = e.Status, e.Message
	}
	jsonResponse(w, status, map[string]any{"schema_version": 1, "error": message})
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method != "GET" && r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	if r.Method == "GET" && (path == "/usage/v1/pro" || path == "/usage/v1/pro/history") {
		var project *string
		if path == "/usage/v1/pro" {
			if vals, ok := r.URL.Query()["project_id"]; ok {
				if len(vals) != 1 || h.options.ProjectExists == nil || !h.options.ProjectExists(vals[0]) {
					responseError(w, &Error{404, "Unknown project."})
					return
				}
				project = &vals[0]
			}
		}
		db, err := h.database()
		if err != nil {
			responseError(w, err)
			return
		}
		if path == "/usage/v1/pro/history" {
			changes, err := db.History(r.Context())
			if err != nil {
				responseError(w, err)
				return
			}
			jsonResponse(w, 200, map[string]any{"schema_version": 1, "changes": changes})
			return
		}
		result, err := db.Snapshot(r.Context(), project)
		if err != nil {
			responseError(w, err)
			return
		}
		jsonResponse(w, 200, result)
		return
	}
	if r.Method != "POST" || (path != "/usage/v1/pro/submissions" && path != "/usage/v1/pro/reset" && path != "/usage/v1/pro/limits") {
		http.NotFound(w, r)
		return
	}
	if h.options.SameOrigin != nil && !h.options.SameOrigin(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128*1024))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			responseError(w, &Error{413, "request entity too large"})
			return
		}
		responseError(w, bad("Invalid usage request."))
		return
	}
	db, err := h.database()
	if err != nil {
		responseError(w, err)
		return
	}
	switch path {
	case "/usage/v1/pro/submissions":
		result, err := db.Ingest(r.Context(), body)
		if err != nil {
			responseError(w, err)
			return
		}
		jsonResponse(w, 200, result)
	case "/usage/v1/pro/reset":
		principal := "unknown"
		if h.options.Principal != nil {
			principal = h.options.Principal(r)
		}
		result, err := db.RequestReset(r.Context(), body, principal)
		if err != nil {
			responseError(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"schema_version": 1, "settings": result})
	case "/usage/v1/pro/limits":
		result, err := db.SyncLimits(r.Context(), body)
		if err != nil {
			responseError(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"schema_version": 1, "settings": result})
	}
}
