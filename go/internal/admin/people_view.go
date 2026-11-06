package admin

import (
	"bytes"
	"html/template"
	"net/http"
)

// The shell is deliberately independent of request identity, credentials and
// policy. Transport supplies its status; only the admitted data route supplies
// facts, using an explicit Authorization header.
var peopleTemplate = template.Must(template.New("people").Parse(peoplePage))

func SendPeoplePage(w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	var body bytes.Buffer
	if err := peopleTemplate.Execute(&body, nil); err != nil {
		http.Error(w, "People is unavailable.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

const peoplePage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="color-scheme" content="light"><title>People · CodexPro</title><style>` + peopleStyle + `</style></head><body>
<a class="skip-link" href="#people-main">Skip to People</a>
<main id="people-main" class="people-main">
<header class="page-bar"><div class="brand"><span class="brand-mark" aria-hidden="true"></span>CodexPro<span class="brand-divider" aria-hidden="true">/</span><span class="brand-area">People</span></div><nav aria-label="CodexPro"><a href="/activity">Activity</a><a href="/status">Status</a></nav></header>
<div class="page-heading"><div><p class="eyebrow">Access administration</p><h1>People</h1><p class="lead">People, their registered agents and credential access.</p></div><div id="people-controls" class="page-actions" hidden><button class="button" type="button" id="people-refresh">Refresh</button><button class="button primary" type="button" id="people-add">Add person</button></div></div>
<p id="people-feedback" class="feedback" role="status" aria-live="polite">Use an existing owner credential to open this view.</p>
<section id="people-auth" class="panel sign-in" aria-labelledby="people-auth-title">
<p class="eyebrow">Owner access</p><h2 id="people-auth-title">Open People</h2>
<p>A personal owner credential bound to an enabled concierge is required.</p>
<form id="people-auth-form"><label for="people-credential">Existing credential</label><input id="people-credential" name="credential" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" required aria-describedby="people-auth-note"><p class="field-note" id="people-auth-note">Used only for authenticated requests from this page. A pasted value is kept in this page’s memory and is cleared from the input.</p><button class="button primary" type="submit">Open People</button></form>
</section>
<div id="people-workspace" hidden>
<section class="self-panel" aria-labelledby="people-self-title"><div><p class="eyebrow" id="people-self-title">Your current access</p><p id="people-self" class="self-name"></p><p id="people-self-binding" class="field-note"></p></div><button class="text-button" type="button" id="people-close">Close this view</button></section>
<section id="people-editor" class="panel editor" hidden aria-labelledby="people-editor-title"></section>
<section class="panel roster-panel" aria-labelledby="people-roster-title">
<div class="section-heading"><div><h2 id="people-roster-title">People and registrations</h2><p id="people-roster-note" class="muted">Enabled describes policy status, not whether an agent is online.</p></div><span id="people-count" class="count-label"></span></div>
<div id="people-roster"></div>
</section>
<p class="page-foot">Credential delivery remains a separate private-file owner operation. This page does not issue or rotate credentials.</p>
</div>
<noscript><p class="feedback error">JavaScript is required for header-authenticated People reads and actions. No roster is included in this page.</p></noscript>
</main><script>` + peopleScript + `</script></body></html>`
