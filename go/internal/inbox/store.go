package inbox

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
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	_ "github.com/mattn/go-sqlite3"
)

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

var identity = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$")

type Question struct {
	Version        integer  "json:\"schema_version\""
	ID             string   "json:\"id\""
	ProjectID      string   "json:\"project_id\""
	Source         string   "json:\"source\""
	Title          string   "json:\"title\""
	Question       string   "json:\"question\""
	Context        string   "json:\"context\""
	Options        []string "json:\"options\""
	Recommendation string   "json:\"recommendation\""
	BlockingScope  string   "json:\"blocking_scope\""
	BlockedWork    []string "json:\"blocked_work\""
	SourceURL      *string  "json:\"source_url,omitempty\""
}
type Answer struct {
	Text      string "json:\"text\""
	At        string "json:\"at\""
	By        string "json:\"by\""
	RequestID string "json:\"request_id\""
}
type Delivery struct {
	Version  integer "json:\"schema_version\""
	Consumer string  "json:\"consumer\""
	Revision integer "json:\"revision\""
	At       string  "json:\"at\""
}
type Item struct {
	Question
	Status     string     "json:\"status\""
	Revision   int64      "json:\"revision\""
	CreatedAt  string     "json:\"created_at\""
	UpdatedAt  string     "json:\"updated_at\""
	Answer     *Answer    "json:\"answer,omitempty\""
	Deliveries []Delivery "json:\"deliveries\""
}
type answerRequest struct {
	Version          integer "json:\"schema_version\""
	RequestID        string  "json:\"request_id\""
	ExpectedRevision integer "json:\"expected_revision\""
	Answer           string  "json:\"answer\""
}
type deliveryRequest struct {
	Version  integer "json:\"schema_version\""
	Consumer string  "json:\"consumer\""
	Revision integer "json:\"revision\""
}
type ListFilter struct {
	Project, Status, Consumer *string
	Limit, Offset             int
}
type ListResult struct {
	Items      []Item "json:\"items\""
	Total      int64  "json:\"total\""
	NextOffset *int   "json:\"next_offset\""
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
	file := filepath.Join(directory, "inbox.sqlite")
	u := url.URL{Scheme: "file", Path: file}
	db, err := sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := "CREATE TABLE IF NOT EXISTS inbox_items (project TEXT NOT NULL,id TEXT NOT NULL,status TEXT NOT NULL,updated TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(project,id));" +
		"CREATE TABLE IF NOT EXISTS inbox_events (sequence INTEGER PRIMARY KEY AUTOINCREMENT,project TEXT NOT NULL,id TEXT NOT NULL,body TEXT NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS inbox_answers (project TEXT NOT NULL,id TEXT NOT NULL,request TEXT NOT NULL,payload TEXT NOT NULL,result TEXT NOT NULL,PRIMARY KEY(project,id,request));"
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(file, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, clock: clock}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func iso(t time.Time) string  { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func strict(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return bad("Invalid inbox payload.")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return bad("Invalid inbox payload.")
	}
	return nil
}
func rawObject(data []byte) map[string]json.RawMessage {
	var v map[string]json.RawMessage
	_ = json.Unmarshal(data, &v)
	return v
}
func isNull(b []byte) bool { return bytes.Equal(bytes.TrimSpace(b), []byte("null")) }
func trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
			return true
		}
		return r >= 0x2000 && r <= 0x200A
	})
}
func length(s string) int { return len(utf16.Encode([]rune(s))) }
func parseQuestion(data []byte) (Question, error) {
	var q Question
	if err := strict(data, &q); err != nil {
		return q, err
	}
	raw := rawObject(data)
	for _, key := range []string{"schema_version", "id", "project_id", "source", "title", "question", "blocking_scope"} {
		if _, ok := raw[key]; !ok || isNull(raw[key]) {
			return q, bad("Invalid inbox payload.")
		}
	}
	for _, key := range []string{"context", "options", "recommendation", "blocked_work", "source_url"} {
		if b, ok := raw[key]; ok && isNull(b) {
			return q, bad("Invalid inbox payload.")
		}
	}
	q.Source = trim(q.Source)
	q.Title = trim(q.Title)
	q.Question = trim(q.Question)
	if q.Options == nil {
		q.Options = []string{}
	}
	if q.BlockedWork == nil {
		q.BlockedWork = []string{}
	}
	if q.Version != 1 || !identity.MatchString(q.ID) || !identity.MatchString(q.ProjectID) || length(q.Source) < 1 || length(q.Source) > 160 || length(q.Title) < 1 || length(q.Title) > 200 || length(q.Question) < 1 || length(q.Question) > 2000 || length(q.Context) > 4000 || length(q.Recommendation) > 2000 || len(q.Options) > 6 || len(q.BlockedWork) > 12 {
		return q, bad("Invalid inbox payload.")
	}
	if q.BlockingScope != "none" && q.BlockingScope != "ticket" && q.BlockingScope != "project" {
		return q, bad("Invalid inbox payload.")
	}
	for i, value := range q.Options {
		value = trim(value)
		if length(value) < 1 || length(value) > 500 {
			return q, bad("Invalid inbox payload.")
		}
		q.Options[i] = value
	}
	for _, value := range q.BlockedWork {
		if length(value) > 200 {
			return q, bad("Invalid inbox payload.")
		}
	}
	if q.SourceURL != nil {
		parsed, err := url.Parse(*q.SourceURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || (!strings.HasPrefix(*q.SourceURL, "http://") && !strings.HasPrefix(*q.SourceURL, "https://")) {
			return q, bad("Invalid inbox payload.")
		}
	}
	return q, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func get(ctx context.Context, q queryRower, project, id string) (Item, bool, error) {
	var body string
	var item Item
	err := q.QueryRowContext(ctx, "SELECT body FROM inbox_items WHERE project=? AND id=?", project, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if err = json.Unmarshal([]byte(body), &item); err != nil {
		return item, false, fmt.Errorf("invalid inbox item: %w", err)
	}
	return item, true, nil
}
func (s *Store) Get(ctx context.Context, project, id string) (Item, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(ctx, s.db, project, id)
}
func (s *Store) save(ctx context.Context, tx *sql.Tx, item Item, kind string, detail any) error {
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO inbox_items VALUES (?,?,?,?,?) ON CONFLICT(project,id) DO UPDATE SET status=excluded.status,updated=excluded.updated,body=excluded.body", item.ProjectID, item.ID, item.Status, item.UpdatedAt, string(body)); err != nil {
		return err
	}
	event := map[string]any{"schema_version": 1, "kind": kind, "at": iso(s.clock()), "project_id": item.ProjectID, "id": item.ID, "revision": item.Revision, "detail": detail}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO inbox_events(project,id,body) VALUES (?,?,?)", item.ProjectID, item.ID, string(b))
	return err
}
func (s *Store) Publish(ctx context.Context, data []byte) (Item, error) {
	q, err := parseQuestion(data)
	if err != nil {
		return Item{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	old, found, err := get(ctx, tx, q.ProjectID, q.ID)
	if err != nil {
		return Item{}, err
	}
	if found {
		previous, err := json.Marshal(old.Question)
		if err != nil {
			return Item{}, err
		}
		current, _ := json.Marshal(q)
		if string(previous) != string(current) {
			return Item{}, conflict("Question ID already exists with different content; read it before posting a new question.")
		}
		return old, nil
	}
	now := iso(s.clock())
	item := Item{Question: q, Status: "pending", Revision: 1, CreatedAt: now, UpdatedAt: now, Deliveries: []Delivery{}}
	if err = s.save(ctx, tx, item, "question.created", q); err != nil {
		return Item{}, err
	}
	if err = tx.Commit(); err != nil {
		return Item{}, err
	}
	return item, nil
}
func (s *Store) Answer(ctx context.Context, project, id string, data []byte, principal string) (Item, error) {
	var a answerRequest
	if err := strict(data, &a); err != nil {
		return Item{}, err
	}
	a.Answer = trim(a.Answer)
	if a.Version != 1 || !identity.MatchString(a.RequestID) || a.ExpectedRevision < 1 || length(a.Answer) < 1 || length(a.Answer) > 8000 {
		return Item{}, bad("Invalid inbox payload.")
	}
	payload, _ := json.Marshal(a)
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	var previous, result string
	err = tx.QueryRowContext(ctx, "SELECT payload,result FROM inbox_answers WHERE project=? AND id=? AND request=?", project, id, a.RequestID).Scan(&previous, &result)
	if err == nil {
		if previous != string(payload) {
			return Item{}, conflict("Answer request ID was already used for different content.")
		}
		var item Item
		err = json.Unmarshal([]byte(result), &item)
		return item, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Item{}, err
	}
	item, found, err := get(ctx, tx, project, id)
	if err != nil {
		return Item{}, err
	}
	if !found {
		return Item{}, &Error{404, "Question not found."}
	}
	if item.Revision != int64(a.ExpectedRevision) {
		return Item{}, conflict("Question changed; refresh before answering.")
	}
	item.Revision++
	item.Status = "answered"
	item.UpdatedAt = iso(s.clock())
	item.Answer = &Answer{Text: a.Answer, At: item.UpdatedAt, By: principal, RequestID: a.RequestID}
	if err = s.save(ctx, tx, item, "question.answered", item.Answer); err != nil {
		return Item{}, err
	}
	resultBytes, _ := json.Marshal(item)
	if _, err = tx.ExecContext(ctx, "INSERT INTO inbox_answers VALUES (?,?,?,?,?)", project, id, a.RequestID, string(payload), string(resultBytes)); err != nil {
		return Item{}, err
	}
	if err = tx.Commit(); err != nil {
		return Item{}, err
	}
	return item, nil
}
func (s *Store) Delivered(ctx context.Context, project, id string, data []byte) (Item, error) {
	var d deliveryRequest
	if err := strict(data, &d); err != nil {
		return Item{}, err
	}
	if d.Version != 1 || !identity.MatchString(d.Consumer) || d.Revision < 1 {
		return Item{}, bad("Invalid inbox payload.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	item, found, err := get(ctx, tx, project, id)
	if err != nil {
		return Item{}, err
	}
	if !found {
		return Item{}, &Error{404, "Question not found."}
	}
	if item.Answer == nil || item.Revision != int64(d.Revision) {
		return Item{}, conflict("Answer revision changed; read the current answer.")
	}
	for _, receipt := range item.Deliveries {
		if receipt.Consumer == d.Consumer && receipt.Revision == d.Revision {
			return item, nil
		}
	}
	kept := []Delivery{}
	for _, receipt := range item.Deliveries {
		if receipt.Consumer != d.Consumer {
			kept = append(kept, receipt)
		}
	}
	if len(kept) >= 50 {
		return Item{}, conflict("Too many consumers.")
	}
	receipt := Delivery{Version: d.Version, Consumer: d.Consumer, Revision: d.Revision, At: iso(s.clock())}
	item.Deliveries = append(kept, receipt)
	if err = s.save(ctx, tx, item, "answer.delivered", receipt); err != nil {
		return Item{}, err
	}
	if err = tx.Commit(); err != nil {
		return Item{}, err
	}
	return item, nil
}
func (s *Store) List(ctx context.Context, f ListFilter) (ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Limit == 0 {
		f.Limit = 100
	}
	args := []any{f.Project, f.Project, f.Status, f.Status, f.Consumer, f.Consumer}
	where := "WHERE (? IS NULL OR project=?) AND (? IS NULL OR status=?) AND (? IS NULL OR status='pending' OR NOT EXISTS (SELECT 1 FROM json_each(json_extract(inbox_items.body,'$.deliveries')) d WHERE json_extract(d.value,'$.consumer')=? AND json_extract(d.value,'$.revision')=json_extract(inbox_items.body,'$.revision')))"
	out := ListResult{Items: []Item{}}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM inbox_items "+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM inbox_items "+where+" ORDER BY status DESC,updated DESC,project,id LIMIT ? OFFSET ?", append(args, f.Limit, f.Offset)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		var item Item
		if err = rows.Scan(&body); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(body), &item); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	next := f.Offset + len(out.Items)
	if int64(next) < out.Total {
		out.NextOffset = &next
	}
	return out, nil
}
func (s *Store) Events(ctx context.Context, project *string, after int64, limit int) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit == 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, "SELECT sequence,body FROM inbox_events WHERE sequence>? AND (? IS NULL OR project=?) ORDER BY sequence LIMIT ?", after, project, project, limit)
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
		var item map[string]any
		if err = json.Unmarshal([]byte(body), &item); err != nil {
			return nil, err
		}
		item["sequence"] = seq
		out = append(out, item)
	}
	return out, rows.Err()
}
