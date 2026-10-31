package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Service struct {
	Config        *config.Config
	Workspaces    wire.Resolver
	Files         *files.Service
	mu            sync.Mutex
	analyses      map[string]*Analysis
	analysisOrder []string
	versions      map[string]string
}

func New(cfg *config.Config, resolver wire.Resolver, fileService *files.Service) (*Service, error) {
	return &Service{Config: cfg, Workspaces: resolver, Files: fileService, analyses: map[string]*Analysis{}, versions: map[string]string{}}, nil
}
func (s *Service) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	w, e := s.Workspaces.Resolve(ctx, scope, wire.String(args, "workspace_id", ""))
	if e != nil {
		return wire.Result{}, e
	}
	switch name {
	case "search":
		return s.search(ctx, scope, w, args)
	case "ast_grep":
		return s.ast(ctx, scope, w, args)
	case "inspect_workspace":
		return s.inspect(ctx, scope, w, args)
	}
	return wire.Result{}, wire.Errorf("tool_unknown", "Unknown search tool: %s", name)
}
func fail(code, message, tool, recovery string) error {
	data := map[string]any{"retry_unchanged": false}
	if recovery != "" {
		data["recovery"] = map[string]any{"tool": tool, "message": recovery}
	}
	return &wire.Error{Code: code, Message: message, Data: data}
}
func warnings(values ...[]string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, group := range values {
		for _, v := range group {
			v = wire.Redact(v)
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
				if len(out) >= 30 {
					return out
				}
			}
		}
	}
	return out
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type options struct {
	Query, Root, Glob, Kind, ConfigFormat, Scope, BaseRef, DiffTarget, Cursor, Intent, Symbol string
	Regex, Hidden, Group, Untracked, IncludeTests, Structured                                 bool
	Max, Before, After                                                                        int
}

func (s *Service) options(a map[string]any) options {
	_, intent := a["intent"]
	_, symbol := a["symbol"]
	_, tests := a["include_tests"]
	o := options{Query: wire.String(a, "query", ""), Root: wire.String(a, "path", "."), Glob: wire.String(a, "glob", ""), Kind: wire.String(a, "kind", "text"),
		ConfigFormat: wire.String(a, "config_format", "auto"), Scope: wire.String(a, "scope", "workspace"), BaseRef: wire.String(a, "base_ref", "HEAD"), DiffTarget: wire.String(a, "diff_target", "worktree"),
		Cursor: wire.String(a, "cursor", ""), Intent: wire.String(a, "intent", "auto"), Symbol: wire.String(a, "symbol", ""), Regex: wire.Bool(a, "regex", false), Hidden: wire.Bool(a, "include_hidden", false), Group: wire.Bool(a, "group_by_file", true),
		Untracked: wire.Bool(a, "include_untracked", true), IncludeTests: wire.Bool(a, "include_tests", false), Structured: intent || symbol || tests,
		Max: max(1, min(s.Config.Int("maxSearchResults", 200), wire.Int(a, "max_results", s.Config.Int("maxSearchResults", 200)))), Before: max(0, min(20, wire.Int(a, "context_before", 2))), After: max(0, min(20, wire.Int(a, "context_after", 2)))}
	if o.Symbol != "" {
		o.Query = o.Symbol
	}
	return o
}
func signature(w wire.Workspace, o options) string {
	v := struct {
		Version   int    `json:"version"`
		Workspace string `json:"workspace_id"`
		Query     string `json:"query"`
		Regex     bool   `json:"regex"`
		Root      string `json:"root"`
		Glob      string `json:"glob"`
		Hidden    bool   `json:"include_hidden"`
		Kind      string `json:"kind"`
		Format    string `json:"config_format"`
		Scope     string `json:"scope"`
		Base      string `json:"base_ref"`
		Target    string `json:"diff_target"`
		Untracked bool   `json:"include_untracked"`
		Before    int    `json:"context_before"`
		After     int    `json:"context_after"`
		Group     bool   `json:"group_by_file"`
	}{1, w.ID, o.Query, o.Regex, o.Root, o.Glob, o.Hidden, o.Kind, o.ConfigFormat, o.Scope, o.BaseRef, o.DiffTarget, o.Untracked, o.Before, o.After, o.Group}
	return files.SHA(files.JSONString(v))
}
func searchCursor(sig, after string) string {
	p := struct {
		V         int    `json:"v"`
		Signature string `json:"signature"`
		After     string `json:"after"`
		Checksum  string `json:"checksum"`
	}{1, sig, after, files.SHA("codexpro-search-cursor-v1\x00" + sig + "\x00" + after)[:20]}
	return base64.RawURLEncoding.EncodeToString([]byte(files.JSONString(p)))
}
func decodeBase64(v string) ([]byte, error) {
	v = strings.TrimRight(v, "=")
	return base64.RawURLEncoding.DecodeString(v)
}
func decodeSearchCursor(value, sig string) (string, error) {
	if value == "" {
		return "", nil
	}
	if files.JSLength(value) > 4096 {
		return "", fail("search_cursor_invalid", "Search cursor is too large. Restart the search without cursor.", "search", "Restart this search without cursor.")
	}
	b, e := decodeBase64(value)
	var p struct {
		V         int     `json:"v"`
		Signature string  `json:"signature"`
		After     *string `json:"after"`
		Checksum  string  `json:"checksum"`
	}
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	if e != nil || p.V != 1 || p.Signature != sig || p.After == nil || p.Checksum != files.SHA("codexpro-search-cursor-v1\x00" + sig + "\x00" + deref(p.After))[:20] {
		return "", fail("search_cursor_mismatch", "Search cursor does not belong to this exact query and scope. Restart without cursor.", "search", "Restart this search without cursor and reuse only the next_cursor it returns.")
	}
	return *p.After, nil
}
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type hit struct {
	Path                                string
	Line, End                           int
	Text, Source, Address, ConfigFormat string
	Editable                            bool
}

func hitKey(h hit) string {
	return fmt.Sprintf("%s\x00%012d\x00%012d\x00%s\x00%s", h.Path, h.Line, h.End, h.Source, h.Address)
}

type page struct {
	Hits     []hit
	More     bool
	Used     string
	Warnings []string
}
type fileState struct {
	Abs, Text string
	Lines     []string
}
type matcher func(string) (bool, error)

func makeMatcher(query string, regex bool) (matcher, error) {
	if !regex {
		return func(line string) (bool, error) { return strings.Contains(line, query), nil }, nil
	}
	flags := regexp2.ECMAScript
	for strings.HasPrefix(query, "(?") {
		end := strings.IndexByte(query, ')')
		if end < 0 {
			break
		}
		raw := query[2:end]
		valid := raw != ""
		for _, c := range raw {
			if !strings.ContainsRune("ims", c) {
				valid = false
			}
		}
		if !valid {
			break
		}
		for _, c := range raw {
			switch c {
			case 'i':
				flags |= regexp2.IgnoreCase
			case 'm':
				flags |= regexp2.Multiline
			case 's':
				flags |= regexp2.Singleline
			}
		}
		query = query[end+1:]
	}
	re, e := regexp2.Compile(query, flags)
	if e != nil {
		return nil, fmt.Errorf("Invalid regular expression: %s", e.Error())
	}
	re.MatchTimeout = 100 * time.Millisecond
	return func(line string) (bool, error) {
		ok, e := re.MatchString(line)
		if e != nil {
			return false, fail("search_regex_timeout", "Regular expression matching exceeded its bounded execution budget.", "search", "Narrow the regular expression or use a fixed-string search.")
		}
		return ok, nil
	}, nil
}
func (s *Service) loadContexts(w wire.Workspace, hits []hit) (map[string]fileState, []string) {
	states := map[string]fileState{}
	seen := map[string]bool{}
	warn := []string{}
	for _, h := range hits {
		if !h.Editable || seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		abs, _, e := s.Files.ResolvePath(w, h.Path, false)
		var b []byte
		if e == nil {
			b, e = files.ReadSource(abs, s.Files.ScanLimit())
		}
		if e != nil {
			warn = append(warn, fmt.Sprintf("%s: current file could not establish edit provenance (%s).", h.Path, e))
			continue
		}
		text := files.DecodeText(b)
		states[h.Path] = fileState{abs, text, files.Lines(text)}
	}
	return states, warn
}
func (s *Service) contexts(scope wire.Scope, o options, raw []hit, states map[string]fileState) ([]hit, []map[string]any, []map[string]any, bool, []string, error) {
	warn := []string{}
	selected := []hit{}
	cost := 0
	limited := false
	budget := max(16*1024, s.Config.Int("maxOutputBytes", 120000)*65/100)
	matchesQuery, e := makeMatcher(o.Query, o.Regex)
	if e != nil {
		return nil, nil, nil, false, nil, e
	}
	type span struct {
		start, end int
		hits       []hit
	}
	ranges := map[string][]span{}
	rangeFor := func(h hit, st fileState) (int, int, bool) {
		requested := max(h.Line, h.End)
		capped := min(requested, h.Line+79)
		return max(1, h.Line-o.Before), min(len(st.Lines), capped+o.After), requested > capped
	}
	for _, h := range raw {
		if !h.Editable {
			n := len(h.Text) + 256
			if len(selected) > 0 && cost+n > budget {
				limited = true
				break
			}
			selected = append(selected, h)
			cost += min(n, 4096)
			continue
		}
		st, ok := states[h.Path]
		valid := ok && h.Line >= 1 && h.Line <= len(st.Lines)
		if valid {
			if h.Source == "config" {
				nodes, _, err := queryConfig(st.Text, h.ConfigFormat, o.Query)
				valid = false
				if err == nil {
					for _, n := range nodes {
						if n.Address == h.Address && n.Line == h.Line {
							valid = true
							break
						}
					}
				}
			} else {
				valid, e = matchesQuery(st.Lines[h.Line-1])
				if e != nil {
					return nil, nil, nil, false, nil, e
				}
			}
		}
		if !valid {
			h.Editable = false
			warn = append(warn, fmt.Sprintf("%s:%d: file changed during search; this match has no edit tag.", h.Path, h.Line))
			selected = append(selected, h)
			cost += min(len(h.Text)+256, 4096)
			continue
		}
		start, end, capped := rangeFor(h, st)
		if capped {
			warn = append(warn, fmt.Sprintf("%s:%d: matched configuration span was capped to 80 lines for display.", h.Path, h.Line))
		}
		n := len(files.Numbered(st.Lines[start-1:end], start)) + 256
		if len(selected) > 0 && cost+min(n, 128*1024) > budget {
			limited = true
			break
		}
		selected = append(selected, h)
		cost += min(n, 128*1024)
	}
	order := map[string]int{}
	paths := []string{}
	for i, h := range selected {
		order[h.Path] = i
		if !h.Editable {
			continue
		}
		if _, ok := ranges[h.Path]; !ok {
			paths = append(paths, h.Path)
		}
		st := states[h.Path]
		start, end, _ := rangeFor(h, st)
		ranges[h.Path] = append(ranges[h.Path], span{start, end, []hit{h}})
	}
	sort.SliceStable(paths, func(i, j int) bool { return order[paths[i]] < order[paths[j]] })
	contexts := []map[string]any{}
	tags := map[string]string{}
	editable := map[string]bool{}
	for _, path := range paths {
		st := states[path]
		rawRanges := ranges[path]
		sort.SliceStable(rawRanges, func(i, j int) bool {
			return rawRanges[i].start < rawRanges[j].start || rawRanges[i].start == rawRanges[j].start && rawRanges[i].end < rawRanges[j].end
		})
		merged := []span{}
		for _, r := range rawRanges {
			if o.Group && len(merged) > 0 && r.start <= merged[len(merged)-1].end+1 {
				p := &merged[len(merged)-1]
				p.end = max(p.end, r.end)
				p.hits = append(p.hits, r.hits...)
			} else {
				merged = append(merged, r)
			}
		}
		for _, r := range merged {
			text := files.Numbered(st.Lines[r.start-1:r.end], r.start)
			full := len(text) <= 128*1024
			tag := ""
			if full {
				tag = s.Files.Snapshots.Record(scope, st.Abs, st.Text, r.start, r.end)
				tags[path] = tag
				for _, h := range r.hits {
					editable[hitKey(h)] = true
				}
			} else {
				warn = append(warn, fmt.Sprintf("%s:%d-%d: context exceeded 131072 bytes, so edit provenance was not granted.", path, r.start, r.end))
				text = files.Short(text, 4000)
			}
			lines := []int{}
			seen := map[int]bool{}
			for _, h := range r.hits {
				if !seen[h.Line] {
					seen[h.Line] = true
					lines = append(lines, h.Line)
				}
			}
			sort.Ints(lines)
			m := map[string]any{"path": path, "start_line": r.start, "end_line": r.end, "total_lines": len(st.Lines), "match_lines": lines, "text": text, "source": r.hits[0].Source, "editable": tag != ""}
			if tag != "" {
				m["edit_tag"] = tag
			}
			if !full {
				m["truncated"] = true
			}
			contexts = append(contexts, m)
		}
	}
	for _, h := range selected {
		if h.Editable {
			continue
		}
		m := map[string]any{"path": h.Path, "start_line": h.Line, "end_line": h.End, "match_lines": []int{h.Line}, "text": fmt.Sprintf("%d | %s", h.Line, files.Short(h.Text, 2000)), "source": h.Source, "editable": false}
		if files.JSLength(h.Text) > 2000 {
			m["truncated"] = true
		}
		contexts = append(contexts, m)
	}
	sort.SliceStable(contexts, func(i, j int) bool {
		a, b := contexts[i], contexts[j]
		ap, bp := a["path"].(string), b["path"].(string)
		if order[ap] != order[bp] {
			return order[ap] < order[bp]
		}
		return a["start_line"].(int) < b["start_line"].(int)
	})
	matches := []map[string]any{}
	for _, h := range selected {
		canEdit := h.Editable && editable[hitKey(h)]
		m := map[string]any{"path": h.Path, "line": h.Line, "text": files.Short(h.Text, 400), "source": h.Source, "editable": canEdit}
		if h.End != h.Line {
			m["end_line"] = h.End
		}
		if h.Address != "" {
			m["address"] = h.Address
		}
		if canEdit && tags[h.Path] != "" {
			m["edit_tag"] = tags[h.Path]
		}
		matches = append(matches, m)
	}
	return selected, matches, contexts, limited, warnings(warn), nil
}
func renderSearch(o options, matches, contexts []map[string]any, more bool, cursor string, warn []string) string {
	lines := []string{"# Search Files", "", "Kind: " + o.Kind, "Scope: " + o.Scope, fmt.Sprintf("Returned matches: %d", len(matches)), fmt.Sprintf("Context: %d before / %d after", o.Before, o.After), ""}
	if len(matches) == 0 {
		lines = append(lines, "No matches.")
	}
	for _, b := range contexts {
		tag := " · read-only context"
		if v, ok := b["edit_tag"].(string); ok {
			tag = " · edit_tag " + v
		}
		lines = append(lines, fmt.Sprintf("## %s:%d-%d%s", b["path"], b["start_line"], b["end_line"], tag), "", "```text", b["text"].(string), "```", "")
	}
	if len(warn) > 0 {
		lines = append(lines, "## Warnings", "")
		for _, w := range warn {
			lines = append(lines, "- "+w)
		}
		lines = append(lines, "")
	}
	if more && cursor != "" {
		lines = append(lines, "More matches are available. Reuse the same arguments with:", "", "cursor: "+cursor)
	}
	return files.TrimJS(strings.Join(lines, "\n"))
}
func (s *Service) search(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	o := s.options(a)
	if o.Query == "" {
		return wire.Result{}, fmt.Errorf("query is required.")
	}
	if o.Kind == "config" && o.Regex {
		return wire.Result{}, wire.Errorf("", "regex is not used by configuration queries. Use wildcards in the query path instead.")
	}
	if o.Kind == "config" && o.Symbol != "" {
		return wire.Result{}, fmt.Errorf("symbol cannot be combined with kind=config.")
	}
	sig := signature(w, o)
	after, e := decodeSearchCursor(o.Cursor, sig)
	if e != nil {
		return wire.Result{}, e
	}
	primary, e := s.primary(ctx, w, o, after)
	if e != nil {
		return wire.Result{}, e
	}
	raw := primary.Hits[:min(len(primary.Hits), o.Max)]
	states, loadWarnings := s.loadContexts(w, raw)
	selected, matches, contexts, limited, contextWarnings, e := s.contexts(scope, o, raw, states)
	if e != nil {
		return wire.Result{}, e
	}
	more := primary.More || limited || len(selected) < len(raw)
	cursor := ""
	if more && len(selected) > 0 {
		cursor = searchCursor(sig, hitKey(selected[len(selected)-1]))
	}
	extra := []string{}
	if limited {
		extra = append(extra, "The page stopped early to keep complete context within the output budget.")
	}
	warn := warnings(primary.Warnings, loadWarnings, contextWarnings, extra)
	data := files.Base(w)
	data["matches"] = matches
	data["contexts"] = contexts
	data["truncated"] = more
	data["has_more"] = more
	data["next_cursor"] = nullString(cursor)
	data["query_fingerprint"] = sig
	data["used"] = primary.Used
	data["scope"] = o.Scope
	data["kind"] = o.Kind
	data["warnings"] = warn
	if o.Structured && o.Kind == "text" && o.Scope == "workspace" && o.Cursor == "" {
		data["analysis"] = s.structured(ctx, w, o)
	}
	return wire.Success("search", "Search Files", data, renderSearch(o, matches, contexts, more, cursor, warn)), nil
}
func relativePath(w wire.Workspace, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.Root, p)
	}
	p = filepath.Clean(p)
	if !files.Within(p, w.Root) {
		return ""
	}
	return files.Rel(p, w.Root)
}
