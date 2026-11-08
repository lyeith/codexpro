package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardReauthShellIsFactFree(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/activity?agent=synthetic-private-filter&token=[REDACTED_SECRET]&codexpro_token=[REDACTED_SECRET]", nil)
			r.Header.Set("Authorization", "Bearer synthetic-header-credential")
			r.Header.Set("Cookie", "synthetic-cookie=value")
			w := httptest.NewRecorder()
			SendDashboardReauth(w, r, status)
			if w.Code != status {
				t.Fatalf("status=%d, want %d", w.Code, status)
			}
			if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type=%q", got)
			}
			for _, directive := range []string{"default-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
				if !strings.Contains(w.Header().Get("Content-Security-Policy"), directive) {
					t.Fatalf("recovery shell lacks %s", directive)
				}
			}
			body := w.Body.String()
			for _, private := range []string{"synthetic-private-filter", "[REDACTED_SECRET]", "synthetic-header-credential", "synthetic-cookie"} {
				if strings.Contains(body, private) {
					t.Fatalf("recovery shell reflected request material: %s", private)
				}
			}
			for _, marker := range []string{`id="dashboard-auth-form"`, `id="dashboard-credential"`, `type="password"`, `role="status"`} {
				if !strings.Contains(body, marker) {
					t.Fatalf("recovery shell missing %s", marker)
				}
			}
			if !strings.Contains(strings.ToLower(body), "personal browser credential") {
				t.Fatal("recovery shell does not identify the personal browser credential")
			}
			if strings.Contains(body, "This page needs the connector token") {
				t.Fatal("recovery shell still directs browser users to a connector token")
			}
		})
	}
}

func TestDashboardReauthScriptRecoversWithExistingNavigation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only dashboard DOM verification")
	}
	domModule, err := filepath.Abs("../../../node_modules/happy-dom/lib/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(domModule); err != nil {
		t.Skip("happy-dom is unavailable; run npm ci for development-only dashboard DOM verification")
	}
	pages := map[string]string{}
	for _, tc := range []struct {
		name    string
		target  string
		status  int
		headers []string
	}{
		{name: "missing", target: "/activity", status: http.StatusUnauthorized},
		{name: "query", target: "/activity?token=[REDACTED_SECRET]&codexpro_token=[REDACTED_SECRET]", status: http.StatusUnauthorized},
		{name: "emptyQuery", target: "/activity?token=", status: http.StatusUnauthorized},
		{name: "header", target: "/activity", status: http.StatusUnauthorized, headers: []string{"Bearer synthetic-refused-header"}},
		{name: "emptyHeader", target: "/activity", status: http.StatusUnauthorized, headers: []string{""}},
		{name: "forbidden", target: "/activity", status: http.StatusForbidden},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.target, nil)
		if tc.headers != nil {
			r.Header["Authorization"] = tc.headers
		}
		w := httptest.NewRecorder()
		SendDashboardReauth(w, r, tc.status)
		if w.Code != tc.status {
			t.Fatalf("%s status=%d, want %d", tc.name, w.Code, tc.status)
		}
		pages[tc.name] = w.Body.String()
	}
	pagesJSON, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-", domModule)
	command.Stdin = strings.NewReader("const pages = " + string(pagesJSON) + ";\n" + dashboardReauthDOMTest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Dashboard recovery behavior failed: %v\n%s", err, output)
	}
}

const dashboardReauthDOMTest = `
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const { Window } = await import(pathToFileURL(process.argv[2]).href);
const storageKey = 'codexpro.activity.credential';
const savedCredential = 'synthetic-saved-browser-value';
const windows = [];

function createPage(name, {stored = '', url = 'https://example.test/activity?agent=SP6#agents', storageUnavailable = false} = {}) {
  const window = new Window({url});
  windows.push(window);
  const document = window.document;
  document.body.innerHTML = pages[name];
  const storage = window.sessionStorage;
  if (stored) storage.setItem(storageKey, stored);
  if (storageUnavailable) {
    const unavailable = () => { throw new Error('synthetic storage unavailable'); };
    Object.defineProperty(window, 'sessionStorage', {
      configurable: true,
      value: {getItem: unavailable, setItem: unavailable, removeItem: unavailable}
    });
  }
  const navigations = [];
  Object.defineProperty(window.location, 'replace', {
    configurable: true,
    value: target => { navigations.push(String(target)); }
  });
  let fetches = 0;
  window.fetch = () => {
    fetches++;
    throw new Error('Recovery must use existing navigation, not a helper fetch');
  };
  const logs = [];
  for (const level of ['log', 'info', 'warn', 'error', 'debug']) {
    window.console[level] = (...args) => { logs.push(args); };
  }
  const script = pages[name].match(/<script>([\s\S]*?)<\/script>/);
  assert.ok(script, 'actual recovery shell has its script');
  window.eval(script[1]);
  return {window, document, storage, navigations, logs, get fetches() { return fetches; }};
}

function form(app) {
  return app.document.getElementById('dashboard-auth-form');
}
function input(app) {
  return app.document.getElementById('dashboard-credential');
}
function assertEditable(app) {
  assert.ok(form(app), 'recovery form is available');
  assert.equal(form(app).closest('[hidden]'), null, 'recovery form is visible');
  assert.equal(input(app).type, 'password');
  assert.equal(input(app).value, '', 'no credential is prefilled');
  assert.equal(input(app).autocomplete, 'off');
  assert.equal(app.document.getElementById('msg').getAttribute('role'), 'status');
  assert.equal(app.fetches, 0, 'shell makes no helper requests');
}
function submit(app, value) {
  input(app).value = value;
  const event = new app.window.Event('submit', {bubbles: true, cancelable: true});
  form(app).dispatchEvent(event);
  assert.equal(event.defaultPrevented, true, 'submit does not invoke native form navigation');
  assert.equal(input(app).value, '', 'submitted credential is cleared immediately');
}
function assertReplacement(app, expected, pathname = '/activity') {
  assert.equal(app.navigations.length, 1, 'one explicit submit causes one navigation');
  const target = app.navigations[0];
  assert.ok(target.startsWith('/') && !target.startsWith('//'), 'navigation stays on the same origin');
  const url = new URL(target, app.window.location.href);
  assert.equal(url.pathname, pathname);
  assert.deepEqual(url.searchParams.getAll('codexpro_token'), [expected], 'one correctly encoded credential alias');
  assert.equal(url.searchParams.has('token'), false, 'legacy credential alias is removed');
  assert.equal(app.document.body.textContent.includes(expected), false, 'credential is absent from rendered copy');
  assert.equal(app.logs.length, 0, 'recovery does not log credentials or request details');
  assert.equal(app.fetches, 0);
  assert.equal(app.window.document.cookie, '', 'recovery creates no cookie');
}

try {
  const anonymous = createPage('missing');
  assertEditable(anonymous);
  assert.equal(anonymous.navigations.length, 0, 'empty tab waits for explicit input');

  const resumed = createPage('missing', {stored: savedCredential});
  assert.equal(resumed.navigations.length, 1, 'credential-free request can resume its saved tab once');
  const resumedURL = new URL(resumed.navigations[0], resumed.window.location.href);
  assert.deepEqual(resumedURL.searchParams.getAll('codexpro_token'), [savedCredential]);
  assert.equal(resumedURL.searchParams.get('agent'), 'SP6');
  assert.equal(resumedURL.hash, '#agents');
  assert.equal(input(resumed).value, '');
  assert.equal(resumed.fetches, 0);
  assert.equal(resumed.logs.length, 0);

  const afterRejectedResume = createPage('query', {stored: savedCredential, url: resumedURL.href});
  assertEditable(afterRejectedResume);
  assert.equal(afterRejectedResume.navigations.length, 0, 'a refused saved credential is not retried on the next page');
  assert.equal(afterRejectedResume.storage.getItem(storageKey), null, 'refused tab credential is discarded');

  for (const name of ['query', 'emptyQuery', 'header', 'emptyHeader', 'forbidden']) {
    const query = name === 'query'
      ? '?agent=SP6&project_id=project%20one&token=[REDACTED_SECRET]&codexpro_token=[REDACTED_SECRET]'
      : name === 'emptyQuery' ? '?agent=SP6&token=' : '?agent=SP6';
    const app = createPage(name, {stored: savedCredential, url: 'https://example.test/activity' + query + '#agents'});
    assertEditable(app);
    assert.equal(app.navigations.length, 0, name + ' never auto-retries saved storage');
    assert.equal(app.storage.getItem(storageKey), null, name + ' clears the rejected tab credential');
    const address = new URL(app.window.location.href);
    assert.equal(address.searchParams.has('token'), false, 'failed legacy alias is scrubbed');
    assert.equal(address.searchParams.has('codexpro_token'), false, 'failed current alias is scrubbed');
    assert.equal(address.searchParams.get('agent'), 'SP6');
    assert.equal(address.hash, '#agents');
    for (const privateValue of [savedCredential, '[REDACTED_SECRET]', 'synthetic-refused-header']) {
      assert.equal(app.document.body.textContent.includes(privateValue), false, 'request material stays out of recovery copy');
    }
    assert.equal(app.logs.length, 0);

    if (name === 'query') {
      const replacement = 'synthetic browser +/&?=#% value';
      submit(app, '  ' + replacement + '  ');
      assertReplacement(app, replacement);
      const target = new URL(app.navigations[0], app.window.location.href);
      assert.equal(target.searchParams.get('agent'), 'SP6');
      assert.equal(target.searchParams.get('project_id'), 'project one');
      assert.equal(target.hash, '#agents');
      assert.equal(app.storage.getItem(storageKey), null, 'the admitted page owns storing the replacement');
    }
  }

  for (const name of ['missing', 'forbidden']) {
    const app = createPage(name, {storageUnavailable: true, url: 'https://example.test/status?view=summary#nightly'});
    assertEditable(app);
    assert.equal(app.navigations.length, 0, 'unavailable storage leaves a usable form');
    submit(app, '  synthetic-storage-free-browser  ');
    assertReplacement(app, 'synthetic-storage-free-browser', '/status');
    const target = new URL(app.navigations[0], app.window.location.href);
    assert.equal(target.searchParams.get('view'), 'summary');
    assert.equal(target.hash, '#nightly');
  }
} finally {
  for (const window of windows) await window.happyDOM.close();
}
`
