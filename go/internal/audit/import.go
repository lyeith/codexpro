package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type ImportResult struct {
	Imported       bool   `json:"imported"`
	SourceSHA256   string `json:"source_sha256"`
	ActionCount    int    `json:"action_count"`
	LatestSequence int64  `json:"latest_sequence"`
	GapDetected    bool   `json:"gap_detected"`
}
type legacyIndex struct {
	Schema      string `json:"schema_version"`
	Journal     string `json:"journal_ref"`
	Rotation    int64  `json:"rotation_count"`
	Earliest    int64  `json:"retained_from_sequence"`
	Dropped     int64  `json:"dropped_through_sequence"`
	Latest      int64  `json:"latest_sequence"`
	CompactedAt string `json:"compacted_at"`
	Mode        string `json:"retention_mode"`
	Through     int64  `json:"compacted_through_sequence"`
	Count       int    `json:"retained_entry_count"`
	Digest      string `json:"retained_entry_digest"`
	Gaps        int64  `json:"planned_gap_count"`
}

// ImportLegacy reads a consistent private copy of the release-0.31 journal and
// sidecar. It never writes the source and only installs into an empty journal.
// Source sequence/action identities survive; unexplained gaps remain explicit.
func (j *Journal) ImportLegacy(ctx context.Context, source, indexPath string) (ImportResult, error) {
	var result ImportResult
	if !j.Enabled() {
		return result, errors.New("cannot import into a disabled audit journal")
	}
	f, err := os.Open(source)
	if err != nil {
		return result, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return result, err
	}
	if !before.Mode().IsRegular() || before.Size() > 64*1024*1024 {
		return result, errors.New("legacy audit import requires a regular journal of at most 64 MiB")
	}
	hash := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, hash))
	scanner.Buffer(make([]byte, 131072), 131073)
	actions := []Action{}
	var latest, malformed int64
	gap := false
	ids := map[string]bool{}
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		a, parseErr := parseAction(line)
		if parseErr != nil {
			malformed++
			gap = true
			continue
		}
		sequence := int64(number(a["sequence"]))
		id := text(a["action_id"])
		if sequence <= latest || ids[id] {
			malformed++
			gap = true
			continue
		}
		if latest > 0 && sequence != latest+1 {
			gap = true
		}
		ids[id] = true
		latest = sequence
		actions = append(actions, a)
	}
	if err = scanner.Err(); err != nil {
		return result, err
	}
	after, err := f.Stat()
	if err != nil {
		return result, err
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return result, errors.New("legacy journal changed during import; copy a consistent source snapshot first")
	}
	result.SourceSHA256 = hex.EncodeToString(hash.Sum(nil))
	var index legacyIndex
	hasIndex := false
	if indexPath != "" {
		data, readErr := os.ReadFile(indexPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return result, readErr
		}
		if readErr == nil {
			if len(data) > 1024*1024 || json.Unmarshal(data, &index) != nil {
				return result, errors.New("invalid legacy audit retention sidecar")
			}
			hasIndex = true
		}
	}
	earliest := int64(0)
	if len(actions) > 0 {
		earliest = int64(number(actions[0]["sequence"]))
	}
	if hasIndex {
		if index.Schema != "1.0" || index.Journal != "codexpro://actions" || index.Latest < latest || index.Earliest != earliest || index.Dropped != max(int64(0), earliest-1) {
			gap = true
		}
		if index.Mode == "per_project" {
			digestHash := sha256.New()
			count := 0
			var first, last int64
			for _, a := range actions {
				seq := int64(number(a["sequence"]))
				if seq > index.Through {
					break
				}
				if first == 0 {
					first = seq
				}
				last = seq
				count++
				fmt.Fprintf(digestHash, "%d\x00%s\n", seq, text(a["action_id"]))
			}
			proved := index.Schema == "1.0" && index.Journal == "codexpro://actions" && index.Earliest == earliest && index.Dropped == max(int64(0), earliest-1) && index.Latest >= latest && count == index.Count && last == index.Through && index.Gaps == last-first+1-int64(count) && index.Digest == hex.EncodeToString(digestHash.Sum(nil))
			if proved && malformed == 0 {
				gap = false
				prev := index.Through
				for _, a := range actions {
					seq := int64(number(a["sequence"]))
					if seq <= index.Through {
						continue
					}
					if seq != prev+1 {
						gap = true
					}
					prev = seq
				}
			} else {
				gap = true
			}
		} else if earliest > 0 && index.Dropped != earliest-1 {
			gap = true
		}
		if index.Latest > latest {
			gap = true
			latest = index.Latest
		}
	} else if earliest > 1 {
		gap = true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err = j.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS imports(sha256 TEXT PRIMARY KEY,body TEXT NOT NULL)"); err != nil {
		return result, err
	}
	var prior string
	if err = j.db.QueryRowContext(ctx, "SELECT body FROM imports WHERE sha256=?", result.SourceSHA256).Scan(&prior); err == nil {
		if json.Unmarshal([]byte(prior), &result) != nil {
			return result, errors.New("invalid import receipt")
		}
		result.Imported = false
		return result, nil
	}
	if j.state.Latest != 0 || len(j.entries) != 0 || j.currentOffset != 0 {
		return result, errors.New("legacy audit import requires an empty destination")
	}
	entries := []entry{}
	segments := map[string]int64{}
	var output *os.File
	var name string
	var offset int64
	closeOutput := func() error {
		if output == nil {
			return nil
		}
		if e := output.Sync(); e != nil {
			output.Close()
			output = nil
			return e
		}
		e := output.Close()
		output = nil
		segments[name] = offset
		return e
	}
	for _, a := range actions {
		if err = ctx.Err(); err != nil {
			if output != nil {
				output.Close()
			}
			return result, err
		}
		if output == nil || offset >= j.options.SegmentBytes {
			if err = closeOutput(); err != nil {
				return result, err
			}
			name = "compact-import-" + randomID("") + ".jsonl"
			offset = 0
			output, err = os.OpenFile(filepath.Join(j.options.Directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return result, err
			}
		}
		body, encodeErr := serializeAction(a)
		if encodeErr != nil {
			output.Close()
			return result, encodeErr
		}
		n, writeErr := output.Write(body)
		if writeErr != nil || n != len(body) {
			output.Close()
			return result, errors.Join(writeErr, io.ErrShortWrite)
		}
		entries = append(entries, entryFrom(a, name, offset, int64(n)))
		offset += int64(n)
	}
	if err = closeOutput(); err != nil {
		return result, err
	}
	if err = syncDirectory(j.options.Directory); err != nil {
		return result, err
	}
	if err = j.checkpoint("import_before_index"); err != nil {
		return result, err
	}
	st := state{Latest: latest, Rotation: index.Rotation, CompactedAt: index.CompactedAt, Malformed: malformed, Gap: gap}
	retained, dropped := selectRetained(entries, j.options)
	if len(dropped) > 0 {
		st.Rotation++
		st.CompactedAt = timestamp(j.options.Clock())
	}
	result.Imported = true
	result.ActionCount = len(retained)
	result.LatestSequence = latest
	result.GapDetected = gap
	receipt, _ := json.Marshal(result)
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	for name, size := range segments {
		if _, err = tx.ExecContext(ctx, "INSERT INTO segments VALUES (?,?)", name, size); err != nil {
			break
		}
	}
	if err == nil {
		for _, e := range retained {
			if err = insert(ctx, tx, e); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = writeState(ctx, tx, st)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO imports VALUES (?,?)", result.SourceSHA256, string(receipt))
	}
	if err != nil {
		tx.Rollback()
		return ImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	j.entries, j.state = retained, st
	j.rebuildMaps()
	return result, nil
}
