// Package workhistory reads an immutable release-0.31 work archive. It has no
// coordinator, sweeper, launcher, session issuer, or mutation API.
package workhistory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/audit"
	"github.com/lyeith/codexpro/go/internal/wire"
	_ "github.com/mattn/go-sqlite3"
)

type Options struct {
	Clock       func() time.Time
	PacketBytes int
	ContextDir  string
	// PrincipalMap is an explicit current-principal to archived-principal mapping
	// from the operator's import manifest. No implicit ownership reassignment.
	PrincipalMap map[string]string
	Audit        *audit.Journal
	Source       func(context.Context, map[string]any) (map[string]any, error)
}
type Service struct {
	db      *sql.DB
	options Options
	runs    []map[string]any
	byID    map[string]map[string]any
}

// Open is the synchronous immutable reader used by offline archive validation.
// HTTP startup uses NewDeferred and starts it only after the listener serves.
func Open(path string, options ...Options) (*Service, error) {
	return OpenContext(context.Background(), path, options...)
}

// OpenContext performs only archive metadata/schema checks and reads the run
// catalogue. It never hashes, integrity-checks, reconciles, or writes an archive.
// All SQLite statements share the caller's cancellation/deadline.
func OpenContext(ctx context.Context, path string, options ...Options) (*Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var option Options
	if len(options) > 0 {
		option = options[0]
	}
	if option.Clock == nil {
		option.Clock = time.Now
	}
	if option.PacketBytes <= 0 {
		option.PacketBytes = 16 * 1024
	}
	if option.ContextDir == "" {
		option.ContextDir = ".ai-bridge"
	}
	option.PacketBytes = max(4096, min(65536, option.PacketBytes))
	principalMap := map[string]string{}
	for key, value := range option.PrincipalMap {
		principalMap[key] = value
	}
	option.PrincipalMap = principalMap
	s := &Service{options: option, runs: []map[string]any{}, byID: map[string]map[string]any{}}
	if path == "" {
		return s, nil
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("work history requires a regular SQLite archive")
	}
	// Immutable SQLite deliberately ignores WAL. Reject an uncheckpointed copy
	// instead of silently dropping accepted historical transactions.
	if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 {
		return nil, errors.New("work history requires a consistent SQLite backup without a nonempty WAL")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?mode=ro&immutable=1&_query_only=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	s.db = db
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	fail := func(err error) (*Service, error) { db.Close(); return nil, err }
	var readonly int
	if err = db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&readonly); err != nil || readonly != 1 {
		if err == nil {
			err = errors.New("archive connection is not query-only")
		}
		return fail(err)
	}
	var schema string
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='schema'").Scan(&schema)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	if schema != "" && schema != "1" && schema != "2" && schema != "3" {
		return fail(fmt.Errorf("Unsupported work database schema: %s", schema))
	}
	for _, table := range []string{"iterations", "operations", "documents", "events", "work_jobs"} {
		rows, e := db.QueryContext(ctx, "SELECT body FROM "+table+" LIMIT 0")
		if e != nil {
			return fail(e)
		}
		rows.Close()
	}
	rows, err := s.query(ctx, "SELECT body FROM runs ORDER BY rowid DESC")
	if err != nil {
		return fail(err)
	}
	for _, run := range rows {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if str(run["id"]) == "" || str(run["principal_id"]) == "" {
			return fail(errors.New("invalid archived run identity"))
		}
		s.runs = append(s.runs, run)
		s.byID[str(run["id"])] = run
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Service) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
func (s *Service) now() string { return s.options.Clock().UTC().Format("2006-01-02T15:04:05.000Z") }
func (s *Service) principal(value string) string {
	if mapped, ok := s.options.PrincipalMap[value]; ok {
		return mapped
	}
	return value
}
func (s *Service) query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	values := []map[string]any{}
	if s.db == nil {
		return values, nil
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var value map[string]any
		if err = json.Unmarshal([]byte(body), &value); err != nil {
			return nil, fmt.Errorf("invalid archived JSON: %w", err)
		}
		if value == nil {
			return nil, errors.New("invalid archived JSON object")
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
func (s *Service) get(ctx context.Context, table, id string) (map[string]any, error) {
	if table != "iterations" && table != "operations" {
		return nil, errors.New("invalid archive table")
	}
	rows, err := s.query(ctx, "SELECT body FROM "+table+" WHERE id=?", id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}
func (s *Service) Runs(ctx context.Context, projectID string) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, run := range s.runs {
		if projectID == "" || str(run["project_id"]) == projectID {
			out = append(out, clone(run))
		}
	}
	return out, nil
}
func (s *Service) Iteration(ctx context.Context, id string) (map[string]any, error) {
	value, err := s.get(ctx, "iterations", id)
	if err != nil || value == nil {
		return value, err
	}
	return publicIteration(value), nil
}
func (s *Service) OperationCount(ctx context.Context, runID string) (int, error) {
	return s.operationCount(ctx, runID, true)
}
func (s *Service) operationCount(ctx context.Context, runID string, unresolved bool) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	query := "SELECT COUNT(*) FROM operations WHERE run_id=?"
	if unresolved {
		query += " AND json_extract(body,'$.state') IN ('prepared','running','unknown')"
	}
	var count int
	err := s.db.QueryRowContext(ctx, query, runID).Scan(&count)
	return count, err
}
func (s *Service) operations(ctx context.Context, runID string, offset, limit int, reverse bool) ([]map[string]any, error) {
	query := "SELECT json_remove(body,'$.result','$.fingerprint') FROM operations WHERE run_id=? ORDER BY rowid "
	if reverse {
		query += "DESC"
	} else {
		query += "ASC"
	}
	query += " LIMIT ? OFFSET ?"
	return s.query(ctx, query, runID, limit, offset)
}
func (s *Service) documents(ctx context.Context, runID string, manifest bool) ([]map[string]any, error) {
	expression := "d.body"
	if manifest {
		expression = "json_remove(d.body,'$.content')"
	}
	return s.query(ctx, "SELECT "+expression+" FROM documents d WHERE run_id=? AND revision=(SELECT MAX(revision) FROM documents v WHERE v.id=d.id) ORDER BY d.id", runID)
}
func (s *Service) jobs(ctx context.Context, runID string, withDeadline bool) ([]map[string]any, error) {
	rows, err := s.query(ctx, "SELECT body FROM work_jobs WHERE run_id=? ORDER BY rowid", runID)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, job := range rows {
		value := map[string]any{"job_id": job["id"]}
		copyKeys(value, job, "status", "quiescent", "started_at", "finished_at")
		copyKeys(value, obj(job["work"]), "operation_id")
		if withDeadline {
			copyKeys(value, job, "deadline_ms")
		}
		out = append(out, value)
	}
	return out, nil
}

var prefixRE = regexp.MustCompile(`^run_[A-Za-z0-9_-]{8,}$`)
var compactRE = regexp.MustCompile(`^(run_[A-Za-z0-9_-]{8,})(?:…|\.\.\.)([A-Za-z0-9_-]{4,})$`)

func (s *Service) require(principal, id, project string) (map[string]any, error) {
	if exact := s.byID[id]; exact != nil {
		if str(exact["principal_id"]) != principal {
			return nil, wire.Errorf("work_not_found", "Unknown or inaccessible run_id.")
		}
		return clone(exact), nil
	}
	prefix, suffix := id, ""
	match := compactRE.FindStringSubmatch(id)
	if match != nil {
		prefix, suffix = match[1], match[2]
	} else if !prefixRE.MatchString(id) {
		return nil, wire.Errorf("work_not_found", "Unknown or inaccessible run_id. Use the full ID or a unique prefix with at least 8 characters after run_.")
	}
	var found map[string]any
	for _, run := range s.runs {
		runID := str(run["id"])
		if str(run["principal_id"]) != principal || (project != "" && str(run["project_id"]) != project) || !strings.HasPrefix(runID, prefix) {
			continue
		}
		if suffix != "" && (!strings.HasSuffix(runID, suffix) || len(runID) <= len(prefix)+len(suffix)) {
			continue
		}
		if found != nil {
			return nil, wire.Errorf("work_run_ambiguous", "Ambiguous run_id. Use a longer prefix or the full ID.")
		}
		found = run
	}
	if found == nil {
		return nil, wire.Errorf("work_not_found", "Unknown or inaccessible run_id. Use the full ID or a unique prefix with at least 8 characters after run_.")
	}
	return clone(found), nil
}
func str(v any) string { s, _ := v.(string); return s }
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func list(v any) []any {
	switch v := v.(type) {
	case []any:
		return v
	case []map[string]any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	default:
		return []any{}
	}
}
func num(v any) int64 {
	switch v := v.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
func boolean(v any) bool { b, _ := v.(bool); return b }
func clone(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	var copy map[string]any
	_ = json.Unmarshal(b, &copy)
	return copy
}
func copyKeys(dst, src map[string]any, keys ...string) {
	for _, key := range keys {
		if v, ok := src[key]; ok {
			dst[key] = v
		}
	}
}
func publicIteration(value map[string]any) map[string]any {
	value = clone(value)
	delete(value, "token_hash")
	delete(value, "clock")
	delete(value, "principal_id")
	return value
}
