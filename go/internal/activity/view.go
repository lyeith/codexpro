package activity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var longRunID = regexp.MustCompile("^run_[A-Za-z0-9_-]{13,}$")

func renderRunID(value string) string {
	if !longRunID.MatchString(value) {
		return escape(value)
	}
	short := value[:12] + "…" + value[len(value)-4:]
	return `<span class="run-reference"><button type="button" class="copy-run-id" data-copy-run-id="` + escape(value) + `" title="` + escape(value) + `" aria-label="Copy full run ID ` + escape(value) + `"><code>` + escape(short) + `</code><span aria-hidden="true">⧉</span></button><span class="copy-run-status" role="status"></span></span>`
}
func renderAgent(value any) string {
	if agent := stringValue(value); agent != "" {
		return `<code class="tool-badge agent-label" title="Caller-supplied label; not a verified identity">` + escape(agent) + `</code>`
	}
	return ""
}
func renderFields(title string, fields []map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	var body strings.Builder
	for _, field := range fields {
		class := ""
		if boolean(field["mono"]) {
			class = "mono-value"
		}
		class += " " + stringValue(field["tone"])
		value := escape(field["value"])
		if field["key"] == "run_id" {
			value = renderRunID(stringValue(field["value"]))
		}
		fmt.Fprintf(&body, `<div><dt>%s</dt><dd class="%s">%s</dd></div>`, escape(field["label"]), escape(class), value)
	}
	return `<section class="action-section"><h4>` + escape(title) + `</h4><dl class="field-grid">` + body.String() + `</dl></section>`
}

type DashboardSection struct {
	Key  string `json:"key"`
	HTML string `json:"html"`
}
type DashboardData struct {
	GeneratedAt string             `json:"generatedAt"`
	Sections    []DashboardSection `json:"sections"`
}

func prettyJSON(v any) string {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if encoder.Encode(v) != nil {
		return "null"
	}
	return strings.TrimSuffix(b.String(), "\n")
}
func renderBatchOperationCards(value any) string {
	definition := object(value)
	operations := array(definition["operations"])
	var cards strings.Builder
	if len(operations) == 0 {
		cards.WriteString(`<p class="empty">This JSON has no operation list.</p>`)
	}
	for i, raw := range operations {
		op := object(raw)
		id := stringValue(op["id"])
		if id == "" {
			id = fmt.Sprintf("op_%d", i+1)
		}
		tool := stringValue(op["tool"])
		if tool == "" {
			tool = "unknown"
		}
		fmt.Fprintf(&cards, `<details class="batch-operation" open>
      <summary><span><b>%d</b><code>%s</code></span><strong>%s</strong></summary>
      <pre>%s</pre>
    </details>`, i, escape(id), escape(tool), escape(prettyJSON(object(op["args"]))))
	}
	return cards.String()
}

func RenderBatchFragment(view map[string]any) string {
	definition := object(view["definition"])
	operations := array(definition["operations"])
	stored := ""
	if boolean(view["autoStored"]) {
		stored = `<span class="badge">auto-stored</span>`
	}
	return `<div class="badges"><span class="badge">` + escape(batchMode(definition)) + `</span><span class="badge">` + escape(plural(float64(len(operations)), "operation")) + `</span>` + stored + `</div>
    <div class="operation-list">` + renderBatchOperationCards(view["definition"]) + `</div>
    <details class="raw"><summary><strong>Raw JSON</strong></summary><pre>` + escape(prettyJSON(view["definition"])) + `</pre></details>`
}
