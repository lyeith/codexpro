package transport

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/admin"
	"github.com/lyeith/codexpro/go/internal/httpcompat"
)

type failureWindow struct {
	count int
	reset time.Time
}

var bearerPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

func credentialFrom(r *http.Request) string {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ""
	}
	queryValues := append(query["codexpro_token"], query["token"]...)
	headers := r.Header.Values("Authorization")
	switch {
	case len(headers) == 1 && len(queryValues) == 0:
		match := bearerPattern.FindStringSubmatch(strings.TrimSpace(headers[0]))
		if len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	case len(headers) == 0 && len(queryValues) == 1:
		return queryValues[0]
	}
	return ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	favicon := r.URL.Path == "/favicon.ico" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	if favicon && len(r.Header.Values("Authorization")) == 0 {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err == nil && !query.Has("codexpro_token") && !query.Has("token") {
			admin.Favicon(w, r)
			return
		}
	}
	hdr := w.Header()
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Pragma", "no-cache")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("X-Frame-Options", "DENY")
	people := isPeopleRequest(r)
	if people && !preparePeopleRequest(w, r) {
		return
	}
	if s.serveStatusCollector(w, r) {
		return
	}
	candidate := credentialFrom(r)
	// This policy observation admits the whole HTTP request. Already admitted
	// work can finish; revocation, role and grant changes apply to the next POST.
	identity, projects, ok, ownerPageReader, policyErr := s.authenticateHTTP(candidate)
	if policyErr != nil {
		policyUnavailable(w)
		return
	}
	if !ok {
		key, _, e := net.SplitHostPort(r.RemoteAddr)
		if e != nil {
			key = r.RemoteAddr
		}
		if key == "" {
			key = "local"
		}
		now := time.Now()
		s.mu.Lock()
		window := s.failures[key]
		if !window.reset.After(now) {
			window = failureWindow{reset: now.Add(time.Minute)}
		}
		window.count++
		s.failures[key] = window
		if len(s.failures) > 4096 {
			for k, v := range s.failures {
				if !v.reset.After(now) {
					delete(s.failures, k)
				}
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if window.count > 10 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(window.reset).Seconds()+0.999))))
			w.WriteHeader(429)
			_, _ = io.WriteString(w, "Too Many Authentication Attempts")
			return
		}
		if people {
			if r.URL.Path == "/people" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				admin.SendPeoplePage(w, r, http.StatusUnauthorized)
			} else {
				JSONError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			}
		} else if admin.WantsDashboardReauth(r) {
			admin.SendDashboardReauth(w, r, http.StatusUnauthorized)
		} else {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "Unauthorized")
		}
		return
	}
	if favicon {
		admin.Favicon(w, r)
		return
	}
	if people {
		if identity.Source != "named" || !ownerPageReader {
			if r.URL.Path == "/people" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.Contains(r.Header.Get("Accept"), "text/html") {
				admin.SendPeoplePage(w, r, http.StatusForbidden)
			} else {
				JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
			}
			return
		}
		if !peopleMethod(w, r) {
			return
		}
	} else if r.URL.Path == "/status" || r.URL.Path == "/activity" || r.URL.Path == "/activity/data" {
		if r.URL.EscapedPath() != r.URL.Path || (r.Method != http.MethodGet && r.Method != http.MethodHead) || !ownerPageReader {
			if r.URL.EscapedPath() == r.URL.Path && (r.URL.Path == "/activity" || r.URL.Path == "/status") && admin.WantsDashboardReauth(r) {
				admin.SendDashboardReauth(w, r, http.StatusForbidden)
			} else {
				JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
			}
			return
		}
	} else if identity.Restricted() && (r.URL.EscapedPath() != "/mcp" ||
		(r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete)) {
		JSONError(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}
	handler, pattern := s.Mux.Handler(r)
	if pattern == "" {
		NotFound(w, r)
		return
	}
	r = r.WithContext(withRequestAuthorization(r.Context(), identity, projects))
	handler.ServeHTTP(w, r)
}

func SameOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, e := url.Parse(origin)
	if e != nil || parsed.Host == "" || r.Host == "" || strings.ToLower(parsed.Host) != r.Host {
		JSONError(w, 403, "origin_denied", "Cross-origin admin requests are not allowed.")
		return false
	}
	return true
}

func NotFound(w http.ResponseWriter, r *http.Request) { httpcompat.NotFound(w, r) }
