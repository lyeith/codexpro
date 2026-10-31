package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/httpcompat"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type HTTPOptions struct {
	SameOrigin  func(http.Handler) http.Handler
	RedactValue func(any, bool) any
	RedactText  func(string) string
	Clock       func() time.Time
}

func (s *Service) Health() map[string]any {
	c := s.Config
	var root any
	if c.String("worktreeMode", "off") == "mcp" {
		root = c.String("worktreeRoot", s.Config.Home+"/worktrees")
	}
	return map[string]any{"ok": true, "name": "CodexPro", "defaultRoot": s.defaultRoot(), "allowedRoots": s.allowedRoots(), "projectsFile": nullable(c.ProjectsFile), "defaultProjectId": s.Workspaces.DefaultProject(), "projects": s.projectLabels(),
		"bashMode": "full", "bashTranscript": c.String("bashTranscript", "compact"), "bashSessionId": nullable(c.String("bashSessionId", "")), "requireBashSession": c.Bool("requireBashSession", false), "codexSessions": "off", "writeMode": "workspace", "toolMode": c.String("toolMode", "standard"), "worktreeMode": c.String("worktreeMode", "off"), "worktreeRoot": root, "worktreeBaseRef": c.String("worktreeBaseRef", "HEAD"), "maxWorktrees": c.Int("maxWorktrees", 64), "widgetDomain": c.String("widgetDomain", "https://rebel0789.github.io"), "contextDir": c.String("contextDir", ".ai-bridge"), "authMode": "static-token", "authMethods": []string{"static-token"}, "authEnabled": true, "authRequired": true}
}
func publicValue(v any, opts HTTPOptions) any {
	b, e := encode(v, false)
	if e == nil {
		var copy any
		if json.Unmarshal(b, &copy) == nil {
			v = copy
		}
	}
	v = wire.RedactValue(v)
	if opts.RedactValue != nil {
		v = opts.RedactValue(v, true)
	}
	return v
}
func sendJSON(w http.ResponseWriter, status int, v any) {
	b, e := encode(v, false)
	if e != nil {
		http.Error(w, "Internal Server Error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func jsonError(w http.ResponseWriter, status int, code, message string, issues any) {
	errorValue := map[string]any{"code": code, "message": message}
	if issues != nil {
		errorValue["issues"] = issues
	}
	sendJSON(w, status, map[string]any{"ok": false, "error": errorValue})
}
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, e := url.Parse(origin)
		if e != nil || u.Host == "" || r.Host == "" || strings.ToLower(u.Host) != r.Host {
			jsonError(w, 403, "origin_denied", "Cross-origin admin requests are not allowed.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type adminRate struct {
	Count int
	Reset time.Time
}

func (s *Service) RegisterRoutes(mux *http.ServeMux, options *HTTPOptions) {
	opts := HTTPOptions{}
	if options != nil {
		opts = *options
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.SameOrigin == nil {
		opts.SameOrigin = SameOrigin
	}
	mux.HandleFunc("/favicon.ico", Favicon)
	mux.Handle("/healthz", s.HealthHandler(&opts))
	setup := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/setup" || r.Method != "GET" && r.Method != "HEAD" {
			httpcompat.NotFound(w, r)
			return
		}
		html, e := s.setupPage()
		if e != nil {
			http.Error(w, "CodexPro setup is temporarily unavailable. Check the local server log.", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, html)
	}
	mux.HandleFunc("/", setup)
	mux.HandleFunc("/setup", setup)
	var rateMu sync.Mutex
	windows := map[string]adminRate{}
	post := opts.SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := opts.Clock()
		key := ipKey(r.RemoteAddr)
		rateMu.Lock()
		current := windows[key]
		if !now.Before(current.Reset) {
			current = adminRate{Reset: now.Add(time.Minute)}
		}
		current.Count++
		windows[key] = current
		if len(windows) > 4096 {
			for k, v := range windows {
				if !now.Before(v.Reset) {
					delete(windows, k)
				}
			}
			if len(windows) > 4096 {
				oldestKey := ""
				var oldest time.Time
				for k, v := range windows {
					if k != key && (oldestKey == "" || v.Reset.Before(oldest)) {
						oldestKey = k
						oldest = v.Reset
					}
				}
				if oldestKey != "" {
					delete(windows, oldestKey)
				}
			}
		}
		limited := current.Count > 30
		rateMu.Unlock()
		if limited {
			jsonError(w, 429, "rate_limited", "Too many profile save attempts. Try again in a minute.", nil)
			return
		}
		if r.ContentLength > 32768 {
			jsonError(w, 413, "payload_too_large", "Profile request body is too large.", nil)
			return
		}
		input := any(map[string]any{})
		keys := []string{}
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if contentType == "application/json" {
			b, e := io.ReadAll(io.LimitReader(r.Body, 32769))
			if e != nil {
				jsonError(w, 400, "invalid_json", "Request body must be valid JSON.", nil)
				return
			}
			if len(b) > 32768 {
				jsonError(w, 413, "payload_too_large", "Request body is too large.", nil)
				return
			}
			b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
			if len(bytes.TrimSpace(b)) > 0 {
				input, keys, e = decodeRequest(b)
				if e != nil {
					jsonError(w, 400, "invalid_json", "Request body must be valid JSON.", nil)
					return
				}
			}
		}
		patch, issues := validateProfile(input, keys)
		if issues != nil {
			jsonError(w, 400, "invalid_profile", "Invalid profile settings.", issues)
			return
		}
		s.profileMu.Lock()
		existing, e := s.readProfile()
		var payload map[string]any
		if e == nil {
			payload, e = s.buildProfile(existing, patch)
		}
		if e == nil {
			e = s.saveProfile(payload)
		}
		s.profileMu.Unlock()
		if e != nil {
			message := wire.Redact(e.Error())
			if opts.RedactText != nil {
				message = opts.RedactText(message)
			}
			jsonError(w, 400, "invalid_profile", message, nil)
			return
		}
		result, e := s.ProfileResponse()
		if e != nil {
			jsonError(w, 400, "invalid_profile", wire.Redact(e.Error()), nil)
			return
		}
		// The TypeScript POST appends the raw private profile_path after its redacted
		// profile response. Preserve that shape while all saved secrets stay sanitized.
		safe, ok := publicValue(result, opts).(map[string]any)
		if !ok {
			safe = result
		}
		safe["saved"] = true
		safe["profile_path"] = s.ProfilePath()
		safe["message"] = "Saved. Restart CodexPro for these profile settings to apply."
		sendJSON(w, 200, safe)
	}))
	mux.Handle("/admin/profile", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET", "HEAD":
			result, e := s.ProfileResponse()
			if e != nil {
				http.Error(w, "Internal Server Error", 500)
				return
			}
			sendJSON(w, 200, publicValue(result, opts))
		case "POST":
			post.ServeHTTP(w, r)
		default:
			jsonError(w, 405, "method_not_allowed", "Use GET or POST for /admin/profile.", nil)
		}
	}))
}
func decodeRequest(b []byte) (any, []string, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		return nil, nil, io.ErrUnexpectedEOF
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(trimmed))
	d.UseNumber()
	if e := d.Decode(&value); e != nil {
		return nil, nil, e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return nil, nil, io.ErrUnexpectedEOF
	}
	keys := []string{}
	if trimmed[0] == '{' {
		d = json.NewDecoder(bytes.NewReader(trimmed))
		_, _ = d.Token()
		for d.More() {
			token, e := d.Token()
			if e != nil {
				return nil, nil, e
			}
			key, ok := token.(string)
			if !ok {
				return nil, nil, io.ErrUnexpectedEOF
			}
			keys = append(keys, key)
			var discard json.RawMessage
			if e = d.Decode(&discard); e != nil {
				return nil, nil, e
			}
		}
	}
	return value, keys, nil
}

const localFavicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="16" fill="#2563eb"/>
  <rect x="8" y="8" width="48" height="48" rx="12" fill="#ffffff" fill-opacity=".12" stroke="#ffffff" stroke-opacity=".38"/>
  <path d="M38.4 40.3c-1.8 1.1-3.9 1.7-6.3 1.7-6.1 0-10.3-4.2-10.3-10s4.2-10 10.4-10c2.4 0 4.5.6 6.2 1.7l-2.1 4.1c-1.1-.7-2.3-1-3.8-1-2.9 0-4.9 2.1-4.9 5.2s2 5.2 4.9 5.2c1.5 0 2.8-.4 3.9-1.1l2 4.2Z" fill="#ffffff"/>
</svg>`

func Favicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		httpcompat.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = io.WriteString(w, localFavicon)
}
func WantsDashboardReauth(r *http.Request) bool {
	return r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/activity") && strings.Contains(r.Header.Get("Accept"), "text/html")
}
func SendDashboardReauth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(401)
	_, _ = io.WriteString(w, reauthPage)
}

const reauthPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>CodexPro · signing in</title>
<style>body{font-family:system-ui,sans-serif;margin:40px;color:#172033}code{background:#eef2f7;padding:2px 5px;border-radius:4px}</style></head>
<body><p id="msg">Re-authenticating…</p>
<script>
  (function () {
    var saved = "";
    try { saved = sessionStorage.getItem("codexpro.activity.credential") || ""; } catch (e) {}
    if (saved) {
      var url = new URL(window.location.href);
      url.searchParams.set("codexpro_token", saved);
      window.location.replace(url.pathname + url.search + url.hash);
    } else {
      document.getElementById("msg").innerHTML = "This page needs the connector token. Open the dashboard from the launcher's connection URL (the one containing <code>codexpro_token=</code>); the token is then remembered for this tab.";
    }
  })();
</script></body></html>`
