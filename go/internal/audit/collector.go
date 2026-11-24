package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"time"
)

// CollectorEventTimes describes only the currently retained journal. A nil
// timestamp means no retained observation, not that a label was never active.
// It deliberately carries no receipt, project, request or identity metadata.
type CollectorEventTimes struct {
	Known  bool
	Labels []CollectorLabelTime
}

type CollectorLabelTime struct {
	Label string
	Last  *time.Time
}

type collectorRecord struct {
	entry entry
	label int
	last  time.Time
}

type collectorSegment struct {
	info        os.FileInfo
	changed     int64
	prefixBytes int64
	digest      [sha256.Size]byte
	records     map[int64]collectorRecord
}

type collectorSnapshot struct {
	version  uint64
	segments map[string]collectorSegment
	last     [9]time.Time
	present  [9]bool
}

// CollectEventTimes caches parsed evidence only while both the committed
// retained set and its segment files are unchanged. Sequence order is not event
// time order: delayed completions can be appended after newer calls. Missing,
// damaged or unavailable retained evidence remains unknown.
func (j *Journal) CollectEventTimes(ctx context.Context, observed time.Time) CollectorEventTimes {
	unknown := CollectorEventTimes{}
	if ctx.Err() != nil || observed.IsZero() || !j.options.Enabled || !j.admitMu.TryRLock() {
		return unknown
	}
	defer j.admitMu.RUnlock()
	if j.closed || !j.mu.TryRLock() {
		return unknown
	}
	defer j.mu.RUnlock()
	if j.fatal != nil || j.state.Gap || j.state.Malformed != 0 {
		return unknown
	}
	snapshot := j.collectorCache.Load()
	if snapshot == nil || !collectorCacheSupported || snapshot.version != j.entryVersion || !j.collectorFilesUnchanged(ctx, snapshot.segments) {
		snapshot = j.loadCollectorSnapshot(ctx, snapshot)
		if snapshot != nil {
			j.collectorCache.Store(snapshot)
		}
	}
	if snapshot == nil || ctx.Err() != nil {
		return unknown
	}
	// A cached event can still be in the future relative to this caller. Check
	// observation time on every call, even if a previous caller saw a later time.
	for i, last := range snapshot.last {
		if snapshot.present[i] && last.After(observed) {
			return unknown
		}
	}
	labels := make([]CollectorLabelTime, len(snapshot.last))
	for i, last := range snapshot.last {
		labels[i].Label = "SP" + string(rune('1'+i))
		if snapshot.present[i] {
			at := last
			labels[i].Last = &at
		}
	}
	if ctx.Err() != nil {
		return unknown
	}
	return CollectorEventTimes{Known: true, Labels: labels}
}

func (j *Journal) collectorSegment(name string) (collectorSegment, bool) {
	info, err := os.Stat(filepath.Join(j.options.Directory, name))
	if err != nil || !info.Mode().IsRegular() {
		return collectorSegment{}, false
	}
	return collectorSegment{info: info, changed: collectorChangeTime(info)}, true
}

func sameCollectorFile(previous, current collectorSegment) bool {
	return os.SameFile(previous.info, current.info) &&
		previous.info.Size() == current.info.Size() && previous.info.Mode() == current.info.Mode() &&
		previous.info.ModTime().Equal(current.info.ModTime()) && previous.changed == current.changed
}

func (j *Journal) collectorFilesUnchanged(ctx context.Context, segments map[string]collectorSegment) bool {
	for name, previous := range segments {
		if ctx.Err() != nil {
			return false
		}
		current, ok := j.collectorSegment(name)
		if !ok || !sameCollectorFile(previous, current) {
			return false
		}
	}
	return ctx.Err() == nil
}

func (j *Journal) loadCollectorSnapshot(ctx context.Context, previous *collectorSnapshot) *collectorSnapshot {
	snapshot := &collectorSnapshot{version: j.entryVersion, segments: make(map[string]collectorSegment)}
	bySegment := make(map[string][]entry)
	for _, e := range j.entries {
		if ctx.Err() != nil {
			return nil
		}
		bySegment[e.Segment] = append(bySegment[e.Segment], e)
	}
	for name, entries := range bySegment {
		var prior *collectorSegment
		if collectorCacheSupported && previous != nil {
			if value, exists := previous.segments[name]; exists {
				prior = &value
			}
		}
		segment, ok := j.loadCollectorSegment(ctx, name, entries, prior)
		if !ok {
			return nil
		}
		snapshot.segments[name] = segment
		for _, record := range segment.records {
			if record.label >= 0 && (!snapshot.present[record.label] || record.last.After(snapshot.last[record.label])) {
				snapshot.present[record.label] = true
				snapshot.last[record.label] = record.last
			}
		}
	}
	// External mutation must not turn a partial read into a trusted snapshot.
	if !j.collectorFilesUnchanged(ctx, snapshot.segments) {
		return nil
	}
	return snapshot
}

func (j *Journal) loadCollectorSegment(ctx context.Context, name string, entries []entry, previous *collectorSegment) (collectorSegment, bool) {
	current, ok := j.collectorSegment(name)
	if !ok {
		return collectorSegment{}, false
	}
	for _, e := range entries {
		if e.Offset < 0 || e.Length < 1 || e.Length > 131072 || e.Offset > current.info.Size()-e.Length {
			return collectorSegment{}, false
		}
		if end := e.Offset + e.Length; end > current.prefixBytes {
			current.prefixBytes = end
		}
	}
	var file *os.File
	defer func() {
		if file != nil {
			file.Close()
		}
	}()
	open := func() bool {
		if file == nil {
			var err error
			file, err = os.Open(filepath.Join(j.options.Directory, name))
			return err == nil
		}
		return true
	}
	reuse := previous != nil && sameCollectorFile(*previous, current) && current.prefixBytes <= previous.prefixBytes
	if reuse {
		current.prefixBytes, current.digest = previous.prefixBytes, previous.digest
	} else {
		if !open() {
			return collectorSegment{}, false
		}
		var err error
		current.digest, reuse, err = collectorPrefixDigest(ctx, file, current.prefixBytes, previous)
		if err != nil {
			return collectorSegment{}, false
		}
	}
	current.records = make(map[int64]collectorRecord, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return collectorSegment{}, false
		}
		if reuse {
			if record, exists := previous.records[e.Sequence]; exists && record.entry == e {
				current.records[e.Sequence] = record
				continue
			}
		}
		if !open() {
			return collectorSegment{}, false
		}
		action, err := readEntryFile(file, e)
		if err != nil {
			return collectorSegment{}, false
		}
		record := collectorRecord{entry: e, label: -1}
		label, _ := action["agent"].(string)
		if len(label) == 3 && label[:2] == "SP" && label[2] >= '1' && label[2] <= '9' {
			start, okStart := action["occurred_at"].(string)
			finish, okFinish := action["finished_at"].(string)
			started, startErr := time.Parse(time.RFC3339Nano, start)
			finished, finishErr := time.Parse(time.RFC3339Nano, finish)
			if !okStart || !okFinish || startErr != nil || finishErr != nil || started.IsZero() || finished.Before(started) {
				return collectorSegment{}, false
			}
			record.label, record.last = int(label[2]-'1'), finished.UTC()
		}
		current.records[e.Sequence] = record
	}
	return current, true
}

// A normal append may reuse parsed facts only after the previous byte prefix
// still matches. Metadata alone cannot distinguish an append from corruption
// of an older retained record during the same write interval. Hashing streams
// bounded buffers; the cache retains facts and digests, never journal bodies.
func collectorPrefixDigest(ctx context.Context, file *os.File, length int64, previous *collectorSegment) ([sha256.Size]byte, bool, error) {
	var digest [sha256.Size]byte
	hash := sha256.New()
	reader := io.NewSectionReader(file, 0, length)
	var consumed int64
	reuse := false
	if previous != nil && previous.prefixBytes <= length {
		n, err := io.CopyN(hash, reader, previous.prefixBytes)
		if err != nil {
			return digest, false, err
		}
		consumed = n
		reuse = bytes.Equal(hash.Sum(nil), previous.digest[:])
	}
	if err := ctx.Err(); err != nil {
		return digest, false, err
	}
	if _, err := io.CopyN(hash, reader, length-consumed); err != nil {
		return digest, false, err
	}
	copy(digest[:], hash.Sum(nil))
	return digest, reuse, ctx.Err()
}
