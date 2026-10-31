package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type Action map[string]any
type Options struct {
	Directory      string
	Enabled        bool
	MaxBytes       int64
	RetainActions  int
	MaxOutputBytes int
	SegmentBytes   int64
	Clock          func() time.Time
	Redact         func(string) string
	Fault          func(string) error
}
type Target struct{ Scope, ProjectID, WorkspaceID, RunID string }
type RecordInput struct {
	// CompletionDedupKey is the private durable outbox identity for bash_job.
	// It never supplies actor/request/transport context or an exported field.
	// Its domain-separated digest supplies an opaque action ID so recovery
	// can reconstruct the completion receipt from a synced JSONL record.
	CompletionDedupKey    string
	ActionID              string
	ToolName              string
	InvocationSurface     string
	Args                  map[string]any
	Result                wire.Result
	Error                 error
	StartedAt, FinishedAt time.Time
	Mutating              bool
	Scope                 wire.Scope
	Target                *Target
	Before, After         map[string]any
	DashboardMetadata     map[string]any
}
type Receipt struct {
	Enabled   bool   "json:\"enabled\""
	Recorded  bool   "json:\"recorded\""
	Duplicate bool   "json:\"duplicate\""
	Status    string "json:\"status\""
	ActionID  string "json:\"action_id,omitempty\""
	Sequence  int64  "json:\"sequence,omitempty\""
}
type InFlightAction struct {
	ActionID        string           "json:\"actionId\""
	ToolName        string           "json:\"toolName\""
	StartedAt       string           "json:\"startedAt\""
	State           string           "json:\"state\""
	ProjectID       string           "json:\"projectId,omitempty\""
	WorkspaceID     string           "json:\"workspaceId,omitempty\""
	RequestMetadata map[string]any   "json:\"requestMetadata\""
	ShellScripts    []map[string]any "json:\"shellScripts\""
}
type Filter struct {
	AfterSequence                                            *int64
	CursorRecovery                                           bool
	Limit                                                    int
	MutatingOnly                                             bool
	ToolName, OperationClass, Status, ProjectID, WorkspaceID string
}
type ListResult struct {
	Enabled          bool           "json:\"enabled\""
	Mode             string         "json:\"mode\""
	Namespace        string         "json:\"namespace\""
	SchemaVersion    string         "json:\"schema_version\""
	Actions          []Action       "json:\"actions\""
	NextSequence     int64          "json:\"next_sequence\""
	EarliestSequence int64          "json:\"earliest_sequence\""
	LatestSequence   int64          "json:\"latest_sequence\""
	HasMore          bool           "json:\"has_more\""
	MalformedRecords int64          "json:\"malformed_records\""
	GapDetected      bool           "json:\"gap_detected\""
	CursorRecovery   map[string]any "json:\"cursor_recovery,omitempty\""
}
type entry struct {
	Completion                                        bool
	Sequence                                          int64
	ActionID, ProjectID, Bucket, Fingerprint, Segment string
	Offset, Length                                    int64
}
type state struct {
	Latest      int64  "json:\"latest\""
	Rotation    int64  "json:\"rotation\""
	CompactedAt string "json:\"compacted_at,omitempty\""
	Malformed   int64  "json:\"malformed\""
	Gap         bool   "json:\"gap\""
}
type request struct {
	action Action
	reply  chan response
}
type response struct {
	receipt Receipt
	err     error
}

func randomID(prefix string) string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b[:])
}
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func clone(v Action) Action {
	data, _ := json.Marshal(v)
	var out Action
	_ = json.Unmarshal(data, &out)
	return out
}
func public(v Action) Action { out := clone(v); delete(out, "dashboard_metadata"); return out }
func toMap(v any) map[string]any {
	data, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Journal) Begin(input RecordInput) string {
	if !s.options.Enabled {
		return ""
	}
	id := randomID("cpa_")
	started := input.StartedAt
	if started.IsZero() {
		started = s.options.Clock()
	}
	target := input.Target
	project, workspace := "", ""
	if target != nil {
		project, workspace = target.ProjectID, target.WorkspaceID
	} else {
		project, _ = input.Before["project_id"].(string)
		workspace, _ = input.Before["workspace_id"].(string)
	}
	scripts := dashboardScripts(input)
	if scripts == nil {
		scripts = []map[string]any{}
	}
	for _, script := range scripts {
		script["script"] = s.options.Redact(text(script["script"]))
	}
	s.inflightMu.Lock()
	s.inflight[id] = InFlightAction{ActionID: id, ToolName: input.ToolName, StartedAt: timestamp(started), State: "queued", ProjectID: project, WorkspaceID: workspace, RequestMetadata: summarizeArgs(input.ToolName, input.Args), ShellScripts: scripts}
	s.inflightMu.Unlock()
	return id
}
func (s *Journal) MarkRunning(id string) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if v, ok := s.inflight[id]; ok {
		v.State = "running"
		s.inflight[id] = v
	}
}
func (s *Journal) End(id string) { s.inflightMu.Lock(); delete(s.inflight, id); s.inflightMu.Unlock() }
func (s *Journal) InFlight() []InFlightAction {
	s.inflightMu.RLock()
	defer s.inflightMu.RUnlock()
	out := make([]InFlightAction, 0, len(s.inflight))
	for _, v := range s.inflight {
		b, _ := json.Marshal(v)
		var c InFlightAction
		_ = json.Unmarshal(b, &c)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, k int) bool {
		if out[i].StartedAt == out[k].StartedAt {
			return out[i].ActionID < out[k].ActionID
		}
		return out[i].StartedAt < out[k].StartedAt
	})
	return out
}
func (s *Journal) Record(ctx context.Context, input RecordInput) (Receipt, error) {
	status, _ := classify(input)
	fallback := Receipt{Enabled: s.options.Enabled, Status: status}
	if !s.options.Enabled {
		return fallback, nil
	}
	if err := ctx.Err(); err != nil {
		return fallback, err
	}
	if input.CompletionDedupKey != "" {
		if input.ToolName != "bash_job" {
			return fallback, errors.New("completion deduplication is only available for bash_job")
		}
		id := completionActionID(input.CompletionDedupKey)
		if input.ActionID != "" && input.ActionID != id {
			return fallback, errors.New("completion deduplication conflicts with the supplied action ID")
		}
		input.ActionID = id
	}
	prepared, err := s.build(input, 1)
	if err != nil {
		return fallback, err
	}
	prepared = cloneMetadata(prepared).(Action)
	s.admitMu.RLock()
	if s.closed {
		s.admitMu.RUnlock()
		return fallback, errors.New("audit journal is closed")
	}
	req := request{action: prepared, reply: make(chan response, 1)}
	select {
	case s.queue <- req:
		s.admitMu.RUnlock()
	case <-ctx.Done():
		s.admitMu.RUnlock()
		return fallback, ctx.Err()
	}
	select {
	case result := <-req.reply:
		return result.receipt, result.err
	case <-ctx.Done():
		return fallback, ctx.Err()
	}
}
func (s *Journal) Close() error {
	s.admitMu.Lock()
	if s.closed {
		s.admitMu.Unlock()
		return nil
	}
	s.closed = true
	if s.options.Enabled {
		close(s.queue)
	}
	s.admitMu.Unlock()
	if !s.options.Enabled {
		return nil
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.current != nil {
		errs = append(errs, s.current.Close())
	}
	if s.db != nil {
		errs = append(errs, s.db.Close())
	}
	if s.lockFile != nil {
		errs = append(errs, s.lockFile.Close())
	}
	return errors.Join(errs...)
}
func (s *Journal) run() {
	defer s.wg.Done()
	for first := range s.queue {
		batch := []request{first}
		timer := time.NewTimer(2 * time.Millisecond)
	gather:
		for len(batch) < 128 {
			select {
			case next, ok := <-s.queue:
				if !ok {
					break gather
				}
				batch = append(batch, next)
			case <-timer.C:
				break gather
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.writeBatch(batch)
	}
}
