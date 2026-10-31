package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// retire removes files only after no durable index references them. The
// tombstone makes a crash between the index commit and unlink unambiguous.
func (j *Journal) retire(names []string) error {
	if len(names) == 0 {
		return nil
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err = tx.Exec("INSERT OR IGNORE INTO retired_segments VALUES (?)", name); err != nil {
			break
		}
		if _, err = tx.Exec("DELETE FROM segments WHERE name=?", name); err != nil {
			break
		}
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return j.removeRetired()
}
func (j *Journal) removeRetired() error {
	rows, err := j.db.Query("SELECT name FROM retired_segments")
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		if filepath.Base(name) != name {
			return errors.New("unsafe retired audit segment name")
		}
		if err = os.Remove(filepath.Join(j.options.Directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(names) > 0 {
		if err = syncDirectory(j.options.Directory); err != nil {
			return err
		}
	}
	for _, name := range names {
		if _, err = j.db.Exec("DELETE FROM retired_segments WHERE name=?", name); err != nil {
			return err
		}
	}
	return nil
}

// maintenance rewrites at most one sealed segment per durable writer batch.
// Only a segment with at least 50% dead bytes is selected, bounding rewrite
// amplification and preventing a quiet project's last record pinning large
// obsolete hot-project history indefinitely.
func (j *Journal) maintenance() error {
	j.reclaimEmpty()
	type candidate struct {
		name        string
		bytes, live int64
	}
	bySegment := map[string][]entry{}
	for _, e := range j.entries {
		bySegment[e.Segment] = append(bySegment[e.Segment], e)
	}
	rows, err := j.db.Query("SELECT name,committed_bytes FROM segments")
	if err != nil {
		return err
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.name, &c.bytes); err != nil {
			rows.Close()
			return err
		}
		if c.name == j.currentName {
			continue
		}
		for _, e := range bySegment[c.name] {
			c.live += e.Length
		}
		if c.live > 0 && c.bytes >= c.live*2 {
			candidates = append(candidates, c)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].bytes-candidates[a].live > candidates[b].bytes-candidates[b].live
	})
	c := candidates[0]
	name := "compact-" + randomID("") + ".jsonl"
	temporary := ".rewrite-" + randomID("") + ".tmp"
	f, err := os.OpenFile(filepath.Join(j.options.Directory, temporary), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cleanup := func() { f.Close(); _ = os.Remove(filepath.Join(j.options.Directory, temporary)) }
	moved := []entry{}
	var offset int64
	for _, e := range bySegment[c.name] {
		a, readErr := j.readEntry(e)
		if readErr != nil {
			cleanup()
			return readErr
		}
		b, encodeErr := serializeAction(a)
		if encodeErr != nil {
			cleanup()
			return encodeErr
		}
		n, writeErr := f.Write(b)
		if writeErr != nil || n != len(b) {
			cleanup()
			return errors.Join(writeErr, errors.New("short audit compaction write"))
		}
		e.Segment, e.Offset, e.Length = name, offset, int64(n)
		moved = append(moved, e)
		offset += int64(n)
	}
	if err = f.Sync(); err != nil {
		cleanup()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(j.options.Directory, temporary), filepath.Join(j.options.Directory, name)); err != nil {
		return err
	}
	if err = syncDirectory(j.options.Directory); err != nil {
		return err
	}
	if err = j.checkpoint("compaction_before_index"); err != nil {
		return err
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO segments VALUES (?,?)", name, offset); err == nil {
		for _, e := range moved {
			if _, err = tx.Exec("UPDATE entries SET segment=?,offset=?,length=? WHERE sequence=?", e.Segment, e.Offset, e.Length, e.Sequence); err != nil {
				break
			}
		}
	}
	if err == nil {
		_, err = tx.Exec("INSERT OR IGNORE INTO retired_segments VALUES (?)", c.name)
	}
	if err == nil {
		_, err = tx.Exec("DELETE FROM segments WHERE name=?", c.name)
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	replacements := map[int64]entry{}
	for _, e := range moved {
		replacements[e.Sequence] = e
	}
	for i, e := range j.entries {
		if replacement, ok := replacements[e.Sequence]; ok {
			j.entries[i] = replacement
		}
	}
	j.rebuildMaps()
	if err = j.checkpoint("compaction_after_index"); err != nil {
		return err
	}
	return j.removeRetired()
}
func (j *Journal) validateIndex() error {
	corrupt := false
	j.projectMutations = map[string][]projectMutation{}
	for _, e := range j.entries {
		action, err := j.readEntry(e)
		if err != nil {
			corrupt = true
			j.state.Malformed++
		} else {
			j.cacheProjectAction(action)
		}
	}
	if corrupt {
		j.state.Gap = true
	}
	retained, dropped := selectRetained(j.entries, j.optionsWithLimits())
	if len(dropped) > 0 {
		j.state.Rotation++
		j.state.CompactedAt = timestamp(j.options.Clock())
	}
	tx, err := j.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	for _, e := range dropped {
		if _, err = tx.Exec("DELETE FROM entries WHERE sequence=?", e.Sequence); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err = writeState(context.Background(), tx, j.state); err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	j.entries = retained
	j.rebuildMaps()
	return nil
}

// Discard only unreferenced completed rewrite files. An interrupted rewrite
// cannot replace or advance any committed source record.
func (j *Journal) cleanupRewrites() error {
	for _, pattern := range []string{"compact-*.jsonl", ".rewrite-*.tmp"} {
		paths, err := filepath.Glob(filepath.Join(j.options.Directory, pattern))
		if err != nil {
			return err
		}
		for _, path := range paths {
			var present int
			err = j.db.QueryRow("SELECT count(*) FROM segments WHERE name=?", filepath.Base(path)).Scan(&present)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if present == 0 {
				if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove unfinished audit rewrite: %w", err)
				}
			}
		}
	}
	return syncDirectory(j.options.Directory)
}
