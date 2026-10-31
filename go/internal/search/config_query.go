package search

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lyeith/codexpro/go/internal/files"
)

type configNode struct {
	segments   []any
	start, end int
	raw        string
}
type configMatch struct {
	Address   string
	Line, End int
	Text      string
}

var simpleConfigKey = regexp.MustCompile("^[A-Za-z_$][A-Za-z0-9_$-]*$")
var decimalIndex = regexp.MustCompile("^(?:0|[1-9][0-9]*)$")
var jsonPrimitive = regexp.MustCompile("^(?:true|false|null|-?(?:0|[1-9][0-9]*)(?:\\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)$")

func configAddress(segments []any) string {
	out := "$"
	for _, part := range segments {
		switch v := part.(type) {
		case int:
			out += fmt.Sprintf("[%d]", v)
		case string:
			if simpleConfigKey.MatchString(v) {
				out += "." + v
			} else {
				out += "[" + files.JSONString(v) + "]"
			}
		}
	}
	return out
}
func configIndex(value string) any {
	if decimalIndex.MatchString(value) {
		if n, e := strconv.Atoi(value); e == nil {
			return n
		}
	}
	return value
}
func bracketSegment(raw string) (any, error) {
	v := files.TrimJS(raw)
	if v == "*" {
		return "*", nil
	}
	if decimalIndex.MatchString(v) {
		return configIndex(v), nil
	}
	if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		var text string
		if e := json.Unmarshal([]byte(v), &text); e != nil {
			return nil, fmt.Errorf("Invalid quoted configuration query segment: [%s]", raw)
		}
		return text, nil
	}
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return strings.ReplaceAll(strings.ReplaceAll(v[1:len(v)-1], "\\'", "'"), "\\\\", "\\"), nil
	}
	if v == "" {
		return nil, fmt.Errorf("Empty [] segment in configuration query.")
	}
	return v, nil
}
func parseQuery(query string) ([]any, error) {
	v := files.TrimJS(query)
	if v == "" || v == "$" || v == "/" {
		return []any{}, nil
	}
	if strings.HasPrefix(v, "/") {
		out := []any{}
		for _, p := range strings.Split(v, "/")[1:] {
			p = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
			out = append(out, configIndex(p))
		}
		return out, nil
	}
	out := []any{}
	i := 0
	if strings.HasPrefix(v, "$") {
		i++
	}
	if i < len(v) && v[i] == '.' {
		i++
	}
	var bare strings.Builder
	flush := func() {
		if bare.Len() > 0 {
			out = append(out, bare.String())
			bare.Reset()
		}
	}
	for i < len(v) {
		switch v[i] {
		case '\\':
			i++
			if i >= len(v) {
				return nil, fmt.Errorf("Configuration query ends with an incomplete escape.")
			}
			bare.WriteByte(v[i])
			i++
		case '.':
			flush()
			i++
		case '[':
			flush()
			start := i + 1
			i++
			quote := byte(0)
			escaped := false
			for i < len(v) {
				c := v[i]
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if quote != 0 {
					if c == quote {
						quote = 0
					}
				} else if c == '\'' || c == '"' {
					quote = c
				} else if c == ']' {
					break
				}
				i++
			}
			if i >= len(v) || v[i] != ']' {
				return nil, fmt.Errorf("Configuration query has an unclosed [ segment.")
			}
			part, e := bracketSegment(v[start:i])
			if e != nil {
				return nil, e
			}
			out = append(out, part)
			i++
		default:
			bare.WriteByte(v[i])
			i++
		}
	}
	flush()
	return out, nil
}
func sameQuery(query, actual []any) bool {
	if len(query) != len(actual) {
		return false
	}
	for i, p := range query {
		if s, ok := p.(string); ok && s == "*" {
			continue
		}
		if p != actual[i] {
			return false
		}
	}
	return true
}
func whitespace(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' }
func rawSummary(value string) string {
	words := strings.FieldsFunc(value, whitespace)
	return files.Short(strings.Join(words, " "), 360)
}
func located(nodes []configNode, query []any) []configMatch {
	out := []configMatch{}
	for _, n := range nodes {
		if sameQuery(query, n.segments) {
			address := configAddress(n.segments)
			out = append(out, configMatch{address, n.start, n.end, address + " = " + rawSummary(n.raw)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return files.Compare(out[i].Address, out[j].Address) < 0
	})
	return out
}
func appendSegment(parts []any, v any) []any { out := append([]any{}, parts...); return append(out, v) }

type jsonLocations struct {
	text       string
	i          int
	lineStarts []int
	nodes      []configNode
}

func (p *jsonLocations) line(offset int) int {
	return sort.Search(len(p.lineStarts), func(i int) bool { return p.lineStarts[i] > offset })
}
func (p *jsonLocations) fail(message string) error {
	return fmt.Errorf("%s at line %d.", message, max(1, p.line(p.i)))
}
func (p *jsonLocations) skip() error {
	for p.i < len(p.text) {
		r, n := utf8.DecodeRuneInString(p.text[p.i:])
		if whitespace(r) {
			p.i += n
			continue
		}
		if strings.HasPrefix(p.text[p.i:], "//") {
			p.i += 2
			for p.i < len(p.text) && p.text[p.i] != '\n' {
				p.i++
			}
			continue
		}
		if strings.HasPrefix(p.text[p.i:], "/*") {
			end := strings.Index(p.text[p.i+2:], "*/")
			if end < 0 {
				return p.fail("Unclosed JSON block comment")
			}
			p.i += end + 4
			continue
		}
		break
	}
	return nil
}
func (p *jsonLocations) peek() byte {
	if p.i >= len(p.text) {
		return 0
	}
	return p.text[p.i]
}
func (p *jsonLocations) str() (string, int, int, error) {
	start := p.i
	if p.peek() != '"' {
		return "", 0, 0, p.fail("Expected a JSON string")
	}
	p.i++
	escaped := false
	for p.i < len(p.text) {
		c := p.text[p.i]
		if escaped {
			escaped = false
			p.i++
			continue
		}
		if c == '\\' {
			escaped = true
			p.i++
			continue
		}
		if c == '"' {
			p.i++
			var value string
			if e := json.Unmarshal([]byte(p.text[start:p.i]), &value); e != nil {
				return "", 0, 0, p.fail("Invalid JSON string escape")
			}
			return value, start, p.i, nil
		}
		if c == '\n' || c == '\r' {
			return "", 0, 0, p.fail("Unclosed JSON string")
		}
		p.i++
	}
	return "", 0, 0, p.fail("Unclosed JSON string")
}
func (p *jsonLocations) add(parts []any, start, end int) {
	p.nodes = append(p.nodes, configNode{append([]any{}, parts...), max(1, p.line(start)), max(1, p.line(max(start, end-1))), p.text[start:end]})
}
func (p *jsonLocations) value(parts []any) error {
	if e := p.skip(); e != nil {
		return e
	}
	switch p.peek() {
	case '{':
		start := p.i
		p.i++
		if e := p.skip(); e != nil {
			return e
		}
		if p.peek() == '}' {
			p.i++
			p.add(parts, start, p.i)
			return nil
		}
		for p.i < len(p.text) {
			if e := p.skip(); e != nil {
				return e
			}
			key, _, _, e := p.str()
			if e != nil {
				return e
			}
			if e = p.skip(); e != nil {
				return e
			}
			if p.peek() != ':' {
				return p.fail("Expected : after JSON object key")
			}
			p.i++
			if e = p.value(appendSegment(parts, key)); e != nil {
				return e
			}
			if e = p.skip(); e != nil {
				return e
			}
			if p.peek() == '}' {
				p.i++
				p.add(parts, start, p.i)
				return nil
			}
			if p.peek() != ',' {
				return p.fail("Expected , or } in JSON object")
			}
			p.i++
			if e = p.skip(); e != nil {
				return e
			}
			if p.peek() == '}' {
				p.i++
				p.add(parts, start, p.i)
				return nil
			}
		}
		return p.fail("Unclosed JSON object")
	case '[':
		start := p.i
		p.i++
		if e := p.skip(); e != nil {
			return e
		}
		if p.peek() == ']' {
			p.i++
			p.add(parts, start, p.i)
			return nil
		}
		item := 0
		for p.i < len(p.text) {
			if e := p.value(appendSegment(parts, item)); e != nil {
				return e
			}
			item++
			if e := p.skip(); e != nil {
				return e
			}
			if p.peek() == ']' {
				p.i++
				p.add(parts, start, p.i)
				return nil
			}
			if p.peek() != ',' {
				return p.fail("Expected , or ] in JSON array")
			}
			p.i++
			if e := p.skip(); e != nil {
				return e
			}
			if p.peek() == ']' {
				p.i++
				p.add(parts, start, p.i)
				return nil
			}
		}
		return p.fail("Unclosed JSON array")
	case '"':
		_, start, end, e := p.str()
		if e != nil {
			return e
		}
		p.add(parts, start, end)
		return nil
	default:
		start := p.i
		for p.i < len(p.text) {
			r, n := utf8.DecodeRuneInString(p.text[p.i:])
			if whitespace(r) || strings.ContainsRune(",}]", r) {
				break
			}
			p.i += n
		}
		raw := p.text[start:p.i]
		if !jsonPrimitive.MatchString(raw) {
			return p.fail("Invalid JSON value " + files.JSONString(raw))
		}
		p.add(parts, start, p.i)
		return nil
	}
}
func parseJSONLocations(text string) ([]configNode, error) {
	p := jsonLocations{text: text, lineStarts: []int{0}, nodes: []configNode{}}
	for i, c := range []byte(text) {
		if c == '\n' {
			p.lineStarts = append(p.lineStarts, i+1)
		}
	}
	if e := p.skip(); e != nil {
		return nil, e
	}
	if p.i >= len(text) {
		return nil, p.fail("Empty JSON document")
	}
	if e := p.value([]any{}); e != nil {
		return nil, e
	}
	if e := p.skip(); e != nil {
		return nil, e
	}
	if p.i != len(text) {
		return nil, p.fail("Unexpected content after JSON document")
	}
	return p.nodes, nil
}
func outsideDelimiter(value string, delimiter byte) int {
	quote := byte(0)
	escaped := false
	square, curly := 0, 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && quote == '"' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		switch c {
		case '[':
			square++
		case ']':
			square = max(0, square-1)
		case '{':
			curly++
		case '}':
			curly = max(0, curly-1)
		default:
			if c == delimiter && square == 0 && curly == 0 {
				return i
			}
		}
	}
	return -1
}
func stripComment(value string) string {
	if i := outsideDelimiter(value, '#'); i >= 0 {
		return value[:i]
	}
	return value
}
func unquoteKey(value string) string {
	v := files.TrimJS(value)
	if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
		var out string
		if e := json.Unmarshal([]byte(v), &out); e == nil {
			return out
		}
		return v[1 : len(v)-1]
	}
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

var yamlBlock = regexp.MustCompile("^[|>][+-]?[0-9]*$")

func parseYAML(text string) ([]configNode, []string) {
	lines := files.Lines(text)
	nodes := []configNode{{[]any{}, 1, max(1, len(lines)), text}}
	warn := []string{}
	type frame struct {
		indent int
		parts  []any
	}
	stack := []frame{{-1, []any{}}}
	counters := map[string]int{}
	add := func(parts []any, line int, raw string) {
		nodes = append(nodes, configNode{append([]any{}, parts...), line, line, raw})
	}
	for i, raw := range lines {
		prefix := strings.TrimLeftFunc(raw, func(r rune) bool { return whitespace(r) && r != '\t' })
		if strings.HasPrefix(prefix, "\t") {
			warn = append(warn, fmt.Sprintf("Skipped tab-indented YAML at line %d.", i+1))
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		content := strings.TrimRightFunc(stripComment(raw[indent:]), whitespace)
		trimmed := strings.TrimLeftFunc(content, whitespace)
		if files.TrimJS(content) == "" || files.TrimJS(content) == "---" || files.TrimJS(content) == "..." {
			continue
		}
		for len(stack) > 1 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1].parts
		if trimmed == "-" || strings.HasPrefix(trimmed, "- ") {
			key := configAddress(parent) + "\x00" + strconv.Itoa(indent)
			index := counters[key]
			counters[key]++
			item := appendSegment(parent, index)
			rest := strings.TrimLeftFunc(trimmed[1:], whitespace)
			if rest == "" {
				add(item, i+1, files.TrimJS(raw))
				stack = append(stack, frame{indent, item})
				continue
			}
			colon := outsideDelimiter(rest, ':')
			if colon > 0 {
				key := unquoteKey(rest[:colon])
				value := files.TrimJS(rest[colon+1:])
				parts := appendSegment(item, key)
				r := value
				if r == "" {
					r = files.TrimJS(raw)
				}
				add(parts, i+1, r)
				stack = append(stack, frame{indent, item})
				if value == "" || yamlBlock.MatchString(value) {
					stack = append(stack, frame{indent + 2, parts})
				}
			} else {
				add(item, i+1, rest)
			}
			continue
		}
		colon := outsideDelimiter(trimmed, ':')
		if colon <= 0 {
			warn = append(warn, fmt.Sprintf("Skipped unsupported YAML syntax at line %d.", i+1))
			continue
		}
		key := unquoteKey(trimmed[:colon])
		value := files.TrimJS(trimmed[colon+1:])
		parts := appendSegment(parent, key)
		r := value
		if r == "" {
			r = files.TrimJS(raw)
		}
		add(parts, i+1, r)
		if value == "" || yamlBlock.MatchString(value) {
			stack = append(stack, frame{indent, parts})
		}
	}
	return nodes, warnings(warn)
}
func dottedKey(value string) ([]any, error) {
	parts := []any{}
	var current strings.Builder
	quote := byte(0)
	escaped := false
	flush := func() error {
		key := unquoteKey(current.String())
		if key == "" {
			return fmt.Errorf("Invalid empty dotted key in %s.", value)
		}
		parts = append(parts, key)
		current.Reset()
		return nil
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			current.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && quote == '"' {
			current.WriteByte(c)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			current.WriteByte(c)
			continue
		}
		if c == '.' {
			if e := flush(); e != nil {
				return nil, e
			}
			continue
		}
		current.WriteByte(c)
	}
	if e := flush(); e != nil {
		return nil, e
	}
	return parts, nil
}
func parseTOML(text string) ([]configNode, []string) {
	lines := files.Lines(text)
	nodes := []configNode{{[]any{}, 1, max(1, len(lines)), text}}
	warn := []string{}
	counters := map[string]int{}
	section := []any{}
	for i, raw := range lines {
		value := files.TrimJS(stripComment(raw))
		if value == "" {
			continue
		}
		arrayTable := strings.HasPrefix(value, "[[") && strings.HasSuffix(value, "]]") && len(value) > 4
		table := strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") && len(value) > 2
		if arrayTable || table {
			bare := value[1 : len(value)-1]
			if arrayTable {
				bare = value[2 : len(value)-2]
			}
			parts, e := dottedKey(bare)
			if e != nil {
				warn = append(warn, fmt.Sprintf("%s (line %d)", e, i+1))
				continue
			}
			section = parts
			if arrayTable {
				key := configAddress(parts)
				index := counters[key]
				counters[key]++
				section = appendSegment(parts, index)
			}
			nodes = append(nodes, configNode{append([]any{}, section...), i + 1, i + 1, value})
			continue
		}
		equal := outsideDelimiter(value, '=')
		if equal <= 0 {
			warn = append(warn, fmt.Sprintf("Skipped unsupported TOML syntax at line %d.", i+1))
			continue
		}
		key, e := dottedKey(value[:equal])
		if e != nil {
			warn = append(warn, fmt.Sprintf("%s (line %d)", e, i+1))
			continue
		}
		rawValue := files.TrimJS(value[equal+1:])
		if rawValue == "" {
			rawValue = value
		}
		parts := append(append([]any{}, section...), key...)
		nodes = append(nodes, configNode{parts, i + 1, i + 1, rawValue})
	}
	return nodes, warnings(warn)
}
func queryConfig(text, format, query string) ([]configMatch, []string, error) {
	parts, e := parseQuery(query)
	if e != nil {
		return nil, nil, e
	}
	var nodes []configNode
	warn := []string{}
	switch format {
	case "json":
		nodes, e = parseJSONLocations(text)
	case "yaml":
		nodes, warn = parseYAML(text)
	case "toml":
		nodes, warn = parseTOML(text)
	default:
		return nil, nil, fmt.Errorf("Unsupported configuration format: %s", format)
	}
	if e != nil {
		return nil, nil, e
	}
	return located(nodes, parts), warn, nil
}
