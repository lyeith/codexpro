package search

import (
	"context"
	"errors"
	"fmt"
	"os"
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

type factCache struct {
	mu       sync.Mutex
	readable map[readableKey]bool
	sources  map[fileIdentity]*sourceFacts
}

func (c *factCache) readableFact(key readableKey) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.readable[key]
	return v, ok
}
func (c *factCache) storeReadable(key readableKey, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readable == nil || len(c.readable) >= maxFileFacts {
		c.readable = map[readableKey]bool{}
	}
	c.readable[key] = ok
}
func (c *factCache) sourceFact(id fileIdentity) *sourceFacts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sources[id]
}
func (c *factCache) storeSource(id fileIdentity, facts *sourceFacts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sources == nil || len(c.sources) >= maxFileFacts {
		c.sources = map[fileIdentity]*sourceFacts{}
	}
	c.sources[id] = facts
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
		s.facts.storeSource(id, facts)
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
