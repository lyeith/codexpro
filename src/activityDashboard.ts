import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import {
  AuditJournal,
  type ActionStatusResult,
  type CodexProActionV1
} from "./audit.js";
import type { CodexProConfig } from "./config.js";
import { PathGuard, normalizeRelPath } from "./guard.js";
import type { ProjectDefinition } from "./projects/types.js";
import { redactSensitiveText } from "./redact.js";

const ACTIONS_PER_PROJECT = 8;
const MAX_DIFF_PATHS = 120;
const MAX_DIFF_BYTES = 512 * 1024;
const MAX_GIT_METADATA_BYTES = 256 * 1024;
const GIT_TIMEOUT_MS = 4_000;

export interface ActivityDashboardAction {
  sequence: number;
  finishedAt: string;
  toolName: string;
  operation: string;
  status: CodexProActionV1["status"];
  durationMs: number;
  mutating: boolean;
  detail: string;
  changedPathCount: number;
  changedPathsTruncated: boolean;
}

export interface ActivityDashboardGit {
  available: boolean;
  message?: string;
  branch?: string;
  head?: string;
  committedAt?: string;
  dirty: boolean;
  trackedChangedPaths: string[];
  untrackedPaths: string[];
  hiddenPathCount: number;
  omittedPathCount: number;
  additions: number;
  deletions: number;
  diff: string;
  diffTruncated: boolean;
}

export interface ActivityDashboardProject {
  id: string;
  label: string;
  latestActivityAt?: string;
  actions: ActivityDashboardAction[];
  git: ActivityDashboardGit;
}

export interface ActivityDashboardSnapshot {
  generatedAt: string;
  audit: ActionStatusResult;
  projects: ActivityDashboardProject[];
}

interface GitRunResult {
  ok: boolean;
  stdout: string;
  truncated: boolean;
}

function runGit(root: string, args: string[], maxBytes = MAX_GIT_METADATA_BYTES): GitRunResult {
  const result = spawnSync("git", args, {
    cwd: root,
    encoding: "utf8",
    maxBuffer: maxBytes,
    timeout: GIT_TIMEOUT_MS,
    env: {
      ...process.env,
      GIT_OPTIONAL_LOCKS: "0",
      GIT_TERMINAL_PROMPT: "0",
      LC_ALL: "C",
      NO_COLOR: "1"
    }
  });
  const stdout = typeof result.stdout === "string" ? result.stdout : "";
  const errorCode = result.error && typeof result.error === "object" && "code" in result.error
    ? String(result.error.code)
    : "";
  const truncated = errorCode === "ENOBUFS" || Buffer.byteLength(stdout, "utf8") >= maxBytes;
  if (truncated && stdout) {
    return {
      ok: true,
      stdout: `${stdout.slice(0, maxBytes)}\n… diff output truncated by CodexPro …\n`,
      truncated: true
    };
  }
  return {
    ok: !result.error && result.status === 0,
    stdout,
    truncated: false
  };
}

function splitNul(value: string): string[] {
  return value.split("\0").filter((item) => item.length > 0);
}

function unique(values: string[]): string[] {
  return [...new Set(values)];
}

function normalizeGitPath(value: string): string {
  return normalizeRelPath(value).replace(/^\.\//, "");
}

function isSafeDashboardPath(guard: PathGuard, value: string): boolean {
  const normalized = normalizeGitPath(value);
  return Boolean(
    normalized &&
    normalized !== "." &&
    !path.isAbsolute(normalized) &&
    !normalized.startsWith("../") &&
    !guard.isBlockedRelativePath(normalized)
  );
}

function parseShortStat(value: string): { additions: number; deletions: number } {
  const additions = /(?:^|,)\s*(\d+) insertion(?:s)?\(\+\)/.exec(value)?.[1];
  const deletions = /(?:^|,)\s*(\d+) deletion(?:s)?\(-\)/.exec(value)?.[1];
  return {
    additions: additions ? Number(additions) : 0,
    deletions: deletions ? Number(deletions) : 0
  };
}

function unavailableGit(message: string): ActivityDashboardGit {
  return {
    available: false,
    message,
    dirty: false,
    trackedChangedPaths: [],
    untrackedPaths: [],
    hiddenPathCount: 0,
    omittedPathCount: 0,
    additions: 0,
    deletions: 0,
    diff: "",
    diffTruncated: false
  };
}

export function collectProjectGit(
  config: CodexProConfig,
  project: ProjectDefinition,
  guard = new PathGuard(config)
): ActivityDashboardGit {
  try {
    if (!fs.existsSync(project.root) || !fs.statSync(project.root).isDirectory()) {
      return unavailableGit("Project root is unavailable.");
    }
  } catch {
    return unavailableGit("Project root is unavailable.");
  }

  const inside = runGit(project.root, ["rev-parse", "--is-inside-work-tree"], 8 * 1024);
  if (!inside.ok || inside.stdout.trim() !== "true") {
    return unavailableGit("Not a Git working tree.");
  }

  const verifiedHead = runGit(project.root, ["rev-parse", "--verify", "HEAD"], 8 * 1024);
  const hasHead = verifiedHead.ok;
  const branchResult = runGit(project.root, ["branch", "--show-current"], 8 * 1024);
  const headResult = hasHead ? runGit(project.root, ["rev-parse", "--short=12", "HEAD"], 8 * 1024) : undefined;
  const committedAtResult = hasHead ? runGit(project.root, ["log", "-1", "--format=%cI"], 8 * 1024) : undefined;

  const trackedResult = hasHead
    ? runGit(project.root, ["diff", "--relative", "--name-only", "-z", "HEAD", "--", "."])
    : { ok: true, stdout: "", truncated: false };
  const untrackedResult = runGit(project.root, ["ls-files", "--others", "--exclude-standard", "-z", "--", "."]);
  if (!trackedResult.ok || !untrackedResult.ok) {
    return unavailableGit("Git working-tree status could not be read.");
  }

  const allTracked = unique(splitNul(trackedResult.stdout).map(normalizeGitPath));
  const allUntracked = unique(splitNul(untrackedResult.stdout).map(normalizeGitPath));
  const safeTracked = allTracked.filter((item) => isSafeDashboardPath(guard, item));
  const safeUntracked = allUntracked.filter((item) => isSafeDashboardPath(guard, item));
  const hiddenPathCount = allTracked.length + allUntracked.length - safeTracked.length - safeUntracked.length;
  const renderedTracked = safeTracked.slice(0, MAX_DIFF_PATHS);
  const remainingSlots = Math.max(0, MAX_DIFF_PATHS - renderedTracked.length);
  const renderedUntracked = safeUntracked.slice(0, remainingSlots);
  const omittedPathCount = safeTracked.length + safeUntracked.length - renderedTracked.length - renderedUntracked.length;

  let diff = "";
  let diffTruncated = false;
  let additions = 0;
  let deletions = 0;
  if (hasHead && renderedTracked.length) {
    const stat = runGit(project.root, ["diff", "--relative", "--shortstat", "HEAD", "--", ...renderedTracked]);
    if (stat.ok) ({ additions, deletions } = parseShortStat(stat.stdout));
    const renderedDiff = runGit(
      project.root,
      ["diff", "--relative", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--", ...renderedTracked],
      MAX_DIFF_BYTES
    );
    if (renderedDiff.ok) {
      diff = redactSensitiveText(renderedDiff.stdout.trim());
      diffTruncated = renderedDiff.truncated;
    } else {
      diff = "Tracked diff is too large or could not be rendered; the changed-path summary remains available.";
      diffTruncated = true;
    }
  } else if (!hasHead) {
    diff = "This Git working tree has no commit yet.";
  }

  return {
    available: true,
    branch: branchResult.ok && branchResult.stdout.trim() ? redactSensitiveText(branchResult.stdout.trim()) : "detached",
    head: headResult?.ok ? headResult.stdout.trim() : undefined,
    committedAt: committedAtResult?.ok ? committedAtResult.stdout.trim() : undefined,
    dirty: allTracked.length > 0 || allUntracked.length > 0,
    trackedChangedPaths: renderedTracked.map(redactSensitiveText),
    untrackedPaths: renderedUntracked.map(redactSensitiveText),
    hiddenPathCount,
    omittedPathCount,
    additions,
    deletions,
    diff,
    diffTruncated
  };
}

function actionDetail(action: CodexProActionV1): string {
  const paths = action.changed_paths.length ? action.changed_paths : action.targets;
  if (!paths.length) return action.operation;
  const visible = paths.slice(0, 3).map((item) => redactSensitiveText(item));
  const remaining = Math.max(0, paths.length - visible.length);
  return `${visible.join(", ")}${remaining ? ` +${remaining} more` : ""}`;
}

function dashboardAction(action: CodexProActionV1): ActivityDashboardAction {
  return {
    sequence: action.sequence,
    finishedAt: action.finished_at,
    toolName: action.tool_name,
    operation: action.operation,
    status: action.status,
    durationMs: action.duration_ms,
    mutating: action.mutating,
    detail: actionDetail(action),
    changedPathCount: action.changed_path_count,
    changedPathsTruncated: action.changed_paths_truncated
  };
}

export function collectActivityDashboard(
  config: CodexProConfig,
  journal = new AuditJournal(config)
): ActivityDashboardSnapshot {
  const audit = journal.status();
  const guard = new PathGuard(config);
  const projects = config.projects.map((project) => {
    const actions = journal.list({ projectId: project.id, limit: ACTIONS_PER_PROJECT })
      .actions
      .slice()
      .reverse()
      .map(dashboardAction);
    return {
      id: project.id,
      label: project.label,
      latestActivityAt: actions[0]?.finishedAt,
      actions,
      git: collectProjectGit(config, project, guard)
    } satisfies ActivityDashboardProject;
  });

  projects.sort((left, right) => {
    if (left.latestActivityAt && right.latestActivityAt) return right.latestActivityAt.localeCompare(left.latestActivityAt);
    if (left.latestActivityAt) return -1;
    if (right.latestActivityAt) return 1;
    return left.label.localeCompare(right.label);
  });

  return {
    generatedAt: new Date().toISOString(),
    audit,
    projects
  };
}

function escapeHtml(value: unknown): string {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function statusTone(status: ActivityDashboardAction["status"]): string {
  if (status === "succeeded") return "good";
  if (status === "cancelled" || status === "blocked") return "warn";
  return "bad";
}

function renderAction(action: ActivityDashboardAction): string {
  return `<tr>
    <td><time datetime="${escapeHtml(action.finishedAt)}" data-local-time>${escapeHtml(action.finishedAt)}</time></td>
    <td><code>${escapeHtml(action.toolName)}</code><span class="operation">${escapeHtml(action.operation)}</span></td>
    <td><span class="status ${statusTone(action.status)}">${escapeHtml(action.status)}</span></td>
    <td class="detail">${escapeHtml(action.detail)}</td>
    <td class="duration">${escapeHtml(`${action.durationMs} ms`)}</td>
  </tr>`;
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
      <pre>${escapeHtml(git.diff || "No tracked diff. The working tree contains only untracked or safety-filtered paths.")}</pre>
    </div>
  </details>`;
}

function renderProject(project: ActivityDashboardProject): string {
  const git = project.git;
  const activity = project.actions.length
    ? `<div class="table-wrap"><table>
        <thead><tr><th>Time</th><th>Tool</th><th>Status</th><th>Target</th><th>Duration</th></tr></thead>
        <tbody>${project.actions.map(renderAction).join("")}</tbody>
      </table></div>`
    : `<p class="empty">No retained activity for this project.</p>`;
  return `<article class="project-card" data-project="${escapeHtml(project.id)}">
    <header class="project-head">
      <div>
        <span class="project-id">${escapeHtml(project.id)}</span>
        <h2>${escapeHtml(project.label)}</h2>
      </div>
      <div class="project-meta">
        ${git.available ? `<span class="branch">${escapeHtml(git.branch ?? "detached")}</span>` : ""}
        ${project.latestActivityAt ? `<time datetime="${escapeHtml(project.latestActivityAt)}" data-local-time>${escapeHtml(project.latestActivityAt)}</time>` : ""}
      </div>
    </header>
    ${renderGit(project)}
    <section class="activity-block">
      <div class="section-title"><h3>Latest CodexPro actions</h3><span>${escapeHtml(`${project.actions.length} retained`)}</span></div>
      ${activity}
    </section>
  </article>`;
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
    .project-grid { display: grid; gap: 16px; }
    .project-card { overflow: hidden; border: 1px solid var(--rule); border-radius: 14px; background: var(--panel); box-shadow: 0 8px 28px rgba(23, 32, 51, .05); }
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
    pre { max-height: 560px; overflow: auto; margin: 12px 0 0; border-radius: 8px; background: #111827; color: #e5e7eb; padding: 13px; font: 12px/1.55 var(--mono); white-space: pre; tab-size: 2; }
    .activity-block { border-top: 1px solid var(--rule); padding: 14px 20px 20px; }
    .section-title { display: flex; justify-content: space-between; align-items: baseline; gap: 10px; margin-bottom: 10px; }
    .section-title span { color: var(--soft); font-size: 12px; }
    .table-wrap { overflow-x: auto; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; }
    th { color: var(--soft); font-size: 11px; letter-spacing: .06em; text-align: left; text-transform: uppercase; }
    th, td { border-bottom: 1px solid #edf0f5; padding: 9px 8px; vertical-align: top; }
    tbody tr:last-child td { border-bottom: 0; }
    td:first-child { min-width: 170px; }
    td:nth-child(2) { min-width: 145px; }
    td.detail { min-width: 220px; color: var(--soft); }
    td.duration { white-space: nowrap; color: var(--soft); text-align: right; font-family: var(--mono); }
    code { font-family: var(--mono); }
    .operation { display: block; margin-top: 3px; color: var(--soft); font-size: 11px; }
    .status { display: inline-block; border-radius: 999px; padding: 3px 7px; font-size: 11px; font-weight: 750; }
    .status.good { background: var(--good-bg); color: var(--good); }
    .status.warn { background: var(--warn-bg); color: var(--warn); }
    .status.bad { background: var(--bad-bg); color: var(--bad); }
    .empty { margin: 12px 0 0; color: var(--soft); }
    .foot { margin-top: 20px; color: var(--soft); font-size: 12px; text-align: center; }
    @media (max-width: 820px) {
      main { width: min(100% - 20px, 1500px); padding-top: 14px; }
      .topbar, .project-head { align-items: stretch; flex-direction: column; }
      .project-meta { align-items: flex-start; }
      .summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .actions { flex-wrap: wrap; }
      .project-head, .activity-block { padding-left: 14px; padding-right: 14px; }
      .git-panel, .git-details { margin-left: 14px; margin-right: 14px; }
    }
  </style>
</head>
<body>
  <main>
    <header class="topbar">
      <div class="brand">
        <img src="/favicon.ico" alt="">
        <div><span class="eyebrow">Authenticated local control</span><h1>Activity & changes</h1><p class="subtitle">Recent CodexPro tool calls and each configured checkout’s working-tree diff.</p></div>
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
    <section class="project-grid">${projectCards}</section>
    <footer class="foot">Auto-refreshes every 15 seconds while no diff panel is open. Raw shell command text, file bodies, blocked paths, and untracked file contents are not rendered.</footer>
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
