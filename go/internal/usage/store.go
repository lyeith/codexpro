package usage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const weekMS int64 = 7 * 24 * 60 * 60 * 1000

type Clock func() time.Time
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string      { return e.Message }
func bad(message string) error      { return &Error{400, message} }
func conflict(message string) error { return &Error{409, message} }

type integer int64

func (i *integer) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) > 9007199254740991 {
		return errors.New("invalid integer")
	}
	*i = integer(f)
	return nil
}

var identity = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.:-]{0,199}$")

type submission struct {
	QueryID      string  "json:\"query_id\""
	TurnID       string  "json:\"turn_id\""
	ProjectID    *string "json:\"project_id\""
	SubmittedAt  string  "json:\"submitted_at\""
	ModeEvidence string  "json:\"mode_evidence\""
}
type coverage struct {
	Unconfirmed      integer "json:\"unconfirmed\""
	FailedBeforeSend integer "json:\"failed_before_send\""
	UnknownMode      integer "json:\"unknown_mode\""
	OldestRecordAt   *string "json:\"oldest_record_at\""
}
type batch struct {
	Version     integer      "json:\"schema_version\""
	CollectorID string       "json:\"collector_id\""
	Submissions []submission "json:\"submissions\""
	Coverage    coverage     "json:\"coverage\""
}
type reset struct {
	Version          integer "json:\"schema_version\""
	RequestID        string  "json:\"request_id\""
	ExpectedRevision integer "json:\"expected_revision\""
}
type weekly struct {
	ResetsAt      string  "json:\"resets_at\""
	WindowMinutes integer "json:\"window_minutes\""
	UsedPercent   float64 "json:\"used_percent\""
}
type limits struct {
	Version        integer "json:\"schema_version\""
	CheckedAt      string  "json:\"checked_at\""
	ResetRequestID *string "json:\"reset_request_id,omitempty\""
	Weekly         *weekly "json:\"weekly\""
}
type PendingReset struct {
	RequestID   string "json:\"request_id\""
	RequestedAt string "json:\"requested_at\""
	By          string "json:\"by\""
}
type Settings struct {
	Revision         int64         "json:\"revision\""
	WeeklyAnchorAt   *string       "json:\"weekly_anchor_at\""
	ManualResetAt    *string       "json:\"manual_reset_at\""
	PendingReset     *PendingReset "json:\"pending_reset\""
	LimitsCheckedAt  *string       "json:\"limits_checked_at\""
	LimitsVerifiedAt *string       "json:\"limits_verified_at\""
	LimitsError      *string       "json:\"limits_error\""
}
type Store struct {
	db    *sql.DB
	clock Clock
	mu    sync.Mutex
}

func Open(directory string, clock Clock) (*Store, error) {
	if clock == nil {
		clock = time.Now
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	file := filepath.Join(directory, "usage.sqlite")
	u := url.URL{Scheme: "file", Path: file}
	db, err := sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := "CREATE TABLE IF NOT EXISTS pro_submissions(query_id TEXT NOT NULL,turn_id TEXT NOT NULL,project TEXT,submitted_at TEXT NOT NULL,submitted_ms INTEGER NOT NULL,mode_evidence TEXT NOT NULL,PRIMARY KEY(query_id,turn_id));" +
		"CREATE INDEX IF NOT EXISTS pro_submissions_time ON pro_submissions(submitted_ms,project);" +
		"CREATE TABLE IF NOT EXISTS usage_settings(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS usage_collectors(id TEXT PRIMARY KEY,body TEXT NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS usage_changes(sequence INTEGER PRIMARY KEY AUTOINCREMENT,body TEXT NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS usage_requests(id TEXT PRIMARY KEY,payload TEXT NOT NULL,result TEXT NOT NULL);"
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(file, 0600); err != nil {
		db.Close()
		return nil, err
	}
	initial, _ := json.Marshal(Settings{Revision: 1})
	if _, err = db.Exec("INSERT OR IGNORE INTO usage_settings VALUES (1,?)", string(initial)); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, clock: clock}, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func iso(t time.Time) string                    { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func parseDate(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
func strict(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return bad("Invalid usage request.")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return bad("Invalid usage request.")
	}
	return nil
}
func fields(data []byte, names ...string) bool {
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil || v == nil {
		return false
	}
	for _, name := range names {
		if _, ok := v[name]; !ok {
			return false
		}
	}
	return true
}
func ptr(v string) *string { return &v }
func millis(v *string) int64 {
	if v == nil {
		return 0
	}
	t, _ := parseDate(*v)
	return t.UnixMilli()
}
func (s *Store) settings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (Settings, error) {
	var body string
	var out Settings
	if err := q.QueryRowContext(ctx, "SELECT body FROM usage_settings WHERE id=1").Scan(&body); err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return out, fmt.Errorf("invalid usage settings: %w", err)
	}
	return out, nil
}
func save(ctx context.Context, tx *sql.Tx, v Settings) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE usage_settings SET body=? WHERE id=1", string(b))
	return err
}
func record(ctx context.Context, tx *sql.Tx, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO usage_changes(body) VALUES (?)", string(b))
	return err
}
func (s *Store) Ingest(ctx context.Context, data []byte) (map[string]any, error) {
	var b batch
	if err := strict(data, &b); err != nil {
		return nil, err
	}
	if b.Version != 1 || !identity.MatchString(b.CollectorID) || b.Submissions == nil || len(b.Submissions) > 100 || !fields(data, "schema_version", "collector_id", "submissions", "coverage") {
		return nil, bad("Invalid usage request.")
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if !fields(raw["coverage"], "unconfirmed", "failed_before_send", "unknown_mode", "oldest_record_at") || b.Coverage.Unconfirmed < 0 || b.Coverage.FailedBeforeSend < 0 || b.Coverage.UnknownMode < 0 {
		return nil, bad("Invalid usage request.")
	}
	if b.Coverage.OldestRecordAt != nil {
		if _, err := parseDate(*b.Coverage.OldestRecordAt); err != nil {
			return nil, bad("Invalid usage request.")
		}
	}
	var rawSubs []json.RawMessage
	_ = json.Unmarshal(raw["submissions"], &rawSubs)
	for i, v := range b.Submissions {
		if !fields(rawSubs[i], "query_id", "turn_id", "project_id", "submitted_at", "mode_evidence") || !identity.MatchString(v.QueryID) || !identity.MatchString(v.TurnID) || (v.ProjectID != nil && !identity.MatchString(*v.ProjectID)) || (v.ModeEvidence != "selected" && v.ModeEvidence != "requested") {
			return nil, bad("Invalid usage request.")
		}
		if _, err := parseDate(v.SubmittedAt); err != nil {
			return nil, bad("Invalid usage request.")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inserted := 0
	for _, v := range b.Submissions {
		t, _ := parseDate(v.SubmittedAt)
		at := t.UnixMilli()
		if at < 0 || at > now.UnixMilli()+300000 {
			return nil, bad("Submission timestamp is outside the supported range.")
		}
		var oldAt int64
		var oldProject sql.NullString
		var oldMode string
		err = tx.QueryRowContext(ctx, "SELECT submitted_ms,project,mode_evidence FROM pro_submissions WHERE query_id=? AND turn_id=?", v.QueryID, v.TurnID).Scan(&oldAt, &oldProject, &oldMode)
		if err == nil {
			if oldAt != at {
				return nil, conflict("Conflicting timestamp for the same submitted turn.")
			}
			if oldProject.Valid && v.ProjectID != nil && oldProject.String != *v.ProjectID {
				return nil, conflict("Conflicting project for the same submitted turn.")
			}
			_, err = tx.ExecContext(ctx, "UPDATE pro_submissions SET project=COALESCE(project,?),mode_evidence=CASE WHEN mode_evidence='selected' THEN mode_evidence ELSE ? END WHERE query_id=? AND turn_id=?", v.ProjectID, v.ModeEvidence, v.QueryID, v.TurnID)
		} else if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO pro_submissions VALUES (?,?,?,?,?,?)", v.QueryID, v.TurnID, v.ProjectID, iso(t), at, v.ModeEvidence)
			inserted++
		}
		if err != nil {
			return nil, err
		}
	}
	collector := map[string]any{"collector_id": b.CollectorID, "last_sync_at": iso(now), "coverage": b.Coverage}
	body, _ := json.Marshal(collector)
	if _, err = tx.ExecContext(ctx, "INSERT INTO usage_collectors VALUES (?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", b.CollectorID, string(body)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"schema_version": 1, "accepted": len(b.Submissions), "inserted": inserted}, nil
}
func (s *Store) RequestReset(ctx context.Context, data []byte, principal string) (Settings, error) {
	var request reset
	var zero Settings
	if err := strict(data, &request); err != nil {
		return zero, err
	}
	if request.Version != 1 || request.ExpectedRevision < 1 || !identity.MatchString(request.RequestID) {
		return zero, bad("Invalid usage request.")
	}
	payload, _ := json.Marshal(request)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var priorPayload, result string
	err = tx.QueryRowContext(ctx, "SELECT payload,result FROM usage_requests WHERE id=?", request.RequestID).Scan(&priorPayload, &result)
	if err == nil {
		if priorPayload != string(payload) {
			return zero, conflict("Request ID was used for a different counter change.")
		}
		err = json.Unmarshal([]byte(result), &zero)
		return zero, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	settings, err := s.settings(ctx, tx)
	if err != nil {
		return zero, err
	}
	if settings.Revision != int64(request.ExpectedRevision) {
		return zero, conflict("Counter settings changed; refresh before trying again.")
	}
	if settings.PendingReset != nil {
		return zero, conflict("A reset is already waiting for the Codex reset date.")
	}
	settings.PendingReset = &PendingReset{RequestID: request.RequestID, RequestedAt: iso(s.clock()), By: principal}
	settings.Revision++
	if err = save(ctx, tx, settings); err != nil {
		return zero, err
	}
	if err = record(ctx, tx, map[string]any{"kind": "reset.requested", "request_id": request.RequestID, "requested_at": settings.PendingReset.RequestedAt, "by": principal}); err != nil {
		return zero, err
	}
	resultBytes, _ := json.Marshal(settings)
	if _, err = tx.ExecContext(ctx, "INSERT INTO usage_requests VALUES (?,?,?)", request.RequestID, string(payload), string(resultBytes)); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return settings, nil
}
func (s *Store) SyncLimits(ctx context.Context, data []byte) (Settings, error) {
	var v limits
	var zero Settings
	if err := strict(data, &v); err != nil {
		return zero, err
	}
	if v.Version != 1 || !fields(data, "weekly") || (v.ResetRequestID != nil && !identity.MatchString(*v.ResetRequestID)) {
		return zero, bad("Invalid usage request.")
	}
	checked, err := parseDate(v.CheckedAt)
	if err != nil {
		return zero, bad("Invalid usage request.")
	}
	now := s.clock()
	if checked.UnixMilli() > now.UnixMilli()+30000 || now.UnixMilli()-checked.UnixMilli() > 120000 {
		return zero, bad("Rate-limit observation is stale or has an invalid clock.")
	}
	var anchor string
	if v.Weekly != nil {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(data, &raw)
		if !fields(raw["weekly"], "resets_at", "window_minutes", "used_percent") || v.Weekly.WindowMinutes != 10080 || v.Weekly.UsedPercent < 0 || v.Weekly.UsedPercent > 100 {
			return zero, bad("Invalid usage request.")
		}
		reset, err := parseDate(v.Weekly.ResetsAt)
		if err != nil {
			return zero, bad("Invalid usage request.")
		}
		if reset.UnixMilli() <= checked.UnixMilli() || reset.UnixMilli() > checked.UnixMilli()+weekMS+300000 {
			return zero, bad("Codex weekly reset date is outside the expected window.")
		}
		anchor = iso(reset)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	settings, err := s.settings(ctx, tx)
	if err != nil {
		return zero, err
	}
	if settings.LimitsCheckedAt != nil && checked.UnixMilli() < millis(settings.LimitsCheckedAt) {
		return zero, conflict("A newer rate-limit observation is already saved.")
	}
	settings.LimitsCheckedAt = ptr(iso(checked))
	settings.LimitsError = nil
	if v.Weekly == nil {
		settings.LimitsError = ptr("Could not verify the Codex weekly reset date. Check the laptop collector and Codex login; pending resets will retry automatically.")
	}
	if v.Weekly != nil {
		settings.LimitsVerifiedAt = ptr(*settings.LimitsCheckedAt)
		if settings.WeeklyAnchorAt == nil || *settings.WeeklyAnchorAt != anchor {
			settings.WeeklyAnchorAt = ptr(anchor)
			settings.Revision++
			if err = record(ctx, tx, map[string]any{"kind": "weekly.synced", "at": *settings.LimitsCheckedAt, "next_reset_at": anchor}); err != nil {
				return zero, err
			}
		}
		pending := settings.PendingReset
		if pending != nil && v.ResetRequestID != nil && *v.ResetRequestID == pending.RequestID && checked.UnixMilli() >= millis(&pending.RequestedAt) {
			settings.ManualResetAt = ptr(pending.RequestedAt)
			settings.PendingReset = nil
			settings.Revision++
			if err = record(ctx, tx, map[string]any{"kind": "reset.completed", "request_id": pending.RequestID, "requested_at": pending.RequestedAt, "by": pending.By, "verified_at": *settings.LimitsCheckedAt, "next_reset_at": anchor}); err != nil {
				return zero, err
			}
		}
	}
	if err = save(ctx, tx, settings); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return settings, nil
}
func (s *Store) Snapshot(ctx context.Context, project *string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	settings, err := s.settings(ctx, s.db)
	if err != nil {
		return nil, err
	}
	since := int64(0)
	var nextReset any
	if settings.WeeklyAnchorAt != nil {
		a := millis(settings.WeeklyAnchorAt)
		since = a
		if now.UnixMilli() < a {
			since = a - weekMS
			nextReset = *settings.WeeklyAnchorAt
		}
	}
	if manual := millis(settings.ManualResetAt); manual > since {
		since = manual
	}
	if since < 0 {
		since = 0
	}
	var count, requested, all int64
	var first, last sql.NullString
	filter := "(? IS NULL OR project=?)"
	err = s.db.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(mode_evidence='requested'),0),min(submitted_at),max(submitted_at) FROM pro_submissions WHERE submitted_ms>=? AND submitted_ms<=? AND "+filter, since, now.UnixMilli(), project, project).Scan(&count, &requested, &first, &last)
	if err != nil {
		return nil, err
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM pro_submissions WHERE submitted_ms<=? AND "+filter, now.UnixMilli(), project, project).Scan(&all); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT project,count(*) FROM pro_submissions WHERE submitted_ms>=? AND submitted_ms<=? AND "+filter+" GROUP BY project ORDER BY count(*) DESC,project", since, now.UnixMilli(), project, project)
	if err != nil {
		return nil, err
	}
	projects := []map[string]any{}
	for rows.Next() {
		var p sql.NullString
		var n int64
		if err = rows.Scan(&p, &n); err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, map[string]any{"project_id": nullable(p), "count": n})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, "SELECT body FROM usage_collectors ORDER BY id")
	if err != nil {
		return nil, err
	}
	collectors := []map[string]any{}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return nil, err
		}
		var c map[string]any
		if err = json.Unmarshal([]byte(body), &c); err != nil {
			rows.Close()
			return nil, err
		}
		at, _ := c["last_sync_at"].(string)
		t, _ := parseDate(at)
		c["stale"] = now.UnixMilli()-t.UnixMilli() > 120000
		collectors = append(collectors, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var period any
	if since != 0 {
		period = iso(time.UnixMilli(since))
	}
	stale := settings.WeeklyAnchorAt == nil || now.UnixMilli() >= millis(settings.WeeklyAnchorAt) || settings.LimitsVerifiedAt == nil || now.UnixMilli()-millis(settings.LimitsVerifiedAt) > 600000
	return map[string]any{"schema_version": 1, "generated_at": iso(now), "settings": settings, "period_started_at": period, "next_weekly_reset_at": nextReset, "reset_date_stale": stale, "count": count, "requested_mode_count": requested, "first_submission_at": nullable(first), "last_submission_at": nullable(last), "all_time_count": all, "projects": projects, "collectors": collectors, "coverage_note": "SessionPilot Pro submissions, including in-flight requests and requests that later fail or are canceled. Pre-send failures and unconfirmed sends are excluded. This count is not OpenAI's remaining allowance."}, nil
}
func nullable(s sql.NullString) any {
	if s.Valid {
		return s.String
	}
	return nil
}
func (s *Store) History(ctx context.Context) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, "SELECT sequence,body FROM usage_changes ORDER BY sequence DESC LIMIT 30")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var body string
		if err = rows.Scan(&seq, &body); err != nil {
			return nil, err
		}
		var v map[string]any
		if err = json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		v["sequence"] = seq
		out = append(out, v)
	}
	return out, rows.Err()
}
