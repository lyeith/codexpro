package activity

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lyeith/codexpro/go/internal/httpcompat"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const dashboardCSP = "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
const fragmentCSP = "default-src 'none'; frame-ancestors 'none'"

func writeJSON(w http.ResponseWriter, status int, value any) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		http.Error(w, "JSON encoding failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(body.Bytes(), []byte("\n")))
}
func writeText(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(value))
}
func writeHTML(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(value))
}
func one(values url.Values, key string) (string, bool, bool) {
	list, exists := values[key]
	if !exists {
		return "", false, true
	}
	if len(list) != 1 {
		return "", true, false
	}
	return list[0], true, true
}
func queryText(values url.Values, key string) string {
	v, _, ok := one(values, key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

var decimalQuery = regexp.MustCompile("^[0-9]+$")

func numberOption(values url.Values, key string, fallback int64) (int64, bool) {
	v, present, ok := one(values, key)
	if !present {
		return fallback, true
	}
	if !ok || !decimalQuery.MatchString(v) {
		return 0, false
	}
	n, e := strconv.ParseInt(v, 10, 64)
	return n, e == nil
}
func jsNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	lower := strings.ToLower(s)
	if len(lower) > 2 && lower[0] == '0' {
		base := 0
		switch lower[1] {
		case 'x':
			base = 16
		case 'b':
			base = 2
		case 'o':
			base = 8
		}
		if base != 0 {
			n, e := strconv.ParseUint(lower[2:], base, 64)
			return float64(n), e == nil
		}
	}
	n, e := strconv.ParseFloat(s, 64)
	return n, e == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpcompat.NotFound(w, r)
		return
	}
	path := r.URL.Path
	query := r.URL.Query()
	switch {
	case path == "/activity.json" || strings.HasPrefix(path, "/activity/projects/"):
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		project, present, ok := one(query, "project_id")
		if strings.HasPrefix(path, "/activity/projects/") {
			segment := strings.TrimPrefix(path, "/activity/projects/")
			if !strings.HasSuffix(segment, ".json") || strings.Contains(segment, "/") {
				httpcompat.NotFound(w, r)
				return
			}
			project = strings.TrimSuffix(segment, ".json")
			present, ok = true, true
		}
		if present && (!ok || project == "") {
			writeJSON(w, 400, map[string]any{"error": "invalid_project_id"})
			return
		}
		run, runPresent, runOK := one(query, "run_id")
		if runPresent && (!runOK || run == "" || len(run) > 200 || project == "") {
			writeJSON(w, 400, map[string]any{"error": "invalid_run_id"})
			return
		}
		if project != "" {
			if _, exists := h.project(project); !exists {
				writeJSON(w, 404, map[string]any{"error": "unknown_project"})
				return
			}
		}
		limit, lok := numberOption(query, "limit", 8)
		output, ook := numberOption(query, "output_bytes", 1024)
		quiet, qok := numberOption(query, "quiet_after_ms", 300000)
		if !lok || !ook || !qok || limit < 1 || limit > 10 || output < 0 || output > 4096 || quiet < 30000 || quiet > 86400000 {
			writeJSON(w, 400, map[string]any{"error": "invalid_options", "limits": map[string]any{"limit": "1–10", "output_bytes": "0–4096", "quiet_after_ms": "30000–86400000"}})
			return
		}
		data, err := h.CollectJSON(r.Context(), JSONOptions{ProjectID: project, RunID: run, Limit: int(limit), OutputBytes: int(output), QuietAfterMS: quiet})
		if err != nil {
			var typed *HTTPError
			if errors.As(err, &typed) {
				data := map[string]any{"error": typed.Code}
				if typed.Code == "ambiguous_run" {
					data["message"] = typed.Message
				}
				writeJSON(w, typed.Status, data)
			} else {
				writeJSON(w, 500, map[string]any{"error": "activity_unavailable"})
			}
			return
		}
		writeJSON(w, 200, data)
	case path == "/activity/live":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", fragmentCSP)
		// Baseline live view is global; project filtering only affects history and JSON.
		writeHTML(w, renderLive(h.CollectLive("")))
	case path == "/activity/job":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", fragmentCSP)
		id, _, iok := one(query, "job_id")
		workspace, _, wok := one(query, "workspace_id")
		if !iok {
			id = ""
		}
		if !wok {
			workspace = ""
		}
		body, err := h.JobFragment(r.Context(), id, workspace)
		if err != nil {
			writeText(w, 404, "Job output unavailable: the job may have expired or its workspace is not accessible.")
			return
		}
		writeHTML(w, body)
	case path == "/activity/diff":
		id := queryText(query, "project_id")
		project, ok := h.project(id)
		if !ok || id == "" || len(id) > 160 {
			writeText(w, 404, "Unknown project.")
			return
		}
		if h.options.Git == nil {
			writeText(w, 500, "The diff could not be rendered.")
			return
		}
		diff, err := h.options.Git(r.Context(), project, true)
		if err != nil {
			writeText(w, 500, "The diff could not be rendered.")
			return
		}
		writeHTML(w, RenderDiff(diff))
	case path == "/activity/batch":
		projectID, workspace, batchPath := queryText(query, "project_id"), queryText(query, "workspace_id"), queryText(query, "path")
		if projectID == "" || batchPath == "" || len(projectID) > 160 || len(batchPath) > 4096 {
			writeText(w, 400, "A valid project_id and saved batch path are required.")
			return
		}
		if _, ok := h.project(projectID); !ok {
			writeText(w, 404, "The project for this saved batch is no longer configured.")
			return
		}
		if h.options.Batch == nil {
			writeText(w, 400, "This saved batch could not be opened. Check that the current definition is valid and still inside the selected workspace.")
			return
		}
		view, err := h.options.Batch(r.Context(), projectID, workspace, batchPath)
		if err != nil {
			var typed *HTTPError
			if errors.As(err, &typed) && typed.Message != "" {
				writeText(w, typed.Status, typed.Message)
				return
			}
			message := strings.ToLower(err.Error())
			missing := strings.Contains(message, "enoent") || strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "not a file")
			if missing {
				writeText(w, 404, "This saved batch no longer exists. It may have been pruned by the 20-batch retention limit.")
			} else {
				writeText(w, 400, "This saved batch could not be opened. Check that the current definition is valid and still inside the selected workspace.")
			}
			return
		}
		w.Header().Set("Content-Security-Policy", dashboardCSP)
		if queryText(query, "fragment") == "1" {
			writeHTML(w, RenderBatchFragment(view))
		} else {
			writeHTML(w, RenderBatchPage(view))
		}
	case path == "/activity" || path == "/activity/data":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		project, _, pok := one(query, "project_id")
		beforeValue, beforePresent, bok := one(query, "before_sequence")
		before := int64(0)
		valid := pok && bok
		if beforePresent {
			value, ok := jsNumber(beforeValue)
			if !ok || value < 1 || value > 9007199254740991 || value != math.Trunc(value) {
				valid = false
			} else {
				before = int64(value)
			}
		}
		if project != "" {
			if _, ok := h.project(project); !ok {
				valid = false
			}
		}
		jsonRoute := path == "/activity/data"
		if !valid {
			if jsonRoute {
				writeJSON(w, 400, map[string]any{"error": "invalid_dashboard_options"})
			} else {
				writeText(w, 400, "Invalid history cursor or project filter.")
			}
			return
		}
		snapshot, err := h.CollectDashboard(r.Context(), project, before)
		if err != nil {
			if jsonRoute {
				writeJSON(w, 500, map[string]any{"error": "activity_dashboard_unavailable"})
			} else {
				writeText(w, 500, "CodexPro activity dashboard is temporarily unavailable. Check the local server log.")
			}
			return
		}
		snapshot.Live = h.CollectLive("")
		if jsonRoute {
			writeJSON(w, 200, RenderDashboardData(snapshot))
		} else {
			w.Header().Set("Content-Security-Policy", dashboardCSP)
			writeHTML(w, RenderDashboardPage(snapshot))
		}
	default:
		httpcompat.NotFound(w, r)
	}
}
