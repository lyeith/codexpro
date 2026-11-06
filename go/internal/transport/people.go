package transport

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/lyeith/codexpro/go/internal/admin"
)

// People is an exact, header-authenticated surface. Keep the historical query
// credential and routing compatibility of the other dashboards outside it.
func isPeopleRequest(r *http.Request) bool {
	clean := path.Clean(r.URL.Path)
	return clean == "/people" || strings.HasPrefix(clean, "/people/") || strings.HasPrefix(clean, "/people.") || strings.HasPrefix(r.URL.Path, "/people/")
}

func preparePeopleRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.EscapedPath() != r.URL.Path {
		JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
		return false
	}
	switch r.URL.Path {
	case "/people", "/people/data", "/people/actions":
	default:
		JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		JSONError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return false
	}
	for key := range query {
		if strings.EqualFold(key, "token") || strings.EqualFold(key, "codexpro_token") {
			JSONError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return false
		}
	}
	if r.URL.Path == "/people" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && len(r.Header.Values("Authorization")) == 0 {
		// The sign-in shell contains no policy facts and does not read the policy.
		admin.SendPeoplePage(w, r, http.StatusUnauthorized)
		return false
	}
	return true
}

func peopleMethod(w http.ResponseWriter, r *http.Request) bool {
	allow := "GET"
	switch r.URL.Path {
	case "/people":
		allow = "GET, HEAD"
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return true
		}
	case "/people/data":
		if r.Method == http.MethodGet {
			return true
		}
	case "/people/actions":
		allow = "POST"
		if r.Method == http.MethodPost {
			return true
		}
	}
	w.Header().Set("Allow", allow)
	JSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	return false
}
