package activity

import "fmt"

// The page copy, markup, style and credential-link behavior are ported from
// src/activityDashboard/page.ts at 44bb76b (source SHA c934eec9f62408e32c3bc62b8a7030e2a8d08478153fc5065e19b55cde418ebe).
// Parsed JSON keeps its complete value; map-key order is not a UI contract.
const batchPageTemplate = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/favicon.ico">
  <title>%s · CodexPro Batch</title>
  <style>
    :root { color-scheme: light; font-family: "Geist", "Aptos", ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; --paper: #f4f6f9; --panel: #fff; --ink: #172033; --soft: #5b667a; --rule: #dce2eb; --accent: #2563eb; --mono: "Fira Code", "Geist Mono", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; }
    * { box-sizing: border-box; }
    body { margin: 0; background: var(--paper); color: var(--ink); }
    main { width: min(1100px, calc(100%% - 28px)); margin: 0 auto; padding: 24px 0 48px; }
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
      <h1>%s</h1>
      <p class="project">%s <code>%s</code>%s</p>
      <div class="badges"><span class="badge">%s</span><span class="badge">%s operation%s</span><span class="badge">continue on error: %s</span><span class="badge">%s</span></div>
      <p class="note">This is the current saved definition. If the batch was edited after the action ran, it may differ from the historical invocation. Exact commands and arguments can contain credentials or other sensitive values; treat this authenticated page as sensitive.</p>
    </header>
    <section class="panel"><h2>Operations</h2><div class="operation-list">%s</div></section>
    <details class="panel raw"><summary><strong>Raw JSON</strong></summary><pre>%s</pre></details>
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
</html>`

func batchMode(definition map[string]any) string {
	if mode, ok := definition["mode"].(string); ok {
		return mode
	}
	return "unknown"
}

func RenderBatchPage(view map[string]any) string {
	definition := object(view["definition"])
	operations := array(definition["operations"])
	workspace := ""
	if id := stringValue(view["workspaceId"]); id != "" {
		workspace = ` · workspace <code>` + escape(id) + `</code>`
	}
	pluralSuffix := "s"
	if len(operations) == 1 {
		pluralSuffix = ""
	}
	continueOnError := "no"
	if yes, _ := definition["continue_on_error"].(bool); yes {
		continueOnError = "yes"
	}
	storageLabel := "custom JSON"
	if boolean(view["autoStored"]) {
		storageLabel = "auto-stored"
	}
	return fmt.Sprintf(batchPageTemplate,
		escape(view["path"]),
		escape(view["path"]),
		escape(view["projectLabel"]),
		escape(view["projectId"]),
		workspace,
		escape(batchMode(definition)),
		escape(len(operations)),
		pluralSuffix,
		continueOnError,
		storageLabel,
		renderBatchOperationCards(view["definition"]),
		escape(prettyJSON(view["definition"])))
}
