package audit

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/lyeith/codexpro/go/internal/wire"
	_ "github.com/mattn/go-sqlite3"
)

type Journal struct {
	options            Options
	limits             atomic.Pointer[operationalLimits]
	projectMutations   map[string][]projectMutation
	lastProjectChanges map[string]map[string]any
	serverSessionRef   string
	db                 *sql.DB
	lockFile, current  *os.File
	currentName        string
	currentOffset      int64
	mu                 sync.RWMutex
	entries            []entry
	byID               map[string]entry
	byFingerprint      map[string]entry
	state              state
	deduplicated       int64
	fatal              error
	queue              chan request
	admitMu            sync.RWMutex
	closed             bool
	wg                 sync.WaitGroup
	inflightMu         sync.RWMutex
	inflight           map[string]InFlightAction
}

func Open(options Options) (*Journal, error) {
	if options.Clock == nil {
		options.Clock = timeNow
	}
	if options.Redact == nil {
		options.Redact = wire.Redact
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = 8 * 1024 * 1024
	}
	if options.RetainActions <= 0 {
		options.RetainActions = 350
	}
	if options.MaxOutputBytes <= 0 {
		options.MaxOutputBytes = 120000
	}
	if options.SegmentBytes <= 0 {
		options.SegmentBytes = 1024 * 1024
	}
	j := &Journal{options: options, serverSessionRef: randomID("srv_"), entries: []entry{}, byID: map[string]entry{}, byFingerprint: map[string]entry{}, inflight: map[string]InFlightAction{}, queue: make(chan request, 256)}
	j.SetLimits(options.MaxBytes, options.RetainActions, options.MaxOutputBytes)
	if !options.Enabled {
		return j, nil
	}
	if err := os.MkdirAll(options.Directory, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(options.Directory, "writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("audit journal already has a writer: %w", err)
	}
	j.lockFile = lock
	dbFile := filepath.Join(options.Directory, "index.sqlite")
	u := url.URL{Scheme: "file", Path: dbFile}
	j.db, err = sql.Open("sqlite3", u.String()+"?_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		lock.Close()
		return nil, err
	}
	j.db.SetMaxOpenConns(1)
	schema := "CREATE TABLE IF NOT EXISTS entries(sequence INTEGER PRIMARY KEY,action_id TEXT NOT NULL UNIQUE,project_id TEXT NOT NULL,bucket TEXT NOT NULL,fingerprint TEXT NOT NULL,segment TEXT NOT NULL,offset INTEGER NOT NULL,length INTEGER NOT NULL);" +
		"CREATE INDEX IF NOT EXISTS entries_fingerprint ON entries(fingerprint);" +
		"CREATE INDEX IF NOT EXISTS entries_project_sequence ON entries(project_id,sequence);" +
		"CREATE TABLE IF NOT EXISTS segments(name TEXT PRIMARY KEY,committed_bytes INTEGER NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS retired_segments(name TEXT PRIMARY KEY);" +
		"CREATE TABLE IF NOT EXISTS completion_receipts(fingerprint TEXT PRIMARY KEY,action_id TEXT NOT NULL,sequence INTEGER NOT NULL);" +
		"CREATE TABLE IF NOT EXISTS metadata(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);"
	if _, err = j.db.Exec(schema); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = os.Chmod(dbFile, 0600); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	var body string
	err = j.db.QueryRow("SELECT body FROM metadata WHERE id=1").Scan(&body)
	if err == nil {
		err = json.Unmarshal([]byte(body), &j.state)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		j.db.Close()
		lock.Close()
		return nil, fmt.Errorf("invalid audit metadata: %w", err)
	}
	if err = j.removeRetired(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = j.cleanupRewrites(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = j.recover(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = j.loadEntries(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = j.validateIndex(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	if err = j.openCurrent(); err != nil {
		j.db.Close()
		lock.Close()
		return nil, err
	}
	j.wg.Add(1)
	go j.run()
	return j, nil
}
func (j *Journal) checkpoint(stage string) error {
	if j.options.Fault != nil {
		return j.options.Fault(stage)
	}
	return nil
}
func syncDirectory(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func (j *Journal) newSegment() error {
	if j.current != nil {
		if err := j.current.Sync(); err != nil {
			return err
		}
		if err := j.current.Close(); err != nil {
			return err
		}
		j.current = nil
	}
	name := fmt.Sprintf("segment-%020d-%s.jsonl", j.state.Latest+1, randomID(""))
	f, err := os.OpenFile(filepath.Join(j.options.Directory, name), os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if err = syncDirectory(j.options.Directory); err != nil {
		f.Close()
		return err
	}
	if _, err = j.db.Exec("INSERT INTO segments VALUES (?,0)", name); err != nil {
		f.Close()
		return err
	}
	j.current, j.currentName, j.currentOffset = f, name, 0
	return nil
}
func (j *Journal) openCurrent() error {
	var name string
	err := j.db.QueryRow("SELECT name FROM segments WHERE name LIKE 'segment-%' ORDER BY name DESC LIMIT 1").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return j.newSegment()
	}
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(j.options.Directory, name), os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	j.current, j.currentName, j.currentOffset = f, name, stat.Size()
	if stat.Size() >= j.options.SegmentBytes {
		return j.newSegment()
	}
	return nil
}
func (j *Journal) loadEntries() error {
	rows, err := j.db.Query("SELECT sequence,action_id,project_id,bucket,fingerprint,segment,offset,length FROM entries ORDER BY sequence")
	if err != nil {
		return err
	}
	defer rows.Close()
	j.entries = []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.Sequence, &e.ActionID, &e.ProjectID, &e.Bucket, &e.Fingerprint, &e.Segment, &e.Offset, &e.Length); err != nil {
			return err
		}
		j.entries = append(j.entries, e)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	j.rebuildMaps()
	return nil
}
func (j *Journal) rebuildMaps() {
	j.byID = make(map[string]entry, len(j.entries))
	j.byFingerprint = make(map[string]entry, len(j.entries))
	for _, e := range j.entries {
		j.byID[e.ActionID] = e
		if e.Fingerprint != "" {
			j.byFingerprint[e.Fingerprint] = e
		}
	}
	j.pruneProjectChanges()
}
func insert(ctx context.Context, tx *sql.Tx, e entry) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO entries VALUES (?,?,?,?,?,?,?,?)", e.Sequence, e.ActionID, e.ProjectID, e.Bucket, e.Fingerprint, e.Segment, e.Offset, e.Length)
	if err == nil && e.Completion && e.Fingerprint != "" {
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO completion_receipts VALUES (?,?,?)", e.Fingerprint, e.ActionID, e.Sequence)
	}
	return err
}
func writeState(ctx context.Context, tx *sql.Tx, st state) error {
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO metadata VALUES (1,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", string(body))
	return err
}
func (j *Journal) recover() error {
	files, err := filepath.Glob(filepath.Join(j.options.Directory, "segment-*.jsonl"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for fileIndex, file := range files {
		name := filepath.Base(file)
		var offset int64
		err = j.db.QueryRow("SELECT committed_bytes FROM segments WHERE name=?", name).Scan(&offset)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = j.db.Exec("INSERT INTO segments VALUES (?,0)", name); err != nil {
				return err
			}
			offset = 0
		} else if err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		stat, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if offset > stat.Size() {
			f.Close()
			return errors.New("audit segment is shorter than its committed index boundary")
		}
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		reader := bufio.NewReader(f)
		added := []entry{}
		nextOffset := offset
		for {
			line, readErr := reader.ReadBytes('\n')
			if readErr == io.EOF && len(line) > 0 {
				if fileIndex != len(files)-1 {
					f.Close()
					return errors.New("incomplete record inside a sealed audit segment")
				}
				if err = f.Truncate(nextOffset); err != nil {
					f.Close()
					return err
				}
				if err = f.Sync(); err != nil {
					f.Close()
					return err
				}
				j.state.Malformed++
				if err = j.recoveryReceipt(name, nextOffset, int64(len(line))); err != nil {
					f.Close()
					return err
				}
				break
			}
			if len(line) == 0 && readErr == io.EOF {
				break
			}
			if readErr != nil {
				f.Close()
				return readErr
			}
			action, parseErr := parseAction(line)
			if parseErr != nil {
				j.state.Malformed++
				j.state.Gap = true
				nextOffset += int64(len(line))
				continue
			}
			seq := int64(number(action["sequence"]))
			id, _ := action["action_id"].(string)
			var exists int
			err = j.db.QueryRow("SELECT count(*) FROM entries WHERE sequence=? OR action_id=?", seq, id).Scan(&exists)
			if err != nil {
				f.Close()
				return err
			}
			if exists == 0 {
				if seq != j.state.Latest+1 {
					j.state.Gap = true
				}
				if seq <= j.state.Latest {
					j.state.Gap = true
					nextOffset += int64(len(line))
					continue
				}
				e := entryFrom(action, name, nextOffset, int64(len(line)))
				added = append(added, e)
				j.state.Latest = seq
			}
			nextOffset += int64(len(line))
		}
		f.Close()
		tx, err := j.db.BeginTx(context.Background(), nil)
		if err != nil {
			return err
		}
		for _, e := range added {
			if err = insert(context.Background(), tx, e); err != nil {
				tx.Rollback()
				return err
			}
		}
		if _, err = tx.Exec("UPDATE segments SET committed_bytes=? WHERE name=?", nextOffset, name); err != nil {
			tx.Rollback()
			return err
		}
		if err = writeState(context.Background(), tx, j.state); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
func (j *Journal) recoveryReceipt(segment string, offset, removed int64) error {
	f, err := os.OpenFile(filepath.Join(j.options.Directory, "recovery.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	body, _ := json.Marshal(map[string]any{"kind": "torn_tail_truncated", "segment": segment, "offset": offset, "removed_bytes": removed, "at": timestamp(j.options.Clock())})
	if _, err = f.Write(append(body, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func parseAction(line []byte) (Action, error) {
	var a Action
	if err := json.Unmarshal(bytes.TrimSpace(line), &a); err != nil {
		return nil, err
	}
	seq := number(a["sequence"])
	schema, _ := a["schema_version"].(string)
	id, _ := a["action_id"].(string)
	tool, _ := a["tool_name"].(string)
	status, _ := a["status"].(string)
	namespace, _ := a["namespace"].(string)
	if schema != "1.0" || (namespace != "" && namespace != "debug") || seq < 1 || seq != float64(int64(seq)) || seq > 9007199254740991 || id == "" || tool == "" || !validStatus(status) {
		return nil, errors.New("invalid action record")
	}
	a["namespace"] = "debug"
	return a, nil
}
func entryFrom(a Action, segment string, offset, length int64) entry {
	id, _ := a["action_id"].(string)
	project, _ := a["project_id"].(string)
	fp, _ := a["request_fingerprint"].(string)
	return entry{Sequence: int64(number(a["sequence"])), ActionID: id, ProjectID: project, Bucket: bucket(a), Fingerprint: fp, Segment: segment, Offset: offset, Length: length, Completion: a["tool_name"] == "bash_job"}
}
func (j *Journal) readEntry(e entry) (Action, error) {
	f, err := os.Open(filepath.Join(j.options.Directory, e.Segment))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if e.Length < 1 || e.Length > 131072 {
		return nil, errors.New("invalid audit entry length")
	}
	body := make([]byte, int(e.Length))
	if _, err = f.ReadAt(body, e.Offset); err != nil {
		return nil, err
	}
	a, err := parseAction(body)
	if err != nil {
		return nil, err
	}
	if int64(number(a["sequence"])) != e.Sequence || a["action_id"] != e.ActionID {
		return nil, errors.New("audit index points to a different record")
	}
	return a, nil
}
func (j *Journal) writeBatch(batch []request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	responses := make([]response, len(batch))
	for i, r := range batch {
		responses[i].receipt = Receipt{Enabled: true, Status: text(r.action["status"])}
	}
	finish := func(err error) {
		for i, r := range batch {
			if responses[i].err == nil && err != nil {
				responses[i].err = err
				responses[i].receipt.Recorded = false
			}
			r.reply <- responses[i]
		}
	}
	if j.fatal != nil {
		finish(j.fatal)
		return
	}
	if j.state.Gap {
		finish(errors.New("audit journal contains an unresolved gap"))
		return
	}
	if err := j.checkpoint("before_append"); err != nil {
		finish(err)
		return
	}
	all := append([]entry(nil), j.entries...)
	added := []entry{}
	addedActions := []Action{}
	pendingFP := map[string]entry{}
	touched := map[string]int64{}
	st := j.state
	for i, r := range batch {
		action := r.action
		action["sequence"] = st.Latest + 1
		fp, _ := action["request_fingerprint"].(string)
		existing, found := j.byFingerprint[fp]
		if !found && fp != "" && action["tool_name"] == "bash_job" {
			lookupErr := j.db.QueryRow("SELECT action_id,sequence FROM completion_receipts WHERE fingerprint=?", fp).Scan(&existing.ActionID, &existing.Sequence)
			if lookupErr == nil {
				found = true
			} else if !errors.Is(lookupErr, sql.ErrNoRows) {
				responses[i].err = lookupErr
				continue
			}
		}
		if !found {
			existing, found = pendingFP[fp]
		}
		if fp != "" && found {
			j.deduplicated++
			responses[i].receipt.Duplicate = true
			responses[i].receipt.ActionID = existing.ActionID
			responses[i].receipt.Sequence = existing.Sequence
			continue
		}
		serialized, err := serializeAction(action)
		if err != nil {
			responses[i].err = err
			continue
		}
		if j.currentOffset >= j.options.SegmentBytes {
			j.state.Latest = st.Latest
			if err = j.newSegment(); err != nil {
				j.fatal = err
				finish(err)
				return
			}
		}
		offset := j.currentOffset
		n, err := j.current.Write(serialized)
		j.currentOffset += int64(n)
		if err != nil || n != len(serialized) {
			if err == nil {
				err = io.ErrShortWrite
			}
			j.fatal = err
			finish(err)
			return
		}
		e := entryFrom(action, j.currentName, offset, int64(n))
		all = append(all, e)
		added = append(added, e)
		addedActions = append(addedActions, action)
		touched[j.currentName] = j.currentOffset
		st.Latest = e.Sequence
		if fp != "" {
			pendingFP[fp] = e
		}
		responses[i].receipt.Recorded = true
		responses[i].receipt.ActionID = e.ActionID
		responses[i].receipt.Sequence = e.Sequence
	}
	if len(added) == 0 {
		finish(nil)
		return
	}
	if err := j.checkpoint("after_append"); err != nil {
		j.fatal = err
		finish(err)
		return
	}
	if err := j.current.Sync(); err != nil {
		j.fatal = err
		finish(err)
		return
	}
	if err := j.checkpoint("after_sync"); err != nil {
		j.fatal = err
		finish(err)
		return
	}
	retained, dropped := selectRetained(all, j.optionsWithLimits())
	if len(dropped) > 0 {
		st.Rotation++
		st.CompactedAt = timestamp(j.options.Clock())
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		j.fatal = err
		finish(err)
		return
	}
	for _, e := range added {
		if err = insert(context.Background(), tx, e); err != nil {
			break
		}
	}
	if err == nil {
		for _, e := range dropped {
			if _, err = tx.Exec("DELETE FROM entries WHERE sequence=?", e.Sequence); err != nil {
				break
			}
		}
	}
	if err == nil {
		for name, offset := range touched {
			if _, err = tx.Exec("UPDATE segments SET committed_bytes=? WHERE name=?", offset, name); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = writeState(context.Background(), tx, st)
	}
	if err == nil {
		err = j.checkpoint("before_index_commit")
	}
	if err != nil {
		tx.Rollback()
		j.fatal = err
		finish(err)
		return
	}
	if err = tx.Commit(); err != nil {
		j.fatal = err
		finish(err)
		return
	}
	j.entries, j.state = retained, st
	for _, action := range addedActions {
		j.cacheProjectAction(action)
	}
	j.rebuildMaps()
	if err = j.checkpoint("after_index_commit"); err != nil {
		j.fatal = err
		finish(err)
		return
	}
	finish(nil)
	// Maintenance touches at most one sealed segment after the receipt is durable.
	if err = j.maintenance(); err != nil {
		j.fatal = err
	}
}
func selectRetained(all []entry, options Options) ([]entry, []entry) {
	if len(all) <= 1 {
		return all, nil
	}
	counts := map[string]int{}
	var logicalBytes int64
	exceeds := false
	for _, e := range all {
		counts[e.Bucket]++
		logicalBytes += e.Length
		if counts[e.Bucket] > options.RetainActions {
			exceeds = true
		}
	}
	if !exceeds && logicalBytes <= options.MaxBytes {
		return all, nil
	}
	counts = map[string]int{}
	candidates := []entry{}
	for i := len(all) - 1; i >= 0; i-- {
		e := all[i]
		if counts[e.Bucket] >= options.RetainActions {
			continue
		}
		counts[e.Bucket]++
		candidates = append(candidates, e)
	}
	sort.Slice(candidates, func(i, k int) bool { return candidates[i].Sequence < candidates[k].Sequence })
	var candidateBytes int64
	for _, e := range candidates {
		candidateBytes += e.Length
	}
	target := options.MaxBytes * 8 / 10
	if target < 1 {
		target = 1
	}
	kept := candidates
	if logicalBytes > options.MaxBytes && candidateBytes > target {
		buckets := map[string][]entry{}
		order := []string{}
		for i := len(candidates) - 1; i >= 0; i-- {
			e := candidates[i]
			if _, ok := buckets[e.Bucket]; !ok {
				order = append(order, e.Bucket)
			}
			buckets[e.Bucket] = append(buckets[e.Bucket], e)
		}
		selected := map[int64]bool{}
		blocked := map[string]bool{}
		var used int64
		for depth := 0; depth < len(candidates); depth++ {
			any := false
			for _, bucket := range order {
				if blocked[bucket] || depth >= len(buckets[bucket]) {
					continue
				}
				any = true
				e := buckets[bucket][depth]
				if len(selected) == 0 || used+e.Length <= target {
					selected[e.Sequence] = true
					used += e.Length
				} else {
					blocked[bucket] = true
				}
			}
			if !any {
				break
			}
		}
		kept = []entry{}
		for _, e := range candidates {
			if selected[e.Sequence] {
				kept = append(kept, e)
			}
		}
	}
	selected := map[int64]bool{}
	for _, e := range kept {
		selected[e.Sequence] = true
	}
	dropped := []entry{}
	for _, e := range all {
		if !selected[e.Sequence] {
			dropped = append(dropped, e)
		}
	}
	return kept, dropped
}
func (j *Journal) reclaimEmpty() {
	keep := map[string]bool{j.currentName: true}
	for _, e := range j.entries {
		keep[e.Segment] = true
	}
	rows, err := j.db.Query("SELECT name FROM segments")
	if err != nil {
		return
	}
	remove := []string{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && !keep[name] {
			remove = append(remove, name)
		}
	}
	rows.Close()
	if err = j.retire(remove); err != nil {
		j.fatal = err
	}
}
func bucket(a Action) string {
	scope, _ := a["audit_scope"].(string)
	tool, _ := a["tool_name"].(string)
	if scope == "server" {
		return "server"
	}
	if scope == "unattributed" || (strings.HasPrefix(tool, "work_") && scope == "") {
		return "unattributed"
	}
	if project, _ := a["project_id"].(string); project != "" {
		return "project:" + project
	}
	return "unattributed"
}
