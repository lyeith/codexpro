// Package oplog bounds repetitive operational diagnostics.
package oplog

import (
	"sync"
	"time"
)

// Repeated admits the first occurrence and at most one per minute thereafter.
// Use a separate instance for each diagnostic.
type Repeated struct {
	mu   sync.Mutex
	next time.Time
}

func (r *Repeated) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Before(r.next) {
		return false
	}
	r.next = now.Add(time.Minute)
	return true
}
