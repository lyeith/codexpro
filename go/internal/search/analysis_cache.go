package search

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// Repository analysis used to read every inventory file on every call, only to
// compute its cache key, and concurrent callers repeated the whole analysis.
// On a large root (the ~/Projects meta-project: 18k files, 45 MB) that cost
// about 8 s per call even on a cache hit, multiplied by concurrent sessions.
//
// Two mechanisms keep the results identical while bounding the cost:
//   - Per-file facts (readability for the inventory, and each source file's
//     declarations and import specifiers) are cached by file identity: absolute
//     path, size, modification time, inode and device. An unchanged file is
//     only stat'ed; a changed file is read again exactly as before.
//   - One analysis runs per workspace at a time, with a time budget. Callers
//     arriving meanwhile share its result. The budget is detached from the
//     first caller's cancellation, so one client abort does not fail the
//     others; facts gathered before the budget expires are kept, so a later
//     call resumes from them instead of starting over.

const maxFileFacts = 200000
const maxFileFactBytes int64 = 64 << 20

type fileIdentity struct {
	Abs        string
	Size       int64
	ModifiedNS int64
	Inode, Dev uint64
}

func identityOf(abs string, st os.FileInfo) fileIdentity {
	id := fileIdentity{Abs: abs, Size: st.Size(), ModifiedNS: st.ModTime().UnixNano()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		id.Inode, id.Dev = sys.Ino, uint64(sys.Dev)
	}
	return id
}

type readableKey struct {
	fileIdentity
	Limit int
}

type declarationFact struct {
	Name, Kind string
	Line       int
	Exported   bool
}

// sourceFacts is everything the analysis derives from one file's text that does
// not depend on the rest of the inventory. Import specifiers are kept raw, in
// line order, and resolved against the current inventory on every analysis.
type sourceFacts struct {
	TextLen      int
	Declarations []declarationFact
	ImportSpecs  []string
}

// A single current version per path avoids retaining every edit. The LRU is
// bounded by both accounted bytes and entries; zero-valued caches use defaults.
type cachedFileFacts struct {
	id            fileIdentity
	readableKnown bool
	readableLimit int
	readable      bool
	source        *sourceFacts
	bytes         int64
	element       *list.Element
}

type factCache struct {
	mu         sync.Mutex
	entries    map[string]*cachedFileFacts
	order      list.List
	bytes      int64
	maxBytes   int64
	maxEntries int
}

func (c *factCache) limits() (int64, int) {
	bytes, entries := c.maxBytes, c.maxEntries
	if bytes <= 0 {
		bytes = maxFileFactBytes
	}
	if entries <= 0 {
		entries = maxFileFacts
	}
	return bytes, entries
}

func (c *factCache) remove(entry *cachedFileFacts) {
	delete(c.entries, entry.id.Abs)
	c.order.Remove(entry.element)
	c.bytes -= entry.bytes
}

func (c *factCache) find(id fileIdentity) *cachedFileFacts {
	entry := c.entries[id.Abs]
	if entry != nil && entry.id == id {
		return entry
	}
	return nil
}

func (c *factCache) ensure(id fileIdentity) *cachedFileFacts {
	if entry := c.find(id); entry != nil {
		c.order.MoveToFront(entry.element)
		return entry
	}
	if old := c.entries[id.Abs]; old != nil {
		c.remove(old)
	}
	if c.entries == nil {
		c.entries = make(map[string]*cachedFileFacts)
	}
	id.Abs = strings.Clone(id.Abs)
	// Includes the entry, list links, map overhead and pathname. Source slices
	// and their detached strings are charged separately.
	entry := &cachedFileFacts{id: id, bytes: 256 + int64(len(id.Abs))}
	entry.element = c.order.PushFront(entry)
	c.entries[id.Abs] = entry
	c.bytes += entry.bytes
	return entry
}

func (c *factCache) trim() {
	bytes, count := c.limits()
	for c.bytes > bytes || len(c.entries) > count {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.remove(oldest.Value.(*cachedFileFacts))
	}
}

func (c *factCache) readableFact(key readableKey) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.find(key.fileIdentity)
	if entry == nil || !entry.readableKnown || entry.readableLimit != key.Limit {
		return false, false
	}
	c.order.MoveToFront(entry.element)
	return entry.readable, true
}

func (c *factCache) storeReadable(key readableKey, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.ensure(key.fileIdentity)
	entry.readableKnown, entry.readableLimit, entry.readable = true, key.Limit, ok
	c.trim()
}

func (c *factCache) sourceFact(id fileIdentity) *sourceFacts {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.find(id)
	if entry == nil || entry.source == nil {
		return nil
	}
	c.order.MoveToFront(entry.element)
	return entry.source
}

func detachedSourceFacts(facts *sourceFacts) (*sourceFacts, int64) {
	out := &sourceFacts{TextLen: facts.TextLen, Declarations: make([]declarationFact, len(facts.Declarations)), ImportSpecs: make([]string, len(facts.ImportSpecs))}
	bytes := int64(64 + 64*len(out.Declarations) + 24*len(out.ImportSpecs))
	for i, declaration := range facts.Declarations {
		declaration.Name = strings.Clone(declaration.Name)
		declaration.Kind = strings.Clone(declaration.Kind)
		out.Declarations[i] = declaration
		bytes += int64(len(declaration.Name) + len(declaration.Kind))
	}
	for i, spec := range facts.ImportSpecs {
		out.ImportSpecs[i] = strings.Clone(spec)
		bytes += int64(len(spec))
	}
	return out, bytes
}

// Return the detached immutable value even when it is too large to retain. The
// first analysis must not keep the original source text through regexp slices.
func (c *factCache) storeSource(id fileIdentity, facts *sourceFacts) *sourceFacts {
	detached, size := detachedSourceFacts(facts)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.ensure(id)
	c.bytes -= entry.bytes
	entry.bytes = 256 + int64(len(entry.id.Abs))
	entry.source = nil
	budget, _ := c.limits()
	if entry.bytes+size <= budget {
		entry.source = detached
		entry.bytes += size
	}
	c.bytes += entry.bytes
	c.trim()
	return detached
}

// readableSource reports whether files.ReadSource would accept the file, reading
// it only when its identity has not been seen with the same limit.
func (s *Service) readableSource(abs string, st os.FileInfo, limit int) bool {
	key := readableKey{identityOf(abs, st), limit}
	if ok, known := s.facts.readableFact(key); known {
		return ok
	}
	_, e := files.ReadSource(abs, limit)
	if after, se := os.Stat(abs); se == nil && identityOf(abs, after) == key.fileIdentity {
		s.facts.storeReadable(key, e == nil)
	}
	return e == nil
}

// extractSource returns the per-file facts for a source file, reading it only
// when its identity is new. remaining is the scan budget left for this
// analysis, applied exactly as the uncached read did.
func (s *Service) extractSource(abs, lang string, remaining int) (*sourceFacts, error) {
	st, e := os.Stat(abs)
	if e != nil {
		return nil, e
	}
	id := identityOf(abs, st)
	if facts := s.facts.sourceFact(id); facts != nil {
		return facts, nil
	}
	b, e := readAnalysisBytes(abs, remaining)
	if e != nil {
		return nil, e
	}
	text := files.DecodeText(b)
	facts := &sourceFacts{TextLen: len(text), Declarations: []declarationFact{}, ImportSpecs: []string{}}
	for i, line := range analysisLines(text) {
		for _, p := range declarations[lang] {
			m := matchRequired(p.re, line, p.required)
			if len(m) < 2 || m[1] == "" {
				continue
			}
			facts.Declarations = append(facts.Declarations, declarationFact{m[1], p.kind, i + 1, exportedDeclaration.MatchString(line)})
		}
		spec := ""
		if lang == "typescript" || lang == "javascript" {
			m := matchRequired(jsImport, line, jsImportRequired)
			if len(m) > 2 {
				spec = m[1]
				if spec == "" {
					spec = m[2]
				}
			}
		} else if lang == "c" || lang == "cpp" {
			m := matchRequired(cImport, line, cImportRequired)
			if len(m) > 1 {
				spec = m[1]
			}
		}
		if spec != "" {
			facts.ImportSpecs = append(facts.ImportSpecs, spec)
		}
	}
	// A read that was cut short by the remaining budget, or a file that changed
	// while it was read, is used once but never remembered.
	if after, se := os.Stat(abs); se == nil && identityOf(abs, after) == id && len(b) <= remaining {
		facts = s.facts.storeSource(id, facts)
	} else {
		facts, _ = detachedSourceFacts(facts)
	}
	return facts, nil
}

type analysisFlight struct {
	done   chan struct{}
	result *Analysis
	err    error
}

func (s *Service) analysisBudget() time.Duration {
	return time.Duration(max(1, s.Config.Int("analysisBudgetMs", 10000))) * time.Millisecond
}

// Analyze returns the repository analysis for the workspace. Concurrent callers
// share one bounded computation; each caller still returns as soon as its own
// context ends.
func (s *Service) Analyze(ctx context.Context, w wire.Workspace) (*Analysis, error) {
	if !s.Config.Bool("analysisEnabled", true) {
		return nil, fmt.Errorf("Repository analysis is disabled by CODEXPRO_ANALYSIS=0.")
	}
	s.mu.Lock()
	if s.flights == nil {
		s.flights = map[string]*analysisFlight{}
	}
	flight := s.flights[w.ID]
	if flight == nil {
		flight = &analysisFlight{done: make(chan struct{})}
		s.flights[w.ID] = flight
		budget := s.analysisBudget()
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
		go func() {
			defer cancel()
			result, err := s.analyze(flightCtx, w)
			if err != nil && errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("repository analysis did not finish within its %d ms time budget; partial progress is kept for the next call", budget.Milliseconds())
			}
			s.mu.Lock()
			flight.result, flight.err = result, err
			delete(s.flights, w.ID)
			s.mu.Unlock()
			close(flight.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-flight.done:
		if flight.err != nil {
			return nil, flight.err
		}
		copy := *flight.result
		return &copy, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
