package files

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/lyeith/codexpro/go/internal/wire"
)

type SeenRange struct{ Start, End int }
type Snapshot struct {
	Tag, Text string
	Seen      []SeenRange
}
type SnapshotStore struct {
	mu        sync.Mutex
	histories map[string][]Snapshot
	order     []string
	bytes     int
}

func NewSnapshots() *SnapshotStore { return &SnapshotStore{histories: map[string][]Snapshot{}} }
func snapshotKey(scope wire.Scope, path string) string {
	p, e := filepath.EvalSymlinks(path)
	if e != nil {
		p, _ = filepath.Abs(path)
	}
	principal := scope.PrincipalID
	if principal == "" {
		principal = "local"
	}
	return principal + "\x00" + filepath.Clean(p)
}
func mergeRanges(r []SeenRange, add SeenRange) []SeenRange {
	r = append(append([]SeenRange(nil), r...), add)
	sort.Slice(r, func(i, j int) bool { return r[i].Start < r[j].Start || r[i].Start == r[j].Start && r[i].End < r[j].End })
	out := []SeenRange{}
	for _, v := range r {
		if len(out) == 0 || v.Start > out[len(out)-1].End+1 {
			out = append(out, v)
		} else {
			out[len(out)-1].End = max(out[len(out)-1].End, v.End)
		}
	}
	return out
}
func (s *SnapshotStore) put(key string, history []Snapshot) {
	for _, v := range s.histories[key] {
		s.bytes -= len(v.Text)
	}
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	delete(s.histories, key)
	if len(history) > 0 {
		s.histories[key] = history
		s.order = append(s.order, key)
		for _, v := range history {
			s.bytes += len(v.Text)
		}
	}
	for len(s.order) > 256 || s.bytes > 64*1024*1024 {
		k := s.order[0]
		s.order = s.order[1:]
		for _, v := range s.histories[k] {
			s.bytes -= len(v.Text)
		}
		delete(s.histories, k)
	}
}
func (s *SnapshotStore) Record(scope wire.Scope, path, text string, start, end int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := snapshotKey(scope, path)
	history := s.histories[key]
	value := Snapshot{Tag: Tag(text), Text: text, Seen: []SeenRange{}}
	for _, old := range history {
		if old.Text == text {
			value = old
			break
		}
	}
	if start > 0 && end >= start {
		value.Seen = mergeRanges(value.Seen, SeenRange{start, end})
	}
	next := []Snapshot{value}
	for _, old := range history {
		if old.Tag != value.Tag {
			next = append(next, old)
		}
	}
	if len(next) > 4 {
		next = next[:4]
	}
	s.put(key, next)
	return value.Tag
}

var validTag = regexp.MustCompile("^[0-9A-F]{4}$")

func failure(code, message string, recovery map[string]any) error {
	data := map[string]any{"retry_unchanged": false}
	if recovery != nil {
		data["recovery"] = recovery
	}
	return &wire.Error{Code: code, Message: message, Data: data}
}
func (s *SnapshotStore) Resolve(scope wire.Scope, path, tag, live, rel string) (Snapshot, error) {
	tag = strings.ToUpper(TrimJS(tag))
	recovery := func(message string) map[string]any {
		return map[string]any{"tool": "read", "message": message, "args": map[string]any{"path": rel}}
	}
	if !validTag.MatchString(tag) {
		return Snapshot{}, failure("edit_tag_invalid", "edit_tag must be exactly four hexadecimal characters from the latest read result.", recovery("Read "+rel+" again and use its returned edit_tag."))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := snapshotKey(scope, path)
	history := s.histories[key]
	found := -1
	for i, v := range history {
		if v.Tag == tag {
			found = i
			break
		}
	}
	if found < 0 {
		return Snapshot{}, failure("edit_tag_unknown", fmt.Sprintf("Edit tag %s for %s is not retained for this connector principal.", tag, rel), recovery("Read "+rel+" again before editing; do not resend this edit unchanged."))
	}
	v := history[found]
	if v.Text != live {
		return Snapshot{}, failure("edit_tag_stale", fmt.Sprintf("File changed since edit tag %s was read: %s.", tag, rel), recovery("Read "+rel+" again, rebuild the edit against the new line numbers, and use the new edit_tag."))
	}
	next := []Snapshot{v}
	for i, old := range history {
		if i != found {
			next = append(next, old)
		}
	}
	s.put(key, next)
	v.Seen = append([]SeenRange(nil), v.Seen...)
	return v, nil
}
func (s Snapshot) AssertSeen(start, end, operation int, rel string) error {
	for _, r := range s.Seen {
		if start >= r.Start && end <= r.End {
			return nil
		}
	}
	target := fmt.Sprintf("lines %d-%d", start, end)
	if start == end {
		target = fmt.Sprintf("line %d", start)
	}
	return failure("edit_range_unseen", fmt.Sprintf("edits[%d] targets %s in %s, but that range was not displayed for edit tag %s.", operation, target, rel, s.Tag), map[string]any{
		"tool": "read", "message": fmt.Sprintf("Read %s from %s, then resend the edit with the returned edit_tag.", target, rel),
		"args": map[string]any{"path": rel, "start_line": start, "end_line": end},
	})
}
