package inbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
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
	mux.Handle("/inbox/v1/", h)
	mux.Handle("/activity/inbox", h)
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
		http.Error(w, "Inbox unavailable.", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}
func responseError(w http.ResponseWriter, err error) {
	status, message := 500, "Inbox unavailable."
	var e *Error
	if errors.As(err, &e) {
		status, message = e.Status, e.Message
	}
	jsonResponse(w, status, map[string]any{"schema_version": 1, "error": message})
}
func queryNumber(r *http.Request, name string, fallback, min, max int64) (int64, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, bad("Invalid pagination.")
	}
	for _, c := range values[0] {
		if c < '0' || c > '9' {
			return 0, bad("Invalid pagination.")
		}
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || value < min || value > max {
		return 0, bad("Invalid pagination.")
	}
	return value, nil
}
func (h *HTTPHandler) projectQuery(r *http.Request) (*string, error) {
	values, ok := r.URL.Query()["project_id"]
	if !ok {
		return nil, nil
	}
	if len(values) != 1 || h.options.ProjectExists == nil || !h.options.ProjectExists(values[0]) {
		return nil, &Error{404, "Unknown project."}
	}
	return &values[0], nil
}
func (h *HTTPHandler) validProject(value string) bool {
	return h.options.ProjectExists != nil && h.options.ProjectExists(value)
}
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == "GET" && path == "/activity/inbox" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		projects := []wire.Project{}
		if h.options.Projects != nil {
			projects = h.options.Projects()
		}
		_, _ = io.WriteString(w, Page(projects))
		return
	}
	if r.Method == "GET" && path == "/inbox/v1/items" {
		var state, consumer *string
		if values, ok := r.URL.Query()["status"]; ok {
			if len(values) != 1 || (values[0] != "pending" && values[0] != "answered") {
				responseError(w, bad("Invalid status."))
				return
			}
			state = &values[0]
		}
		if values, ok := r.URL.Query()["consumer"]; ok {
			if len(values) != 1 || !identity.MatchString(values[0]) {
				responseError(w, bad("Invalid consumer."))
				return
			}
			consumer = &values[0]
		}
		project, err := h.projectQuery(r)
		if err != nil {
			responseError(w, err)
			return
		}
		limit, err := queryNumber(r, "limit", 100, 1, 100)
		if err != nil {
			responseError(w, err)
			return
		}
		offset, err := queryNumber(r, "offset", 0, 0, 1000000)
		if err != nil {
			responseError(w, err)
			return
		}
		db, err := h.database()
		if err != nil {
			responseError(w, err)
			return
		}
		result, err := db.List(r.Context(), ListFilter{Project: project, Status: state, Consumer: consumer, Limit: int(limit), Offset: int(offset)})
		if err != nil {
			responseError(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"schema_version": 1, "items": result.Items, "total": result.Total, "next_offset": result.NextOffset})
		return
	}
	if r.Method == "GET" && path == "/inbox/v1/events" {
		project, err := h.projectQuery(r)
		if err != nil {
			responseError(w, err)
			return
		}
		after, err := queryNumber(r, "after", 0, 0, 9007199254740991)
		if err != nil {
			responseError(w, err)
			return
		}
		limit, err := queryNumber(r, "limit", 100, 1, 100)
		if err != nil {
			responseError(w, err)
			return
		}
		db, err := h.database()
		if err != nil {
			responseError(w, err)
			return
		}
		events, err := db.Events(r.Context(), project, after, int(limit))
		if err != nil {
			responseError(w, err)
			return
		}
		next := after
		if len(events) > 0 {
			next = events[len(events)-1]["sequence"].(int64)
		}
		jsonResponse(w, 200, map[string]any{"schema_version": 1, "events": events, "next_after": next})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/inbox/v1/items/"), "/")
	isItem := strings.HasPrefix(path, "/inbox/v1/items/") && len(parts) >= 2 && parts[0] != "" && parts[1] != ""
	if r.Method == "GET" && isItem && len(parts) == 2 {
		if !h.validProject(parts[0]) {
			responseError(w, &Error{404, "Unknown project."})
			return
		}
		db, err := h.database()
		if err != nil {
			responseError(w, err)
			return
		}
		item, found, err := db.Get(r.Context(), parts[0], parts[1])
		if err != nil {
			responseError(w, err)
			return
		}
		if !found {
			responseError(w, &Error{404, "Question not found."})
			return
		}
		jsonResponse(w, 200, item)
		return
	}
	validPost := path == "/inbox/v1/items" || (isItem && len(parts) == 3 && (parts[2] == "answers" || parts[2] == "deliveries"))
	if r.Method != "POST" || !validPost {
		http.NotFound(w, r)
		return
	}
	if h.options.SameOrigin != nil && !h.options.SameOrigin(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32*1024))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			responseError(w, &Error{413, "request entity too large"})
			return
		}
		responseError(w, bad("Invalid inbox payload."))
		return
	}
	if path == "/inbox/v1/items" {
		var raw map[string]any
		if err = json.Unmarshal(body, &raw); err != nil {
			responseError(w, bad("Invalid inbox payload."))
			return
		}
		p, ok := raw["project_id"].(string)
		if !ok || !h.validProject(p) {
			responseError(w, &Error{404, "Unknown project."})
			return
		}
	} else if !h.validProject(parts[0]) {
		responseError(w, &Error{404, "Unknown project."})
		return
	}
	db, err := h.database()
	if err != nil {
		responseError(w, err)
		return
	}
	var item Item
	if path == "/inbox/v1/items" {
		item, err = db.Publish(r.Context(), body)
	} else if parts[2] == "answers" {
		principal := "unknown"
		if h.options.Principal != nil {
			principal = h.options.Principal(r)
		}
		item, err = db.Answer(r.Context(), parts[0], parts[1], body, principal)
	} else {
		item, err = db.Delivered(r.Context(), parts[0], parts[1], body)
	}
	if err != nil {
		responseError(w, err)
		return
	}
	jsonResponse(w, 200, item)
}
