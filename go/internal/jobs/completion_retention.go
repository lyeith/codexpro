package jobs

import (
	"context"
	"encoding/json"
	"errors"
)

// PendingCompletionKeys is the complete durable replay set for this producer.
// Completion outboxes are created only on the unfinished-to-terminal transition;
// acknowledging one never permits a later terminal metadata update to recreate it.
// Callers must not hold a jobs transaction while invoking an audit operation.
func (s *Service) PendingCompletionKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT job_id, payload FROM job_completions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var jobID string
		var body []byte
		if err := rows.Scan(&jobID, &body); err != nil {
			return nil, err
		}
		var record Record
		if err := json.Unmarshal(body, &record); err != nil {
			return nil, err
		}
		if record.ID == "" || record.ID != jobID || record.StartedAt == "" || unfinished(record) {
			return nil, errors.New("pending completion lacks its durable terminal incarnation")
		}
		keys = append(keys, CompletionDedupKey(record))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// SetCompletionMaintenance binds the producer to optional receipt maintenance.
// It runs from the completion worker with no jobs lock, rows or transaction held.
// Failed maintenance is retried on the next minute; completion delivery proceeds.
func (s *Service) SetCompletionMaintenance(fn func(context.Context) error) {
	s.observerMu.Lock()
	s.completionMaintenance = fn
	s.observerMu.Unlock()
	select {
	case s.completionWake <- struct{}{}:
	default:
	}
}
