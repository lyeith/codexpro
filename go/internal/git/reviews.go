package git

import (
	"container/list"
	"sync"
)

// maxReviewCheckpoints bounds the process-wide show_changes(since="last_shown")
// store. Each entry is one fingerprint per principal, workspace, path and stage.
const maxReviewCheckpoints = 4096

// reviewCheckpoints remembers the last diff fingerprint shown to a principal.
// It is shared by every MCP request in the process, so continuity does not
// depend on an MCP session; the least recently used entry is evicted at the cap.
type reviewCheckpoints struct {
	mu      sync.Mutex
	limit   int
	order   *list.List
	entries map[string]*list.Element
}

type reviewEntry struct{ key, fingerprint string }

func newReviewCheckpoints(limit int) *reviewCheckpoints {
	return &reviewCheckpoints{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

func reviewKey(principal, workspace, path, stage string) string {
	return principal + "\x00" + workspace + "\x00" + path + "\x00" + stage
}

// observe reports whether fingerprint matches the stored checkpoint and, when
// mark is set, records it as the latest one shown.
func (r *reviewCheckpoints) observe(key, fingerprint string, mark bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	element := r.entries[key]
	hit := element != nil && element.Value.(*reviewEntry).fingerprint == fingerprint
	if !mark {
		return hit
	}
	if element != nil {
		element.Value.(*reviewEntry).fingerprint = fingerprint
		r.order.MoveToFront(element)
		return hit
	}
	r.entries[key] = r.order.PushFront(&reviewEntry{key: key, fingerprint: fingerprint})
	for r.order.Len() > r.limit {
		oldest := r.order.Back()
		r.order.Remove(oldest)
		delete(r.entries, oldest.Value.(*reviewEntry).key)
	}
	return hit
}

func (r *reviewCheckpoints) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.order.Len()
}
