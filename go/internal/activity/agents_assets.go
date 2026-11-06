package activity

const agentsStyle = `
:root { color-scheme: light; --ink: #202a34; --muted: #63707c; --line: #e1e7eb; --paper: #fff; --teal: #176b60; --teal-light: #e7f4f0; }
* { box-sizing: border-box; }
body { margin: 0; background: #f6f8fa; color: var(--ink); font: 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
button { font: inherit; }
button:focus-visible { outline: 3px solid #348b81; outline-offset: 3px; }
[hidden] { display: none !important; }
.agents-main { width: min(1320px, calc(100% - 80px)); margin: 0 auto; padding: 28px 0 22px; }
.page-bar { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 0 0 24px; }
.brand { display: inline-flex; align-items: center; gap: 10px; font-size: 16px; font-weight: 650; letter-spacing: -.02em; }
.brand-mark { display: inline-block; width: 17px; height: 17px; border: 2px solid var(--teal); border-radius: 4px; box-shadow: inset 0 0 0 3px #f6f8fa; background: var(--teal); }
.brand-divider { margin: 0 3px; color: #b2bcc5; font-weight: 400; }
.brand-area { color: var(--muted); font-size: 14px; font-weight: 400; letter-spacing: 0; }
.refresh-button { min-height: 38px; min-width: 84px; padding: 7px 15px; border: 1px solid #ccd6dc; border-radius: 8px; background: var(--paper); color: var(--ink); cursor: pointer; font-weight: 550; }
.refresh-button:hover { border-color: #97aaa5; background: #f0f6f4; }
.refresh-button:disabled { cursor: wait; opacity: .65; }
.agents-section { padding: 28px; background: var(--paper); border: 1px solid var(--line); border-radius: 15px; }
.agents-heading { display: flex; justify-content: space-between; align-items: flex-start; gap: 24px; }
.eyebrow { margin: 0 0 5px; color: var(--muted); font-size: 12px; font-weight: 600; }
h1 { margin: 0; font-size: clamp(25px, 2.2vw, 32px); line-height: 1.25; letter-spacing: -.035em; font-weight: 650; }
.window-range { margin: 9px 0 0; color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.coverage-chip { flex-shrink: 0; margin-top: 5px; padding: 5px 10px; border: 1px solid #dce6e1; border-radius: 999px; color: #42645a; background: #f5f9f7; font-size: 11px; font-weight: 600; white-space: nowrap; }
.coverage-chip.partial { border-color: #e4dac1; background: #fcf8ed; color: #775a27; }
.coverage-chip.unknown { border-color: #e6d5d4; background: #fbf2f1; color: #8b4943; }
.coverage-note { margin: 20px 0 0; padding: 12px 15px; border-radius: 8px; font-size: 13px; }
.partial-note { color: #765c2c; background: #fcf8ed; border: 1px solid #eee5ce; }
.unknown-note { color: #874a44; background: #fbf4f3; border: 1px solid #eeddda; }
.source-note { margin: 12px 0 0; color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
.agent-totals { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); margin: 24px 0 0; border: 1px solid var(--line); border-radius: 10px; background: #fafbfc; }
.agent-total { padding: 16px 20px; border-right: 1px solid var(--line); min-width: 0; }
.agent-total:last-child { border-right: 0; }
.agent-total dt { color: var(--muted); font-size: 12px; }
.agent-total dd { margin: 6px 0 0; font-size: 31px; font-weight: 620; letter-spacing: -.035em; line-height: 1.25; font-variant-numeric: tabular-nums; }
.agent-total:last-child dd { color: var(--teal); }
.unavailable { color: #7a858e; }
.observed-count { white-space: nowrap; }
.agents-table-wrap { margin-top: 25px; border-top: 1px solid var(--line); }
.agents-table { width: 100%; border-collapse: collapse; border-spacing: 0; font-size: 13px; }
.agents-table thead th { padding: 12px 10px; text-align: left; color: var(--muted); font-size: 11px; font-weight: 600; border-bottom: 1px solid var(--line); white-space: nowrap; }
.agents-table thead th:first-child { padding-left: 7px; }
.agents-table .number-heading { text-align: right; }
.agent-row > th, .agent-row > td { padding: 14px 10px; border-bottom: 1px solid var(--line); vertical-align: middle; }
.agent-name { text-align: left; font-weight: 600; }
.agent-toggle { display: inline-flex; align-items: center; gap: 11px; min-height: 40px; padding: 6px 8px; margin-left: -8px; border: 0; border-radius: 6px; color: var(--ink); background: transparent; text-align: left; cursor: pointer; }
.agent-toggle:hover { background: #f0f4f5; }
.agent-toggle code { font: 600 14px/1.3 ui-monospace, SFMono-Regular, Consolas, monospace; overflow-wrap: anywhere; }
.agent-chevron { width: 6px; height: 6px; border-top: 1.5px solid #71838f; border-right: 1.5px solid #71838f; transform: rotate(45deg); transition: transform .12s ease; flex-shrink: 0; }
.agent-toggle[aria-expanded="true"] .agent-chevron { transform: rotate(135deg); }
.agent-state { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; border-radius: 999px; font-size: 11px; font-weight: 550; white-space: nowrap; }
.agent-state.idle { color: #65727c; background: #f0f3f5; }
.agent-state.running { color: #155f53; background: var(--teal-light); }
.state-dot { display: inline-block; width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
.running-count { padding-left: 5px; border-left: 1px solid #b3d3c9; font-variant-numeric: tabular-nums; }
.agent-tool-summary { display: flex; flex-wrap: wrap; align-items: center; gap: 5px; max-width: 270px; margin: 0; padding: 0; list-style: none; }
.agent-tool-summary li { display: inline-flex; align-items: baseline; gap: 6px; min-width: 0; max-width: 100%; padding: 3px 6px; border: 1px solid #e3e8ec; border-radius: 5px; background: #f7f9fa; }
.agent-tool-summary code { min-width: 0; max-width: 18ch; font: 11px/1.4 ui-monospace, SFMono-Regular, Consolas, monospace; overflow-wrap: anywhere; }
.tool-summary-count { flex-shrink: 0; font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.agent-tool-summary .tool-summary-empty { padding: 0; border: 0; background: transparent; color: var(--muted); font-size: 11px; }
.last-command { display: flex; flex-direction: column; gap: 3px; }
.agent-relative { font-size: 13px; font-weight: 500; white-space: nowrap; }
.agent-utc { color: var(--muted); font-size: 10px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.agent-number { text-align: right; font-variant-numeric: tabular-nums; font-size: 14px; }
.agent-detail-row > td { padding: 0; border-bottom: 1px solid var(--line); }
.agent-tools-panel { padding: 17px 20px 20px; background: #f7f9fa; }
.agent-tools-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 10px 24px; flex-wrap: wrap; margin-bottom: 13px; }
.agent-tools-heading h2 { margin: 0; font-size: 12px; font-weight: 650; }
.agent-tools-heading p { margin: 0; color: var(--muted); font-size: 11px; }
.tool-tally { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px 22px; margin: 0; padding: 0; list-style: none; }
.tool-tally li { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-width: 0; padding: 7px 0; border-bottom: 1px solid #e5eaed; font-variant-numeric: tabular-nums; }
.tool-tally code { font: 12px/1.4 ui-monospace, SFMono-Regular, Consolas, monospace; overflow-wrap: anywhere; }
.tool-tally li > span { flex-shrink: 0; font-size: 13px; font-weight: 600; }
.agents-empty { padding: 38px 20px; margin-top: 24px; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); text-align: center; }
.agents-empty h2 { margin: 0 0 7px; font-size: 16px; font-weight: 600; }
.agents-empty p { margin: 0; color: var(--muted); font-size: 13px; }
.counts-note { margin: 18px 0 0; color: var(--muted); font-size: 11px; line-height: 1.7; max-width: 1000px; }
.agents-updated { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 5px 24px; margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); color: var(--muted); font-size: 11px; }
.agents-updated time { color: #4c5c69; font-variant-numeric: tabular-nums; }
.page-foot { margin: 12px 2px 0; color: var(--muted); font-size: 11px; }
.page-foot p { margin: 0; }
[data-agents-refresh-status][data-state="failed"], [data-agents-refresh-status][data-state="unknown"] { color: #944a40; }
.visually-hidden { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
@media (max-width: 1000px) {
  .agents-main { width: calc(100% - 40px); }
  .agents-section { padding: 23px 20px; }
  .agent-row > th, .agent-row > td, .agents-table thead th { padding-left: 7px; padding-right: 7px; }
  .agent-total { padding: 15px; }
  .tool-tally { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@media (max-width: 720px) {
  .agents-main { width: calc(100% - 32px); padding-top: 20px; }
  .page-bar { padding-bottom: 22px; }
  .brand { gap: 8px; font-size: 15px; }
  .brand-area { font-size: 13px; }
  .refresh-button { min-height: 40px; min-width: 79px; padding: 7px 12px; font-size: 13px; }
  .agents-section { padding: 0; border: 0; border-radius: 0; background: transparent; }
  .agents-heading { display: block; }
  h1 { font-size: 28px; max-width: 18ch; line-height: 1.2; }
  .eyebrow { font-size: 11px; margin-bottom: 8px; }
  .coverage-chip { display: inline-block; margin-top: 14px; }
  .window-range { margin-top: 10px; font-size: 11px; }
  .coverage-note { margin-top: 14px; padding: 12px; font-size: 12px; }
  .source-note { font-size: 11px; }
  .agent-totals { grid-template-columns: repeat(2, minmax(0, 1fr)); margin-top: 20px; background: #fff; }
  .agent-total { padding: 14px 16px; }
  .agent-total:nth-child(2) { border-right: 0; }
  .agent-total:nth-child(-n+2) { border-bottom: 1px solid var(--line); }
  .agent-total dt { font-size: 11px; }
  .agent-total dd { font-size: 29px; }
  .agents-table-wrap { margin-top: 20px; border: 0; }
  .agents-table { display: block; width: 100%; }
  .agents-table thead { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
  .agent-entry { display: block; margin-bottom: 12px; border: 1px solid var(--line); border-radius: 10px; background: #fff; overflow: hidden; }
  .agent-row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .agent-row > th, .agent-row > td { display: block; padding: 12px 16px; border: 0; min-width: 0; }
  .agent-row .agent-name { display: flex; align-items: center; padding-right: 6px; }
  .agent-toggle { min-height: 36px; gap: 10px; }
  .agent-toggle code { font-size: 16px; }
  .agent-row .agent-status { display: flex; align-items: center; justify-content: flex-end; padding-left: 0; }
  .agent-state { font-size: 10px; gap: 5px; }
  .agent-row .agent-tool-summary-cell { grid-column: 1 / -1; border-top: 1px solid var(--line); padding-top: 10px; padding-bottom: 10px; }
  .agent-tool-summary-cell::before { content: attr(data-label); display: block; margin-bottom: 6px; color: var(--muted); font-size: 11px; }
  .agent-tool-summary { max-width: none; }
  .agent-row .agent-last { grid-column: 1 / -1; display: flex; align-items: center; justify-content: space-between; gap: 12px; border-top: 1px solid var(--line); padding-top: 11px; padding-bottom: 11px; }
  .agent-last::before { content: attr(data-label); color: var(--muted); font-size: 11px; }
  .last-command { align-items: flex-end; }
  .agent-utc { font-size: 10px; }
  .agent-row .agent-number { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; border-top: 1px solid var(--line); font-size: 18px; text-align: left; }
  .agent-number::before { content: attr(data-label); color: var(--muted); font-size: 11px; font-weight: 400; }
  .agent-number[data-agent-value="last-hour"], .agent-number[data-agent-value="six-hours"] { border-right: 1px solid var(--line); }
  .agent-detail-row { display: block; }
  .agent-detail-row > td { display: block; border: 0; }
  .agent-tools-panel { padding: 16px; border-top: 1px solid var(--line); border-radius: 0 0 10px 10px; }
  .agent-tools-heading { display: block; }
  .agent-tools-heading p { margin-top: 6px; font-size: 10px; }
  .tool-tally { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 6px 18px; }
  .counts-note { margin-top: 16px; font-size: 10px; }
  .agents-updated { display: block; font-size: 10px; }
  .agents-updated > span { display: block; margin-top: 5px; }
  .agents-empty { background: #fff; border: 1px solid var(--line); border-radius: 10px; padding: 30px 16px; }
}
@media (prefers-reduced-motion: reduce) { .agent-chevron { transition: none; } }
`

const agentsScript = `
const agentsAuthStorageName = "codexpro.activity.credential";
const agentsInitialURL = new URL(window.location.href);
let agentsCredential = agentsInitialURL.searchParams.get("codexpro_token") || agentsInitialURL.searchParams.get("token") || "";
try {
  if (agentsCredential) sessionStorage.setItem(agentsAuthStorageName, agentsCredential);
  else agentsCredential = sessionStorage.getItem(agentsAuthStorageName) || "";
} catch {}
if (agentsInitialURL.searchParams.has("codexpro_token") || agentsInitialURL.searchParams.has("token")) {
  agentsInitialURL.searchParams.delete("codexpro_token");
  agentsInitialURL.searchParams.delete("token");
  history.replaceState(null, "", agentsInitialURL.pathname + agentsInitialURL.search + agentsInitialURL.hash);
}
function agentsDataURL() {
  const target = new URL("/activity/data", window.location.origin);
  if (agentsCredential) target.searchParams.set("codexpro_token", agentsCredential);
  return target.pathname + target.search;
}

const expandedAgentKeys = new Set();
function setAgentDetails(entry, expanded) {
  const button = entry.querySelector("[data-agent-toggle]");
  const details = entry.querySelector("[data-agent-tools]");
  if (!button || !details) return;
  button.setAttribute("aria-expanded", String(expanded));
  button.setAttribute("aria-label", (expanded ? "Hide" : "Show") + " tool counts for " + (entry.dataset.agentKey || "Untagged"));
  details.hidden = !expanded;
}
document.addEventListener("click", (event) => {
  const button = event.target.closest?.("[data-agent-toggle]");
  const entry = button?.closest("[data-agent-entry]");
  if (!entry) return;
  const expanded = button.getAttribute("aria-expanded") !== "true";
  if (expanded) expandedAgentKeys.add(entry.dataset.agentKey);
  else expandedAgentKeys.delete(entry.dataset.agentKey);
  setAgentDetails(entry, expanded);
});

let agentsLoading = false;
async function refreshAgents() {
  if (document.hidden || agentsLoading) return false;
  const current = document.querySelector('[data-dashboard-section="agents"]');
  if (!current) return false;
  agentsLoading = true;
  const button = document.querySelector("[data-agents-refresh]");
  const status = document.querySelector("[data-agents-refresh-status]");
  if (button) { button.disabled = true; button.textContent = "Refreshing…"; }
  current.setAttribute("aria-busy", "true");
  try {
    const response = await fetch(agentsDataURL(), { credentials: "same-origin", cache: "no-store" });
    if (!response.ok) throw new Error("Activity read failed");
    const data = await response.json();
    if (!data || typeof data.generatedAt !== "string" || !Number.isFinite(Date.parse(data.generatedAt)) ||
        !Array.isArray(data.sections) || data.sections.length !== 1 ||
        data.sections[0]?.key !== "agents" || typeof data.sections[0].html !== "string") {
      throw new Error("Invalid activity response");
    }
    const template = document.createElement("template");
    template.innerHTML = data.sections[0].html;
    const incoming = template.content.firstElementChild;
    if (!incoming || template.content.childElementCount !== 1 ||
        incoming.getAttribute("data-dashboard-section") !== "agents" ||
        incoming.dataset.generatedAt !== data.generatedAt ||
        !["true", "false"].includes(incoming.dataset.known) ||
        !["true", "false"].includes(incoming.dataset.partial)) {
      throw new Error("Invalid activity fragment");
    }
    const focused = document.activeElement?.closest?.("[data-agent-entry]");
    const focusKey = focused?.dataset.agentKey;
    current.querySelectorAll("[data-agent-entry]").forEach((entry) => {
      if (entry.querySelector("[data-agent-toggle]")?.getAttribute("aria-expanded") === "true") expandedAgentKeys.add(entry.dataset.agentKey);
      else expandedAgentKeys.delete(entry.dataset.agentKey);
    });
    incoming.querySelectorAll("[data-agent-entry]").forEach((entry) => setAgentDetails(entry, expandedAgentKeys.has(entry.dataset.agentKey)));
    const scrollX = window.scrollX;
    const scrollY = window.scrollY;
    current.replaceWith(incoming);
    if (focusKey !== undefined) {
      const nextFocus = Array.from(incoming.querySelectorAll("[data-agent-entry]"))
        .find((entry) => entry.dataset.agentKey === focusKey)?.querySelector("[data-agent-toggle]");
      (nextFocus || button)?.focus({ preventScroll: true });
    }
    if (window.scrollX !== scrollX || window.scrollY !== scrollY) window.scrollTo(scrollX, scrollY);
    if (status) {
      status.dataset.state = incoming.dataset.known === "true" ? "current" : "unknown";
      status.textContent = incoming.dataset.known === "true"
        ? "Snapshot refreshed. Refreshes every 60 seconds while this page is visible."
        : "Activity is unavailable. Refresh to try again.";
    }
    return true;
  } catch {
    if (status) {
      const updated = current.querySelector("[data-agents-updated]")?.textContent;
      status.dataset.state = "failed";
      status.textContent = "Update failed. Showing the last snapshot" + (updated ? " from " + updated : "") + ". Use Refresh to try again.";
    }
    return false;
  } finally {
    agentsLoading = false;
    document.querySelector('[data-dashboard-section="agents"]')?.removeAttribute("aria-busy");
    if (button) { button.disabled = false; button.textContent = "Refresh"; }
  }
}
document.querySelector("[data-agents-refresh]")?.addEventListener("click", refreshAgents);
window.setInterval(refreshAgents, 60_000);
`
