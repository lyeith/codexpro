// Static assets ported from release 0.31.0. The Go server has no Node dependency.
package activity

import "github.com/lyeith/codexpro/go/internal/usage"

const dashboardStyle = `    :root {
      color-scheme: light;
      font-family: "Geist", "Aptos", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      --paper: #f4f6f9;
      --panel: #ffffff;
      --ink: #172033;
      --soft: #5b667a;
      --rule: #dce2eb;
      --accent: #2563eb;
      --good: #137a47;
      --good-bg: #eaf8f0;
      --warn: #9a5a00;
      --warn-bg: #fff5dc;
      --bad: #b42318;
      --bad-bg: #fff0ee;
      --mono: "Fira Code", "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--paper); color: var(--ink); }
    button, a { font: inherit; }
    main { width: min(1500px, calc(100% - 32px)); margin: 0 auto; padding: 24px 0 56px; }
    .topbar { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 18px; }
    .brand { display: flex; align-items: center; gap: 12px; }
    .brand img { width: 42px; height: 42px; border-radius: 11px; }
    .eyebrow, .project-id { display: block; color: var(--soft); font-size: 12px; font-weight: 750; letter-spacing: .09em; text-transform: uppercase; }
    h1, h2, h3, p { margin-top: 0; }
    h1 { margin-bottom: 4px; font-size: clamp(25px, 4vw, 38px); letter-spacing: -.035em; }
    h2 { margin-bottom: 0; font-size: 21px; letter-spacing: -.02em; }
    h3 { margin-bottom: 0; font-size: 15px; }
    .subtitle { margin: 0; color: var(--soft); }
    .actions { display: flex; align-items: center; gap: 8px; }
    .button { border: 1px solid var(--rule); border-radius: 9px; background: var(--panel); color: var(--ink); padding: 9px 12px; text-decoration: none; cursor: pointer; }
    .button.primary { border-color: var(--accent); background: var(--accent); color: white; }
    .summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; margin: 18px 0; }
    .metric { background: var(--panel); border: 1px solid var(--rule); border-radius: 12px; padding: 14px; }
    .metric span { display: block; color: var(--soft); font-size: 12px; margin-bottom: 5px; }
    .metric strong { font-family: var(--mono); font-size: 16px; }
    .banner { margin: 12px 0; border: 1px solid var(--rule); border-radius: 10px; background: var(--panel); padding: 12px 14px; }
    .banner.warn { border-color: #f0cc88; background: var(--warn-bg); color: var(--warn); }
    .banner.bad { border-color: #f0aaa3; background: var(--bad-bg); color: var(--bad); }
    .dashboard-section { margin: 16px 0; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); padding: 18px 20px; box-shadow: 0 8px 28px rgba(23, 32, 51, .05); }
    .section-heading { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
    .section-heading > span { color: var(--soft); font-size: 12px; }
    .history-heading { align-items: center; }
    .project-filter { display: flex; align-items: center; gap: 10px; color: var(--soft); font-size: 13px; }
    .project-filter select { width: 220px; max-width: 100%; border: 1px solid var(--rule); border-radius: 8px; padding: 8px 30px 8px 10px; background: var(--panel); color: var(--ink); font: inherit; }
    .history-navigation { display: flex; justify-content: space-between; gap: 16px; padding-top: 14px; color: var(--soft); font-size: 12px; }
    .history-navigation div { display: flex; gap: 18px; }
    .history-navigation a { color: var(--accent); text-decoration: none; }
    .live-panel .action-card > summary { grid-template-columns: minmax(0, 1fr) auto 16px; }
    .live-heading { margin: 18px 0 8px; color: var(--soft); font-size: 12px; }
    .live-columns { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(440px, 100%), 1fr)); gap: 16px; }
    .live-columns > section { min-width: 0; }
    .live-list { display: grid; gap: 6px; max-height: 300px; overflow: auto; }
    .live-list:has(details[open]) { max-height: none; }
    .job-live { margin-top: 14px; font-size: 12px; line-height: 1.5; }
    .job-live h4 { margin: 12px 0 8px; font-size: 11px; }
    .job-live pre { max-height: 360px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: 12px/1.5 var(--mono); }
    .visually-hidden { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); clip-path: inset(50%); white-space: nowrap; }
    .timeline-scroll { overflow-x: auto; padding-bottom: 6px; }
    .timeline-chart { min-width: 900px; }
    .timeline-axis, .timeline-lane { display: grid; grid-template-columns: 210px minmax(620px, 1fr); gap: 14px; }
    .timeline-axis { height: 30px; }
    .timeline-axis-track, .timeline-track { position: relative; }
    .timeline-axis-track::after { content: ""; position: absolute; right: 0; bottom: 0; left: 0; border-top: 1px solid var(--rule); }
    .timeline-tick { position: absolute; top: 0; bottom: 0; z-index: 1; transform: translateX(-50%); color: var(--soft); font-size: 10px; white-space: nowrap; text-align: center; }
    .timeline-tick i { display: block; width: 1px; height: 7px; margin: 3px auto 0; background: var(--rule); }
    .timeline-lane { min-height: 30px; align-items: stretch; border-top: 1px solid #edf0f5; }
    .timeline-label { min-width: 0; padding: 5px 0; }
    .timeline-label strong, .timeline-label code { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .timeline-label strong { font-size: 12px; }
    .timeline-label code { margin-top: 2px; color: var(--soft); font-size: 10px; }
    .timeline-track { min-height: 30px; }
    .timeline-grid { position: absolute; top: 0; bottom: 0; width: 1px; background: #edf0f5; }
    .timeline-cell { position: absolute; top: 5px; bottom: 5px; z-index: 2; min-width: 3px; border-radius: 2px; --tone: var(--good); background: color-mix(in srgb, var(--tone) calc(var(--fill, 1) * 100%), var(--panel)); }
    .timeline-cell.warn { --tone: var(--warn); }
    .timeline-cell.bad { --tone: var(--bad); }
    .timeline-cell.has-mutation::after { content: ""; position: absolute; right: 0; bottom: 0; left: 0; height: 3px; border-radius: 0 0 2px 2px; background: var(--ink); opacity: .55; }
    .timeline-cell:hover { --fill: 1; outline: 2px solid var(--ink); outline-offset: -1px; z-index: 3; }
    .timeline-range { display: flex; justify-content: flex-end; align-items: center; gap: 7px; margin: 8px 0 0; color: var(--soft); font-size: 10px; }
    .timeline-legend { margin-right: auto; display: inline-flex; align-items: center; gap: 5px; }
    .timeline-legend i { display: inline-block; width: 10px; height: 10px; border-radius: 2px; background: var(--good); opacity: .7; }
    .timeline-legend i.warn { background: var(--warn); }
    .timeline-legend i.bad { background: var(--bad); }
    .command-table-scroll { overflow-x: auto; }
    .command-table { width: 100%; min-width: 940px; border-collapse: separate; border-spacing: 0; }
    .command-table th { border-bottom: 1px solid var(--rule); padding: 8px 10px; color: var(--soft); font-size: 10px; letter-spacing: .06em; text-align: left; text-transform: uppercase; }
    .command-table td { vertical-align: top; border-bottom: 1px solid #edf0f5; padding: 9px 10px; }
    .command-table tbody tr:last-child td { border-bottom: 0; }
    .command-when { width: 185px; color: var(--soft); font-size: 11px; }
    .command-when time, .command-when small, .command-project strong, .command-project code, .command-project .recovered { display: block; }
    .command-when small { margin-top: 4px; color: #8a94a6; font-family: var(--mono); }
    .command-project { width: 210px; }
    .command-project strong { font-size: 12px; }
    .command-project strong, .command-project code { max-width: 210px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .command-project code { margin-top: 4px; color: var(--soft); font-size: 10px; }
    .command-project .recovered { margin-top: 5px; color: #38517d; font-size: 9px; }
    .command-cell { min-width: 520px; padding-top: 6px !important; padding-bottom: 6px !important; }
    .command-cell .action-card { background: transparent; }
    .command-cell .action-card > summary { grid-template-columns: minmax(0, 1fr) auto 16px; }
    .command-cell .action-time { display: none; }
    .project-section { margin-top: 18px; }
    .project-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(430px, 100%), 1fr)); gap: 14px; }
    .project-card { min-width: 0; overflow: hidden; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); box-shadow: 0 8px 28px rgba(23, 32, 51, .05); }
    .project-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding: 18px 20px 14px; }
    .project-head > div:first-child { min-width: 0; }
    .project-head h2 { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .project-meta { display: flex; align-items: flex-end; flex-direction: column; gap: 6px; color: var(--soft); font-size: 12px; }
    .branch { border: 1px solid var(--rule); border-radius: 999px; padding: 4px 8px; font-family: var(--mono); color: var(--ink); }
    .git-panel { display: flex; justify-content: space-between; gap: 12px; margin: 0 20px 16px; border: 1px solid var(--rule); border-radius: 10px; background: #f8fafc; padding: 11px 12px; color: var(--soft); font-size: 13px; }
    .git-panel div { display: flex; align-items: center; gap: 8px; }
    .git-panel.clean { border-color: #b9e3ca; background: var(--good-bg); color: var(--good); }
    .git-panel.unavailable { opacity: .85; }
    .git-details { margin: 0 20px 16px; border: 1px solid #f0cc88; border-radius: 10px; background: #fffaf0; }
    .git-details summary { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 12px 14px; cursor: pointer; list-style: none; }
    .git-details summary::-webkit-details-marker { display: none; }
    .git-details summary span:first-child { display: flex; align-items: baseline; gap: 8px; }
    .git-details small { color: var(--soft); }
    .delta { display: flex; gap: 9px; font-family: var(--mono); }
    .delta b:first-child { color: var(--good); }
    .delta b:last-child { color: var(--bad); }
    .git-body { border-top: 1px solid #f0cc88; padding: 14px; }
    .git-diff .empty { margin: 0; }
    .path-group { margin-bottom: 11px; }
    .path-group > strong { display: block; margin-bottom: 6px; font-size: 12px; color: var(--soft); }
    .path-list { display: flex; flex-wrap: wrap; gap: 5px; }
    .path { border-radius: 6px; padding: 4px 6px; font-size: 11px; }
    .path.tracked { background: #e9eef7; }
    .path.untracked { background: var(--warn-bg); color: var(--warn); }
    .safety-note { color: var(--soft); font-size: 12px; }
    .diff-fallback, .shell-script pre { max-height: 560px; overflow: auto; margin: 12px 0 0; border-radius: 8px; background: #111827; color: #e5e7eb; padding: 13px; font: 12px/1.55 var(--mono); white-space: pre; tab-size: 2; }
    .shell-script { overflow: hidden; margin-top: 10px; border: 1px solid #25324a; border-radius: 8px; background: #111827; }
    .shell-script-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; background: #172033; padding: 8px 11px; color: #d9e2f2; }
    .shell-script-head h4 { margin: 0; font-size: 10px; letter-spacing: .07em; text-transform: uppercase; }
    .shell-script-head span { color: #9fb0ca; font-size: 10px; }
    .shell-script pre { margin: 0; border-radius: 0; background: transparent; }
    .split-diff { display: grid; gap: 12px; margin-top: 12px; }
    .diff-file { overflow: hidden; border: 1px solid var(--rule); border-radius: 9px; background: var(--panel); }
    .diff-file-heading { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 9px 11px; background: #f3f6fa; }
    .diff-file-heading code { overflow-wrap: anywhere; font-size: 12px; font-weight: 750; }
    .diff-file-heading span { flex: 0 0 auto; color: var(--soft); font-size: 10px; text-transform: uppercase; }
    .split-diff-scroll { max-height: 620px; overflow: auto; border-top: 1px solid var(--rule); overscroll-behavior: contain; }
    .split-diff-grid { display: grid; grid-template-columns: 58px minmax(440px, 1fr) 58px minmax(440px, 1fr); min-width: 1040px; font: 12px/1.55 var(--mono); }
    .diff-side-heading { position: sticky; top: 0; z-index: 3; display: flex; align-items: baseline; gap: 9px; border-bottom: 1px solid #cfd7e4; background: #edf1f7; padding: 7px 9px; }
    .diff-side-heading.before { grid-column: 1 / 3; border-right: 1px solid #c7d0df; }
    .diff-side-heading.after { grid-column: 3 / 5; }
    .diff-side-heading span { color: var(--soft); font: 700 9px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; }
    .diff-side-heading code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .diff-metadata, .diff-hunk-header, .diff-note { grid-column: 1 / -1; white-space: pre; }
    .diff-metadata { background: #f8fafc; padding: 3px 10px; color: var(--soft); }
    .diff-hunk-header { border-top: 1px solid #d7dfeb; border-bottom: 1px solid #d7dfeb; background: #eef4ff; padding: 4px 10px; color: #38517d; }
    .diff-note { background: #fff8e8; padding: 4px 10px; color: var(--warn); }
    .diff-row { display: contents; }
    .diff-line-number, .diff-line-code { min-height: 23px; border-top: 1px solid #edf0f5; }
    .diff-line-number { padding: 2px 8px 2px 4px; color: #8791a3; text-align: right; user-select: none; }
    .diff-line-code { padding: 2px 9px; white-space: pre; tab-size: 2; }
    .diff-line-code.before { border-right: 1px solid #cfd7e4; }
    .diff-line-number.removed, .diff-line-code.removed { background: #fff0ee; }
    .diff-line-number.added, .diff-line-code.added { background: #eaf8f0; }
    .diff-line-number.empty, .diff-line-code.empty { background: #f7f9fc; }
    .diff-line-code.empty { color: transparent; }
    .activity-block { border-top: 1px solid var(--rule); padding: 14px 20px 20px; }
    .section-title { display: flex; justify-content: space-between; align-items: baseline; gap: 10px; margin-bottom: 10px; }
    .section-title span { color: var(--soft); font-size: 12px; }
    code { font-family: var(--mono); }
    .action-list { display: grid; gap: 8px; }
    .action-card { overflow: hidden; border: 1px solid #e4e8ef; border-radius: 10px; background: #fbfcfe; }
    .action-card[open] { border-color: #b9c9e8; background: var(--panel); box-shadow: 0 8px 22px rgba(23, 32, 51, .06); }
    .action-card > summary { display: grid; grid-template-columns: minmax(155px, 190px) minmax(0, 1fr) auto 16px; align-items: center; gap: 12px; padding: 11px 12px; cursor: pointer; list-style: none; }
    .action-card > summary::-webkit-details-marker { display: none; }
    .action-card > summary::after { content: "›"; color: var(--soft); font-size: 22px; line-height: 1; transition: transform 120ms ease; }
    .action-card[open] > summary::after { transform: rotate(90deg); }
    .action-card[open] > summary { border-bottom: 1px solid var(--rule); background: #f8faff; }
    .action-time { display: flex; flex-direction: column; gap: 3px; color: var(--soft); font-size: 12px; }
    .action-time span { font-family: var(--mono); font-size: 10px; color: #8a94a6; }
    .action-summary-main { min-width: 0; }
    .action-title { display: flex; align-items: baseline; gap: 9px; min-width: 0; }
    .action-title code { flex: 0 0 auto; border-radius: 5px; background: #e9eef7; padding: 3px 6px; color: #273754; font-size: 11px; font-weight: 750; }
    .action-title strong { min-width: 0; overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
    .action-subtitle { display: flex; flex-wrap: wrap; gap: 5px 10px; margin-top: 5px; color: var(--soft); font-size: 10px; }
    .action-subtitle span:not(.mutating) + span:not(.mutating)::before { content: "·"; margin-right: 10px; color: #a2aaba; }
    .mutating { border-radius: 999px; background: #edf1f7; padding: 1px 5px; color: #59677e; font-weight: 700; }
    .status { display: inline-block; border-radius: 999px; padding: 3px 7px; font-size: 11px; font-weight: 750; white-space: nowrap; }
    .status.good { background: var(--good-bg); color: var(--good); }
    .status.warn { background: var(--warn-bg); color: var(--warn); }
    .status.bad { background: var(--bad-bg); color: var(--bad); }
    .action-body { padding: 13px 14px 14px; }
    .job-evidence { margin: 10px 0; padding: 12px; border: 1px solid var(--rule); border-radius: 8px; }
    .job-live pre { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow: auto; padding: 12px; background: #f4f6fa; border-radius: 6px; }
    .job-live h4 { margin-top: 14px; }
    .recorded-metadata { margin: 10px 0; }
    .recorded-metadata summary { cursor: pointer; color: var(--soft); }
    nav.section-heading a { padding: 5px 9px; background: white; border: 1px solid var(--rule); border-radius: 7px; color: #3459be; text-decoration: none; }
    nav.section-heading { flex-wrap: wrap; justify-content: flex-start; gap: 12px; }
    .fact-row dd { overflow-wrap: anywhere; min-width: 0; }
    .fact-row > div { max-width: 100%; }

    .action-detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
    .action-section { min-width: 0; border: 1px solid #e7ebf1; border-radius: 8px; background: #fcfdff; padding: 10px; }
    .action-section h4 { margin: 0 0 8px; color: var(--soft); font-size: 10px; letter-spacing: .07em; text-transform: uppercase; }
    .changed-paths { margin-bottom: 10px; }
    .field-grid, .evidence-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 7px; margin: 0; }
    .field-grid > div, .evidence-list > div { min-width: 0; border-radius: 6px; background: #f3f6fa; padding: 7px 8px; }
    .field-grid dt, .evidence-list dt { margin-bottom: 3px; color: var(--soft); font-size: 9px; letter-spacing: .035em; text-transform: uppercase; }
    .field-grid dd, .evidence-list dd { overflow-wrap: anywhere; margin: 0; font-size: 12px; }
    .mono-value, .evidence-list dt, .git-transition code, .action-identity code { font-family: var(--mono); }
    .field-grid dd.positive { color: var(--good); font-weight: 750; }
    .field-grid dd.negative { color: var(--bad); font-weight: 750; }
    .field-grid dd.muted { color: var(--soft); }
    .git-transition { display: grid; gap: 7px; }
    .git-transition div { display: grid; grid-template-columns: 52px minmax(0, 1fr); align-items: baseline; gap: 8px; }
    .git-transition span { color: var(--soft); font-size: 10px; text-transform: uppercase; }
    .git-transition code { overflow-wrap: anywhere; font-size: 11px; }
    .privacy-note, .error-note { margin: 10px 0 0; border-radius: 7px; padding: 8px 10px; font-size: 11px; }
    .privacy-note { background: #eef4ff; color: #38517d; }
    .error-note { background: var(--bad-bg); color: var(--bad); }
    /* Operation-class colour coding: badge tint + left accent on the card. */
    .action-card { --class: #8290a8; border-left: 3px solid var(--class); }
    .action-card[data-class="read"] { --class: #3b82f6; }
    .action-card[data-class="analysis"] { --class: #6366f1; }
    .action-card[data-class="write"] { --class: #d97706; }
    .action-card[data-class="execute"] { --class: #7c3aed; }
    .action-card[data-class="git"] { --class: #059669; }
    .action-card[data-class="lifecycle"] { --class: #0891b2; }
    .action-card[data-class="handoff"] { --class: #db2777; }
    .action-card[data-class="administrative"] { --class: #64748b; }
    .tool-badge { background: color-mix(in srgb, var(--class) 14%, white) !important; color: color-mix(in srgb, var(--class) 70%, black) !important; }
    .class-tag { color: var(--class); font-weight: 700; }
    .attribution { border-radius: 999px; background: #fff5dc; padding: 1px 6px; color: var(--warn); font-size: 10px; }
    .fact-row { display: flex; flex-wrap: wrap; gap: 6px; margin: 0 0 10px; }
    .fact-row > div { display: flex; align-items: baseline; gap: 5px; border: 1px solid #e7ebf1; border-radius: 7px; background: #f8fafc; padding: 4px 8px; }
    .fact-row dt { color: var(--soft); font-size: 10px; letter-spacing: .04em; text-transform: uppercase; }
    .fact-row dd { margin: 0; font-family: var(--mono); font-size: 12px; }
    .fact-row dd.positive { color: var(--good); font-weight: 750; }
    .fact-row dd.negative { color: var(--bad); font-weight: 750; }
    .fact-row dd.muted { color: var(--soft); }
    .run-reference { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; max-width: 100%; }
    .copy-run-id { display: inline-flex; align-items: center; gap: 6px; border: 0; border-radius: 3px; background: transparent; padding: 2px; color: inherit; cursor: pointer; }
    .copy-run-id:hover { background: #e9eef7; }
    .copy-run-id:focus-visible { outline: 2px solid #3459be; outline-offset: 2px; }
    .copy-run-status { font: 11px system-ui, sans-serif; color: var(--soft); overflow-wrap: anywhere; }
    .action-section h4 span { margin-left: 6px; font-weight: 400; letter-spacing: 0; text-transform: none; }
    .action-section h4 a { margin-left: 8px; color: var(--accent); font-weight: 600; letter-spacing: 0; text-transform: none; text-decoration: none; }
    .action-section h4 code { margin-left: 6px; font-size: 10px; letter-spacing: 0; text-transform: none; }
    .path.read { background: #e8f1ff; color: #1d4ed8; }
    .batch-inline .badges { display: flex; flex-wrap: wrap; gap: 6px; margin: 0 0 8px; }
    .batch-inline .badge { border-radius: 999px; background: #eef2f7; padding: 3px 8px; font: 10px var(--mono); }
    .batch-inline .operation-list { display: grid; gap: 6px; }
    .batch-inline .batch-operation { overflow: hidden; border: 1px solid #e4e8ef; border-radius: 8px; background: #fbfcfe; }
    .batch-inline .batch-operation summary { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 7px 10px; cursor: pointer; }
    .batch-inline .batch-operation summary span { display: flex; align-items: center; gap: 8px; min-width: 0; }
    .batch-inline .batch-operation summary b { display: inline-grid; min-width: 22px; height: 22px; place-items: center; border-radius: 6px; background: #e9eef7; color: #59677e; font: 10px var(--mono); }
    .batch-inline .batch-operation summary strong { color: #38517d; font: 700 11px var(--mono); }
    .batch-inline pre { overflow: auto; max-height: 360px; margin: 0; border-top: 1px solid #e4e8ef; background: #111827; padding: 10px; color: #e5e7eb; font: 11px/1.5 var(--mono); white-space: pre; tab-size: 2; }
    .batch-inline .raw { margin-top: 8px; }
    .batch-inline .raw pre { border: 0; border-radius: 8px; }
    .action-identity { display: flex; gap: 8px; margin-top: 10px; color: var(--soft); font-size: 10px; }
    .action-identity code { overflow-wrap: anywhere; color: #667085; }

    .batch-link { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 9px; margin-bottom: 10px; border: 1px solid #bfd0ef; border-radius: 8px; background: #eef4ff; padding: 9px 10px; color: #294f91; text-decoration: none; }
    .batch-link:hover { border-color: #7fa3e3; background: #e5efff; }
    .batch-link span { font-size: 11px; font-weight: 750; }
    .batch-link code { overflow: hidden; color: #38517d; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
    .batch-link b { font-size: 12px; }
    .empty { margin: 12px 0 0; color: var(--soft); }
    .foot { margin-top: 20px; color: var(--soft); font-size: 12px; text-align: center; }
    @media (max-width: 820px) {
      main { width: min(100% - 20px, 1500px); padding-top: 14px; }
      .topbar, .project-head, .section-heading { align-items: stretch; flex-direction: column; }
      .project-meta { align-items: flex-start; }
      .summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .actions { flex-wrap: wrap; }
      .dashboard-section { padding: 14px; }
      .project-grid { grid-template-columns: 1fr; }
      .project-head, .activity-block { padding-left: 14px; padding-right: 14px; }
      .git-panel, .git-details { margin-left: 14px; margin-right: 14px; }
      .action-card > summary { grid-template-columns: minmax(0, 1fr) auto 16px; gap: 8px; }
      .action-time { grid-column: 1 / -1; flex-direction: row; justify-content: space-between; }
      .action-summary-main { grid-column: 1; }
      .action-card > summary > .status { grid-column: 2; }
      .action-detail-grid, .field-grid, .evidence-list { grid-template-columns: 1fr; }
      .action-title { align-items: flex-start; flex-direction: column; gap: 5px; }
      .action-title strong { white-space: normal; }
    }` + `
  .tool-metrics { margin: 16px 0; padding: 18px 20px; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); }
  .tool-metrics-heading { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px 20px; margin-bottom: 14px; }
  .tool-metrics-heading h2 { margin: 0; font-size: 16px; }
  .tool-metrics-heading p, .tool-metrics-note { margin: 0; color: var(--muted); font-size: 12px; }
  .tool-metrics-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin-bottom: 12px; }
  .tool-metrics-grid .metric { background: var(--paper); }
  @media (max-width: 640px) { .tool-metrics { padding: 14px; } .tool-metrics-grid { grid-template-columns: 1fr; } }
` + usage.Style

const dashboardScript = `
    const authStorageName = "codexpro.activity.credential";
    const initialUrl = new URL(window.location.href);
    const queryCredential = initialUrl.searchParams.get("codexpro_token") || initialUrl.searchParams.get("token") || "";
    if (queryCredential) sessionStorage.setItem(authStorageName, queryCredential);
    const connectorCredential = queryCredential || sessionStorage.getItem(authStorageName) || "";
    if (queryCredential) {
      initialUrl.searchParams.delete("codexpro_token");
      initialUrl.searchParams.delete("token");
      const clean = initialUrl.searchParams.toString();
      history.replaceState(null, "", initialUrl.pathname + (clean ? "?" + clean : "") + initialUrl.hash);
    }
    function authenticatedLocalUrl(pathname) {
      const target = new URL(pathname, window.location.origin);
      if (connectorCredential) target.searchParams.set("codexpro_token", connectorCredential);
      return target.pathname + target.search;
    }
    /**
 * Reconcile server-rendered dashboard fragments without replacing retained nodes.
 * Kept self-contained so the same implementation can be embedded in the page
 * and exercised against a DOM in unit tests.
 */
function reconcileDashboardElement(current, incoming, preserveRoot = true) {
  const key = (node) => {
    if (node.nodeType !== 1) return null;
    const element = node;
    for (const attribute of ["data-refresh-key", "data-command-id", "data-action-id", "data-live-id", "data-project"]) {
      const value = element.getAttribute(attribute);
      if (value !== null) return attribute + ":" + value;
    }
    return null;
  };
  const compatible = (left, right) =>
    left.nodeType === right.nodeType &&
    (left.nodeType !== 1 || left.tagName === right.tagName) &&
    key(left) === key(right);

  const patch = (target, source) => {
    if (target.nodeType !== 1 || source.nodeType !== 1) {
      if (target.nodeValue !== source.nodeValue) target.nodeValue = source.nodeValue;
      return;
    }
    const before = target;
    const after = source;
    // Output and fetched details have their own refresh owners. A changed
    // revision (for example Git state) invalidates that island normally.
    if ((preserveRoot || before !== current) && before.hasAttribute("data-refresh-preserve") &&
        after.hasAttribute("data-refresh-preserve") &&
        before.getAttribute("data-refresh-revision") === after.getAttribute("data-refresh-revision")) return;
    const selection = before.tagName === "SELECT" ? before.value : undefined;
    const preserveAttribute = (name) => name === "open" && before.tagName === "DETAILS";
    for (const attribute of Array.from(before.attributes)) {
      if (!preserveAttribute(attribute.name) && !after.hasAttribute(attribute.name)) before.removeAttribute(attribute.name);
    }
    for (const attribute of Array.from(after.attributes)) {
      if (!preserveAttribute(attribute.name) && before.getAttribute(attribute.name) !== attribute.value) {
        before.setAttribute(attribute.name, attribute.value);
      }
    }

    const children = Array.from(before.childNodes);
    const keyed = new Map(children.flatMap((node) => key(node) === null ? [] : [[key(node), node]]));
    const retained = new Set();
    let cursor = before.firstChild;
    for (const next of Array.from(after.childNodes)) {
      const identity = key(next);
      let match = identity === null
        ? children.find((node) => !retained.has(node) && key(node) === null && compatible(node, next))
        : keyed.get(identity);
      if (match && !compatible(match, next)) match = undefined;
      if (match) {
        retained.add(match);
        if (match !== cursor) before.insertBefore(match, cursor);
        patch(match, next);
      } else {
        match = next.cloneNode(true);
        before.insertBefore(match, cursor);
      }
      cursor = match.nextSibling;
    }
    for (const child of children) if (!retained.has(child)) child.remove();
    if (selection !== undefined && Array.from(before.options).some((option) => option.value === selection)) {
      before.value = selection;
    }
  };
  patch(current, incoming);
}

    ` + usage.Script + `
    async function refreshInboxCount() {
      try {
        const response = await fetch(authenticatedLocalUrl("/inbox/v1/items?status=pending&limit=1"));
        if (!response.ok) return;
        const result = await response.json();
        const badge = document.querySelector("[data-inbox-count]");
        if (badge) badge.textContent = result.total ? "(" + result.total + ")" : "";
      } catch {}
    }
    refreshInboxCount(); setInterval(refreshInboxCount, 15000);

    function prepareFragment(root) {
      root.querySelectorAll("[data-local-link]").forEach((link) => {
        link.setAttribute("href", authenticatedLocalUrl(link.getAttribute("href") || "/"));
      });
      root.querySelectorAll("[data-local-time], [data-local-axis]").forEach((element) => {
        const value = element.getAttribute("datetime");
        const parsed = value ? new Date(value) : null;
        if (!parsed || Number.isNaN(parsed.getTime())) return;
        element.textContent = element.hasAttribute("data-local-axis")
          ? parsed.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
          : parsed.toLocaleString();
        element.setAttribute("title", value);
      });
      const binTime = (value) => new Date(value).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      root.querySelectorAll(".timeline-cell[data-bin-start]").forEach((cell) => {
        const start = cell.getAttribute("data-bin-start");
        const end = cell.getAttribute("data-bin-end");
        const summary = cell.getAttribute("data-bin-summary") || "";
        if (!start || !end || Number.isNaN(Date.parse(start)) || Number.isNaN(Date.parse(end))) return;
        cell.setAttribute("title", binTime(start) + " – " + binTime(end) + " · " + summary);
      });
    }
    prepareFragment(document);
    function withScrollPreserved(update) {
      const positions = [document.scrollingElement, ...document.querySelectorAll("main, main *")]
        .filter((element) => element && (element.scrollTop || element.scrollLeft))
        .map((element) => [element, element.scrollTop, element.scrollLeft]);
      const selection = document.getSelection?.();
      const anchor = selection?.anchorNode;
      const anchorOffset = selection?.anchorOffset;
      const focus = selection?.focusNode;
      const focusOffset = selection?.focusOffset;
      const active = document.activeElement;
      update();
      if (active?.isConnected && document.activeElement !== active) active.focus?.({ preventScroll: true });
      if (anchor?.isConnected && focus?.isConnected &&
          (selection.anchorNode !== anchor || selection.anchorOffset !== anchorOffset || selection.focusNode !== focus || selection.focusOffset !== focusOffset)) {
        const bound = (node, offset) => Math.min(offset, node.nodeType === 3 ? node.nodeValue.length : node.childNodes.length);
        selection.setBaseAndExtent(anchor, bound(anchor, anchorOffset), focus, bound(focus, focusOffset));
      }
      positions.forEach(([element, top, left]) => {
        if (!element.isConnected) return;
        if (element.scrollTop !== top) element.scrollTop = top;
        if (element.scrollLeft !== left) element.scrollLeft = left;
      });
    }
    async function refreshJob(target) {
      if (target.dataset.loading === "true") return;
      target.dataset.loading = "true";
      const body = target.querySelector(".job-live");
      try {
        const response = await fetch(authenticatedLocalUrl(target.getAttribute("data-job-href")), { credentials: "same-origin" });
        if (!response.ok) throw new Error("HTTP " + response.status);
        const incoming = body.cloneNode(false);
        incoming.innerHTML = await response.text();
        withScrollPreserved(() => reconcileDashboardElement(body, incoming));
        target.querySelector("[data-job-error]")?.remove();
      } catch (error) {
        let note = target.querySelector("[data-job-error]");
        if (!note) { note = document.createElement("p"); note.dataset.jobError = "true"; note.className = "error-note"; target.append(note); }
        note.textContent = "Job update failed; keeping the last output. " + String(error.message || error);
      } finally { target.dataset.loading = "false"; }
    }
    async function loadBatch(card) {
      const target = card.querySelector('.batch-inline[data-batch-state="idle"]');
      if (!card.open || !target) return;
      target.setAttribute("data-batch-state", "loading");
      const body = target.querySelector(".batch-inline-body");
      try {
        const href = target.getAttribute("data-batch-href") || "";
        const response = await fetch(authenticatedLocalUrl(href + (href.includes("?") ? "&" : "?") + "fragment=1"), { credentials: "same-origin" });
        if (!response.ok) throw new Error("HTTP " + response.status);
        body.innerHTML = await response.text();
        prepareFragment(body);
        target.setAttribute("data-batch-state", "loaded");
      } catch (error) {
        body.textContent = "The saved batch could not be loaded (" + String(error && error.message || error) + ").";
        target.setAttribute("data-batch-state", "idle");
      }
    }
    let diffRequest = 0;
    async function loadDiff(panel, force = false) {
      const target = panel.querySelector(".git-diff");
      if (!panel.open || !target || (!force && target.dataset.diffState !== "idle")) return;
      const request = String(++diffRequest);
      const revision = target.dataset.refreshRevision;
      const previousState = target.dataset.diffState;
      target.dataset.diffRequest = request;
      target.dataset.diffState = "loading";
      const isCurrent = () => target.isConnected && target.dataset.diffRequest === request && target.dataset.refreshRevision === revision;
      try {
        const response = await fetch(authenticatedLocalUrl("/activity/diff?project_id=" + encodeURIComponent(target.getAttribute("data-diff-project") || "")), { credentials: "same-origin" });
        if (!response.ok) throw new Error("HTTP " + response.status);
        const html = await response.text();
        if (!isCurrent()) return;
        const incoming = target.cloneNode(false);
        incoming.innerHTML = html;
        withScrollPreserved(() => reconcileDashboardElement(target, incoming, false));
        target.dataset.diffState = "loaded";
      } catch (error) {
        if (!isCurrent()) return;
        let note = target.querySelector("[data-diff-error]");
        if (!note) { note = document.createElement("p"); note.dataset.diffError = "true"; note.className = "error-note"; target.append(note); }
        note.textContent = "Diff update failed; keeping the last content. " + String(error && error.message || error);
        target.dataset.diffState = previousState === "loaded" ? "loaded" : "idle";
      }
    }
    function loadExpandedDetails() {
      document.querySelectorAll("details.action-card[open]").forEach(loadBatch);
      document.querySelectorAll("details.git-details[open]").forEach((panel) => loadDiff(panel, true));
    }
    document.addEventListener("click", (event) => {
      const button = event.target.closest("[data-job-refresh]");
      if (button) refreshJob(button.closest("[data-job-href]"));
    });
    document.addEventListener("click", async (event) => {
      const button = event.target.closest?.("[data-copy-run-id]");
      if (!button) return;
      const id = button.dataset.copyRunId;
      const status = button.parentElement.querySelector(".copy-run-status");
      try {
        await navigator.clipboard.writeText(id);
        status.textContent = "Full ID copied";
      } catch {
        status.textContent = "Copy manually: " + id;
      }
    });
    document.addEventListener("toggle", (event) => {
      const card = event.target;
      if (card.matches?.("details.action-card") && card.open) {
        card.querySelectorAll("[data-job-href]").forEach(refreshJob);
        loadBatch(card);
      }
      if (card.matches?.("details.git-details") && card.open) loadDiff(card);
    }, true);

    let liveLoading = false;
    let liveEpoch = 0;
    let liveScope = new URL(window.location.href).searchParams.get("project_id") || "";
    let liveUpdatedAt = Date.parse(document.querySelector("[data-live-generated-at]")?.getAttribute("data-live-generated-at") || "") || 0;
    const liveTimestamp = (root) => Date.parse(root.querySelector("[data-live-generated-at]")?.getAttribute("data-live-generated-at") || "") || 0;
    async function refreshLive() {
      if (document.hidden || liveLoading) return;
      liveLoading = true;
      const epoch = liveEpoch;
      const panel = document.querySelector("[data-live-panel]");
      if (!panel) { liveLoading = false; return; }
      try {
        const source = new URL(window.location.href);
        const project = source.searchParams.get("project_id") || "";
        const isCurrent = () => epoch === liveEpoch && project === (new URL(window.location.href).searchParams.get("project_id") || "");
        const response = await fetch(authenticatedLocalUrl("/activity/live" + (project ? "?project_id=" + encodeURIComponent(project) : "")), { credentials: "same-origin" });
        if (!response.ok) throw new Error("HTTP " + response.status);
        const incoming = panel.cloneNode(false);
        incoming.innerHTML = await response.text();
        if (!isCurrent() || (project === liveScope && liveTimestamp(incoming) < liveUpdatedAt)) return;
        prepareFragment(incoming);
        withScrollPreserved(() => reconcileDashboardElement(panel, incoming));
        liveScope = project;
        liveUpdatedAt = liveTimestamp(incoming);
      } catch (error) {
        if (epoch !== liveEpoch) return;
        let note = panel.querySelector("[data-live-error]");
        if (!note) { note = document.createElement("p"); note.dataset.liveError = "true"; note.className = "error-note"; panel.append(note); }
        note.textContent = "Live update failed; keeping the last content. Retrying in 5 seconds.";
      } finally { if (epoch === liveEpoch) liveLoading = false; }
    }
    window.setInterval(refreshLive, 5000);
    window.setInterval(() => {
      if (document.hidden) return;
      document.querySelectorAll("details.action-card[open] [data-job-href]").forEach((target) => {
        if (!target.querySelector("[data-job-status]") || target.querySelector('[data-job-status="running"]')) refreshJob(target);
      });
    }, 5000);

    let dashboardLoading = false;
    let dashboardRequest = 0;
    async function refreshDashboard(nextUrl, force = false) {
      if ((document.hidden && !force) || (dashboardLoading && !force)) return false;
      dashboardLoading = true;
      const request = ++dashboardRequest;
      const status = document.querySelector("[data-refresh-status]");
      try {
        const source = new URL(nextUrl || window.location.href, window.location.origin);
        const response = await fetch(authenticatedLocalUrl("/activity/data" + source.search), { credentials: "same-origin" });
        if (!response.ok) throw new Error("HTTP " + response.status);
        const data = await response.json();
        if (request !== dashboardRequest) return null;
        if (!data || typeof data.generatedAt !== "string" || !Array.isArray(data.sections)) throw new Error("Invalid dashboard response");
        const current = new Map(Array.from(document.querySelectorAll("[data-dashboard-section]")).map((element) => [element.getAttribute("data-dashboard-section"), element]));
        const seen = new Set();
        const updates = data.sections.map((section) => {
          if (!section || typeof section.key !== "string" || typeof section.html !== "string" || seen.has(section.key) || !current.has(section.key)) throw new Error("Invalid dashboard section");
          seen.add(section.key);
          const template = document.createElement("template");
          template.innerHTML = section.html;
          const incoming = template.content.firstElementChild;
          if (!incoming || template.content.childElementCount !== 1 || incoming.getAttribute("data-dashboard-section") !== section.key) throw new Error("Invalid dashboard fragment");
          prepareFragment(incoming);
          return [current.get(section.key), incoming];
        });
        if (seen.size !== current.size) throw new Error("Incomplete dashboard response");
        const scope = source.searchParams.get("project_id") || "";
        // A completed dashboard snapshot invalidates any older live fetch.
        // If a newer live snapshot already arrived, keep its panel instead.
        liveEpoch += 1;
        liveLoading = false;
        withScrollPreserved(() => updates.forEach(([before, after]) => {
          if (before.hasAttribute("data-live-panel")) {
            const timestamp = liveTimestamp(after);
            if (scope === liveScope && timestamp < liveUpdatedAt) return;
            liveUpdatedAt = timestamp;
            liveScope = scope;
          }
          reconcileDashboardElement(before, after);
        }));
        const filter = document.querySelector("[data-project-filter]");
        if (filter) filter.value = scope;
        if (nextUrl) {
          history.pushState(null, "", source.pathname + source.search + source.hash);
          window.dispatchEvent(new Event("activityfilterchange"));
        }
        loadExpandedDetails();
        if (status) { status.textContent = "Updated at " + new Date(data.generatedAt).toLocaleString(); status.dataset.state = "current"; }
        return true;
      } catch (error) {
        if (request !== dashboardRequest) return null;
        if (status) {
          status.textContent = "Update failed; keeping the last content. Retrying in 15 seconds. " + String(error.message || error);
          status.dataset.state = "failed";
        }
        return false;
      } finally { if (request === dashboardRequest) dashboardLoading = false; }
    }
    document.addEventListener("change", async (event) => {
      if (!event.target.matches?.("[data-project-filter]")) return;
      const source = new URL(window.location.href);
      const previous = source.searchParams.get("project_id") || "";
      source.searchParams.delete("before_sequence");
      if (event.target.value) source.searchParams.set("project_id", event.target.value);
      else source.searchParams.delete("project_id");
      if (await refreshDashboard(source.href, true) === false) event.target.value = previous;
    });
    document.querySelector("[data-refresh]")?.addEventListener("click", () => { refreshDashboard(undefined, true); refreshLive(); });
    window.addEventListener?.("popstate", () => refreshDashboard(undefined, true));
    window.setInterval(() => refreshDashboard(), 15_000);`
