// Package activity provides authenticated, read-only operator projections.
package activity

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/jobs"
	"github.com/lyeith/codexpro/go/internal/wire"
)

type JobSource interface {
	Snapshot() []jobs.Record
	Get(string, string) (jobs.Record, bool)
	OutputMetadata(jobs.Record) map[string]any
	ReadTail(context.Context, string, string, int) (jobs.Output, error)
}

// WorkHistory deliberately exposes no coordinator sweep, claim, or mutation.
type WorkHistory struct {
	Runs           func(context.Context, string) ([]map[string]any, error)
	Iteration      func(context.Context, string) (map[string]any, error)
	OperationCount func(context.Context, string) (int, error)
}
type Options struct {
	Config   *config.Config
	Resolver wire.Resolver
	Audit    *audit.Journal
	Jobs     JobSource
	Clock    func() time.Time
	Git      func(context.Context, wire.Project, bool) (map[string]any, error)
	Batch    func(context.Context, string, string, string) (map[string]any, error)
	SafePath func(string) bool
	Work     *WorkHistory
}
type Handler struct {
	options  Options
	ctx      context.Context
	cancel   context.CancelFunc
	gitMu    sync.Mutex
	gitCache map[string]*gitCacheEntry
	gitSlots chan struct{}
}
type gitCacheEntry struct {
	at    time.Time
	value map[string]any
	ready chan struct{}
}
type JSONOptions struct {
	ProjectID, RunID   string
	Limit, OutputBytes int
	QuietAfterMS       int64
}
type HTTPError struct {
	Status        int
	Code, Message string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}
func New(options Options) *Handler {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Audit == nil {
		options.Audit, _ = audit.Open(audit.Options{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Handler{options: options, ctx: ctx, cancel: cancel, gitCache: map[string]*gitCacheEntry{}, gitSlots: make(chan struct{}, 2)}
}
func (h *Handler) Close() error { h.cancel(); return nil }
func RegisterRoutes(mux *http.ServeMux, options Options) *Handler {
	h := New(options)
	for _, path := range []string{"/activity", "/activity/data", "/activity/live", "/activity/job", "/activity/diff", "/activity/batch", "/activity.json", "/activity/projects/"} {
		mux.Handle(path, h)
	}
	return h
}
