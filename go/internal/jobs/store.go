// Package jobs implements durable process admission, retained output and the four
// compatible Bash/job tools. HTTP request cancellation never owns a job lifetime.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lyeith/codexpro/go/internal/config"
	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
	_ "github.com/mattn/go-sqlite3"
)

type Record struct {
	ID                  string                 `json:"id"`
	WorkspaceID         string                 `json:"workspace_id"`
	ProjectID           string                 `json:"project_id,omitempty"`
	Root                string                 `json:"root"`
	Cwd                 string                 `json:"cwd"`
	Command             string                 `json:"command"`
	CommandLabel        string                 `json:"command_label"`
	PID                 int                    `json:"pid"`
	ProcessIdentity     string                 `json:"process_identity,omitempty"`
	ScopeUnit           string                 `json:"scope_unit,omitempty"`
	ScopeIdentity       *process.ScopeIdentity `json:"scope_identity,omitempty"`
	ResourceClaim       *process.ResourceClaim `json:"resource_claim,omitempty"`
	Origin              string                 `json:"origin"`
	Status              string                 `json:"status"`
	StartedAt           string                 `json:"started_at"`
	StartedAtMS         int64                  `json:"started_at_ms"`
	FinishedAt          string                 `json:"finished_at,omitempty"`
	FinishedAtMS        int64                  `json:"finished_at_ms,omitempty"`
	ExitCode            *int                   `json:"exit_code"`
	Signal              string                 `json:"signal,omitempty"`
	StopReason          string                 `json:"stop_reason,omitempty"`
	StopRequestedAtMS   int64                  `json:"stop_requested_at_ms,omitempty"`
	RequestedStopReason string                 `json:"requested_stop_reason,omitempty"`
	DeadlineMS          int64                  `json:"deadline_ms"`
	TimeoutMS           int64                  `json:"timeout_ms"`
	OutputLimitBytes    int64                  `json:"output_limit_bytes"`
	StdoutPath          string                 `json:"stdout_path"`
	StderrPath          string                 `json:"stderr_path"`
	ExitPath            string                 `json:"exit_path"`
	ResultPath          string                 `json:"result_path"`
	ControlPath         string                 `json:"control_path"`
	GrantPath           string                 `json:"grant_path"`
	SpecPath            string                 `json:"spec_path"`
	Acknowledged        bool                   `json:"acknowledged"`
	BashSessionID       string                 `json:"bash_session_id,omitempty"`
	RunnerVersion       int                    `json:"runner_version"`
	InputJobIDs         []string               `json:"input_job_ids"`
	OutputExpired       bool                   `json:"output_expired,omitempty"`
	CapturedStdoutBytes int64                  `json:"captured_stdout_bytes,omitempty"`
	CapturedStderrBytes int64                  `json:"captured_stderr_bytes,omitempty"`
	StoredBytes         int64                  `json:"stored_bytes,omitempty"`
	LaunchState         string                 `json:"launch_state"`
	Quiescent           bool                   `json:"quiescent"`
	QuiescenceScope     string                 `json:"quiescence_scope,omitempty"`
	QuiescedAt          string                 `json:"quiesced_at,omitempty"`
	OwnerPrincipalID    string                 `json:"owner_principal_id,omitempty"`
	OwnerSessionID      string                 `json:"owner_session_id,omitempty"`
	RequestID           string                 `json:"request_id,omitempty"`
	ParentActionID      string                 `json:"parent_action_id,omitempty"`
}

func copyRecord(r Record) Record {
	r.InputJobIDs = append([]string{}, r.InputJobIDs...)
	if r.ExitCode != nil {
		n := *r.ExitCode
		r.ExitCode = &n
	}
	if r.ScopeIdentity != nil {
		x := *r.ScopeIdentity
		r.ScopeIdentity = &x
	}
	if r.ResourceClaim != nil {
		x := *r.ResourceClaim
		r.ResourceClaim = &x
	}
	return r
}

type jobEntry struct {
	mu         sync.Mutex
	outputMu   sync.RWMutex
	record     Record
	published  atomic.Pointer[Record]
	launching  atomic.Bool
	settling   bool
	nextSettle time.Time
}

func newEntry(r Record, launching bool) *jobEntry {
	e := &jobEntry{record: r}
	snapshot := copyRecord(r)
	e.published.Store(&snapshot)
	e.launching.Store(launching)
	return e
}
func publishEntry(e *jobEntry, r Record) {
	e.record = r
	snapshot := copyRecord(r)
	e.published.Store(&snapshot)
}

type Service struct {
	cfg              *config.Config
	resolver         wire.Resolver
	guard            *files.Service
	db               *sql.DB
	dir              string
	executable       string
	useScopes        bool
	mu               sync.RWMutex
	records          map[string]*jobEntry
	admission        sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	closed           atomic.Bool
	settlements      chan string
	changeMu         sync.Mutex
	changed          chan struct{}
	observerMu       sync.RWMutex
	finishedObserver func(Record)
	completionSink   func(Record) error
	completionWake   chan struct{}
	backgroundMu     sync.Mutex
	backgroundActive atomic.Bool
	scopesReady      atomic.Bool
}

func New(cfg *config.Config, resolver wire.Resolver) (*Service, error) {
	return newService(cfg, resolver, false)
}

// NewPaused loads durable ownership without starting recovery or invoking host
// control commands. HTTP startup publishes its listener before StartBackground.
func NewPaused(cfg *config.Config, resolver wire.Resolver) (*Service, error) {
	return newService(cfg, resolver, true)
}

func newService(cfg *config.Config, resolver wire.Resolver, paused bool) (*Service, error) {
	if cfg == nil || resolver == nil {
		return nil, errors.New("jobs requires configuration and workspace resolver")
	}
	if err := cfg.ValidateIsolation(); err != nil {
		return nil, err
	}
	homeLock, err := lockJobsHome(cfg.Home, false)
	if err != nil {
		return nil, err
	}
	defer homeLock.Close()
	guard, err := files.New(cfg, resolver)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.Home, "jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	useScopes := cfg.HostResourcesHelper != "" || cfg.Bool("jobScopes", false)
	if os.Getenv("CODEXPRO_JOB_SCOPES") == "0" {
		useScopes = false
	}
	if cfg.HostResourcesHelper != "" {
		if !filepath.IsAbs(cfg.HostResourcesHelper) {
			return nil, errors.New("Host job resources require an absolute helper path on Linux.")
		}
		if !useScopes {
			return nil, errors.New("Configured host job resources require systemd job scopes.")
		}
	}
	if useScopes && !paused {
		if err := process.ProbeScopes(context.Background()); err != nil {
			return nil, errors.New("Configured host job resources require systemd job scopes.")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if custom := cfg.String("runnerExecutable", ""); custom != "" {
		executable = custom
	}
	path := filepath.Join(cfg.Home, "jobs.sqlite")
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL,started_ms INTEGER NOT NULL,status TEXT NOT NULL,origin TEXT NOT NULL,payload BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_workspace ON jobs(workspace_id,started_ms DESC);
CREATE TABLE IF NOT EXISTS job_inputs(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,input_id TEXT NOT NULL REFERENCES jobs(id),PRIMARY KEY(job_id,input_id));
CREATE TABLE IF NOT EXISTS job_completions(job_id TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,payload BLOB NOT NULL);
PRAGMA user_version=2;`); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{cfg: cfg, resolver: resolver, guard: guard, db: db, dir: dir, executable: executable, useScopes: useScopes, records: map[string]*jobEntry{}, ctx: ctx, cancel: cancel, settlements: make(chan string, 256), changed: make(chan struct{}), completionWake: make(chan struct{}, 1)}
	s.scopesReady.Store(!useScopes || !paused)
	rows, err := db.Query("SELECT payload FROM jobs")
	if err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			break
		}
		var r Record
		if err = json.Unmarshal(payload, &r); err != nil {
			break
		}
		if r.ID == "" || r.WorkspaceID == "" {
			err = errors.New("invalid persisted job identity")
			break
		}
		// A copied database must never adopt or remove the production store's files.
		for _, p := range []string{r.StdoutPath, r.StderrPath, r.ExitPath, r.ResultPath, r.ControlPath, r.GrantPath, r.SpecPath} {
			if p != "" && !process.Within(dir, p) {
				err = errors.New("job recovery path escapes private state")
				break
			}
		}
		if err != nil {
			break
		}
		s.records[r.ID] = newEntry(r, false)
	}
	if rowErr := rows.Err(); err == nil {
		err = rowErr
	}
	rows.Close()
	if err != nil {
		cancel()
		db.Close()
		return nil, err
	}
	if !paused {
		s.StartBackground()
	}
	return s, nil
}

func (s *Service) Close() error {
	s.backgroundMu.Lock()
	if !s.closed.CompareAndSwap(false, true) {
		s.backgroundMu.Unlock()
		return nil
	}
	s.cancel()
	s.notify()
	s.backgroundMu.Unlock()
	s.wg.Wait()
	return s.db.Close()
}

// OnFinished publishes a copy after the public terminal transition commits.
// The observer must enqueue work rather than block process-control workers.
func (s *Service) OnFinished(fn func(Record)) {
	s.observerMu.Lock()
	s.finishedObserver = fn
	s.observerMu.Unlock()
}
func (s *Service) finished(r Record) {
	s.observerMu.RLock()
	fn := s.finishedObserver
	s.observerMu.RUnlock()
	if fn != nil {
		fn(copyRecord(r))
	}
}

func (s *Service) notify() {
	s.changeMu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.changeMu.Unlock()
}
func (s *Service) changeSignal() <-chan struct{} {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	return s.changed
}

func (s *Service) entry(id string) *jobEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records[id]
}
func (s *Service) entries() []*jobEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*jobEntry, 0, len(s.records))
	for _, e := range s.records {
		out = append(out, e)
	}
	return out
}

// Snapshot is a read-only copy; dashboard reads do not acknowledge or renew jobs.
func (s *Service) Snapshot() []Record {
	out := make([]Record, 0)
	for _, entry := range s.entries() {
		if r := entry.published.Load(); r != nil {
			out = append(out, copyRecord(*r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "running") != (out[j].Status == "running") {
			return out[i].Status == "running"
		}
		return out[i].StartedAtMS > out[j].StartedAtMS
	})
	return out
}

func (s *Service) Get(id, workspace string) (Record, bool) {
	e := s.entry(id)
	if e == nil {
		return Record{}, false
	}
	r := e.published.Load()
	if r == nil {
		return Record{}, false
	}
	if workspace != "" && r.WorkspaceID != workspace {
		return Record{}, false
	}
	return copyRecord(*r), true
}

func (s *Service) require(id, workspace string) (Record, error) {
	if r, ok := s.Get(id, workspace); ok {
		return r, nil
	}
	known := []string{}
	for _, r := range s.Snapshot() {
		if workspace == "" || r.WorkspaceID == workspace {
			known = append(known, r.ID)
			if len(known) == 10 {
				break
			}
		}
	}
	where := ""
	if workspace != "" {
		where = " for this workspace"
	}
	display := strings.Join(known, ", ")
	if display == "" {
		display = "none"
	}
	return Record{}, &wire.Error{Code: "job_not_found", Message: fmt.Sprintf("Unknown job_id: %s. Known jobs%s: %s.", id, where, display), Data: map[string]any{"retry_unchanged": false, "known_job_ids": known}}
}

func (s *Service) persist(previous, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if previous.Status == "running" && r.Status != "running" && r.Origin != "foreground" {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec("UPDATE jobs SET status=?,origin=?,payload=? WHERE id=?", r.Status, r.Origin, b, r.ID); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO job_completions(job_id,payload) VALUES(?,?) ON CONFLICT(job_id) DO NOTHING", r.ID, b); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		select {
		case s.completionWake <- struct{}{}:
		default:
		}
		return nil
	}
	_, err = s.db.Exec("UPDATE jobs SET status=?,origin=?,payload=? WHERE id=?", r.Status, r.Origin, b, r.ID)
	return err
}

func (s *Service) update(id string, fn func(*Record) bool) (Record, error) {
	e := s.entry(id)
	if e == nil {
		return Record{}, wire.Errorf("job_not_found", "Unknown job_id: %s.", id)
	}
	e.mu.Lock()
	next := copyRecord(e.record)
	changed := fn(&next)
	var err error
	if changed {
		err = s.persist(e.record, next)
		if err == nil {
			publishEntry(e, next)
		}
	}
	out := copyRecord(e.record)
	e.mu.Unlock()
	if changed && err == nil {
		s.notify()
	}
	return out, err
}

func ownershipPending(r Record) bool {
	return (r.ScopeUnit != "" || r.ResourceClaim != nil) && !r.Quiescent
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func intPointer(n int) *int { return &n }
func badArgs(message string) error {
	return &wire.Error{Code: "args_invalid", Message: message, Data: map[string]any{"retry_unchanged": false}}
}
func jobError(code, message string) error {
	return &wire.Error{Code: code, Message: message, Data: map[string]any{"retry_unchanged": false}}
}
