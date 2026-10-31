package files

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// DecodeText follows the UTF-8 replacement decoder used by Buffer.toString.
// Invalid subsequences consume their maximal valid prefix, not an entire run.
func DecodeText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out.WriteByte(c)
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case c >= 0xc2 && c <= 0xdf:
			n = 2
		case c >= 0xe0 && c <= 0xef:
			n = 3
			if c == 0xe0 {
				lo = 0xa0
			}
			if c == 0xed {
				hi = 0x9f
			}
		case c >= 0xf0 && c <= 0xf4:
			n = 4
			if c == 0xf0 {
				lo = 0x90
			}
			if c == 0xf4 {
				hi = 0x8f
			}
		default:
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		j := 1
		for j < n && i+j < len(b) {
			l, h := byte(0x80), byte(0xbf)
			if j == 1 {
				l, h = lo, hi
			}
			if b[i+j] < l || b[i+j] > h {
				break
			}
			j++
		}
		if j == n {
			out.Write(b[i : i+n])
			i += n
		} else {
			out.WriteRune(utf8.RuneError)
			i += j
		}
	}
	return out.String()
}

func SHA(text string) string      { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func SHABytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func Tag(text string) string      { return strings.ToUpper(SHA(text)[:4]) }
func Lines(text string) []string {
	return strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
}
func Numbered(lines []string, start int) string {
	width := len(fmt.Sprint(start + len(lines) - 1))
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = fmt.Sprintf("%*d | %s", width, start+i, line)
	}
	return strings.Join(out, "\n")
}
func JSLength(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}
func JSSlice(s string, start, end int) string {
	u := utf16.Encode([]rune(s))
	if start < 0 {
		start += len(u)
	}
	if end < 0 {
		end += len(u)
	}
	start = max(0, min(start, len(u)))
	end = max(start, min(end, len(u)))
	return string(utf16.Decode(u[start:end]))
}
func TrimJS(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
}
func Short(s string, n int) string {
	if JSLength(s) <= n {
		return s
	}
	return JSSlice(s, 0, n) + "…"
}
func ShortInside(s string, n int) (string, bool) {
	if JSLength(s) <= n {
		return s, false
	}
	return JSSlice(s, 0, max(0, n-1)) + "…", true
}

var collators = sync.Pool{New: func() any { return collate.New(language.English) }}

func Compare(a, b string) int {
	c := collators.Get().(*collate.Collator)
	n := c.CompareString(a, b)
	collators.Put(c)
	return n
}

type Diff struct {
	Text                 string
	Additions, Deletions int
	Changed              bool
}

// UnifiedDiff deliberately preserves the reference's one-span diff, including
// unchanged lines between separate edits. It is not a minimal edit script.
func UnifiedDiff(before, after, path string) Diff {
	if before == after {
		return Diff{Text: "No changes in " + path + "."}
	}
	old, next := Lines(before), Lines(after)
	pre, suffix := 0, 0
	for pre < len(old) && pre < len(next) && old[pre] == next[pre] {
		pre++
	}
	for suffix < len(old)-pre && suffix < len(next)-pre && old[len(old)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	oldEnd, nextEnd := len(old)-suffix, len(next)-suffix
	start := max(0, pre-3)
	aEnd, bEnd := min(len(old), oldEnd+3), min(len(next), nextEnd+3)
	out := []string{"--- a/" + path, "+++ b/" + path, fmt.Sprintf("@@ -%d,%d +%d,%d @@", start+1, aEnd-start, start+1, bEnd-start)}
	for _, s := range old[start:pre] {
		out = append(out, " "+s)
	}
	for _, s := range old[pre:oldEnd] {
		out = append(out, "-"+s)
	}
	for _, s := range next[pre:nextEnd] {
		out = append(out, "+"+s)
	}
	for _, s := range old[oldEnd:aEnd] {
		out = append(out, " "+s)
	}
	result := strings.Join(out, "\n")
	if JSLength(result) > 60000 {
		result = JSSlice(result, 0, 60000) + "\n...[diff truncated to 60000 chars]"
	}
	return Diff{Text: result, Additions: nextEnd - pre, Deletions: oldEnd - pre, Changed: true}
}
func DiffBlock(diff string) string { return "\n\n```diff\n" + diff + "\n```" }
