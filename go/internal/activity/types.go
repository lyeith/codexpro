// Package activity provides authenticated, read-only operator projections.
package activity

import (
	"context"
	"net/http"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
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
	Resolver wire.Resolver
	Audit    *audit.Journal
	Metrics  *Metrics
	Jobs     JobSource
	Clock    func() time.Time
	Git      func(context.Context, wire.Project, bool) (map[string]any, error)
	Batch    func(context.Context, string, string, string) (map[string]any, error)
	Work     *WorkHistory
}
type Handler struct {
	options Options
	ctx     context.Context
	cancel  context.CancelFunc
}
type JSONOptions struct {
	ProjectID, RunID, Agent string
	Limit, OutputBytes      int
	QuietAfterMS            int64
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
	return &Handler{options: options, ctx: ctx, cancel: cancel}
}
func (h *Handler) Close() error { h.cancel(); return nil }
func RegisterRoutes(mux *http.ServeMux, options Options) *Handler {
	h := New(options)
	mux.HandleFunc("/status/collector", h.serveCollector)
	for _, path := range []string{"/activity", "/activity/data", "/activity/live", "/activity/job", "/activity/diff", "/activity/batch", "/activity.json", "/activity/projects/"} {
		mux.Handle(path, h)
	}
	return h
}
