package jobs

import (
	"encoding/json"
	"log/slog"
	"time"
)

// CompletionDedupKey identifies the job incarnation, not just its reusable
// compact public ID. Its receipt survives visible retention until the durable
// outbox acknowledges it and source-authorized maintenance releases it.
func CompletionDedupKey(r Record) string { return r.ID + ":" + r.StartedAt }

// SetCompletionSink installs a durable, idempotent audit receiver. Return nil
// only after either recording or finding a durable duplicate receipt. Errors
// retain the outbox entry for retry, including after a process restart.
func (s *Service) SetCompletionSink(fn func(Record) error) {
	s.observerMu.Lock()
	s.completionSink = fn
	s.observerMu.Unlock()
	select {
	case s.completionWake <- struct{}{}:
	default:
	}
}

func (s *Service) completionWorker() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var nextMaintenance time.Time
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.completionWake:
		case <-ticker.C:
		}
		s.observerMu.RLock()
		sink := s.completionSink
		maintenance := s.completionMaintenance
		s.observerMu.RUnlock()
		if maintenance != nil && !time.Now().Before(nextMaintenance) {
			if err := maintenance(s.ctx); err != nil && s.ctx.Err() == nil {
				slog.Warn("job completion receipt retention deferred", "error", err)
			}
			nextMaintenance = time.Now().Add(time.Minute)
		}
		if sink == nil {
			continue
		}
		rows, err := s.db.Query("SELECT payload FROM job_completions ORDER BY rowid LIMIT 32")
		if err != nil {
			continue
		}
		pending := []Record{}
		for rows.Next() {
			var payload []byte
			if err = rows.Scan(&payload); err != nil {
				break
			}
			var r Record
			if err = json.Unmarshal(payload, &r); err != nil {
				break
			}
			pending = append(pending, r)
		}
		if rowErr := rows.Err(); err == nil {
			err = rowErr
		}
		rows.Close()
		if err != nil {
			continue
		}
		for _, r := range pending {
			if s.ctx.Err() != nil {
				return
			}
			if err = sink(copyRecord(r)); err != nil {
				break
			}
			if _, err = s.db.Exec("DELETE FROM job_completions WHERE job_id=?", r.ID); err != nil {
				break
			}
			s.notify() // A durable acknowledgement can release queue admission.
		}
		if err == nil && len(pending) == 32 {
			select {
			case s.completionWake <- struct{}{}:
			default:
			}
		}
	}
}
