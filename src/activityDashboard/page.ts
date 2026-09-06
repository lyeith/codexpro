import { escapeHtml, humanDuration, plural, statusTone } from "./format.js";
import { renderTimeline } from "./timeline.js";
import type {
  ActivityBatchView,
  ActivityDashboardAction,
  ActivityDashboardEvidence,
  ActivityDashboardField,
  ActivityDashboardGitEvidence,
  ActivityDashboardProject,
  ActivityDashboardSnapshot
} from "./types.js";

type SplitDiffTone = "context" | "removed" | "added" | "empty";

interface SplitDiffCell {
  lineNumber?: number;
  text: string;
  tone: SplitDiffTone;
}

type SplitDiffRow =
  | { kind: "line"; before: SplitDiffCell; after: SplitDiffCell }
  | { kind: "note"; text: string };

interface SplitDiffHunk {
  header: string;
  rows: SplitDiffRow[];
}

interface SplitDiffFile {
  beforePath: string;
  afterPath: string;
  metadata: string[];
  hunks: SplitDiffHunk[];
}

function parseDiffPath(value: string): string {
  const withoutTimestamp = value.trim().split("\t", 1)[0] ?? value.trim();
  const unquoted = withoutTimestamp.startsWith('"') && withoutTimestamp.endsWith('"')
    ? withoutTimestamp.slice(1, -1)
    : withoutTimestamp;
  return unquoted === "/dev/null" ? unquoted : unquoted.replace(/^[ab]\//, "");
}

function parseUnifiedDiff(value: string): SplitDiffFile[] {
  const files: SplitDiffFile[] = [];
  let file: SplitDiffFile | undefined;
  let hunk: SplitDiffHunk | undefined;
  let beforeLine = 0;
  let afterLine = 0;
  let removed: SplitDiffCell[] = [];
  let added: SplitDiffCell[] = [];

  const flushChanges = () => {
    if (!hunk || (!removed.length && !added.length)) return;
    const count = Math.max(removed.length, added.length);
    for (let index = 0; index < count; index += 1) {
      hunk.rows.push({
        kind: "line",
        before: removed[index] ?? { text: "", tone: "empty" },
        after: added[index] ?? { text: "", tone: "empty" }
      });
    }
    removed = [];
    added = [];
  };

  for (const line of value.replace(/\r\n/g, "\n").split("\n")) {
    if (line.startsWith("diff --git ")) {
      flushChanges();
      const paths = /^diff --git a\/(.+) b\/(.+)$/.exec(line);
      file = {
        beforePath: paths?.[1] ?? "Before",
        afterPath: paths?.[2] ?? "After",
        metadata: [],
        hunks: []
      };
      files.push(file);
      hunk = undefined;
      continue;
    }
    if (!file) continue;

    const hunkHeader = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(?:.*)$/.exec(line);
    if (hunkHeader) {
      flushChanges();
      beforeLine = Number(hunkHeader[1]);
      afterLine = Number(hunkHeader[2]);
      hunk = { header: line, rows: [] };
      file.hunks.push(hunk);
      continue;
    }

    if (!hunk && line.startsWith("--- ")) {
      file.beforePath = parseDiffPath(line.slice(4));
      continue;
    }
    if (!hunk && line.startsWith("+++ ")) {
      file.afterPath = parseDiffPath(line.slice(4));
      continue;
    }
    if (!hunk) {
      if (line) file.metadata.push(line);
      continue;
    }

    const prefix = line[0];
    if (prefix === " ") {
      flushChanges();
      const text = line.slice(1);
      hunk.rows.push({
        kind: "line",
        before: { lineNumber: beforeLine, text, tone: "context" },
        after: { lineNumber: afterLine, text, tone: "context" }
      });
      beforeLine += 1;
      afterLine += 1;
    } else if (prefix === "-") {
      removed.push({ lineNumber: beforeLine, text: line.slice(1), tone: "removed" });
      beforeLine += 1;
    } else if (prefix === "+") {
      added.push({ lineNumber: afterLine, text: line.slice(1), tone: "added" });
      afterLine += 1;
    } else {
      flushChanges();
      if (line) hunk.rows.push({ kind: "note", text: line });
    }
  }
  flushChanges();
  return files;
}

function renderSplitDiffCell(cell: SplitDiffCell, side: "before" | "after"): string {
  const lineNumber = cell.lineNumber === undefined ? "" : String(cell.lineNumber);
  return `<span class="diff-line-number ${side} ${cell.tone}">${escapeHtml(lineNumber)}</span><code class="diff-line-code ${side} ${cell.tone}">${escapeHtml(cell.text)}</code>`;
}

function renderSplitDiffRow(row: SplitDiffRow): string {
  if (row.kind === "note") return `<div class="diff-note">${escapeHtml(row.text)}</div>`;
  return `<div class="diff-row">${renderSplitDiffCell(row.before, "before")}${renderSplitDiffCell(row.after, "after")}</div>`;
}

function renderSplitDiffFile(file: SplitDiffFile): string {
  const visiblePath = file.afterPath === "/dev/null" ? file.beforePath : file.afterPath;
  const metadata = file.metadata.map((line) => `<div class="diff-metadata">${escapeHtml(line)}</div>`).join("");
  const hunks = file.hunks.map((item) => `<div class="diff-hunk-header">${escapeHtml(item.header)}</div>${item.rows.map(renderSplitDiffRow).join("")}`).join("");
  const contents = metadata || hunks
    ? `${metadata}${hunks}`
    : `<div class="diff-note">No textual hunk is available for this mode, rename, or binary change.</div>`;
  return `<section class="diff-file">
    <header class="diff-file-heading"><code>${escapeHtml(visiblePath)}</code><span>shared scroll · matched lines</span></header>
    <div class="split-diff-scroll" tabindex="0" aria-label="Side-by-side diff for ${escapeHtml(visiblePath)}">
      <div class="split-diff-grid">
        <div class="diff-side-heading before"><span>Before</span><code>${escapeHtml(file.beforePath)}</code></div>
        <div class="diff-side-heading after"><span>After</span><code>${escapeHtml(file.afterPath)}</code></div>
        ${contents}
      </div>
    </div>
  </section>`;
}

function renderSplitDiff(value: string): string {
  const files = parseUnifiedDiff(value);
  if (!files.length) return `<pre class="diff-fallback">${escapeHtml(value)}</pre>`;
  return `<div class="split-diff">${files.map(renderSplitDiffFile).join("")}</div>`;
}

function renderFieldSection(title: string, fields: ActivityDashboardField[]): string {
  if (!fields.length) return "";
  return `<section class="action-section">
    <h4>${escapeHtml(title)}</h4>
    <dl class="field-grid">${fields.map((field) => `<div>
      <dt>${escapeHtml(field.label)}</dt>
      <dd class="${field.mono ? "mono-value" : ""} ${field.tone ?? ""}">${escapeHtml(field.value)}</dd>
    </div>`).join("")}</dl>
  </section>`;
}

function renderActionEvidence(title: string, evidence: ActivityDashboardEvidence[]): string {
  if (!evidence.length) return "";
  return `<section class="action-section">
    <h4>${escapeHtml(title)}</h4>
    <dl class="evidence-list">${evidence.map((item) => `<div><dt>${escapeHtml(item.label)}</dt><dd>${escapeHtml(item.value)}</dd></div>`).join("")}</dl>
  </section>`;
}

function gitEvidenceText(value: ActivityDashboardGitEvidence): string {
  const identity = [value.branch, value.head?.slice(0, 12)].filter(Boolean).join(" @ ") || "No Git identity";
  return `${identity} · ${value.dirty ? plural(value.changedPathCount, "changed path") : "clean"}`;
}

function renderActionGit(action: ActivityDashboardAction): string {
  if (!action.gitBefore && !action.gitAfter) return "";
  return `<section class="action-section">
    <h4>Git evidence</h4>
    <div class="git-transition">
      ${action.gitBefore ? `<div><span>Before</span><code>${escapeHtml(gitEvidenceText(action.gitBefore))}</code></div>` : ""}
      ${action.gitAfter ? `<div><span>After</span><code>${escapeHtml(gitEvidenceText(action.gitAfter))}</code></div>` : ""}
    </div>
  </section>`;
}

function renderShellScripts(action: ActivityDashboardAction): string {
  if (!action.shellScripts.length) {
    return action.toolName === "bash"
      ? `<p class="privacy-note"><strong>Shell script unavailable:</strong> this retained action predates exact-command capture.</p>`
      : "";
  }
  return action.shellScripts.map((item, index) => {
    const title = item.operationId
      ? `Shell script · ${item.operationId}`
      : action.shellScripts.length > 1
        ? `Shell script ${index + 1}`
        : "Shell script";
    return `<section class="shell-script"><div class="shell-script-head"><h4>${escapeHtml(title)}</h4><span>${item.truncated ? "truncated at journal limit" : "exact command"}</span></div><pre>${escapeHtml(item.script)}</pre></section>`;
  }).join("");
}

function renderAction(action: ActivityDashboardAction): string {
  const pathNotes = [
    action.hiddenPathCount ? `${plural(action.hiddenPathCount, "blocked path")} hidden` : "",
    action.changedPathsTruncated ? "changed-path list truncated" : ""
  ].filter(Boolean);
  const changedPaths = action.changedPaths.length
    ? `<section class="action-section changed-paths"><h4>Changed paths</h4><div class="path-list">${action.changedPaths.map((item) => `<code class="path tracked">${escapeHtml(item)}</code>`).join("")}</div></section>`
    : "";
  const batchLink = action.batchHref && action.batchPath
    ? `<a class="batch-link" href="${escapeHtml(action.batchHref)}" data-local-link target="_blank" rel="noopener"><span>Open saved batch</span><code>${escapeHtml(action.batchPath)}</code><b aria-hidden="true">↗</b></a>`
    : "";
  const shellScripts = renderShellScripts(action);
  const error = action.errorCode
    ? `<p class="error-note"><strong>Error code:</strong> <code>${escapeHtml(action.errorCode)}</code></p>`
    : "";
  return `<details class="action-card" id="action-${escapeHtml(action.actionId)}" data-action-id="${escapeHtml(action.actionId)}">
    <summary>
      <div class="action-time"><time datetime="${escapeHtml(action.finishedAt)}" data-local-time>${escapeHtml(action.finishedAt)}</time><span>#${escapeHtml(action.sequence)}</span></div>
      <div class="action-summary-main">
        <div class="action-title"><code>${escapeHtml(action.toolName)}</code><strong>${escapeHtml(action.headline)}</strong></div>
        <div class="action-subtitle"><span>${escapeHtml(action.operation)}</span><span>${escapeHtml(action.operationClass)}</span><span>${escapeHtml(humanDuration(action.durationMs))}</span>${action.mutating ? `<span class="mutating">mutating</span>` : ""}</div>
      </div>
      <span class="status ${statusTone(action.status)}">${escapeHtml(action.status)}</span>
    </summary>
    <div class="action-body">
      ${changedPaths}
      ${batchLink}
      ${pathNotes.length ? `<p class="safety-note">${escapeHtml(pathNotes.join(" · "))}</p>` : ""}
      <div class="action-detail-grid">
        ${renderFieldSection("Request", action.requestFields)}
        ${renderFieldSection("Result", action.resultFields)}
        ${renderActionEvidence("File evidence", action.pathEvidence)}
        ${renderActionGit(action)}
      </div>
      ${shellScripts}
      ${error}
      <div class="action-identity"><span>Action</span><code>${escapeHtml(action.actionId)}</code></div>
    </div>
  </details>`;
}

function renderPathList(title: string, paths: string[], tone: string): string {
  if (!paths.length) return "";
  return `<div class="path-group">
    <strong>${escapeHtml(title)}</strong>
    <div class="path-list">${paths.map((item) => `<code class="path ${tone}">${escapeHtml(item)}</code>`).join("")}</div>
  </div>`;
}

function renderGit(project: ActivityDashboardProject): string {
  const git = project.git;
  if (!git.available) {
    return `<section class="git-panel unavailable"><div><strong>Git</strong><span>${escapeHtml(git.message ?? "Unavailable")}</span></div></section>`;
  }
  if (!git.dirty) {
    return `<section class="git-panel clean">
      <div><strong>Working tree</strong><span>Clean at <code>${escapeHtml(git.head ?? "no commit")}</code></span></div>
      ${git.committedAt ? `<time datetime="${escapeHtml(git.committedAt)}" data-local-time>${escapeHtml(git.committedAt)}</time>` : ""}
    </section>`;
  }

  const visibleCount = git.trackedChangedPaths.length + git.untrackedPaths.length;
  const notes = [
    git.hiddenPathCount ? `${git.hiddenPathCount} safety-blocked path${git.hiddenPathCount === 1 ? "" : "s"} hidden` : "",
    git.omittedPathCount ? `${git.omittedPathCount} additional path${git.omittedPathCount === 1 ? "" : "s"} omitted` : "",
    git.diffTruncated ? "diff output truncated" : ""
  ].filter(Boolean);
  return `<details class="git-details">
    <summary>
      <span><strong>Diff from HEAD</strong><small>${escapeHtml(`${visibleCount} visible path${visibleCount === 1 ? "" : "s"}`)}</small></span>
      <span class="delta"><b>+${git.additions}</b><b>−${git.deletions}</b></span>
    </summary>
    <div class="git-body">
      ${renderPathList("Tracked changes", git.trackedChangedPaths, "tracked")}
      ${renderPathList("Untracked files (contents not rendered)", git.untrackedPaths, "untracked")}
      ${notes.length ? `<p class="safety-note">${escapeHtml(notes.join(" · "))}</p>` : ""}
      ${renderSplitDiff(git.diff || "No tracked diff. The working tree contains only untracked or safety-filtered paths.")}
    </div>
  </details>`;
}

function renderProject(project: ActivityDashboardProject): string {
  const git = project.git;
  return `<article class="project-card" data-project="${escapeHtml(project.id)}">
    <header class="project-head">
      <div>
        <span class="project-id">${escapeHtml(project.id)}</span>
        <h2>${escapeHtml(project.label)}</h2>
      </div>
      <div class="project-meta">
        ${git.available ? `<span class="branch">${escapeHtml(git.branch ?? "detached")}</span>` : ""}
        ${project.latestActivityAt ? `<time datetime="${escapeHtml(project.latestActivityAt)}" data-local-time>${escapeHtml(project.latestActivityAt)}</time>` : `<span>No retained activity for this project.</span>`}
      </div>
    </header>
    ${renderGit(project)}
  </article>`;
}

function renderRecentCommands(actions: ActivityDashboardAction[]): string {
  const rows = actions.map((action) => `<tr class="command-record">
    <td class="command-when"><time datetime="${escapeHtml(action.finishedAt)}" data-local-time>${escapeHtml(action.finishedAt)}</time><small>#${escapeHtml(action.sequence)}</small></td>
    <td class="command-project"><strong>${escapeHtml(action.projectLabel)}</strong><code>${escapeHtml(action.projectId ?? "global")}</code>${action.attributionRecovered ? `<span class="recovered">recovered from workspace</span>` : ""}</td>
    <td class="command-cell">${renderAction(action)}</td>
  </tr>`).join("");
  return `<section class="dashboard-section recent-panel">
    <div class="section-heading"><div><span class="eyebrow">Newest first</span><h2>Last 30 commands</h2></div><span>${escapeHtml(`${actions.length} shown across all projects`)}</span></div>
    ${rows ? `<div class="command-table-scroll"><table class="command-table"><caption class="visually-hidden">Last 30 CodexPro commands across every project</caption><thead><tr><th scope="col">Time</th><th scope="col">Project</th><th scope="col">Command</th></tr></thead><tbody>${rows}</tbody></table></div>` : `<p class="empty">No retained commands.</p>`}
  </section>`;
}

export function renderActivityDashboardPage(snapshot: ActivityDashboardSnapshot): string {
  const auditWarning = snapshot.audit.enabled
    ? snapshot.audit.gap_detected || snapshot.audit.malformed_records
      ? `<div class="banner bad">The action journal reports ${escapeHtml(snapshot.audit.malformed_records)} malformed record(s)${snapshot.audit.gap_detected ? " and a sequence gap" : ""}. Activity may be incomplete.</div>`
      : ""
    : `<div class="banner warn">Debug activity is disabled. Git state is available, but recent CodexPro actions require <code>--audit metadata</code>.</div>`;
  const projectCards = snapshot.projects.length
    ? snapshot.projects.map(renderProject).join("")
    : `<div class="banner warn">No runnable projects are configured.</div>`;
  const timeline = renderTimeline(snapshot.timelineActions, new Set(snapshot.projects.map((project) => project.id)), snapshot.generatedAt);
  const recentCommands = renderRecentCommands(snapshot.recentActions);

  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico">
  <title>CodexPro Activity & Changes</title>
  <style>
    :root {
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
    .timeline-track { min-height: 30px; overflow: hidden; }
    .timeline-grid { position: absolute; top: 0; bottom: 0; width: 1px; background: #edf0f5; }
    .timeline-cell { position: absolute; top: 5px; bottom: 5px; z-index: 2; min-width: 3px; border-radius: 2px; background: var(--good); }
    .timeline-cell.warn { background: var(--warn); }
    .timeline-cell.bad { background: var(--bad); }
    .timeline-cell.has-mutation { box-shadow: inset 0 -3px 0 rgba(23, 32, 51, .45); }
    .timeline-cell:hover { opacity: 1 !important; outline: 2px solid var(--ink); outline-offset: 1px; z-index: 3; }
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
      .command-cell .action-time { display: none; }
      .action-summary-main { grid-column: 1; }
      .action-card > summary > .status { grid-column: 2; }
      .action-detail-grid, .field-grid, .evidence-list { grid-template-columns: 1fr; }
      .action-title { align-items: flex-start; flex-direction: column; gap: 5px; }
      .action-title strong { white-space: normal; }
    }
  </style>
</head>
<body>
  <main>
    <header class="topbar">
      <div class="brand">
        <img src="/favicon.ico" alt="">
        <div><span class="eyebrow">Authenticated local control</span><h1>Activity & changes</h1><p class="subtitle">A cross-project timeline, the latest commands, and every configured checkout’s current working-tree state.</p></div>
      </div>
      <div class="actions">
        <a class="button" href="/setup" data-local-link>Setup</a>
        <button class="button primary" type="button" data-refresh>Refresh</button>
      </div>
    </header>
    <section class="summary" aria-label="Activity status">
      <div class="metric"><span>Projects</span><strong>${escapeHtml(snapshot.projects.length)}</strong></div>
      <div class="metric"><span>Retained actions</span><strong>${escapeHtml(snapshot.audit.action_count)}</strong></div>
      <div class="metric"><span>Latest sequence</span><strong>${escapeHtml(snapshot.audit.latest_sequence)}</strong></div>
      <div class="metric"><span>Updated</span><strong><time datetime="${escapeHtml(snapshot.generatedAt)}" data-local-time>${escapeHtml(snapshot.generatedAt)}</time></strong></div>
    </section>
    ${auditWarning}
    ${timeline}
    ${recentCommands}
    <section class="dashboard-section project-section"><div class="section-heading"><div><span class="eyebrow">Current repository state</span><h2>Project working trees</h2></div><span>${escapeHtml(`${snapshot.projects.length} configured`)}</span></div><div class="project-grid">${projectCards}</div></section>
    <footer class="foot">Auto-refreshes every 15 seconds while no detail panel is open. Exact Bash scripts and safety-filtered tracked diffs are rendered; blocked paths and untracked file contents remain hidden.</footer>
  </main>
  <script>
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
    document.querySelectorAll("[data-local-link]").forEach((link) => {
      link.setAttribute("href", authenticatedLocalUrl(link.getAttribute("href") || "/"));
    });
    document.querySelectorAll("[data-local-time]").forEach((element) => {
      const value = element.getAttribute("datetime");
      const parsed = value ? new Date(value) : null;
      if (!parsed || Number.isNaN(parsed.getTime())) return;
      element.textContent = parsed.toLocaleString();
      element.setAttribute("title", value);
    });
    document.querySelectorAll("[data-local-axis]").forEach((element) => {
      const value = element.getAttribute("datetime");
      const parsed = value ? new Date(value) : null;
      if (!parsed || Number.isNaN(parsed.getTime())) return;
      element.textContent = parsed.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
      element.setAttribute("title", value);
    });
    document.querySelector("[data-refresh]")?.addEventListener("click", () => {
      window.location.assign(authenticatedLocalUrl("/activity"));
    });
    window.setInterval(() => {
      if (document.hidden || document.querySelector("details[open]")) return;
      window.location.assign(authenticatedLocalUrl("/activity"));
    }, 15_000);
  </script>
</body>
</html>`;
}

function batchOperationCards(definition: unknown): string {
  const root = definition && typeof definition === "object" && !Array.isArray(definition)
    ? definition as Record<string, unknown>
    : {};
  const operations = Array.isArray(root.operations) ? root.operations : [];
  if (!operations.length) return `<p class="empty">This JSON has no operation list.</p>`;
  return operations.map((raw, index) => {
    const operation = raw && typeof raw === "object" && !Array.isArray(raw)
      ? raw as Record<string, unknown>
      : {};
    const id = typeof operation.id === "string" && operation.id ? operation.id : `op_${index + 1}`;
    const tool = typeof operation.tool === "string" && operation.tool ? operation.tool : "unknown";
    const args = operation.args && typeof operation.args === "object" && !Array.isArray(operation.args)
      ? operation.args
      : {};
    return `<details class="batch-operation" open>
      <summary><span><b>${escapeHtml(index)}</b><code>${escapeHtml(id)}</code></span><strong>${escapeHtml(tool)}</strong></summary>
      <pre>${escapeHtml(JSON.stringify(args, null, 2) ?? "{}")}</pre>
    </details>`;
  }).join("");
}

export function renderActivityBatchPage(view: ActivityBatchView): string {
  const root = view.definition && typeof view.definition === "object" && !Array.isArray(view.definition)
    ? view.definition as Record<string, unknown>
    : {};
  const operationCount = Array.isArray(root.operations) ? root.operations.length : 0;
  const mode = typeof root.mode === "string" ? root.mode : "unknown";
  const continueOnError = root.continue_on_error === true;
  const rawDefinition = JSON.stringify(view.definition, null, 2) ?? "null";
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico">
  <title>${escapeHtml(view.path)} · CodexPro Batch</title>
  <style>
    :root { color-scheme: light; font-family: "Geist", "Aptos", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; --paper: #f4f6f9; --panel: #fff; --ink: #172033; --soft: #5b667a; --rule: #dce2eb; --accent: #2563eb; --mono: "Fira Code", "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--paper); color: var(--ink); }
    main { width: min(1100px, calc(100% - 28px)); margin: 0 auto; padding: 24px 0 48px; }
    a { color: inherit; }
    .back { display: inline-flex; align-items: center; gap: 7px; margin-bottom: 16px; color: var(--accent); font-size: 13px; font-weight: 700; text-decoration: none; }
    .head, .panel { border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); box-shadow: 0 8px 28px rgba(23,32,51,.05); }
    .head { padding: 20px; }
    .eyebrow { color: var(--soft); font-size: 11px; font-weight: 750; letter-spacing: .08em; text-transform: uppercase; }
    h1 { overflow-wrap: anywhere; margin: 5px 0 7px; font: 700 clamp(22px, 4vw, 34px)/1.15 var(--mono); letter-spacing: -.025em; }
    .project { margin: 0; color: var(--soft); }
    .badges { display: flex; flex-wrap: wrap; gap: 7px; margin-top: 15px; }
    .badge { border-radius: 999px; background: #eef2f7; padding: 5px 9px; font: 11px var(--mono); }
    .note { margin: 12px 0 0; border-radius: 8px; background: #fff7e5; padding: 9px 11px; color: #895000; font-size: 12px; }
    .panel { margin-top: 14px; padding: 16px; }
    .panel h2 { margin: 0 0 12px; font-size: 15px; }
    .operation-list { display: grid; gap: 8px; }
    .batch-operation { overflow: hidden; border: 1px solid #e4e8ef; border-radius: 9px; background: #fbfcfe; }
    .batch-operation summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 12px; cursor: pointer; }
    .batch-operation summary span { display: flex; align-items: center; gap: 9px; min-width: 0; }
    .batch-operation summary b { display: inline-grid; min-width: 24px; height: 24px; place-items: center; border-radius: 6px; background: #e9eef7; color: #59677e; font: 10px var(--mono); }
    .batch-operation summary code, .batch-operation summary strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .batch-operation summary strong { color: #38517d; font: 700 11px var(--mono); }
    pre { overflow: auto; max-height: 580px; margin: 0; border-top: 1px solid #e4e8ef; background: #111827; padding: 13px; color: #e5e7eb; font: 12px/1.55 var(--mono); white-space: pre; tab-size: 2; }
    .raw pre { border: 0; border-radius: 9px; }
    .empty { margin: 0; color: var(--soft); }
  </style>
</head>
<body>
  <main>
    <a class="back" href="/activity" data-local-link>← Activity & changes</a>
    <header class="head">
      <span class="eyebrow">Saved CodexPro batch</span>
      <h1>${escapeHtml(view.path)}</h1>
      <p class="project">${escapeHtml(view.projectLabel)} <code>${escapeHtml(view.projectId)}</code>${view.workspaceId ? ` · workspace <code>${escapeHtml(view.workspaceId)}</code>` : ""}</p>
      <div class="badges"><span class="badge">${escapeHtml(mode)}</span><span class="badge">${escapeHtml(operationCount)} operation${operationCount === 1 ? "" : "s"}</span><span class="badge">continue on error: ${continueOnError ? "yes" : "no"}</span><span class="badge">${view.autoStored ? "auto-stored" : "custom JSON"}</span></div>
      <p class="note">This is the current saved definition. If the batch was edited after the action ran, it may differ from the historical invocation. Exact commands and arguments can contain credentials or other sensitive values; treat this authenticated page as sensitive.</p>
    </header>
    <section class="panel"><h2>Operations</h2><div class="operation-list">${batchOperationCards(view.definition)}</div></section>
    <details class="panel raw"><summary><strong>Raw JSON</strong></summary><pre>${escapeHtml(rawDefinition)}</pre></details>
  </main>
  <script>
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
    document.querySelectorAll("[data-local-link]").forEach((link) => {
      const target = new URL(link.getAttribute("href") || "/", window.location.origin);
      if (connectorCredential) target.searchParams.set("codexpro_token", connectorCredential);
      link.setAttribute("href", target.pathname + target.search);
    });
  </script>
</body>
</html>`;
}
