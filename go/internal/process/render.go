package process

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxRecordBytes = 1024 * 1024
const omittedRecord = "[codexpro: overlong or non-text output record omitted]\n"
const renderedLimit = "[codexpro: rendered output limit reached]\n"

var redactors = []struct {
	pattern *regexp.Regexp
	kind    string
}{
	{regexp.MustCompile(`(?i)\b(codexpro_token\s*=\s*)(?:"[^"\r\n]{8,512}"|'[^'\r\n]{8,512}'|\x60[^\x60\r\n]{8,512}\x60|[A-Za-z0-9_./+=-]{8,512})`), "always-prefix"},
	{regexp.MustCompile(`(?i)(["']?codexpro_token["']?\s*:\s*)(?:"[^"\r\n]{8,512}"|'[^'\r\n]{8,512}'|\x60[^\x60\r\n]{8,512}\x60|[A-Za-z0-9_./+=-]{8,512})`), "always-prefix"},
	{regexp.MustCompile(`(?i)((?:\bngrok\s+config\s+add-authtoken|\bcloudflared\s+service\s+install|--(?:token|access-token|auth-token|api[_-]?key|authtoken))(?:=|\s+))[A-Za-z0-9._~+/=-]{8,}`), "prefix"},
	{regexp.MustCompile(`(?i)\b[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}\s*=\s*(?:"[^"\r\n]{12,512}"|'[^'\r\n]{12,512}'|\x60[^\x60\r\n]{12,512}\x60|[A-Za-z0-9_./+=-]{20,512})`), "assignment"},
	{regexp.MustCompile(`(?i)(["']?[A-Za-z0-9_]{0,64}(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PRIVATE[_-]?KEY)[A-Za-z0-9_]{0,64}["']?\s*:\s*)(?:"[^"\r\n]{12,512}"|'[^'\r\n]{12,512}'|\x60[^\x60\r\n]{12,512}\x60|[A-Za-z0-9_./+=-]{20,512})`), "prefix"},
	{regexp.MustCompile(`(?i)\b(Authorization\s*:\s*Bearer\s+)[A-Za-z0-9._~+/=-]{12,}`), "always-prefix"},
	{regexp.MustCompile(`(?i)([?&](?:codexpro_token|token|access_token|auth_token|api[_-]?key)=)[^&\s"'\x60<>]{8,}`), "always-prefix"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}\b`), "whole"},
	{regexp.MustCompile(`\b(?:sk-ant-[A-Za-z0-9_-]{10,}|gh[opsru]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|npm_[A-Za-z0-9_-]{20,})\b`), "whole"},
}

func placeholder(s string) bool {
	s = strings.ToLower(s)
	for _, part := range []string{"[redacted_secret]", "replace-me", "replace-with-long-random-token", "keep-this-codexpro-token-stable", "keep-this-stable-token", "your-ngrok-token", "your-token", "your-api-key-here", "<openai_api_key>", "process.env.", "import.meta.env.", "os.environ", "getenv("} {
		if strings.Contains(s, part) {
			return true
		}
	}
	return s == "sk-..." || strings.HasSuffix(s, "=sk-...")
}

func RedactSensitiveText(s string) string {
	for _, r := range redactors {
		s = r.pattern.ReplaceAllStringFunc(s, func(match string) string {
			if r.kind != "always-prefix" && placeholder(match) {
				return match
			}
			switch r.kind {
			case "assignment":
				i := strings.IndexByte(match, '=')
				if i >= 0 {
					return strings.TrimRight(match[:i], " \t") + "= [REDACTED_SECRET]"
				}
			case "prefix", "always-prefix":
				m := r.pattern.FindStringSubmatch(match)
				if len(m) > 1 {
					return m[1] + "[REDACTED_SECRET]"
				}
			}
			return "[REDACTED_SECRET]"
		})
	}
	return s
}

func pathContinuation(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '.' || b == '-'
}

func RedactPaths(s string, paths []PathRedaction) string {
	for _, p := range paths {
		if p[0] == "" {
			continue
		}
		var b strings.Builder
		at := 0
		for {
			i := strings.Index(s[at:], p[0])
			if i < 0 {
				b.WriteString(s[at:])
				break
			}
			i += at
			j := i + len(p[0])
			b.WriteString(s[at:i])
			if j == len(s) || !pathContinuation(s[j]) {
				b.WriteString(p[1])
			} else {
				b.WriteString(s[i:j])
			}
			at = j
		}
		s = b.String()
	}
	return s
}

func OrderedPaths(paths []PathRedaction) []PathRedaction {
	seen := map[string]bool{}
	out := make([]PathRedaction, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p[0]) && !seen[p[0]] {
			seen[p[0]] = true
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i][0]) > len(out[j][0]) })
	return out
}

type RenderBudget struct {
	mu        sync.Mutex
	remaining int64
	exhausted bool
}

func NewRenderBudget(limit int64) *RenderBudget { return &RenderBudget{remaining: limit} }
func (b *RenderBudget) Exhausted() bool         { b.mu.Lock(); defer b.mu.Unlock(); return b.exhausted }

// OutputWriter publishes complete redacted UTF-8 records only. Partial credentials
// stay private until their record ends, including across arbitrary read boundaries.
type OutputWriter struct {
	mu       sync.Mutex
	file     *os.File
	paths    []PathRedaction
	budget   *RenderBudget
	pending  []byte
	dropping bool
	closed   bool
}

func NewOutputWriter(path string, paths []PathRedaction, budget *RenderBudget) (*OutputWriter, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if e != nil {
		return nil, e
	}
	return &OutputWriter{file: f, paths: OrderedPaths(paths), budget: budget}, nil
}

func (w *OutputWriter) record() error {
	s := string(w.pending)
	if w.dropping || bytes.IndexByte(w.pending, 0) >= 0 || !utf8.Valid(w.pending) || strings.ContainsRune(s, utf8.RuneError) {
		s = omittedRecord
	} else {
		s = RedactPaths(RedactSensitiveText(s), w.paths)
	}
	w.pending = w.pending[:0]
	w.dropping = false
	if w.budget != nil {
		w.budget.mu.Lock()
		if w.budget.exhausted {
			w.budget.mu.Unlock()
			return nil
		}
		if int64(len(s)) > w.budget.remaining {
			w.budget.exhausted = true
			s = renderedLimit
			if int64(len(s)) > w.budget.remaining {
				w.budget.mu.Unlock()
				return nil
			}
		}
		w.budget.remaining -= int64(len(s))
		w.budget.mu.Unlock()
	}
	_, e := io.WriteString(w.file, s)
	return e
}

func (w *OutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		count := len(p)
		if i >= 0 {
			count = i + 1
		}
		if !w.dropping {
			if len(w.pending)+count > maxRecordBytes {
				w.dropping = true
				w.pending = w.pending[:0]
			} else {
				w.pending = append(w.pending, p[:count]...)
			}
		}
		p = p[count:]
		if i >= 0 {
			if e := w.record(); e != nil {
				return n - len(p), e
			}
		}
	}
	return n, nil
}

func (w *OutputWriter) Finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var e error
	if len(w.pending) > 0 || w.dropping {
		e = w.record()
	}
	if syncErr := w.file.Sync(); e == nil {
		e = syncErr
	}
	if closeErr := w.file.Close(); e == nil {
		e = closeErr
	}
	return e
}
