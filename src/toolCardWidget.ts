export const TOOL_CARD_URI = "ui://widget/codexpro-tool-card-v10.html";
export const TOOL_CARD_LEGACY_URIS = [
  "ui://widget/codexpro-tool-card-v9.html",
  "ui://widget/codexpro-tool-card-v8.html"
];
export const TOOL_CARD_MIME_TYPE = "text/html;profile=mcp-app";

// This widget deliberately stays self-contained. It receives tool results through the
// Apps SDK bridge and does not make network requests, invoke tools, or persist data.
export const toolCardWidgetHtml = String.raw`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root {
      color-scheme: light;
      --card: #ffffff;
      --canvas: #f7f7f8;
      --ink: #202123;
      --muted: #6e6e80;
      --faint: #ececf1;
      --line: #dedee5;
      --code: #f1f1f3;
      --good: #1f7a4c;
      --warn: #a55200;
      --bad: #b42318;
      --shadow: 0 1px 2px rgba(15, 15, 20, .06), 0 8px 28px rgba(15, 15, 20, .05);
    }

    :root[data-theme="dark"] {
      color-scheme: dark;
      --card: #2f2f2f;
      --canvas: #212121;
      --ink: #ececf1;
      --muted: #b4b4bf;
      --faint: #3a3a3a;
      --line: #4a4a4a;
      --code: #242424;
      --good: #5ccf91;
      --warn: #f1a75b;
      --bad: #ff8a80;
      --shadow: 0 1px 2px rgba(0, 0, 0, .22), 0 10px 28px rgba(0, 0, 0, .18);
    }

    * { box-sizing: border-box; }
    html, body { margin: 0; min-width: 0; background: transparent; }
    body {
      color: var(--ink);
      font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      font-size: 13px;
      line-height: 1.45;
      -webkit-font-smoothing: antialiased;
    }
    button, summary { font: inherit; }
    #root { width: 100%; }
    .card {
      width: 100%;
      max-width: 780px;
      overflow: hidden;
      border: 1px solid var(--line);
      border-radius: 16px;
      background: var(--card);
      box-shadow: var(--shadow);
    }
    .head {
      display: flex;
      align-items: center;
      gap: 10px;
      min-height: 50px;
      padding: 12px 14px;
      border-bottom: 1px solid var(--faint);
    }
    .tool-icon {
      display: grid;
      width: 26px;
      height: 26px;
      flex: 0 0 auto;
      place-items: center;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--canvas);
      color: var(--muted);
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 12px;
      font-weight: 700;
    }
    .title-group { min-width: 0; flex: 1; }
    .title {
      overflow: hidden;
      color: var(--ink);
      font-size: 13px;
      font-weight: 650;
      letter-spacing: -.01em;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .subtitle {
      overflow: hidden;
      margin-top: 1px;
      color: var(--muted);
      font-size: 11px;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .badge {
      flex: 0 0 auto;
      padding: 3px 7px;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: var(--canvas);
      color: var(--muted);
      font-size: 10px;
      font-weight: 650;
      letter-spacing: .01em;
    }
    .badge.good { color: var(--good); }
    .badge.warn { color: var(--warn); }
    .badge.bad { color: var(--bad); }
    .body { padding: 13px 14px 14px; }
    .summary { margin: 0 0 12px; color: var(--muted); }
    .summary strong { color: var(--ink); font-weight: 600; }
    .metrics {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(96px, 1fr));
      gap: 8px;
      margin: 0 0 12px;
    }
    .metric {
      min-width: 0;
      padding: 9px 10px;
      border: 1px solid var(--faint);
      border-radius: 10px;
      background: var(--canvas);
    }
    .metric-value {
      overflow: hidden;
      color: var(--ink);
      font-size: 15px;
      font-weight: 680;
      letter-spacing: -.02em;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .metric-label { margin-top: 1px; color: var(--muted); font-size: 10px; }
    .facts {
      display: grid;
      grid-template-columns: minmax(78px, auto) minmax(0, 1fr);
      gap: 7px 12px;
      margin: 0;
    }
    .facts dt { color: var(--muted); }
    .facts dd {
      min-width: 0;
      margin: 0;
      overflow-wrap: anywhere;
      color: var(--ink);
    }
    .mono, code, pre {
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 11px;
    }
    .mono { color: var(--ink); }
    .path { overflow-wrap: anywhere; }
    .chips { display: flex; flex-wrap: wrap; gap: 6px; }
    .chip {
      max-width: 100%;
      overflow: hidden;
      padding: 4px 7px;
      border: 1px solid var(--line);
      border-radius: 7px;
      background: var(--canvas);
      color: var(--ink);
      font-size: 11px;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
    .list li {
      display: flex;
      align-items: baseline;
      gap: 8px;
      min-width: 0;
      color: var(--ink);
    }
    .list li::before {
      width: 4px;
      height: 4px;
      flex: 0 0 auto;
      border-radius: 50%;
      background: var(--muted);
      content: "";
    }
    .list span { min-width: 0; overflow-wrap: anywhere; }
    .notice {
      padding: 10px 11px;
      border: 1px solid var(--faint);
      border-radius: 10px;
      background: var(--canvas);
      color: var(--muted);
    }
    .notice.warn { border-color: color-mix(in srgb, var(--warn) 30%, var(--line)); color: var(--warn); }
    .notice.bad { border-color: color-mix(in srgb, var(--bad) 34%, var(--line)); color: var(--bad); }
    details.fold { margin-top: 10px; border-top: 1px solid var(--faint); }
    details.fold summary {
      display: flex;
      cursor: pointer;
      align-items: center;
      min-height: 34px;
      color: var(--muted);
      font-size: 11px;
      list-style: none;
      user-select: none;
    }
    details.fold summary::-webkit-details-marker { display: none; }
    details.fold summary::after { margin-left: auto; content: "⌄"; font-size: 14px; }
    details.fold[open] summary::after { content: "⌃"; }
    .fold-content { padding: 0 0 3px; }
    .code-shell { position: relative; overflow: hidden; border: 1px solid var(--faint); border-radius: 10px; background: var(--code); }
    .code-topline {
      display: flex;
      align-items: center;
      min-height: 34px;
      padding: 0 8px 0 10px;
      border-bottom: 1px solid var(--faint);
      color: var(--muted);
      font-size: 10px;
    }
    .copy-card-output {
      margin-left: auto;
      border: 0;
      border-radius: 7px;
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      padding: 4px 6px;
      font-size: 10px;
    }
    .copy-card-output:hover, .copy-card-output:focus-visible { background: var(--faint); color: var(--ink); outline: none; }
    pre {
      max-height: 244px;
      margin: 0;
      padding: 10px;
      overflow: auto;
      color: var(--ink);
      line-height: 1.5;
      tab-size: 2;
      white-space: pre-wrap;
      word-break: break-word;
    }
    .empty { color: var(--muted); }
    .pending .body { padding: 12px 14px; }
    @media (max-width: 420px) {
      .card { border-radius: 13px; }
      .head, .body { padding-left: 12px; padding-right: 12px; }
      .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .badge { display: none; }
    }
  </style>
</head>
<body>
  <main id="root" aria-live="polite"></main>
  <script>
    (() => {
      const root = document.getElementById("root");
      let copyableText = "";
      let fallbackTimer = null;

      const escapeHtml = (value) => String(value ?? "").replace(/[&<>\"']/g, (character) => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
      })[character]);

      const toArray = (value) => Array.isArray(value) ? value : value == null ? [] : [value];
      const asText = (value, fallback = "") => typeof value === "string" ? value : value == null ? fallback : String(value);
      const number = (value, fallback = 0) => Number.isFinite(Number(value)) ? Number(value) : fallback;
      const truncate = (value, max = 2600) => {
        const text = asText(value);
        return text.length > max ? text.slice(0, max - 1) + "…" : text;
      };
      const fileName = (value) => {
        if (!value || typeof value !== "object") return asText(value);
        return asText(value.path || value.file || value.name || value.label || value.id || "");
      };
      const values = (items, limit = 12) => toArray(items).map(fileName).filter(Boolean).slice(0, limit);
      const list = (items, empty = "None") => {
        const all = values(items, Number.MAX_SAFE_INTEGER);
        const entries = all.slice(0, 12);
        const limitNote = all.length > entries.length ? '<div class="notice">Showing ' + entries.length + ' of ' + all.length + ' items.</div>' : "";
        return entries.length
          ? '<ul class="list">' + entries.map((item) => '<li><span>' + escapeHtml(item) + '</span></li>').join("") + '</ul>' + limitNote
          : '<div class="empty">' + escapeHtml(empty) + '</div>';
      };
      const chips = (items, empty = "None") => {
        const all = values(items, Number.MAX_SAFE_INTEGER);
        const entries = all.slice(0, 18);
        const limitNote = all.length > entries.length ? '<div class="notice">Showing ' + entries.length + ' of ' + all.length + ' items.</div>' : "";
        return entries.length
          ? '<div class="chips">' + entries.map((item) => '<span class="chip mono">' + escapeHtml(item) + '</span>').join("") + '</div>' + limitNote
          : '<div class="empty">' + escapeHtml(empty) + '</div>';
      };
      const metric = (value, label) => '<div class="metric"><div class="metric-value">' + escapeHtml(value) + '</div><div class="metric-label">' + escapeHtml(label) + '</div></div>';
      function factRows(entries) {
        return '<dl class="facts">' + entries.filter((entry) => entry[1] !== undefined && entry[1] !== null && entry[1] !== "").map((entry) => {
          const value = entry[2] ? entry[1] : escapeHtml(entry[1]);
          return '<dt>' + escapeHtml(entry[0]) + '</dt><dd>' + value + '</dd>';
        }).join("") + '</dl>';
      }

      function header(title, subtitle, badge, tone = "") {
        return '<header class="head"><div class="tool-icon" aria-hidden="true">›_</div><div class="title-group"><div class="title">' + escapeHtml(title) + '</div>' +
          (subtitle ? '<div class="subtitle">' + escapeHtml(subtitle) + '</div>' : '') +
          '</div>' + (badge ? '<span class="badge ' + escapeHtml(tone) + '">' + escapeHtml(badge) + '</span>' : '') + '</header>';
      }

      function card(title, subtitle, badge, tone, body, extraClass = "") {
        return '<section class="card ' + escapeHtml(extraClass) + '">' + header(title, subtitle, badge, tone) + '<div class="body">' + body + '</div></section>';
      }

      function fold(title, content, open = false) {
        return '<details class="fold"' + (open ? ' open' : '') + '><summary>' + escapeHtml(title) + '</summary><div class="fold-content">' + content + '</div></details>';
      }

      function codeBlock(label, text, copy = false) {
        const bounded = truncate(text, 9000);
        const omitted = asText(text).length > 9000 ? '<div class="notice warn">Display truncated at 9,000 characters. Use the tool result or stored file for the complete data.</div>' : "";
        if (copy) copyableText = bounded;
        return '<div class="code-shell"><div class="code-topline"><span>' + escapeHtml(label) + '</span>' +
          (copy ? '<button type="button" class="copy-card-output" data-copy-card-output aria-label="Copy result">Copy</button>' : '') +
          '</div>' + omitted + '<pre>' + escapeHtml(bounded || "No output") + '</pre></div>';
      }

      function normalizedJson(value) {
        if (typeof value !== "string") return value;
        const text = value.trim();
        if (!text || (text[0] !== "{" && text[0] !== "[")) return value;
        try { return JSON.parse(text); } catch { return value; }
      }

      function extractStructuredContent(value, depth = 0, seen = new Set()) {
        const normalized = normalizedJson(value);
        if (!normalized || typeof normalized !== "object" || depth > 6 || seen.has(normalized)) return null;
        seen.add(normalized);
        if (Array.isArray(normalized)) {
          for (const item of normalized) {
            const match = extractStructuredContent(item, depth + 1, seen);
            if (match) return match;
          }
          return null;
        }
        if (normalized.codexpro_tool || normalized.codexpro_title) return normalized;
        const candidates = [
          normalized.structuredContent,
          normalized.toolOutput,
          normalized.toolResponseMetadata,
          normalized.toolResult,
          normalized.tool_result,
          normalized.mcp_tool_result,
          normalized.call_tool_result,
          normalized.result,
          normalized.output,
          normalized.payload,
          normalized.data,
          normalized.params
        ];
        for (const candidate of candidates) {
          const match = extractStructuredContent(candidate, depth + 1, seen);
          if (match) return match;
        }
        return null;
      }

      function applyHostTheme(globals = window.openai || {}) {
        const theme = asText(globals.theme || window.openai?.theme || "light").toLowerCase();
        document.documentElement.dataset.theme = theme.includes("dark") ? "dark" : "light";
      }

      function renderWorkspace(data) {
        const workspaces = toArray(data.workspaces);
        if (workspaces.length > 1) {
          const primaryId = asText(data.primary_workspace_id || data.selected_workspace_id, "");
          const changed = workspaces.some((workspace) => {
            const git = asText(workspace?.git_status, "");
            return git && !/clean|nothing to commit|no changes/i.test(git);
          });
          const rows = workspaces.slice(0, 12).map((workspace) => {
            const workspaceId = asText(workspace?.workspace_id, "unknown");
            const projectId = asText(workspace?.project_id, workspaceId);
            const rootPath = asText(workspace?.root, "Workspace");
            const selected = workspaceId === primaryId ? ' <span class="chip">primary</span>' : "";
            const state = workspace?.already_open ? "already open" : "opened";
            return '<li><strong>' + escapeHtml(projectId) + '</strong>' + selected +
              '<div class="muted mono">' + escapeHtml(workspaceId) + '</div>' +
              '<div class="muted path">' + escapeHtml(rootPath) + '</div>' +
              '<div class="muted">' + escapeHtml(state) + '</div></li>';
          }).join("");
          const summary = '<div class="summary"><strong>' + workspaces.length + ' workspaces connected.</strong> Reuse these workspace handles for later calls; the first requested project is the selected primary.</div>' +
            factRows([
              ["Access", asText(data.tool_mode, "standard") + " tools · " + asText(data.write_mode, "off") + " writes"],
              ["Shell", asText(data.bash_mode, "off")],
              ["Trees", data.include_tree ? "Included" : "Skipped for compact multi-open"]
            ]);
          return card("Connected workspaces", workspaces.length + " projects", changed ? "Changes" : "Ready", changed ? "warn" : "good", summary + fold("Workspace handles", '<ul class="list">' + rows + '</ul>', true));
        }

        const rootPath = asText(data.root, "Workspace");
        const git = asText(data.git_status, "");
        const changed = git && !/clean|nothing to commit|no changes/i.test(git);
        const summary = '<div class="summary"><strong>Ready to work.</strong> This workspace is connected for the current conversation.</div>' +
          factRows([
            ["Root", '<span class="mono path">' + escapeHtml(rootPath) + '</span>', true],
            ["Instructions", data.agents_loaded ? "AGENTS.md loaded" : "No AGENTS.md found"],
            ["Access", asText(data.tool_mode, "standard") + " tools · " + asText(data.write_mode, "off") + " writes"],
            ["Shell", asText(data.bash_mode, "off")]
          ]);
        const context = chips(data.ai_context_files || data.skills, "No extra workspace context");
        const status = git ? fold("Git status", codeBlock("Git", git), false) : "";
        return card("Connected workspace", rootPath, changed ? "Changes" : "Ready", changed ? "warn" : "good", summary + fold("Available context", context, false) + status);
      }

      function renderWorkspaceAnalysis(data) {
        const coverage = data.coverage && typeof data.coverage === "object" ? data.coverage : {};
        const analyzed = number(coverage.analyzedFiles ?? data.returned?.files ?? toArray(data.files).length);
        const inventory = number(coverage.inventoryFiles, analyzed);
        const symbols = number(coverage.symbolCount ?? data.returned?.symbols ?? toArray(data.symbols).length);
        const relationships = number(coverage.relationshipCount ?? data.returned?.relationships ?? toArray(data.relationships).length);
        const warnings = values(data.warnings, 6);
        const metrics = '<div class="metrics">' + metric(analyzed + (inventory ? "/" + inventory : ""), "files analyzed") + metric(symbols, "symbols") + metric(relationships, "relationships") + '</div>';
        const overview = factRows([
          ["Scope", '<span class="mono path">' + escapeHtml(asText(data.path, ".")) + '</span>', true],
          ["Languages", escapeHtml(values(data.languages).join(", ") || "Not detected"), true],
          ["Projects", escapeHtml(values(data.project_types).join(", ") || "Not detected"), true]
        ]);
        const warningBlock = warnings.length ? '<div class="notice warn">' + escapeHtml(warnings.join(" ")) + '</div>' : "";
        return card("Workspace map", asText(data.root, "Analysis complete"), data.output_limited ? "Partial" : "Ready", data.output_limited ? "warn" : "good", metrics + overview + warningBlock +
          fold("Entrypoints", list(data.entrypoints, "No entrypoints detected"), false) +
          fold("Important files", list(data.important_files || data.files, "No files returned"), false) +
          fold("Areas", list(data.areas, "No areas returned"), false));
      }

      function renderChanges(data) {
        const allFiles = values(data.changed_files, Number.MAX_SAFE_INTEGER);
        const files = allFiles;
        const checkpoint = data.review_checkpoint_hit === true;
        const failed = asText(data.status_error || data.diff_error, "");
        const hasChanges = Boolean(data.changed) || files.length > 0 || number(data.additions) > 0 || number(data.deletions) > 0;
        const metrics = '<div class="metrics">' + metric(allFiles.length, "files") + metric("+" + number(data.additions), "additions") + metric("−" + number(data.deletions), "deletions") + '</div>';
        const result = failed
          ? '<div class="notice bad">' + escapeHtml(failed) + '</div>'
          : checkpoint ? '<div class="notice">No new changes since last review. This does not mean the working tree is clean.</div>' : hasChanges ? list(files, "Changes detected") : '<div class="notice">No changes detected.</div>';
        const diff = asText(data.diff, "");
        return card("Changes", asText(data.path, "Workspace review"), failed ? "Unavailable" : checkpoint ? "Unchanged review" : hasChanges ? "Review" : "Clean", failed ? "bad" : hasChanges ? "warn" : "good", metrics + result +
          (diff ? fold("Raw diff", codeBlock("Diff", diff), false) : "") +
          (data.status ? fold("Git status", codeBlock("Git", asText(data.status)), false) : ""));
      }

      function renderChangeAnalysis(data) {
        const analysis = data.analysis && typeof data.analysis === "object" ? data.analysis : {};
        const changeCard = renderChanges({ ...data, analysis: undefined });
        const risks = toArray(analysis.risk_signals).map((risk) => typeof risk === "object" ? risk.label || risk.message || risk.path : risk).filter(Boolean);
        const tests = toArray(analysis.related_tests).map(fileName).filter(Boolean);
        const supplemental = '<div class="body">' +
          (risks.length ? '<div class="notice warn">' + escapeHtml(values(risks, 5).join(" · ")) + '</div>' : '') +
          fold("Affected areas", list(analysis.affected_areas, "No affected areas identified"), false) +
          fold("Related tests", list(tests, "No related tests identified"), false) +
          fold("Recommended checks", list(analysis.recommended_commands, "No additional checks suggested"), false) +
          '</div>';
        return changeCard.replace('</section>', supplemental + '</section>');
      }

      function renderStatus(data) {
        const status = asText(data.status, "");
        const failed = asText(data.status_error, "");
        const files = values(data.changed_files, 20);
        const changed = Boolean(data.changed) || files.length > 0;
        return card("Git status", asText(data.path, "Workspace"), failed ? "Unavailable" : changed ? "Changes" : "Clean", failed ? "bad" : changed ? "warn" : "good",
          failed ? '<div class="notice bad">' + escapeHtml(failed) + '</div>' : (changed ? list(files, "No changed files") : '<div class="notice">Working tree is clean.</div>') +
          (status ? fold("Full status", codeBlock("Git", status), false) : ""));
      }

      function renderHandoff(data) {
        const target = asText(data.agent_name || data.agent, "agent");
        const details = factRows([
          ["Target", target],
          ["Plan", '<span class="mono path">' + escapeHtml(asText(data.plan_path, "Not recorded")) + '</span>', true],
          ["Status", '<span class="mono path">' + escapeHtml(asText(data.status_path, "Not recorded")) + '</span>', true],
          ["Changes", "+" + number(data.additions) + " −" + number(data.deletions)]
        ]);
        return card("Handoff ready", target, "Written", "good", '<div class="summary">The implementation plan is available to the selected local agent.</div>' + details +
          (data.diff ? fold("Handoff diff", codeBlock("Diff", asText(data.diff)), false) : ""));
      }

      function renderBash(data) {
        const exitCode = data.exit_code ?? data.exitCode;
        const running = data.job_status === "running";
        const success = !running && exitCode !== null && exitCode !== undefined && Number(exitCode) === 0 && !data.signal && !data.timed_out;
        const title = running ? "Command running" : success ? "Command completed" : "Command needs attention";
        const command = asText(data.command, "");
        const output = "$ " + command + "\n\n" + (asText(data.stdout, "") || "(no stdout)") + (data.stderr ? "\n\n[stderr]\n" + asText(data.stderr) : "");
        const factsBlock = factRows([
          ["Directory", '<span class="mono path">' + escapeHtml(asText(data.cwd || data.root, "Workspace")) + '</span>', true],
          ["Exit", asText(exitCode, "unknown") + (data.signal ? " · " + asText(data.signal) : "")],
          ["Job", data.job_id],
          ["State", data.job_status],
          ["Origin", data.job_origin],
          ["Stop reason", data.stop_reason],
          ["Timed out", data.timed_out ? "yes" : undefined],
          ["Duration", number(data.duration_ms ?? data.durationMs) ? number(data.duration_ms ?? data.durationMs) + " ms" : "Not reported"]
        ]);
        return card(title, command || "Command finished", running ? "Running" : success ? "Passed" : "Review", success ? "good" : "warn", factsBlock + (running ? '<div class="notice">Collect this job by ID to retrieve its final result.</div>' : "") + (data.truncated || data.output_truncated ? '<div class="notice warn">Output is truncated; inspect the stored output or collect the job again.</div>' : "") + codeBlock("Terminal", output, true));
      }

      function renderCommit(data) {
        const clean = data.working_tree_clean;
        const state = clean === true ? "Clean" : clean === false ? "Changes remain" : "Status unavailable";
        return card("Commit created", asText(data.commit, "Commit identity unavailable"), state, clean === true ? "good" : "warn",
          factRows([["Commit", data.commit], ["Branch", data.branch], ["Files", data.file_count]]) +
          (data.status_error ? '<div class="notice warn">' + escapeHtml(data.status_error) + '</div>' : '') +
          list(data.files, "File list unavailable") +
          (toArray(data.skipped_blocked_paths).length ? fold("Skipped paths", list(data.skipped_blocked_paths), true) : '') +
          (data.status ? fold("Post-commit status snapshot", codeBlock("Git", data.status), true) : '') +
          '<div class="notice">Status reflects the commit-time snapshot; later writes may change it.</div>');
      }

      function renderJobs(data) {
        const jobs = toArray(data.jobs);
        const running = jobs.some(job => job.status === "running");
        const failed = jobs.some(job => job.status && job.status !== "running" && job.status !== "succeeded");
        const body = jobs.map(job => {
          const output = asText(job.stdout ?? job.stdout_tail) + (job.stderr || job.stderr_tail ? "\n[stderr]\n" + asText(job.stderr ?? job.stderr_tail) : "");
          return fold(asText(job.job_id) + " · " + asText(job.label || job.command) + " · " + asText(job.status),
            factRows([["Exit", job.exit_code], ["Signal", job.signal], ["Stop reason", job.stop_reason], ["Duration (ms)", job.elapsed_ms], ["Deadline in (ms)", job.deadline_in_ms], ["Output view", job.output_mode]]) +
            (job.output_truncated ? '<div class="notice warn">Output truncated. Collect with full_output=false for the ending or full_output=true for the bounded beginning.</div>' : '') +
            (output ? codeBlock("Output", output) : '<div class="notice">No output in this response. Collect by job ID to inspect output.</div>'), true);
        }).join("");
        return card("Background jobs", "Execution outcomes", running ? "Running" : failed ? "Needs attention" : jobs.length ? "Succeeded" : "No jobs", running || failed ? "warn" : "good",
          factRows([["Actual wait (ms)", data.waited_ms], ["Requested wait (ms)", data.requested_wait_ms], ["All finished", data.all_finished], ["All succeeded", data.all_succeeded]]) +
          (data.server_restarting ? '<div class="notice warn">Server restarting. Collect existing job IDs again.</div>' : '') + body +
          (data.stopped_ids ? fold("Stopped by this call", list(data.stopped_ids), false) : '') +
          (data.already_finished_ids ? fold("Already finished", list(data.already_finished_ids), false) : ''));
      }

      function renderGeneric(data) {
        const title = asText(data.codexpro_title, "Tool result");
        const failed = Boolean(data.error || data.error_code || data.is_error || data.succeeded === false);
        const partial = data.truncated || data.output_truncated || data.output_limited || data.has_more;
        return card(title, "CodexPro result", failed ? "Failed" : partial ? "Partial" : "Ready", failed ? "bad" : partial ? "warn" : "good",
          (failed ? '<div class="notice bad">' + escapeHtml(data.error || data.error_code || "Some operations failed") + '</div>' : '') +
          (partial ? '<div class="notice warn">This response is bounded or has more results.</div>' : '') +
          Object.entries(data).filter(([key]) => !key.startsWith("codexpro_")).map(([key, value]) =>
            fold(key.replaceAll("_", " "), codeBlock(key, typeof value === "string" ? value : JSON.stringify(value, null, 2)), ["results", "matches", "changed_paths", "error", "recovery"].includes(key))).join(""));
      }

      function renderUnavailable() {
        copyableText = "";
        root.innerHTML = card("Result unavailable", "CodexPro", "Retry", "warn", '<div class="notice">The tool finished, but its display data did not reach this card. Inspect the workspace or collect the existing job by ID before retrying. A commit or other change may already have succeeded; do not repeat it blindly.</div>');
      }

      function renderPending() {
        copyableText = "";
        root.innerHTML = card("Preparing result", "CodexPro", "Loading", "", '<div class="notice">Loading the tool result…</div>', "pending");
      }

      function render(data) {
        if (!data || typeof data !== "object") return false;
        if (fallbackTimer) {
          clearTimeout(fallbackTimer);
          fallbackTimer = null;
        }
        copyableText = "";
        const tool = asText(data.codexpro_tool, "");
        if (data.error || data.error_code || data.is_error) root.innerHTML = renderGeneric(data);
        else if (tool === "commit_changes") root.innerHTML = renderCommit(data);
        else if (["start_jobs", "jobs", "stop_jobs"].includes(tool)) root.innerHTML = renderJobs(data);
        else if (tool === "open_current_workspace" || tool === "open_workspace") root.innerHTML = renderWorkspace(data);
        else if (tool === "inspect_workspace") root.innerHTML = renderWorkspaceAnalysis(data);
        else if (tool === "show_changes") root.innerHTML = data.analysis ? renderChangeAnalysis(data) : renderChanges(data);
        else if (tool === "handoff_to_agent") root.innerHTML = renderHandoff(data);
        else if (tool === "bash") root.innerHTML = renderBash(data);
        else root.innerHTML = renderGeneric(data);
        return true;
      }

      function renderFromHost(value) {
        const data = extractStructuredContent(value);
        if (data) render(data);
      }

      root.addEventListener("click", async (event) => {
        const target = event.target instanceof Element ? event.target.closest("[data-copy-card-output]") : null;
        if (!target || !copyableText) return;
        const original = target.textContent;
        try {
          if (!navigator.clipboard || !navigator.clipboard.writeText) throw new Error("Clipboard unavailable");
          await navigator.clipboard.writeText(copyableText);
          target.textContent = "Copied";
        } catch {
          target.textContent = "Copy unavailable";
        }
        window.setTimeout(() => { target.textContent = original || "Copy"; }, 1400);
      });

      applyHostTheme();
      renderPending();
      fallbackTimer = window.setTimeout(renderUnavailable, 1200);
      renderFromHost(window.openai?.toolOutput || window.openai?.toolResponseMetadata || window.openai?.toolResult || {});
      window.addEventListener("openai:set_globals", (event) => {
        const globals = event.detail?.globals || window.openai || {};
        applyHostTheme(globals);
        renderFromHost(globals.toolOutput || globals.toolResponseMetadata || globals.toolResult || globals);
      });
      window.addEventListener("message", (event) => {
        const message = event.data || {};
        if (message.type === "ui/notifications/tool-result" || message.method === "ui/notifications/tool-result") {
          renderFromHost(message.params || message.data || message);
        }
      });
    })();
  </script>
</body>
</html>`;
