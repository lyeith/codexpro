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

func TestPeopleShellIsFactFreeAndHeadHasNoBody(t *testing.T) {
	var shell string
	for _, status := range []int{http.StatusUnauthorized, http.StatusOK} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(method, "/people?ignored=synthetic-request-value", nil)
			r.Header.Set("Authorization", "Bearer synthetic-request-credential")
			r.Header.Set("Cookie", "synthetic-cookie=value")
			w := httptest.NewRecorder()
			SendPeoplePage(w, r, status)
			if w.Code != status {
				t.Fatalf("%s status=%d, want %d", method, w.Code, status)
			}
			for name, want := range map[string]string{
				"Cache-Control": "no-store", "Referrer-Policy": "no-referrer",
				"Content-Type": "text/html; charset=utf-8", "X-Frame-Options": "DENY",
			} {
				if got := w.Header().Get(name); got != want {
					t.Fatalf("%s=%q, want %q", name, got, want)
				}
			}
			csp := w.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "form-action 'none'") {
				t.Fatalf("People shell lacks its same-origin read and no-form-navigation policy: %q", csp)
			}
			if method == http.MethodHead {
				if w.Body.Len() != 0 {
					t.Fatal("HEAD returned a page body")
				}
				continue
			}
			body := w.Body.String()
			if shell == "" {
				shell = body
			} else if body != shell {
				t.Fatal("shell content varies with caller admission status")
			}
			for _, private := range []string{"synthetic-request-value", "synthetic-request-credential", "synthetic-cookie", "codexpro_token"} {
				if strings.Contains(body, private) {
					t.Fatalf("shell reflected request material or retained query-token navigation: %q", private)
				}
			}
		}
	}
}

func TestPeopleScriptReadsActionsAndUncertainOutcomes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for development-only People DOM verification")
	}
	domModule, err := filepath.Abs("testdata/dom/node_modules/happy-dom/lib/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(domModule); err != nil {
		t.Skip("happy-dom is unavailable; from the repository root run npm ci --prefix go/internal/admin/testdata/dom --ignore-scripts --no-audit --no-fund for development-only People DOM verification")
	}
	w := httptest.NewRecorder()
	SendPeoplePage(w, httptest.NewRequest(http.MethodGet, "/people", nil), http.StatusUnauthorized)
	pageJSON, err := json.Marshal(w.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--input-type=module", "-", domModule)
	command.Stdin = strings.NewReader("const page = " + string(pageJSON) + ";\n" + peopleDOMTest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("People page behavior failed: %v\n%s", err, output)
	}
}

const peopleDOMTest = `
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
const { Window } = await import(pathToFileURL(process.argv[2]).href);
const credential = 'synthetic-owner-credential';
const storageKey = 'codexpro.activity.credential';
const copy = value => JSON.parse(JSON.stringify(value));
function fixture() {
  return {
    ok: true,
    self: { principal_id:'owner-fixture', agent_id:'owner-concierge', credential_id:'current-device', role:'owner' },
    roster: {
      principals: [
        { id:'owner-fixture', type:'person', role:'owner', projects:['alpha'], disabled:false, has_delegations:false },
        { id:'second-person', type:'person', role:'engineer', projects:[], disabled:false, has_delegations:false },
        { id:'system-fixture', type:'system', role:'', projects:[], disabled:false, has_delegations:false }
      ],
      agents: [
        { id:'owner-concierge', principal_id:'owner-fixture', kind:'concierge', address:'people/owner-fixture/concierge', project_id:'', disabled:false },
        { id:'owner-crew', principal_id:'owner-fixture', kind:'crew', address:'people/owner-fixture/crew', project_id:'alpha', disabled:false },
        { id:'system-agent', principal_id:'system-fixture', kind:'coordinator', address:'system-fixture/coordinator', project_id:'', disabled:false }
      ],
      devices: [
        { id:'current-device', principal_id:'owner-fixture', agent_id:'owner-concierge', device:'Synthetic current device', audience:'codexpro', revoked:false, can_revoke:true },
        { id:'spare-device', principal_id:'owner-fixture', agent_id:'owner-crew', device:'<img data-injected src=x>', audience:'agentmail', revoked:false, can_revoke:true },
        { id:'unmanaged-device', principal_id:'owner-fixture', agent_id:'owner-concierge', device:'Read-only registration', audience:'codexpro', revoked:false, can_revoke:false }
      ]
    }
  };
}
const result = (status, body) => ({ status, ok:status >= 200 && status < 300, json:async () => body });
const refused = (code='invalid_request', status=400) => result(status, {ok:false,error:{code,outcome:'refused',message:'server-private-detail'}});
async function settle() { for (let i=0;i<30;i++) await Promise.resolve(); }
function createPage(stored='', url='https://example.test/people') {
  const window = new Window({url});
  const document = window.document;
  document.body.innerHTML = page;
  if (stored) window.sessionStorage.setItem(storageKey, stored);
  const calls = [];
  let handler = async () => result(200, fixture());
  window.fetch = async (url, options) => {
    calls.push({url,options});
    return handler(url, options);
  };
  const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
  window.eval(script);
  return {window,document,calls,setHandler:value => { handler=value; }};
}
function submit(app, form) {
  form.dispatchEvent(new app.window.Event('submit', {bubbles:true,cancelable:true}));
}
function editForm(app) { return app.document.querySelector('[data-people-form]'); }
function field(app, name, value) {
  const element = editForm(app).elements.namedItem(name);
  element.value = value;
  element.dispatchEvent(new app.window.Event('input', {bubbles:true}));
  return element;
}
function person(app, id) { return [...app.document.querySelectorAll('[data-person-id]')].find(el => el.dataset.personId === id); }
function expand(app, id) {
  const toggle = person(app,id).querySelector('.person-toggle');
  if (toggle.getAttribute('aria-expanded') !== 'true') toggle.click();
  return person(app,id);
}
function action(app, kind, id='owner-fixture') {
  const entry = expand(app,id);
  const button = entry.querySelector('[data-people-action="' + kind + '"]');
  assert.ok(button, 'action must be offered for its exact personal target');
  button.click();
  return editForm(app);
}
function targetAction(app, kind, target) {
  const entry = expand(app,'owner-fixture');
  const item = [...entry.querySelectorAll('.detail-item')].find(item =>
    item.querySelector('h4').textContent === target ||
    item.textContent.includes('ID: ' + target));
  assert.ok(item);
  const button = item.querySelector('[data-people-action="' + kind + '"]');
  assert.ok(button);
  button.click();
  return editForm(app);
}
const posts = app => app.calls.filter(call => call.options.method === 'POST');
const lastAction = app => JSON.parse(posts(app).at(-1).options.body);

// A static shell does not consume a credential from the URL or fetch facts.
const anonymous = createPage('', 'https://example.test/people?codexpro_token=[REDACTED_SECRET]');
await settle();
assert.equal(anonymous.calls.length, 0);
assert.equal(anonymous.document.getElementById('people-workspace').hidden, true);
assert.equal(anonymous.document.getElementById('people-auth').hidden, false);
anonymous.document.getElementById('people-credential').value = credential;
submit(anonymous, anonymous.document.getElementById('people-auth-form'));
assert.equal(anonymous.document.getElementById('people-credential').value, '', 'pasted credential is cleared immediately');
await settle();
assert.equal(anonymous.window.sessionStorage.getItem(storageKey), null, 'pasted credential is not persisted');
assert.equal(anonymous.calls[0].url, '/people/data');
assert.equal(anonymous.calls[0].options.headers.Authorization, 'Bearer ' + credential);

const app = createPage(credential);
await settle();
const doc = app.document;
assert.equal(app.calls.length, 1);
assert.equal(doc.getElementById('people-workspace').hidden, false);
assert.equal(doc.querySelector('[data-injected]'), null, 'owner strings are text, never HTML');
assert.ok(doc.body.textContent.includes('<img data-injected src=x>'));
for (const call of app.calls) {
  assert.equal(call.url, '/people/data');
  assert.equal(call.options.credentials, 'omit');
  assert.equal(call.options.redirect, 'error');
  assert.equal(call.options.headers.Authorization, 'Bearer ' + credential);
}
assert.equal(doc.querySelectorAll('a[href*="token"]').length, 0);
assert.equal(doc.body.textContent.includes(credential), false);
const system = expand(app,'system-fixture');
assert.equal(system.querySelectorAll('[data-people-action]').length, 0, 'system records have no administrative actions');
const unmanaged = [...expand(app,'owner-fixture').querySelectorAll('.detail-item')].find(item => item.textContent.includes('ID: unmanaged-device'));
assert.equal(unmanaged.querySelector('[data-people-action]'), null, 'revoke eligibility is explicit, not inferred from ID or audience');

// Registration contains only the approved fields and an exact parent snapshot.
app.setHandler(async (url) => url === '/people/actions' ? refused() : result(200,fixture()));
action(app,'register_agent');
assert.deepEqual([...editForm(app).elements.namedItem('project_id').options].map(option => option.value), ['', 'alpha']);
field(app,'id','new-agent');
field(app,'kind','crew');
field(app,'address','people/owner-fixture/new-agent');
field(app,'project_id','alpha');
submit(app,editForm(app));
await settle();
assert.deepEqual(lastAction(app), {
  kind:'register_agent',
  agent:{id:'new-agent',principal_id:'owner-fixture',kind:'crew',address:'people/owner-fixture/new-agent',project_id:'alpha'},
  expected:fixture().roster.principals[0]
});
assert.equal(editForm(app).elements.namedItem('address').value, 'people/owner-fixture/new-agent', 'refusal keeps form input');
assert.equal(doc.body.textContent.includes('server-private-detail'), false, 'server detail is never reflected');
const addressField = editForm(app).elements.namedItem('address');
addressField.focus();
doc.getElementById('people-refresh').click();
await settle();
assert.equal(doc.activeElement, addressField, 'refresh preserves focus in the retained form');
assert.equal(editForm(app).elements.namedItem('id').value, 'new-agent');
assert.equal(person(app,'owner-fixture').querySelector('.person-toggle').getAttribute('aria-expanded'),'true');


// Reviewing a changed parent never silently drops an entered project binding.
const changedParent = fixture();
changedParent.roster.principals[0].projects = [];
app.setHandler(async () => result(200,copy(changedParent)));
doc.getElementById('people-refresh').click();
await settle();
editForm(app).querySelector('.review-note button').click();
assert.equal(editForm(app).elements.namedItem('project_id').value,'alpha');
assert.equal(editForm(app).elements.namedItem('project_id').checkValidity(),false);
assert.equal(editForm(app).elements.namedItem('address').value,'people/owner-fixture/new-agent');
app.setHandler(async (url) => url === '/people/actions' ? refused() : result(200,fixture()));
doc.getElementById('people-refresh').click();
await settle();

// Add person has no implicit grant, agent or credential.
doc.getElementById('people-add').click();
field(app,'id','new-person');
field(app,'role','tester');
submit(app,editForm(app));
await settle();
assert.deepEqual(lastAction(app), {kind:'add_person',id:'new-person',role:'tester'});

// Retiring an agent requires explicit confirmation and uses its safe snapshot.
targetAction(app,'retire_agent','owner-crew');
let before = posts(app).length;
submit(app,editForm(app));
await settle();
assert.equal(posts(app).length,before,'unchecked consequence sends nothing');
editForm(app).elements.namedItem('confirm').checked = true;
submit(app,editForm(app));
await settle();
assert.deepEqual(lastAction(app), {kind:'retire_agent',id:'owner-crew',expected:fixture().roster.agents[1]});

// A maintenance hold is a known refusal, even with HTTP 503; it is not uncertainty.
const held = createPage(credential);
await settle();
action(held,'change_role','second-person');
field(held,'role','product');
const heldDoc = held.document;
const heldForm = editForm(held);
const heldConfirmation = heldForm.elements.namedItem('confirm');
heldConfirmation.checked = true;
const heldDraft = [...new held.window.FormData(heldForm).entries()];
const heldRoster = heldDoc.getElementById('people-roster');
const heldRosterNode = heldRoster.firstElementChild;
const heldRosterText = heldRoster.textContent;
const heldCalls = held.calls.length;
assert.equal(heldCalls,1);
held.setHandler(async (url) => url === '/people/actions'
  ? refused('execution_held',503)
  : result(200,fixture()));
submit(held,heldForm);
await settle();
assert.deepEqual(held.calls.slice(heldCalls).map(({url,options}) => ({url,method:options.method})),
  [{url:'/people/actions',method:'POST'}], 'a held apply performs no retry or access GET');
assert.deepEqual(lastAction(held),
  {kind:'change_role',id:'second-person',role:'product',expected:fixture().roster.principals[1]});
assert.equal(editForm(held),heldForm,'a held apply retains the original editor');
assert.deepEqual([...new held.window.FormData(heldForm).entries()],heldDraft,'a held apply retains the draft');
assert.equal(heldConfirmation.checked,true,'a held apply retains the consequence confirmation');
assert.equal(heldRoster.firstElementChild,heldRosterNode,'a held apply retains the roster nodes');
assert.equal(heldRoster.textContent,heldRosterText,'a held apply retains the roster facts');
assert.equal(heldForm.querySelector('.form-feedback').textContent,
  'Execution is held for maintenance. No policy change was accepted.');
assert.equal(heldDoc.getElementById('people-feedback').textContent,
  'The change was refused. No change was confirmed.');
assert.equal(heldDoc.getElementById('people-add').disabled,false,'a held apply preserves fresh records');
assert.equal(heldForm.querySelector('button[type=submit]').disabled,false);
assert.equal(heldForm.querySelector('.review-note').hidden,true);
assert.equal(heldDoc.body.textContent.includes('server-private-detail'),false);
await held.window.close();

// A conflict is refused, never retried; current data must be reviewed.
let current = fixture();
app.setHandler(async (url) => url === '/people/actions' ? refused('conflict',409) : result(200,copy(current)));
action(app,'disable_person','second-person');
editForm(app).elements.namedItem('confirm').checked = true;
submit(app,editForm(app));
await settle();
assert.deepEqual(lastAction(app), {kind:'disable_person',id:'second-person',expected:fixture().roster.principals[1]});
assert.equal(doc.getElementById('people-add').disabled,true);
const conflictPosts = posts(app).length;
current.roster.principals[1].role = 'product';
doc.getElementById('people-refresh').click();
await settle();
assert.equal(posts(app).length,conflictPosts);
assert.equal(editForm(app).querySelector('.review-note').hidden,false);
editForm(app).querySelector('.review-note button').click();
assert.equal(editForm(app).elements.namedItem('confirm').checked,false,'review requires a new consequence confirmation');

// An uncertain write retains its unknown outcome even when desired state appears.
current = fixture();
app.setHandler(async (url) => url === '/people/actions'
  ? result(503,{ok:false,error:{code:'uncertain',outcome:'uncertain',message:'server-private-detail'}})
  : result(200,copy(current)));
targetAction(app,'revoke_credential','spare-device');
editForm(app).elements.namedItem('confirm').checked = true;
before = app.calls.length;
submit(app,editForm(app));
await settle();
assert.equal(app.calls.length,before+1,'uncertainty performs no automatic retry or state read');
assert.deepEqual(lastAction(app), {kind:'revoke_credential',id:'spare-device',expected:fixture().roster.devices[1]});
assert.match(doc.getElementById('people-feedback').textContent,/outcome unknown/i);
const uncertainPosts = posts(app).length;
current.roster.devices[1].revoked = true;
current.roster.devices[1].can_revoke = false;
doc.getElementById('people-refresh').click();
await settle();
assert.equal(posts(app).length,uncertainPosts);
assert.match(doc.getElementById('people-feedback').textContent,/earlier request.*unconfirmed/i);
assert.equal(editForm(app).querySelector('button[type=submit]').disabled,true,'achieved current state does not offer a repeat action');

// Failed reads keep the last roster and prevent writes until a fresh read.
const lastRoster = {node:doc.getElementById('people-roster').firstElementChild,text:doc.getElementById('people-roster').textContent};
app.setHandler(async () => { throw new Error('server-private-detail'); });
doc.getElementById('people-refresh').click();
await settle();
assert.equal(doc.getElementById('people-roster').firstElementChild,lastRoster.node,'failed reads keep the existing roster nodes');
assert.equal(doc.getElementById('people-roster').textContent,lastRoster.text,'failed reads preserve every displayed fact');
assert.equal(doc.getElementById('people-add').disabled,true);
assert.match(doc.getElementById('people-feedback').textContent,/last roster is retained/i);
assert.equal(doc.body.textContent.includes('server-private-detail'),false);
app.setHandler(async () => result(200,copy(current)));
doc.getElementById('people-refresh').click();
await settle();

// The last enabled owner has locked destructive actions and a visible reason.
const ownerSafety = createPage(credential);
await settle();
const ownerDoc = ownerSafety.document;
const ownerDetails = expand(ownerSafety,'owner-fixture');
for (const kind of ['disable_person','change_role']) {
  const button = ownerDetails.querySelector('[data-people-action="' + kind + '"]');
  assert.equal(button.disabled,true,kind + ' is locked on the last enabled owner');
  assert.equal(button.title,'Plans needs at least one enabled owner.');
  button.click();
  assert.equal(editForm(ownerSafety),null,'a locked control cannot open a form');
}
assert.ok(ownerDetails.textContent.includes('Plans needs at least one enabled owner.'));
assert.equal(posts(ownerSafety).length,0);

// Current credential and current concierge consequences name this page.
targetAction(ownerSafety,'revoke_credential','current-device');
assert.match(ownerDoc.getElementById('people-consequence').textContent,/current credential; this page will stop working/);
targetAction(ownerSafety,'retire_agent','owner-concierge');
assert.match(ownerDoc.getElementById('people-consequence').textContent,/current concierge; this page will stop working/);
assert.equal(posts(ownerSafety).length,0);

// A previously open form locks when another owner becomes disabled.
const twoOwners = fixture();
twoOwners.roster.principals[1].role = 'owner';
ownerSafety.setHandler(async () => result(200,copy(twoOwners)));
ownerDoc.getElementById('people-refresh').click();
await settle();
action(ownerSafety,'change_role');
field(ownerSafety,'role','engineer');
editForm(ownerSafety).elements.namedItem('confirm').checked = true;
twoOwners.roster.principals[1].disabled = true;
ownerDoc.getElementById('people-refresh').click();
await settle();
assert.equal(editForm(ownerSafety).querySelector('button[type=submit]').disabled,true);
assert.equal(editForm(ownerSafety).elements.namedItem('role').disabled,true);
assert.equal(editForm(ownerSafety).querySelector('.form-feedback').textContent,'Plans needs at least one enabled owner.');
submit(ownerSafety,editForm(ownerSafety));
await settle();
assert.equal(posts(ownerSafety).length,0,'stale open form cannot remove the last owner');
await ownerSafety.window.happyDOM.close();

// Self changes remain available with another enabled owner and clear sign-out.
const remainingOwner = fixture();
remainingOwner.roster.principals[1].role = 'owner';
app.setHandler(async () => result(200,copy(remainingOwner)));
doc.getElementById('people-refresh').click();
await settle();
action(app,'change_role');
field(app,'role','engineer');
assert.match(doc.getElementById('people-consequence').textContent,/end your access to People/);
editForm(app).elements.namedItem('confirm').checked = true;
app.setHandler(async (url) => url === '/people/actions'
  ? result(200,{ok:true,outcome:'confirmed'})
  : result(403,{ok:false,error:{code:'forbidden',outcome:'refused'}}));
submit(app,editForm(app));
await settle();
assert.deepEqual(lastAction(app), {kind:'change_role',id:'owner-fixture',role:'engineer',expected:fixture().roster.principals[0]});
assert.equal(doc.getElementById('people-workspace').hidden,true);
assert.equal(doc.getElementById('people-roster').childElementCount,0,'signed-out results clear private records');
assert.match(doc.getElementById('people-feedback').textContent,/Change confirmed.*no longer has access/);
assert.equal(doc.getElementById('people-auth').hidden,false);
assert.deepEqual(new Set(posts(app).map(call => JSON.parse(call.options.body).kind)), new Set(['add_person','change_role','disable_person','register_agent','retire_agent','revoke_credential']));
for (const call of app.calls) {
  assert.ok(call.url === '/people/data' || call.url === '/people/actions');
  assert.equal(call.url.includes('?'),false);
  assert.equal(call.options.credentials,'omit');
  assert.equal(call.options.headers.Authorization,'Bearer ' + credential);
}

// Closing a pending read invalidates the fetch, JSON continuation and failure path.
function deferred() {
  let resolve, reject;
  const promise = new Promise((done, fail) => { resolve=done; reject=fail; });
  return {promise,resolve,reject};
}
for (const mode of ['response','body','failure']) {
  const closing = createPage(credential);
  await settle();
  const pending = deferred();
  closing.setHandler(async () => mode === 'body'
    ? {status:200,ok:true,json:() => pending.promise}
    : pending.promise);
  closing.document.getElementById('people-refresh').click();
  await settle();
  assert.equal(closing.document.getElementById('people-workspace').getAttribute('aria-busy'),'true');
  closing.document.getElementById('people-close').click();
  const closedMessage = closing.document.getElementById('people-feedback').textContent;
  assert.equal(closing.document.getElementById('people-workspace').hidden,true);
  assert.equal(closing.document.getElementById('people-credential').disabled,false);
  if (mode === 'failure') pending.reject(new Error('late-private-failure'));
  else pending.resolve(mode === 'body' ? fixture() : result(200,fixture()));
  await settle();
  assert.equal(closing.document.getElementById('people-workspace').hidden,true,'late reads cannot reopen a closed roster');
  assert.equal(closing.document.getElementById('people-roster').childElementCount,0);
  assert.equal(closing.document.getElementById('people-feedback').textContent,closedMessage,'late failures cannot overwrite closure');
  assert.equal(closing.document.getElementById('people-credential').disabled,false);
  assert.equal(closing.document.body.textContent.includes('owner-fixture'),false);
  await closing.window.happyDOM.close();
}

// Outer auth errors have no People outcome: perform one access read, never a retry.
for (const [status,code,readStatus] of [[401,'unauthorized',401],[403,'forbidden',403],[403,'forbidden',200]]) {
  const denied = createPage(credential);
  await settle();
  denied.setHandler(async url => url === '/people/actions'
    ? result(status,{ok:false,error:{code,message:'Access denied'}})
    : readStatus === 200 ? result(200,fixture())
      : result(readStatus,{ok:false,error:{code:readStatus === 401 ? 'unauthorized' : 'forbidden'}}));
  denied.document.getElementById('people-add').click();
  field(denied,'id','retained-form-id');
  field(denied,'role','tester');
  const retainedForm = editForm(denied);
  submit(denied,retainedForm);
  await settle();
  assert.equal(posts(denied).length,1);
  assert.equal(denied.calls.length,3,'one POST refusal leads to exactly one access GET');
  assert.equal(denied.document.getElementById('people-feedback').textContent.includes('outcome unknown'),false);
  if (readStatus === 200) {
    assert.equal(denied.document.getElementById('people-workspace').hidden,false);
    assert.equal(editForm(denied),retainedForm,'target refusal preserves the admitted actor’s form');
    assert.equal(editForm(denied).elements.namedItem('id').value,'retained-form-id');
    assert.match(editForm(denied).querySelector('.form-feedback').textContent,/refused/i);
  } else {
    assert.equal(denied.document.getElementById('people-workspace').hidden,true);
    assert.equal(denied.document.getElementById('people-roster').childElementCount,0);
    assert.equal(denied.document.getElementById('people-credential').required,true);
    assert.match(denied.document.getElementById('people-feedback').textContent,/cannot open People/);
  }
  await denied.window.happyDOM.close();
}

// A different 403 code must not be treated as loss of owner access.
const origin = createPage(credential);
await settle();
origin.setHandler(async () => result(403,{ok:false,error:{code:'origin_denied',message:'Cross-origin admin requests are not allowed.'}}));
origin.document.getElementById('people-add').click();
field(origin,'id','origin-refused-person');
field(origin,'role','tester');
submit(origin,editForm(origin));
await settle();
assert.equal(origin.calls.length,2,'an unrelated refusal does not trigger an auth classification read');
assert.equal(origin.document.getElementById('people-workspace').hidden,false);
await origin.window.happyDOM.close();

// An unknown write followed by access denial stays unknown, including its causality.
const unknown = createPage(credential);
await settle();
unknown.setHandler(async url => url === '/people/actions'
  ? result(503,{ok:false,error:{code:'uncertain',outcome:'uncertain'}})
  : result(403,{ok:false,error:{code:'forbidden'}}));
targetAction(unknown,'revoke_credential','current-device');
editForm(unknown).elements.namedItem('confirm').checked = true;
submit(unknown,editForm(unknown));
await settle();
assert.equal(unknown.calls.length,2);
unknown.document.getElementById('people-refresh').click();
await settle();
assert.equal(posts(unknown).length,1);
assert.equal(unknown.document.getElementById('people-workspace').hidden,true);
assert.match(unknown.document.getElementById('people-feedback').textContent,/earlier request.*still unconfirmed/i);
assert.match(unknown.document.getElementById('people-feedback').textContent,/does not establish whether that change happened/i);
assert.equal(unknown.document.getElementById('people-feedback').textContent.includes('Change confirmed'),false);
await unknown.window.happyDOM.close();

// Omitted policy facts are read failures, never default-enabled/default-unrevoked data.
const incomplete = createPage(credential);
await settle();
const completeRoster = {node:incomplete.document.getElementById('people-roster').firstElementChild,text:incomplete.document.getElementById('people-roster').textContent};
for (const [collection,fields] of [
  ['principals',['role','projects','disabled','has_delegations']],
  ['agents',['project_id','disabled']],
  ['devices',['agent_id','revoked','can_revoke']]
]) {
  for (const fieldName of fields) {
    const missing = fixture();
    delete missing.roster[collection][0][fieldName];
    incomplete.setHandler(async () => result(200,missing));
    incomplete.document.getElementById('people-refresh').click();
    await settle();
    assert.equal(incomplete.document.getElementById('people-roster').firstElementChild,completeRoster.node,collection + '.' + fieldName + ' omission retains the roster nodes');
    assert.equal(incomplete.document.getElementById('people-roster').textContent,completeRoster.text,collection + '.' + fieldName + ' omission preserves the last good facts');
    assert.equal(incomplete.document.getElementById('people-add').disabled,true);
    assert.match(incomplete.document.getElementById('people-feedback').textContent,/Couldn’t read people/);
  }
}
await incomplete.window.happyDOM.close();

// Leaving Product clears existing product delegations; show this before confirmation.
const delegations = createPage(credential);
await settle();
const delegated = fixture();
delegated.roster.principals[1].role = 'product';
delegated.roster.principals[1].has_delegations = true;
delegations.setHandler(async () => result(200,delegated));
delegations.document.getElementById('people-refresh').click();
await settle();
action(delegations,'change_role','second-person');
field(delegations,'role','engineer');
assert.match(delegations.document.getElementById('people-consequence').textContent,/Existing product delegations.*will be cleared/);
assert.equal(editForm(delegations).elements.namedItem('confirm').checked,false);
field(delegations,'role','product');
assert.equal(delegations.document.getElementById('people-consequence').textContent.includes('will be cleared'),false);
assert.equal(posts(delegations).length,0);
await delegations.window.happyDOM.close();

await anonymous.window.happyDOM.close();
await app.window.happyDOM.close();
`
