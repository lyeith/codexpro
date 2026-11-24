package audit

import (
	"context"
	"encoding/json"
	"errors"
)

// PruneAcknowledgedCompletions releases private job receipts only when their
// durable producer proves that they cannot be replayed. The producer must return
// its COMPLETE pending outbox, and must never recreate an acknowledged outbox
// incarnation. A read failure must return an error, never a partial snapshot.
//
// The journal write lock precedes the source snapshot and remains held through
// deletion. A newly queued completion cannot acquire an audit receipt between
// that snapshot and deletion. Publicly retained actions and legacy request
// fingerprints keep their existing deduplication behavior.
func (j *Journal) PruneAcknowledgedCompletions(ctx context.Context, pending func(context.Context) ([]string, error)) (int64, error) {
	if pending == nil {
		return 0, errors.New("completion retention requires the durable producer")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	j.admitMu.RLock()
	defer j.admitMu.RUnlock()
	if j.closed {
		return 0, errors.New("audit journal is closed")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.db == nil {
		return 0, nil
	}
	if j.fatal != nil {
		return 0, j.fatal
	}
	keys, err := pending(ctx)
	if err != nil {
		return 0, err
	}
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			return 0, errors.New("completion retention source returned an empty incarnation")
		}
		fingerprints = append(fingerprints, "completion:"+completionActionID(key))
	}
	body, err := json.Marshal(fingerprints)
	if err != nil {
		return 0, err
	}
	const eligible = `fingerprint LIKE 'completion:%'
 AND action_id NOT IN (SELECT action_id FROM entries)
 AND fingerprint NOT IN (SELECT value FROM json_each(?))`
	// Do not acquire a SQLite write transaction on an unchanged idle pass.
	var found bool
	if err = j.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM completion_receipts WHERE "+eligible+")", string(body)).Scan(&found); err != nil || !found {
		return 0, err
	}
	result, err := j.db.ExecContext(ctx, "DELETE FROM completion_receipts WHERE "+eligible, string(body))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
