package workhistory

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
)

// ArchiveInitializationTimeout bounds the background SQLite metadata/catalogue
// work. Archive validation and complete exports belong to explicit offline
// operations; neither is part of HTTP startup or this initializer.
const ArchiveInitializationTimeout = 2 * time.Second

// Deferred keeps an optional immutable archive independent of listener and
// ordinary-tool readiness. Construct it before wiring handlers, then call Start
// only after the HTTP server is serving. Readers never wait for initialization.
// The stable Service API remains available for synchronous offline proofs.
type Deferred struct {
	mu      sync.RWMutex
	path    string
	options Options
	service *Service
	state   string
	err     error
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
	open    func(context.Context, string, ...Options) (*Service, error)
}

// NewDeferred performs no filesystem or SQLite operation for a configured
// archive. An absent archive is an immediately ready, empty history, preserving
// the ordinary no-archive work_status contract.
func NewDeferred(path string, options ...Options) *Deferred {
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	if option.PrincipalMap != nil {
		copied := make(map[string]string, len(option.PrincipalMap))
		for current, archived := range option.PrincipalMap {
			copied[current] = archived
		}
		option.PrincipalMap = copied
	}
	d := &Deferred{path: path, options: option, state: "waiting", done: make(chan struct{}), open: OpenContext}
	if path == "" {
		// Open of the empty path normalizes options only; it cannot open a file.
		d.service, d.err = Open("", option)
		d.state = "ready"
		if d.err != nil {
			d.state = "failed"
		}
		d.once.Do(func() { close(d.done) })
	}
	return d
}

// Start starts at most one bounded initializer and returns immediately. A
// failed immutable archive is not retried on ordinary requests, so a damaged or
// slow archive cannot create a request-triggered scan/recovery loop. Restarting
// a private process may retry initialization; no archive mutation is involved.
func (d *Deferred) Start(ctx context.Context) {
	d.mu.Lock()
	if d.state != "waiting" {
		d.mu.Unlock()
		return
	}
	initialization, cancel := context.WithTimeout(ctx, ArchiveInitializationTimeout)
	d.cancel = cancel
	d.state = "loading"
	opener, path, option := d.open, d.path, d.options
	d.mu.Unlock()
	go func() {
		service, err := opener(initialization, path, option)
		if err == nil {
			err = initialization.Err()
		}
		cancel()
		d.mu.Lock()
		if d.state == "closed" {
			d.mu.Unlock()
			if service != nil {
				_ = service.Close()
			}
			return
		}
		d.err = err
		if err != nil {
			d.state = "failed"
		} else {
			d.state = "ready"
			d.service = service
		}
		d.cancel = nil
		d.once.Do(func() { close(d.done) })
		d.mu.Unlock()
		if err != nil && service != nil {
			_ = service.Close()
		}
	}()
}

// State reports operator diagnostics. The underlying error is never included
// in public tool errors, because it may contain private archive paths.
func (d *Deferred) State() (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state, d.err
}

// Done closes when initialization resolves or the wrapper is closed. It is a
// diagnostic signal, never a condition that HTTP/tool handlers should wait on.
func (d *Deferred) Done() <-chan struct{} { return d.done }

func (d *Deferred) reader(ctx context.Context) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	service, state := d.service, d.state
	d.mu.RUnlock()
	if state == "ready" {
		return service, nil
	}
	message := "Archived work history is initializing; retry this request."
	if state == "failed" {
		message = "Archived work history is unavailable; the private archive could not be opened."
	} else if state == "closed" {
		message = "Archived work history is closed."
	}
	return nil, wire.Errorf("work_history_unavailable", "%s", message)
}

func (d *Deferred) Call(ctx context.Context, scope wire.Scope, name string, args map[string]any) (wire.Result, error) {
	if name != "work_status" {
		return wire.Result{}, wire.Errorf("INVALID_ARGUMENT", "Unknown work history tool: %s.", name)
	}
	if scope.PrincipalID == "" {
		return wire.Result{}, wire.Errorf("work_invalid", "Missing authenticated work context.")
	}
	service, err := d.reader(ctx)
	if err != nil {
		return wire.Result{}, err
	}
	return service.Call(ctx, scope, name, args)
}

func (d *Deferred) Runs(ctx context.Context, projectID string) ([]map[string]any, error) {
	service, err := d.reader(ctx)
	if err != nil {
		return nil, err
	}
	return service.Runs(ctx, projectID)
}

func (d *Deferred) Iteration(ctx context.Context, id string) (map[string]any, error) {
	service, err := d.reader(ctx)
	if err != nil {
		return nil, err
	}
	return service.Iteration(ctx, id)
}

func (d *Deferred) OperationCount(ctx context.Context, runID string) (int, error) {
	service, err := d.reader(ctx)
	if err != nil {
		return 0, err
	}
	return service.OperationCount(ctx, runID)
}

func (d *Deferred) AuditTarget(ctx context.Context, scope wire.Scope, args map[string]any, result wire.Result) *audit.Target {
	service, err := d.reader(ctx)
	if err != nil {
		// The nil-reader implementation preserves the source's unattributed
		// target for selected but inaccessible runs, and nil for list scope.
		return (*Service)(nil).AuditTarget(ctx, scope, args, result)
	}
	return service.AuditTarget(ctx, scope, args, result)
}

func (d *Deferred) Export(ctx context.Context, scope wire.Scope, destination io.Writer) (ExportSummary, error) {
	if scope.PrincipalID == "" {
		return ExportSummary{}, wire.Errorf("work_invalid", "Missing authenticated work context.")
	}
	service, err := d.reader(ctx)
	if err != nil {
		return ExportSummary{}, err
	}
	return service.Export(ctx, scope, destination)
}

// Close does not wait on archive initialization. An in-flight initializer is
// cancelled and closes any reader it later finishes opening. Published readers
// close normally, cancelling/refusing their subsequent database work.
func (d *Deferred) Close() error {
	d.mu.Lock()
	if d.state == "closed" {
		d.mu.Unlock()
		return nil
	}
	d.state = "closed"
	cancel, service := d.cancel, d.service
	d.cancel, d.service = nil, nil
	d.once.Do(func() { close(d.done) })
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if service != nil {
		return service.Close()
	}
	return nil
}
