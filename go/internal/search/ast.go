package search

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type astOptions struct {
	Pattern, Kind, Language, Selector, Strictness, Root, Cursor string
	Globs                                                       []string
	Hidden, Group                                               bool
	Max, Before, After, Timeout                                 int
}
type astHit struct {
	Path, Language, Preview, Digest                                            string
	StartLine, StartColumn, EndLine, EndColumn, StartByte, EndByte, MatchBytes int
	Captures                                                                   []map[string]any
}
type astState struct {
	Abs, Text string
	Buffer    []byte
	Lines     []string
}
type astCandidate struct {
	Hit            astHit
	State          *astState
	Verified, Full bool
	Start, End     int
	Text, Warning  string
}

func (s *Service) astOptions(a map[string]any) astOptions {
	return astOptions{wire.String(a, "pattern", ""), wire.String(a, "kind", ""), wire.String(a, "language", ""), wire.String(a, "selector", ""), wire.String(a, "strictness", ""), wire.String(a, "path", "."), wire.String(a, "cursor", ""), wire.Strings(a, "globs"), wire.Bool(a, "include_hidden", false), wire.Bool(a, "group_by_file", true), max(1, min(s.Config.Int("maxSearchResults", 200), wire.Int(a, "max_results", s.Config.Int("maxSearchResults", 200)))), max(0, min(20, wire.Int(a, "context_before", 2))), max(0, min(20, wire.Int(a, "context_after", 2))), max(1000, min(60000, wire.Int(a, "timeout_ms", 15000)))}
}
func validateAst(o astOptions) (string, error) {
	pattern, kind := files.TrimJS(o.Pattern), files.TrimJS(o.Kind)
	if (pattern == "") == (kind == "") {
		return "", fail("ast_grep_query_invalid", "Provide exactly one of pattern or kind for ast_grep.", "", "")
	}
	if o.Selector != "" && pattern == "" {
		return "", fail("ast_grep_query_invalid", "selector requires pattern mode.", "", "")
	}
	if o.Strictness != "" && pattern == "" {
		return "", fail("ast_grep_query_invalid", "strictness requires pattern mode.", "", "")
	}
	if len(o.Globs) > 64 {
		return "", fmt.Errorf("ast_grep accepts at most 64 globs.")
	}
	for _, g := range o.Globs {
		if g == "" || files.JSLength(g) > 512 || strings.ContainsAny(g, "\x00\r\n") {
			return "", fmt.Errorf("Invalid ast_grep glob. Each glob must be 1-512 characters without NUL or newlines.")
		}
	}
	if pattern != "" {
		return "pattern", nil
	}
	return "kind", nil
}
func (s *Service) astExecutable() (string, error) {
	raw := s.Config.String("astGrepPath", os.Getenv("CODEXPRO_AST_GREP_PATH"))
	if raw == "" {
		raw = os.Getenv("AST_GREP_PATH")
	}
	if files.TrimJS(raw) != "" {
		value := files.TrimJS(raw)
		if strings.ContainsAny(value, "\x00\r\n") {
			return "", fmt.Errorf("CODEXPRO_AST_GREP_PATH must be one executable path or command name.")
		}
		if strings.ContainsAny(value, "/\\") || strings.HasPrefix(value, "~") {
			if value == "~" || strings.HasPrefix(value, "~/") {
				home, _ := os.UserHomeDir()
				if value == "~" {
					value = home
				} else {
					value = filepath.Join(home, value[2:])
				}
			}
			return filepath.Abs(value)
		}
		return value, nil
	}
	roots := []string{}
	if p, e := os.Executable(); e == nil {
		roots = append(roots, filepath.Dir(p))
	}
	if p, e := os.Getwd(); e == nil {
		roots = append(roots, p)
	}
	for _, root := range roots {
		for p := root; ; p = filepath.Dir(p) {
			candidate := filepath.Join(p, "node_modules", "@ast-grep", "cli", "ast-grep")
			if st, e := os.Stat(candidate); e == nil && st.Mode().IsRegular() {
				return candidate, nil
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return "ast-grep", nil
}

var astVersionPattern = regexp.MustCompile(`\b(\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?)\b`)

func (s *Service) astVersion(ctx context.Context, executable string) (string, error) {
	s.mu.Lock()
	cached := s.versions[executable]
	s.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	r := files.RunCommand(ctx, "", executable, []string{"--version"}, "", 1024*1024, 5*time.Second)
	if r.Err != nil {
		if _, e := exec.LookPath(executable); e != nil && !r.TimedOut {
			return "", fail("ast_grep_unavailable", "ast-grep is unavailable. Reinstall CodexPro dependencies, install @ast-grep/cli, or set CODEXPRO_AST_GREP_PATH to the ast-grep executable.", "ast_grep", "Install @ast-grep/cli or configure CODEXPRO_AST_GREP_PATH, then retry the structural search.")
		}
		return "", fail("ast_grep_unavailable", "Unable to start ast-grep: "+wire.Redact(r.Err.Error()), "", "")
	}
	if r.ExitCode != 0 {
		detail := r.Stderr
		if detail == "" {
			detail = r.Stdout
		}
		if detail == "" {
			detail = fmt.Sprintf("exit %d", r.ExitCode)
		}
		return "", fail("ast_grep_unavailable", "ast-grep version check failed: "+files.TrimJS(wire.Redact(detail)), "", "")
	}
	version := files.TrimJS(r.Stdout)
	if m := astVersionPattern.FindStringSubmatch(version); len(m) > 1 {
		version = m[1]
	}
	if version == "" {
		version = "unknown"
	}
	s.mu.Lock()
	s.versions[executable] = version
	s.mu.Unlock()
	return version, nil
}
func astSignature(w wire.Workspace, o astOptions, mode, version string) string {
	if o.Globs == nil {
		o.Globs = []string{}
	}
	value := struct {
		CursorVersion   int      `json:"cursor_version"`
		WorkspaceID     string   `json:"workspace_id"`
		Provider        string   `json:"provider"`
		ProviderVersion string   `json:"provider_version"`
		Mode            string   `json:"mode"`
		Pattern         string   `json:"pattern"`
		Kind            string   `json:"kind"`
		Language        string   `json:"language"`
		Selector        string   `json:"selector"`
		Strictness      string   `json:"strictness"`
		Root            string   `json:"root"`
		Globs           []string `json:"globs"`
		Hidden          bool     `json:"include_hidden"`
		Before          int      `json:"context_before"`
		After           int      `json:"context_after"`
		Group           bool     `json:"group_by_file"`
	}{1, w.ID, "ast-grep-cli", version, mode, o.Pattern, o.Kind, o.Language, o.Selector, o.Strictness, o.Root, o.Globs, o.Hidden, o.Before, o.After, o.Group}
	return files.SHA(files.JSONString(value))
}
func astCursor(signature string, offset int) string {
	value := struct {
		V         int    `json:"v"`
		Signature string `json:"signature"`
		Offset    int    `json:"offset"`
		Checksum  string `json:"checksum"`
	}{1, signature, offset, files.SHA("codexpro-ast-grep-cursor-v1\x00" + signature + "\x00" + strconv.Itoa(offset))[:20]}
	return base64.RawURLEncoding.EncodeToString([]byte(files.JSONString(value)))
}
func decodeAstCursor(value, signature string) (int, error) {
	if value == "" {
		return 0, nil
	}
	if files.JSLength(value) > 4096 {
		return 0, fail("ast_grep_cursor_invalid", "ast_grep cursor is too large. Restart without cursor.", "ast_grep", "Restart this structural search without cursor.")
	}
	var p struct {
		V         int    `json:"v"`
		Signature string `json:"signature"`
		Offset    *int   `json:"offset"`
		Checksum  string `json:"checksum"`
	}
	b, e := decodeBase64(value)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	if e == nil && p.V == 1 && p.Signature == signature && p.Offset != nil && *p.Offset >= 0 && int64(*p.Offset) <= 9007199254740991 && p.Checksum == files.SHA("codexpro-ast-grep-cursor-v1\x00" + signature + "\x00" + strconv.Itoa(*p.Offset))[:20] {
		return *p.Offset, nil
	}
	return 0, fail("ast_grep_cursor_mismatch", "ast_grep cursor does not belong to this exact structural query. Restart without cursor.", "ast_grep", "Restart this structural search without cursor and reuse only the next_cursor it returns.")
}

type nativePoint struct {
	Line   *int `json:"line"`
	Column *int `json:"column"`
}
type nativeRange struct {
	Start nativePoint `json:"start"`
	End   nativePoint `json:"end"`
	Bytes struct {
		Start *int `json:"start"`
		End   *int `json:"end"`
	} `json:"byteOffset"`
}

func parseNativeRange(raw json.RawMessage) (nativeRange, bool) {
	var r nativeRange
	e := json.Unmarshal(raw, &r)
	return r, e == nil && r.Start.Line != nil && r.Start.Column != nil && r.End.Line != nil && r.End.Column != nil
}
func publicRange(raw json.RawMessage) map[string]any {
	r, ok := parseNativeRange(raw)
	if !ok {
		return nil
	}
	m := map[string]any{"start_line": *r.Start.Line + 1, "start_column": *r.Start.Column, "end_line": *r.End.Line + 1, "end_column": *r.End.Column}
	if r.Bytes.Start != nil {
		m["start_byte"] = *r.Bytes.Start
	}
	if r.Bytes.End != nil {
		m["end_byte"] = *r.Bytes.End
	}
	return m
}

type objectEntry struct {
	Key   string
	Value json.RawMessage
}

func objectEntries(raw json.RawMessage) []objectEntry {
	out := []objectEntry{}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return out
	}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return out
		}
		name, ok := key.(string)
		if !ok {
			return out
		}
		var value json.RawMessage
		if e = d.Decode(&value); e != nil {
			return out
		}
		out = append(out, objectEntry{name, value})
	}
	return out
}
func astCaptures(raw json.RawMessage) []map[string]any {
	var meta map[string]json.RawMessage
	_ = json.Unmarshal(raw, &meta)
	out := []map[string]any{}
	add := func(name, kind string, raw json.RawMessage) {
		if len(out) >= 64 {
			return
		}
		var node struct {
			Text  string          `json:"text"`
			Range json.RawMessage `json:"range"`
		}
		_ = json.Unmarshal(raw, &node)
		text := node.Text
		if text == "" {
			_ = json.Unmarshal(raw, &text)
		}
		if text == "" {
			return
		}
		m := map[string]any{"name": name, "capture_type": kind, "text": astTruncate(text, 800)}
		if r := publicRange(node.Range); r != nil {
			m["range"] = r
		}
		if files.JSLength(text) > 800 {
			m["truncated"] = true
		}
		out = append(out, m)
	}
	for _, entry := range objectEntries(meta["single"]) {
		add(entry.Key, "single", entry.Value)
	}
	for _, entry := range objectEntries(meta["multi"]) {
		var values []json.RawMessage
		if json.Unmarshal(entry.Value, &values) != nil {
			continue
		}
		for _, v := range values {
			add(entry.Key, "multi", v)
		}
	}
	for _, entry := range objectEntries(meta["transformed"]) {
		add(entry.Key, "transformed", entry.Value)
	}
	return out
}
func (s *Service) parseAstHit(w wire.Workspace, raw []byte) (astHit, bool, error) {
	var value struct {
		File     string          `json:"file"`
		Text     string          `json:"text"`
		Range    json.RawMessage `json:"range"`
		Language string          `json:"language"`
		Meta     json.RawMessage `json:"metaVariables"`
	}
	if !json.Valid(raw) {
		var v any
		e := json.Unmarshal(raw, &v)
		return astHit{}, false, fail("ast_grep_invalid_output", "ast-grep returned malformed JSON: "+e.Error(), "", "")
	}
	if json.Unmarshal(raw, &value) != nil {
		return astHit{}, false, nil
	}
	r, ok := parseNativeRange(value.Range)
	if !ok || value.File == "" || r.Bytes.Start == nil || r.Bytes.End == nil || *r.Bytes.Start < 0 || *r.Bytes.End < *r.Bytes.Start {
		return astHit{}, false, nil
	}
	rel := relativePath(w, value.File)
	if rel == "" || s.Files.IsBlocked(rel) {
		return astHit{}, false, nil
	}
	_, rel, e := s.Files.ResolvePath(w, rel, false)
	if e != nil {
		return astHit{}, false, nil
	}
	lang := files.TrimJS(value.Language)
	if lang == "" {
		lang = "unknown"
	}
	return astHit{rel, lang, astTruncate(value.Text, 800), files.SHA(value.Text), *r.Start.Line + 1, *r.Start.Column, *r.End.Line + 1, *r.End.Column, *r.Bytes.Start, *r.Bytes.End, len(value.Text), astCaptures(value.Meta)}, true, nil
}
func (s *Service) runAst(ctx context.Context, w wire.Workspace, o astOptions, mode, executable string, offset int) ([]astHit, bool, []string, error) {
	_, rel, e := s.Files.ResolvePath(w, o.Root, false)
	if e != nil {
		return nil, false, nil, e
	}
	args := []string{"run", "--json=stream", "--color", "never", "--threads", "1"}
	if mode == "pattern" {
		args = append(args, "--pattern", o.Pattern)
	} else {
		args = append(args, "--kind", o.Kind)
	}
	if v := files.TrimJS(o.Language); v != "" {
		args = append(args, "--lang", v)
	}
	if v := files.TrimJS(o.Selector); v != "" {
		args = append(args, "--selector", v)
	}
	if o.Strictness != "" {
		args = append(args, "--strictness", o.Strictness)
	}
	if o.Hidden {
		args = append(args, "--no-ignore", "hidden")
	}
	for _, g := range o.Globs {
		args = append(args, "--globs", g)
	}
	for _, g := range s.Config.BlockedGlobs {
		if !strings.HasPrefix(g, "!") {
			g = "!" + g
		}
		args = append(args, "--globs", g)
	}
	args = append(args, rel)
	hits := []astHit{}
	warn := []string{}
	eligible := 0
	limit := max(8*1024*1024, min(64*1024*1024, s.Config.Int("maxOutputBytes", 120000)*64))
	r := streamJSON(ctx, w.Root, executable, args, time.Duration(o.Timeout)*time.Millisecond, 8*1024*1024, limit, func(line []byte) (bool, error) {
		hit, ok, e := s.parseAstHit(w, line)
		if e != nil {
			return false, e
		}
		if !ok {
			return false, nil
		}
		eligible++
		if eligible <= offset {
			return false, nil
		}
		hits = append(hits, hit)
		return len(hits) >= o.Max+1, nil
	})
	if r.cancelled {
		return nil, false, nil, fail("ast_grep_cancelled", "ast_grep search was cancelled.", "", "")
	}
	if r.timedOut {
		return nil, false, nil, fail("ast_grep_timeout", fmt.Sprintf("ast_grep timed out after %d ms. Narrow the path or pattern.", o.Timeout), "", "")
	}
	if r.eventLimited {
		return nil, false, nil, fail("ast_grep_invalid_output", "ast-grep returned an unterminated JSON event larger than 8388608 bytes.", "", "")
	}
	if r.limited {
		warn = append(warn, fmt.Sprintf("ast-grep output exceeded the %d-byte discovery limit.", limit))
	}
	if r.err != nil && !r.stopped && !r.limited {
		if e, ok := r.err.(*wire.Error); ok {
			return nil, false, nil, e
		}
		if _, e := exec.LookPath(executable); e != nil {
			return nil, false, nil, fail("ast_grep_unavailable", "ast-grep is unavailable. Reinstall CodexPro dependencies, install @ast-grep/cli, or set CODEXPRO_AST_GREP_PATH.", "", "")
		}
		detail := files.TrimJS(r.stderr)
		if detail == "" {
			detail = fmt.Sprintf("exit %d", r.exit)
		}
		return nil, false, nil, fail("ast_grep_failed", "ast-grep failed: "+wire.Redact(detail), "", "")
	}
	if r.exit != 0 && !r.stopped && !r.limited {
		detail := files.TrimJS(r.stderr)
		if detail == "" {
			detail = fmt.Sprintf("exit %d", r.exit)
		}
		return nil, false, nil, fail("ast_grep_failed", "ast-grep failed: "+wire.Redact(detail), "", "")
	}
	return hits, len(hits) > o.Max || r.stopped || r.limited, warnings(warn), nil
}
func (s *Service) astStates(w wire.Workspace, hits []astHit) (map[string]*astState, []string) {
	states := map[string]*astState{}
	seen := map[string]bool{}
	warn := []string{}
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		abs, _, e := s.Files.ResolvePath(w, h.Path, false)
		var b []byte
		if e == nil {
			b, e = files.ReadSource(abs, s.Files.ScanLimit())
		}
		if e != nil {
			warn = append(warn, fmt.Sprintf("%s: current file could not be loaded for structural context (%s).", h.Path, e))
			continue
		}
		text := files.DecodeText(b)
		states[h.Path] = &astState{abs, text, b, files.Lines(text)}
	}
	return states, warnings(warn)
}
func astCandidateFor(h astHit, state *astState, o astOptions) astCandidate {
	c := astCandidate{Hit: h, State: state, Start: h.StartLine, End: h.EndLine, Text: fmt.Sprintf("%d | %s", h.StartLine, h.Preview)}
	valid := state != nil && h.StartByte >= 0 && h.EndByte <= len(state.Buffer) && h.EndByte >= h.StartByte
	if valid {
		b := state.Buffer[h.StartByte:h.EndByte]
		valid = len(b) == h.MatchBytes && files.SHABytes(b) == h.Digest
	}
	if !valid {
		c.Warning = fmt.Sprintf("%s:%d: file changed during ast_grep search; this match has no edit provenance.", h.Path, h.StartLine)
		return c
	}
	c.Verified = true
	fullSpan := max(1, h.EndLine-h.StartLine+1) <= 200
	matchEnd := h.EndLine
	if !fullSpan {
		matchEnd = h.StartLine + 199
	}
	c.Start = max(1, h.StartLine-o.Before)
	c.End = min(len(state.Lines), matchEnd+o.After)
	start, end := max(0, min(len(state.Lines), c.Start-1)), max(0, min(len(state.Lines), c.End))
	if end < start {
		end = start
	}
	c.Text = files.Numbered(state.Lines[start:end], c.Start)
	complete := len(c.Text) <= 128*1024
	c.Full = fullSpan && complete
	if !complete {
		c.Text = astTruncate(c.Text, 4000)
	}
	if !fullSpan {
		c.Warning = fmt.Sprintf("%s:%d: structural match spans more than 200 lines; context is read-only and truncated.", h.Path, h.StartLine)
	} else if !complete {
		c.Warning = fmt.Sprintf("%s:%d-%d: context exceeded 131072 bytes; edit provenance was not granted.", h.Path, c.Start, c.End)
	}
	return c
}
func astKey(h astHit) string {
	return h.Path + "\x00" + strconv.Itoa(h.StartByte) + "\x00" + strconv.Itoa(h.EndByte)
}
func (s *Service) astContexts(scope wire.Scope, o astOptions, hits []astHit, states map[string]*astState) ([]astHit, []map[string]any, []map[string]any, bool, []string) {
	selected := []astCandidate{}
	warn := []string{}
	cost := 0
	limited := false
	budget := max(8*1024, s.Config.Int("maxOutputBytes", 120000)*65/100)
	for _, h := range hits {
		c := astCandidateFor(h, states[h.Path], o)
		n := min(128*1024, len(c.Text)+384)
		if len(selected) > 0 && cost+n > budget {
			limited = true
			break
		}
		selected = append(selected, c)
		cost += n
		if c.Warning != "" {
			warn = append(warn, c.Warning)
		}
	}
	type span struct {
		start, end int
		candidates []astCandidate
	}
	byFile := map[string][]span{}
	paths := []string{}
	firstOrder, matchIndex := map[string]int{}, map[string]int{}
	for i, c := range selected {
		firstOrder[c.Hit.Path] = i
		matchIndex[astKey(c.Hit)] = i
		if c.Verified && c.State != nil {
			if _, ok := byFile[c.Hit.Path]; !ok {
				paths = append(paths, c.Hit.Path)
			}
			byFile[c.Hit.Path] = append(byFile[c.Hit.Path], span{c.Start, c.End, []astCandidate{c}})
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return firstOrder[paths[i]] < firstOrder[paths[j]] })
	contexts := []map[string]any{}
	editable := map[string]bool{}
	tags := map[string]string{}
	for _, path := range paths {
		state := states[path]
		raw := byFile[path]
		sort.SliceStable(raw, func(i, j int) bool {
			return raw[i].start < raw[j].start || raw[i].start == raw[j].start && raw[i].end < raw[j].end
		})
		ranges := []span{}
		for _, r := range raw {
			if o.Group && len(ranges) > 0 && r.start <= ranges[len(ranges)-1].end+1 {
				p := &ranges[len(ranges)-1]
				p.end = max(p.end, r.end)
				p.candidates = append(p.candidates, r.candidates...)
			} else {
				ranges = append(ranges, r)
			}
		}
		for _, r := range ranges {
			start, end := max(0, min(len(state.Lines), r.start-1)), max(0, min(len(state.Lines), r.end))
			if end < start {
				end = start
			}
			text := files.Numbered(state.Lines[start:end], r.start)
			full := len(text) <= 128*1024
			for _, c := range r.candidates {
				full = full && c.Full
			}
			tag := ""
			if full {
				tag = s.Files.Snapshots.Record(scope, state.Abs, state.Text, r.start, r.end)
				tags[path] = tag
				for _, c := range r.candidates {
					editable[astKey(c.Hit)] = true
				}
			}
			indices, lines := []int{}, []int{}
			seen := map[int]bool{}
			for _, c := range r.candidates {
				indices = append(indices, matchIndex[astKey(c.Hit)])
				if !seen[c.Hit.StartLine] {
					seen[c.Hit.StartLine] = true
					lines = append(lines, c.Hit.StartLine)
				}
			}
			sort.Ints(lines)
			if !full && len(text) > 128*1024 {
				text = astTruncate(text, 4000)
			}
			m := map[string]any{"path": path, "start_line": r.start, "end_line": r.end, "total_lines": len(state.Lines), "match_indices": indices, "match_lines": lines, "text": text, "editable": tag != ""}
			if tag != "" {
				m["edit_tag"] = tag
			}
			if !full {
				m["truncated"] = true
			}
			contexts = append(contexts, m)
		}
	}
	for _, c := range selected {
		if c.Verified && c.State != nil {
			continue
		}
		contexts = append(contexts, map[string]any{"path": c.Hit.Path, "start_line": c.Hit.StartLine, "end_line": c.Hit.EndLine, "match_indices": []int{matchIndex[astKey(c.Hit)]}, "match_lines": []int{c.Hit.StartLine}, "text": c.Text, "editable": false, "truncated": true})
	}
	sort.SliceStable(contexts, func(i, j int) bool {
		a, b := contexts[i], contexts[j]
		ao, bo := firstOrder[a["path"].(string)], firstOrder[b["path"].(string)]
		if ao != bo {
			return ao < bo
		}
		return a["start_line"].(int) < b["start_line"].(int)
	})
	matches := []map[string]any{}
	selectedHits := []astHit{}
	for _, c := range selected {
		h := c.Hit
		text := h.Preview
		if c.State != nil && c.Verified {
			text = files.DecodeText(c.State.Buffer[h.StartByte:h.EndByte])
		}
		canEdit := editable[astKey(h)]
		m := map[string]any{"path": h.Path, "language": h.Language, "text": astTruncate(text, 800), "start_line": h.StartLine, "start_column": h.StartColumn, "end_line": h.EndLine, "end_column": h.EndColumn, "start_byte": h.StartByte, "end_byte": h.EndByte, "captures": h.Captures, "editable": canEdit}
		if canEdit && tags[h.Path] != "" {
			m["edit_tag"] = tags[h.Path]
		}
		matches = append(matches, m)
		selectedHits = append(selectedHits, h)
	}
	return selectedHits, matches, contexts, limited, warnings(warn)
}
func renderAst(o astOptions, mode, version string, matches, contexts []map[string]any, more bool, cursor string, warn []string) string {
	lang := files.TrimJS(o.Language)
	if lang == "" {
		lang = "inferred per file"
	}
	lines := []string{"# AST Grep", "", "Provider: ast-grep " + version, "Mode: " + mode, "Language: " + lang, fmt.Sprintf("Returned matches: %d", len(matches)), fmt.Sprintf("Context: %d before / %d after", o.Before, o.After), ""}
	if len(matches) == 0 {
		lines = append(lines, "No structural matches.", "")
	} else {
		lines = append(lines, "## Matches", "")
	}
	for i, m := range matches {
		tag := " · read-only"
		if value, ok := m["edit_tag"].(string); ok {
			tag = " · edit_tag " + value
		}
		lines = append(lines, fmt.Sprintf("%d. %s:%d:%d-%d:%d · %s%s", i+1, m["path"], m["start_line"], m["start_column"], m["end_line"], m["end_column"], m["language"], tag))
		captures := []string{}
		for _, capture := range m["captures"].([]map[string]any) {
			text := capture["text"].(string)
			trim := files.TrimJS(text)
			if trim == "," || trim == ";" {
				continue
			}
			captures = append(captures, "$"+capture["name"].(string)+"="+strings.Join(strings.Fields(text), " "))
			if len(captures) >= 8 {
				break
			}
		}
		if len(captures) > 0 {
			lines = append(lines, "   Captures: "+strings.Join(captures, ", "))
		}
	}
	if len(matches) > 0 {
		lines = append(lines, "")
	}
	for _, b := range contexts {
		tag := " · read-only context"
		if value, ok := b["edit_tag"].(string); ok {
			tag = " · edit_tag " + value
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
		lines = append(lines, "More structural matches are available. Reuse the same arguments with:", "", "cursor: "+cursor)
	}
	return files.TrimJS(strings.Join(lines, "\n"))
}
func (s *Service) ast(ctx context.Context, scope wire.Scope, w wire.Workspace, a map[string]any) (wire.Result, error) {
	o := s.astOptions(a)
	if o.Globs == nil {
		o.Globs = []string{}
	}
	mode, e := validateAst(o)
	if e != nil {
		return wire.Result{}, e
	}
	if _, _, e = s.Files.ResolvePath(w, o.Root, false); e != nil {
		return wire.Result{}, e
	}
	executable, e := s.astExecutable()
	if e != nil {
		return wire.Result{}, e
	}
	version, e := s.astVersion(ctx, executable)
	if e != nil {
		return wire.Result{}, e
	}
	signature := astSignature(w, o, mode, version)
	offset, e := decodeAstCursor(o.Cursor, signature)
	if e != nil {
		return wire.Result{}, e
	}
	hits, processMore, processWarnings, e := s.runAst(ctx, w, o, mode, executable, offset)
	if e != nil {
		return wire.Result{}, e
	}
	hits = hits[:min(o.Max, len(hits))]
	states, loadWarnings := s.astStates(w, hits)
	selected, matches, contexts, limited, contextWarnings := s.astContexts(scope, o, hits, states)
	more := processMore || limited || len(selected) < len(hits)
	cursor := ""
	if more && len(selected) > 0 {
		cursor = astCursor(signature, offset+len(selected))
	}
	extra := []string{}
	if limited {
		extra = append(extra, "The page stopped early to keep complete structural context within the output budget.")
	}
	warn := warnings(processWarnings, loadWarnings, contextWarnings, extra)
	data := files.Base(w)
	data["provider"] = "ast-grep-cli"
	data["provider_version"] = version
	data["mode"] = mode
	data["matches"] = matches
	data["contexts"] = contexts
	data["truncated"] = more
	data["has_more"] = more
	data["next_cursor"] = nullString(cursor)
	data["query_fingerprint"] = signature
	data["warnings"] = warn
	return wire.Success("ast_grep", "AST Grep", data, renderAst(o, mode, version, matches, contexts, more, cursor, warn)), nil
}
func astTruncate(text string, limit int) string {
	value, _ := files.ShortInside(text, limit)
	return value
}
