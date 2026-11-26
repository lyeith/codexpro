package admin

const peopleStyle = `
:root { color-scheme: light; --ink:#202a34; --muted:#63707c; --line:#e1e7eb; --paper:#fff; --teal:#176b60; --teal-light:#e7f4f0; --danger:#944b43; --warning:#775a27; }
* { box-sizing:border-box; }
[hidden] { display:none !important; }
body { margin:0; background:#f6f8fa; color:var(--ink); font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
button,input,select { font:inherit; }
button,a,input,select { -webkit-tap-highlight-color:transparent; }
:focus-visible { outline:3px solid #348b81; outline-offset:3px; }
a { color:var(--teal); text-underline-offset:3px; }
button { cursor:pointer; }
button:disabled { cursor:not-allowed; opacity:.55; }
.people-main { width:min(1240px,calc(100% - 80px)); margin:auto; padding:28px 0 36px; }
.page-bar,.page-heading,.section-heading,.self-panel,.detail-heading { display:flex; justify-content:space-between; align-items:center; gap:20px; }
.page-bar { margin-bottom:34px; }
.brand { display:inline-flex; align-items:center; gap:10px; font-size:16px; font-weight:650; letter-spacing:-.02em; }
.brand-mark { width:17px; height:17px; border:2px solid var(--teal); border-radius:4px; box-shadow:inset 0 0 0 3px #f6f8fa; background:var(--teal); }
.brand-divider { color:#b2bcc5; margin:0 3px; font-weight:400; }
.brand-area { color:var(--muted); font-size:14px; font-weight:400; }
.page-bar nav { display:flex; gap:22px; }
.page-bar nav a { font-size:13px; text-decoration:none; padding:8px 0; }
.eyebrow { color:var(--muted); font-size:11px; font-weight:650; letter-spacing:.025em; margin:0 0 6px; }
h1 { margin:0; font-size:36px; line-height:1.2; letter-spacing:-.035em; font-weight:650; }
h2 { margin:0; font-size:19px; letter-spacing:-.02em; font-weight:650; }
h3 { margin:0; font-size:14px; font-weight:650; }
.lead { color:var(--muted); margin:10px 0 0; font-size:14px; }
.page-actions,.form-actions,.inline-actions { display:flex; flex-wrap:wrap; align-items:center; gap:10px; }
.button { min-height:40px; padding:8px 15px; border:1px solid #ccd6dc; border-radius:7px; background:var(--paper); color:var(--ink); font-weight:550; white-space:nowrap; }
.button:hover:not(:disabled) { border-color:#97aaa5; background:#f0f6f4; }
.button.primary { background:var(--teal); border-color:var(--teal); color:#fff; }
.button.primary:hover:not(:disabled) { background:#12594f; }
.button.danger { color:var(--danger); border-color:#e4c8c3; background:#fff; }
.button.danger:hover:not(:disabled) { background:#fbf1ef; border-color:#bd8277; }
.text-button { border:0; padding:7px 0; background:transparent; color:var(--teal); font-weight:550; text-align:left; }
.text-button.danger { color:var(--danger); }
.panel { border:1px solid var(--line); border-radius:12px; background:var(--paper); }
.feedback { padding:12px 15px; margin:23px 0 20px; color:var(--muted); border:1px solid var(--line); background:#fff; border-radius:8px; }
.feedback:empty { display:none; }
.feedback.warning,.consequence.warning { color:var(--warning); background:#fcf8ed; border-color:#e8ddc0; }
.feedback.error { color:#874a44; background:#fbf4f3; border-color:#eeddda; }
.feedback.success { color:#195c50; background:#f0f8f4; border-color:#cfe4db; }
.sign-in { max-width:620px; padding:28px; margin-top:24px; }
.sign-in p:not(.eyebrow) { color:var(--muted); }
.sign-in form { margin-top:23px; }
label { display:block; font-size:12px; font-weight:600; margin-bottom:7px; }
input:not([type=checkbox]),select { width:100%; min-height:42px; padding:9px 11px; border:1px solid #cbd5db; border-radius:6px; background:#fff; color:var(--ink); }
input:disabled,select:disabled { background:#f3f5f7; }
.field-note,.muted { color:var(--muted); font-size:12px; }
.field-note { margin:7px 0 0; overflow-wrap:anywhere; }
.sign-in .button { margin-top:18px; }
.self-panel { padding:17px 21px; margin-bottom:20px; border-left:3px solid #91b9ab; background:#edf4f1; border-radius:0 9px 9px 0; }
.self-panel .eyebrow { color:#547569; margin-bottom:3px; }
.self-name { margin:0; font-size:14px; font-weight:600; overflow-wrap:anywhere; }
.self-panel .field-note { margin-top:3px; }
.editor { padding:24px; margin-bottom:20px; border-color:#aac5bb; }
.editor-heading { display:flex; justify-content:space-between; align-items:flex-start; gap:18px; margin-bottom:18px; }
.editor-description { margin:6px 0 0; color:var(--muted); font-size:13px; overflow-wrap:anywhere; }
.form-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:18px 22px; max-width:850px; }
.form-field { min-width:0; }
.form-actions { margin-top:20px; }
.form-feedback { margin:13px 0 0; color:var(--danger); font-size:13px; }
.form-feedback:empty { display:none; }
.consequence { margin:20px 0 0; padding:13px 15px; border:1px solid var(--line); border-radius:7px; font-size:13px; background:#f8fafb; max-width:850px; }
.confirm-label { display:flex; align-items:flex-start; gap:9px; margin-top:14px; font-size:13px; font-weight:500; }
.confirm-label input { margin:4px 0 0; width:16px; height:16px; flex-shrink:0; }
.review-note { margin-top:14px; padding:12px 14px; background:#fcf8ed; color:var(--warning); border-radius:6px; }
.review-note p { margin:0 0 8px; font-size:12px; }
.roster-panel { overflow:hidden; }
.section-heading { padding:23px 25px; align-items:flex-start; }
.section-heading .muted { margin:7px 0 0; }
.count-label { white-space:nowrap; color:var(--muted); font-size:12px; padding-top:4px; font-variant-numeric:tabular-nums; }
.roster-table { border-collapse:collapse; width:100%; table-layout:fixed; }
.roster-table thead th { padding:11px 18px; border-top:1px solid var(--line); border-bottom:1px solid var(--line); background:#fafbfc; color:var(--muted); font-size:11px; font-weight:600; text-align:left; }
.roster-table thead th:first-child { width:34%; padding-left:25px; }
.roster-row > th,.roster-row > td { border-bottom:1px solid var(--line); padding:17px 18px; vertical-align:middle; text-align:left; font-size:13px; overflow-wrap:anywhere; }
.roster-row > th:first-child { padding-left:25px; }
.person-toggle { display:inline-flex; align-items:center; gap:10px; max-width:100%; min-height:40px; padding:5px 7px; margin-left:-7px; border:0; background:transparent; color:var(--ink); border-radius:5px; text-align:left; }
.person-toggle:hover { background:#f0f4f5; }
.person-toggle code { min-width:0; overflow-wrap:anywhere; font-size:13px; font-weight:600; }
.chevron { width:6px; height:6px; border-top:1.5px solid #71838f; border-right:1.5px solid #71838f; transform:rotate(45deg); flex-shrink:0; }
.person-toggle[aria-expanded=true] .chevron { transform:rotate(135deg); }
.badge { display:inline-block; font-size:10px; font-weight:550; padding:4px 8px; border-radius:99px; background:#f0f3f5; color:#65727c; white-space:nowrap; }
.badge.enabled { color:#246456; background:#eaf5ef; }
.badge.disabled { color:#826850; background:#f6efe6; }
.badge.you { margin-left:8px; color:#1d6657; background:#e5f1ec; }
.person-kind { margin:2px 0 0 16px; color:var(--muted); font-size:10px; font-weight:400; }
.numeric { font-variant-numeric:tabular-nums; }
.person-details > td { padding:0; border-bottom:1px solid var(--line); }
.person-detail-panel { background:#f8fafb; padding:22px 25px 25px; }
.detail-heading { align-items:flex-start; flex-wrap:wrap; margin-bottom:20px; }
.detail-heading .field-note { max-width:650px; }
.metadata { display:flex; flex-wrap:wrap; gap:18px 35px; margin:0 0 22px; }
.metadata div { min-width:0; max-width:100%; }
.metadata dt { color:var(--muted); font-size:11px; margin-bottom:3px; }
.metadata dd { margin:0; font-size:13px; overflow-wrap:anywhere; }
.detail-section + .detail-section { border-top:1px solid var(--line); padding-top:20px; margin-top:22px; }
.detail-section-heading { display:flex; align-items:center; justify-content:space-between; gap:14px; margin-bottom:12px; }
.detail-list { margin:0; padding:0; list-style:none; display:grid; gap:10px; }
.detail-item { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:14px; border:1px solid var(--line); border-radius:7px; background:#fff; padding:13px 15px; }
.detail-item h4 { margin:0 0 5px; font-size:13px; font-weight:600; overflow-wrap:anywhere; }
.detail-item .item-meta { display:flex; flex-wrap:wrap; align-items:center; gap:5px 14px; color:var(--muted); font-size:11px; }
.detail-item .item-meta span { min-width:0; overflow-wrap:anywhere; }
.detail-item .item-binding { margin:6px 0 0; font-size:11px; color:var(--muted); overflow-wrap:anywhere; }
.detail-item .text-button { align-self:start; font-size:12px; }
.empty-note { margin:8px 0; color:var(--muted); font-size:12px; }
.roster-empty { padding:30px 25px; border-top:1px solid var(--line); color:var(--muted); }
.page-foot { color:var(--muted); font-size:11px; margin:15px 2px 0; }
.visually-hidden { position:absolute; width:1px; height:1px; padding:0; margin:-1px; overflow:hidden; clip:rect(0,0,0,0); white-space:nowrap; border:0; }
.skip-link { position:absolute; left:12px; top:-60px; padding:8px 12px; background:#fff; z-index:2; }
.skip-link:focus { top:10px; }
@media(max-width:900px) { .people-main { width:calc(100% - 40px); } .roster-row > th,.roster-row > td,.roster-table thead th { padding-left:13px; padding-right:13px; } }
@media(max-width:680px) {
 .people-main { width:calc(100% - 32px); padding-top:20px; }
 .page-bar { margin-bottom:28px; gap:12px; flex-wrap:wrap; }
 .page-bar nav { gap:18px; }
 .brand { font-size:15px; gap:8px; }
 .page-heading { display:block; }
 h1 { font-size:32px; }
 .page-actions { margin-top:20px; }
 .page-actions .primary { margin-left:auto; }
 .sign-in,.editor { padding:20px; }
 .self-panel { padding:15px 16px; align-items:flex-start; }
 .self-panel .text-button { font-size:11px; max-width:72px; padding-top:0; }
 .form-grid { grid-template-columns:1fr; gap:16px; }
 .form-actions .button { flex:1; }
 .section-heading { padding:19px 0; }
 .roster-panel { border:0; border-radius:0; background:transparent; overflow:visible; }
 .count-label { font-size:11px; }
 .roster-table,.roster-table tbody { display:block; }
 .roster-table thead { position:absolute; width:1px; height:1px; padding:0; margin:-1px; overflow:hidden; clip:rect(0,0,0,0); white-space:nowrap; border:0; }
 .person-entry { border:1px solid var(--line); border-radius:10px; background:#fff; overflow:hidden; margin-bottom:12px; }
 .roster-row { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); }
 .roster-row > th,.roster-row > td { display:block; border:0; min-width:0; padding:12px 16px; }
 .roster-row > th:first-child { grid-column:1/-1; padding:13px 16px 8px; }
 .roster-row > td::before { content:attr(data-label); display:block; color:var(--muted); font-size:10px; margin-bottom:5px; }
 .roster-row > td { border-top:1px solid var(--line); }
 .roster-row > td:nth-child(even) { border-right:1px solid var(--line); }
 .person-toggle code { font-size:14px; }
 .person-details,.person-details > td { display:block; border:0; }
 .person-detail-panel { border-top:1px solid var(--line); padding:18px 16px; }
 .detail-heading { margin-bottom:18px; gap:14px; }
 .inline-actions { gap:5px 16px; }
 .metadata { gap:14px 25px; }
 .detail-item { padding:12px; grid-template-columns:minmax(0,1fr); gap:4px; }
 .detail-item .text-button { justify-self:start; }
 .detail-section-heading { align-items:flex-start; }
 .detail-section-heading .text-button { font-size:12px; padding-top:0; }
 .feedback { font-size:12px; }
}
@media(prefers-reduced-motion:reduce) { *,*::before,*::after { scroll-behavior:auto !important; } }
`

const peopleScript = `
(function () {
"use strict";
const byID = (id) => document.getElementById(id);
const storageKey = "codexpro.activity.credential";
let credential = "";
try { credential = sessionStorage.getItem(storageKey) || ""; } catch {}
const state = { data: null, expanded: new Set(), loading: false, readGeneration: 0, saving: false, fresh: false, uncertain: false, editor: null };
const roles = { owner: "Owner", engineer: "Engineer", product: "Product", tester: "Tester" };
const lastOwnerMessage = "Plans needs at least one enabled owner.";
const actionLabels = { add_person: "Add person", change_role: "Change role", disable_person: "Disable person", register_agent: "Register agent", retire_agent: "Retire agent", revoke_credential: "Revoke credential" };
const hasOwn = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
function node(tag, attrs, children) {
  const result = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value !== false && value !== null && value !== undefined) result.setAttribute(key, value === true ? "" : String(value));
  }
  for (const child of Array.isArray(children) ? children : [children]) {
    if (child !== undefined && child !== null) result.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return result;
}
function feedback(text, tone) {
  const target = byID("people-feedback");
  target.textContent = text;
  target.className = "feedback" + (tone ? " " + tone : "");
}
function clone(value) { return JSON.parse(JSON.stringify(value)); }
function isRecord(value) { return value !== null && typeof value === "object" && !Array.isArray(value); }
function recordShape(value, fields) {
  return isRecord(value) && Object.keys(value).every((key) => hasOwn(fields, key)) &&
    Object.entries(fields).every(([key, kind]) => kind(value[key]));
}
const textValue = (v) => typeof v === "string";
const boolValue = (v) => typeof v === "boolean";
const textsValue = (v) => Array.isArray(v) && v.every(textValue);
function validRead(value) {
  if (!isRecord(value) || value.ok !== true || !isRecord(value.roster)) return false;
  if (!recordShape(value.self, {principal_id:textValue, agent_id:textValue, credential_id:textValue, role:textValue})) return false;
  const roster = value.roster;
  return Array.isArray(roster.principals) && Array.isArray(roster.agents) && Array.isArray(roster.devices) &&
    roster.principals.every((p) => recordShape(p, {id:textValue, type:textValue, role:textValue, projects:textsValue, disabled:boolValue, has_delegations:boolValue})) &&
    roster.agents.every((a) => recordShape(a, {id:textValue, principal_id:textValue, kind:textValue, address:textValue, project_id:textValue, disabled:boolValue})) &&
    roster.devices.every((d) => recordShape(d, {id:textValue, principal_id:textValue, agent_id:textValue, device:textValue, audience:textValue, revoked:boolValue, can_revoke:boolValue}));
}
function sorted(items) {
  return items.slice().sort((a, b) => a.id.localeCompare(b.id, "en", {numeric:true, sensitivity:"base"}) || a.id.localeCompare(b.id, "en"));
}
function badge(text, tone) { return node("span", {class:"badge" + (tone ? " " + tone : "")}, text); }
function policyBadge(disabled) { return badge(disabled ? "Disabled" : "Enabled", disabled ? "disabled" : "enabled"); }
function actionButton(kind, id, label, danger, blockedReason) {
  const button = node("button", {type:"button", class:"text-button" + (danger ? " danger" : ""), "data-people-action":kind, "data-people-locked":blockedReason || null, disabled:!!blockedReason, title:blockedReason || null}, label);
  button.addEventListener("click", () => openEditor(kind, id, button));
  return button;
}
function fact(label, value) {
  return node("div", {}, [node("dt", {}, label), node("dd", {}, value)]);
}
function renderPersonDetails(person) {
  const personal = person.type === "person";
  const roster = state.data.roster;
  const agents = sorted(roster.agents.filter((item) => item.principal_id === person.id));
  const devices = sorted(roster.devices.filter((item) => item.principal_id === person.id));
  const panel = node("div", {class:"person-detail-panel", role:"region", "aria-label":"Details for " + person.id});
  const actions = node("div", {class:"inline-actions"});
  if (personal) {
    const ownerLock = lastOwnerAction("change_role", person) ? lastOwnerMessage : "";
    actions.append(actionButton("change_role", person.id, "Change role", false, ownerLock));
    if (!person.disabled) actions.append(actionButton("disable_person", person.id, "Disable person", true, ownerLock));
    if (ownerLock) actions.append(node("p", {class:"field-note"}, ownerLock));
  }
  panel.append(node("div", {class:"detail-heading"}, [
    node("div", {}, [node("h3", {}, personal ? "Access details" : "System registration"), node("p", {class:"field-note"}, personal ? "Registered policy records for this person." : "System registrations are read-only in People.")]),
    actions
  ]));
  const projects = (person.projects || []).join(", ") || "None recorded";
  const metadata = [fact("Person ID", person.id), fact("Policy status", person.disabled ? "Disabled" : "Enabled"), fact("Existing projects", projects)];
  if (person.has_delegations) metadata.push(fact("Delegations", "Recorded · managed separately"));
  panel.append(node("dl", {class:"metadata"}, metadata));
  const agentSection = node("section", {class:"detail-section"});
  const agentHeading = node("div", {class:"detail-section-heading"}, node("h3", {}, "Registered agents"));
  if (personal && !person.disabled) agentHeading.append(actionButton("register_agent", person.id, "Register agent"));
  agentSection.append(agentHeading);
  if (!agents.length) agentSection.append(node("p", {class:"empty-note"}, "No registered agents."));
  else {
    const list = node("ul", {class:"detail-list"});
    for (const agent of agents) {
      const info = node("div", {}, [
        node("h4", {}, agent.id),
        node("div", {class:"item-meta"}, [node("span", {}, agent.kind), policyBadge(agent.disabled), node("span", {}, agent.project_id ? "Project: " + agent.project_id : "No project binding")]),
        node("p", {class:"item-binding"}, "Address: " + agent.address)
      ]);
      const item = node("li", {class:"detail-item"}, info);
      if (personal && !agent.disabled && (agent.kind === "concierge" || agent.kind === "crew")) item.append(actionButton("retire_agent", agent.id, "Retire agent", true));
      list.append(item);
    }
    agentSection.append(list);
  }
  panel.append(agentSection);
  const deviceSection = node("section", {class:"detail-section"}, [
    node("div", {class:"detail-section-heading"}, node("h3", {}, "Credentials")),
    node("p", {class:"field-note"}, "Not revoked does not guarantee that a credential can be used; its person and agent bindings also apply.")
  ]);
  if (!devices.length) deviceSection.append(node("p", {class:"empty-note"}, "No credentials recorded."));
  else {
    const list = node("ul", {class:"detail-list"});
    for (const device of devices) {
      const info = node("div", {}, [
        node("h4", {}, device.device || device.id),
        node("div", {class:"item-meta"}, [node("span", {}, "ID: " + device.id), node("span", {}, "Audience: " + device.audience), badge(device.revoked ? "Revoked" : "Not revoked", device.revoked ? "disabled" : "")]),
        node("p", {class:"item-binding"}, device.agent_id ? "Registered agent: " + device.agent_id : "No registered agent binding")
      ]);
      const item = node("li", {class:"detail-item"}, info);
      if (personal && !device.revoked && device.can_revoke === true) item.append(actionButton("revoke_credential", device.id, "Revoke", true));
      list.append(item);
    }
    deviceSection.append(list);
  }
  panel.append(deviceSection);
  return panel;
}
function renderPeople() {
  const {self, roster} = state.data;
  byID("people-auth").hidden = true;
  byID("people-workspace").hidden = false;
  byID("people-controls").hidden = false;
  byID("people-self").textContent = self.principal_id + " · " + (roles[self.role] || self.role);
  byID("people-self-binding").textContent = "Concierge: " + self.agent_id + " · Credential: " + self.credential_id;
  const peopleCount = roster.principals.filter((p) => p.type === "person").length;
  const systemCount = roster.principals.length - peopleCount;
  byID("people-count").textContent = peopleCount + (peopleCount === 1 ? " person" : " people") + (systemCount ? " · " + systemCount + " system" : "");
  const target = byID("people-roster");
  const focused = document.activeElement;
  const focusedEntry = focused && focused.closest("[data-person-id]");
  const focusID = focusedEntry && focusedEntry.dataset.personId;
  if (!roster.principals.length) {
    target.replaceChildren(node("p", {class:"roster-empty"}, "No people or registrations are recorded."));
    return;
  }
  const table = node("table", {class:"roster-table", "aria-describedby":"people-roster-note"}, [
    node("caption", {class:"visually-hidden"}, "People and registered system identities"),
    node("thead", {}, node("tr", {}, ["Person ID", "Role", "Policy status", "Agents", "Credentials"].map((name) => node("th", {scope:"col"}, name))))
  ]);
  const entries = [];
  sorted(roster.principals).forEach((person, index) => {
    const detailID = "people-detail-" + index;
    const expanded = state.expanded.has(person.id);
    const entry = node("tbody", {class:"person-entry", "data-person-id":person.id});
    const toggle = node("button", {type:"button", class:"person-toggle", "aria-expanded":String(expanded), "aria-controls":detailID, "aria-label":(expanded ? "Hide" : "Show") + " details for " + person.id}, [node("span", {class:"chevron", "aria-hidden":"true"}), node("code", {}, person.id)]);
    const name = node("th", {scope:"row"}, toggle);
    if (person.id === self.principal_id) name.append(badge("You", "you"));
    if (person.type !== "person") name.append(node("p", {class:"person-kind"}, "System · read only"));
    const row = node("tr", {class:"roster-row"}, [
      name,
      node("td", {"data-label":"Role"}, roles[person.role] || person.role || "System"),
      node("td", {"data-label":"Policy status"}, policyBadge(person.disabled)),
      node("td", {"data-label":"Registered agents", class:"numeric"}, roster.agents.filter((a) => a.principal_id === person.id).length),
      node("td", {"data-label":"Credentials", class:"numeric"}, roster.devices.filter((d) => d.principal_id === person.id).length)
    ]);
    const details = node("tr", {class:"person-details", id:detailID, hidden:!expanded}, node("td", {colspan:"5"}, renderPersonDetails(person)));
    toggle.addEventListener("click", () => {
      const open = toggle.getAttribute("aria-expanded") !== "true";
      if (open) state.expanded.add(person.id); else state.expanded.delete(person.id);
      toggle.setAttribute("aria-expanded", String(open));
      toggle.setAttribute("aria-label", (open ? "Hide" : "Show") + " details for " + person.id);
      details.hidden = !open;
    });
    entry.append(row, details);
    table.append(entry);
    entries.push({id:person.id, toggle});
  });
  target.replaceChildren(table);
  if (focusID !== null && focusID !== undefined) entries.find((entry) => entry.id === focusID)?.toggle.focus({preventScroll:true});
}
function targetRecord(kind, id) {
  if (!state.data) return null;
  const roster = state.data.roster;
  if (kind === "add_person") return null;
  const list = kind === "retire_agent" ? roster.agents : kind === "revoke_credential" ? roster.devices : roster.principals;
  return list.find((item) => item.id === id) || null;
}

function lastOwnerAction(kind, record) {
  if (!record || !state.data || (kind !== "disable_person" && kind !== "change_role") ||
      record.type !== "person" || record.role !== "owner" || record.disabled) return false;
  return !state.data.roster.principals.some((person) =>
    person.id !== record.id && person.type === "person" && person.role === "owner" && !person.disabled);
}
function actionAvailable(kind, record) {
  if (kind === "add_person") return true;
  if (!record) return false;
  if (kind === "revoke_credential") return record.can_revoke === true && !record.revoked;
  if (kind === "retire_agent") return !record.disabled && (record.kind === "concierge" || record.kind === "crew") &&
    state.data.roster.principals.some((person) => person.id === record.principal_id && person.type === "person");
  return record.type === "person" && !lastOwnerAction(kind, record) && (kind === "change_role" || !record.disabled);
}
function closeEditor(restoreFocus) {
  const previous = state.editor;
  state.editor = null;
  byID("people-editor").replaceChildren();
  byID("people-editor").hidden = true;
  if (restoreFocus) {
    if (previous?.returnFocus?.isConnected) previous.returnFocus.focus({preventScroll:true});
    else byID("people-add").focus({preventScroll:true});
  }
}
function lockView(message) {
  state.readGeneration++;
  state.loading = false;
  credential = "";
  state.data = null;
  state.fresh = false;
  state.expanded.clear();
  closeEditor(false);
  byID("people-roster").replaceChildren();
  byID("people-self").textContent = "";
  byID("people-self-binding").textContent = "";
  byID("people-count").textContent = "";
  byID("people-workspace").hidden = true;
  byID("people-controls").hidden = true;
  byID("people-auth").hidden = false;
  byID("people-credential").value = "";
  const uncertainty = state.uncertain ? " The earlier request’s outcome is still unconfirmed. Access state does not establish whether that change happened." : "";
  feedback(message + uncertainty, "warning");
  updateControls();
}
function updateControls() {
  const busy = state.loading || state.saving;
  const allowed = !!state.data && state.fresh && !busy;
  byID("people-refresh").disabled = busy || !credential;
  byID("people-add").disabled = !allowed;
  byID("people-close").disabled = state.saving;
  document.querySelectorAll("[data-people-action]").forEach((button) => { button.disabled = !allowed || button.hasAttribute("data-people-locked"); });
  const authForm = byID("people-auth-form");
  authForm.querySelector("button").disabled = busy;
  authForm.querySelector("button").textContent = busy ? "Reading…" : credential ? "Retry read" : "Open People";
  byID("people-credential").disabled = busy;
  byID("people-credential").required = !credential;
  byID("people-workspace").setAttribute("aria-busy", String(busy));
  if (state.editor) {
    const editor = state.editor;
    const ownerBlocked = lastOwnerAction(editor.kind, targetRecord(editor.kind, editor.targetID));
    editor.form.querySelectorAll("input,select").forEach((field) => { field.disabled = state.saving || ownerBlocked; });
    editor.cancel.disabled = state.saving;
    editor.submit.disabled = !allowed || editor.stale || ownerBlocked;
    if (ownerBlocked) editor.status.textContent = lastOwnerMessage;
    editor.review.disabled = !allowed || !actionAvailable(editor.kind, targetRecord(editor.kind, editor.targetID));
    editor.submit.textContent = state.saving ? "Saving…" : actionLabels[editor.kind];
  }
}
function checkEditorSnapshot() {
  const editor = state.editor;
  if (!editor || editor.kind === "add_person") return;
  const current = targetRecord(editor.kind, editor.targetID);
  editor.stale = !actionAvailable(editor.kind, current) || JSON.stringify(current) !== JSON.stringify(editor.expected);
  editor.reviewBox.hidden = !editor.stale;
  if (!current) editor.status.textContent = "This record is no longer available. Close this form and review the roster.";
  else if (!actionAvailable(editor.kind, current)) editor.status.textContent = "The current record no longer allows this action. Close this form and review current details.";
}
async function readPeople(options) {
  options = options || {};
  if (!credential || state.loading || (state.saving && !options.afterAction)) return false;
  const generation = ++state.readGeneration;
  state.loading = true;
  feedback(options.confirmed ? "Change confirmed. Refreshing current access…" : "Reading current people and registrations…", "");
  updateControls();
  try {
    const response = await fetch("/people/data", {method:"GET", headers:{Authorization:"Bearer " + credential}, credentials:"omit", cache:"no-store", redirect:"error"});
    if (generation !== state.readGeneration) return false;
    if (response.status === 401 || response.status === 403) {
      lockView(options.confirmed ? "Change confirmed. This credential no longer has access to People." : "This credential cannot open People. Use a current personal owner credential bound to an enabled concierge.");
      return false;
    }
    const data = await response.json().catch(() => null);
    if (generation !== state.readGeneration) return false;
    if (!response.ok || !validRead(data)) throw new Error("read_unavailable");
    state.data = data;
    state.fresh = true;
    renderPeople();
    checkEditorSnapshot();
    if (state.uncertain) feedback("Current state reloaded. The earlier request’s outcome is still unconfirmed; a matching record does not establish what caused it.", "warning");
    else feedback(options.confirmed ? "Change confirmed. Current access is shown below." : "Current policy records loaded.", options.confirmed ? "success" : "");
    return true;
  } catch {
    if (generation !== state.readGeneration) return false;
    state.fresh = false;
    const suffix = state.data ? " The last roster is retained; refresh before making another change." : " No roster is available.";
    const uncertainty = state.uncertain ? " The earlier request’s outcome is still unconfirmed." : "";
    feedback((options.confirmed ? "Change confirmed, but current records could not be read." : "Couldn’t read people and registrations.") + suffix + uncertainty, "error");
    return false;
  } finally {
    if (generation === state.readGeneration) {
      state.loading = false;
      updateControls();
    }
  }
}
function inputField(form, name, label, options) {
  options = options || {};
  const id = "people-field-" + name;
  const field = node("div", {class:"form-field"}, node("label", {for:id}, label));
  let input;
  if (options.choices) {
    input = node("select", {id, name, required:!!options.required});
    for (const choice of options.choices) input.append(node("option", {value:choice[0]}, choice[1]));
  } else input = node("input", {id, name, type:"text", required:!!options.required, maxlength:200, autocomplete:"off", autocapitalize:"none", spellcheck:"false"});
  if (options.value !== undefined) input.value = options.value;
  field.append(input);
  if (options.note) {
    const noteID = id + "-note";
    field.append(node("p", {class:"field-note", id:noteID}, options.note));
    input.setAttribute("aria-describedby", noteID);
  }
  form.append(field);
  return input;
}
function consequence(kind, record, form) {
  const self = state.data.self;
  let words = "";
  if (kind === "change_role") {
    const nextRole = form.elements.namedItem("role").value;
    words = "Change " + record.id + " from " + (roles[record.role] || record.role) + " to " + (roles[nextRole] || nextRole) + ".";
    if (record.id === self.principal_id && nextRole !== "owner") words += " You are changing your own role; this will end your access to People.";
    if (record.has_delegations && nextRole !== "product") words += " Existing product delegations for this person will be cleared.";
  } else if (kind === "disable_person") {
    words = "Disable " + record.id + ". Their credentials will no longer authenticate while this person is disabled.";
    if (record.id === self.principal_id) words += " This will end your access to People.";
  } else if (kind === "retire_agent") {
    words = "Retire " + record.id + ". This disables the registered agent and credentials bound to it will no longer authenticate.";
    if (record.id === self.agent_id) words += " This is your current concierge; this page will stop working.";
  } else if (kind === "revoke_credential") {
    words = "Revoke credential " + record.id + (record.device ? " on " + record.device : "") + ". It will no longer authenticate.";
    if (record.id === self.credential_id) words += " This is your current credential; this page will stop working.";
  }
  return words;
}
function openEditor(kind, id, returnFocus, restored) {
  if (!state.data || !state.fresh || state.saving || state.loading) return;
  const record = targetRecord(kind, id);
  if (!actionAvailable(kind, record)) return;
  const panel = byID("people-editor");
  closeEditor(false);
  panel.hidden = false;
  const heading = node("h2", {id:"people-editor-title", tabindex:"-1"}, actionLabels[kind]);
  const description = kind === "add_person" ? "Create a personal registration. No project access or credential is issued." :
    kind === "register_agent" ? "Register a personal agent for " + record.id + ". This does not issue a credential." :
    "Review the change to " + record.id + " before saving.";
  const cancel = node("button", {type:"button", class:"text-button"}, "Cancel");
  cancel.addEventListener("click", () => closeEditor(true));
  panel.append(node("div", {class:"editor-heading"}, [node("div", {}, [heading, node("p", {class:"editor-description"}, description)]), cancel]));
  const form = node("form", {"data-people-form":kind});
  const grid = node("div", {class:"form-grid"});
  const roleChoices = Object.entries(roles).map(([key, value]) => [key, value]);
  if (kind === "add_person") {
    inputField(grid, "id", "Person ID", {required:true});
    inputField(grid, "role", "Role", {choices:[["","Choose a role"]].concat(roleChoices), required:true});
  } else if (kind === "change_role") {
    inputField(grid, "role", "New role", {choices:roleChoices, value:record.role, required:true, note:"Current role: " + (roles[record.role] || record.role)});
  } else if (kind === "register_agent") {
    inputField(grid, "id", "Registered agent ID", {required:true});
    inputField(grid, "kind", "Agent kind", {required:true, choices:[["","Choose a kind"],["concierge","Concierge"],["crew","Crew"]]});
    inputField(grid, "address", "Agentmail address", {required:true, note:"Use a personal address belonging to this person."});
    inputField(grid, "project_id", "Existing project (optional)", {choices:[["","No project binding"]].concat((record.projects || []).map((project) => [project,project])), note:"Selecting a project binds this agent to existing access. It does not grant project access."});
  }
  form.append(grid);
  if (restored) for (const [key, value] of Object.entries(restored)) {
    const input = form.elements.namedItem(key);
    if (!input || input.type === "checkbox") continue;
    if (input.tagName === "SELECT" && value && !Array.from(input.options).some((option) => option.value === value)) {
      input.append(node("option", {value, disabled:true}, "No longer available · " + value));
      input.setCustomValidity("Choose a currently available option.");
      input.addEventListener("change", () => input.setCustomValidity(""));
    }
    input.value = value;
  }
  let confirmation = null;
  const needsConfirmation = ["change_role","disable_person","retire_agent","revoke_credential"].includes(kind);
  if (needsConfirmation) {
    const consequences = node("p", {class:"consequence warning", id:"people-consequence"}, consequence(kind, record, form));
    confirmation = node("input", {type:"checkbox", name:"confirm", required:true, "aria-describedby":"people-consequence"});
    form.append(consequences, node("label", {class:"confirm-label"}, [confirmation, node("span", {}, "I understand this consequence and confirm the change.")]));
    form.addEventListener("input", (event) => {
      if (event.target !== confirmation) confirmation.checked = false;
      consequences.textContent = consequence(kind, record, form);
    });
  }
  const status = node("p", {class:"form-feedback", role:"status", "aria-live":"polite"});
  const review = node("button", {type:"button", class:"button"}, "Review current record");
  const reviewBox = node("div", {class:"review-note", hidden:true}, [node("p", {}, "This record changed. Review its current details before submitting; your entered values will be kept."), review]);
  const submit = node("button", {type:"submit", class:"button" + (needsConfirmation ? " danger" : " primary")}, actionLabels[kind]);
  form.append(reviewBox, status, node("div", {class:"form-actions"}, submit));
  panel.append(form);
  state.editor = {kind, targetID:id, expected:record ? clone(record) : null, form, status, submit, cancel, review, reviewBox, returnFocus, stale:false};
  review.addEventListener("click", () => {
    const values = Object.fromEntries(new FormData(form).entries());
    openEditor(kind, id, returnFocus, values);
  });
  form.addEventListener("submit", (event) => { event.preventDefault(); submitAction(); });
  updateControls();
  (form.querySelector("input:not([type=checkbox]),select") || heading).focus({preventScroll:false});
}
function actionFromEditor(editor) {
  const fields = Object.fromEntries(new FormData(editor.form).entries());
  const action = {kind:editor.kind};
  if (editor.kind === "add_person") return {...action,id:String(fields.id || "").trim(),role:fields.role};
  if (editor.kind === "register_agent") {
    const agent = {id:String(fields.id || "").trim(),principal_id:editor.targetID,kind:fields.kind,address:String(fields.address || "").trim()};
    if (fields.project_id) agent.project_id = fields.project_id;
    return {...action,agent,expected:editor.expected};
  }
  action.id = editor.targetID;
  action.expected = editor.expected;
  if (editor.kind === "change_role") action.role = fields.role;
  return action;
}
const refusalMessages = {
  invalid_request:"Some details are invalid. Check the fields and try again.",
  forbidden:"This change is not permitted for the current person, agent or credential.",
  not_found:"This record is no longer available. Refresh and review current details.",
  conflict:"This record changed. Refresh and review it before submitting again.",
  execution_held:"Execution is held for maintenance. No policy change was accepted.",
  unavailable:"The change could not be made. The owner is unavailable; no change was confirmed."
};
async function submitAction() {
  const editor = state.editor;
  if (!editor) return;
  if (lastOwnerAction(editor.kind, targetRecord(editor.kind, editor.targetID))) {
    editor.status.textContent = lastOwnerMessage;
    return;
  }
  if (!state.fresh || state.saving || state.loading || editor.stale || !editor.form.reportValidity()) return;
  const action = actionFromEditor(editor);
  if (action.kind === "change_role" && action.role === editor.expected.role) {
    editor.status.textContent = "Choose a different role before saving.";
    return;
  }
  state.saving = true;
  editor.status.textContent = "Saving…";
  updateControls();
  try {
    const response = await fetch("/people/actions", {method:"POST", headers:{Authorization:"Bearer " + credential,"Content-Type":"application/json"}, credentials:"omit", cache:"no-store", redirect:"error", body:JSON.stringify(action)});
    const body = await response.json().catch(() => null);
    if (response.ok && body?.ok === true && body.outcome === "confirmed") {
      state.uncertain = false;
      closeEditor(false);
      await readPeople({afterAction:true,confirmed:true});
      return;
    }
    const code = body?.error?.code;
    const outerAccessRefusal = body?.ok === false && isRecord(body.error) && !hasOwn(body.error, "outcome") &&
      ((response.status === 401 && code === "unauthorized") || (response.status === 403 && code === "forbidden"));
    const ownerRefusal = body?.ok === false && body.error?.outcome === "refused" && hasOwn(refusalMessages, code);
    if (outerAccessRefusal || ownerRefusal) {
      const refusal = outerAccessRefusal ? "This request was refused before the change was applied." : refusalMessages[code];
      editor.status.textContent = refusal;
      feedback("The change was refused. No change was confirmed.", "error");
      if (code === "conflict" || code === "not_found") state.fresh = false;
      if (outerAccessRefusal || code === "forbidden") {
        const admitted = await readPeople({afterAction:true});
        if (state.editor === editor) {
          editor.status.textContent = refusal;
          if (admitted && !state.uncertain) feedback("The change was refused. Your current access is available; review this form.", "error");
        }
      }
      return;
    }
    throw new Error("outcome_uncertain");
  } catch {
    state.uncertain = true;
    state.fresh = false;
    if (editor.form.elements.namedItem("confirm")) editor.form.elements.namedItem("confirm").checked = false;
    editor.status.textContent = "The outcome could not be confirmed. Refresh current state before deciding whether another change is needed.";
    feedback("Request outcome unknown. It will not be retried automatically. Refresh shows current state; it cannot establish what caused that state.", "warning");
  } finally {
    state.saving = false;
    updateControls();
  }
}
byID("people-auth-form").addEventListener("submit", (event) => {
  event.preventDefault();
  if (state.loading || state.saving) return;
  const entered = byID("people-credential").value.trim();
  if (entered) credential = entered;
  byID("people-credential").value = "";
  if (credential) readPeople();
});
byID("people-refresh").addEventListener("click", () => readPeople());
byID("people-add").addEventListener("click", () => openEditor("add_person", "", byID("people-add")));
byID("people-close").addEventListener("click", () => { lockView("This view is closed."); byID("people-credential").focus(); });
if (credential) readPeople();
})();
`
