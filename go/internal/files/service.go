package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type Service struct {
	Config           *config.Config
	Workspaces       wire.Resolver
	Snapshots        *SnapshotStore
	TransformText    func(wire.Scope, wire.Workspace, string) string
	SecretWriteCheck func(tool, before, after string) error
}
type fileLock struct {
	mu    sync.Mutex
	users int
}

// All file services in this process share mutation serialization, including
// internal batch persistence that deliberately bypasses the audited tool path.
var writeLockMu sync.Mutex
var writeLocks = map[string]*fileLock{}

func New(cfg *config.Config, resolver wire.Resolver) (*Service, error) {
	return &Service{Config: cfg, Workspaces: resolver, Snapshots: NewSnapshots()}, nil
}
func Within(child, parent string) bool {
	r, e := filepath.Rel(parent, child)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func Rel(abs, root string) string {
	r, e := filepath.Rel(root, abs)
	if e != nil {
		return abs
	}
	if r == "" {
		return "."
	}
	return filepath.ToSlash(r)
}
func Canonical(path string) string {
	p, _ := filepath.Abs(path)
	tail := []string{}
	for {
		if real, e := filepath.EvalSymlinks(p); e == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return filepath.Clean(real)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Clean(path)
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}
func (s *Service) LockPaths(paths ...string) func() {
	keys := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		k := Canonical(p)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	held := make([]*fileLock, 0, len(keys))
	for _, k := range keys {
		writeLockMu.Lock()
		l := writeLocks[k]
		if l == nil {
			l = &fileLock{}
			writeLocks[k] = l
		}
		l.users++
		writeLockMu.Unlock()
		l.mu.Lock()
		held = append(held, l)
	}
	return func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i].mu.Unlock()
			writeLockMu.Lock()
			held[i].users--
			if held[i].users == 0 {
				delete(writeLocks, keys[i])
			}
			writeLockMu.Unlock()
		}
	}
}
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		h, _ := os.UserHomeDir()
		if path == "~" {
			return h
		}
		return filepath.Join(h, strings.TrimPrefix(path, "~/"))
	}
	return path
}
func (s *Service) BlockedGlob(path string) string {
	p := strings.TrimPrefix(filepath.ToSlash(path), "./")
	if p == "" || p == "." {
		return ""
	}
	for _, g := range s.Config.BlockedGlobs {
		if MatchGlob(g, p) || MatchGlob(g, filepath.Base(p)) {
			return g
		}
	}
	return ""
}
func (s *Service) IsBlocked(path string) bool { return s.BlockedGlob(path) != "" }
func (s *Service) AssertNotBlocked(path string) error {
	g := s.BlockedGlob(path)
	if g == "" {
		return nil
	}
	lower := strings.ToLower(g)
	secret := strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, ".env") || strings.Contains(lower, "token") || strings.Contains(lower, ".pem") || strings.Contains(lower, ".key") || strings.Contains(lower, "password") || strings.Contains(lower, "private")
	reason, message := "artifact", fmt.Sprintf("Path is blocked by the artifact/dependency pattern %s: %s. Build outputs, dependencies and caches are not readable or writable through file tools; verify them with bash instead.", g, path)
	if secret {
		reason = "secret"
		message = fmt.Sprintf("Path is blocked because it matches the secret-like pattern %s: %s. Its contents are never exposed to the model.", g, path)
	}
	return &wire.Error{Code: "path_blocked", Message: message, Data: map[string]any{"retry_unchanged": false, "details": map[string]any{"blocked_glob": g, "blocked_reason": reason}}}
}
func (s *Service) ResolvePath(w wire.Workspace, input string, write bool) (string, string, error) {
	if input == "" {
		input = "."
	}
	expanded := expandHome(input)
	abs := expanded
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(w.Root, abs)
	}
	abs, _ = filepath.Abs(abs)
	real, e := filepath.EvalSymlinks(abs)
	hasReal := e == nil
	rel := Rel(abs, w.Root)
	if !Within(abs, w.Root) {
		if hasReal && Within(real, w.Root) {
			abs = real
			rel = Rel(abs, w.Root)
		} else if write {
			parent := filepath.Dir(abs)
			for {
				if _, e := os.Lstat(parent); e == nil {
					break
				}
				n := filepath.Dir(parent)
				if n == parent {
					break
				}
				parent = n
			}
			rp, e := filepath.EvalSymlinks(parent)
			if e != nil || !Within(rp, w.Root) {
				return "", "", failure("path_outside_workspace", "Path escapes workspace root: "+input, nil)
			}
			tail, _ := filepath.Rel(parent, abs)
			abs = filepath.Join(rp, tail)
			rel = Rel(abs, w.Root)
		} else {
			return "", "", failure("path_outside_workspace", "Path escapes workspace root: "+input, nil)
		}
	}
	if e := s.AssertNotBlocked(rel); e != nil {
		return "", "", e
	}
	if hasReal {
		if !Within(real, w.Root) {
			return "", "", failure("path_outside_workspace", "Path resolves outside workspace root through a symlink: "+input, nil)
		}
		if e := s.AssertNotBlocked(Rel(real, w.Root)); e != nil {
			return "", "", e
		}
	}
	if write {
		if st, e := os.Lstat(abs); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", "", failure("path_symlink_refused", "Refusing to write through a symlink: "+input, nil)
		}
		parent := filepath.Dir(abs)
		for {
			if _, e := os.Lstat(parent); e == nil {
				break
			}
			n := filepath.Dir(parent)
			if n == parent {
				break
			}
			parent = n
		}
		if rp, e := filepath.EvalSymlinks(parent); e == nil {
			if !Within(rp, w.Root) {
				return "", "", failure("path_outside_workspace", "Write path resolves through a parent outside the workspace: "+input, nil)
			}
			if e = s.AssertNotBlocked(Rel(rp, w.Root)); e != nil {
				return "", "", e
			}
		}
	}
	return abs, rel, nil
}
func FileError(err error, op, path string) error {
	if err == nil {
		return nil
	}
	var we *wire.Error
	if errors.As(err, &we) {
		return err
	}
	code, detail := "", ""
	switch {
	case errors.Is(err, os.ErrNotExist):
		code, detail = "ENOENT", "no such file or directory"
	case errors.Is(err, os.ErrPermission):
		code, detail = "EACCES", "permission denied"
	case errors.Is(err, syscall.EISDIR):
		code, detail = "EISDIR", "illegal operation on a directory"
	case errors.Is(err, syscall.ENOTDIR):
		code, detail = "ENOTDIR", "not a directory"
	case errors.Is(err, syscall.EEXIST):
		code, detail = "EEXIST", "file already exists"
	}
	if code != "" {
		return fmt.Errorf("%s: %s, %s '%s'", code, detail, op, path)
	}
	return err
}
func (s *Service) ScanLimit() int { return min(2000000, s.Config.Int("maxReadBytes", 180000)*4) }
func ReadSource(path string, limit int) ([]byte, error) {
	st, e := os.Stat(path)
	if e != nil {
		return nil, FileError(e, "stat", path)
	}
	if !st.Mode().IsRegular() {
		return nil, failure("path_not_file", "Not a file: "+path, nil)
	}
	if st.Size() > int64(limit) {
		return nil, failure("file_too_large", fmt.Sprintf("File is too large (%d bytes). Limit: %d bytes.", st.Size(), limit), map[string]any{"tool": "search", "message": "Search for the relevant lines, or read a line range with start_line/end_line."})
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, FileError(e, "open", path)
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil {
		return nil, FileError(e, "read", path)
	}
	if len(b) > limit {
		return nil, failure("file_too_large", fmt.Sprintf("File is too large (%d bytes). Limit: %d bytes.", len(b), limit), nil)
	}
	for _, c := range b {
		if c == 0 {
			return nil, failure("file_binary", "Refusing to read binary file.", nil)
		}
	}
	return b, nil
}
func (s *Service) Display(scope wire.Scope, w wire.Workspace, text string) string {
	if s.TransformText != nil {
		return s.TransformText(scope, w, text)
	}
	text = wire.Redact(text)
	if !s.Config.Bool("exposeAbsolutePaths", false) {
		text = strings.ReplaceAll(text, w.Root, "[workspace:"+w.ID+"]")
	}
	return text
}
func (s *Service) ListFiles(ctx context.Context, w wire.Workspace, root, glob string, hidden bool, limit int) ([]string, error) {
	abs, _, e := s.ResolvePath(w, root, false)
	if e != nil {
		return nil, e
	}
	st, e := os.Stat(abs)
	if e != nil {
		return nil, FileError(e, "stat", abs)
	}
	files := []string{}
	add := func(p string) {
		r := Rel(p, w.Root)
		if s.IsBlocked(r) {
			return
		}
		if !hidden {
			for _, part := range strings.Split(r, "/") {
				if strings.HasPrefix(part, ".") {
					return
				}
			}
		}
		if glob != "" && !MatchGlob(glob, r) {
			return
		}
		files = append(files, r)
	}
	var walk func(string) error
	walk = func(dir string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if len(files) >= limit {
			return nil
		}
		entries, e := os.ReadDir(dir)
		if e != nil {
			return nil
		}
		sort.SliceStable(entries, func(i, j int) bool { return Compare(entries[i].Name(), entries[j].Name()) < 0 })
		for _, entry := range entries {
			if len(files) >= limit {
				break
			}
			p := filepath.Join(dir, entry.Name())
			r := Rel(p, w.Root)
			if s.IsBlocked(r) || !hidden && strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if entry.IsDir() {
				if e = walk(p); e != nil {
					return e
				}
			} else if entry.Type().IsRegular() {
				add(p)
			}
		}
		return nil
	}
	if st.IsDir() {
		e = walk(abs)
	} else if st.Mode().IsRegular() {
		add(abs)
	}
	return files, e
}

var globMu sync.Mutex
var globCache = map[string]*regexp2.Regexp{}

func MatchGlob(pattern, name string) bool {
	neg := false
	for strings.HasPrefix(pattern, "!") && !strings.HasPrefix(pattern, "!(") {
		neg = !neg
		pattern = pattern[1:]
	}
	if strings.HasPrefix(pattern, "#") {
		return false
	}
	key := pattern
	globMu.Lock()
	re, ok := globCache[key]
	globMu.Unlock()
	if !ok {
		compiled, e := regexp2.Compile("^(?:"+globExpression(pattern)+")$", regexp2.ECMAScript)
		if e != nil {
			return false
		}
		compiled.MatchTimeout = 50 * time.Millisecond
		re = compiled
		globMu.Lock()
		if len(globCache) >= 4096 {
			globCache = map[string]*regexp2.Regexp{}
		}
		globCache[key] = re
		globMu.Unlock()
	}
	match, e := re.MatchString(filepath.ToSlash(name))
	if e != nil {
		return false
	}
	if neg {
		return !match
	}
	return match
}
func globExpression(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if strings.ContainsRune("@+*?!", rune(c)) && i+1 < len(p) && p[i+1] == '(' {
			depth, j := 1, i+2
			for ; j < len(p) && depth > 0; j++ {
				if p[j] == '(' {
					depth++
				}
				if p[j] == ')' {
					depth--
				}
			}
			if depth == 0 {
				inside := p[i+2 : j-1]
				parts := splitGlobChoices(inside, '|')
				expr := []string{}
				for _, v := range parts {
					expr = append(expr, globExpression(v))
				}
				joined := strings.Join(expr, "|")
				switch c {
				case '@':
					b.WriteString("(?:" + joined + ")")
				case '+', '*', '?':
					b.WriteString("(?:" + joined + ")" + string(c))
				case '!':
					b.WriteString("(?!(?:" + joined + ")(?:/|$))[^/]*")
				}
				i = j - 1
				continue
			}
		}
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				for i+1 < len(p) && p[i+1] == '*' {
					i++
				}
				if i+1 < len(p) && p[i+1] == '/' {
					b.WriteString("(?:.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := i + 1
			if j < len(p) && (p[j] == '!' || p[j] == '^') {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j < len(p) {
				part := p[i+1 : j]
				if strings.HasPrefix(part, "!") {
					part = "^" + part[1:]
				}
				b.WriteString("[" + part + "]")
				i = j
			} else {
				b.WriteString("\\[")
			}
		case '{':
			depth, j := 1, i+1
			for ; j < len(p) && depth > 0; j++ {
				if p[j] == '{' {
					depth++
				}
				if p[j] == '}' {
					depth--
				}
			}
			if depth == 0 {
				parts := splitGlobChoices(p[i+1:j-1], ',')
				if len(parts) > 1 {
					expr := []string{}
					for _, v := range parts {
						expr = append(expr, globExpression(v))
					}
					b.WriteString("(?:" + strings.Join(expr, "|") + ")")
					i = j - 1
					continue
				}
			}
			b.WriteString("\\{")
		case '\\':
			if i+1 < len(p) {
				i++
				r, n := utf8.DecodeRuneInString(p[i:])
				b.WriteString(regexp2.Escape(string(r)))
				i += n - 1
			} else {
				b.WriteString("\\\\")
			}
		default:
			r, n := utf8.DecodeRuneInString(p[i:])
			b.WriteString(regexp2.Escape(string(r)))
			i += n - 1
		}
	}
	return b.String()
}
func splitGlobChoices(p string, sep byte) []string {
	parts := []string{}
	depth, start := 0, 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '{', '(':
			depth++
		case '}', ')':
			depth--
		default:
			if p[i] == sep && depth == 0 {
				parts = append(parts, p[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, p[start:])
}
