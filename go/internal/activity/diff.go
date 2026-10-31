package activity

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type diffCell struct {
	Line       int
	HasLine    bool
	Text, Tone string
}
type diffRow struct {
	Note          string
	IsNote        bool
	Before, After diffCell
}
type diffHunk struct {
	Header string
	Rows   []diffRow
}
type diffFile struct {
	BeforePath, AfterPath string
	Metadata              []string
	Hunks                 []diffHunk
}

var diffHeader = regexp.MustCompile("^diff --git a/(.+) b/(.+)$")
var hunkHeader = regexp.MustCompile("^@@ -([0-9]+)(?:,[0-9]+)? \\+([0-9]+)(?:,[0-9]+)? @@(?:.*)$")

func parseDiffPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.SplitN(value, "\t", 2)[0]
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && len(value) > 1 {
		value = value[1 : len(value)-1]
	}
	if value != "/dev/null" && (strings.HasPrefix(value, "a/") || strings.HasPrefix(value, "b/")) {
		value = value[2:]
	}
	return value
}
func parseUnifiedDiff(value string) []diffFile {
	files := []diffFile{}
	fileIndex, hunkIndex := -1, -1
	beforeLine, afterLine := 0, 0
	removed, added := []diffCell{}, []diffCell{}
	flush := func() {
		if fileIndex < 0 || hunkIndex < 0 || len(removed) == 0 && len(added) == 0 {
			return
		}
		hunk := &files[fileIndex].Hunks[hunkIndex]
		for i := 0; i < max(len(removed), len(added)); i++ {
			before, after := diffCell{Tone: "empty"}, diffCell{Tone: "empty"}
			if i < len(removed) {
				before = removed[i]
			}
			if i < len(added) {
				after = added[i]
			}
			hunk.Rows = append(hunk.Rows, diffRow{Before: before, After: after})
		}
		removed, added = []diffCell{}, []diffCell{}
	}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			before, after := "Before", "After"
			if m := diffHeader.FindStringSubmatch(line); m != nil {
				before, after = m[1], m[2]
			}
			files = append(files, diffFile{BeforePath: before, AfterPath: after, Metadata: []string{}, Hunks: []diffHunk{}})
			fileIndex = len(files) - 1
			hunkIndex = -1
			continue
		}
		if fileIndex < 0 {
			continue
		}
		if header := hunkHeader.FindStringSubmatch(line); header != nil {
			flush()
			beforeLine, _ = strconv.Atoi(header[1])
			afterLine, _ = strconv.Atoi(header[2])
			files[fileIndex].Hunks = append(files[fileIndex].Hunks, diffHunk{Header: line, Rows: []diffRow{}})
			hunkIndex = len(files[fileIndex].Hunks) - 1
			continue
		}
		if hunkIndex < 0 {
			if strings.HasPrefix(line, "--- ") {
				files[fileIndex].BeforePath = parseDiffPath(line[4:])
			} else if strings.HasPrefix(line, "+++ ") {
				files[fileIndex].AfterPath = parseDiffPath(line[4:])
			} else if line != "" {
				files[fileIndex].Metadata = append(files[fileIndex].Metadata, line)
			}
			continue
		}
		if strings.HasPrefix(line, " ") {
			flush()
			files[fileIndex].Hunks[hunkIndex].Rows = append(files[fileIndex].Hunks[hunkIndex].Rows, diffRow{Before: diffCell{Line: beforeLine, HasLine: true, Text: line[1:], Tone: "context"}, After: diffCell{Line: afterLine, HasLine: true, Text: line[1:], Tone: "context"}})
			beforeLine++
			afterLine++
		} else if strings.HasPrefix(line, "-") {
			removed = append(removed, diffCell{Line: beforeLine, HasLine: true, Text: line[1:], Tone: "removed"})
			beforeLine++
		} else if strings.HasPrefix(line, "+") {
			added = append(added, diffCell{Line: afterLine, HasLine: true, Text: line[1:], Tone: "added"})
			afterLine++
		} else {
			flush()
			if line != "" {
				files[fileIndex].Hunks[hunkIndex].Rows = append(files[fileIndex].Hunks[hunkIndex].Rows, diffRow{Note: line, IsNote: true})
			}
		}
	}
	flush()
	return files
}
func renderDiffCell(cell diffCell, side string) string {
	number := ""
	if cell.HasLine {
		number = strconv.Itoa(cell.Line)
	}
	return fmt.Sprintf(`<span class="diff-line-number %s %s">%s</span><code class="diff-line-code %s %s">%s</code>`, side, cell.Tone, escape(number), side, cell.Tone, escape(cell.Text))
}
func RenderDiff(view map[string]any) string {
	diff := stringValue(view["diff"])
	files := parseUnifiedDiff(diff)
	note := ""
	if boolean(view["truncated"]) {
		note = `<p class="safety-note">diff output truncated</p>`
	}
	if len(files) == 0 {
		return note + `<pre class="diff-fallback">` + escape(diff) + `</pre>`
	}
	var out strings.Builder
	for _, file := range files {
		visible := file.AfterPath
		if visible == "/dev/null" {
			visible = file.BeforePath
		}
		var body strings.Builder
		for _, line := range file.Metadata {
			body.WriteString(`<div class="diff-metadata">` + escape(line) + `</div>`)
		}
		for _, hunk := range file.Hunks {
			body.WriteString(`<div class="diff-hunk-header">` + escape(hunk.Header) + `</div>`)
			for _, row := range hunk.Rows {
				if row.IsNote {
					body.WriteString(`<div class="diff-note">` + escape(row.Note) + `</div>`)
				} else {
					body.WriteString(`<div class="diff-row">` + renderDiffCell(row.Before, "before") + renderDiffCell(row.After, "after") + `</div>`)
				}
			}
		}
		if body.Len() == 0 {
			body.WriteString(`<div class="diff-note">No textual hunk is available for this mode, rename, or binary change.</div>`)
		}
		fmt.Fprintf(&out, `<section class="diff-file"><header class="diff-file-heading"><code>%s</code><span>shared scroll · matched lines</span></header><div class="split-diff-scroll" tabindex="0" aria-label="Side-by-side diff for %s"><div class="split-diff-grid"><div class="diff-side-heading before"><span>Before</span><code>%s</code></div><div class="diff-side-heading after"><span>After</span><code>%s</code></div>%s</div></div></section>`, escape(visible), escape(visible), escape(file.BeforePath), escape(file.AfterPath), body.String())
	}
	return note + `<div class="split-diff">` + out.String() + `</div>`
}
